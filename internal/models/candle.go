package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// Candle represents OHLCV data from multiple exchanges
type Candle struct {
	Symbol       string          `json:"symbol"`
	Interval     string          `json:"interval"`
	OpenTime     time.Time       `json:"open_time"`
	CloseTime    time.Time       `json:"close_time"`
	Open         decimal.Decimal `json:"open"`
	High         decimal.Decimal `json:"high"`
	Low          decimal.Decimal `json:"low"`
	Close        decimal.Decimal `json:"close"`
	Volume       decimal.Decimal `json:"volume"`
	QuoteVolume  decimal.Decimal `json:"quote_volume"`
	TradeCount   int             `json:"trade_count"`
	Source       string          `json:"source"`        // binance, kraken, coinbase, bybit, okx, gateio, aggregated
	IsClosed     bool            `json:"is_closed"`
	ContractType string          `json:"contract_type"` // spot, futures, perpetual
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`

	// Optional exchange-specific fields
	TakerBuyBaseVolume  decimal.Decimal `json:"taker_buy_base_volume,omitempty"`
	TakerBuyQuoteVolume decimal.Decimal `json:"taker_buy_quote_volume,omitempty"`
}

// CandleResponse represents API response format
type CandleResponse struct {
	Symbol      string `json:"symbol"`
	Interval    string `json:"interval"`
	OpenTime    int64  `json:"open_time"`  // Milliseconds
	CloseTime   int64  `json:"close_time"` // Milliseconds
	Open        string `json:"open"`
	High        string `json:"high"`
	Low         string `json:"low"`
	Close       string `json:"close"`
	Volume      string `json:"volume"`
	QuoteVolume string `json:"quote_volume"`
	TradeCount  int    `json:"trade_count"`
	IsClosed    bool   `json:"is_closed"`
}

// ToResponse converts Candle to API response format
func (c *Candle) ToResponse() *CandleResponse {
	return &CandleResponse{
		Symbol:      c.Symbol,
		Interval:    c.Interval,
		OpenTime:    c.OpenTime.UnixMilli(),
		CloseTime:   c.CloseTime.UnixMilli(),
		Open:        c.Open.String(),
		High:        c.High.String(),
		Low:         c.Low.String(),
		Close:       c.Close.String(),
		Volume:      c.Volume.String(),
		QuoteVolume: c.QuoteVolume.String(),
		TradeCount:  c.TradeCount,
		IsClosed:    c.IsClosed,
	}
}

// IntervalToDuration converts interval string to duration
func IntervalToDuration(interval string) time.Duration {
	switch interval {
	case "1s":
		return 1 * time.Second
	case "1m":
		return 1 * time.Minute
	case "3m":
		return 3 * time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return 1 * time.Hour
	case "2h":
		return 2 * time.Hour
	case "4h":
		return 4 * time.Hour
	case "6h":
		return 6 * time.Hour
	case "8h":
		return 8 * time.Hour
	case "12h":
		return 12 * time.Hour
	case "1d":
		return 24 * time.Hour
	case "3d":
		return 72 * time.Hour
	case "1w":
		return 168 * time.Hour
	case "1M":
		return 720 * time.Hour // Approx 30 days
	default:
		return 1 * time.Minute
	}
}

// TruncateToInterval truncates a time to the interval boundary
func TruncateToInterval(t time.Time, interval string) time.Time {
	duration := IntervalToDuration(interval)
	return t.Truncate(duration)
}

// ValidIntervals returns list of valid intervals
func ValidIntervals() []string {
	return []string{
		"1s", "1m", "3m", "5m", "15m", "30m",
		"1h", "2h", "4h", "6h", "8h", "12h",
		"1d", "3d", "1w", "1M",
	}
}
