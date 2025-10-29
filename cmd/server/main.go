package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nyyu-market/internal/cache"
	"nyyu-market/internal/config"
	grpcServer "nyyu-market/internal/grpc"
	"nyyu-market/internal/pubsub"
	"nyyu-market/internal/repository"
	"nyyu-market/internal/services/aggregator"
	candleService "nyyu-market/internal/services/candle"
	priceService "nyyu-market/internal/services/price"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/go-redis/redis/v8"
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

	// Initialize Redis
	logger.Info("Connecting to Redis...")
	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})

	ctx := context.Background()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.Fatal("Failed to connect to Redis: ", err)
	}
	defer redisClient.Close()
	logger.Info("Redis connected successfully")

	// Initialize repositories
	candleRepo := repository.NewCandleRepository(clickhouseConn, logger)

	// Initialize cache
	candleCache := cache.NewCandleCache(redisClient, logger)
	priceCache := cache.NewPriceCache(redisClient, logger)

	// Initialize pub/sub
	publisher := pubsub.NewPublisher(redisClient, logger)

	// Initialize services
	candleSvc := candleService.NewService(candleRepo, candleCache, publisher, cfg, logger)

	// Initialize exchange aggregator
	exchangeAgg := aggregator.NewExchangeAggregator(cfg, candleSvc, publisher, logger)

	// Initialize price service
	priceSvc := priceService.NewService(candleSvc, priceCache, publisher, cfg, logger)

	// Initialize gRPC server
	grpcSrv := grpcServer.NewServer(cfg, candleSvc, logger)

	// Start HTTP server for health checks
	go startHTTPServer(cfg, logger, candleSvc)

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

	// Subscribe to popular pairs
	popularSymbols := []string{
		"BTCUSDT", "ETHUSDT", "BNBUSDT", "SOLUSDT", "XRPUSDT",
		"ADAUSDT", "DOGEUSDT", "AVAXUSDT", "DOTUSDT", "MATICUSDT",
	}

	for _, symbol := range popularSymbols {
		_ = exchangeAgg.SubscribeToSymbol(context.Background(), symbol, "1m")
	}

	// Trigger reconnection for all exchanges after subscribing
	logger.Info("Triggering exchange connections...")
	exchangeAgg.TriggerReconnectAll()

	// Start price updater (updates every 10 seconds)
	go priceSvc.StartPriceUpdater(context.Background(), popularSymbols, 10*time.Second)

	logger.Infof("Nyyu Market Service v%s started successfully", version)
	logger.Infof("HTTP server listening on :%d", cfg.Server.HTTPPort)
	logger.Infof("gRPC server listening on :%d", cfg.Server.GRPCPort)

	// Wait for shutdown signal or server error
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case <-sigChan:
		logger.Info("Received shutdown signal")
	case err := <-grpcErrChan:
		logger.WithError(err).Error("gRPC server error")
	}

	logger.Info("Shutting down gracefully...")

	// Stop gRPC server
	grpcSrv.Stop()

	// Stop exchange aggregator
	exchangeAgg.Stop()

	time.Sleep(2 * time.Second)
	logger.Info("Shutdown complete")
}

func startHTTPServer(cfg *config.Config, logger *logrus.Logger, candleSvc *candleService.Service) {
	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"healthy":true,"version":"%s","uptime_seconds":%d,"services":{"clickhouse":"healthy","redis":"healthy"}}`,
			version, int64(time.Since(startTime).Seconds()))
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
