package aggregator

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nyyu-market/internal/config"
	"nyyu-market/internal/models"
	"nyyu-market/internal/pubsub"
	candleService "nyyu-market/internal/services/candle"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

// ProxyServiceInterface defines methods needed from proxy service
type ProxyServiceInterface interface {
	GetWorkingProxy() string
	GetProxyListWithWorkingFirst() []string
	SetWorkingProxy(proxy string)
	TestProxy(proxy string) bool
}

// Exchange represents a single exchange WebSocket connection
type Exchange struct {
	Name       string
	WSUrl      string
	IsEnabled  bool
	conn       *websocket.Conn
	isHealthy  bool
	msgCount   int64
	errorCount int64
	lastUpdate time.Time
	mu         sync.RWMutex

	// Circuit breaker
	failureCount    int64
	lastFailureTime time.Time
	backoffUntil    time.Time

	// Connection state tracking
	isConnecting    bool
	isSubscribed    bool
	lastConnectTime time.Time
}

// BinanceKlineMessage represents Binance kline WebSocket message
type BinanceKlineMessage struct {
	EventType string `json:"e"`
	Symbol    string `json:"s"`
	Kline     struct {
		StartTime           int64  `json:"t"`
		CloseTime           int64  `json:"T"`
		Symbol              string `json:"s"`
		Interval            string `json:"i"`
		Open                string `json:"o"`
		Close               string `json:"c"`
		High                string `json:"h"`
		Low                 string `json:"l"`
		BaseAssetVolume     string `json:"v"`
		NumberOfTrades      int    `json:"n"`
		IsKlineClosed       bool   `json:"x"`
		QuoteAssetVolume    string `json:"q"`
		TakerBuyBaseVolume  string `json:"V"`
		TakerBuyQuoteVolume string `json:"Q"`
	} `json:"k"`
}

// ExchangeAggregator aggregates market data from multiple exchanges
type ExchangeAggregator struct {
	config       *config.Config
	candleSvc    *candleService.Service
	pubsub       *pubsub.Publisher
	logger       *logrus.Logger
	proxyService ProxyServiceInterface // For bypassing 403 errors

	// Exchanges
	exchanges []*Exchange

	// Active subscriptions with reference counting for shared subscriptions
	// Key: "symbol@interval" (e.g., "btcusdt@1m")
	// Value: reference count (number of users watching)
	activeStreams   map[string]int32 // int32 for atomic operations
	activeStreamsMu sync.RWMutex

	// Subscription tracking by symbol
	// symbol -> interval -> refCount
	subscriptions   map[string]map[string]int32
	subscriptionsMu sync.RWMutex

	// Price aggregation (lock-free)
	aggregatedPrices sync.Map // symbol -> *sync.Map (exchange->price)

	// Batch processing
	candleBatchChan chan *models.Candle
	batchMu         sync.Mutex
	currentBatch    []*models.Candle
	batchTicker     *time.Ticker

	// Rate limiters
	rateLimiters  map[string]*rate.Limiter
	rateLimitMgr  *RateLimiterManager
	subBatchers   map[string]*SubscriptionBatcher
	subBatchersMu sync.Mutex

	// ⚡ NEW: Zero-delay real-time aggregator (lock-free, streaming)
	realtimeAgg *RealtimeCandleAggregator

	// ⚡ DEPRECATED: Old pending candles system (keep for compatibility during migration)
	// Map structure: symbol -> interval -> openTime -> exchange -> candle
	pendingCandles   map[string]map[string]map[int64]map[string]*models.Candle
	pendingCandlesMu sync.RWMutex

	// Track which candles have aggregation scheduled (prevent duplicates)
	scheduledAggregations   map[string]map[string]map[int64]bool
	scheduledAggregationsMu sync.Mutex

	// Control
	stopChan      chan struct{}
	reconnectChan chan struct{}
	isRunning     bool
	mu            sync.Mutex

	// Stats
	priceUpdateCount int64
	lastLogTime      time.Time
}

// NewExchangeAggregator creates a new exchange aggregator
func NewExchangeAggregator(
	cfg *config.Config,
	candleSvc *candleService.Service,
	pubsub *pubsub.Publisher,
	logger *logrus.Logger,
	proxyService ProxyServiceInterface,
) *ExchangeAggregator {
	now := time.Now()
	exchanges := []*Exchange{
		{Name: "Binance", WSUrl: "wss://stream.binance.com:443/stream", IsEnabled: cfg.Exchange.EnableBinance, lastUpdate: now},
		{Name: "Kraken", WSUrl: "wss://ws.kraken.com/v2", IsEnabled: cfg.Exchange.EnableKraken, lastUpdate: now},
		{Name: "Coinbase", WSUrl: "wss://ws-feed.exchange.coinbase.com", IsEnabled: cfg.Exchange.EnableCoinbase, lastUpdate: now},
		{Name: "Bybit", WSUrl: "wss://stream.bybit.com/v5/public/spot", IsEnabled: cfg.Exchange.EnableBybit, lastUpdate: now},
		{Name: "OKX", WSUrl: "wss://ws.okx.com:8443/ws/v5/public", IsEnabled: cfg.Exchange.EnableOKX, lastUpdate: now},
		{Name: "Gate.io", WSUrl: "wss://api.gateio.ws/ws/v4/", IsEnabled: cfg.Exchange.EnableGateIO, lastUpdate: now},
	}

	// Initialize rate limiter manager
	rateLimitMgr := NewRateLimiterManager()

	// Register rate limiters for each exchange (conservative limits for production)
	rateLimitMgr.RegisterExchange("Binance", 4.5, 10) // 4.5 req/sec, burst 10
	rateLimitMgr.RegisterExchange("Kraken", 10, 20)   // 10 req/sec, burst 20
	rateLimitMgr.RegisterExchange("Coinbase", 8, 15)  // 8 req/sec, burst 15
	rateLimitMgr.RegisterExchange("Bybit", 50, 100)   // 50 req/sec, burst 100
	rateLimitMgr.RegisterExchange("OKX", 3, 5)        // 3 req/sec, burst 5 (OKX is strict)
	rateLimitMgr.RegisterExchange("Gate.io", 30, 50)  // 30 req/sec, burst 50

	// Create candle batch channel
	candleBatchChan := make(chan *models.Candle, 10000)

	// ⚡ NEW: Initialize zero-delay real-time aggregator
	realtimeAgg := NewRealtimeCandleAggregator(pubsub, logger, candleBatchChan)

	// ⚡ Connect candle service to realtime aggregator for 24h stats
	realtimeAgg.SetCandleService(candleSvc)

	agg := &ExchangeAggregator{
		config:          cfg,
		candleSvc:       candleSvc,
		pubsub:          pubsub,
		logger:          logger,
		proxyService:    proxyService,
		exchanges:       exchanges,
		activeStreams:   make(map[string]int32),
		subscriptions:   make(map[string]map[string]int32),
		candleBatchChan: candleBatchChan, // ⚡ Shared channel for batch writing
		currentBatch:    make([]*models.Candle, 0, cfg.Service.BatchWriteSize),
		rateLimiters: map[string]*rate.Limiter{
			"Binance":  rate.NewLimiter(rate.Limit(4.5), 10),
			"Kraken":   rate.NewLimiter(rate.Limit(10), 20),
			"Coinbase": rate.NewLimiter(rate.Limit(8), 15),
			"Bybit":    rate.NewLimiter(rate.Limit(50), 100),
			"OKX":      rate.NewLimiter(rate.Limit(3), 5),
			"Gate.io":  rate.NewLimiter(rate.Limit(30), 50),
		},
		rateLimitMgr:          rateLimitMgr,
		subBatchers:           make(map[string]*SubscriptionBatcher),
		realtimeAgg:           realtimeAgg, // ⚡ NEW: Lock-free aggregator
		pendingCandles:        make(map[string]map[string]map[int64]map[string]*models.Candle),
		scheduledAggregations: make(map[string]map[string]map[int64]bool),
		stopChan:              make(chan struct{}),
		reconnectChan:         make(chan struct{}, 10), // Buffered for all exchanges
		lastLogTime:           time.Now(),
	}

	// Initialize subscription batchers for each exchange
	for _, ex := range exchanges {
		if ex.IsEnabled {
			batcher := NewSubscriptionBatcher(
				2*time.Second, // Batch subscriptions for 2 seconds
				50,            // Max 50 subscriptions per batch
				func(reqs []*SubscriptionRequest) {
					agg.processBatchedSubscriptions(reqs)
				},
			)
			agg.subBatchers[ex.Name] = batcher
		}
	}

	return agg
}

// SetIntervalAggregator connects the interval aggregator to receive real-time 1m candles
func (a *ExchangeAggregator) SetIntervalAggregator(intervalAgg IntervalAggregatorInterface) {
	if a.realtimeAgg != nil {
		a.realtimeAgg.SetIntervalAggregator(intervalAgg)
		a.logger.Info("✅ Interval aggregator connected to real-time 1m candle stream")
	}
}

// Start initializes all exchange connections
func (a *ExchangeAggregator) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.isRunning {
		a.mu.Unlock()
		return fmt.Errorf("aggregator already running")
	}
	a.isRunning = true
	a.mu.Unlock()

	// Start batch writer workers (5 workers for parallel processing)
	for i := 0; i < 5; i++ {
		go a.batchWriterWorker(ctx, i)
	}

	// Start subscription batchers for each exchange
	for name, batcher := range a.subBatchers {
		go batcher.Start(ctx)
		a.logger.Infof("Started subscription batcher for %s", name)
	}

	// Start exchange connections
	a.logger.Info("Initializing exchange connections...")
	enabledCount := 0
	for _, ex := range a.exchanges {
		if ex.IsEnabled {
			go a.manageExchangeConnection(ctx, ex)
			a.logger.Infof("✅ %s enabled - manager started (%s)", ex.Name, ex.WSUrl)
			enabledCount++
		} else {
			a.logger.Infof("⏭️  %s disabled in config - skipping", ex.Name)
		}
	}
	a.logger.Infof("Started managers for %d/%d exchanges", enabledCount, len(a.exchanges))

	// Start price aggregation monitor
	go a.monitorPriceAggregation(ctx)

	// Start rate limiter stats monitor
	go a.monitorRateLimiters(ctx)

	a.logger.Info("Exchange aggregator started successfully")
	return nil
}

// Stop gracefully stops the aggregator
func (a *ExchangeAggregator) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.isRunning {
		return
	}

	a.logger.Info("🛑 Stopping exchange aggregator...")
	close(a.stopChan)

	// Stop real-time aggregator first (flush pending data)
	if a.realtimeAgg != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := a.realtimeAgg.Stop(shutdownCtx); err != nil {
			a.logger.WithError(err).Warn("⚠️  Real-time aggregator stop with timeout")
		}
	}

	// Close all connections
	for _, ex := range a.exchanges {
		ex.mu.Lock()
		if ex.conn != nil {
			ex.conn.Close()
		}
		ex.mu.Unlock()
	}

	// Flush remaining batches
	a.flushBatch(context.Background())

	a.isRunning = false
	a.logger.Info("✅ Exchange aggregator stopped successfully")
}

// SubscribeToInterval adds interval to subscriptions with reference counting
// Multiple users watching the same symbol@interval share ONE websocket subscription
// This is how we scale to 1M+ concurrent users!
func (a *ExchangeAggregator) SubscribeToInterval(ctx context.Context, symbol, interval string) error {
	symbol = strings.ToUpper(symbol)

	// ⚡ OPTIMIZATION: Always subscribe to 1m interval on exchanges
	// All other intervals (3m, 5m, 15m, 30m, 1h, 2h, 4h, etc.) are derived from 1m candles
	// This reduces exchange websocket connections from 187 to just 91 (one per symbol)!
	baseInterval := "1m"
	streamKey := fmt.Sprintf("%s@%s", strings.ToLower(symbol), baseInterval)

	// Track subscription reference count for REQUESTED interval
	a.subscriptionsMu.Lock()
	if a.subscriptions[symbol] == nil {
		a.subscriptions[symbol] = make(map[string]int32)
	}
	a.subscriptions[symbol][interval]++
	refCount := a.subscriptions[symbol][interval]
	a.subscriptionsMu.Unlock()

	// Check if this is a NEW subscription (first user watching this symbol with ANY interval)
	a.activeStreamsMu.Lock()
	_, exists := a.activeStreams[streamKey]
	wasNew := !exists
	if wasNew {
		a.activeStreams[streamKey] = 1
	} else {
		a.activeStreams[streamKey]++
	}
	totalStreams := len(a.activeStreams)
	a.activeStreamsMu.Unlock()

	if wasNew {
		a.logger.Infof("📊 NEW subscription: %s 1m (derives %s and all other intervals, total active streams: %d)", symbol, interval, totalStreams)

		// Trigger reconnection for ALL exchanges (each exchange needs the signal)
		// Since all 6 exchanges share the same channel, we need to send 6 signals
		for i := 0; i < len(a.exchanges); i++ {
			select {
			case a.reconnectChan <- struct{}{}:
			default:
				// Channel full, reconnection already pending
			}
		}
	} else {
		a.logger.Debugf("📈 Shared subscription: %s %s (uses existing 1m stream, ref count: %d)", symbol, interval, refCount)
	}

	return nil
}

// UnsubscribeFromInterval removes a subscription reference
// When refCount reaches 0, the websocket subscription is removed
func (a *ExchangeAggregator) UnsubscribeFromInterval(ctx context.Context, symbol, interval string) error {
	symbol = strings.ToUpper(symbol)

	// ⚡ OPTIMIZATION: We only track 1m interval on exchanges
	baseInterval := "1m"
	streamKey := fmt.Sprintf("%s@%s", strings.ToLower(symbol), baseInterval)

	// Decrease reference count for REQUESTED interval
	a.subscriptionsMu.Lock()
	if a.subscriptions[symbol] != nil && a.subscriptions[symbol][interval] > 0 {
		a.subscriptions[symbol][interval]--
		if a.subscriptions[symbol][interval] == 0 {
			delete(a.subscriptions[symbol], interval)
			if len(a.subscriptions[symbol]) == 0 {
				delete(a.subscriptions, symbol)
			}
		}
	}
	a.subscriptionsMu.Unlock()

	// Decrease active stream count (only remove when NO intervals are subscribed for this symbol)
	a.activeStreamsMu.Lock()
	if count, exists := a.activeStreams[streamKey]; exists {
		count--
		if count <= 0 {
			// No more users watching ANY interval for this symbol, remove 1m subscription
			delete(a.activeStreams, streamKey)
			a.logger.Infof("🗑️  Removed subscription: %s 1m (no more users watching any interval)", symbol)

			// TODO: Send unsubscribe message to exchanges if needed
			// For now, we keep the connection but stop tracking
		} else {
			a.activeStreams[streamKey] = count
			a.logger.Debugf("📉 Unsubscribed: %s %s (1m stream still active, ref count: %d)", symbol, interval, count)
		}
	}
	a.activeStreamsMu.Unlock()

	return nil
}

// SubscribeToSymbol is an alias for backward compatibility
func (a *ExchangeAggregator) SubscribeToSymbol(ctx context.Context, symbol, interval string) error {
	return a.SubscribeToInterval(ctx, symbol, interval)
}

// TriggerReconnectAll triggers reconnection for all exchanges
func (a *ExchangeAggregator) TriggerReconnectAll() {
	for i := 0; i < len(a.exchanges); i++ {
		select {
		case a.reconnectChan <- struct{}{}:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// manageExchangeConnection manages a single exchange connection with circuit breaker
func (a *ExchangeAggregator) manageExchangeConnection(ctx context.Context, ex *Exchange) {
	ticker := time.NewTicker(30 * time.Second) // Reduced frequency - check every 30s instead of 10s
	defer ticker.Stop()

	for {
		select {
		case <-a.stopChan:
			return

		case <-a.reconnectChan:
			// Immediate reconnection trigger (from subscriptions)
			ex.mu.Lock()
			backoffUntil := ex.backoffUntil
			isConnecting := ex.isConnecting
			isHealthy := ex.isHealthy
			lastConnectTime := ex.lastConnectTime
			ex.mu.Unlock()

			// Prevent duplicate connection attempts
			if isConnecting {
				a.logger.Debugf("%s already connecting, skipping", ex.Name)
				continue
			}

			// If connected and healthy within last 5 seconds, skip
			if isHealthy && time.Since(lastConnectTime) < 5*time.Second {
				a.logger.Debugf("%s already connected, skipping", ex.Name)
				continue
			}

			if time.Now().Before(backoffUntil) {
				a.logger.Debugf("%s in backoff period, skipping", ex.Name)
				continue // Still in backoff period
			}

			// Attempt connection
			if err := a.connectExchange(ctx, ex); err != nil {
				a.handleConnectionError(ex, err)
				continue
			}

			// Listen for messages
			a.listenExchange(ctx, ex)

		case <-ticker.C:
			// Periodic health check/reconnection
			ex.mu.RLock()
			backoffUntil := ex.backoffUntil
			isHealthy := ex.isHealthy
			isConnecting := ex.isConnecting
			lastUpdate := ex.lastUpdate
			ex.mu.RUnlock()

			// If already connecting, skip
			if isConnecting {
				continue
			}

			// If healthy and receiving messages, skip
			if isHealthy && time.Since(lastUpdate) < 60*time.Second {
				continue
			}

			if time.Now().Before(backoffUntil) {
				continue // Still in backoff period
			}

			a.logger.Infof("%s health check triggered reconnection (last update: %v)", ex.Name, time.Since(lastUpdate))

			// Attempt connection
			if err := a.connectExchange(ctx, ex); err != nil {
				a.handleConnectionError(ex, err)
				continue
			}

			// Listen for messages
			a.listenExchange(ctx, ex)
		}
	}
}

// connectExchange establishes WebSocket connection to an exchange
func (a *ExchangeAggregator) connectExchange(ctx context.Context, ex *Exchange) error {
	ex.mu.Lock()

	// Mark as connecting
	ex.isConnecting = true
	ex.mu.Unlock()

	// Ensure we clear the connecting flag when done
	defer func() {
		ex.mu.Lock()
		ex.isConnecting = false
		ex.mu.Unlock()
	}()

	ex.mu.Lock()
	// Close existing connection
	if ex.conn != nil {
		ex.conn.Close()
		ex.conn = nil
	}
	ex.mu.Unlock()

	// Connect with proxy support (handles 403 errors)
	conn, proxyUsed, err := a.dialWithProxy(ex)
	if err != nil {
		return fmt.Errorf("failed to dial %s: %w", ex.Name, err)
	}

	// If we used a proxy successfully, mark it as working
	if proxyUsed != "" && a.proxyService != nil {
		a.proxyService.SetWorkingProxy(proxyUsed)
	}

	ex.mu.Lock()
	ex.conn = conn
	ex.isHealthy = true
	ex.lastUpdate = time.Now()
	ex.lastConnectTime = time.Now()
	ex.failureCount = 0
	ex.isSubscribed = false
	ex.mu.Unlock()

	a.logger.Infof("✅ %s WebSocket connected successfully (%s)", ex.Name, ex.WSUrl)

	// Get rate limiter for this exchange
	limiter, err := a.rateLimitMgr.GetLimiter(ex.Name)
	if err != nil {
		return fmt.Errorf("rate limiter not found for %s: %w", ex.Name, err)
	}

	// Wait for rate limiter before subscribing
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit wait failed for %s: %w", ex.Name, err)
	}

	// Subscribe to streams based on activeStreams and exchange
	var subErr error
	switch ex.Name {
	case "Binance":
		subErr = a.subscribeBinance(ex)
	case "Kraken":
		subErr = a.subscribeKraken(ex)
	case "Coinbase":
		subErr = a.subscribeCoinbase(ex)
	case "Bybit":
		subErr = a.subscribeBybit(ex)
	case "OKX":
		subErr = a.subscribeOKX(ex)
	case "Gate.io":
		subErr = a.subscribeGateIO(ex)
	}

	if subErr != nil {
		limiter.RecordRateLimitHit()
		ex.mu.Lock()
		ex.isHealthy = false
		ex.mu.Unlock()
		return fmt.Errorf("failed to subscribe %s: %w", ex.Name, subErr)
	}

	ex.mu.Lock()
	ex.isSubscribed = true
	ex.mu.Unlock()

	// Get stream count for logging
	a.activeStreamsMu.RLock()
	streamCount := len(a.activeStreams)
	a.activeStreamsMu.RUnlock()

	limiter.RecordSuccess()
	a.logger.Infof("✅ %s subscribed successfully (%d active streams)", ex.Name, streamCount)
	return nil
}

// dialWithProxy attempts to connect to exchange with proxy support
// Returns: (connection, proxyUsed, error)
func (a *ExchangeAggregator) dialWithProxy(ex *Exchange) (*websocket.Conn, string, error) {
	var proxies []string

	// Get proxy list (working proxy first)
	if a.proxyService != nil {
		proxies = a.proxyService.GetProxyListWithWorkingFirst()
		a.logger.Infof("🔌 Connecting to %s with %d proxies available", ex.Name, len(proxies))
	} else {
		proxies = []string{""} // Direct connection only
		a.logger.Infof("🔌 Connecting to %s (no proxy service)", ex.Name)
	}

	var lastErr error

	// Try each proxy
	for i, proxyURL := range proxies {
		proxyDesc := "direct"
		if proxyURL != "" {
			proxyDesc = proxyURL
		}

		a.logger.Infof("Attempt %d/%d: Trying %s with %s", i+1, len(proxies), ex.Name, proxyDesc)

		// Create dialer with proxy
		dialer := a.createDialerWithProxy(proxyURL)

		// Attempt connection
		conn, resp, err := dialer.Dial(ex.WSUrl, nil)

		if err == nil {
			// Success!
			a.logger.Infof("✅ %s connected successfully using %s", ex.Name, proxyDesc)
			return conn, proxyURL, nil
		}

		// Check for 403 Forbidden (blocked by exchange)
		if resp != nil && resp.StatusCode == 403 {
			a.logger.Warnf("❌ %s returned 403 Forbidden with %s (IP blocked)", ex.Name, proxyDesc)
			lastErr = fmt.Errorf("403 forbidden: %w", err)
			continue // Try next proxy
		}

		// Other errors
		a.logger.Warnf("❌ %s connection failed with %s: %v", ex.Name, proxyDesc, err)
		lastErr = err

		// Don't try more proxies for non-403 errors on first attempt
		if i == 0 && proxyURL == "" {
			break
		}
	}

	// All proxies failed
	if lastErr != nil {
		return nil, "", fmt.Errorf("all connection attempts failed: %w", lastErr)
	}

	return nil, "", fmt.Errorf("no proxies available")
}

// createDialerWithProxy creates a WebSocket dialer with optional proxy
func (a *ExchangeAggregator) createDialerWithProxy(proxyURL string) *websocket.Dialer {
	dialer := &websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // Required for SOCKS5 proxies
		},
	}

	if proxyURL != "" {
		parsedURL, err := url.Parse(proxyURL)
		if err == nil {
			dialer.Proxy = http.ProxyURL(parsedURL)
		} else {
			a.logger.Warnf("Invalid proxy URL %s: %v", proxyURL, err)
		}
	}

	return dialer
}

// subscribeBinance subscribes to Binance kline streams
func (a *ExchangeAggregator) subscribeBinance(ex *Exchange) error {
	a.activeStreamsMu.RLock()
	streams := make([]string, 0, len(a.activeStreams))
	for stream, refCount := range a.activeStreams {
		// Only subscribe to streams with active users
		if refCount > 0 {
			// Binance expects format: symbol@kline_interval (e.g., btcusdt@kline_1m)
			// activeStreams stores: symbol@interval (e.g., btcusdt@1m)
			// We need to convert to: symbol@kline_interval
			parts := strings.Split(stream, "@")
			if len(parts) == 2 {
				binanceStream := fmt.Sprintf("%s@kline_%s", strings.ToLower(parts[0]), parts[1])
				streams = append(streams, binanceStream)
			}
		}
	}
	a.activeStreamsMu.RUnlock()

	if len(streams) == 0 {
		a.logger.Warn("No active streams to subscribe to on Binance - waiting for subscriptions from API")
		return nil // No fallback - must have symbols from API
	}

	// Binance allows up to 1024 streams per connection, but we batch for safety
	// Limit: 5 requests per second, so we send max 200 streams per batch with delays
	const maxStreamsPerBatch = 200

	for i := 0; i < len(streams); i += maxStreamsPerBatch {
		end := i + maxStreamsPerBatch
		if end > len(streams) {
			end = len(streams)
		}
		batch := streams[i:end]

		subscribeMsg := map[string]interface{}{
			"method": "SUBSCRIBE",
			"params": batch,
			"id":     i/maxStreamsPerBatch + 1,
		}

		if err := ex.conn.WriteJSON(subscribeMsg); err != nil {
			a.logger.WithError(err).Warnf("Failed to subscribe to Binance batch %d", i/maxStreamsPerBatch)
			return err
		}

		a.logger.Infof("Subscribed to %d streams on Binance (batch %d/%d)", len(batch), i/maxStreamsPerBatch+1, (len(streams)+maxStreamsPerBatch-1)/maxStreamsPerBatch)

		// Add delay between batches to respect rate limits (5 req/sec = 200ms between requests)
		if end < len(streams) {
			time.Sleep(250 * time.Millisecond)
		}
	}

	return nil
}

// listenExchange listens for messages from an exchange
func (a *ExchangeAggregator) listenExchange(ctx context.Context, ex *Exchange) {
	// Start ping goroutine for exchanges that need it (OKX)
	if ex.Name == "OKX" {
		go a.sendPeriodicPing(ctx, ex)
	}

	for {
		select {
		case <-a.stopChan:
			return
		default:
		}

		ex.mu.RLock()
		conn := ex.conn
		ex.mu.RUnlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			a.handleConnectionError(ex, err)
			return
		}

		// Process message based on exchange
		switch ex.Name {
		case "Binance":
			a.processBinanceMessage(ex, message)
		case "Kraken":
			a.processKrakenMessage(ex, message)
		case "Coinbase":
			a.processCoinbaseMessage(ex, message)
		case "Bybit":
			a.processBybitMessage(ex, message)
		case "OKX":
			a.processOKXMessage(ex, message)
		case "Gate.io":
			a.processGateIOMessage(ex, message)
		default:
			a.logger.Debugf("Received message from %s (no handler)", ex.Name)
		}

		// Update stats
		atomic.AddInt64(&ex.msgCount, 1)
		ex.mu.Lock()
		ex.lastUpdate = time.Now()
		ex.mu.Unlock()
	}
}

// sendPeriodicPing sends periodic ping messages to keep connection alive
func (a *ExchangeAggregator) sendPeriodicPing(ctx context.Context, ex *Exchange) {
	ticker := time.NewTicker(20 * time.Second) // Send every 20s (OKX closes after 30s)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopChan:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			ex.mu.RLock()
			conn := ex.conn
			ex.mu.RUnlock()

			if conn == nil {
				return
			}

			// Send ping message (OKX format)
			pingMsg := map[string]string{"op": "ping"}
			if err := conn.WriteJSON(pingMsg); err != nil {
				a.logger.WithError(err).Debugf("%s ping failed", ex.Name)
				return
			}
			a.logger.Debugf("📡 Sent ping to %s", ex.Name)
		}
	}
}

// processBinanceMessage processes Binance kline message
func (a *ExchangeAggregator) processBinanceMessage(ex *Exchange, message []byte) {
	var msg BinanceKlineMessage
	if err := json.Unmarshal(message, &msg); err != nil {
		return // Ignore parse errors (ping/pong messages, etc.)
	}

	if msg.EventType != "kline" {
		return
	}

	// Convert to our candle model
	candle := &models.Candle{
		Symbol:       msg.Symbol,
		Interval:     msg.Kline.Interval,
		OpenTime:     time.UnixMilli(msg.Kline.StartTime),
		CloseTime:    time.UnixMilli(msg.Kline.CloseTime),
		IsClosed:     msg.Kline.IsKlineClosed,
		Source:       "binance",
		ContractType: "spot",
		TradeCount:   msg.Kline.NumberOfTrades,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	// Parse decimal values
	candle.Open, _ = decimal.NewFromString(msg.Kline.Open)
	candle.High, _ = decimal.NewFromString(msg.Kline.High)
	candle.Low, _ = decimal.NewFromString(msg.Kline.Low)
	candle.Close, _ = decimal.NewFromString(msg.Kline.Close)
	candle.Volume, _ = decimal.NewFromString(msg.Kline.BaseAssetVolume)
	candle.QuoteVolume, _ = decimal.NewFromString(msg.Kline.QuoteAssetVolume)
	candle.TakerBuyBaseVolume, _ = decimal.NewFromString(msg.Kline.TakerBuyBaseVolume)
	candle.TakerBuyQuoteVolume, _ = decimal.NewFromString(msg.Kline.TakerBuyQuoteVolume)

	// ⚡ Use real-time aggregator (zero-delay, lock-free)
	a.realtimeAgg.ProcessCandleUpdate("Binance", candle)
}

// updateAggregatedPrice is DEPRECATED - kept for compatibility
// Real-time aggregator now handles all price updates
func (a *ExchangeAggregator) updateAggregatedPrice(symbol, exchange string, price decimal.Decimal) {
	// No-op: realtimeAgg handles this now
}

// GetAggregatedPrice calculates average price from all exchanges
func (a *ExchangeAggregator) GetAggregatedPrice(symbol string) (decimal.Decimal, int) {
	// ⚡ NEW: Use zero-delay real-time aggregator first (lock-free O(1))
	if price, count := a.realtimeAgg.GetAggregatedPrice(symbol); count > 0 {
		return price, count
	}

	// ⚡ FALLBACK: Use old system for compatibility during migration
	priceMapInterface, ok := a.aggregatedPrices.Load(symbol)
	if !ok {
		return decimal.Zero, 0
	}

	priceMap := priceMapInterface.(*sync.Map)

	var sum decimal.Decimal
	count := 0

	priceMap.Range(func(_, value interface{}) bool {
		price := value.(decimal.Decimal)
		sum = sum.Add(price)
		count++
		return true
	})

	if count == 0 {
		return decimal.Zero, 0
	}

	avg := sum.Div(decimal.NewFromInt(int64(count)))
	return avg, count
}

// aggregateCandles aggregates candles from multiple exchanges for a specific timestamp
// This is used for real-time publishing and accepts even 1 exchange's data
func (a *ExchangeAggregator) aggregateCandles(symbol, interval string, candles map[string]*models.Candle, timestamp int64) *models.Candle {
	if candles == nil || len(candles) == 0 {
		return nil
	}

	var totalOpen, totalClose, totalVolume, totalQuoteVolume decimal.Decimal
	var maxHigh, minLow decimal.Decimal
	var totalTradeCount int
	validCount := 0
	firstCandle := true
	var closeTime time.Time
	var openTime time.Time

	for _, candle := range candles {
		if candle.Open.IsZero() && candle.Close.IsZero() {
			continue
		}

		validCount++

		if firstCandle || candle.High.GreaterThan(maxHigh) {
			maxHigh = candle.High
		}

		if firstCandle || candle.Low.LessThan(minLow) {
			minLow = candle.Low
		}

		totalOpen = totalOpen.Add(candle.Open)
		totalClose = totalClose.Add(candle.Close)
		totalVolume = totalVolume.Add(candle.Volume)
		totalQuoteVolume = totalQuoteVolume.Add(candle.QuoteVolume)
		totalTradeCount += candle.TradeCount

		if firstCandle {
			closeTime = candle.CloseTime
			openTime = candle.OpenTime
			firstCandle = false
		}
	}

	if validCount == 0 {
		return nil
	}

	// Calculate averages
	validCountDecimal := decimal.NewFromInt(int64(validCount))
	avgOpen := totalOpen.Div(validCountDecimal)
	avgClose := totalClose.Div(validCountDecimal)

	// Candle is considered closed if we have data from 3+ exchanges and they all report it as closed
	allClosed := true
	closedCount := 0
	for _, candle := range candles {
		if candle.IsClosed {
			closedCount++
		} else {
			allClosed = false
		}
	}
	isClosed := allClosed && validCount >= 3

	// Return aggregated candle
	return &models.Candle{
		Symbol:       symbol,
		Interval:     interval,
		OpenTime:     openTime,
		CloseTime:    closeTime,
		Open:         avgOpen,
		High:         maxHigh,
		Low:          minLow,
		Close:        avgClose,
		Volume:       totalVolume,
		QuoteVolume:  totalQuoteVolume,
		TradeCount:   totalTradeCount,
		Source:       "aggregated",
		IsClosed:     isClosed,
		ContractType: "spot",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
}

// GetLatestInMemoryCandle gets the most recent candle from in-memory pending candles
func (a *ExchangeAggregator) GetLatestInMemoryCandle(symbol, interval string) *models.Candle {
	// ⚡ NEW: Use zero-delay real-time aggregator first (lock-free O(1))
	if candle := a.realtimeAgg.GetLatestCandle(symbol, interval); candle != nil {
		return candle
	}

	// ⚡ FALLBACK: Use old system for compatibility during migration
	a.pendingCandlesMu.RLock()
	defer a.pendingCandlesMu.RUnlock()

	if a.pendingCandles[symbol] == nil || a.pendingCandles[symbol][interval] == nil {
		return nil
	}

	// Get the ABSOLUTE latest candle with ANY data
	var latestTimestamp int64
	var latestCandles map[string]*models.Candle

	for timestamp, candles := range a.pendingCandles[symbol][interval] {
		if timestamp > latestTimestamp {
			latestTimestamp = timestamp
			latestCandles = candles
		}
	}

	if latestCandles == nil || len(latestCandles) == 0 {
		return nil
	}

	selectedCandles := latestCandles

	// Aggregate candles from all exchanges for this timestamp
	var totalOpen, totalClose, totalVolume, totalQuoteVolume decimal.Decimal
	var maxHigh, minLow decimal.Decimal
	var totalTradeCount int
	validCount := 0
	firstCandle := true
	var closeTime time.Time
	var openTime time.Time

	for _, candle := range selectedCandles {
		if candle.Open.IsZero() && candle.Close.IsZero() {
			continue
		}

		validCount++

		if firstCandle || candle.High.GreaterThan(maxHigh) {
			maxHigh = candle.High
		}

		if firstCandle || candle.Low.LessThan(minLow) {
			minLow = candle.Low
		}

		totalOpen = totalOpen.Add(candle.Open)
		totalClose = totalClose.Add(candle.Close)
		totalVolume = totalVolume.Add(candle.Volume)
		totalQuoteVolume = totalQuoteVolume.Add(candle.QuoteVolume)
		totalTradeCount += candle.TradeCount

		if firstCandle {
			closeTime = candle.CloseTime
			openTime = candle.OpenTime
			firstCandle = false
		}
	}

	if validCount == 0 {
		return nil
	}

	// Calculate averages
	validCountDecimal := decimal.NewFromInt(int64(validCount))
	avgOpen := totalOpen.Div(validCountDecimal)
	avgClose := totalClose.Div(validCountDecimal)

	// ⚡ REAL-TIME FIX: Candle is closed if ANY exchange reports it as closed
	// No longer require 3+ exchanges
	isClosed := false
	for _, candle := range selectedCandles {
		if candle.IsClosed {
			isClosed = true
			break
		}
	}

	// Return aggregated in-memory candle
	return &models.Candle{
		Symbol:       symbol,
		Interval:     interval,
		OpenTime:     openTime,
		CloseTime:    closeTime,
		Open:         avgOpen,
		High:         maxHigh,
		Low:          minLow,
		Close:        avgClose,
		Volume:       totalVolume,
		QuoteVolume:  totalQuoteVolume,
		TradeCount:   validCount,
		Source:       "aggregated",
		IsClosed:     isClosed,
		ContractType: "spot",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
}

// addPendingCandle is DEPRECATED - kept for compatibility
// Real-time aggregator now handles all candle aggregation
func (a *ExchangeAggregator) addPendingCandle(exchange, symbol, interval string, candle *models.Candle) {
	// No-op: realtimeAgg handles this now
}

// aggregatePendingCandles is DEPRECATED - kept for compatibility
// Real-time aggregator now handles all candle aggregation
func (a *ExchangeAggregator) aggregatePendingCandles(symbol, interval string, openTime time.Time) {
	// No-op: realtimeAgg handles this now
}

// batchWriterWorker processes batches of candles
func (a *ExchangeAggregator) batchWriterWorker(ctx context.Context, workerID int) {
	ticker := time.NewTicker(a.config.Service.BatchWriteInterval)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopChan:
			return

		case candle := <-a.candleBatchChan:
			a.batchMu.Lock()
			a.currentBatch = append(a.currentBatch, candle)
			shouldFlush := len(a.currentBatch) >= a.config.Service.BatchWriteSize
			a.batchMu.Unlock()

			if shouldFlush {
				a.flushBatch(ctx)
			}

		case <-ticker.C:
			a.flushBatch(ctx)
		}
	}
}

// flushBatch writes accumulated candles to database
func (a *ExchangeAggregator) flushBatch(ctx context.Context) {
	a.batchMu.Lock()
	if len(a.currentBatch) == 0 {
		a.batchMu.Unlock()
		return
	}

	batch := a.currentBatch
	a.currentBatch = make([]*models.Candle, 0, a.config.Service.BatchWriteSize)
	a.batchMu.Unlock()

	// Write to database
	if err := a.candleSvc.BatchCreateCandles(ctx, batch); err != nil {
		a.logger.WithError(err).Error("Failed to batch write candles")
		return
	}

	a.logger.Debugf("Flushed batch of %d candles", len(batch))
}

// monitorPriceAggregation logs price aggregation stats
func (a *ExchangeAggregator) monitorPriceAggregation(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopChan:
			return
		case <-ticker.C:
			count := atomic.LoadInt64(&a.priceUpdateCount)
			symbolCount := 0
			a.aggregatedPrices.Range(func(_, _ interface{}) bool {
				symbolCount++
				return true
			})

			a.logger.Infof("Aggregator stats: %d price updates, %d symbols tracked", count, symbolCount)
		}
	}
}

// handleConnectionError handles exchange connection errors with circuit breaker
func (a *ExchangeAggregator) handleConnectionError(ex *Exchange, err error) {
	ex.mu.Lock()
	defer ex.mu.Unlock()

	ex.isHealthy = false
	ex.errorCount++
	ex.failureCount++
	ex.lastFailureTime = time.Now()

	// Exponential backoff
	backoffDuration := time.Duration(ex.failureCount*ex.failureCount) * time.Second
	if backoffDuration > 60*time.Second {
		backoffDuration = 60 * time.Second
	}
	ex.backoffUntil = time.Now().Add(backoffDuration)

	a.logger.WithError(err).Warnf("%s connection error (backoff: %v)", ex.Name, backoffDuration)
}

// GetExchangeStats returns statistics for all exchanges
func (a *ExchangeAggregator) GetExchangeStats() map[string]map[string]interface{} {
	stats := make(map[string]map[string]interface{})

	for _, ex := range a.exchanges {
		ex.mu.RLock()
		exStats := map[string]interface{}{
			"is_healthy":  ex.isHealthy,
			"msg_count":   ex.msgCount,
			"error_count": ex.errorCount,
			"last_update": ex.lastUpdate,
		}
		ex.mu.RUnlock()

		// Add rate limiter stats
		if limiter, err := a.rateLimitMgr.GetLimiter(ex.Name); err == nil {
			rateLimitStats := limiter.GetStats()
			for k, v := range rateLimitStats {
				exStats[k] = v
			}
		}

		stats[ex.Name] = exStats
	}

	return stats
}

// processBatchedSubscriptions processes batched subscription requests
func (a *ExchangeAggregator) processBatchedSubscriptions(reqs []*SubscriptionRequest) {
	if len(reqs) == 0 {
		return
	}

	// Group by exchange
	byExchange := make(map[string][]*SubscriptionRequest)
	for _, req := range reqs {
		byExchange[req.Exchange] = append(byExchange[req.Exchange], req)
	}

	// Process each exchange's subscriptions
	for exchange, exchangeReqs := range byExchange {
		// Wait for rate limiter
		limiter, err := a.rateLimitMgr.GetLimiter(exchange)
		if err != nil {
			a.logger.WithError(err).Warnf("Rate limiter not found for %s", exchange)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := limiter.Wait(ctx); err != nil {
			cancel()
			a.logger.WithError(err).Warnf("Rate limit wait failed for %s", exchange)
			limiter.RecordRateLimitHit()
			continue
		}
		cancel()

		// Find the exchange
		var ex *Exchange
		for _, e := range a.exchanges {
			if e.Name == exchange {
				ex = e
				break
			}
		}

		if ex == nil || ex.conn == nil {
			continue
		}

		// Subscribe based on exchange
		var subErr error
		switch exchange {
		case "Binance":
			subErr = a.subscribeBinanceWithLimiter(ex, limiter)
		case "Kraken":
			subErr = a.subscribeKrakenWithLimiter(ex, limiter)
		case "Coinbase":
			subErr = a.subscribeCoinbaseWithLimiter(ex, limiter)
		case "Bybit":
			subErr = a.subscribeBybitWithLimiter(ex, limiter)
		case "OKX":
			subErr = a.subscribeOKXWithLimiter(ex, limiter)
		case "Gate.io":
			subErr = a.subscribeGateIOWithLimiter(ex, limiter)
		}

		if subErr != nil {
			a.logger.WithError(subErr).Warnf("Failed to subscribe to %s", exchange)
			limiter.RecordRateLimitHit()

			// Call error callbacks
			for _, req := range exchangeReqs {
				if req.Callback != nil {
					req.Callback(subErr)
				}
			}
		} else {
			limiter.RecordSuccess()

			// Call success callbacks
			for _, req := range exchangeReqs {
				if req.Callback != nil {
					req.Callback(nil)
				}
			}
		}
	}
}

// monitorRateLimiters monitors rate limiter statistics
func (a *ExchangeAggregator) monitorRateLimiters(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopChan:
			return
		case <-ticker.C:
			a.logger.Info("Rate Limiter Stats:")
			for _, ex := range a.exchanges {
				if !ex.IsEnabled {
					continue
				}

				limiter, err := a.rateLimitMgr.GetLimiter(ex.Name)
				if err != nil {
					continue
				}

				stats := limiter.GetStats()
				a.logger.Infof("  %s: requests=%v, rate_limit_hits=%v, backoff=%vms, queue=%v",
					ex.Name,
					stats["request_count"],
					stats["rate_limit_hits"],
					stats["current_backoff_ms"],
					stats["queue_size"],
				)
			}
		}
	}
}

// subscribeBinanceWithLimiter subscribes to Binance with rate limit enforcement
func (a *ExchangeAggregator) subscribeBinanceWithLimiter(ex *Exchange, limiter *ExchangeRateLimiter) error {
	// Check rate limit before subscribing
	if !limiter.Allow() {
		return fmt.Errorf("rate limit exceeded for %s", ex.Name)
	}

	return a.subscribeBinance(ex)
}

// subscribeKrakenWithLimiter subscribes to Kraken with rate limit enforcement
func (a *ExchangeAggregator) subscribeKrakenWithLimiter(ex *Exchange, limiter *ExchangeRateLimiter) error {
	if !limiter.Allow() {
		return fmt.Errorf("rate limit exceeded for %s", ex.Name)
	}

	return a.subscribeKraken(ex)
}

// subscribeCoinbaseWithLimiter subscribes to Coinbase with rate limit enforcement
func (a *ExchangeAggregator) subscribeCoinbaseWithLimiter(ex *Exchange, limiter *ExchangeRateLimiter) error {
	if !limiter.Allow() {
		return fmt.Errorf("rate limit exceeded for %s", ex.Name)
	}

	return a.subscribeCoinbase(ex)
}

// subscribeBybitWithLimiter subscribes to Bybit with rate limit enforcement
func (a *ExchangeAggregator) subscribeBybitWithLimiter(ex *Exchange, limiter *ExchangeRateLimiter) error {
	if !limiter.Allow() {
		return fmt.Errorf("rate limit exceeded for %s", ex.Name)
	}

	return a.subscribeBybit(ex)
}

// subscribeOKXWithLimiter subscribes to OKX with rate limit enforcement
func (a *ExchangeAggregator) subscribeOKXWithLimiter(ex *Exchange, limiter *ExchangeRateLimiter) error {
	if !limiter.Allow() {
		return fmt.Errorf("rate limit exceeded for %s", ex.Name)
	}

	return a.subscribeOKX(ex)
}

// subscribeGateIOWithLimiter subscribes to Gate.io with rate limit enforcement
func (a *ExchangeAggregator) subscribeGateIOWithLimiter(ex *Exchange, limiter *ExchangeRateLimiter) error {
	if !limiter.Allow() {
		return fmt.Errorf("rate limit exceeded for %s", ex.Name)
	}

	return a.subscribeGateIO(ex)
}

// SubscribePrice subscribes to real-time price updates for a symbol
// Delegates to the realtime aggregator for direct callback subscription
func (a *ExchangeAggregator) SubscribePrice(symbol string, callback func(symbol string, price decimal.Decimal, exchangeCount int)) {
	if a.realtimeAgg != nil {
		a.realtimeAgg.SubscribePrice(symbol, callback)
	}
}

// SubscribeCandle subscribes to real-time candle updates for a symbol+interval
// Delegates to the realtime aggregator for direct callback subscription
func (a *ExchangeAggregator) SubscribeCandle(symbol, interval string, callback func(candle *models.Candle)) {
	if a.realtimeAgg != nil {
		a.realtimeAgg.SubscribeCandle(symbol, interval, callback)
	}
}
