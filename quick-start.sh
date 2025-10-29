#!/bin/bash

###############################################################################
# Nyyu Market - Quick Start Script (for existing Docker installations)
###############################################################################

set -e

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}================================================${NC}"
echo -e "${BLUE}  Nyyu Market - Quick Start${NC}"
echo -e "${BLUE}================================================${NC}"
echo ""

# Check if Docker is installed
if ! command -v docker &> /dev/null; then
    echo -e "${RED}❌ Docker is not installed!${NC}"
    echo ""
    echo "Please install Docker first:"
    echo "  sudo bash install.sh"
    exit 1
fi


# Check if docker compose is available
if ! docker compose version &> /dev/null; then
    echo -e "${RED}❌ Docker Compose is not available!${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Docker detected${NC}"

# Create .env if it doesn't exist
if [[ ! -f .env ]]; then
    echo -e "${YELLOW}⚠️  .env file not found. Creating from example...${NC}"

    if [[ -f .env.example ]]; then
        cp .env.example .env
        echo -e "${GREEN}✅ Created .env from .env.example${NC}"
    else
        echo -e "${YELLOW}Creating basic .env file...${NC}"
        cat > .env <<EOF
SERVER_PORT=50051
HTTP_PORT=8080
ENVIRONMENT=development

CLICKHOUSE_HOST=clickhouse
CLICKHOUSE_PORT=9000
CLICKHOUSE_DATABASE=trade
CLICKHOUSE_USERNAME=default
CLICKHOUSE_PASSWORD=

REDIS_HOST=redis
REDIS_PORT=6379
REDIS_PASSWORD=
REDIS_DB=0

ENABLE_BINANCE=true
ENABLE_KRAKEN=true
ENABLE_COINBASE=true
ENABLE_BYBIT=true
ENABLE_OKX=true
ENABLE_GATEIO=true

LOG_LEVEL=info
EOF
        echo -e "${GREEN}✅ Created basic .env file${NC}"
    fi
fi

# Stop existing containers
echo ""
echo -e "${BLUE}🛑 Stopping existing containers...${NC}"
docker compose down 2>/dev/null || true

# Build and start
echo ""
echo -e "${BLUE}🔨 Building application...${NC}"
docker compose build

echo ""
echo -e "${BLUE}🚀 Starting services...${NC}"
docker compose up -d

echo ""
echo -e "${BLUE}⏳ Waiting for services to be ready...${NC}"
sleep 10

# Check status
echo ""
echo -e "${BLUE}📊 Container Status:${NC}"
docker compose ps

# Test health
echo ""
echo -e "${BLUE}🏥 Testing health endpoint...${NC}"
for i in {1..10}; do
    if curl -s http://localhost:8080/health > /dev/null 2>&1; then
        echo -e "${GREEN}✅ Service is healthy!${NC}"
        echo ""
        curl -s http://localhost:8080/health | python3 -m json.tool 2>/dev/null || curl -s http://localhost:8080/health
        break
    fi
    if [ $i -eq 10 ]; then
        echo -e "${YELLOW}⚠️  Service is still starting... Check logs with: docker compose logs -f${NC}"
    else
        sleep 3
    fi
done

echo ""
echo -e "${GREEN}================================================${NC}"
echo -e "${GREEN}  ✅ Nyyu Market is running!${NC}"
echo -e "${GREEN}================================================${NC}"
echo ""
echo "📡 gRPC API:      localhost:50051"
echo "🌐 HTTP API:      http://localhost:8080"
echo "🏥 Health Check:  http://localhost:8080/health"
echo "📊 Stats:         http://localhost:8080/api/v1/stats"
echo ""
echo "📝 View logs:     docker compose logs -f"
echo "🛑 Stop:          docker compose down"
echo "🔄 Restart:       docker compose restart"
echo ""
