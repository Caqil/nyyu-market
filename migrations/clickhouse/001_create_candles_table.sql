-- ============================================
-- OPTIMIZED CLICKHOUSE CANDLES TABLE
-- Best for: Time-series data, minimal disk usage
-- ============================================

-- Use trade database (as per your config)
CREATE DATABASE IF NOT EXISTS trade;

-- Drop old table if exists (for fresh start)
-- DROP TABLE IF EXISTS trade.candles;

-- Create highly optimized candles table
CREATE TABLE IF NOT EXISTS trade.candles (
    -- Symbol and interval (LowCardinality reduces storage by 10-30%)
    symbol LowCardinality(String),
    interval LowCardinality(String),

    -- Time columns (DateTime64(3) = millisecond precision)
    open_time DateTime64(3),
    close_time DateTime64(3),

    -- OHLCV data with specialized compression
    -- DoubleDelta: Perfect for gradually changing prices (90% compression!)
    -- ZSTD(3): General purpose compression, level 3 = fast + good ratio
    -- Note: Gorilla only works with Float types, so we use DoubleDelta for Decimal64
    open Decimal64(8) CODEC(DoubleDelta, ZSTD(3)),
    high Decimal64(8) CODEC(DoubleDelta, ZSTD(3)),
    low Decimal64(8) CODEC(DoubleDelta, ZSTD(3)),
    close Decimal64(8) CODEC(DoubleDelta, ZSTD(3)),
    volume Decimal64(8) CODEC(DoubleDelta, ZSTD(3)),
    quote_volume Decimal64(8) CODEC(DoubleDelta, ZSTD(3)),

    -- Trade metrics
    trade_count UInt32 CODEC(T64, ZSTD(3)),
    taker_buy_base_volume Decimal64(8) CODEC(DoubleDelta, ZSTD(3)),
    taker_buy_quote_volume Decimal64(8) CODEC(DoubleDelta, ZSTD(3)),

    -- Source and flags
    source LowCardinality(String) DEFAULT 'aggregated',
    is_closed UInt8 DEFAULT 0,
    contract_type LowCardinality(String) DEFAULT 'spot',

    -- Timestamps (simple DateTime is enough)
    created_at DateTime DEFAULT now(),
    updated_at DateTime DEFAULT now()
)
ENGINE = ReplacingMergeTree(updated_at)
-- Smart partitioning: By interval and month
-- Why? Each interval has different retention needs
-- Why month? Drops entire partitions when TTL expires (faster than row-level TTL)
PARTITION BY (interval, toYYYYMM(open_time))
-- Optimal sort order for queries
-- Most queries: "Get candles for symbol X, interval Y, time range Z"
ORDER BY (symbol, interval, open_time, source)
PRIMARY KEY (symbol, interval, open_time)
-- TTL: Auto-delete old data to save space
-- 1m, 3m, 5m: Keep 90 days (short-term data)
-- 15m, 30m, 1h: Keep 180 days (medium-term)
-- 4h, 1d, 1w: Keep 10 years (long-term historical data back to 2018)
TTL
    open_time + INTERVAL 90 DAY DELETE WHERE interval IN ('1m', '3m', '5m'),
    open_time + INTERVAL 180 DAY DELETE WHERE interval IN ('15m', '30m', '1h', '2h'),
    open_time + INTERVAL 10 YEAR DELETE WHERE interval IN ('4h', '6h', '8h', '12h', '1d', '3d', '1w', '1M')
SETTINGS
    -- Optimize for time-series inserts
    index_granularity = 8192,  -- Default, good for most cases
    -- Compact storage (saves 30-50% disk space)
    min_bytes_for_wide_part = 10485760,  -- 10MB - use compact format below this
    min_rows_for_wide_part = 0,
    -- Enable TTL for space saving
    ttl_only_drop_parts = 1,  -- Drop entire parts (faster)
    merge_with_ttl_timeout = 86400;  -- Check TTL daily

-- ============================================
-- INDEXES FOR FASTER QUERIES
-- ============================================

-- Bloom filter: Fast lookups for WHERE symbol = 'BTCUSDT'
ALTER TABLE trade.candles ADD INDEX IF NOT EXISTS idx_symbol (symbol) TYPE bloom_filter() GRANULARITY 1;

-- Bloom filter: Fast lookups for WHERE source = 'aggregated'
ALTER TABLE trade.candles ADD INDEX IF NOT EXISTS idx_source (source) TYPE bloom_filter() GRANULARITY 1;

-- MinMax: Fast range filtering for WHERE interval = '1m'
ALTER TABLE trade.candles ADD INDEX IF NOT EXISTS idx_interval (interval) TYPE minmax GRANULARITY 1;

-- ============================================
-- MATERIALIZED VIEW FOR LATEST CANDLES
-- (Makes GetLatestCandle queries 100x faster!)
-- ============================================

CREATE TABLE IF NOT EXISTS trade.candles_latest (
    symbol LowCardinality(String),
    interval LowCardinality(String),
    source LowCardinality(String),
    latest_open_time DateTime64(3),
    open Decimal64(8),
    high Decimal64(8),
    low Decimal64(8),
    close Decimal64(8),
    volume Decimal64(8),
    quote_volume Decimal64(8),
    trade_count UInt32,
    is_closed UInt8,
    updated_at DateTime
)
ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (symbol, interval, source)
PRIMARY KEY (symbol, interval, source);

-- Materialized view: Auto-updates latest candles
CREATE MATERIALIZED VIEW IF NOT EXISTS trade.candles_latest_mv TO trade.candles_latest AS
SELECT
    symbol,
    interval,
    source,
    open_time as latest_open_time,
    open,
    high,
    low,
    close,
    volume,
    quote_volume,
    trade_count,
    is_closed,
    now() as updated_at
FROM trade.candles
WHERE open_time = (
    SELECT max(open_time)
    FROM trade.candles AS c2
    WHERE c2.symbol = trade.candles.symbol
      AND c2.interval = trade.candles.interval
      AND c2.source = trade.candles.source
);

-- ============================================
-- USAGE EXAMPLES
-- ============================================

-- Query latest candle (super fast with materialized view):
-- SELECT * FROM trade.candles_latest WHERE symbol = 'BTCUSDT' AND interval = '1m' AND source = 'aggregated';

-- Query historical candles (efficient with indexes):
-- SELECT * FROM trade.candles WHERE symbol = 'BTCUSDT' AND interval = '1h' AND open_time >= now() - INTERVAL 7 DAY ORDER BY open_time DESC LIMIT 168;

-- Check table size:
-- SELECT
--     table,
--     formatReadableSize(sum(bytes)) as size,
--     formatReadableQuantity(sum(rows)) as rows,
--     count() as parts
-- FROM system.parts
-- WHERE table = 'candles' AND active
-- GROUP BY table;

-- Force merge partitions (run weekly to optimize):
-- OPTIMIZE TABLE trade.candles FINAL;

-- Force TTL check (delete old data immediately):
-- OPTIMIZE TABLE trade.candles FINAL;
