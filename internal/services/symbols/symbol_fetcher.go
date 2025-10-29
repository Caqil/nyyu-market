package symbols

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// SymbolResponse represents the API response for available symbols
type SymbolResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    struct {
		Symbols []string `json:"symbols"`
		Count   int      `json:"count"`
	} `json:"data"`
}

// SymbolFetcher fetches and caches available symbols from Binance API
type SymbolFetcher struct {
	apiURL      string
	symbols     []string
	lastFetch   time.Time
	cacheTTL    time.Duration
	mu          sync.RWMutex
	logger      *logrus.Logger
	httpClient  *http.Client
}

// NewSymbolFetcher creates a new symbol fetcher
func NewSymbolFetcher(apiURL string, logger *logrus.Logger) *SymbolFetcher {
	return &SymbolFetcher{
		apiURL:   apiURL,
		cacheTTL: 1 * time.Hour, // Refresh symbols every hour
		logger:   logger,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		// Default fallback symbols in case API is down
		symbols: []string{
			"BTCUSDT", "ETHUSDT", "BNBUSDT", "SOLUSDT", "XRPUSDT",
			"ADAUSDT", "DOGEUSDT", "AVAXUSDT", "DOTUSDT", "MATICUSDT",
		},
	}
}

// GetSymbols returns cached symbols or fetches them if cache is stale
func (f *SymbolFetcher) GetSymbols(ctx context.Context) ([]string, error) {
	f.mu.RLock()
	// Check if cache is still valid
	if time.Since(f.lastFetch) < f.cacheTTL && len(f.symbols) > 0 {
		symbols := make([]string, len(f.symbols))
		copy(symbols, f.symbols)
		f.mu.RUnlock()
		return symbols, nil
	}
	f.mu.RUnlock()

	// Cache is stale, fetch new symbols
	return f.FetchSymbols(ctx)
}

// GetPopularSymbols returns a subset of popular trading symbols
func (f *SymbolFetcher) GetPopularSymbols(ctx context.Context, limit int) ([]string, error) {
	symbols, err := f.GetSymbols(ctx)
	if err != nil {
		return nil, err
	}

	// Return first N symbols (most popular ones are usually first)
	if len(symbols) < limit {
		return symbols, nil
	}

	return symbols[:limit], nil
}

// FetchSymbols fetches symbols from the API
func (f *SymbolFetcher) FetchSymbols(ctx context.Context) ([]string, error) {
	f.logger.Info("Fetching available symbols from Binance API...")

	req, err := http.NewRequestWithContext(ctx, "GET", f.apiURL, nil)
	if err != nil {
		return f.getFallbackSymbols(), fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		f.logger.WithError(err).Warn("Failed to fetch symbols, using cached/fallback")
		return f.getFallbackSymbols(), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		f.logger.Warnf("Symbols API returned status %d, using cached/fallback", resp.StatusCode)
		return f.getFallbackSymbols(), nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		f.logger.WithError(err).Warn("Failed to read response body, using cached/fallback")
		return f.getFallbackSymbols(), nil
	}

	var symbolResp SymbolResponse
	if err := json.Unmarshal(body, &symbolResp); err != nil {
		f.logger.WithError(err).Warn("Failed to parse symbols response, using cached/fallback")
		return f.getFallbackSymbols(), nil
	}

	if symbolResp.Status != "success" || len(symbolResp.Data.Symbols) == 0 {
		f.logger.Warn("Invalid symbols response, using cached/fallback")
		return f.getFallbackSymbols(), nil
	}

	// Update cache
	f.mu.Lock()
	f.symbols = symbolResp.Data.Symbols
	f.lastFetch = time.Now()
	f.mu.Unlock()

	f.logger.Infof("Successfully fetched %d symbols from Binance API", len(symbolResp.Data.Symbols))
	return symbolResp.Data.Symbols, nil
}

// RefreshSymbols forces a refresh of the symbol cache
func (f *SymbolFetcher) RefreshSymbols(ctx context.Context) error {
	_, err := f.FetchSymbols(ctx)
	return err
}

// StartAutoRefresh starts automatic symbol refresh in the background
func (f *SymbolFetcher) StartAutoRefresh(ctx context.Context) {
	ticker := time.NewTicker(f.cacheTTL)
	defer ticker.Stop()

	// Fetch immediately on start
	if _, err := f.FetchSymbols(ctx); err != nil {
		f.logger.WithError(err).Warn("Initial symbol fetch failed")
	}

	for {
		select {
		case <-ctx.Done():
			f.logger.Info("Symbol auto-refresh stopped")
			return
		case <-ticker.C:
			if _, err := f.FetchSymbols(ctx); err != nil {
				f.logger.WithError(err).Warn("Symbol refresh failed")
			}
		}
	}
}

// getFallbackSymbols returns cached or default fallback symbols
func (f *SymbolFetcher) getFallbackSymbols() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()

	symbols := make([]string, len(f.symbols))
	copy(symbols, f.symbols)
	return symbols
}

// GetSymbolCount returns the number of cached symbols
func (f *SymbolFetcher) GetSymbolCount() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.symbols)
}
