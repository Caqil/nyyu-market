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

type Service struct {
	repo   *repository.CandleRepository
	cache  *cache.CandleCache
	pubsub *pubsub.Publisher
	config *config.Config
	logger *logrus.Logger
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

	// Try cache first
	cacheKey := fmt.Sprintf("latest:%s:%s:%s:%s", symbol, interval, source, contractType)
	if cached, err := s.cache.GetLatest(ctx, cacheKey); err == nil && cached != nil {
		return cached, nil
	}

	// Fetch from repository
	candle, err := s.repo.GetLatestCandle(ctx, symbol, interval, source, contractType)
	if err != nil {
		return nil, err
	}

	// Cache it
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
