package repository

import (
	"context"
	"fmt"
	"time"

	"nyyu-market/internal/models"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

type CandleRepository struct {
	clickhouse driver.Conn
	logger     *logrus.Logger
}

func NewCandleRepository(clickhouse driver.Conn, logger *logrus.Logger) *CandleRepository {
	return &CandleRepository{
		clickhouse: clickhouse,
		logger:     logger,
	}
}

// GetCandles retrieves candles from ClickHouse
func (r *CandleRepository) GetCandles(ctx context.Context, symbol, interval, source, contractType string, startTime, endTime time.Time, limit int) ([]models.Candle, error) {
	// ⚡ FIX: Cast Decimal64 to String in SQL for proper scanning
	// Note: Import script now prevents duplicates, so we don't need FINAL or complex deduplication
	query := `
		SELECT
			symbol, interval, open_time, close_time,
			toString(open) as open, toString(high) as high, toString(low) as low, toString(close) as close,
			toString(volume) as volume, toString(quote_volume) as quote_volume, trade_count,
			toString(taker_buy_base_volume) as taker_buy_base_volume, toString(taker_buy_quote_volume) as taker_buy_quote_volume,
			source, is_closed, contract_type,
			created_at, updated_at
		FROM candles
		WHERE symbol = ? AND interval = ? AND contract_type = ?`

	args := []interface{}{symbol, interval, contractType}

	if source != "" && source != "aggregated" {
		query += " AND source = ?"
		args = append(args, source)
	}

	if !startTime.IsZero() {
		query += " AND open_time >= ?"
		args = append(args, startTime)
	}

	if !endTime.IsZero() {
		query += " AND open_time < ?"
		args = append(args, endTime)
	}

	query += " ORDER BY open_time DESC"

	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := r.clickhouse.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query candles: %w", err)
	}
	defer rows.Close()

	var candles []models.Candle
	for rows.Next() {
		var candle models.Candle
		var isClosed uint8
		var tradeCount uint32

		// ⚡ FIX: Scan Decimal64 as strings and convert to decimal.Decimal
		// ClickHouse Decimal64 values need to be scanned as strings
		var open, high, low, close, volume, quoteVolume string
		var takerBuyBase, takerBuyQuote string

		err := rows.Scan(
			&candle.Symbol, &candle.Interval, &candle.OpenTime, &candle.CloseTime,
			&open, &high, &low, &close,
			&volume, &quoteVolume, &tradeCount,
			&takerBuyBase, &takerBuyQuote,
			&candle.Source, &isClosed, &candle.ContractType,
			&candle.CreatedAt, &candle.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan candle: %w", err)
		}

		// Convert string decimals to decimal.Decimal
		candle.Open, _ = decimal.NewFromString(open)
		candle.High, _ = decimal.NewFromString(high)
		candle.Low, _ = decimal.NewFromString(low)
		candle.Close, _ = decimal.NewFromString(close)
		candle.Volume, _ = decimal.NewFromString(volume)
		candle.QuoteVolume, _ = decimal.NewFromString(quoteVolume)
		candle.TakerBuyBaseVolume, _ = decimal.NewFromString(takerBuyBase)
		candle.TakerBuyQuoteVolume, _ = decimal.NewFromString(takerBuyQuote)
		candle.IsClosed = isClosed == 1
		candle.TradeCount = int(tradeCount)

		candles = append(candles, candle)
	}

	// Reverse to chronological order
	for i, j := 0, len(candles)-1; i < j; i, j = i+1, j-1 {
		candles[i], candles[j] = candles[j], candles[i]
	}

	return candles, nil
}

// GetLatestCandle retrieves the latest candle
func (r *CandleRepository) GetLatestCandle(ctx context.Context, symbol, interval, source, contractType string) (*models.Candle, error) {
	// ⚡ FIX: Cast Decimal64 to String in SQL for proper scanning
	// Note: Import script now prevents duplicates, so we don't need FINAL or complex deduplication
	query := `
		SELECT
			symbol, interval, open_time, close_time,
			toString(open) as open, toString(high) as high, toString(low) as low, toString(close) as close,
			toString(volume) as volume, toString(quote_volume) as quote_volume, trade_count,
			toString(taker_buy_base_volume) as taker_buy_base_volume, toString(taker_buy_quote_volume) as taker_buy_quote_volume,
			source, is_closed, contract_type,
			created_at, updated_at
		FROM candles
		WHERE symbol = ? AND interval = ? AND contract_type = ?`

	args := []interface{}{symbol, interval, contractType}

	if source != "" && source != "aggregated" {
		query += " AND source = ?"
		args = append(args, source)
	}

	query += " ORDER BY open_time DESC LIMIT 1"

	row := r.clickhouse.QueryRow(ctx, query, args...)

	var candle models.Candle
	var isClosed uint8
	var tradeCount uint32
	// ⚡ FIX: Scan Decimal64 as strings
	var open, high, low, close, volume, quoteVolume string
	var takerBuyBase, takerBuyQuote string

	err := row.Scan(
		&candle.Symbol, &candle.Interval, &candle.OpenTime, &candle.CloseTime,
		&open, &high, &low, &close,
		&volume, &quoteVolume, &tradeCount,
		&takerBuyBase, &takerBuyQuote,
		&candle.Source, &isClosed, &candle.ContractType,
		&candle.CreatedAt, &candle.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	// Convert string decimals to decimal.Decimal
	candle.Open, _ = decimal.NewFromString(open)
	candle.High, _ = decimal.NewFromString(high)
	candle.Low, _ = decimal.NewFromString(low)
	candle.Close, _ = decimal.NewFromString(close)
	candle.Volume, _ = decimal.NewFromString(volume)
	candle.QuoteVolume, _ = decimal.NewFromString(quoteVolume)
	candle.TakerBuyBaseVolume, _ = decimal.NewFromString(takerBuyBase)
	candle.TakerBuyQuoteVolume, _ = decimal.NewFromString(takerBuyQuote)
	candle.IsClosed = isClosed == 1
	candle.TradeCount = int(tradeCount)

	return &candle, nil
}

// CreateCandle inserts a new candle
func (r *CandleRepository) CreateCandle(ctx context.Context, candle *models.Candle) error {
	query := `
		INSERT INTO candles (
			symbol, interval, open_time, close_time,
			open, high, low, close,
			volume, quote_volume, trade_count,
			taker_buy_base_volume, taker_buy_quote_volume,
			source, is_closed, contract_type,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	// ⚡ OPTIMIZED: Use decimal.Decimal directly with Decimal64(8) columns
	// ClickHouse Go driver automatically converts decimal.Decimal to Decimal64
	// This preserves precision better than Float64 conversion
	isClosed := uint8(0)
	if candle.IsClosed {
		isClosed = 1
	}

	// CRITICAL FIX: Convert time.Time to int64 milliseconds for DateTime64(3) columns
	// The ClickHouse Go driver has issues with time.Time -> DateTime64(3) conversion
	openTimeMs := candle.OpenTime.UnixMilli()
	closeTimeMs := candle.CloseTime.UnixMilli()
	createdAtMs := candle.CreatedAt.UnixMilli()
	updatedAtMs := candle.UpdatedAt.UnixMilli()

	err := r.clickhouse.Exec(ctx, query,
		candle.Symbol, candle.Interval, openTimeMs, closeTimeMs,
		candle.Open, candle.High, candle.Low, candle.Close,
		candle.Volume, candle.QuoteVolume, candle.TradeCount,
		candle.TakerBuyBaseVolume, candle.TakerBuyQuoteVolume,
		candle.Source, isClosed, candle.ContractType,
		createdAtMs, updatedAtMs,
	)

	return err
}

// BatchCreateCandles inserts multiple candles efficiently
func (r *CandleRepository) BatchCreateCandles(ctx context.Context, candles []*models.Candle) error {
	if len(candles) == 0 {
		return nil
	}

	// Set max_partitions_per_insert_block to handle large batches with many partitions
	if err := r.clickhouse.Exec(ctx, "SET max_partitions_per_insert_block = 10000"); err != nil {
		return fmt.Errorf("failed to set max_partitions_per_insert_block: %w", err)
	}

	batch, err := r.clickhouse.PrepareBatch(ctx, `
		INSERT INTO candles (
			symbol, interval, open_time, close_time,
			open, high, low, close,
			volume, quote_volume, trade_count,
			taker_buy_base_volume, taker_buy_quote_volume,
			source, is_closed, contract_type,
			created_at, updated_at
		)`)
	if err != nil {
		return fmt.Errorf("failed to prepare batch: %w", err)
	}

	// ⚡ OPTIMIZED: Batch insert with decimal.Decimal directly
	// Better precision with Decimal64(8) columns in ClickHouse
	for _, candle := range candles {
		isClosed := uint8(0)
		if candle.IsClosed {
			isClosed = 1
		}

		// CRITICAL FIX: Convert time.Time to int64 milliseconds for DateTime64(3) columns
		// The ClickHouse Go driver has issues with time.Time -> DateTime64(3) conversion
		// This ensures accurate timestamp storage
		openTimeMs := candle.OpenTime.UnixMilli()
		closeTimeMs := candle.CloseTime.UnixMilli()
		createdAtMs := candle.CreatedAt.UnixMilli()
		updatedAtMs := candle.UpdatedAt.UnixMilli()

		err := batch.Append(
			candle.Symbol, candle.Interval, openTimeMs, closeTimeMs,
			candle.Open, candle.High, candle.Low, candle.Close,
			candle.Volume, candle.QuoteVolume, candle.TradeCount,
			candle.TakerBuyBaseVolume, candle.TakerBuyQuoteVolume,
			candle.Source, isClosed, candle.ContractType,
			createdAtMs, updatedAtMs,
		)
		if err != nil {
			return fmt.Errorf("failed to append to batch: %w", err)
		}
	}

	return batch.Send()
}

// GetStats retrieves candle statistics
func (r *CandleRepository) GetStats(ctx context.Context) (map[string]interface{}, error) {
	query := `
		SELECT
			count() as total_candles,
			count(DISTINCT symbol) as total_symbols,
			min(open_time) as earliest_candle,
			max(open_time) as latest_candle
		FROM candles`

	row := r.clickhouse.QueryRow(ctx, query)

	var totalCandles, totalSymbols uint64
	var earliest, latest time.Time

	err := row.Scan(&totalCandles, &totalSymbols, &earliest, &latest)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"total_candles":   totalCandles,
		"total_symbols":   totalSymbols,
		"earliest_candle": earliest,
		"latest_candle":   latest,
	}, nil
}

// GetAvailableSymbols retrieves all unique symbols from the candles table
func (r *CandleRepository) GetAvailableSymbols(ctx context.Context) ([]string, error) {
	query := `
		SELECT DISTINCT symbol
		FROM candles
		ORDER BY symbol`

	rows, err := r.clickhouse.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query symbols: %w", err)
	}
	defer rows.Close()

	var symbols []string
	for rows.Next() {
		var symbol string
		if err := rows.Scan(&symbol); err != nil {
			return nil, fmt.Errorf("failed to scan symbol: %w", err)
		}
		symbols = append(symbols, symbol)
	}

	return symbols, nil
}
