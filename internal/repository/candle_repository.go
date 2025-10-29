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
	query := `
		SELECT
			symbol, interval, open_time, close_time,
			open, high, low, close,
			volume, quote_volume, trade_count,
			taker_buy_base_volume, taker_buy_quote_volume,
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

		var open, high, low, close, volume, quoteVolume float64
		var takerBuyBase, takerBuyQuote float64

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

		candle.Open = decimal.NewFromFloat(open)
		candle.High = decimal.NewFromFloat(high)
		candle.Low = decimal.NewFromFloat(low)
		candle.Close = decimal.NewFromFloat(close)
		candle.Volume = decimal.NewFromFloat(volume)
		candle.QuoteVolume = decimal.NewFromFloat(quoteVolume)
		candle.TakerBuyBaseVolume = decimal.NewFromFloat(takerBuyBase)
		candle.TakerBuyQuoteVolume = decimal.NewFromFloat(takerBuyQuote)
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
	query := `
		SELECT
			symbol, interval, open_time, close_time,
			open, high, low, close,
			volume, quote_volume, trade_count,
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
	var open, high, low, close, volume, quoteVolume float64

	err := row.Scan(
		&candle.Symbol, &candle.Interval, &candle.OpenTime, &candle.CloseTime,
		&open, &high, &low, &close,
		&volume, &quoteVolume, &tradeCount,
		&candle.Source, &isClosed, &candle.ContractType,
		&candle.CreatedAt, &candle.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	candle.Open = decimal.NewFromFloat(open)
	candle.High = decimal.NewFromFloat(high)
	candle.Low = decimal.NewFromFloat(low)
	candle.Close = decimal.NewFromFloat(close)
	candle.Volume = decimal.NewFromFloat(volume)
	candle.QuoteVolume = decimal.NewFromFloat(quoteVolume)
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

	open, _ := candle.Open.Float64()
	high, _ := candle.High.Float64()
	low, _ := candle.Low.Float64()
	close, _ := candle.Close.Float64()
	volume, _ := candle.Volume.Float64()
	quoteVolume, _ := candle.QuoteVolume.Float64()
	takerBuyBase, _ := candle.TakerBuyBaseVolume.Float64()
	takerBuyQuote, _ := candle.TakerBuyQuoteVolume.Float64()

	isClosed := uint8(0)
	if candle.IsClosed {
		isClosed = 1
	}

	err := r.clickhouse.Exec(ctx, query,
		candle.Symbol, candle.Interval, candle.OpenTime, candle.CloseTime,
		open, high, low, close,
		volume, quoteVolume, candle.TradeCount,
		takerBuyBase, takerBuyQuote,
		candle.Source, isClosed, candle.ContractType,
		candle.CreatedAt, candle.UpdatedAt,
	)

	return err
}

// BatchCreateCandles inserts multiple candles efficiently
func (r *CandleRepository) BatchCreateCandles(ctx context.Context, candles []*models.Candle) error {
	if len(candles) == 0 {
		return nil
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

	for _, candle := range candles {
		open, _ := candle.Open.Float64()
		high, _ := candle.High.Float64()
		low, _ := candle.Low.Float64()
		close, _ := candle.Close.Float64()
		volume, _ := candle.Volume.Float64()
		quoteVolume, _ := candle.QuoteVolume.Float64()
		takerBuyBase, _ := candle.TakerBuyBaseVolume.Float64()
		takerBuyQuote, _ := candle.TakerBuyQuoteVolume.Float64()

		isClosed := uint8(0)
		if candle.IsClosed {
			isClosed = 1
		}

		err := batch.Append(
			candle.Symbol, candle.Interval, candle.OpenTime, candle.CloseTime,
			open, high, low, close,
			volume, quoteVolume, candle.TradeCount,
			takerBuyBase, takerBuyQuote,
			candle.Source, isClosed, candle.ContractType,
			candle.CreatedAt, candle.UpdatedAt,
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
