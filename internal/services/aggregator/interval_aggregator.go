package aggregator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"nyyu-market/internal/models"
	"nyyu-market/internal/pubsub"
	"nyyu-market/internal/repository"

	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

// IntervalAggregator builds higher timeframe candles from 1m candles
// This is the missing piece that creates 3m, 5m, 15m, 1h, 4h, 6h, 8h, 12h, 1d candles
type IntervalAggregator struct {
	repo   *repository.CandleRepository
	pubsub *pubsub.Publisher
	logger *logrus.Logger

	// Track building candles for each interval
	// Key: "symbol:interval:opentime" -> *BuildingCandle
	buildingCandles sync.Map

	// ⚡ FIX: Store the last completed candle for each symbol:interval
	// This allows access to previous candle data when building the next one
	// Key: "symbol:interval" -> *models.Candle
	lastCompletedCandles sync.Map

	// Batch channel for persisting aggregated candles
	batchChan chan *models.Candle

	// Control
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Intervals to aggregate (excluding 1m which comes from exchanges)
	intervals []string
}

// BuildingCandle represents a candle being built from multiple 1m candles
type BuildingCandle struct {
	Symbol    string
	Interval  string
	OpenTime  time.Time
	CloseTime time.Time

	// Store full 1m candle data for real-time recalculation
	// Key: 1m candle open time (unix) -> full candle data
	// This allows us to update volumes in real-time as 1m candles update
	candles1m       map[int64]*models.Candle
	expected1mCount int // how many 1m candles we need
	mu              sync.Mutex
}

// NewIntervalAggregator creates a new interval aggregator
func NewIntervalAggregator(repo *repository.CandleRepository, pubsub *pubsub.Publisher, logger *logrus.Logger) *IntervalAggregator {
	ctx, cancel := context.WithCancel(context.Background())

	return &IntervalAggregator{
		repo:      repo,
		pubsub:    pubsub,
		logger:    logger,
		batchChan: make(chan *models.Candle, 10000),
		ctx:       ctx,
		cancel:    cancel,
		// Intervals to aggregate from 1m candles
		intervals: []string{"3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "8h", "12h", "1d", "3d", "1w", "1M"},
	}
}

// Start begins the interval aggregation process
func (a *IntervalAggregator) Start() {
	a.logger.Info("🚀 Starting interval aggregator for higher timeframes...")

	// Start batch writer
	a.wg.Add(1)
	go a.batchWriter()

	// Start periodic aggregation from historical 1m candles
	a.wg.Add(1)
	go a.periodicAggregation()

	a.logger.Info("✅ Interval aggregator started - will build 3m, 5m, 15m, 30m, 1h, 2h, 4h, 6h, 8h, 12h, 1d, 3d, 1w, 1M candles")
}

// Stop gracefully stops the aggregator
func (a *IntervalAggregator) Stop() {
	a.logger.Info("🛑 Stopping interval aggregator...")
	a.cancel()
	close(a.batchChan)
	a.wg.Wait()
	a.logger.Info("✅ Interval aggregator stopped")
}

// GetLastCompletedCandle retrieves the last completed candle for a symbol:interval
// This is useful for accessing previous candle data
// ⚡ FIX: Falls back to database query if not found in memory
func (a *IntervalAggregator) GetLastCompletedCandle(symbol, interval string) *models.Candle {
	key := fmt.Sprintf("%s:%s", symbol, interval)

	// First check in-memory cache (fastest)
	if value, ok := a.lastCompletedCandles.Load(key); ok {
		candle := value.(*models.Candle)
		a.logger.Debugf("✅ GetLastCompletedCandle %s %s: Found in MEMORY at %v (close: %s)",
			symbol, interval, candle.OpenTime, candle.Close.String())
		return candle
	}

	a.logger.Debugf("⚠️  GetLastCompletedCandle %s %s: Not in memory, querying database...", symbol, interval)

	// ⚡ FIX: If not in memory, query database for the most recent completed candle
	// This handles cases where:
	// 1. System just started and memory cache is empty
	// 2. First candle period is building and user requests previous candle
	// 3. Historical aggregation hasn't run yet
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Query for the most recent candle before now
	now := time.Now()
	startTime := now.Add(-30 * 24 * time.Hour) // Look back 30 days

	// ⚡ FIX: Query for "aggregated" source specifically
	// The IntervalAggregator only creates candles with source="aggregated"
	// Without this, it might return candles from other sources (binance, kraken, etc.)
	candles, err := a.repo.GetCandles(ctx, symbol, interval, "aggregated", "spot", startTime, now, 1)
	if err != nil {
		a.logger.WithError(err).Debugf("Failed to query last completed candle for %s %s", symbol, interval)
		return nil
	}

	if len(candles) > 0 {
		lastCandle := &candles[0]
		// Store in cache for future access
		a.lastCompletedCandles.Store(key, lastCandle)
		a.logger.Infof("✅ Loaded last completed candle from DATABASE: %s %s at %v (close: %s)",
			symbol, interval, lastCandle.OpenTime, lastCandle.Close.String())
		return lastCandle
	}

	a.logger.Warnf("❌ No completed candle found for %s %s (not in memory or database)", symbol, interval)
	return nil
}

// GetLatestCandle retrieves the most recent candle (building or completed) for a symbol:interval
// ⚡ FIX: This now returns BOTH the current building candle AND previous completed candle
// Priority:
// 1. If building candle exists with data -> return it (most recent)
// 2. If building candle is empty OR doesn't exist -> return last completed (previous period)
func (a *IntervalAggregator) GetLatestCandle(symbol, interval string) *models.Candle {
	// First check if there's a currently building candle (most recent)
	var latestTimestamp int64
	var buildingCandle *models.Candle

	prefix := fmt.Sprintf("%s:%s:", symbol, interval)
	a.buildingCandles.Range(func(key, value interface{}) bool {
		keyStr := key.(string)
		if len(keyStr) > len(prefix) && keyStr[:len(prefix)] == prefix {
			building := value.(*BuildingCandle)
			if building.OpenTime.Unix() > latestTimestamp {
				building.mu.Lock()
				// Recalculate the current state ONLY if it has data
				if len(building.candles1m) > 0 {
					buildingCandle = a.buildCurrentCandle(building)
					latestTimestamp = building.OpenTime.Unix()
				}
				building.mu.Unlock()
			}
		}
		return true
	})

	// Get last completed candle (previous period)
	lastCompleted := a.GetLastCompletedCandle(symbol, interval)

	// ⚡ FIX: Return the most appropriate candle:
	// - If we have a building candle with data, use it (current period)
	// - Otherwise, return the last completed candle (previous period)
	// This ensures the chart always shows the most recent candle with actual data
	if buildingCandle != nil {
		// Building candle has data, return it
		a.logger.Debugf("🔄 GetLatestCandle %s %s: Returning BUILDING candle at %v (close: %s)",
			symbol, interval, buildingCandle.OpenTime, buildingCandle.Close.String())
		return buildingCandle
	}

	// No building candle or it's empty, return last completed
	if lastCompleted != nil {
		a.logger.Debugf("📊 GetLatestCandle %s %s: Returning COMPLETED candle at %v (close: %s)",
			symbol, interval, lastCompleted.OpenTime, lastCompleted.Close.String())
	} else {
		a.logger.Debugf("❌ GetLatestCandle %s %s: No candle found (building or completed)", symbol, interval)
	}
	return lastCompleted
}

// buildCurrentCandle rebuilds a candle from its current 1m candles (must be called with lock held)
func (a *IntervalAggregator) buildCurrentCandle(building *BuildingCandle) *models.Candle {
	var (
		open        decimal.Decimal
		high        decimal.Decimal
		low         decimal.Decimal
		close       decimal.Decimal
		volume      decimal.Decimal
		quoteVolume decimal.Decimal
		tradeCount  int
		firstCandle = true
	)

	// Sort candle times
	var candleTimes []int64
	for t := range building.candles1m {
		candleTimes = append(candleTimes, t)
	}

	// Simple insertion sort
	for i := 1; i < len(candleTimes); i++ {
		key := candleTimes[i]
		j := i - 1
		for j >= 0 && candleTimes[j] > key {
			candleTimes[j+1] = candleTimes[j]
			j--
		}
		candleTimes[j+1] = key
	}

	// Aggregate all 1m candles
	for _, t := range candleTimes {
		candle := building.candles1m[t]
		if firstCandle {
			open = candle.Open
			high = candle.High
			low = candle.Low
			close = candle.Close
			firstCandle = false
		} else {
			if candle.High.GreaterThan(high) {
				high = candle.High
			}
			if candle.Low.LessThan(low) || low.IsZero() {
				low = candle.Low
			}
			close = candle.Close
		}
		volume = volume.Add(candle.Volume)
		quoteVolume = quoteVolume.Add(candle.QuoteVolume)
		tradeCount += candle.TradeCount
	}

	return &models.Candle{
		Symbol:       building.Symbol,
		Interval:     building.Interval,
		OpenTime:     building.OpenTime,
		CloseTime:    building.CloseTime,
		Open:         open,
		High:         high,
		Low:          low,
		Close:        close,
		Volume:       volume,
		QuoteVolume:  quoteVolume,
		TradeCount:   tradeCount,
		Source:       "aggregated",
		IsClosed:     false,
		ContractType: "spot",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
}

// Process1mCandle processes a new 1m candle and updates all higher timeframes
func (a *IntervalAggregator) Process1mCandle(candle *models.Candle) {
	// ⚡ FIX: Before updating, close any interval candles whose time has passed
	// When we receive a 1m candle for time T, any interval ending at or before T is complete
	a.closeExpiredIntervals(candle.Symbol, candle.OpenTime)

	// For each interval, check if this 1m candle belongs to it and update
	for _, interval := range a.intervals {
		a.updateIntervalCandle(candle, interval)
	}
}

// closeExpiredIntervals closes any interval candles whose period has ended
func (a *IntervalAggregator) closeExpiredIntervals(symbol string, currentTime time.Time) {
	for _, interval := range a.intervals {
		prefix := fmt.Sprintf("%s:%s:", symbol, interval)

		a.buildingCandles.Range(func(key, value interface{}) bool {
			keyStr := key.(string)
			if len(keyStr) > len(prefix) && keyStr[:len(prefix)] == prefix {
				building := value.(*BuildingCandle)

				// If current 1m candle time >= building candle's close time, the period is over
				if currentTime.After(building.CloseTime) || currentTime.Equal(building.CloseTime) {
					building.mu.Lock()
					// Only close if we have data
					if len(building.candles1m) > 0 {
						a.completeIntervalNow(building, interval, keyStr)
					}
					building.mu.Unlock()
				}
			}
			return true
		})
	}
}

// completeIntervalNow immediately completes an interval candle (called with lock held)
func (a *IntervalAggregator) completeIntervalNow(building *BuildingCandle, interval, key string) {
	// Aggregate what we have
	var (
		open, high, low, close, volume, quoteVolume decimal.Decimal
		tradeCount                                  int
		firstCandle                                 = true
	)

	var candleTimes []int64
	for t := range building.candles1m {
		candleTimes = append(candleTimes, t)
	}

	for i := 1; i < len(candleTimes); i++ {
		k := candleTimes[i]
		j := i - 1
		for j >= 0 && candleTimes[j] > k {
			candleTimes[j+1] = candleTimes[j]
			j--
		}
		candleTimes[j+1] = k
	}

	for _, t := range candleTimes {
		c := building.candles1m[t]
		if firstCandle {
			open, high, low, close = c.Open, c.High, c.Low, c.Close
			firstCandle = false
		} else {
			if c.High.GreaterThan(high) {
				high = c.High
			}
			if c.Low.LessThan(low) || low.IsZero() {
				low = c.Low
			}
			close = c.Close
		}
		volume = volume.Add(c.Volume)
		quoteVolume = quoteVolume.Add(c.QuoteVolume)
		tradeCount += c.TradeCount
	}

	agg := &models.Candle{
		Symbol: building.Symbol, Interval: building.Interval,
		OpenTime: building.OpenTime, CloseTime: building.CloseTime,
		Open: open, High: high, Low: low, Close: close,
		Volume: volume, QuoteVolume: quoteVolume, TradeCount: tradeCount,
		Source: "aggregated", IsClosed: true, ContractType: "spot",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	latestKey := fmt.Sprintf("%s:%s", building.Symbol, interval)
	a.lastCompletedCandles.Store(latestKey, agg)
	go a.publishCandle(agg)

	select {
	case a.batchChan <- agg:
		a.logger.Infof("💾 Completed %s candle stored in memory: %s at %v (close: %s)",
			interval, building.Symbol, agg.OpenTime, agg.Close.String())
	default:
		a.logger.Warn("⚠️ Batch channel full")
	}

	a.buildingCandles.Delete(key)
}

// updateIntervalCandle updates a specific interval candle with new 1m data
// ⚡ REAL-TIME: This is called for EVERY 1m candle update (even incomplete ones)
// So all intervals update as fast as 1m candles!
func (a *IntervalAggregator) updateIntervalCandle(m1Candle *models.Candle, interval string) {
	// Calculate the open time for this interval candle
	intervalOpenTime := getIntervalOpenTime(m1Candle.OpenTime, interval)
	intervalCloseTime := intervalOpenTime.Add(intervalToDuration(interval))

	// Create key for this building candle
	key := fmt.Sprintf("%s:%s:%d", m1Candle.Symbol, interval, intervalOpenTime.Unix())

	// Get or create building candle
	buildingInterface, _ := a.buildingCandles.LoadOrStore(key, &BuildingCandle{
		Symbol:          m1Candle.Symbol,
		Interval:        interval,
		OpenTime:        intervalOpenTime,
		CloseTime:       intervalCloseTime,
		candles1m:       make(map[int64]*models.Candle),
		expected1mCount: getExpected1mCount(interval),
	})
	building := buildingInterface.(*BuildingCandle)

	// Update the building candle
	building.mu.Lock()
	defer building.mu.Unlock()

	// ⚡ CRITICAL FIX: Store the full 1m candle data (replacing old if exists)
	// This allows us to recalculate volumes when the 1m candle updates
	m1OpenTime := m1Candle.OpenTime.Unix()
	building.candles1m[m1OpenTime] = m1Candle

	// ⚡ RECALCULATE ALL VALUES from stored 1m candles
	// This ensures REAL-TIME updates for volume, close, high, low
	var (
		open        decimal.Decimal
		high        decimal.Decimal
		low         decimal.Decimal
		close       decimal.Decimal
		volume      decimal.Decimal
		quoteVolume decimal.Decimal
		tradeCount  int
		firstCandle = true
	)

	// Sort candle times to ensure correct order
	var candleTimes []int64
	for t := range building.candles1m {
		candleTimes = append(candleTimes, t)
	}

	// Simple insertion sort (efficient for small arrays)
	for i := 1; i < len(candleTimes); i++ {
		key := candleTimes[i]
		j := i - 1
		for j >= 0 && candleTimes[j] > key {
			candleTimes[j+1] = candleTimes[j]
			j--
		}
		candleTimes[j+1] = key
	}

	// Aggregate all stored 1m candles
	for _, t := range candleTimes {
		candle := building.candles1m[t]

		if firstCandle {
			// First candle sets the initial values
			open = candle.Open
			high = candle.High
			low = candle.Low
			close = candle.Close
			firstCandle = false
		} else {
			// Update high/low/close
			if candle.High.GreaterThan(high) {
				high = candle.High
			}
			if candle.Low.LessThan(low) || low.IsZero() {
				low = candle.Low
			}
			close = candle.Close // Keep updating to latest
		}

		// Sum volumes from all 1m candles
		volume = volume.Add(candle.Volume)
		quoteVolume = quoteVolume.Add(candle.QuoteVolume)
		tradeCount += candle.TradeCount
	}

	// ⚡ FIX: No longer complete candles here - closeExpiredIntervals() handles completion
	// Just publish the current building state for real-time updates
	aggregatedCandle := &models.Candle{
		Symbol:       building.Symbol,
		Interval:     building.Interval,
		OpenTime:     building.OpenTime,
		CloseTime:    building.CloseTime,
		Open:         open,
		High:         high,
		Low:          low,
		Close:        close,
		Volume:       volume,
		QuoteVolume:  quoteVolume,
		TradeCount:   tradeCount,
		Source:       "aggregated",
		IsClosed:     false, // Always building - closeExpiredIntervals() marks as complete
		ContractType: "spot",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	// ⚡ Publish to Redis immediately for real-time updates (building state)
	go a.publishCandle(aggregatedCandle)
}

// periodicAggregation builds historical candles from existing 1m data
func (a *IntervalAggregator) periodicAggregation() {
	defer a.wg.Done()

	// Run immediately on start
	a.aggregateHistoricalCandles()

	// Then run every 5 minutes to catch up any missing candles
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.aggregateHistoricalCandles()
		}
	}
}

// aggregateHistoricalCandles builds missing candles from historical 1m data
func (a *IntervalAggregator) aggregateHistoricalCandles() {
	a.logger.Info("📊 Aggregating historical candles from 1m data...")

	// Get list of symbols that have 1m candles
	symbols, err := a.repo.GetAvailableSymbols(context.Background())
	if err != nil {
		a.logger.WithError(err).Error("Failed to get available symbols")
		return
	}

	a.logger.Infof("Found %d symbols to aggregate", len(symbols))

	// For each symbol and interval, build candles for the last 24 hours
	for _, symbol := range symbols {
		for _, interval := range a.intervals {
			go a.buildHistoricalInterval(symbol, interval)
		}
	}
}

// buildHistoricalInterval builds candles for a specific symbol and interval
func (a *IntervalAggregator) buildHistoricalInterval(symbol, interval string) {
	ctx := context.Background()

	// Calculate time range (last 7 days to ensure we have enough data)
	endTime := time.Now()
	startTime := endTime.Add(-7 * 24 * time.Hour)

	// Get all 1m candles for this symbol in the time range
	candles1m, err := a.repo.GetCandles(ctx, symbol, "1m", "", "spot", startTime, endTime, 100000)
	if err != nil {
		a.logger.WithError(err).Debugf("Failed to get 1m candles for %s", symbol)
		return
	}

	if len(candles1m) == 0 {
		return
	}

	a.logger.Debugf("Building %s candles for %s from %d 1m candles", interval, symbol, len(candles1m))

	// Group 1m candles by interval periods
	intervalCandles := a.group1mCandlesByInterval(candles1m, interval)

	// Save to database
	if len(intervalCandles) > 0 {
		err := a.repo.BatchCreateCandles(ctx, intervalCandles)
		if err != nil {
			a.logger.WithError(err).Errorf("Failed to save %s candles for %s", interval, symbol)
		} else {
			a.logger.Infof("✅ Built %d %s candles for %s", len(intervalCandles), interval, symbol)

			// ⚡ FIX: Store the most recent completed candle for future reference
			if len(intervalCandles) > 0 {
				latestCandle := intervalCandles[len(intervalCandles)-1]
				latestKey := fmt.Sprintf("%s:%s", symbol, interval)
				a.lastCompletedCandles.Store(latestKey, latestCandle)
			}
		}
	}
}

// group1mCandlesByInterval groups 1m candles into higher timeframe candles
func (a *IntervalAggregator) group1mCandlesByInterval(candles1m []models.Candle, interval string) []*models.Candle {
	if len(candles1m) == 0 {
		return nil
	}

	intervalDuration := intervalToDuration(interval)
	result := make([]*models.Candle, 0)

	// Group candles by interval periods
	groups := make(map[int64][]models.Candle)

	for _, candle := range candles1m {
		intervalOpenTime := getIntervalOpenTime(candle.OpenTime, interval)
		key := intervalOpenTime.Unix()
		groups[key] = append(groups[key], candle)
	}

	// Build aggregated candles from groups
	for openTimeUnix, group := range groups {
		if len(group) == 0 {
			continue
		}

		openTime := time.Unix(openTimeUnix, 0)
		closeTime := openTime.Add(intervalDuration)

		// Aggregate the candles
		var high, low decimal.Decimal
		var open, close decimal.Decimal
		var volume, quoteVolume decimal.Decimal
		var tradeCount int

		for i, candle := range group {
			if i == 0 {
				open = candle.Open
				high = candle.High
				low = candle.Low
			} else {
				if candle.High.GreaterThan(high) {
					high = candle.High
				}
				if candle.Low.LessThan(low) {
					low = candle.Low
				}
			}
			close = candle.Close // Keep updating to get the latest
			volume = volume.Add(candle.Volume)
			quoteVolume = quoteVolume.Add(candle.QuoteVolume)
			tradeCount += candle.TradeCount
		}

		result = append(result, &models.Candle{
			Symbol:       group[0].Symbol,
			Interval:     interval,
			OpenTime:     openTime,
			CloseTime:    closeTime,
			Open:         open,
			High:         high,
			Low:          low,
			Close:        close,
			Volume:       volume,
			QuoteVolume:  quoteVolume,
			TradeCount:   tradeCount,
			Source:       "aggregated",
			IsClosed:     true,
			ContractType: "spot",
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		})
	}

	return result
}

// batchWriter persists aggregated candles to database
func (a *IntervalAggregator) batchWriter() {
	defer a.wg.Done()

	batch := make([]*models.Candle, 0, 100)
	// ⚡ FIX: Reduced from 5 seconds to 1 second for faster persistence
	// This ensures completed candles are saved to database quickly
	// Previous completed candles will be available in DB sooner for fallback queries
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}

		if err := a.repo.BatchCreateCandles(context.Background(), batch); err != nil {
			a.logger.WithError(err).Error("Failed to persist aggregated candles")
		} else {
			a.logger.Infof("💾 Persisted %d aggregated candles to database", len(batch))
		}

		batch = make([]*models.Candle, 0, 100)
	}

	for {
		select {
		case <-a.ctx.Done():
			flush()
			return
		case candle, ok := <-a.batchChan:
			if !ok {
				flush()
				return
			}
			batch = append(batch, candle)
			// ⚡ FIX: Reduced batch size from 100 to 50 for faster flushes
			// This ensures completed candles are written more frequently
			if len(batch) >= 50 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// publishCandle publishes aggregated candle to Redis
func (a *IntervalAggregator) publishCandle(candle *models.Candle) {
	channel := fmt.Sprintf("nyyu:market:candle:%s:%s", candle.Symbol, candle.Interval)
	if err := a.pubsub.PublishCandle(context.Background(), channel, candle); err != nil {
		a.logger.WithError(err).Debugf("Failed to publish %s candle for %s", candle.Interval, candle.Symbol)
	}
}

// Helper functions

func getIntervalOpenTime(t time.Time, interval string) time.Time {
	switch interval {
	case "3m":
		m := (t.Minute() / 3) * 3
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), m, 0, 0, t.Location())
	case "5m":
		m := (t.Minute() / 5) * 5
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), m, 0, 0, t.Location())
	case "15m":
		m := (t.Minute() / 15) * 15
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), m, 0, 0, t.Location())
	case "30m":
		m := (t.Minute() / 30) * 30
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), m, 0, 0, t.Location())
	case "1h":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
	case "2h":
		h := (t.Hour() / 2) * 2
		return time.Date(t.Year(), t.Month(), t.Day(), h, 0, 0, 0, t.Location())
	case "4h":
		h := (t.Hour() / 4) * 4
		return time.Date(t.Year(), t.Month(), t.Day(), h, 0, 0, 0, t.Location())
	case "6h":
		h := (t.Hour() / 6) * 6
		return time.Date(t.Year(), t.Month(), t.Day(), h, 0, 0, 0, t.Location())
	case "8h":
		h := (t.Hour() / 8) * 8
		return time.Date(t.Year(), t.Month(), t.Day(), h, 0, 0, 0, t.Location())
	case "12h":
		h := (t.Hour() / 12) * 12
		return time.Date(t.Year(), t.Month(), t.Day(), h, 0, 0, 0, t.Location())
	case "1d":
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	case "3d":
		daysSinceEpoch := int(t.Unix() / 86400)
		alignedDay := (daysSinceEpoch / 3) * 3
		return time.Unix(int64(alignedDay*86400), 0).UTC()
	case "1w":
		// Align to Monday 00:00
		weekday := int(t.Weekday())
		if weekday == 0 {
			weekday = 7 // Sunday = 7
		}
		daysBack := weekday - 1 // Days back to Monday
		return time.Date(t.Year(), t.Month(), t.Day()-daysBack, 0, 0, 0, 0, t.Location())
	case "1M":
		// Align to first day of the month at 00:00
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	default:
		return t
	}
}

func intervalToDuration(interval string) time.Duration {
	switch interval {
	case "1m":
		return time.Minute
	case "3m":
		return 3 * time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return time.Hour
	case "2h":
		return 2 * time.Hour
	case "4h":
		return 4 * time.Hour
	case "6h":
		return 6 * time.Hour
	case "8h":
		return 8 * time.Hour
	case "12h":
		return 12 * time.Hour
	case "1d":
		return 24 * time.Hour
	case "3d":
		return 72 * time.Hour
	case "1w":
		return 7 * 24 * time.Hour
	case "1M":
		return 30 * 24 * time.Hour // Approximate 30 days
	default:
		return time.Minute
	}
}

func getExpected1mCount(interval string) int {
	switch interval {
	case "3m":
		return 3
	case "5m":
		return 5
	case "15m":
		return 15
	case "30m":
		return 30
	case "1h":
		return 60
	case "2h":
		return 120
	case "4h":
		return 240
	case "6h":
		return 360
	case "8h":
		return 480
	case "12h":
		return 720
	case "1d":
		return 1440
	case "3d":
		return 4320
	case "1w":
		return 10080
	case "1M":
		return 43200 // Approximate 30 days * 1440 minutes
	default:
		return 1
	}
}
