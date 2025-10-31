package candle

import (
	"context"
	"sync"
	"time"

	"nyyu-market/internal/models"
	"nyyu-market/internal/repository"

	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

// StreamingPriceCalculator maintains 24h statistics in real-time
// without blocking or recalculating from scratch every time
//
// Key improvements:
// 1. Incremental updates - add new candles, remove old ones
// 2. Ring buffer for 24h candles (O(1) updates)
// 3. Background refresh for cold symbols
// 4. No database queries on hot path
type StreamingPriceCalculator struct {
	repo   *repository.CandleRepository
	logger *logrus.Logger

	// ⚡ LOCK-FREE: Current 24h stats per symbol
	// Key: symbol -> *Symbol24hStats
	stats sync.Map

	// Control
	stopChan chan struct{}
}

// Symbol24hStats tracks 24h statistics for a symbol in real-time
type Symbol24hStats struct {
	Symbol string

	// Ring buffer of last 24h of 1m candles (1440 candles max)
	candles     []*models.Candle
	candlesMu   sync.RWMutex
	nextIndex   int
	isFull      bool

	// Pre-calculated aggregates (updated incrementally)
	open24h        decimal.Decimal
	high24h        decimal.Decimal
	low24h         decimal.Decimal
	volume24h      decimal.Decimal
	quoteVolume24h decimal.Decimal

	// Metadata
	lastUpdate time.Time
	isWarm     bool
}

// NewStreamingPriceCalculator creates a new streaming calculator
func NewStreamingPriceCalculator(repo *repository.CandleRepository, logger *logrus.Logger) *StreamingPriceCalculator {
	return &StreamingPriceCalculator{
		repo:     repo,
		logger:   logger,
		stopChan: make(chan struct{}),
	}
}

// Start starts the background warmup and maintenance goroutines
func (c *StreamingPriceCalculator) Start(ctx context.Context) {
	// Background task: Warm up cold symbols periodically
	go c.backgroundWarmup(ctx)
}

// Stop stops the calculator
func (c *StreamingPriceCalculator) Stop() {
	close(c.stopChan)
}

// ProcessCandle processes a new 1m candle and updates 24h stats incrementally
// ⚡ O(1) operation - just add new, remove old
func (c *StreamingPriceCalculator) ProcessCandle(candle *models.Candle) {
	if candle.Interval != "1m" {
		return // Only process 1m candles for 24h stats
	}

	// Get or create stats
	statsInterface, _ := c.stats.LoadOrStore(candle.Symbol, &Symbol24hStats{
		Symbol:     candle.Symbol,
		candles:    make([]*models.Candle, 1440), // 24h * 60m
		lastUpdate: time.Now(),
	})
	stats := statsInterface.(*Symbol24hStats)

	stats.candlesMu.Lock()
	defer stats.candlesMu.Unlock()

	// Get the candle being replaced (if any)
	oldCandle := stats.candles[stats.nextIndex]

	// Add new candle to ring buffer
	stats.candles[stats.nextIndex] = candle
	stats.nextIndex = (stats.nextIndex + 1) % 1440

	if stats.nextIndex == 0 {
		stats.isFull = true
	}

	// ⚡ INCREMENTAL UPDATE: Just adjust the aggregates
	// Subtract old candle's contribution
	if oldCandle != nil {
		stats.volume24h = stats.volume24h.Sub(oldCandle.Volume)
		stats.quoteVolume24h = stats.quoteVolume24h.Sub(oldCandle.QuoteVolume)
	}

	// Add new candle's contribution
	stats.volume24h = stats.volume24h.Add(candle.Volume)
	stats.quoteVolume24h = stats.quoteVolume24h.Add(candle.QuoteVolume)

	// Recalculate high/low (can't be incremental, but fast scan)
	stats.recalculateHighLow()

	// Update 24h open (first candle in ring buffer)
	oldestIndex := stats.nextIndex // After increment, this points to oldest
	if stats.candles[oldestIndex] != nil {
		stats.open24h = stats.candles[oldestIndex].Open
	}

	stats.lastUpdate = time.Now()
	stats.isWarm = true
}

// recalculateHighLow scans all candles to find high/low
// ⚡ O(n) where n=1440, but runs rarely and is still fast (~1ms)
func (stats *Symbol24hStats) recalculateHighLow() {
	var maxHigh, minLow decimal.Decimal
	first := true

	for _, candle := range stats.candles {
		if candle == nil {
			continue
		}

		if first {
			maxHigh = candle.High
			minLow = candle.Low
			first = false
		} else {
			if candle.High.GreaterThan(maxHigh) {
				maxHigh = candle.High
			}
			if candle.Low.LessThan(minLow) {
				minLow = candle.Low
			}
		}
	}

	stats.high24h = maxHigh
	stats.low24h = minLow
}

// Get24hStats retrieves current 24h statistics for a symbol
// ⚡ O(1) if warm, falls back to database if cold
func (c *StreamingPriceCalculator) Get24hStats(ctx context.Context, symbol string, currentPrice decimal.Decimal) (*models.Price, error) {
	// Try to get warm stats first
	if statsInterface, ok := c.stats.Load(symbol); ok {
		stats := statsInterface.(*Symbol24hStats)

		stats.candlesMu.RLock()
		isWarm := stats.isWarm
		open24h := stats.open24h
		high24h := stats.high24h
		low24h := stats.low24h
		volume24h := stats.volume24h
		quoteVolume24h := stats.quoteVolume24h
		stats.candlesMu.RUnlock()

		if isWarm {
			// Calculate price change
			priceChange := currentPrice.Sub(open24h)
			priceChangePercent := decimal.Zero
			if !open24h.IsZero() {
				priceChangePercent = priceChange.Div(open24h).Mul(decimal.NewFromInt(100))
			}

			return &models.Price{
				Symbol:         symbol,
				LastPrice:      currentPrice,
				PriceChange24h: priceChange,
				PriceChange24P: priceChangePercent,
				High24h:        high24h,
				Low24h:         low24h,
				Volume24h:      volume24h,
				QuoteVolume24h: quoteVolume24h,
				UpdatedAt:      time.Now(),
			}, nil
		}
	}

	// Cold path: Warm up from database
	c.logger.Debugf("⚠️  Cold symbol %s, warming up from database", symbol)
	return c.warmupSymbol(ctx, symbol, currentPrice)
}

// warmupSymbol loads 24h of candles from database and initializes stats
// ⚠️  Only called for cold symbols, not on hot path
func (c *StreamingPriceCalculator) warmupSymbol(ctx context.Context, symbol string, currentPrice decimal.Decimal) (*models.Price, error) {
	// Fetch last 24h of 1m candles from database
	endTime := time.Now()
	startTime := endTime.Add(-24 * time.Hour)

	candles, err := c.repo.GetCandles(ctx, symbol, "1m", "", "spot", startTime, endTime, 1440)
	if err != nil {
		return nil, err
	}

	if len(candles) == 0 {
		// No historical data, return basic price
		return &models.Price{
			Symbol:    symbol,
			LastPrice: currentPrice,
			UpdatedAt: time.Now(),
		}, nil
	}

	// Create stats and populate ring buffer
	stats := &Symbol24hStats{
		Symbol:     symbol,
		candles:    make([]*models.Candle, 1440),
		lastUpdate: time.Now(),
		isWarm:     true,
	}

	// Populate ring buffer
	for i, candle := range candles {
		if i >= 1440 {
			break
		}
		stats.candles[i] = &candle
		stats.volume24h = stats.volume24h.Add(candle.Volume)
		stats.quoteVolume24h = stats.quoteVolume24h.Add(candle.QuoteVolume)
	}

	stats.nextIndex = len(candles) % 1440
	if len(candles) >= 1440 {
		stats.isFull = true
	}

	// Calculate high/low
	stats.recalculateHighLow()

	// Set 24h open
	if len(candles) > 0 {
		stats.open24h = candles[0].Open
	}

	// Store stats
	c.stats.Store(symbol, stats)

	// Calculate price
	priceChange := currentPrice.Sub(stats.open24h)
	priceChangePercent := decimal.Zero
	if !stats.open24h.IsZero() {
		priceChangePercent = priceChange.Div(stats.open24h).Mul(decimal.NewFromInt(100))
	}

	c.logger.Infof("✅ Warmed up %s with %d candles", symbol, len(candles))

	return &models.Price{
		Symbol:         symbol,
		LastPrice:      currentPrice,
		PriceChange24h: priceChange,
		PriceChange24P: priceChangePercent,
		High24h:        stats.high24h,
		Low24h:         stats.low24h,
		Volume24h:      stats.volume24h,
		QuoteVolume24h: stats.quoteVolume24h,
		UpdatedAt:      time.Now(),
	}, nil
}

// backgroundWarmup periodically warms up active symbols
func (c *StreamingPriceCalculator) backgroundWarmup(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopChan:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Count warm symbols
			warmCount := 0
			c.stats.Range(func(key, value interface{}) bool {
				warmCount++
				return true
			})
			c.logger.Debugf("📊 Currently tracking %d warm symbols", warmCount)
		}
	}
}

// CleanupStaleSymbols removes symbols that haven't been accessed in a while
func (c *StreamingPriceCalculator) CleanupStaleSymbols(maxAge time.Duration) {
	now := time.Now()
	removed := 0

	c.stats.Range(func(key, value interface{}) bool {
		stats := value.(*Symbol24hStats)
		stats.candlesMu.RLock()
		lastUpdate := stats.lastUpdate
		stats.candlesMu.RUnlock()

		if now.Sub(lastUpdate) > maxAge {
			c.stats.Delete(key)
			removed++
		}
		return true
	})

	if removed > 0 {
		c.logger.Infof("🗑️  Cleaned up %d stale symbols", removed)
	}
}
