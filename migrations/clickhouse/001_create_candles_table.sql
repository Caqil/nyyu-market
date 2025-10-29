-- Create separate ClickHouse database for nyyu-market
CREATE DATABASE IF NOT EXISTS nyyu_market;

-- Create candles table for nyyu-market (independent from trade system)
CREATE TABLE IF NOT EXISTS nyyu_market.candles (
    -- Symbol and interval
    symbol LowCardinality(String),
    interval LowCardinality(String),

    -- Time columns (millisecond precision)
    open_time DateTime64(3),
    close_time DateTime64(3),

    -- OHLCV data with compression
    open Float64 CODEC(DoubleDelta, LZ4),
    high Float64 CODEC(DoubleDelta, LZ4),
    low Float64 CODEC(DoubleDelta, LZ4),
    close Float64 CODEC(DoubleDelta, LZ4),
    volume Float64 CODEC(Gorilla, ZSTD(1)),
    quote_volume Float64 CODEC(Gorilla, ZSTD(1)),

    -- Trade metrics
    trade_count UInt32,
    taker_buy_base_volume Float64 CODEC(Gorilla, ZSTD(1)),
    taker_buy_quote_volume Float64 CODEC(Gorilla, ZSTD(1)),

    -- Multi-exchange support
    source LowCardinality(String) DEFAULT 'binance',
    is_closed UInt8,

    -- Contract type support
    contract_type LowCardinality(String) DEFAULT 'spot',

    -- Timestamps
    created_at DateTime DEFAULT now(),
    updated_at DateTime DEFAULT now(),

    -- Materialized date for partitioning
    date Date MATERIALIZED toDate(open_time)
)
ENGINE = ReplacingMergeTree(updated_at)
PARTITION BY (contract_type, interval, toYYYYMM(date))
ORDER BY (contract_type, symbol, interval, open_time, source)
PRIMARY KEY (contract_type, symbol, interval, open_time)
TTL date + INTERVAL 2 YEAR
SETTINGS
    index_granularity = 8192;

-- Add indexes for faster queries
ALTER TABLE nyyu_market.candles ADD INDEX IF NOT EXISTS symbol_idx (symbol) TYPE bloom_filter() GRANULARITY 1;
ALTER TABLE nyyu_market.candles ADD INDEX IF NOT EXISTS source_idx (source) TYPE bloom_filter() GRANULARITY 1;
ALTER TABLE nyyu_market.candles ADD INDEX IF NOT EXISTS contract_type_idx (contract_type) TYPE bloom_filter() GRANULARITY 1;

-- Add projection for latest price queries
ALTER TABLE nyyu_market.candles ADD PROJECTION IF NOT EXISTS latest_candle_projection
(
    SELECT
        symbol,
        interval,
        source,
        max(open_time) as latest_time,
        argMax(close, open_time) as latest_close
    GROUP BY symbol, interval, source
);
