package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// Price represents current price data for a symbol
type Price struct {
	Symbol         string          `json:"symbol"`
	LastPrice      decimal.Decimal `json:"last_price"`
	PriceChange24h decimal.Decimal `json:"price_change_24h"`
	PriceChange24P decimal.Decimal `json:"price_change_24h_percent"`
	High24h        decimal.Decimal `json:"high_24h"`
	Low24h         decimal.Decimal `json:"low_24h"`
	Volume24h      decimal.Decimal `json:"volume_24h"`
	QuoteVolume24h decimal.Decimal `json:"quote_volume_24h"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// Price24hStats represents 24h statistics
type Price24hStats struct {
	PriceChange24h decimal.Decimal `json:"price_change_24h"`
	PriceChange24P decimal.Decimal `json:"price_change_24h_percent"`
	High24h        decimal.Decimal `json:"high_24h"`
	Low24h         decimal.Decimal `json:"low_24h"`
	Volume24h      decimal.Decimal `json:"volume_24h"`
	QuoteVolume24h decimal.Decimal `json:"quote_volume_24h"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// MarkPrice represents futures mark price
type MarkPrice struct {
	Symbol               string          `json:"symbol"`
	MarkPrice            decimal.Decimal `json:"mark_price"`
	IndexPrice           decimal.Decimal `json:"index_price"`
	LastPrice            decimal.Decimal `json:"last_price"`
	FundingBasis         decimal.Decimal `json:"funding_basis"`
	EstimatedFundingRate decimal.Decimal `json:"estimated_funding_rate"`
	NextFundingTime      time.Time       `json:"next_funding_time"`
	Timestamp            time.Time       `json:"timestamp"`
}
