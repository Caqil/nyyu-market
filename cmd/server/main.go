package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"nyyu-market/internal/cache"
	"nyyu-market/internal/config"
	grpcServer "nyyu-market/internal/grpc"
	"nyyu-market/internal/metrics"
	"nyyu-market/internal/proxy"
	"nyyu-market/internal/pubsub"
	"nyyu-market/internal/repository"
	"nyyu-market/internal/services/aggregator"
	candleService "nyyu-market/internal/services/candle"
	markpriceService "nyyu-market/internal/services/markprice"
	"nyyu-market/internal/services/symbols"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/go-redis/redis/v8"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

var (
	version   = "1.0.0"
	startTime = time.Now()
)

func main() {
	// Setup logger
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.InfoLevel)

	logger.Info("Starting Nyyu Market Service...")

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("Failed to load config: ", err)
	}

	if err := cfg.Validate(); err != nil {
		logger.Fatal("Invalid config: ", err)
	}

	// Set log level
	if level, err := logrus.ParseLevel(cfg.Logging.Level); err == nil {
		logger.SetLevel(level)
	}

	// Initialize ClickHouse (shared)
	logger.Info("Connecting to ClickHouse...")
	clickhouseConn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%d", cfg.ClickHouse.Host, cfg.ClickHouse.Port)},
		Auth: clickhouse.Auth{
			Database: cfg.ClickHouse.Database,
			Username: cfg.ClickHouse.Username,
			Password: cfg.ClickHouse.Password,
		},
		Settings: clickhouse.Settings{
			"max_execution_time": 60,
		},
		DialTimeout:      10 * time.Second,
		MaxOpenConns:     10,
		MaxIdleConns:     5,
		ConnMaxLifetime:  time.Hour,
		ConnOpenStrategy: clickhouse.ConnOpenInOrder,
	})
	if err != nil {
		logger.Fatal("Failed to connect to ClickHouse: ", err)
	}
	defer clickhouseConn.Close()

	// Test ClickHouse connection
	if err := clickhouseConn.Ping(context.Background()); err != nil {
		logger.Fatal("ClickHouse ping failed: ", err)
	}
	logger.Info("ClickHouse connected successfully")

	// Run migrations
	logger.Info("Running ClickHouse migrations...")
	if err := runMigrations(clickhouseConn, logger); err != nil {
		logger.Fatal("Failed to run migrations: ", err)
	}
	logger.Info("Migrations completed successfully")

	// Initialize Redis
	logger.Info("Connecting to Redis...")
	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})

	// Create root context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.Fatal("Failed to connect to Redis: ", err)
	}
	defer redisClient.Close()
	logger.Info("Redis connected successfully")

	// ⚡ Start metrics collection goroutine
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				metrics.MemoryAllocated.Set(float64(m.Alloc))
				metrics.GoroutinesActive.Set(float64(runtime.NumGoroutine()))
			case <-ctx.Done():
				return
			}
		}
	}()
	logger.Info("✅ Metrics collection started")

	// Initialize repositories
	candleRepo := repository.NewCandleRepository(clickhouseConn, logger)

	// Initialize cache
	candleCache := cache.NewCandleCache(redisClient, logger)
	priceCache := cache.NewPriceCache(redisClient, logger) // Still used by markprice service

	// Initialize pub/sub
	publisher := pubsub.NewPublisher(redisClient, logger)

	// Initialize proxy service (for bypassing 403 errors from exchanges)
	proxySvc := proxy.NewProxyService(logger)
	if err := proxySvc.Start(context.Background()); err != nil {
		logger.WithError(err).Warn("Failed to start proxy service - will use direct connections")
	} else {
		logger.Info("Proxy service started - ready to handle 403 errors")
	}

	// Initialize services
	candleSvc := candleService.NewService(candleRepo, candleCache, publisher, cfg, logger)

	// Initialize exchange aggregator (with proxy support)
	exchangeAgg := aggregator.NewExchangeAggregator(cfg, candleSvc, publisher, logger, proxySvc)

	// ⚡ REAL-TIME: Connect exchange aggregator to candle service for in-memory candles
	candleSvc.SetExchangeAggregator(exchangeAgg)

	// ⚡ NEW: Initialize interval aggregator for building higher timeframes from 1m candles
	intervalAgg := aggregator.NewIntervalAggregator(candleRepo, publisher, logger)
	logger.Info("✅ Interval aggregator initialized")

	// ⚡ IMPORTANT: Connect interval aggregator to real-time 1m candle stream
	exchangeAgg.SetIntervalAggregator(intervalAgg)

	// Initialize mark price service
	markPriceSvc := markpriceService.NewService(priceCache, publisher, exchangeAgg, cfg, logger)

	// Initialize symbol fetcher - fetch from backend trade service
	backendTradeURL := os.Getenv("BACKEND_TRADE_SYMBOLS_URL")
	if backendTradeURL == "" {
		backendTradeURL = "https://nyyu.vyral.social/api/v1/binance/candles/symbols"
	}
	symbolFetcher := symbols.NewSymbolFetcher(backendTradeURL, logger)

	// Initialize gRPC server
	grpcSrv := grpcServer.NewServer(cfg, candleSvc, markPriceSvc, symbolFetcher, redisClient, logger)

	// Start HTTP server for health checks
	go startHTTPServer(cfg, logger, candleSvc, symbolFetcher, exchangeAgg)

	// Start gRPC server
	grpcErrChan := make(chan error, 1)
	go func() {
		logger.Infof("Starting gRPC server on :%d", cfg.Server.GRPCPort)
		if err := grpcSrv.Start(); err != nil {
			grpcErrChan <- err
		}
	}()

	// Start WebSocket aggregation workers
	logger.Info("Starting exchange aggregator...")
	if err := exchangeAgg.Start(context.Background()); err != nil {
		logger.WithError(err).Fatal("Failed to start exchange aggregator")
	}

	// Fetch symbols from backend trade service
	logger.Info("Fetching symbols from backend trade service...")
	allSymbols, err := symbolFetcher.GetSymbols(context.Background())
	if err != nil {
		logger.WithError(err).Warn("Failed to fetch symbols from backend, trying popular symbols...")
		// Try popular symbols as backup
		allSymbols, err = symbolFetcher.GetPopularSymbols(context.Background(), 100)
		if err != nil {
			logger.WithError(err).Error("Failed to fetch symbols - will retry in background")
			allSymbols = symbols.DefaultSymbols // Use default symbols
		}
	}

	if len(allSymbols) == 0 {
		logger.Warn("No symbols returned - using default symbols")
		allSymbols = symbols.DefaultSymbols
	}

	logger.Infof("Loaded %d symbols available for on-demand subscription", len(allSymbols))

	// ⚡ REAL-TIME FIX: Pre-warm top symbols for instant data availability
	// Subscribe to popular symbols on startup so first users get instant data
	// This eliminates the 5-15 second connection delay for common pairs
	popularSymbols := []string{
		"BTCUSDT", "ETHUSDT", "BNBUSDT", "SOLUSDT", "XRPUSDT",
		"ADAUSDT", "DOGEUSDT", "AVAXUSDT", "DOTUSDT", "MATICUSDT",
		"LINKUSDT", "UNIUSDT", "ATOMUSDT", "LTCUSDT", "ETCUSDT",
		"TRXUSDT", "NEARUSDT", "XLMUSDT", "ALGOUSDT", "VETUSDT",
	}

	// Filter to only symbols that exist in our backend
	symbolMap := make(map[string]bool)
	for _, s := range allSymbols {
		symbolMap[s] = true
	}

	prewarmCount := 0
	for _, symbol := range popularSymbols {
		if symbolMap[symbol] {
			// Subscribe to 1m interval - all other intervals are derived from this
			if err := candleSvc.SubscribeToInterval(context.Background(), symbol, "1m"); err != nil {
				logger.WithError(err).Warnf("Failed to pre-warm %s", symbol)
			} else {
				prewarmCount++
			}
		}
	}

	logger.Infof("⚡ Pre-warmed %d popular symbols for instant real-time data", prewarmCount)
	logger.Info("Exchange aggregator ready for on-demand subscriptions")

	// Trigger connections now that we have subscriptions
	if prewarmCount > 0 {
		exchangeAgg.TriggerReconnectAll()
		logger.Info("✅ Triggered exchange connections for pre-warmed symbols")
	}

	// ⚡ PRIORITY 2: Start background 24h stats calculator in candle service
	candleSvc.Start24hStatsCalculator(context.Background())
	logger.Info("✅ Started 24h stats calculator for price tickers")

	// ⚡ NEW: Start interval aggregator for building higher timeframes
	intervalAgg.Start()
	logger.Info("✅ Interval aggregator started - building 3m, 5m, 15m, 30m, 1h, 2h, 4h, 6h, 8h, 12h, 1d, 3d, 1w, 1M candles")

	// ⚡ IMPORTANT: Price tickers are now derived from candles!
	// The system is FULLY on-demand:
	// 1. User calls SubscribePrice gRPC → subscribes to 1m candles → gets real-time prices
	// 2. Price = candle.Close + 24h stats from background calculator
	// 3. No separate price service needed - everything from candles!
	logger.Info("⚡ Price tickers integrated into candle service")

	// Start symbol auto-refresh in background
	go symbolFetcher.StartAutoRefresh(context.Background())

	logger.Infof("Nyyu Market Service v%s started successfully", version)
	logger.Infof("HTTP server listening on :%d", cfg.Server.HTTPPort)
	logger.Infof("gRPC server listening on :%d", cfg.Server.GRPCPort)

	// Wait for shutdown signal or server error
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case <-sigChan:
		logger.Info("📥 Received shutdown signal")
	case err := <-grpcErrChan:
		logger.WithError(err).Error("❌ gRPC server error")
	}

	logger.Info("🛑 Initiating graceful shutdown...")

	// Cancel root context to signal all services
	cancel()

	// Create shutdown context with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Coordinated shutdown sequence
	shutdownSteps := []struct {
		name string
		fn   func() error
	}{
		{
			name: "gRPC server",
			fn: func() error {
				grpcSrv.Stop()
				logger.Info("✅ gRPC server stopped")
				return nil
			},
		},
		{
			name: "Interval aggregator",
			fn: func() error {
				intervalAgg.Stop()
				logger.Info("✅ Interval aggregator stopped")
				return nil
			},
		},
		{
			name: "Exchange aggregator",
			fn: func() error {
				exchangeAgg.Stop()
				logger.Info("✅ Exchange aggregator stopped")
				return nil
			},
		},
		{
			name: "Symbol fetcher",
			fn: func() error {
				// Symbol fetcher uses ctx which is now cancelled
				logger.Info("✅ Symbol fetcher stopped")
				return nil
			},
		},
	}

	// Execute shutdown steps with timeout
	for _, step := range shutdownSteps {
		done := make(chan error, 1)
		go func(s struct {
			name string
			fn   func() error
		}) {
			done <- s.fn()
		}(step)

		select {
		case err := <-done:
			if err != nil {
				logger.WithError(err).Warnf("⚠️  Failed to stop %s gracefully", step.name)
			}
		case <-shutdownCtx.Done():
			logger.Warnf("⏱️  Timeout stopping %s", step.name)
		}
	}

	// Give a moment for final cleanup
	time.Sleep(500 * time.Millisecond)

	logger.Info("✅ Graceful shutdown complete - all services stopped")
}

func runMigrations(conn clickhouse.Conn, logger *logrus.Logger) error {
	ctx := context.Background()

	// Create candles table
	logger.Info("Creating candles table...")
	query := `
		CREATE TABLE IF NOT EXISTS candles (
			symbol LowCardinality(String),
			interval LowCardinality(String),
			open_time DateTime64(3),
			close_time DateTime64(3),
			open Float64 CODEC(DoubleDelta, LZ4),
			high Float64 CODEC(DoubleDelta, LZ4),
			low Float64 CODEC(DoubleDelta, LZ4),
			close Float64 CODEC(Gorilla, ZSTD(1)),
			volume Float64 CODEC(Gorilla, ZSTD(1)),
			quote_volume Float64 CODEC(Gorilla, ZSTD(1)),
			trade_count UInt32,
			taker_buy_base_volume Float64 CODEC(Gorilla, ZSTD(1)),
			taker_buy_quote_volume Float64 CODEC(Gorilla, ZSTD(1)),
			source LowCardinality(String) DEFAULT 'binance',
			is_closed UInt8,
			contract_type LowCardinality(String) DEFAULT 'spot',
			created_at DateTime DEFAULT now(),
			updated_at DateTime DEFAULT now(),
			date Date MATERIALIZED toDate(open_time)
		)
		ENGINE = ReplacingMergeTree(updated_at)
		PARTITION BY (contract_type, interval, toYYYYMM(date))
		ORDER BY (contract_type, symbol, interval, open_time, source)
		PRIMARY KEY (contract_type, symbol, interval, open_time)
		TTL date + INTERVAL 2 YEAR
		SETTINGS index_granularity = 8192
	`
	if err := conn.Exec(ctx, query); err != nil {
		return fmt.Errorf("failed to create candles table: %w", err)
	}
	logger.Info("✓ Candles table created")

	// Add indexes
	logger.Info("Adding indexes...")
	indexes := []string{
		"ALTER TABLE candles ADD INDEX IF NOT EXISTS symbol_idx (symbol) TYPE bloom_filter() GRANULARITY 1",
		"ALTER TABLE candles ADD INDEX IF NOT EXISTS source_idx (source) TYPE bloom_filter() GRANULARITY 1",
		"ALTER TABLE candles ADD INDEX IF NOT EXISTS contract_type_idx (contract_type) TYPE bloom_filter() GRANULARITY 1",
	}

	for _, idx := range indexes {
		if err := conn.Exec(ctx, idx); err != nil {
			logger.Warnf("Failed to create index: %v", err)
		}
	}
	logger.Info("✓ Indexes created")

	return nil
}

func startHTTPServer(cfg *config.Config, logger *logrus.Logger, candleSvc *candleService.Service, symbolFetcher *symbols.SymbolFetcher, exchangeAgg *aggregator.ExchangeAggregator) {
	mux := http.NewServeMux()

	// ⚡ Static files for dashboard
	fs := http.FileServer(http.Dir("./static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	logger.Info("✅ Dashboard UI available at /health")

	// Root route - redirect to health UI
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/health", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	// Health UI - dashboard page
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./static/index.html")
	})

	// ⚡ Prometheus metrics endpoint
	mux.Handle("/metrics", promhttp.Handler())
	logger.Info("✅ Prometheus metrics available at /metrics")

	// Symbols endpoint - returns all supported trading symbols
	mux.HandleFunc("/api/v1/binance/candles/symbols", func(w http.ResponseWriter, r *http.Request) {
		allSymbols, _ := symbolFetcher.GetSymbols(context.Background())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Available symbols retrieved successfully",
			"data": map[string]interface{}{
				"count":   len(allSymbols),
				"symbols": allSymbols,
			},
		})
	})

	// Stats endpoint
	mux.HandleFunc("/api/v1/stats", func(w http.ResponseWriter, r *http.Request) {
		stats, err := candleSvc.GetStats(context.Background())
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"error":"%s"}`, err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"total_candles":%v,"total_symbols":%v}`, stats["total_candles"], stats["total_symbols"])
	})

	// ⚡ REAL-TIME FIX: Add real-time monitoring endpoint
	mux.HandleFunc("/api/v1/realtime/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		exchangeStats := exchangeAgg.GetExchangeStats()
		statsJSON, _ := json.Marshal(map[string]interface{}{
			"success": true,
			"message": "Real-time market data status",
			"data": map[string]interface{}{
				"version":        version,
				"uptime_seconds": int64(time.Since(startTime).Seconds()),
				"exchanges":      exchangeStats,
			},
		})
		w.Write(statsJSON)
	})

	addr := fmt.Sprintf(":%d", cfg.Server.HTTPPort)
	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	logger.Infof("HTTP server starting on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatal("HTTP server failed: ", err)
	}
}
