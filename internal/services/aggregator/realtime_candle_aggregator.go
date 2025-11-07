package aggregator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"nyyu-market/internal/metrics"
	"nyyu-market/internal/models"
	"nyyu-market/internal/pubsub"

	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

// CandleSubscriber is a callback interface for real-time candle updates
type CandleSubscriber func(candle *models.Candle)

// PriceSubscriber is a callback interface for real-time price updates
type PriceSubscriber func(symbol string, price decimal.Decimal, exchangeCount int)

// CandleServiceInterface for processing candles
type CandleServiceInterface interface {
	ProcessCandleForStats(candle *models.Candle)
}

// IntervalAggregatorInterface for building higher timeframes
type IntervalAggregatorInterface interface {
	Process1mCandle(candle *models.Candle)
	GetLastCompletedCandle(symbol, interval string) *models.Candle
	GetLatestCandle(symbol, interval string) *models.Candle
}

// RepositoryInterface for querying historical candles
type RepositoryInterface interface {
	GetCandles(ctx context.Context, symbol, interval, exchange, contractType string, startTime, endTime time.Time, limit int) ([]models.Candle, error)
}

// RealtimeCandleAggregator is a lock-free, zero-delay candle aggregation system
// Key improvements:
// 1. Streaming aggregation - no batching or waiting
// 2. Lock-free concurrent maps for O(1) access
// 3. Immediate publishing on every update
// 4. Separate hot/cold data paths
// 5. ⚡ NEW: Direct subscriber callbacks (bypass Redis for ultra-low latency)
type RealtimeCandleAggregator struct {
	pubsub *pubsub.Publisher
	logger *logrus.Logger
	repo   RepositoryInterface // ⚡ NEW: For querying previous candles from database

	// ⚡ LOCK-FREE: Live candles being built in real-time
	// Key: "symbol:interval:timestamp" -> *LiveCandle
	liveCandles sync.Map

	// ⚡ LOCK-FREE: Latest completed candle per symbol+interval
	// Key: "symbol:interval" -> *models.Candle
	latestCandles sync.Map

	// ⚡ LOCK-FREE: Current aggregated prices
	// Key: "symbol" -> *AggregatedPrice
	livePrices sync.Map

	// ⚡ PRIORITY 4: Direct streaming subscribers (no Redis overhead)
	// Key: "symbol:interval" -> []CandleSubscriber
	candleSubscribers sync.Map
	// Key: "symbol" -> []PriceSubscriber
	priceSubscribers sync.Map

	// Batch writer channel for closed candles
	candleBatchChan chan *models.Candle

	// ⚡ Reference to candle service for 24h stats processing
	candleService CandleServiceInterface

	// ⚡ Reference to interval aggregator for building higher timeframes
	intervalAgg IntervalAggregatorInterface

	// Statistics
	updateCount    int64
	publishCount   int64
	lastStatsTime  time.Time
	statsMu        sync.Mutex
}

// LiveCandle represents a candle being built in real-time from multiple exchanges
type LiveCandle struct {
	Symbol       string
	Interval     string
	OpenTime     time.Time
	CloseTime    time.Time

	// Per-exchange data (lock-free)
	exchangeData sync.Map // exchange name -> *ExchangeCandleData

	// Aggregated values (atomic updates)
	aggregated   *models.Candle
	aggregatedMu sync.RWMutex

	// Metadata
	lastUpdate   time.Time
	isClosed     bool
}

// ExchangeCandleData represents data from a single exchange
type ExchangeCandleData struct {
	Open         decimal.Decimal
	High         decimal.Decimal
	Low          decimal.Decimal
	Close        decimal.Decimal
	Volume       decimal.Decimal
	QuoteVolume  decimal.Decimal
	TradeCount   int
	IsClosed     bool
	UpdatedAt    time.Time
}

// AggregatedPrice represents live price from multiple exchanges
type AggregatedPrice struct {
	Symbol        string
	LastPrice     decimal.Decimal
	ExchangeCount int
	UpdatedAt     time.Time

	// Per-exchange prices (for debugging)
	exchangePrices sync.Map // exchange -> decimal.Decimal
}

// NewRealtimeCandleAggregator creates a new real-time aggregator
func NewRealtimeCandleAggregator(pubsub *pubsub.Publisher, logger *logrus.Logger, candleBatchChan chan *models.Candle, repo RepositoryInterface) *RealtimeCandleAggregator {
	return &RealtimeCandleAggregator{
		pubsub:          pubsub,
		logger:          logger,
		repo:            repo,
		candleBatchChan: candleBatchChan,
		lastStatsTime:   time.Now(),
	}
}

// SetCandleService sets the candle service for 24h stats processing
func (a *RealtimeCandleAggregator) SetCandleService(svc CandleServiceInterface) {
	a.candleService = svc
}

// SetIntervalAggregator sets the interval aggregator for building higher timeframes
func (a *RealtimeCandleAggregator) SetIntervalAggregator(agg IntervalAggregatorInterface) {
	a.intervalAgg = agg
}

// ProcessCandleUpdate processes a candle update from an exchange in real-time
// ⚡ ZERO DELAY: Publishes immediately on every update
func (a *RealtimeCandleAggregator) ProcessCandleUpdate(exchange string, candle *models.Candle) {
	// ⚡ METRICS: Track processing start time
	start := time.Now()
	defer func() {
		metrics.TrackLatency(start, metrics.RealtimeCandleLatency.WithLabelValues(exchange))
	}()

	// ⚡ METRICS: Track candle update
	metrics.TrackCandleUpdate(exchange, candle.Symbol, candle.Interval)

	// Create unique key for this candle's time window
	candleKey := fmt.Sprintf("%s:%s:%d", candle.Symbol, candle.Interval, candle.OpenTime.Unix())

	// Get or create live candle
	liveInterface, _ := a.liveCandles.LoadOrStore(candleKey, &LiveCandle{
		Symbol:    candle.Symbol,
		Interval:  candle.Interval,
		OpenTime:  candle.OpenTime,
		CloseTime: candle.CloseTime,
	})
	live := liveInterface.(*LiveCandle)

	// Update exchange-specific data (lock-free)
	live.exchangeData.Store(exchange, &ExchangeCandleData{
		Open:        candle.Open,
		High:        candle.High,
		Low:         candle.Low,
		Close:       candle.Close,
		Volume:      candle.Volume,
		QuoteVolume: candle.QuoteVolume,
		TradeCount:  candle.TradeCount,
		IsClosed:    candle.IsClosed,
		UpdatedAt:   time.Now(),
	})

	// ⚡ IMMEDIATE AGGREGATION: Recalculate aggregate instantly
	aggregated := a.aggregateLiveCandle(live)

	// Update the live candle's aggregated data
	live.aggregatedMu.Lock()
	live.aggregated = aggregated
	live.lastUpdate = time.Now()
	live.isClosed = candle.IsClosed
	live.aggregatedMu.Unlock()

	// ⚡ IMMEDIATE PUBLISH: No waiting, no batching
	go a.publishCandleUpdate(candle.Symbol, candle.Interval, aggregated)

	// ⚡ IMMEDIATE PRICE UPDATE: Update live price instantly
	a.updateLivePrice(candle.Symbol, exchange, candle.Close)

	// If candle is closed, persist it
	if candle.IsClosed {
		a.handleClosedCandle(live, aggregated)
	}
}

// aggregateLiveCandle combines all exchange data into one aggregated candle
// ⚡ OPTIMIZED: O(n) where n = number of exchanges (typically 1-6)
func (a *RealtimeCandleAggregator) aggregateLiveCandle(live *LiveCandle) *models.Candle {
	var totalOpen, totalClose, totalVolume, totalQuoteVolume decimal.Decimal
	var maxHigh, minLow decimal.Decimal
	var totalTradeCount int
	validCount := 0
	firstExchange := true
	anyClosed := false

	// Iterate through all exchange data (lock-free read)
	live.exchangeData.Range(func(key, value interface{}) bool {
		data := value.(*ExchangeCandleData)

		// Skip invalid data
		if data.Open.IsZero() && data.Close.IsZero() {
			return true // continue
		}

		validCount++

		// Aggregate high/low
		if firstExchange {
			maxHigh = data.High
			minLow = data.Low
			firstExchange = false
		} else {
			if data.High.GreaterThan(maxHigh) {
				maxHigh = data.High
			}
			if data.Low.LessThan(minLow) {
				minLow = data.Low
			}
		}

		// Sum for averages
		totalOpen = totalOpen.Add(data.Open)
		totalClose = totalClose.Add(data.Close)
		totalVolume = totalVolume.Add(data.Volume)
		totalQuoteVolume = totalQuoteVolume.Add(data.QuoteVolume)
		totalTradeCount += data.TradeCount

		if data.IsClosed {
			anyClosed = true
		}

		return true // continue
	})

	if validCount == 0 {
		return nil
	}

	// Calculate averages
	validCountDecimal := decimal.NewFromInt(int64(validCount))
	avgOpen := totalOpen.Div(validCountDecimal)
	avgClose := totalClose.Div(validCountDecimal)

	// Return aggregated candle
	return &models.Candle{
		Symbol:       live.Symbol,
		Interval:     live.Interval,
		OpenTime:     live.OpenTime,
		CloseTime:    live.CloseTime,
		Open:         avgOpen,
		High:         maxHigh,
		Low:          minLow,
		Close:        avgClose,
		Volume:       totalVolume,
		QuoteVolume:  totalQuoteVolume,
		TradeCount:   validCount,
		Source:       "aggregated",
		IsClosed:     anyClosed,
		ContractType: "spot",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
}

// publishCandleUpdate publishes candle update to Redis AND direct subscribers (non-blocking)
func (a *RealtimeCandleAggregator) publishCandleUpdate(symbol, interval string, candle *models.Candle) {
	if candle == nil {
		return
	}

	// ⚡ Process candle for 24h stats calculation (non-blocking)
	if a.candleService != nil && interval == "1m" {
		go a.candleService.ProcessCandleForStats(candle)
	}

	// ⚡ Process 1m candle for interval aggregation (build higher timeframes)
	if a.intervalAgg != nil && interval == "1m" {
		go a.intervalAgg.Process1mCandle(candle)
	}

	// ⚡ PRIORITY 4: Call direct subscribers FIRST (ultra-low latency, no Redis hop)
	key := fmt.Sprintf("%s:%s", symbol, interval)
	if subs, ok := a.candleSubscribers.Load(key); ok {
		subscribers := subs.([]CandleSubscriber)
		for _, sub := range subscribers {
			// Call subscriber in goroutine to avoid blocking
			go sub(candle)
		}
	}

	// Then publish to Redis for other consumers
	start := time.Now()
	channel := fmt.Sprintf("nyyu:market:candle:%s:%s", symbol, interval)
	if err := a.pubsub.PublishCandle(context.Background(), channel, candle); err != nil {
		a.logger.WithError(err).Debugf("Failed to publish candle for %s %s", symbol, interval)
		metrics.PublishFailures.WithLabelValues("candle").Inc()
	} else {
		metrics.PublishSuccess.WithLabelValues("candle").Inc()
		metrics.TrackLatency(start, metrics.PublishLatency.WithLabelValues("candle"))
	}
}

// updateLivePrice updates the live aggregated price for a symbol
func (a *RealtimeCandleAggregator) updateLivePrice(symbol, exchange string, price decimal.Decimal) {
	// Get or create aggregated price
	priceInterface, _ := a.livePrices.LoadOrStore(symbol, &AggregatedPrice{
		Symbol:    symbol,
		UpdatedAt: time.Now(),
	})
	aggPrice := priceInterface.(*AggregatedPrice)

	// Update exchange price (lock-free)
	aggPrice.exchangePrices.Store(exchange, price)

	// Recalculate aggregate
	var sum decimal.Decimal
	count := 0
	aggPrice.exchangePrices.Range(func(key, value interface{}) bool {
		p := value.(decimal.Decimal)
		sum = sum.Add(p)
		count++
		return true
	})

	if count > 0 {
		avgPrice := sum.Div(decimal.NewFromInt(int64(count)))
		aggPrice.LastPrice = avgPrice
		aggPrice.ExchangeCount = count
		aggPrice.UpdatedAt = time.Now()

		// ⚡ IMMEDIATE PUBLISH: Publish price update
		go a.publishPriceUpdate(symbol, avgPrice, count)
	}
}

// publishPriceUpdate publishes price update to Redis AND direct subscribers
func (a *RealtimeCandleAggregator) publishPriceUpdate(symbol string, price decimal.Decimal, exchangeCount int) {
	// ⚡ PRIORITY 4: Call direct subscribers FIRST (ultra-low latency)
	if subs, ok := a.priceSubscribers.Load(symbol); ok {
		subscribers := subs.([]PriceSubscriber)
		for _, sub := range subscribers {
			// Call subscriber in goroutine to avoid blocking
			go sub(symbol, price, exchangeCount)
		}
	}

	// Then publish to Redis
	priceObj := &models.Price{
		Symbol:    symbol,
		LastPrice: price,
		UpdatedAt: time.Now(),
	}

	start := time.Now()
	channel := fmt.Sprintf("nyyu:market:price:%s", symbol)
	if err := a.pubsub.PublishPrice(context.Background(), channel, priceObj); err != nil {
		a.logger.WithError(err).Debugf("Failed to publish price for %s", symbol)
		metrics.PublishFailures.WithLabelValues("price").Inc()
	} else {
		a.logger.Debugf("📊 Published real-time price for %s: %s (from %d exchanges)", symbol, price.String(), exchangeCount)
		metrics.TrackPriceUpdate(symbol)
		metrics.PublishSuccess.WithLabelValues("price").Inc()
		metrics.TrackLatency(start, metrics.PublishLatency.WithLabelValues("price"))
	}
}

// handleClosedCandle handles a closed candle (persists to DB)
func (a *RealtimeCandleAggregator) handleClosedCandle(live *LiveCandle, aggregated *models.Candle) {
	if aggregated == nil {
		return
	}

	// Store as latest candle for this symbol+interval
	latestKey := fmt.Sprintf("%s:%s", live.Symbol, live.Interval)
	a.latestCandles.Store(latestKey, aggregated)

	// ⚡ Send closed candle to batch writer for DB persistence
	if a.candleBatchChan != nil {
		select {
		case a.candleBatchChan <- aggregated:
			a.logger.Debugf("✅ Closed candle queued for DB: %s %s at %v", live.Symbol, live.Interval, live.OpenTime)
		default:
			a.logger.Warn("⚠️ Candle batch channel full, dropping closed candle")
		}
	}

	// Remove from live candles after a short grace period
	// This gives clients time to receive the final update
	candleKey := fmt.Sprintf("%s:%s:%d", live.Symbol, live.Interval, live.OpenTime.Unix())
	go func() {
		time.Sleep(200 * time.Millisecond) // Minimal grace period
		a.liveCandles.Delete(candleKey)
	}()
}

// GetLatestCandle retrieves the latest aggregated candle for a symbol+interval
// ⚡ O(1) lookup, no locks
// ⚡ FIX: Falls back to database query if not found in memory
func (a *RealtimeCandleAggregator) GetLatestCandle(symbol, interval string) *models.Candle {
	// First check live candles (most recent)
	var latestTimestamp int64
	var latestCandle *models.Candle

	// Scan live candles for this symbol+interval
	prefix := fmt.Sprintf("%s:%s:", symbol, interval)
	a.liveCandles.Range(func(key, value interface{}) bool {
		keyStr := key.(string)
		if len(keyStr) > len(prefix) && keyStr[:len(prefix)] == prefix {
			live := value.(*LiveCandle)
			if live.OpenTime.Unix() > latestTimestamp {
				live.aggregatedMu.RLock()
				latestCandle = live.aggregated
				live.aggregatedMu.RUnlock()
				latestTimestamp = live.OpenTime.Unix()
			}
		}
		return true
	})

	if latestCandle != nil {
		return latestCandle
	}

	// Fallback to latest closed candle in memory
	latestKey := fmt.Sprintf("%s:%s", symbol, interval)
	if candleInterface, ok := a.latestCandles.Load(latestKey); ok {
		return candleInterface.(*models.Candle)
	}

	// ⚡ FIX: If not in memory, query database for the most recent candle
	// This handles cases where system just started and memory cache is empty
	if a.repo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		now := time.Now()
		startTime := now.Add(-30 * 24 * time.Hour) // Look back 30 days

		candles, err := a.repo.GetCandles(ctx, symbol, interval, "", "spot", startTime, now, 1)
		if err != nil {
			a.logger.WithError(err).Debugf("Failed to query last candle for %s %s", symbol, interval)
			return nil
		}

		if len(candles) > 0 {
			lastCandle := &candles[0]
			// Store in cache for future access
			a.latestCandles.Store(latestKey, lastCandle)
			a.logger.Debugf("✅ Loaded last candle from database: %s %s at %v", symbol, interval, lastCandle.OpenTime)
			return lastCandle
		}
	}

	return nil
}

// GetAggregatedPrice retrieves the current aggregated price for a symbol
// ⚡ O(1) lookup, no locks
func (a *RealtimeCandleAggregator) GetAggregatedPrice(symbol string) (decimal.Decimal, int) {
	if priceInterface, ok := a.livePrices.Load(symbol); ok {
		aggPrice := priceInterface.(*AggregatedPrice)
		return aggPrice.LastPrice, aggPrice.ExchangeCount
	}
	return decimal.Zero, 0
}

// ⚡ PRIORITY 4: Subscribe to direct candle updates (bypass Redis)
func (a *RealtimeCandleAggregator) SubscribeCandle(symbol, interval string, callback CandleSubscriber) {
	key := fmt.Sprintf("%s:%s", symbol, interval)

	// Get existing subscribers or create new list
	var subscribers []CandleSubscriber
	if subs, ok := a.candleSubscribers.Load(key); ok {
		subscribers = subs.([]CandleSubscriber)
	}

	// Add new subscriber
	subscribers = append(subscribers, callback)
	a.candleSubscribers.Store(key, subscribers)

	a.logger.Debugf("➕ Added direct candle subscriber for %s %s", symbol, interval)
}

// ⚡ PRIORITY 4: Subscribe to direct price updates (bypass Redis)
func (a *RealtimeCandleAggregator) SubscribePrice(symbol string, callback PriceSubscriber) {
	// Get existing subscribers or create new list
	var subscribers []PriceSubscriber
	if subs, ok := a.priceSubscribers.Load(symbol); ok {
		subscribers = subs.([]PriceSubscriber)
	}

	// Add new subscriber
	subscribers = append(subscribers, callback)
	a.priceSubscribers.Store(symbol, subscribers)

	a.logger.Debugf("➕ Added direct price subscriber for %s", symbol)
}

// Stop gracefully stops the real-time aggregator and flushes pending data
func (a *RealtimeCandleAggregator) Stop(ctx context.Context) error {
	a.logger.Info("🛑 Stopping real-time candle aggregator...")

	// Flush all pending live candles
	done := make(chan struct{})
	go func() {
		flushedCount := 0
		a.liveCandles.Range(func(key, value interface{}) bool {
			live := value.(*LiveCandle)

			// Aggregate and publish one final time
			if aggregated := a.aggregateLiveCandle(live); aggregated != nil {
				a.publishCandleUpdate(live.Symbol, live.Interval, aggregated)
				flushedCount++
			}
			return true
		})

		if flushedCount > 0 {
			a.logger.Infof("✅ Flushed %d pending candles", flushedCount)
		}
		close(done)
	}()

	// Wait for flush or timeout
	select {
	case <-done:
		a.logger.Info("✅ Real-time aggregator stopped gracefully")
		return nil
	case <-ctx.Done():
		a.logger.Warn("⏱️ Real-time aggregator shutdown timed out")
		return ctx.Err()
	}
}
