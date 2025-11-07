package symbols

import (
	"context"
	"fmt"
	"sync"
	"time"

	"nyyu-market/internal/config"

	"github.com/sirupsen/logrus"
)

// SubscriptionInterface defines methods needed for subscribing to symbols
type SubscriptionInterface interface {
	SubscribeToInterval(ctx context.Context, symbol, interval string) error
	UnsubscribeFromInterval(ctx context.Context, symbol, interval string) error
}

// SymbolManager manages dynamic symbol subscriptions
type SymbolManager struct {
	config       *config.SymbolConfig
	fetcher      *SymbolFetcher
	subscriber   SubscriptionInterface
	logger       *logrus.Logger

	// Track subscribed symbols
	subscribedSymbols map[string]bool
	mu                sync.RWMutex

	// Control
	ctx    context.Context
	cancel context.CancelFunc
}

// NewSymbolManager creates a new symbol manager
func NewSymbolManager(
	cfg *config.SymbolConfig,
	fetcher *SymbolFetcher,
	subscriber SubscriptionInterface,
	logger *logrus.Logger,
) *SymbolManager {
	ctx, cancel := context.WithCancel(context.Background())

	return &SymbolManager{
		config:            cfg,
		fetcher:           fetcher,
		subscriber:        subscriber,
		logger:            logger,
		subscribedSymbols: make(map[string]bool),
		ctx:               ctx,
		cancel:            cancel,
	}
}

// Start begins the dynamic symbol management
func (m *SymbolManager) Start() error {
	m.logger.Info("🚀 Starting dynamic symbol manager...")

	// Initial subscription
	if err := m.refreshAndSubscribe(); err != nil {
		m.logger.WithError(err).Warn("Initial symbol subscription failed")
	}

	// Start periodic refresh if auto-subscribe is enabled
	if m.config.AutoSubscribe {
		go m.periodicRefresh()
		m.logger.Infof("✅ Auto-subscribe enabled - refreshing every %v", m.config.RefreshInterval)
	}

	return nil
}

// Stop stops the symbol manager
func (m *SymbolManager) Stop() {
	m.logger.Info("🛑 Stopping symbol manager...")
	m.cancel()
}

// refreshAndSubscribe fetches symbols and subscribes based on strategy
func (m *SymbolManager) refreshAndSubscribe() error {
	// Fetch all available symbols
	allSymbols, err := m.fetcher.GetSymbols(m.ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch symbols: %w", err)
	}

	if len(allSymbols) == 0 {
		m.logger.Warn("No symbols fetched - using defaults")
		allSymbols = DefaultSymbols
	}

	// Apply subscription strategy to get symbols to subscribe
	symbolsToSubscribe := m.applyStrategy(allSymbols)

	m.logger.Infof("📊 Strategy: %s, Total: %d, To subscribe: %d",
		m.config.SubscriptionStrategy, len(allSymbols), len(symbolsToSubscribe))

	// Subscribe to new symbols
	newCount := 0
	for _, symbol := range symbolsToSubscribe {
		m.mu.RLock()
		alreadySubscribed := m.subscribedSymbols[symbol]
		m.mu.RUnlock()

		if !alreadySubscribed {
			// Subscribe to 1m interval - all other intervals are derived from this
			if err := m.subscriber.SubscribeToInterval(m.ctx, symbol, "1m"); err != nil {
				m.logger.WithError(err).Warnf("Failed to subscribe to %s", symbol)
			} else {
				m.mu.Lock()
				m.subscribedSymbols[symbol] = true
				m.mu.Unlock()
				newCount++
			}
		}
	}

	if newCount > 0 {
		m.logger.Infof("✅ Subscribed to %d new symbols", newCount)
	}

	m.logger.Infof("📈 Total subscribed symbols: %d", len(m.subscribedSymbols))

	return nil
}

// applyStrategy applies the subscription strategy to filter symbols
func (m *SymbolManager) applyStrategy(allSymbols []string) []string {
	switch m.config.SubscriptionStrategy {
	case "all":
		// Subscribe to all symbols
		m.logger.Info("Strategy: ALL - subscribing to all symbols")
		return allSymbols

	case "popular":
		// Subscribe to curated popular symbols
		m.logger.Info("Strategy: POPULAR - subscribing to curated popular symbols")
		return m.getPopularSymbols(allSymbols)

	case "top_N":
		// Subscribe to top N symbols
		m.logger.Infof("Strategy: TOP_N - subscribing to top %d symbols", m.config.MaxSymbols)
		if len(allSymbols) <= m.config.MaxSymbols {
			return allSymbols
		}
		return allSymbols[:m.config.MaxSymbols]

	default:
		// Default to top_N with 50 symbols
		m.logger.Warnf("Unknown strategy '%s', using top_N with 50 symbols", m.config.SubscriptionStrategy)
		if len(allSymbols) <= 50 {
			return allSymbols
		}
		return allSymbols[:50]
	}
}

// getPopularSymbols returns a curated list of popular trading symbols
func (m *SymbolManager) getPopularSymbols(allSymbols []string) []string {
	popularList := []string{
		"BTCUSDT", "ETHUSDT", "BNBUSDT", "SOLUSDT", "XRPUSDT",
		"ADAUSDT", "DOGEUSDT", "AVAXUSDT", "DOTUSDT", "MATICUSDT",
		"LINKUSDT", "UNIUSDT", "ATOMUSDT", "LTCUSDT", "ETCUSDT",
		"TRXUSDT", "NEARUSDT", "XLMUSDT", "ALGOUSDT", "VETUSDT",
		"AAVEUSDT", "ARBUSDT", "OPUSDT", "INJUSDT", "IMXUSDT",
		"GMXUSDT", "DYDXUSDT", "RUNEUSDT", "CRVUSDT", "SUSHIUSDT",
	}

	// Filter to only symbols that exist in allSymbols
	symbolMap := make(map[string]bool)
	for _, s := range allSymbols {
		symbolMap[s] = true
	}

	result := make([]string, 0, len(popularList))
	for _, symbol := range popularList {
		if symbolMap[symbol] {
			result = append(result, symbol)
		}
	}

	return result
}

// periodicRefresh periodically checks for new symbols and subscribes
func (m *SymbolManager) periodicRefresh() {
	ticker := time.NewTicker(m.config.RefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.logger.Info("🔄 Refreshing symbol list...")
			if err := m.refreshAndSubscribe(); err != nil {
				m.logger.WithError(err).Warn("Symbol refresh failed")
			}
		}
	}
}

// GetSubscribedSymbols returns the list of currently subscribed symbols
func (m *SymbolManager) GetSubscribedSymbols() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	symbols := make([]string, 0, len(m.subscribedSymbols))
	for symbol := range m.subscribedSymbols {
		symbols = append(symbols, symbol)
	}

	return symbols
}

// GetSubscribedCount returns the number of subscribed symbols
func (m *SymbolManager) GetSubscribedCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.subscribedSymbols)
}

// ForceRefresh forces an immediate symbol refresh
func (m *SymbolManager) ForceRefresh() error {
	m.logger.Info("🔄 Force refreshing symbol list...")
	return m.refreshAndSubscribe()
}
