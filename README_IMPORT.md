# 📥 Binance Historical Data Import

Import historical candle data from Binance into your Azure ClickHouse database.

## 🚀 Quick Start

```bash
./import-binance-to-azure.sh
```

That's it! The script will guide you through the process.

## ✨ New Feature: Multi-Interval Support

You can now import **multiple intervals at once**:

```
Enter interval(s) or 'all': 1h,4h,1d
```

This will import:
- 1-hour candles
- 4-hour candles
- 1-day candles

All in a single command!

## 📋 Menu Options

**Option 1**: Import single month
- Example: BTCUSDT, 1h,4h,1d for January 2024

**Option 2**: Import date range ⭐ Recommended
- Example: BTCUSDT, 1h,4h,1d from Jan-Oct 2024

**Option 3**: Batch import multiple symbols
- Example: BTCUSDT,ETHUSDT,BNBUSDT with 1h,4h,1d for 2024

## 💡 Interval Shortcuts

| Input | Result |
|-------|--------|
| `1h` | Single interval (1 hour) |
| `1h,4h,1d` | Multiple intervals |
| `all` | All intervals (1m,3m,5m,15m,30m,1h,2h,4h,6h,8h,12h,1d,3d,1w,1M) |

## 📊 Example Usage

### Import Bitcoin with 3 intervals
```
Option: 2
Symbol: BTCUSDT
Interval: 1h,4h,1d
Period: 2024-01 to 2025-10
```

### Import Top 5 coins
```
Option: 3
Symbols: BTCUSDT,ETHUSDT,BNBUSDT,SOLUSDT,XRPUSDT
Interval: 1h,4h,1d
Period: 2024-01 to 2025-10
```

### Quick test
```
Option: 1
Symbol: BTCUSDT
Interval: 1d
Month: 2024-01
```

## ✅ What's Imported

- Source: `aggregated` (matches your aggregator)
- Contract: `spot` market
- Status: `is_closed=1` (complete candles)
- Direct to Azure ClickHouse

## 📖 Full Documentation

See [IMPORT_GUIDE.md](IMPORT_GUIDE.md) for detailed documentation.

## 🎯 Popular Symbols

```
BTCUSDT  ETHUSDT  BNBUSDT  SOLUSDT  XRPUSDT
ADAUSDT  DOGEUSDT AVAXUSDT DOTUSDT  MATICUSDT
```

## ⚡ Performance

- **1d**: ~2 sec/month
- **1h**: ~5 sec/month
- **15m**: ~15 sec/month
- **3 intervals**: 3× the time

## 🔍 Verify Data

```bash
ssh -i ~/Downloads/Azure-NYYU.pem azureuser@market.nyyu.io
cd /opt/nyyu-market
docker compose exec clickhouse clickhouse-client

SELECT symbol, interval, count()
FROM trade.candles
WHERE source='aggregated'
GROUP BY symbol, interval;
```

## 🎉 Ready to Go!

```bash
./import-binance-to-azure.sh
```

Start importing your historical data now!
