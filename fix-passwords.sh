#!/bin/bash

###############################################################################
# Fix password authentication on Azure server
# Run this script on the AZURE SERVER to fix ClickHouse/Redis authentication
###############################################################################

set -e

echo "Fixing password authentication..."
cd /opt/nyyu-market

# Stop all containers
echo "Stopping containers..."
docker compose down

# Remove old volumes (will recreate with new passwords)
echo "Removing old database volumes..."
docker volume rm nyyu-market_clickhouse_data nyyu-market_redis_data 2>/dev/null || true

# Restart with correct passwords
echo "Starting services with correct passwords..."
docker compose up -d

echo ""
echo "Waiting for services to start..."
sleep 15

echo ""
echo "Container status:"
docker compose ps

echo ""
echo "Testing health endpoint..."
sleep 5
curl -s http://localhost:8080/health | python3 -m json.tool 2>/dev/null || curl -s http://localhost:8080/health || echo "Pending..."

echo ""
echo "✅ Done! Check logs with: docker compose logs -f"
