package price

import (
	"context"
	"fmt"
	"time"

	"nyyu-market/internal/cache"
	"nyyu-market/internal/config"
	"nyyu-market/internal/models"
	"nyyu-market/internal/pubsub"
	candleService "nyyu-market/internal/services/candle"

	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

type Service struct {
	candleSvc *candleService.Service
	cache     *cache.PriceCache
	pubsub    *pubsub.Publisher
	config    *config.Config
	logger    *logrus.Logger
}

func NewService(
	candleSvc *candleService.Service,
	cache *cache.PriceCache,
	pubsub *pubsub.Publisher,
	config *config.Config,
	logger *logrus.Logger,
) *Service {
	return &Service{
		candleSvc: candleSvc,
		cache:     cache,
		pubsub:    pubsub,
		config:    config,
		logger:    logger,
	}
}

// GetPrice retrieves current price for a symbol
func (s *Service) GetPrice(ctx context.Context, symbol string) (*models.Price, error) {
	// Try cache first
	if cached, err := s.cache.GetPrice(ctx, symbol); err == nil && cached != nil {
		return cached, nil
	}

	// Calculate from latest candle
	price, err := s.calculatePriceFromCandles(ctx, symbol)
	if err != nil {
		return nil, err
	}

	// Cache it
	_ = s.cache.SetPrice(ctx, symbol, price, s.config.Cache.PriceTTL)

	return price, nil
}

// calculatePriceFromCandles calculates 24h stats from candles
func (s *Service) calculatePriceFromCandles(ctx context.Context, symbol string) (*models.Price, error) {
	// Get latest 1m candle for current price
	latestCandle, err := s.candleSvc.GetLatestCandle(ctx, symbol, "1m", "", "spot")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest candle: %w", err)
	}

	if latestCandle == nil {
		return nil, fmt.Errorf("no candle data for %s", symbol)
	}

	// Get 24h candles for statistics
	endTime := time.Now()
	startTime := endTime.Add(-24 * time.Hour)

	candles, err := s.candleSvc.GetCandles(ctx, symbol, "1m", "", "spot", startTime, endTime, 1440) // 24h of 1m candles
	if err != nil {
		return nil, fmt.Errorf("failed to get 24h candles: %w", err)
	}

	if len(candles) == 0 {
		// Fallback to just latest candle
		return &models.Price{
			Symbol:    symbol,
			LastPrice: latestCandle.Close,
			UpdatedAt: latestCandle.CloseTime,
		}, nil
	}

	// Calculate 24h stats
	firstCandle := candles[0]
	high24h := latestCandle.High
	low24h := latestCandle.Low
	volume24h := decimal.Zero
	quoteVolume24h := decimal.Zero

	for _, candle := range candles {
		if candle.High.GreaterThan(high24h) {
			high24h = candle.High
		}
		if candle.Low.LessThan(low24h) {
			low24h = candle.Low
		}
		volume24h = volume24h.Add(candle.Volume)
		quoteVolume24h = quoteVolume24h.Add(candle.QuoteVolume)
	}

	// Calculate price change
	priceChange := latestCandle.Close.Sub(firstCandle.Open)
	priceChangePercent := decimal.Zero
	if !firstCandle.Open.IsZero() {
		priceChangePercent = priceChange.Div(firstCandle.Open).Mul(decimal.NewFromInt(100))
	}

	price := &models.Price{
		Symbol:         symbol,
		LastPrice:      latestCandle.Close,
		PriceChange24h: priceChange,
		PriceChange24P: priceChangePercent,
		High24h:        high24h,
		Low24h:         low24h,
		Volume24h:      volume24h,
		QuoteVolume24h: quoteVolume24h,
		UpdatedAt:      latestCandle.CloseTime,
	}

	return price, nil
}

// UpdatePrice updates and broadcasts price for a symbol
func (s *Service) UpdatePrice(ctx context.Context, symbol string) error {
	price, err := s.calculatePriceFromCandles(ctx, symbol)
	if err != nil {
		return err
	}

	// Cache it
	if err := s.cache.SetPrice(ctx, symbol, price, s.config.Cache.PriceTTL); err != nil {
		s.logger.WithError(err).Warn("Failed to cache price")
	}

	// Publish update
	if err := s.pubsub.PublishPrice(ctx, symbol, price); err != nil {
		s.logger.WithError(err).Warn("Failed to publish price update")
	}

	return nil
}

// StartPriceUpdater starts background price update worker
func (s *Service) StartPriceUpdater(ctx context.Context, symbols []string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, symbol := range symbols {
				go func(sym string) {
					if err := s.UpdatePrice(ctx, sym); err != nil {
						s.logger.WithError(err).Debugf("Failed to update price for %s", sym)
					}
				}(symbol)
			}
		}
	}
}
