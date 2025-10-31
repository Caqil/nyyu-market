package markprice

import (
	"context"
	"fmt"
	"sync"
	"time"

	"nyyu-market/internal/cache"
	"nyyu-market/internal/config"
	"nyyu-market/internal/models"
	"nyyu-market/internal/pubsub"
	"nyyu-market/internal/services/aggregator"

	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

type Service struct {
	cache      *cache.PriceCache
	pubsub     *pubsub.Publisher
	aggregator *aggregator.ExchangeAggregator
	config     *config.Config
	logger     *logrus.Logger

	// EMA state for funding basis
	fundingBasisEMA sync.Map // symbol -> decimal.Decimal (thread-safe)
}

func NewService(
	cache *cache.PriceCache,
	pubsub *pubsub.Publisher,
	aggregator *aggregator.ExchangeAggregator,
	config *config.Config,
	logger *logrus.Logger,
) *Service {
	return &Service{
		cache:      cache,
		pubsub:     pubsub,
		aggregator: aggregator,
		config:     config,
		logger:     logger,
		// fundingBasisEMA is sync.Map, no initialization needed
	}
}

// GetMarkPrice retrieves mark price for a symbol
func (s *Service) GetMarkPrice(ctx context.Context, symbol string) (*models.MarkPrice, error) {
	// Try cache first
	if cached, err := s.cache.GetMarkPrice(ctx, symbol); err == nil && cached != nil {
		return cached, nil
	}

	// Calculate on the fly if not in cache
	markPrice, err := s.CalculateMarkPrice(ctx, symbol)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate mark price: %w", err)
	}

	// Cache it
	_ = s.cache.SetMarkPrice(ctx, symbol, markPrice, s.config.Cache.MarkPriceTTL)

	return markPrice, nil
}

// CalculateMarkPrice calculates mark price using Binance-style formula
// Mark Price = Index Price + Funding Basis (EMA)
// Index Price = Weighted average from multiple spot exchanges
// Funding Basis = EMA of (Perpetual Price - Index Price)
func (s *Service) CalculateMarkPrice(ctx context.Context, symbol string) (*models.MarkPrice, error) {
	// Get aggregated spot price (index price)
	indexPrice, exchangeCount := s.aggregator.GetAggregatedPrice(symbol)
	if exchangeCount == 0 {
		return nil, fmt.Errorf("no price data available for %s", symbol)
	}

	// Get last perpetual price (simplified - use spot price for now)
	// TODO: Add futures price from futures WebSocket streams
	lastPrice := indexPrice

	// Calculate funding basis: (Perpetual - Index)
	fundingBasis := lastPrice.Sub(indexPrice)

	// Get or initialize EMA for this symbol using sync.Map
	previousEMAInterface, exists := s.fundingBasisEMA.LoadOrStore(symbol, fundingBasis)
	var previousEMA decimal.Decimal
	if exists {
		previousEMA = previousEMAInterface.(decimal.Decimal)
	} else {
		previousEMA = fundingBasis
	}

	// Calculate EMA: EMA = (Current * α) + (Previous * (1 - α))
	// α = 2 / (N + 1), where N = period
	alpha := decimal.NewFromInt(2).Div(decimal.NewFromInt(int64(s.config.MarkPrice.EMAPeriod + 1)))
	ema := fundingBasis.Mul(alpha).Add(previousEMA.Mul(decimal.NewFromInt(1).Sub(alpha)))

	// Store new EMA
	s.fundingBasisEMA.Store(symbol, ema)

	// Mark Price = Index Price + Funding Basis (EMA)
	markPriceValue := indexPrice.Add(ema)

	// Calculate estimated funding rate (simplified)
	// Funding Rate = Funding Basis / Index Price
	estimatedFundingRate := decimal.Zero
	if !indexPrice.IsZero() {
		estimatedFundingRate = ema.Div(indexPrice)
	}

	// Next funding time (every 8 hours at 00:00, 08:00, 16:00 UTC)
	now := time.Now().UTC()
	hour := now.Hour()
	nextFundingHour := ((hour / 8) + 1) * 8
	if nextFundingHour >= 24 {
		nextFundingHour = 0
	}
	nextFundingTime := time.Date(now.Year(), now.Month(), now.Day(), nextFundingHour, 0, 0, 0, time.UTC)
	if nextFundingTime.Before(now) {
		nextFundingTime = nextFundingTime.Add(24 * time.Hour)
	}

	markPrice := &models.MarkPrice{
		Symbol:               symbol,
		MarkPrice:            markPriceValue,
		IndexPrice:           indexPrice,
		LastPrice:            lastPrice,
		FundingBasis:         ema,
		EstimatedFundingRate: estimatedFundingRate,
		NextFundingTime:      nextFundingTime,
		Timestamp:            now,
	}

	return markPrice, nil
}

// UpdateMarkPrice calculates and stores mark price (cache only, no database)
func (s *Service) UpdateMarkPrice(ctx context.Context, symbol string) error {
	markPrice, err := s.CalculateMarkPrice(ctx, symbol)
	if err != nil {
		return err
	}

	// Cache it
	if err := s.cache.SetMarkPrice(ctx, symbol, markPrice, s.config.Cache.MarkPriceTTL); err != nil {
		s.logger.WithError(err).Warn("Failed to cache mark price")
	}

	// Publish update
	if err := s.pubsub.PublishMarkPrice(ctx, symbol, markPrice); err != nil {
		s.logger.WithError(err).Warn("Failed to publish mark price update")
	}

	return nil
}

// StartMarkPriceUpdater starts background mark price calculation
// Now uses event-driven updates (triggered by price changes) + timer as backup
func (s *Service) StartMarkPriceUpdater(ctx context.Context, symbols []string) {
	s.logger.Infof("⚡ Starting mark price updater (interval: %v, symbols: %d)", s.config.MarkPrice.UpdateInterval, len(symbols))

	// ⚡ INSTANT: Subscribe to real-time price updates from aggregator
	s.logger.Info("⚡ Subscribing to real-time price updates for instant mark price calculations...")
	for _, symbol := range symbols {
		sym := symbol // Capture for closure
		s.aggregator.SubscribePrice(sym, func(symbol string, price decimal.Decimal, exchangeCount int) {
			// ⚡ Update mark price IMMEDIATELY when price changes
			if err := s.UpdateMarkPrice(ctx, sym); err != nil {
				s.logger.WithError(err).Debugf("Failed to update mark price for %s", sym)
			}
		})
		s.logger.Debugf("✅ Subscribed to real-time prices for %s", sym)
	}

	// ⚡ Timer as backup (catches any symbols that don't have active price updates)
	ticker := time.NewTicker(s.config.MarkPrice.UpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Backup timer - updates all symbols
			for _, symbol := range symbols {
				go func(sym string) {
					if err := s.UpdateMarkPrice(ctx, sym); err != nil {
						s.logger.WithError(err).Debugf("Failed to update mark price for %s", sym)
					}
				}(symbol)
			}
		}
	}
}
