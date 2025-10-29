package aggregator

import (
	"context"
	"encoding/json"
	"fmt"
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

// Exchange represents a single exchange WebSocket connection
type Exchange struct {
	Name         string
	WSUrl        string
	IsEnabled    bool
	conn         *websocket.Conn
	isHealthy    bool
	msgCount     int64
	errorCount   int64
	lastUpdate   time.Time
	mu           sync.RWMutex

	// Circuit breaker
	failureCount    int64
	lastFailureTime time.Time
	backoffUntil    time.Time
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
	config        *config.Config
	candleSvc     *candleService.Service
	pubsub        *pubsub.Publisher
	logger        *logrus.Logger

	// Exchanges
	exchanges     []*Exchange

	// Active subscriptions
	activeStreams   map[string]bool
	activeStreamsMu sync.RWMutex

	// Price aggregation (lock-free)
	aggregatedPrices sync.Map // symbol -> *sync.Map (exchange->price)

	// Batch processing
	candleBatchChan chan *models.Candle
	batchMu         sync.Mutex
	currentBatch    []*models.Candle
	batchTicker     *time.Ticker

	// Rate limiters
	rateLimiters map[string]*rate.Limiter

	// ⚡ IN-MEMORY AGGREGATION: Collect candles from all exchanges before writing
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

	return &ExchangeAggregator{
		config:          cfg,
		candleSvc:       candleSvc,
		pubsub:          pubsub,
		logger:          logger,
		exchanges:       exchanges,
		activeStreams:   make(map[string]bool),
		candleBatchChan: make(chan *models.Candle, 10000),
		currentBatch:    make([]*models.Candle, 0, cfg.Service.BatchWriteSize),
		rateLimiters: map[string]*rate.Limiter{
			"Binance":  rate.NewLimiter(rate.Limit(4.5), 10),
			"Kraken":   rate.NewLimiter(rate.Limit(10), 20),
			"Coinbase": rate.NewLimiter(rate.Limit(8), 15),
			"Bybit":    rate.NewLimiter(rate.Limit(50), 100),
			"OKX":      rate.NewLimiter(rate.Limit(3), 5),
			"Gate.io":  rate.NewLimiter(rate.Limit(30), 50),
		},
		pendingCandles:        make(map[string]map[string]map[int64]map[string]*models.Candle),
		scheduledAggregations: make(map[string]map[string]map[int64]bool),
		stopChan:              make(chan struct{}),
		reconnectChan:         make(chan struct{}, 10), // Buffered for all exchanges
		lastLogTime:           time.Now(),
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

	// Start exchange connections
	for _, ex := range a.exchanges {
		if ex.IsEnabled {
			go a.manageExchangeConnection(ctx, ex)
			a.logger.Infof("Starting %s WebSocket connection", ex.Name)
		} else {
			a.logger.Infof("Skipping %s (disabled in config)", ex.Name)
		}
	}

	// Start price aggregation monitor
	go a.monitorPriceAggregation(ctx)

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

	a.logger.Info("Stopping exchange aggregator...")
	close(a.stopChan)

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
	a.logger.Info("Exchange aggregator stopped")
}

// SubscribeToSymbol subscribes to a symbol on all enabled exchanges
func (a *ExchangeAggregator) SubscribeToSymbol(ctx context.Context, symbol, interval string) error {
	symbol = strings.ToUpper(symbol)
	streamKey := fmt.Sprintf("%s@%s", strings.ToLower(symbol), interval)

	a.activeStreamsMu.Lock()
	a.activeStreams[streamKey] = true
	a.activeStreamsMu.Unlock()

	a.logger.Infof("Subscribed to %s %s on all exchanges", symbol, interval)
	return nil
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
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopChan:
			return

		case <-a.reconnectChan:
			// Immediate reconnection trigger (from subscriptions)
			ex.mu.RLock()
			backoffUntil := ex.backoffUntil
			ex.mu.RUnlock()

			if time.Now().Before(backoffUntil) {
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
			// Periodic check/reconnection
			ex.mu.RLock()
			backoffUntil := ex.backoffUntil
			ex.mu.RUnlock()

			if time.Now().Before(backoffUntil) {
				continue // Still in backoff period
			}

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
	defer ex.mu.Unlock()

	// Close existing connection
	if ex.conn != nil {
		ex.conn.Close()
		ex.conn = nil
	}

	// Connect with timeout
	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	conn, _, err := dialer.Dial(ex.WSUrl, nil)
	if err != nil {
		return fmt.Errorf("failed to dial %s: %w", ex.Name, err)
	}

	ex.conn = conn
	ex.isHealthy = true
	ex.lastUpdate = time.Now()
	ex.failureCount = 0

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
		return fmt.Errorf("failed to subscribe %s: %w", ex.Name, subErr)
	}

	a.logger.Infof("%s connected successfully", ex.Name)
	return nil
}

// subscribeBinance subscribes to Binance kline streams
func (a *ExchangeAggregator) subscribeBinance(ex *Exchange) error {
	a.activeStreamsMu.RLock()
	streams := make([]string, 0, len(a.activeStreams))
	for stream := range a.activeStreams {
		streams = append(streams, stream)
	}
	a.activeStreamsMu.RUnlock()

	if len(streams) == 0 {
		// Subscribe to default popular pairs
		streams = []string{
			"btcusdt@kline_1m", "ethusdt@kline_1m", "bnbusdt@kline_1m",
			"solusdt@kline_1m", "xrpusdt@kline_1m",
		}
	}

	subscribeMsg := map[string]interface{}{
		"method": "SUBSCRIBE",
		"params": streams,
		"id":     1,
	}

	return ex.conn.WriteJSON(subscribeMsg)
}

// listenExchange listens for messages from an exchange
func (a *ExchangeAggregator) listenExchange(ctx context.Context, ex *Exchange) {
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

	// Update aggregated price
	a.updateAggregatedPrice(msg.Symbol, "Binance", candle.Close)

	// Add to pending candles for aggregation (NOT direct write to DB)
	a.addPendingCandle("Binance", msg.Symbol, msg.Kline.Interval, candle)
}

// updateAggregatedPrice updates price aggregation from an exchange
func (a *ExchangeAggregator) updateAggregatedPrice(symbol, exchange string, price decimal.Decimal) {
	// Get or create exchange price map for this symbol
	priceMapInterface, _ := a.aggregatedPrices.LoadOrStore(symbol, &sync.Map{})
	priceMap := priceMapInterface.(*sync.Map)

	// Store exchange price
	priceMap.Store(exchange, price)
}

// GetAggregatedPrice calculates average price from all exchanges
func (a *ExchangeAggregator) GetAggregatedPrice(symbol string) (decimal.Decimal, int) {
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

// addPendingCandle adds a candle from an exchange to the in-memory buffer
// This does NOT save to database - candles are aggregated first, then saved
func (a *ExchangeAggregator) addPendingCandle(exchange, symbol, interval string, candle *models.Candle) {
	// Truncate openTime to interval boundary to ensure all exchanges align
	truncatedOpenTime := models.TruncateToInterval(candle.OpenTime, interval)
	openTimeUnix := truncatedOpenTime.Unix()

	a.pendingCandlesMu.Lock()

	// Initialize nested maps if needed
	if a.pendingCandles[symbol] == nil {
		a.pendingCandles[symbol] = make(map[string]map[int64]map[string]*models.Candle)
	}
	if a.pendingCandles[symbol][interval] == nil {
		a.pendingCandles[symbol][interval] = make(map[int64]map[string]*models.Candle)
	}
	if a.pendingCandles[symbol][interval][openTimeUnix] == nil {
		a.pendingCandles[symbol][interval][openTimeUnix] = make(map[string]*models.Candle)
	}

	// Store or update the candle for this exchange
	a.pendingCandles[symbol][interval][openTimeUnix][exchange] = candle

	var shouldSchedule bool
	if candle.IsClosed {
		a.scheduledAggregationsMu.Lock()

		// Initialize nested maps if needed
		if a.scheduledAggregations[symbol] == nil {
			a.scheduledAggregations[symbol] = make(map[string]map[int64]bool)
		}
		if a.scheduledAggregations[symbol][interval] == nil {
			a.scheduledAggregations[symbol][interval] = make(map[int64]bool)
		}

		// Check if aggregation already scheduled
		alreadyScheduled := a.scheduledAggregations[symbol][interval][openTimeUnix]
		if !alreadyScheduled {
			a.scheduledAggregations[symbol][interval][openTimeUnix] = true
			shouldSchedule = true
		}

		a.scheduledAggregationsMu.Unlock()
	}

	a.pendingCandlesMu.Unlock()

	// Schedule aggregation OUTSIDE the lock to prevent deadlock
	if shouldSchedule {
		go func() {
			// Wait 500ms for other exchanges to send their candles
			time.Sleep(500 * time.Millisecond)
			a.aggregatePendingCandles(symbol, interval, truncatedOpenTime)
		}()
	}
}

// aggregatePendingCandles combines candles from all exchanges into ONE aggregated candle
func (a *ExchangeAggregator) aggregatePendingCandles(symbol, interval string, openTime time.Time) {
	openTimeUnix := openTime.Unix()

	a.pendingCandlesMu.Lock()

	// Get all candles for this symbol/interval/timestamp
	if a.pendingCandles[symbol] == nil ||
		a.pendingCandles[symbol][interval] == nil ||
		a.pendingCandles[symbol][interval][openTimeUnix] == nil {
		a.pendingCandlesMu.Unlock()
		return
	}

	candles := a.pendingCandles[symbol][interval][openTimeUnix]

	// Copy candles to avoid holding lock during processing
	candleCopy := make(map[string]*models.Candle)
	for exchange, candle := range candles {
		candleCopy[exchange] = candle
	}

	// Delete from pending to free memory
	delete(a.pendingCandles[symbol][interval], openTimeUnix)

	a.pendingCandlesMu.Unlock()

	// Clear scheduled flag
	a.scheduledAggregationsMu.Lock()
	if a.scheduledAggregations[symbol] != nil &&
		a.scheduledAggregations[symbol][interval] != nil {
		delete(a.scheduledAggregations[symbol][interval], openTimeUnix)
	}
	a.scheduledAggregationsMu.Unlock()

	// Need at least one candle to aggregate
	if len(candleCopy) == 0 {
		return
	}

	// Aggregate the candles using the formula:
	// - Open: Average of all exchange opens
	// - Close: Average of all exchange closes
	// - High: Maximum of all exchange highs
	// - Low: Minimum of all exchange lows (or zero initially)
	// - Volume: SUM of all exchange volumes (true market volume!)
	// - QuoteVolume: SUM of all quote volumes

	var totalOpen, totalClose, totalVolume, totalQuoteVolume decimal.Decimal
	var maxHigh, minLow decimal.Decimal
	var totalTradeCount int
	validCount := 0

	firstCandle := true
	var closeTime time.Time

	for _, candle := range candleCopy {
		// Validate candle data
		if candle.Open.IsZero() && candle.Close.IsZero() {
			continue // Skip invalid candles
		}

		validCount++

		// Calculate max high
		if firstCandle || candle.High.GreaterThan(maxHigh) {
			maxHigh = candle.High
		}

		// Calculate min low
		if firstCandle || candle.Low.LessThan(minLow) {
			minLow = candle.Low
		}

		// Sum for averaging
		totalOpen = totalOpen.Add(candle.Open)
		totalClose = totalClose.Add(candle.Close)

		// Sum volumes (true market volume across exchanges)
		totalVolume = totalVolume.Add(candle.Volume)
		totalQuoteVolume = totalQuoteVolume.Add(candle.QuoteVolume)

		// Sum trade counts
		totalTradeCount += candle.TradeCount

		// Use the close time from the first candle
		if firstCandle {
			closeTime = candle.CloseTime
			firstCandle = false
		}
	}

	// Need at least one valid candle
	if validCount == 0 {
		a.logger.Warnf("No valid candles to aggregate for %s %s at %v", symbol, interval, openTime)
		return
	}

	// Calculate averages
	validCountDecimal := decimal.NewFromInt(int64(validCount))
	avgOpen := totalOpen.Div(validCountDecimal)
	avgClose := totalClose.Div(validCountDecimal)

	// Create aggregated candle with source="aggregated"
	aggregatedCandle := &models.Candle{
		Symbol:       symbol,
		Interval:     interval,
		OpenTime:     openTime,
		CloseTime:    closeTime,
		Open:         avgOpen,          // Average open price
		High:         maxHigh,          // Maximum high price
		Low:          minLow,           // Minimum low price
		Close:        avgClose,         // Average close price
		Volume:       totalVolume,      // SUM of all volumes (true market volume)
		QuoteVolume:  totalQuoteVolume, // SUM of all quote volumes
		TradeCount:   validCount,       // Number of exchanges aggregated
		Source:       "aggregated",     // Mark as aggregated (NOT individual exchange)
		IsClosed:     true,
		ContractType: "spot",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	// Send ONLY the aggregated candle to batch writer (not individual exchange candles)
	select {
	case a.candleBatchChan <- aggregatedCandle:
		a.logger.Debugf("Aggregated candle from %d exchanges: %s %s at %v", validCount, symbol, interval, openTime)
	default:
		a.logger.Warn("Candle batch channel full, dropping aggregated candle")
	}

	// Increment counter for the aggregated candle
	atomic.AddInt64(&a.priceUpdateCount, 1)
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
		stats[ex.Name] = map[string]interface{}{
			"is_healthy":  ex.isHealthy,
			"msg_count":   ex.msgCount,
			"error_count": ex.errorCount,
			"last_update": ex.lastUpdate,
		}
		ex.mu.RUnlock()
	}

	return stats
}
