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

// BackendTradeSymbolsResponse represents backend trade API response
type BackendTradeSymbolsResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		Count   int      `json:"count"`
		Symbols []string `json:"symbols"`
	} `json:"data"`
}

// BinanceExchangeInfo represents Binance exchangeInfo API response
type BinanceExchangeInfo struct {
	Timezone   string `json:"timezone"`
	ServerTime int64  `json:"serverTime"`
	Symbols    []struct {
		Symbol     string `json:"symbol"`
		Status     string `json:"status"`
		BaseAsset  string `json:"baseAsset"`
		QuoteAsset string `json:"quoteAsset"`
	} `json:"symbols"`
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
		symbols: DefaultSymbols, // Use curated default list
	}
}

// NewSymbolFetcherWithSymbols creates a new symbol fetcher with custom symbols
func NewSymbolFetcherWithSymbols(apiURL string, customSymbols []string, logger *logrus.Logger) *SymbolFetcher {
	return &SymbolFetcher{
		apiURL:   apiURL,
		cacheTTL: 1 * time.Hour,
		logger:   logger,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		symbols: customSymbols, // Use provided symbols
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

// FetchSymbols fetches symbols from backend trade API
func (f *SymbolFetcher) FetchSymbols(ctx context.Context) ([]string, error) {
	f.logger.Info("Fetching available symbols from backend trade API...")

	req, err := http.NewRequestWithContext(ctx, "GET", f.apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		f.logger.WithError(err).Error("Failed to fetch symbols from backend trade API")
		return f.getCachedSymbols(), fmt.Errorf("failed to fetch symbols: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		f.logger.Errorf("Backend trade API returned status %d", resp.StatusCode)
		return f.getCachedSymbols(), fmt.Errorf("backend API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		f.logger.WithError(err).Error("Failed to read response body")
		return f.getCachedSymbols(), fmt.Errorf("failed to read response: %w", err)
	}

	var symbolsResp BackendTradeSymbolsResponse
	if err := json.Unmarshal(body, &symbolsResp); err != nil {
		f.logger.WithError(err).Error("Failed to parse backend trade response")
		return f.getCachedSymbols(), fmt.Errorf("failed to parse response: %w", err)
	}

	if !symbolsResp.Success {
		f.logger.Errorf("Backend trade API returned error: %s", symbolsResp.Message)
		return f.getCachedSymbols(), fmt.Errorf("backend API error: %s", symbolsResp.Message)
	}

	symbols := symbolsResp.Data.Symbols
	if len(symbols) == 0 {
		f.logger.Error("No symbols found in backend trade response")
		return f.getCachedSymbols(), fmt.Errorf("no symbols found")
	}

	// Update cache
	f.mu.Lock()
	f.symbols = symbols
	f.lastFetch = time.Now()
	f.mu.Unlock()

	f.logger.Infof("Successfully fetched %d symbols from backend trade API", len(symbols))
	return symbols, nil
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

// getCachedSymbols returns cached symbols only (no defaults)
func (f *SymbolFetcher) getCachedSymbols() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if len(f.symbols) == 0 {
		return []string{} // Return empty, no fallback
	}

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
