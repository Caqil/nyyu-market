package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env
	_ = godotenv.Load()

	host := getEnv("CLICKHOUSE_HOST", "localhost")
	port := getEnv("CLICKHOUSE_PORT", "9000")
	database := getEnv("CLICKHOUSE_DATABASE", "nyyu_market")
	username := getEnv("CLICKHOUSE_USERNAME", "default")
	password := getEnv("CLICKHOUSE_PASSWORD", "")

	// Connect to ClickHouse
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%s", host, port)},
		Auth: clickhouse.Auth{
			Database: "default", // Connect to default first
			Username: username,
			Password: password,
		},
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		log.Fatal("Failed to connect to ClickHouse:", err)
	}
	defer conn.Close()

	ctx := context.Background()

	// Create database
	log.Printf("Creating database: %s", database)
	query := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", database)
	if err := conn.Exec(ctx, query); err != nil {
		log.Fatal("Failed to create database:", err)
	}
	log.Println("✓ Database created")

	// Reconnect to the new database
	conn.Close()
	conn, err = clickhouse.Open(&clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%s", host, port)},
		Auth: clickhouse.Auth{
			Database: database,
			Username: username,
			Password: password,
		},
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		log.Fatal("Failed to reconnect to database:", err)
	}
	defer conn.Close()

	// Create candles table
	log.Println("Creating candles table...")
	query = `
		CREATE TABLE IF NOT EXISTS candles (
			symbol LowCardinality(String),
			interval LowCardinality(String),
			open_time DateTime64(3),
			close_time DateTime64(3),
			open Float64 CODEC(DoubleDelta, LZ4),
			high Float64 CODEC(DoubleDelta, LZ4),
			low Float64 CODEC(DoubleDelta, LZ4),
			close Float64 CODEC(DoubleDelta, LZ4),
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
		log.Fatal("Failed to create candles table:", err)
	}
	log.Println("✓ Candles table created")

	// Add indexes
	log.Println("Adding indexes...")

	indexes := []string{
		"ALTER TABLE candles ADD INDEX IF NOT EXISTS symbol_idx (symbol) TYPE bloom_filter() GRANULARITY 1",
		"ALTER TABLE candles ADD INDEX IF NOT EXISTS source_idx (source) TYPE bloom_filter() GRANULARITY 1",
		"ALTER TABLE candles ADD INDEX IF NOT EXISTS contract_type_idx (contract_type) TYPE bloom_filter() GRANULARITY 1",
	}

	for _, idx := range indexes {
		if err := conn.Exec(ctx, idx); err != nil {
			log.Printf("Warning: Failed to create index: %v", err)
		}
	}
	log.Println("✓ Indexes created")

	log.Println("\n✅ ClickHouse migration completed successfully!")
	log.Printf("Database: %s", database)
	log.Println("Table: candles")
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
