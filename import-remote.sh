#!/bin/bash

# Import Binance data by running the Go importer on Azure server
# This is MUCH faster than running locally!

set -e

SSH_KEY="$HOME/Downloads/Azure-NYYU.pem"
SERVER="azureuser@market.nyyu.io"
REMOTE_DIR="/opt/nyyu-market"

echo "================================================"
echo "  Remote Binance Data Import (Fast)"
echo "================================================"
echo ""

# Parse arguments or prompt for input
if [ $# -eq 0 ]; then
    # Interactive mode
    echo "Interactive Mode"
    echo ""

    read -p "Symbols (space-separated, e.g., BTCUSDT ETHUSDT): " SYMBOLS
    read -p "Intervals (default: all): " INTERVALS
    INTERVALS=${INTERVALS:-"all"}
    read -p "Start Year (default: 2018): " START_YEAR
    START_YEAR=${START_YEAR:-2018}
    read -p "Start Month (default: 01): " START_MONTH
    START_MONTH=${START_MONTH:-01}
    read -p "End Year (default: 2025): " END_YEAR
    END_YEAR=${END_YEAR:-2025}
    read -p "End Month (default: 10): " END_MONTH
    END_MONTH=${END_MONTH:-10}
    read -p "Workers (parallel downloads, default: 10): " WORKERS
    WORKERS=${WORKERS:-10}
else
    # Use provided arguments
    SYMBOLS=$1
    INTERVALS=${2:-"all"}  # Default to all intervals for complete historical data
    START_YEAR=${3:-2018}
    START_MONTH=${4:-01}
    END_YEAR=${5:-2025}
    END_MONTH=${6:-10}
    WORKERS=${7:-10}
fi

# Convert space-separated symbols to array
IFS=' ' read -ra SYMBOL_ARRAY <<< "$SYMBOLS"

echo ""
echo "📊 Import Configuration:"
echo "  Symbols:    ${SYMBOL_ARRAY[@]}"
echo "  Count:      ${#SYMBOL_ARRAY[@]}"
echo "  Intervals:  $INTERVALS"
echo "  Period:     $START_YEAR-$START_MONTH to $END_YEAR-$END_MONTH"
echo "  Workers:    $WORKERS per symbol"
echo ""

# Build the importer for Linux (cross-compile)
echo "🔨 Building Go importer for Linux..."
cd "$(dirname "$0")"
# Set CGO_ENABLED=0 for pure Go binary without C dependencies
# This ensures consistent behavior across different systems
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/importer-linux cmd/importer/main.go
echo "✅ Build complete"
echo ""

# Upload importer to Azure
echo "📤 Uploading importer to Azure..."
scp -i "$SSH_KEY" -o StrictHostKeyChecking=no bin/importer-linux "$SERVER:$REMOTE_DIR/importer"
echo "✅ Upload complete"
echo ""

# Run importer on Azure for each symbol
echo "🚀 Running importer on Azure server..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

for SYMBOL in "${SYMBOL_ARRAY[@]}"; do
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "📊 Importing: $SYMBOL"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""

    ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SERVER" <<REMOTE_SCRIPT
cd $REMOTE_DIR

# Make executable
chmod +x importer

# Set environment variables to connect via localhost (ClickHouse is exposed on port 9000)
export CLICKHOUSE_HOST=localhost
export CLICKHOUSE_PORT=9000
export CLICKHOUSE_DATABASE=trade
export CLICKHOUSE_USERNAME=default
export CLICKHOUSE_PASSWORD=
export TZ=UTC

# Run importer for this symbol
./importer \
  -symbol="$SYMBOL" \
  -intervals="$INTERVALS" \
  -start-year=$START_YEAR \
  -start-month=$START_MONTH \
  -end-year=$END_YEAR \
  -end-month=$END_MONTH \
  -workers=$WORKERS
REMOTE_SCRIPT

    echo ""
    echo "✅ $SYMBOL import completed!"
done

# Clean up importer on server
ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SERVER" "rm -f $REMOTE_DIR/importer"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "✅ All symbols imported successfully!"
echo ""
