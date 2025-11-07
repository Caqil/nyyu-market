-- ============================================
-- UPDATE TTL TO PRESERVE 10 YEARS OF HISTORICAL DATA
-- Migration to extend retention for long-term intervals
-- ============================================

-- Modify TTL settings to keep historical data back to 2018
-- This allows access to 10 years of historical candle data for daily, weekly, and longer intervals
ALTER TABLE trade.candles
MODIFY TTL
    open_time + INTERVAL 90 DAY DELETE WHERE interval IN ('1m', '3m', '5m'),
    open_time + INTERVAL 180 DAY DELETE WHERE interval IN ('15m', '30m', '1h', '2h'),
    open_time + INTERVAL 10 YEAR DELETE WHERE interval IN ('4h', '6h', '8h', '12h', '1d', '3d', '1w', '1M');

-- Note: This change will:
-- 1. Preserve existing historical data that would have been deleted under the old 2-year TTL
-- 2. Allow importing data back to 2018 for long-term analysis
-- 3. Increase storage requirements for daily/weekly candles, but these are relatively small
-- 4. Short-term intervals (1m-2h) still have aggressive TTL to manage storage costs
