package candle

import (
	"context"
	"fmt"
	"time"

	"nyyu-market/internal/cache"
	"nyyu-market/internal/config"
	"nyyu-market/internal/models"
	"nyyu-market/internal/pubsub"
	"nyyu-market/internal/repository"

	"github.com/sirupsen/logrus"
)

// ExchangeAggregatorInterface defines methods needed from exchange aggregator
type ExchangeAggregatorInterface interface {
	GetLatestInMemoryCandle(symbol, interval string) *models.Candle
	SubscribeToInterval(ctx context.Context, symbol, interval string) error
	UnsubscribeFromInterval(ctx context.Context, symbol, interval string) error
}

type Service struct {
	repo   *repository.CandleRepository
	cache  *cache.CandleCache
	pubsub *pubsub.Publisher
	config *config.Config
	logger *logrus.Logger

	// ⚡ REAL-TIME: Use exchange aggregator's in-memory candles
	exchangeAgg ExchangeAggregatorInterface

	// ⚡ 24h STATS: Background calculator for price tickers
	stats24hCalc *StreamingPriceCalculator
}

func NewService(
	repo *repository.CandleRepository,
	cache *cache.CandleCache,
	pubsub *pubsub.Publisher,
	config *config.Config,
	logger *logrus.Logger,
) *Service {
	return &Service{
		repo:   repo,
		cache:  cache,
		pubsub: pubsub,
		config: config,
		logger: logger,
	}
}

// SetExchangeAggregator sets the exchange aggregator for real-time in-memory candles
func (s *Service) SetExchangeAggregator(agg ExchangeAggregatorInterface) {
	s.exchangeAgg = agg
	s.logger.Info("✅ Exchange aggregator connected to candle service for real-time in-memory candles")
}

// SubscribeToInterval subscribes to a symbol+interval on exchange websockets
// This delegates to the exchange aggregator for on-demand subscriptions
func (s *Service) SubscribeToInterval(ctx context.Context, symbol, interval string) error {
	if s.exchangeAgg == nil {
		return fmt.Errorf("exchange aggregator not initialized")
	}
	return s.exchangeAgg.SubscribeToInterval(ctx, symbol, interval)
}

// UnsubscribeFromInterval unsubscribes from a symbol+interval
// This decrements the reference count and removes the subscription if no longer needed
func (s *Service) UnsubscribeFromInterval(ctx context.Context, symbol, interval string) error {
	if s.exchangeAgg == nil {
		return fmt.Errorf("exchange aggregator not initialized")
	}
	return s.exchangeAgg.UnsubscribeFromInterval(ctx, symbol, interval)
}

// GetCandles retrieves candles with caching
func (s *Service) GetCandles(ctx context.Context, symbol, interval, source, contractType string, startTime, endTime time.Time, limit int) ([]models.Candle, error) {
	// Apply limits
	if limit <= 0 {
		limit = s.config.Service.DefaultCandlesLimit
	}
	if limit > s.config.Service.MaxCandlesLimit {
		limit = s.config.Service.MaxCandlesLimit
	}

	// Default to spot if not specified
	if contractType == "" {
		contractType = "spot"
	}

	// Try cache for latest candles (no time range, small limit)
	if startTime.IsZero() && endTime.IsZero() && limit <= 100 {
		cacheKey := fmt.Sprintf("%s:%s:%s:%s", symbol, interval, source, contractType)
		if cached, err := s.cache.Get(ctx, cacheKey); err == nil && cached != nil {
			s.logger.Debug("Cache hit for candles")
			return cached, nil
		}
	}

	// Fetch from repository
	candles, err := s.repo.GetCandles(ctx, symbol, interval, source, contractType, startTime, endTime, limit)
	if err != nil {
		return nil, err
	}

	// Cache latest candles
	if startTime.IsZero() && endTime.IsZero() && limit <= 100 {
		cacheKey := fmt.Sprintf("%s:%s:%s:%s", symbol, interval, source, contractType)
		_ = s.cache.Set(ctx, cacheKey, candles, s.config.Cache.CandleTTL)
	}

	return candles, nil
}

// GetLatestCandle retrieves the latest candle with caching
func (s *Service) GetLatestCandle(ctx context.Context, symbol, interval, source, contractType string) (*models.Candle, error) {
	if contractType == "" {
		contractType = "spot"
	}

	// ⚡ PRIORITY 1: Real-time in-memory candle from exchange aggregator
	// Only use in-memory for aggregated source (source == "" or "aggregated")
	if s.exchangeAgg != nil && (source == "" || source == "aggregated") {
		inMemoryCandle := s.exchangeAgg.GetLatestInMemoryCandle(symbol, interval)
		if inMemoryCandle != nil {
			s.logger.Debugf("✅ [TIER-1] Real-time in-memory candle for %s %s (<1ms)", symbol, interval)

			// ⚡ REAL-TIME FIX: Cache the in-memory candle for fallback
			// This ensures fast fallback if in-memory data temporarily unavailable
			cacheKey := fmt.Sprintf("latest:%s:%s:%s:%s", symbol, interval, source, contractType)
			_ = s.cache.SetLatest(ctx, cacheKey, inMemoryCandle, 5*time.Second)

			return inMemoryCandle, nil
		}
		s.logger.Debugf("⚠️  No in-memory candle for %s %s, falling back to cache", symbol, interval)
	}

	// PRIORITY 2: Try Redis cache (fast fallback)
	cacheKey := fmt.Sprintf("latest:%s:%s:%s:%s", symbol, interval, source, contractType)
	if cached, err := s.cache.GetLatest(ctx, cacheKey); err == nil && cached != nil {
		s.logger.Debugf("✅ [TIER-2] Redis cached candle for %s %s (1-5ms)", symbol, interval)
		return cached, nil
	}

	// PRIORITY 3: Fetch from ClickHouse repository (slow fallback)
	s.logger.Debugf("⚠️  Cache miss for %s %s, querying ClickHouse (100-500ms)", symbol, interval)
	candle, err := s.repo.GetLatestCandle(ctx, symbol, interval, source, contractType)
	if err != nil {
		return nil, err
	}

	// Cache it with longer TTL for database-sourced data
	_ = s.cache.SetLatest(ctx, cacheKey, candle, s.config.Cache.CandleTTL)

	return candle, nil
}

// CreateCandle creates a new candle and broadcasts it
func (s *Service) CreateCandle(ctx context.Context, candle *models.Candle) error {
	// Save to repository
	if err := s.repo.CreateCandle(ctx, candle); err != nil {
		return err
	}

	// Invalidate cache
	cacheKey := fmt.Sprintf("%s:%s:%s:%s", candle.Symbol, candle.Interval, candle.Source, candle.ContractType)
	_ = s.cache.Delete(ctx, cacheKey)

	latestKey := fmt.Sprintf("latest:%s:%s:%s:%s", candle.Symbol, candle.Interval, candle.Source, candle.ContractType)
	_ = s.cache.Delete(ctx, latestKey)

	// Publish update
	channel := fmt.Sprintf("nyyu:market:candle:%s:%s", candle.Symbol, candle.Interval)
	if err := s.pubsub.PublishCandle(ctx, channel, candle); err != nil {
		s.logger.WithError(err).Error("Failed to publish candle update")
	}

	return nil
}

// BatchCreateCandles creates multiple candles efficiently
func (s *Service) BatchCreateCandles(ctx context.Context, candles []*models.Candle) error {
	if len(candles) == 0 {
		return nil
	}

	// Batch insert
	if err := s.repo.BatchCreateCandles(ctx, candles); err != nil {
		return err
	}

	// Invalidate cache for affected symbols
	for _, candle := range candles {
		cacheKey := fmt.Sprintf("%s:%s:%s:%s", candle.Symbol, candle.Interval, candle.Source, candle.ContractType)
		_ = s.cache.Delete(ctx, cacheKey)

		latestKey := fmt.Sprintf("latest:%s:%s:%s:%s", candle.Symbol, candle.Interval, candle.Source, candle.ContractType)
		_ = s.cache.Delete(ctx, latestKey)

		// Publish last candle (most recent)
		if candle == candles[len(candles)-1] {
			channel := fmt.Sprintf("nyyu:market:candle:%s:%s", candle.Symbol, candle.Interval)
			_ = s.pubsub.PublishCandle(ctx, channel, candle)
		}
	}

	return nil
}

// GetStats retrieves candle statistics
func (s *Service) GetStats(ctx context.Context) (map[string]interface{}, error) {
	return s.repo.GetStats(ctx)
}

// GetAvailableSymbols retrieves all available trading symbols from the database
func (s *Service) GetAvailableSymbols(ctx context.Context) ([]string, error) {
	return s.repo.GetAvailableSymbols(ctx)
}

// ========================================================================
// PRICE TICKER METHODS
// ========================================================================

// Start24hStatsCalculator starts the background 24h statistics calculator
func (s *Service) Start24hStatsCalculator(ctx context.Context) {
	if s.stats24hCalc == nil {
		s.stats24hCalc = NewStreamingPriceCalculator(s.repo, s.logger)
		s.stats24hCalc.Start(ctx)
		s.logger.Info("✅ 24h stats calculator started for price tickers")
	}
}

// Stop24hStatsCalculator stops the background calculator
func (s *Service) Stop24hStatsCalculator() {
	if s.stats24hCalc != nil {
		s.stats24hCalc.Stop()
	}
}

// ProcessCandleForStats processes a new candle for 24h stats calculation
func (s *Service) ProcessCandleForStats(candle *models.Candle) {
	if s.stats24hCalc != nil {
		s.stats24hCalc.ProcessCandle(candle)
	}
}

// GetPriceTicker retrieves price ticker from latest 1m candle + 24h stats
func (s *Service) GetPriceTicker(ctx context.Context, symbol string) (*models.Price, error) {
	// Get latest 1m candle for current price
	candle, err := s.GetLatestCandle(ctx, symbol, "1m", "", "spot")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest candle: %w", err)
	}
	if candle == nil {
		return nil, fmt.Errorf("no candle data available for %s", symbol)
	}

	// Extract current price from candle's close price
	currentPrice := candle.Close

	// Get 24h stats from background calculator
	if s.stats24hCalc != nil {
		priceWithStats, err := s.stats24hCalc.Get24hStats(ctx, symbol, currentPrice)
		if err == nil && priceWithStats != nil {
			return priceWithStats, nil
		}
		// If stats unavailable, continue with basic price
		s.logger.Debugf("⚠️  24h stats unavailable for %s, returning basic price", symbol)
	}

	// Return basic price without 24h stats
	return &models.Price{
		Symbol:    symbol,
		LastPrice: currentPrice,
		UpdatedAt: candle.UpdatedAt,
	}, nil
}

// GetPriceTickers retrieves price tickers for multiple symbols
func (s *Service) GetPriceTickers(ctx context.Context, symbols []string) ([]*models.Price, error) {
	if len(symbols) == 0 {
		// If no symbols specified, get all available symbols
		availableSymbols, err := s.GetAvailableSymbols(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get available symbols: %w", err)
		}
		symbols = availableSymbols
	}

	prices := make([]*models.Price, 0, len(symbols))
	for _, symbol := range symbols {
		price, err := s.GetPriceTicker(ctx, symbol)
		if err != nil {
			s.logger.WithError(err).Warnf("Failed to get price ticker for %s", symbol)
			continue
		}
		prices = append(prices, price)
	}

	return prices, nil
}

// ConvertCandleToPrice converts a candle to a price ticker with 24h stats
func (s *Service) ConvertCandleToPrice(ctx context.Context, candle *models.Candle) *models.Price {
	if candle == nil {
		return nil
	}

	currentPrice := candle.Close

	// Try to get 24h stats
	if s.stats24hCalc != nil {
		if priceWithStats, err := s.stats24hCalc.Get24hStats(ctx, candle.Symbol, currentPrice); err == nil && priceWithStats != nil {
			return priceWithStats
		}
	}

	// Return basic price without 24h stats
	return &models.Price{
		Symbol:    candle.Symbol,
		LastPrice: currentPrice,
		UpdatedAt: candle.UpdatedAt,
	}
}
