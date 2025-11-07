#!/bin/bash

###############################################################################
# Import Binance Historical Candle Data to Azure ClickHouse
# Downloads data from https://data.binance.vision and imports to Azure
###############################################################################

set -e

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${BLUE}================================================${NC}"
echo -e "${BLUE}  Binance Historical Data Import to Azure${NC}"
echo -e "${BLUE}================================================${NC}"
echo ""

###############################################################################
# Configuration
###############################################################################

# SSH Configuration
SSH_KEY="$HOME/Downloads/Azure-NYYU.pem"
SERVER="azureuser@market.nyyu.io"

# ClickHouse Configuration (from your Azure server)
CLICKHOUSE_HOST="clickhouse"
CLICKHOUSE_PORT="9000"
CLICKHOUSE_DB="trade"
CLICKHOUSE_USER="default"
CLICKHOUSE_PASSWORD=""

# Binance Data URL
BINANCE_DATA_URL="https://data.binance.vision/data/spot/monthly/klines"

# Local working directory
WORK_DIR="./binance_import_temp"
mkdir -p "$WORK_DIR"

###############################################################################
# Functions
###############################################################################

# Check if SSH key exists
check_ssh_key() {
    if [ ! -f "$SSH_KEY" ]; then
        echo -e "${RED}❌ SSH key not found: $SSH_KEY${NC}"
        echo "Please make sure Azure-NYYU.pem is in ~/Downloads/"
        exit 1
    fi
    chmod 600 "$SSH_KEY"
}

# Test SSH connection
test_ssh_connection() {
    echo "Testing SSH connection to Azure server..."
    if ! ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=10 "$SERVER" "echo 'Connection OK'" &>/dev/null; then
        echo -e "${RED}❌ Cannot connect to Azure server${NC}"
        echo "Please check your SSH configuration"
        exit 1
    fi
    echo -e "${GREEN}✅ SSH connection OK${NC}"
}

# Download monthly data from Binance with daily fallback
download_binance_data() {
    local symbol=$1
    local interval=$2
    local year=$3
    local month=$4

    local filename="${symbol}-${interval}-${year}-${month}.zip"
    local url="${BINANCE_DATA_URL}/${symbol}/${interval}/${filename}"

    echo "Downloading: $filename"

    if [ -f "$WORK_DIR/$filename" ]; then
        echo "  ⏭️  File already exists, skipping download"
        return 0
    fi

    # Try monthly file first
    if curl -f -L -o "$WORK_DIR/$filename" "$url" 2>/dev/null; then
        echo -e "  ${GREEN}✅ Downloaded monthly file${NC}"
        return 0
    else
        echo -e "  ${YELLOW}⚠️  Monthly file not available, trying daily files...${NC}"

        # Try downloading daily files as fallback
        if download_daily_files "$symbol" "$interval" "$year" "$month"; then
            return 0
        else
            echo -e "  ${RED}❌ No data available (neither monthly nor daily)${NC}"
            return 1
        fi
    fi
}

# Download and merge daily files for a month
download_daily_files() {
    local symbol=$1
    local interval=$2
    local year=$3
    local month=$4

    local daily_url="https://data.binance.vision/data/spot/daily/klines/${symbol}/${interval}"
    local merged_csv="$WORK_DIR/${symbol}-${interval}-${year}-${month}.csv"
    local merged_zip="$WORK_DIR/${symbol}-${interval}-${year}-${month}.zip"

    # Calculate days in month
    local days_in_month
    case $month in
        01|03|05|07|08|10|12) days_in_month=31 ;;
        04|06|09|11) days_in_month=30 ;;
        02)
            # Leap year check
            if [ $((year % 4)) -eq 0 ] && ([ $((year % 100)) -ne 0 ] || [ $((year % 400)) -eq 0 ]); then
                days_in_month=29
            else
                days_in_month=28
            fi
            ;;
    esac

    local daily_success=0
    local temp_files=()

    # Download each day
    for day in $(seq -f "%02g" 1 $days_in_month); do
        local date="${year}-${month}-${day}"
        local daily_file="${symbol}-${interval}-${date}.zip"
        local daily_csv="${symbol}-${interval}-${date}.csv"

        if curl -f -L -s -o "$WORK_DIR/$daily_file" "${daily_url}/${daily_file}" 2>/dev/null; then
            # Extract daily file
            unzip -q -o "$WORK_DIR/$daily_file" -d "$WORK_DIR" 2>/dev/null

            if [ -f "$WORK_DIR/$daily_csv" ]; then
                temp_files+=("$WORK_DIR/$daily_csv")
                ((daily_success++))
            fi
            rm -f "$WORK_DIR/$daily_file"
        fi
    done

    # If we got at least some daily files, merge them
    if [ $daily_success -gt 0 ]; then
        echo "  ${GREEN}✅ Downloaded $daily_success daily files${NC}"

        # Merge all CSV files
        cat "${temp_files[@]}" > "$merged_csv" 2>/dev/null

        # Clean up temp files
        rm -f "${temp_files[@]}"

        # Create a zip file to maintain compatibility with the rest of the script
        zip -q -j "$merged_zip" "$merged_csv"
        rm -f "$merged_csv"

        return 0
    else
        echo "  ${RED}❌ No daily files available${NC}"
        return 1
    fi
}

# Extract and convert CSV to ClickHouse format
convert_csv_to_clickhouse() {
    local csv_file=$1
    local symbol=$2
    local interval=$3
    local output_file=$4

    echo "Converting CSV: $(basename $csv_file)"

    # Binance CSV format:
    # 0: open_time, 1: open, 2: high, 3: low, 4: close, 5: volume,
    # 6: close_time, 7: quote_volume, 8: trade_count,
    # 9: taker_buy_base_volume, 10: taker_buy_quote_volume, 11: ignore

    # Convert to ClickHouse INSERT format
    # Remove microseconds from timestamps and add required fields
    awk -F',' -v symbol="$symbol" -v interval="$interval" '{
        # Convert microsecond timestamps to millisecond DateTime64(3)
        # Binance timestamps are in microseconds, remove last 3 digits for milliseconds
        open_time = substr($1, 1, length($1)-3)
        close_time = substr($7, 1, length($7)-3)

        # Build INSERT statement with source=aggregated
        printf("INSERT INTO candles FORMAT Values ('\''%s'\'', '\''%s'\'', %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, '\''aggregated'\'', 1, '\''spot'\'', now(), now());\n",
            symbol, interval,
            open_time, close_time,
            $2, $3, $4, $5,      # open, high, low, close
            $6, $8, $9,          # volume, quote_volume, trade_count
            $10, $11)            # taker_buy_base_volume, taker_buy_quote_volume
    }' "$csv_file" > "$output_file"

    echo -e "  ${GREEN}✅ Converted $(wc -l < $output_file) candles${NC}"
}

# Import SQL file to Azure ClickHouse
import_to_clickhouse() {
    local sql_file=$1
    local symbol=$2
    local interval=$3
    local year=$4
    local month=$5

    echo "Importing to Azure ClickHouse: $symbol $interval $year-$month"

    # Create temp script for remote execution
    local remote_script="/tmp/import_candles_${symbol}_${interval}_${year}_${month}.sh"
    local remote_sql="/tmp/import_candles_${symbol}_${interval}_${year}_${month}.sql"

    # Upload SQL file to server
    scp -i "$SSH_KEY" -o StrictHostKeyChecking=no -q "$sql_file" "$SERVER:$remote_sql"

    # Create import script on server
    ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SERVER" "cat > $remote_script" <<REMOTE_SCRIPT
#!/bin/bash
set -e

# Use docker to connect to ClickHouse
DOCKER_CMD="docker"
if ! docker ps &> /dev/null; then
    DOCKER_CMD="sudo docker"
fi

cd /opt/nyyu-market

# Check if ClickHouse is running
if ! \$DOCKER_CMD compose ps clickhouse | grep -q "Up"; then
    echo "ERROR: ClickHouse is not running"
    exit 1
fi

# Get ClickHouse container name
CONTAINER=\$(\$DOCKER_CMD compose ps -q clickhouse)

# Count total INSERT statements for progress
TOTAL_ROWS=\$(grep -c "^INSERT" $remote_sql)
echo "Importing \$TOTAL_ROWS candles..."

# Import data using clickhouse-client
# Split into chunks and show progress
CHUNK_SIZE=5000
if [ \$TOTAL_ROWS -gt \$CHUNK_SIZE ]; then
    echo "Processing in chunks for better progress tracking..."
    CHUNKS=\$(( (\$TOTAL_ROWS + \$CHUNK_SIZE - 1) / \$CHUNK_SIZE ))
    for i in \$(seq 1 \$CHUNKS); do
        START=\$(( (\$i - 1) * \$CHUNK_SIZE + 1 ))
        END=\$(( \$i * \$CHUNK_SIZE ))
        sed -n "\${START},\${END}p" $remote_sql | \$DOCKER_CMD exec -i \$CONTAINER clickhouse-client \\
            --host=$CLICKHOUSE_HOST \\
            --port=$CLICKHOUSE_PORT \\
            --database=$CLICKHOUSE_DB \\
            --user=$CLICKHOUSE_USER 2>&1 | grep -v "^$" || true
        PROGRESS=\$(( \$i * 100 / \$CHUNKS ))
        echo "  [\$PROGRESS%] Processed \$(( \$i * \$CHUNK_SIZE )) / \$TOTAL_ROWS candles"
    done
else
    cat $remote_sql | \$DOCKER_CMD exec -i \$CONTAINER clickhouse-client \\
        --host=$CLICKHOUSE_HOST \\
        --port=$CLICKHOUSE_PORT \\
        --database=$CLICKHOUSE_DB \\
        --user=$CLICKHOUSE_USER 2>&1 | grep -v "^$" || true
fi

# Cleanup
rm -f $remote_sql $remote_script
echo "✅ Import completed"
REMOTE_SCRIPT

    # Execute import on server
    ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SERVER" "bash $remote_script"

    echo -e "  ${GREEN}✅ Imported to Azure ClickHouse${NC}"
}

# Optimize table after import
optimize_table() {
    echo "Optimizing ClickHouse table..."

    ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SERVER" <<'REMOTE_OPTIMIZE'
set -e

DOCKER_CMD="docker"
if ! docker ps &> /dev/null; then
    DOCKER_CMD="sudo docker"
fi

cd /opt/nyyu-market
CONTAINER=$($DOCKER_CMD compose ps -q clickhouse)

$DOCKER_CMD exec $CONTAINER clickhouse-client \
    --query="OPTIMIZE TABLE trade.candles FINAL"

echo "✅ Table optimized"
REMOTE_OPTIMIZE
}

###############################################################################
# Interactive Menu
###############################################################################

show_menu() {
    echo ""
    echo -e "${BLUE}Import Options:${NC}"
    echo "1) Import single month (e.g., BTCUSDT 1h,4h,1d for 2025-01)"
    echo "2) Import date range (e.g., BTCUSDT all from 2024-01 to 2025-10)"
    echo "3) Import multiple symbols (batch import)"
    echo "4) Quit"
    echo ""
    echo -e "${YELLOW}Note: 2025 data available up to October${NC}"
    echo ""
}

# Single month import (supports multiple intervals)
import_single_month() {
    echo ""
    read -p "Enter symbol (e.g., BTCUSDT): " SYMBOL

    # Validate symbol
    if [[ -z "$SYMBOL" ]]; then
        echo -e "${RED}❌ Symbol is required${NC}"
        return 1
    fi

    echo ""
    echo "Available intervals: 1m, 3m, 5m, 15m, 30m, 1h, 2h, 4h, 6h, 8h, 12h, 1d, 3d, 1w, 1M"
    echo ""
    echo "Interval options:"
    echo "  • Single: 1h"
    echo "  • Multiple: 1h,4h,1d"
    echo "  • all: All intervals (1m,3m,5m,15m,30m,1h,2h,4h,6h,8h,12h,1d,3d,1w,1M)"
    echo ""
    read -p "Enter interval(s) or 'all': " INTERVAL_INPUT

    # Validate interval
    if [[ -z "$INTERVAL_INPUT" ]]; then
        echo -e "${RED}❌ Interval is required${NC}"
        return 1
    fi

    if [ "$INTERVAL_INPUT" = "all" ]; then
        INTERVALS="1m,3m,5m,15m,30m,1h,2h,4h,6h,8h,12h,1d,3d,1w,1M"
    else
        INTERVALS="$INTERVAL_INPUT"
    fi

    echo ""
    read -p "Enter year (default: 2024): " YEAR
    YEAR=${YEAR:-2024}

    read -p "Enter month (default: 01): " MONTH
    MONTH=${MONTH:-01}

    # Convert to array
    IFS=',' read -ra INTERVAL_ARRAY <<< "$INTERVALS"

    for INTERVAL in "${INTERVAL_ARRAY[@]}"; do
        INTERVAL=$(echo "$INTERVAL" | xargs)

        echo ""
        echo -e "${YELLOW}Importing: $SYMBOL $INTERVAL for $YEAR-$MONTH${NC}"
        echo ""

        # Download
        if ! download_binance_data "$SYMBOL" "$INTERVAL" "$YEAR" "$MONTH"; then
            echo -e "${RED}Failed to download data for $INTERVAL${NC}"
            continue
        fi

        # Extract
        local zip_file="$WORK_DIR/${SYMBOL}-${INTERVAL}-${YEAR}-${MONTH}.zip"
        local csv_file="$WORK_DIR/${SYMBOL}-${INTERVAL}-${YEAR}-${MONTH}.csv"

        if [ ! -f "$csv_file" ]; then
            echo "Extracting ZIP..."
            unzip -q -o "$zip_file" -d "$WORK_DIR"
        fi

        # Convert
        local sql_file="$WORK_DIR/${SYMBOL}-${INTERVAL}-${YEAR}-${MONTH}.sql"
        convert_csv_to_clickhouse "$csv_file" "$SYMBOL" "$INTERVAL" "$sql_file"

        # Import
        import_to_clickhouse "$sql_file" "$SYMBOL" "$INTERVAL" "$YEAR" "$MONTH"

        # Cleanup
        rm -f "$csv_file" "$sql_file"
    done

    echo ""
    echo -e "${GREEN}✅ Import completed successfully!${NC}"
}

# Date range import
import_date_range() {
    echo ""
    read -p "Enter symbol (e.g., BTCUSDT): " SYMBOL

    # Validate symbol
    if [[ -z "$SYMBOL" ]]; then
        echo -e "${RED}❌ Symbol is required${NC}"
        return 1
    fi

    echo ""
    echo "Available intervals: 1m, 3m, 5m, 15m, 30m, 1h, 2h, 4h, 6h, 8h, 12h, 1d, 3d, 1w, 1M"
    echo ""
    echo "Interval options:"
    echo "  • Single: 1h"
    echo "  • Multiple: 1h,4h,1d"
    echo "  • all: All intervals (1m,3m,5m,15m,30m,1h,2h,4h,6h,8h,12h,1d,3d,1w,1M)"
    echo ""
    read -p "Enter interval(s) or 'all': " INTERVAL_INPUT

    # Validate interval
    if [[ -z "$INTERVAL_INPUT" ]]; then
        echo -e "${RED}❌ Interval is required${NC}"
        return 1
    fi

    if [ "$INTERVAL_INPUT" = "all" ]; then
        INTERVALS="1m,3m,5m,15m,30m,1h,2h,4h,6h,8h,12h,1d,3d,1w,1M"
    else
        INTERVALS="$INTERVAL_INPUT"
    fi

    echo ""
    read -p "Enter start year (default: 2018): " START_YEAR
    START_YEAR=${START_YEAR:-2018}

    read -p "Enter start month (default: 01): " START_MONTH
    START_MONTH=${START_MONTH:-01}

    echo ""
    read -p "Enter end year (default: 2025): " END_YEAR
    END_YEAR=${END_YEAR:-2025}

    read -p "Enter end month (default: 10): " END_MONTH
    END_MONTH=${END_MONTH:-10}

    # Convert to array
    IFS=',' read -ra INTERVAL_ARRAY <<< "$INTERVALS"

    echo ""
    echo -e "${YELLOW}Importing: $SYMBOL (${#INTERVAL_ARRAY[@]} intervals) from $START_YEAR-$START_MONTH to $END_YEAR-$END_MONTH${NC}"
    echo ""

    success_count=0
    fail_count=0

    for INTERVAL in "${INTERVAL_ARRAY[@]}"; do
        INTERVAL=$(echo "$INTERVAL" | xargs)

        echo ""
        echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
        echo -e "${BLUE}Processing interval: $INTERVAL${NC}"
        echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

        # Batch SQL file for this interval
        local batch_sql_file="$WORK_DIR/${SYMBOL}-${INTERVAL}-batch.sql"
        rm -f "$batch_sql_file"

        # PHASE 1: Download and convert all months
        echo ""
        echo -e "${YELLOW}📥 Phase 1: Downloading and converting...${NC}"

        current_year=$START_YEAR
        current_month=$((10#$START_MONTH))
        end_year=$END_YEAR
        end_month=$((10#$END_MONTH))

        month_count=0

        while true; do
            month_str=$(printf "%02d" $current_month)
            echo -n "$current_year-$month_str "

            # Download
            if download_binance_data "$SYMBOL" "$INTERVAL" "$current_year" "$month_str"; then
                # Extract
                local zip_file="$WORK_DIR/${SYMBOL}-${INTERVAL}-${current_year}-${month_str}.zip"
                local csv_file="$WORK_DIR/${SYMBOL}-${INTERVAL}-${current_year}-${month_str}.csv"

                if [ ! -f "$csv_file" ]; then
                    unzip -q -o "$zip_file" -d "$WORK_DIR"
                fi

                # Convert and append to batch file
                local sql_file="$WORK_DIR/${SYMBOL}-${INTERVAL}-${current_year}-${month_str}.sql"
                convert_csv_to_clickhouse "$csv_file" "$SYMBOL" "$INTERVAL" "$sql_file"
                cat "$sql_file" >> "$batch_sql_file"

                ((month_count++))
                rm -f "$csv_file" "$sql_file"
            fi

            # Check if we've reached the end
            if [ $current_year -eq $end_year ] && [ $current_month -eq $end_month ]; then
                break
            fi

            # Increment month
            current_month=$((current_month + 1))
            if [ $current_month -gt 12 ]; then
                current_month=1
                current_year=$((current_year + 1))
            fi
        done

        echo ""
        echo ""

        # PHASE 2: Bulk import to ClickHouse
        if [ $month_count -gt 0 ] && [ -f "$batch_sql_file" ]; then
            echo -e "${YELLOW}⚡ Phase 2: Bulk importing $month_count months to ClickHouse...${NC}"

            if import_to_clickhouse "$batch_sql_file" "$SYMBOL" "$INTERVAL" "batch" ""; then
                success_count=$((success_count + month_count))
                echo -e "${GREEN}✅ Successfully imported $month_count months for $INTERVAL${NC}"
            else
                fail_count=$((fail_count + month_count))
                echo -e "${RED}❌ Failed to import $month_count months for $INTERVAL${NC}"
            fi

            rm -f "$batch_sql_file"
        else
            echo -e "${RED}❌ No data downloaded for $INTERVAL${NC}"
        fi
    done

    echo ""
    echo -e "${GREEN}================================================${NC}"
    echo -e "${GREEN}Import Summary:${NC}"
    echo -e "${GREEN}  Success: $success_count${NC}"
    echo -e "${YELLOW}  Failed: $fail_count${NC}"
    echo -e "${GREEN}================================================${NC}"

    # Optimize table
    echo ""
    optimize_table
}

# Batch import multiple symbols
import_batch() {
    echo ""
    echo "Enter symbols separated by comma (e.g., BTCUSDT,ETHUSDT,BNBUSDT):"
    read -p "Symbols: " SYMBOLS_INPUT

    # Validate symbols
    if [[ -z "$SYMBOLS_INPUT" ]]; then
        echo -e "${RED}❌ Symbols are required${NC}"
        return 1
    fi

    echo ""
    echo "Available intervals: 1m, 3m, 5m, 15m, 30m, 1h, 2h, 4h, 6h, 8h, 12h, 1d, 3d, 1w, 1M"
    echo ""
    echo "Interval options:"
    echo "  • Single: 1h"
    echo "  • Multiple: 1h,4h,1d"
    echo "  • all: All intervals (1m,3m,5m,15m,30m,1h,2h,4h,6h,8h,12h,1d,3d,1w,1M)"
    echo ""
    read -p "Enter interval(s) or 'all': " INTERVAL_INPUT

    # Validate interval
    if [[ -z "$INTERVAL_INPUT" ]]; then
        echo -e "${RED}❌ Interval is required${NC}"
        return 1
    fi

    if [ "$INTERVAL_INPUT" = "all" ]; then
        INTERVALS="1m,3m,5m,15m,30m,1h,2h,4h,6h,8h,12h,1d,3d,1w,1M"
    else
        INTERVALS="$INTERVAL_INPUT"
    fi

    echo ""
    read -p "Enter start year (default: 2018): " START_YEAR
    START_YEAR=${START_YEAR:-2018}

    read -p "Enter start month (default: 01): " START_MONTH
    START_MONTH=${START_MONTH:-01}

    echo ""
    read -p "Enter end year (default: 2025): " END_YEAR
    END_YEAR=${END_YEAR:-2025}

    read -p "Enter end month (default: 10): " END_MONTH
    END_MONTH=${END_MONTH:-10}

    # Convert comma-separated string to array
    IFS=',' read -ra SYMBOLS <<< "$SYMBOLS_INPUT"

    # Convert to arrays
    IFS=',' read -ra INTERVAL_ARRAY <<< "$INTERVALS"

    echo ""
    echo -e "${YELLOW}Batch importing ${#SYMBOLS[@]} symbols with ${#INTERVAL_ARRAY[@]} intervals...${NC}"
    echo ""

    total_success=0
    total_fail=0

    for SYMBOL in "${SYMBOLS[@]}"; do
        # Trim whitespace
        SYMBOL=$(echo "$SYMBOL" | xargs)

        echo ""
        echo -e "${BLUE}================================================${NC}"
        echo -e "${BLUE}Processing Symbol: $SYMBOL${NC}"
        echo -e "${BLUE}================================================${NC}"

        for INTERVAL in "${INTERVAL_ARRAY[@]}"; do
            INTERVAL=$(echo "$INTERVAL" | xargs)

            echo ""
            echo -e "${YELLOW}Interval: $INTERVAL${NC}"

            # Generate month list
            current_year=$START_YEAR
            current_month=$((10#$START_MONTH))

            end_year=$END_YEAR
            end_month=$((10#$END_MONTH))

            while true; do
                month_str=$(printf "%02d" $current_month)

                echo ""
                echo -e "${YELLOW}$SYMBOL $INTERVAL: $current_year-$month_str${NC}"

                # Download
                if download_binance_data "$SYMBOL" "$INTERVAL" "$current_year" "$month_str"; then
                    # Extract
                    local zip_file="$WORK_DIR/${SYMBOL}-${INTERVAL}-${current_year}-${month_str}.zip"
                    local csv_file="$WORK_DIR/${SYMBOL}-${INTERVAL}-${current_year}-${month_str}.csv"

                    if [ ! -f "$csv_file" ]; then
                        unzip -q -o "$zip_file" -d "$WORK_DIR"
                    fi

                    # Convert
                    local sql_file="$WORK_DIR/${SYMBOL}-${INTERVAL}-${current_year}-${month_str}.sql"
                    convert_csv_to_clickhouse "$csv_file" "$SYMBOL" "$INTERVAL" "$sql_file"

                    # Import
                    if import_to_clickhouse "$sql_file" "$SYMBOL" "$INTERVAL" "$current_year" "$month_str"; then
                        ((total_success++))
                    else
                        ((total_fail++))
                    fi

                    # Cleanup
                    rm -f "$csv_file" "$sql_file"
                else
                    ((total_fail++))
                fi

                # Check if we've reached the end
                if [ $current_year -eq $end_year ] && [ $current_month -eq $end_month ]; then
                    break
                fi

                # Increment month
                current_month=$((current_month + 1))
                if [ $current_month -gt 12 ]; then
                    current_month=1
                    current_year=$((current_year + 1))
                fi
            done
        done
    done

    echo ""
    echo -e "${GREEN}================================================${NC}"
    echo -e "${GREEN}Batch Import Summary:${NC}"
    echo -e "${GREEN}  Total Success: $total_success${NC}"
    echo -e "${YELLOW}  Total Failed: $total_fail${NC}"
    echo -e "${GREEN}================================================${NC}"

    # Optimize table
    echo ""
    optimize_table
}

###############################################################################
# Main Script
###############################################################################

echo "Checking prerequisites..."
check_ssh_key
test_ssh_connection

echo ""
echo -e "${GREEN}✅ Ready to import!${NC}"

while true; do
    show_menu
    read -p "Select option [1-4]: " option

    case $option in
        1)
            import_single_month
            ;;
        2)
            import_date_range
            ;;
        3)
            import_batch
            ;;
        4)
            echo ""
            echo "Cleaning up temporary files..."
            rm -rf "$WORK_DIR"
            echo -e "${GREEN}Goodbye!${NC}"
            exit 0
            ;;
        *)
            echo -e "${RED}Invalid option${NC}"
            ;;
    esac
done
