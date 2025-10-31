package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Server     ServerConfig
	ClickHouse ClickHouseConfig
	Redis      RedisConfig
	Cache      CacheConfig
	Service    ServiceConfig
	Exchange   ExchangeConfig
	MarkPrice  MarkPriceConfig
	Logging    LoggingConfig
}

type ServerConfig struct {
	GRPCPort    int
	HTTPPort    int
	Environment string
}

type ClickHouseConfig struct {
	Host     string
	Port     int
	Database string
	Username string
	Password string
}

type RedisConfig struct {
	Host           string
	Port           int
	Password       string
	DB             int
	PubSubChannel  string
}

type CacheConfig struct {
	PriceTTL     time.Duration
	MarkPriceTTL time.Duration
	CandleTTL    time.Duration
}

type ServiceConfig struct {
	MaxCandlesLimit     int
	DefaultCandlesLimit int
	BatchWriteSize      int
	BatchWriteInterval  time.Duration
}

type ExchangeConfig struct {
	EnableBinance  bool
	EnableKraken   bool
	EnableCoinbase bool
	EnableBybit    bool
	EnableOKX      bool
	EnableGateIO   bool
}

type MarkPriceConfig struct {
	UpdateInterval time.Duration
	EMAPeriod      int
}

type LoggingConfig struct {
	Level  string
	Format string
}

// Load loads configuration from environment variables
func Load() (*Config, error) {
	// Load .env file if exists
	_ = godotenv.Load()

	cfg := &Config{
		Server: ServerConfig{
			GRPCPort:    getEnvInt("SERVER_PORT", 50051),
			HTTPPort:    getEnvInt("HTTP_PORT", 8080),
			Environment: getEnv("ENVIRONMENT", "development"),
		},
		ClickHouse: ClickHouseConfig{
			Host:     getEnv("CLICKHOUSE_HOST", "localhost"),
			Port:     getEnvInt("CLICKHOUSE_PORT", 9000),
			Database: getEnv("CLICKHOUSE_DATABASE", "trade"),
			Username: getEnv("CLICKHOUSE_USERNAME", "default"),
			Password: getEnv("CLICKHOUSE_PASSWORD", ""),
		},
		Redis: RedisConfig{
			Host:          getEnv("REDIS_HOST", "localhost"),
			Port:          getEnvInt("REDIS_PORT", 6379),
			Password:      getEnv("REDIS_PASSWORD", ""),
			DB:            getEnvInt("REDIS_DB", 0),
			PubSubChannel: getEnv("REDIS_PUBSUB_CHANNEL", "nyyu:market:updates"),
		},
		Cache: CacheConfig{
			PriceTTL:     time.Duration(getEnvInt("CACHE_TTL_PRICE", 60)) * time.Second,
			MarkPriceTTL: time.Duration(getEnvInt("CACHE_TTL_MARK_PRICE", 3)) * time.Second,
			CandleTTL:    time.Duration(getEnvInt("CACHE_TTL_CANDLE", 5)) * time.Second,
		},
		Service: ServiceConfig{
			MaxCandlesLimit:     getEnvInt("MAX_CANDLES_LIMIT", 1000),
			DefaultCandlesLimit: getEnvInt("DEFAULT_CANDLES_LIMIT", 100),
			BatchWriteSize:      getEnvInt("BATCH_WRITE_SIZE", 100),
			BatchWriteInterval:  parseDuration(getEnv("BATCH_WRITE_INTERVAL", "1s"), 1*time.Second),
		},
		Exchange: ExchangeConfig{
			EnableBinance:  getEnvBool("ENABLE_BINANCE", true),
			EnableKraken:   getEnvBool("ENABLE_KRAKEN", true),
			EnableCoinbase: getEnvBool("ENABLE_COINBASE", true),
			EnableBybit:    getEnvBool("ENABLE_BYBIT", true),
			EnableOKX:      getEnvBool("ENABLE_OKX", true),
			EnableGateIO:   getEnvBool("ENABLE_GATEIO", true),
		},
		MarkPrice: MarkPriceConfig{
			UpdateInterval: parseDuration(getEnv("MARK_PRICE_UPDATE_INTERVAL", "100ms"), 100*time.Millisecond),
			EMAPeriod:      getEnvInt("FUNDING_RATE_EMA_PERIOD", 20),
		},
		Logging: LoggingConfig{
			Level:  getEnv("LOG_LEVEL", "info"),
			Format: getEnv("LOG_FORMAT", "json"),
		},
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	if c.ClickHouse.Host == "" {
		return fmt.Errorf("CLICKHOUSE_HOST is required")
	}
	if c.Redis.Host == "" {
		return fmt.Errorf("REDIS_HOST is required")
	}
	return nil
}

func (c *ClickHouseConfig) DSN() string {
	return fmt.Sprintf("clickhouse://%s:%s@%s:%d/%s?dial_timeout=10s&max_execution_time=60",
		c.Username, c.Password, c.Host, c.Port, c.Database)
}

func (c *RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// Helper functions
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolVal, err := strconv.ParseBool(value); err == nil {
			return boolVal
		}
	}
	return defaultValue
}

func parseDuration(s string, defaultValue time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultValue
	}
	return d
}
