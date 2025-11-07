# Binance Historical Data Import Guide

## Quick Start

```bash
./import-binance-to-azure.sh
```

This single script handles all import scenarios with an interactive menu.

## Features

✅ **Multi-Interval Support** - Import multiple intervals at once (1h,4h,1d)
✅ **Interactive Menu** - Easy-to-use interface
✅ **Single Month Import** - Import specific month
✅ **Date Range Import** - Import multiple months
✅ **Batch Import** - Import multiple symbols
✅ **Auto-Download** - Downloads from Binance public data
✅ **Azure Integration** - Direct import to your Azure ClickHouse
✅ **Progress Tracking** - Real-time status updates

## Usage

### Interactive Menu

Run the script:
```bash
./import-binance-to-azure.sh
```

You'll see 4 options:

#### Option 1: Import Single Month
```
Select: 1
Symbol: BTCUSDT
Interval: 1h,4h,1d  (or "all" for 1h,4h,1d,1w)
Year: 2024
Month: 01
```

#### Option 2: Import Date Range (Recommended)
```
Select: 2
Symbol: BTCUSDT
Interval: 1h,4h,1d  (or "all" for 1h,4h,1d,1w)
Start: 2024-01
End: 2025-10
```

#### Option 3: Batch Import Multiple Symbols
```
Select: 3
Symbols: BTCUSDT,ETHUSDT,BNBUSDT
Interval: 1h,4h,1d  (or "all" for 1h,4h,1d,1w)
Start: 2024-01
End: 2025-10
```

## Interval Options

When asked for interval, you can enter:

### Single Interval
```
1h
```

### Multiple Intervals (Recommended)
```
1h,4h,1d
```

### All Intervals
```
all
```
This imports all available intervals: 1m, 3m, 5m, 15m, 30m, 1h, 2h, 4h, 6h, 8h, 12h, 1d, 3d, 1w, 1M

### Available Intervals
- Minutes: 1m, 3m, 5m, 15m, 30m
- Hours: 1h, 2h, 4h, 6h, 8h, 12h
- Days: 1d, 3d
- Week: 1w
- Month: 1M

## Common Use Cases

### 1. Import Bitcoin with Multiple Intervals
```
Option: 2
Symbol: BTCUSDT
Interval: 1h,4h,1d
Start: 2024-01
End: 2024-10
```
Result: 3 intervals × 10 months = 30 import operations

### 2. Import Top 5 Coins
```
Option: 3
Symbols: BTCUSDT,ETHUSDT,BNBUSDT,SOLUSDT,XRPUSDT
Interval: 1h,4h,1d
Start: 2024-01
End: 2024-10
```
Result: 5 symbols × 3 intervals × 10 months = 150 import operations

### 3. Import All Common Intervals
```
Option: 2
Symbol: BTCUSDT
Interval: all
Start: 2024-01
End: 2024-10
```
Result: Imports 1h, 4h, 1d, 1w for all 10 months

### 4. Quick Test Import
```
Option: 1
Symbol: BTCUSDT
Interval: 1d
Year: 2024
Month: 01
```
Result: Just 31 candles (fastest test)

## Popular Symbols

```
BTCUSDT   ETHUSDT   BNBUSDT   SOLUSDT   XRPUSDT
ADAUSDT   DOGEUSDT  AVAXUSDT  DOTUSDT   MATICUSDT
LINKUSDT  UNIUSDT   ATOMUSDT  LTCUSDT   ETCUSDT
```

## What Happens During Import

1. **Download** - Fetches ZIP from Binance (https://data.binance.vision)
2. **Extract** - Unzips CSV files
3. **Convert** - Transforms to ClickHouse format with `source='aggregated'`
4. **Upload** - Transfers to Azure server via SSH
5. **Import** - Loads into ClickHouse via Docker
6. **Optimize** - Runs OPTIMIZE TABLE to merge data
7. **Cleanup** - Removes temporary files

## Data Format

All imported data is marked with:
- `source='aggregated'` - Matches your aggregator format
- `is_closed=1` - Historical complete candles
- `contract_type='spot'` - Spot market data

## Performance

Typical import speeds:
- **1d interval**: ~1-2 seconds per month (~30 candles)
- **1h interval**: ~3-5 seconds per month (~720 candles)
- **15m interval**: ~10-15 seconds per month (~2,880 candles)
- **1m interval**: ~30-60 seconds per month (~43,200 candles)

## Verify Imported Data

After importing, check your data:

```bash
# SSH to Azure
ssh -i ~/Downloads/Azure-NYYU.pem azureuser@market.nyyu.io

# Connect to ClickHouse
cd /opt/nyyu-market
docker compose exec clickhouse clickhouse-client

# Check data
SELECT
    symbol,
    interval,
    count() as candles,
    min(open_time) as earliest,
    max(open_time) as latest
FROM trade.candles
WHERE source = 'aggregated'
GROUP BY symbol, interval
ORDER BY symbol, interval;
```

## Troubleshooting

### "File not available"
The month doesn't exist on Binance yet. Try earlier months.

### "SSH connection failed"
```bash
chmod 600 ~/Downloads/Azure-NYYU.pem
```

### Import seems slow
Large datasets take time. Multiple intervals will run sequentially.

### Data not showing
Wait for OPTIMIZE to complete or run manually:
```bash
docker compose exec clickhouse clickhouse-client \
  --query="OPTIMIZE TABLE trade.candles FINAL"
```

## Tips

1. **Start with daily data** - Fastest, smallest size
2. **Use multiple intervals** - Get 1h,4h,1d in one command
3. **Import recent data first** - 2024 data more useful than old data
4. **Avoid 1-minute data** unless needed - Very large dataset
5. **Use "all" option** - Quick way to get common intervals

## Example Session

```
$ ./import-binance-to-azure.sh
================================================
  Binance Historical Data Import to Azure
================================================

✅ SSH connection OK
✅ Ready to import!

Import Options:
1) Import single month (e.g., BTCUSDT 1m 2024-01)
2) Import date range (e.g., BTCUSDT 1m 2024-01 to 2024-12)
3) Import multiple symbols (batch import)
4) Quit

Select option [1-4]: 2

Enter symbol (e.g., BTCUSDT): BTCUSDT

Available intervals: 1m, 3m, 5m, 15m, 30m, 1h, 2h, 4h, 6h, 8h, 12h, 1d, 3d, 1w, 1M

Interval options:
  • Single: 1h
  • Multiple: 1h,4h,1d
  • all: 1h,4h,1d,1w

Enter interval(s) or 'all': 1h,4h,1d

Enter start year (e.g., 2024): 2024
Enter start month (e.g., 01): 01

Enter end year (e.g., 2024): 2024
Enter end month (e.g., 12): 10

Importing: BTCUSDT (3 intervals) from 2024-01 to 2024-10

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Processing interval: 1h
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

1h: 2024-01
Downloading: BTCUSDT-1h-2024-01.zip
  ✅ Downloaded successfully
...
```

## Need Help?

The script is self-explanatory with clear prompts. Just follow the questions!
