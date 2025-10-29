#!/bin/bash

###############################################################################
# Build locally on Mac and deploy to Azure server
# Run this script on your LOCAL machine
###############################################################################

set -e

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${BLUE}================================================${NC}"
echo -e "${BLUE}  Deploy to Azure: market.nyyu.io${NC}"
echo -e "${BLUE}================================================${NC}"
echo ""

# Configuration
SSH_KEY="$HOME/Downloads/Azure-NYYU.pem"
SERVER="azureuser@market.nyyu.io"
REMOTE_DIR="/opt/nyyu-market"

# Check if SSH key exists
if [ ! -f "$SSH_KEY" ]; then
    echo -e "${RED}❌ SSH key not found: $SSH_KEY${NC}"
    echo "Please make sure Azure-NYYU.pem is in ~/Downloads/"
    exit 1
fi

###############################################################################
# Step 1: Build Binary
###############################################################################

echo -e "${YELLOW}[1/5] Building binary for Linux...${NC}"

GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-w -s" -o nyyu-market ./cmd/server

if [ ! -f "nyyu-market" ]; then
    echo -e "${RED}❌ Build failed${NC}"
    exit 1
fi

FILE_SIZE=$(ls -lh nyyu-market | awk '{print $5}')
echo -e "${GREEN}✅ Binary built: $FILE_SIZE${NC}"

###############################################################################
# Step 2: Prepare Files
###############################################################################

echo ""
echo -e "${YELLOW}[2/5] Preparing files...${NC}"

# Create temp directory
TEMP_DIR=$(mktemp -d)
echo "Using temp directory: $TEMP_DIR"

# Copy files
cp nyyu-market "$TEMP_DIR/"
cp Dockerfile.simple "$TEMP_DIR/Dockerfile"
cp docker-compose.yml "$TEMP_DIR/"
cp .env.example "$TEMP_DIR/"

# Copy migrations if exists
if [ -d "migrations" ]; then
    cp -r migrations "$TEMP_DIR/"
fi

echo -e "${GREEN}✅ Files prepared${NC}"

###############################################################################
# Step 3: Upload to Server
###############################################################################

echo ""
echo -e "${YELLOW}[3/5] Uploading to Azure server...${NC}"

# Create directory on server
ssh -i "$SSH_KEY" "$SERVER" "sudo mkdir -p $REMOTE_DIR && sudo chown -R azureuser:azureuser $REMOTE_DIR"

# Upload files using rsync
rsync -avz --progress -e "ssh -i $SSH_KEY" \
    "$TEMP_DIR/" "$SERVER:$REMOTE_DIR/"

echo -e "${GREEN}✅ Files uploaded${NC}"

###############################################################################
# Step 4: Setup Environment on Server
###############################################################################

echo ""
echo -e "${YELLOW}[4/5] Setting up environment...${NC}"

ssh -i "$SSH_KEY" "$SERVER" bash << 'ENDSSH'
set -e
cd /opt/nyyu-market

# Make binary executable
chmod +x nyyu-market

# Generate .env if it doesn't exist
if [ ! -f .env ]; then
    echo "Generating passwords..."
    CLICKHOUSE_PASSWORD=$(openssl rand -base64 32)
    REDIS_PASSWORD=$(openssl rand -base64 32)

    cat > .env <<EOF
SERVER_PORT=50051
HTTP_PORT=8080
ENVIRONMENT=production

CLICKHOUSE_HOST=clickhouse
CLICKHOUSE_PORT=9000
CLICKHOUSE_DATABASE=trade
CLICKHOUSE_USERNAME=default
CLICKHOUSE_PASSWORD=${CLICKHOUSE_PASSWORD}

REDIS_HOST=redis
REDIS_PORT=6379
REDIS_PASSWORD=${REDIS_PASSWORD}
REDIS_DB=0
REDIS_PUBSUB_CHANNEL=nyyu:market:updates

CACHE_TTL_PRICE=60
CACHE_TTL_MARK_PRICE=3
CACHE_TTL_CANDLE=5

MAX_CANDLES_LIMIT=1000
DEFAULT_CANDLES_LIMIT=100
BATCH_WRITE_SIZE=100
BATCH_WRITE_INTERVAL=1s

ENABLE_BINANCE=true
ENABLE_KRAKEN=true
ENABLE_COINBASE=true
ENABLE_BYBIT=true
ENABLE_OKX=true
ENABLE_GATEIO=true

MARK_PRICE_UPDATE_INTERVAL=3s
FUNDING_RATE_EMA_PERIOD=20

LOG_LEVEL=info
LOG_FORMAT=json
EOF

    # Save passwords
    cat > passwords.txt <<EOF
=== NYYU MARKET PASSWORDS ===
Generated: $(date)

ClickHouse: ${CLICKHOUSE_PASSWORD}
Redis: ${REDIS_PASSWORD}

Save these passwords securely!
EOF
    chmod 600 passwords.txt

    echo "✅ Environment configured"
    echo "⚠️  Passwords saved in: /opt/nyyu-market/passwords.txt"
else
    echo "✅ Using existing .env file"
fi
ENDSSH

echo -e "${GREEN}✅ Environment ready${NC}"

###############################################################################
# Step 5: Deploy
###############################################################################

echo ""
echo -e "${YELLOW}[5/5] Deploying application...${NC}"

ssh -i "$SSH_KEY" "$SERVER" bash << 'ENDSSH'
set -e
cd /opt/nyyu-market

echo "Stopping existing containers..."
docker compose down 2>/dev/null || true

echo "Building Docker image (fast, no compilation)..."
docker compose build

echo "Starting services..."
docker compose up -d

echo "Waiting for services to start..."
sleep 10

echo ""
echo "=== Container Status ==="
docker compose ps

echo ""
echo "=== Testing Health Endpoint ==="
sleep 5
curl -s http://localhost:8080/health | python3 -m json.tool 2>/dev/null || curl -s http://localhost:8080/health || echo "Health check pending..."

echo ""
echo "✅ Deployment complete!"
ENDSSH

###############################################################################
# Cleanup and Summary
###############################################################################

echo ""
echo -e "${GREEN}================================================${NC}"
echo -e "${GREEN}  Deployment Successful! 🚀${NC}"
echo -e "${GREEN}================================================${NC}"
echo ""

echo -e "${BLUE}Your API is deployed at:${NC}"
echo "  • HTTP API: https://market.nyyu.io"
echo "  • Health: https://market.nyyu.io/health"
echo "  • Stats: https://market.nyyu.io/api/v1/stats"
echo ""

echo -e "${BLUE}Useful commands:${NC}"
echo "  • View logs:"
echo "    ssh -i $SSH_KEY $SERVER 'cd $REMOTE_DIR && docker compose logs -f'"
echo ""
echo "  • Check status:"
echo "    ssh -i $SSH_KEY $SERVER 'cd $REMOTE_DIR && docker compose ps'"
echo ""
echo "  • Restart:"
echo "    ssh -i $SSH_KEY $SERVER 'cd $REMOTE_DIR && docker compose restart'"
echo ""
echo "  • SSH to server:"
echo "    ssh -i $SSH_KEY $SERVER"
echo ""

# Cleanup temp directory
rm -rf "$TEMP_DIR"
echo -e "${YELLOW}Cleaned up temporary files${NC}"
echo ""
