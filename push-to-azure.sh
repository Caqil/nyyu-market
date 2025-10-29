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
echo -e "${BLUE}  Nyyu Market - Azure Deployment${NC}"
echo -e "${BLUE}================================================${NC}"
echo ""

# Ask for domain
read -p "Enter your domain (e.g., market.nyyu.io): " DOMAIN

if [[ -z "$DOMAIN" ]]; then
    echo -e "${RED}❌ Domain is required${NC}"
    exit 1
fi

echo ""
echo -e "${GREEN}Deploying to: $DOMAIN${NC}"
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

# Set correct permissions on SSH key
chmod 600 "$SSH_KEY"

# Test SSH connection
echo "Testing SSH connection..."
if ! ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=10 "$SERVER" "echo 'Connection OK'" &>/dev/null; then
    echo -e "${RED}❌ Cannot connect to server${NC}"
    echo "Please check:"
    echo "  - SSH key is correct"
    echo "  - Server is reachable: market.nyyu.io"
    echo "  - Your internet connection"
    exit 1
fi
echo -e "${GREEN}✅ SSH connection OK${NC}"
echo ""

###############################################################################
# Step 1: Build Binary
###############################################################################

echo -e "${YELLOW}[1/5] Building binary for Linux...${NC}"

# Build with optimization and compression
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-w -s" -trimpath -o nyyu-market ./cmd/server

# Compress binary
if command -v upx &> /dev/null; then
    echo "Compressing binary with UPX..."
    upx -q nyyu-market 2>/dev/null || true
fi

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
ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SERVER" "sudo mkdir -p $REMOTE_DIR && sudo chown -R azureuser:azureuser $REMOTE_DIR"

# Upload files using scp (simpler and shows progress)
echo "Uploading binary (21MB, ~30-60 seconds)..."
scp -i "$SSH_KEY" -o StrictHostKeyChecking=no -C "$TEMP_DIR/nyyu-market" "$SERVER:$REMOTE_DIR/"

echo "Uploading config files..."
scp -i "$SSH_KEY" -o StrictHostKeyChecking=no -C "$TEMP_DIR/Dockerfile" "$SERVER:$REMOTE_DIR/"
scp -i "$SSH_KEY" -o StrictHostKeyChecking=no -C "$TEMP_DIR/docker-compose.yml" "$SERVER:$REMOTE_DIR/"
scp -i "$SSH_KEY" -o StrictHostKeyChecking=no -C "$TEMP_DIR/.env.example" "$SERVER:$REMOTE_DIR/"

# Upload migrations if exists
if [ -d "$TEMP_DIR/migrations" ]; then
    echo "Uploading migrations..."
    scp -i "$SSH_KEY" -o StrictHostKeyChecking=no -C -r "$TEMP_DIR/migrations" "$SERVER:$REMOTE_DIR/"
fi

echo -e "${GREEN}✅ Files uploaded${NC}"

###############################################################################
# Step 4: Setup Environment on Server
###############################################################################

echo ""
echo -e "${YELLOW}[4/5] Setting up environment...${NC}"

ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SERVER" bash << 'ENDSSH'
set -e

# Install Docker if not present
if ! command -v docker &> /dev/null; then
    echo "Installing Docker..."
    curl -fsSL https://get.docker.com -o /tmp/get-docker.sh
    sudo sh /tmp/get-docker.sh
    sudo usermod -aG docker $USER
    echo "✅ Docker installed"
else
    echo "✅ Docker already installed"
fi

cd /opt/nyyu-market

# Make binary executable
chmod +x nyyu-market

# Generate .env if it doesn't exist
if [ ! -f .env ]; then
    echo "Creating .env file..."

    cat > .env <<EOF
SERVER_PORT=50051
HTTP_PORT=8080
ENVIRONMENT=production

CLICKHOUSE_HOST=clickhouse
CLICKHOUSE_PORT=9000
CLICKHOUSE_DATABASE=trade
CLICKHOUSE_USERNAME=default
CLICKHOUSE_PASSWORD=

REDIS_HOST=redis
REDIS_PORT=6379
REDIS_PASSWORD=
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

    echo "✅ Environment configured (no passwords needed)"
else
    echo "✅ Using existing .env file"
fi
ENDSSH

echo -e "${GREEN}✅ Environment ready${NC}"

###############################################################################
# Step 5: Setup Nginx
###############################################################################

echo ""
echo -e "${YELLOW}[5/7] Setting up Nginx...${NC}"

ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SERVER" "DOMAIN=$DOMAIN" bash << 'ENDSSH'
set -e

# Get domain from environment
DOMAIN=${DOMAIN}

# Install Nginx if not present
if ! command -v nginx &> /dev/null; then
    echo "Installing Nginx..."
    sudo apt update
    sudo apt install -y nginx
    echo "✅ Nginx installed"
else
    echo "✅ Nginx already installed"
fi

# Create Nginx configuration
echo "Configuring Nginx for $DOMAIN..."
sudo tee /etc/nginx/sites-available/nyyu-market > /dev/null <<EOF
# HTTP API Upstream
upstream nyyu_http_api {
    server localhost:8080;
    keepalive 32;
}

# gRPC API Upstream
upstream nyyu_grpc_api {
    server localhost:50051;
    keepalive 32;
}

# HTTP Server
server {
    listen 80;
    server_name $DOMAIN;

    # HTTP API
    location / {
        proxy_pass http://nyyu_http_api;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header Connection "";
        proxy_connect_timeout 30s;
        proxy_send_timeout 30s;
        proxy_read_timeout 30s;
    }

    # Health check
    location /health {
        proxy_pass http://nyyu_http_api/health;
        access_log off;
    }

    access_log /var/log/nginx/nyyu_http_access.log;
    error_log /var/log/nginx/nyyu_http_error.log;
}

# gRPC Server
server {
    listen 50051 http2;
    server_name $DOMAIN;

    location / {
        grpc_pass grpc://nyyu_grpc_api;
        error_page 502 = /error502grpc;
    }

    location = /error502grpc {
        internal;
        default_type application/grpc;
        add_header grpc-status 14;
        add_header content-length 0;
        return 204;
    }
}
EOF

# Enable site
sudo ln -sf /etc/nginx/sites-available/nyyu-market /etc/nginx/sites-enabled/
sudo rm -f /etc/nginx/sites-enabled/default

# Test and reload Nginx
sudo nginx -t
sudo systemctl enable nginx
sudo systemctl restart nginx

echo "✅ Nginx configured"
ENDSSH

echo -e "${GREEN}✅ Nginx ready${NC}"

###############################################################################
# Step 6: Setup SSL
###############################################################################

echo ""
echo -e "${YELLOW}[6/7] Setting up SSL...${NC}"
echo ""
echo "Choose SSL option:"
echo "1) Cloudflare SSL (recommended - free, easy)"
echo "2) Let's Encrypt (requires DNS pointing to server)"
echo "3) Skip SSL setup"
echo ""
read -p "Select option [1-3]: " SSL_OPTION

if [[ "$SSL_OPTION" == "1" ]]; then
    echo ""
    echo -e "${BLUE}Cloudflare SSL Certificate Setup${NC}"
    echo ""

    # Auto-detect certificates in certs/ folder
    CERT_FILE=""
    KEY_FILE=""

    if [[ -f "certs/fullchain.pem" && -f "certs/privkey.pem" ]]; then
        echo -e "${GREEN}✅ Found certificates in certs/ folder${NC}"
        CERT_FILE="certs/fullchain.pem"
        KEY_FILE="certs/privkey.pem"
    else
        echo "Certificate files not found in certs/ folder"
        echo ""
        read -p "Do you have Cloudflare Origin Certificate files? [Y/n]: " HAS_CERTS

        if [[ ! $HAS_CERTS =~ ^[Nn]$ ]]; then
            # Ask for certificate files location
            read -p "Enter path to certificate file (.pem or .crt): " CERT_FILE
            read -p "Enter path to private key file (.key): " KEY_FILE
        fi
    fi

    if [[ -n "$CERT_FILE" && -n "$KEY_FILE" ]]; then
        if [[ -f "$CERT_FILE" && -f "$KEY_FILE" ]]; then
            echo "Uploading certificates to server..."

            # Create ssl directory and upload certificates
            ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SERVER" "sudo mkdir -p /etc/nginx/ssl"
            scp -i "$SSH_KEY" -o StrictHostKeyChecking=no "$CERT_FILE" "$SERVER:/tmp/cloudflare.crt"
            scp -i "$SSH_KEY" -o StrictHostKeyChecking=no "$KEY_FILE" "$SERVER:/tmp/cloudflare.key"
            ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SERVER" "DOMAIN=$DOMAIN" bash << 'ENDSSH'
set -e
DOMAIN=${DOMAIN}

# Move certificates to nginx ssl directory
sudo mv /tmp/cloudflare.crt /etc/nginx/ssl/$DOMAIN.crt
sudo mv /tmp/cloudflare.key /etc/nginx/ssl/$DOMAIN.key
sudo chmod 644 /etc/nginx/ssl/$DOMAIN.crt
sudo chmod 600 /etc/nginx/ssl/$DOMAIN.key

# Update Nginx config for HTTPS
sudo tee /etc/nginx/sites-available/nyyu-market > /dev/null <<EOF
# HTTP API Upstream
upstream nyyu_http_api {
    server localhost:8080;
    keepalive 32;
}

# HTTP Server - Redirect to HTTPS
server {
    listen 80;
    server_name $DOMAIN;
    return 301 https://\$server_name\$request_uri;
}

# HTTPS Server
server {
    listen 443 ssl http2;
    server_name $DOMAIN;

    # Cloudflare SSL Certificate
    ssl_certificate /etc/nginx/ssl/$DOMAIN.crt;
    ssl_certificate_key /etc/nginx/ssl/$DOMAIN.key;

    # SSL Configuration
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;
    ssl_prefer_server_ciphers on;

    # HTTP API
    location / {
        proxy_pass http://nyyu_http_api;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header Connection "";
        proxy_connect_timeout 30s;
        proxy_send_timeout 30s;
        proxy_read_timeout 30s;
    }

    # Health check
    location /health {
        proxy_pass http://nyyu_http_api/health;
        access_log off;
    }

    access_log /var/log/nginx/nyyu_https_access.log;
    error_log /var/log/nginx/nyyu_https_error.log;
}
EOF

# Reload Nginx
sudo nginx -t && sudo systemctl reload nginx

echo "✅ Cloudflare SSL certificates installed"
ENDSSH

            echo -e "${GREEN}✅ Cloudflare SSL configured${NC}"
        else
            echo -e "${RED}❌ Certificate files not found${NC}"
            echo "Continuing without SSL..."
        fi
    else
        echo ""
        echo -e "${BLUE}To add SSL later:${NC}"
        echo "1. Create Origin Certificate in Cloudflare → SSL/TLS → Origin Server"
        echo "2. Save as fullchain.pem and privkey.pem in certs/ folder"
        echo "3. Re-run this script (it will auto-detect the files)"
    fi

elif [[ "$SSL_OPTION" == "2" ]]; then
    # Prompt for email
    echo ""
    read -p "Enter email for SSL notifications (or press Enter for admin@$DOMAIN): " SSL_EMAIL
    if [[ -z "$SSL_EMAIL" ]]; then
        SSL_EMAIL="admin@$DOMAIN"
    fi

    ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SERVER" "DOMAIN=$DOMAIN SSL_EMAIL=$SSL_EMAIL" bash << 'ENDSSH'
set -e

# Get domain from environment
DOMAIN=${DOMAIN}
SSL_EMAIL=${SSL_EMAIL}

# Install Certbot if not present
if ! command -v certbot &> /dev/null; then
    echo "Installing Certbot..."
    sudo apt install -y certbot python3-certbot-nginx
    echo "✅ Certbot installed"
else
    echo "✅ Certbot already installed"
fi

# Check if certificate already exists
if [ -f /etc/letsencrypt/live/$DOMAIN/fullchain.pem ]; then
    echo "✅ SSL certificate already exists for $DOMAIN"
else
    echo "Requesting SSL certificate for $DOMAIN..."
    echo "Using email: $SSL_EMAIL"

    # Use standalone mode temporarily (stop nginx first)
    sudo systemctl stop nginx

    sudo certbot certonly \
        --standalone \
        -d $DOMAIN \
        --non-interactive \
        --agree-tos \
        -m $SSL_EMAIL || {
            echo "⚠️  SSL setup failed, continuing without SSL"
            sudo systemctl start nginx
            exit 0
        }

    # Update Nginx config with Let's Encrypt certificates
    sudo tee /etc/nginx/sites-available/nyyu-market > /dev/null <<EOF
# HTTP API Upstream
upstream nyyu_http_api {
    server localhost:8080;
    keepalive 32;
}

# HTTP Server - Redirect to HTTPS
server {
    listen 80;
    server_name $DOMAIN;
    return 301 https://\\\$server_name\\\$request_uri;
}

# HTTPS Server
server {
    listen 443 ssl http2;
    server_name $DOMAIN;

    # Let's Encrypt SSL Certificate
    ssl_certificate /etc/letsencrypt/live/$DOMAIN/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/$DOMAIN/privkey.pem;

    # SSL Configuration
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;
    ssl_prefer_server_ciphers on;

    # HTTP API
    location / {
        proxy_pass http://nyyu_http_api;
        proxy_http_version 1.1;
        proxy_set_header Host \\\$host;
        proxy_set_header X-Real-IP \\\$remote_addr;
        proxy_set_header X-Forwarded-For \\\$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \\\$scheme;
        proxy_set_header Connection "";
        proxy_connect_timeout 30s;
        proxy_send_timeout 30s;
        proxy_read_timeout 30s;
    }

    # Health check
    location /health {
        proxy_pass http://nyyu_http_api/health;
        access_log off;
    }

    access_log /var/log/nginx/nyyu_https_access.log;
    error_log /var/log/nginx/nyyu_https_error.log;
}
EOF

    sudo systemctl start nginx

    # Enable auto-renewal
    sudo systemctl enable certbot.timer
    sudo systemctl start certbot.timer

    echo "✅ SSL certificate installed"
fi
ENDSSH
else
    echo "⏭️  Skipping SSL setup"
fi

echo -e "${GREEN}✅ SSL configuration complete${NC}"

###############################################################################
# Step 7: Deploy Application
###############################################################################

echo ""
echo -e "${YELLOW}[7/7] Deploying application...${NC}"

ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no "$SERVER" bash << 'ENDSSH'
set -e
cd /opt/nyyu-market

# Use sudo for docker if user not in docker group yet
DOCKER_CMD="docker"
if ! docker ps &> /dev/null; then
    echo "Using sudo for docker (group membership will take effect after logout)"
    DOCKER_CMD="sudo docker"
fi

echo "Stopping existing containers..."
$DOCKER_CMD compose down 2>/dev/null || true

echo "Building Docker image (fast, no compilation)..."
$DOCKER_CMD compose build

echo "Starting services..."
$DOCKER_CMD compose up -d

echo "Waiting for services to start..."
sleep 10

echo ""
echo "=== Container Status ==="
$DOCKER_CMD compose ps

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
echo "  • HTTP API: https://$DOMAIN"
echo "  • gRPC API: $DOMAIN:50051 (with TLS after SSL setup)"
echo "  • Health: https://$DOMAIN/health"
echo "  • Stats: https://$DOMAIN/api/v1/stats"
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
