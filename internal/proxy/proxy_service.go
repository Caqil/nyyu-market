package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"time"

	"github.com/sirupsen/logrus"
	"go.uber.org/zap"
)

const (
	// ProxyListURL is the GitHub URL for dynamic SOCKS5 proxy list
	ProxyListURL = "https://raw.githubusercontent.com/hookzof/socks5_list/master/proxy.txt"

	// Update intervals
	InitialFetchTimeout = 10 * time.Second
	UpdateInterval      = 30 * time.Minute // Update every 30 minutes
	FetchTimeout        = 15 * time.Second

	// Default protocol for proxies from this source
	DefaultProtocol = "socks5"
)

type ProxyService struct {
	logger     *logrus.Logger
	proxyList  []string
	lastUpdate time.Time
	mu         sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
	httpClient *http.Client

	// Shared working proxy - when one service connects successfully, others use it
	workingProxy   string
	workingProxyMu sync.RWMutex
}

func NewProxyService(logger *logrus.Logger) *ProxyService {
	return &ProxyService{
		logger:     logger,
		proxyList:  []string{""}, // Start with direct connection as fallback
		lastUpdate: time.Time{},
		httpClient: &http.Client{
			Timeout: FetchTimeout,
			Transport: &http.Transport{
				MaxIdleConns:       10,
				IdleConnTimeout:    10 * time.Second,
				DisableCompression: true,
			},
		},
	}
}

// Start initializes the proxy service and begins periodic updates
func (p *ProxyService) Start(ctx context.Context) error {
	p.ctx, p.cancel = context.WithCancel(ctx)

	// Initial fetch with timeout
	p.logger.Info("Starting ProxyService - fetching initial proxy list from GitHub")

	fetchCtx, fetchCancel := context.WithTimeout(ctx, InitialFetchTimeout)
	defer fetchCancel()

	if err := p.fetchProxies(fetchCtx); err != nil {
		p.logger.WithError(err).Warn("Failed to fetch initial proxy list, using direct connection")
		// Don't return error - service can work with direct connection
	}

	// Start background updater
	go p.periodicUpdate()

	p.logger.WithFields(logrus.Fields{
		"initial_proxy_count": len(p.proxyList),
		"last_update":         p.lastUpdate,
	}).Info("ProxyService started successfully")

	return nil
}

// Stop gracefully stops the proxy service
func (p *ProxyService) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.logger.Info("ProxyService stopped")
}

// periodicUpdate runs in background and updates proxy list periodically
func (p *ProxyService) periodicUpdate() {
	ticker := time.NewTicker(UpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			p.logger.Info("Stopping proxy updater")
			return
		case <-ticker.C:
			p.logger.Info("Updating proxy list from GitHub")
			if err := p.fetchProxies(p.ctx); err != nil {
				p.logger.WithError(err).Error("Failed to update proxy list")
			}
		}
	}
}

// fetchProxies fetches the latest proxy list from GitHub
func (p *ProxyService) fetchProxies(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", ProxyListURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Add headers to avoid rate limiting
	req.Header.Set("User-Agent", "CoinbaseClone-ProxyService/1.0")
	req.Header.Set("Accept", "text/plain")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch proxy list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	proxies, err := p.parseProxyList(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to parse proxy list: %w", err)
	}

	if len(proxies) == 0 {
		return fmt.Errorf("no proxies found in response")
	}

	// Update the proxy list
	p.mu.Lock()
	p.proxyList = proxies
	p.lastUpdate = time.Now()
	p.mu.Unlock()

	p.logger.WithFields(logrus.Fields{
		"proxy_count": len(proxies),
		"updated_at":  p.lastUpdate,
	}).Info("Successfully updated proxy list")

	return nil
}

// parseProxyList parses the proxy list from the response body
// This source provides IP:PORT format, we'll add the protocol prefix
func (p *ProxyService) parseProxyList(body io.Reader) ([]string, error) {
	var proxies []string
	scanner := bufio.NewScanner(body)

	validCount := 0
	invalidCount := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check if line already has protocol prefix
		var proxyURL string
		if strings.Contains(line, "://") {
			// Already has protocol
			proxyURL = line
		} else {
			// Add default protocol (socks5)
			// Validate IP:PORT format
			if !strings.Contains(line, ":") {
				invalidCount++
				continue
			}

			// Basic validation - should have port number
			parts := strings.Split(line, ":")
			if len(parts) != 2 {
				invalidCount++
				continue
			}

			// Add protocol prefix
			proxyURL = DefaultProtocol + "://" + line
		}

		// Validate the proxy URL format
		if p.isValidProxyURL(proxyURL) {
			proxies = append(proxies, proxyURL)
			validCount++
		} else {
			invalidCount++
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading proxy list: %w", err)
	}

	// Add direct connection as fallback
	proxies = append(proxies, "")

	p.logger.WithFields(logrus.Fields{
		"valid":    validCount,
		"invalid":  invalidCount,
		"total":    len(proxies) - 1, // -1 for direct connection
		"protocol": DefaultProtocol,
	}).Info("Parsed proxy list")

	return proxies, nil
}

// isValidProxyURL validates proxy URL format
func (p *ProxyService) isValidProxyURL(proxyURL string) bool {
	if proxyURL == "" {
		return false
	}

	// Must have protocol
	if !strings.Contains(proxyURL, "://") {
		return false
	}

	parts := strings.SplitN(proxyURL, "://", 2)
	if len(parts) != 2 {
		return false
	}

	protocol := strings.ToLower(parts[0])
	address := parts[1]

	// Validate protocol
	if protocol != "http" && protocol != "https" &&
		protocol != "socks4" && protocol != "socks5" {
		return false
	}

	// Validate address format (should have ip:port)
	if !strings.Contains(address, ":") {
		return false
	}

	// Check if port is present and looks valid
	addressParts := strings.Split(address, ":")
	if len(addressParts) != 2 {
		return false
	}

	// Port should not be empty
	if addressParts[1] == "" {
		return false
	}

	return true
}

// GetProxyList returns the current proxy list
func (p *ProxyService) GetProxyList() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Return a copy to avoid race conditions
	list := make([]string, len(p.proxyList))
	copy(list, p.proxyList)

	return list
}

// GetHTTPProxyList returns only HTTP/HTTPS proxies
func (p *ProxyService) GetHTTPProxyList() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var httpProxies []string
	for _, proxy := range p.proxyList {
		if proxy == "" || strings.HasPrefix(proxy, "http://") || strings.HasPrefix(proxy, "https://") {
			httpProxies = append(httpProxies, proxy)
		}
	}

	return httpProxies
}

// GetSOCKSProxyList returns only SOCKS4/SOCKS5 proxies
func (p *ProxyService) GetSOCKSProxyList() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var socksProxies []string
	for _, proxy := range p.proxyList {
		if strings.HasPrefix(proxy, "socks4://") || strings.HasPrefix(proxy, "socks5://") {
			socksProxies = append(socksProxies, proxy)
		}
	}

	// Add direct connection fallback
	socksProxies = append(socksProxies, "")

	return socksProxies
}

// GetProxyCount returns the current number of proxies (excluding direct connection)
func (p *ProxyService) GetProxyCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	count := len(p.proxyList)
	if count > 0 && p.proxyList[len(p.proxyList)-1] == "" {
		count-- // Don't count direct connection
	}

	return count
}

// GetLastUpdate returns the timestamp of the last successful update
func (p *ProxyService) GetLastUpdate() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.lastUpdate
}

// GetProxyInfo returns detailed information about the current proxy list
func (p *ProxyService) GetProxyInfo() map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()

	httpCount := 0
	socks4Count := 0
	socks5Count := 0
	httpsCount := 0

	for _, proxy := range p.proxyList {
		switch {
		case strings.HasPrefix(proxy, "http://"):
			httpCount++
		case strings.HasPrefix(proxy, "https://"):
			httpsCount++
		case strings.HasPrefix(proxy, "socks4://"):
			socks4Count++
		case strings.HasPrefix(proxy, "socks5://"):
			socks5Count++
		}
	}

	return map[string]interface{}{
		"total_proxies":    p.GetProxyCount(),
		"http_proxies":     httpCount,
		"https_proxies":    httpsCount,
		"socks4_proxies":   socks4Count,
		"socks5_proxies":   socks5Count,
		"last_update":      p.lastUpdate,
		"source":           ProxyListURL,
		"source_type":      "SOCKS5 List (hookzof/socks5_list)",
		"default_protocol": DefaultProtocol,
		"update_interval":  UpdateInterval.String(),
		"status":           p.getStatus(),
	}
}

// getStatus returns the current status of the proxy service
func (p *ProxyService) getStatus() string {
	if p.lastUpdate.IsZero() {
		return "not_initialized"
	}

	timeSinceUpdate := time.Since(p.lastUpdate)
	if timeSinceUpdate > UpdateInterval*2 {
		return "stale"
	}

	if p.GetProxyCount() == 0 {
		return "no_proxies"
	}

	return "healthy"
}

// ForceUpdate triggers an immediate proxy list update
func (p *ProxyService) ForceUpdate() error {
	p.logger.Info("Force updating proxy list")

	ctx, cancel := context.WithTimeout(context.Background(), FetchTimeout)
	defer cancel()

	return p.fetchProxies(ctx)
}

// GetRandomProxy returns a random proxy from the list
func (p *ProxyService) GetRandomProxy() string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.proxyList) == 0 {
		return ""
	}

	// Use time-based randomization (simple but effective)
	index := int(time.Now().UnixNano()) % len(p.proxyList)
	return p.proxyList[index]
}

// GetProxyByType returns proxies filtered by protocol type
func (p *ProxyService) GetProxyByType(proxyType string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var filtered []string
	prefix := strings.ToLower(proxyType) + "://"

	for _, proxy := range p.proxyList {
		if proxy == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(proxy), prefix) {
			filtered = append(filtered, proxy)
		}
	}

	// Add direct connection as fallback
	if len(filtered) > 0 {
		filtered = append(filtered, "")
	}

	return filtered
}

// HealthCheck performs a basic health check on the proxy service
func (p *ProxyService) HealthCheck() error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.proxyList) <= 1 { // Only direct connection
		return fmt.Errorf("no proxies available")
	}

	if p.lastUpdate.IsZero() {
		return fmt.Errorf("proxy list never updated")
	}

	timeSinceUpdate := time.Since(p.lastUpdate)
	if timeSinceUpdate > UpdateInterval*3 {
		return fmt.Errorf("proxy list is stale (last update: %v ago)", timeSinceUpdate)
	}

	return nil
}

// SetWorkingProxy stores a proxy that successfully connected
// This allows other services to use the same working proxy
func (p *ProxyService) SetWorkingProxy(proxy string) {
	p.workingProxyMu.Lock()
	defer p.workingProxyMu.Unlock()

	if p.workingProxy != proxy {
		p.workingProxy = proxy
		p.logger.WithFields(logrus.Fields{"proxy": proxy}).Info("✅ Working proxy updated - all services will now use this")
	}
}

// GetWorkingProxy returns the last successfully connected proxy
// If available, services should try this proxy first
func (p *ProxyService) GetWorkingProxy() string {
	p.workingProxyMu.RLock()
	defer p.workingProxyMu.RUnlock()
	return p.workingProxy
}

// GetProxyListWithWorkingFirst returns proxy list with working proxy first
// This ensures all services try the working proxy before others
func (p *ProxyService) GetProxyListWithWorkingFirst() []string {
	p.mu.RLock()
	list := make([]string, len(p.proxyList))
	copy(list, p.proxyList)
	p.mu.RUnlock()

	p.workingProxyMu.RLock()
	workingProxy := p.workingProxy
	p.workingProxyMu.RUnlock()

	// If we have a working proxy, put it first
	if workingProxy != "" {
		// Remove working proxy from list if it exists
		filtered := make([]string, 0, len(list))
		for _, proxy := range list {
			if proxy != workingProxy {
				filtered = append(filtered, proxy)
			}
		}

		// Put working proxy first
		result := make([]string, 0, len(list))
		result = append(result, workingProxy)
		result = append(result, filtered...)

		return result
	}

	return list
}

// TestProxy tests if a proxy can connect to Binance API
// Returns true if proxy works, false otherwise
func (p *ProxyService) TestProxy(proxy string) bool {
	// Test URLs for Binance
	testURLs := []string{
		"https://api.binance.com/api/v3/ping",
		"https://api.binance.com/api/v3/time",
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:    1,
			IdleConnTimeout: 5 * time.Second,
		},
	}

	// If proxy specified, configure it
	if proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			p.logger.WithError(err).WithFields(logrus.Fields{"proxy": proxy}).Warn("Invalid proxy URL")
			return false
		}

		client.Transport = &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			MaxIdleConns:    1,
			IdleConnTimeout: 5 * time.Second,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // Skip TLS verification for SOCKS5 proxies
			},
		}
	}

	// Try each test URL
	for _, testURL := range testURLs {
		resp, err := client.Get(testURL)
		if err != nil {
			p.logger.WithError(err).WithFields(logrus.Fields{
				"proxy": proxy,
				"url":   testURL,
			}).Debug("Proxy test failed")
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			p.logger.WithFields(logrus.Fields{
				"proxy":    proxy,
				"test_url": testURL,
				"status":   resp.StatusCode,
			}).Info("✅ Proxy test PASSED")
			return true
		}
	}

	p.logger.WithFields(logrus.Fields{"proxy": proxy}).Warn("❌ Proxy test FAILED - all URLs unreachable")
	return false
}

// GetTestedProxyList tests proxies and returns working ones (stops after finding first working proxy)
func (p *ProxyService) GetTestedProxyList() []string {
	// Check if we already have a working proxy
	existingWorking := p.GetWorkingProxy()
	if existingWorking != "" {
		p.logger.Info("✅ Using existing working proxy - no testing needed",
			zap.String("proxy", existingWorking))
		return []string{existingWorking, ""} // Working proxy + direct fallback
	}

	p.mu.RLock()
	allProxies := make([]string, len(p.proxyList))
	copy(allProxies, p.proxyList)
	p.mu.RUnlock()

	p.logger.Info("🧪 Testing proxies for Binance connectivity (will stop after first success)...",
		zap.Int("total_proxies", len(allProxies)))

	workingProxies := make([]string, 0)
	testedCount := 0
	passedCount := 0
	failedCount := 0

	for _, proxy := range allProxies {
		testedCount++

		// Test the proxy
		if p.TestProxy(proxy) {
			workingProxies = append(workingProxies, proxy)
			passedCount++

			// Set as working proxy and STOP testing
			p.SetWorkingProxy(proxy)

			p.logger.Info("✅ Found working proxy - stopping tests",
				zap.Int("tested", testedCount),
				zap.Int("remaining", len(allProxies)-testedCount),
				zap.String("working_proxy", proxy))

			// Stop testing - we found a working one!
			break
		} else {
			failedCount++
		}

		// Log progress every 3 proxies
		if testedCount%3 == 0 {
			p.logger.Info("🧪 Proxy testing progress",
				zap.Int("tested", testedCount),
				zap.Int("total", len(allProxies)),
				zap.Int("failed", failedCount))
		}
	}

	p.logger.Info("✅ Proxy testing completed",
		zap.Int("total_tested", testedCount),
		zap.Int("working", passedCount),
		zap.Int("failed", failedCount))

	// If no working proxies, return original list (with warning)
	if len(workingProxies) == 0 {
		p.logger.Warn("⚠️ No working proxies found! Using original list as fallback")
		return allProxies
	}

	// Update proxy list with only working proxies
	p.mu.Lock()
	p.proxyList = workingProxies
	p.mu.Unlock()

	return workingProxies
}

// GetTestedProxyListWithWorkingFirst gets tested proxies with working proxy first
func (p *ProxyService) GetTestedProxyListWithWorkingFirst() []string {
	// Test all proxies first
	testedProxies := p.GetTestedProxyList()

	// Put working proxy first
	p.workingProxyMu.RLock()
	workingProxy := p.workingProxy
	p.workingProxyMu.RUnlock()

	if workingProxy != "" {
		// Remove working proxy from list if it exists
		filtered := make([]string, 0, len(testedProxies))
		for _, proxy := range testedProxies {
			if proxy != workingProxy {
				filtered = append(filtered, proxy)
			}
		}

		// Put working proxy first
		result := make([]string, 0, len(testedProxies))
		result = append(result, workingProxy)
		result = append(result, filtered...)

		return result
	}

	return testedProxies
}
