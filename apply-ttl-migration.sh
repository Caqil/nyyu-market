#!/bin/bash

# Apply TTL migration to extend data retention to 10 years
# This script updates the ClickHouse table TTL settings

set -e

echo "================================================"
echo "  Applying TTL Migration (10 Year Retention)"
echo "================================================"
echo ""

# Check if we're running locally or on Azure
if [ -f "$HOME/Downloads/Azure-NYYU.pem" ]; then
    SSH_KEY="$HOME/Downloads/Azure-NYYU.pem"
    SERVER="azureuser@market.nyyu.io"
    REMOTE_DIR="/opt/nyyu-market"

    echo "🌐 Detected Azure environment"
    echo "   Applying migration on Azure server..."
    echo ""

    # Copy migration file to server
    scp -i "$SSH_KEY" -o StrictHostKeyChecking=no \
        migrations/clickhouse/002_update_ttl_10_years.sql \
        "$SERVER:$REMOTE_DIR/migrations/clickhouse/"

    # Execute migration via SSH
    ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SERVER" <<'REMOTE_SCRIPT'
cd /opt/nyyu-market

# Apply migration to ClickHouse
docker exec -i $(docker ps -qf "name=clickhouse") \
    clickhouse-client --query "$(cat migrations/clickhouse/002_update_ttl_10_years.sql)"

echo ""
echo "✅ Migration applied successfully!"
echo ""
echo "Verifying TTL settings..."
docker exec -i $(docker ps -qf "name=clickhouse") \
    clickhouse-client --query "SHOW CREATE TABLE trade.candles" | grep TTL

REMOTE_SCRIPT

else
    echo "💻 Detected local environment"
    echo "   Applying migration locally..."
    echo ""

    # Get ClickHouse connection details from environment or defaults
    CLICKHOUSE_HOST=${CLICKHOUSE_HOST:-localhost}
    CLICKHOUSE_PORT=${CLICKHOUSE_PORT:-9000}
    CLICKHOUSE_USER=${CLICKHOUSE_USERNAME:-default}
    CLICKHOUSE_PASSWORD=${CLICKHOUSE_PASSWORD:-}

    # Check if ClickHouse is running in Docker
    if docker ps | grep -q clickhouse; then
        echo "🐳 Using Docker ClickHouse..."
        docker exec -i $(docker ps -qf "name=clickhouse") \
            clickhouse-client --query "$(cat migrations/clickhouse/002_update_ttl_10_years.sql)"
    else
        echo "🔌 Using ClickHouse at $CLICKHOUSE_HOST:$CLICKHOUSE_PORT..."
        clickhouse-client \
            --host="$CLICKHOUSE_HOST" \
            --port="$CLICKHOUSE_PORT" \
            --user="$CLICKHOUSE_USER" \
            --password="$CLICKHOUSE_PASSWORD" \
            --query "$(cat migrations/clickhouse/002_update_ttl_10_years.sql)"
    fi

    echo ""
    echo "✅ Migration applied successfully!"
    echo ""
    echo "Verifying TTL settings..."

    if docker ps | grep -q clickhouse; then
        docker exec -i $(docker ps -qf "name=clickhouse") \
            clickhouse-client --query "SHOW CREATE TABLE trade.candles" | grep TTL
    else
        clickhouse-client \
            --host="$CLICKHOUSE_HOST" \
            --port="$CLICKHOUSE_PORT" \
            --user="$CLICKHOUSE_USER" \
            --password="$CLICKHOUSE_PASSWORD" \
            --query "SHOW CREATE TABLE trade.candles" | grep TTL
    fi
fi

echo ""
echo "================================================"
echo "  Migration Complete!"
echo "================================================"
echo ""
echo "📊 Data Retention Policy:"
echo "   • 1m, 3m, 5m intervals:  90 days"
echo "   • 15m, 30m, 1h, 2h:      180 days"
echo "   • 4h, 6h, 8h, 12h:       10 years"
echo "   • 1d, 3d, 1w, 1M:        10 years"
echo ""
echo "✅ You can now import and access historical data back to 2018!"
echo ""
