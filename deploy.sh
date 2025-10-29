#!/bin/bash

###############################################################################
# Nyyu Market - Manual Deployment Script
# Uses pre-built Docker images from GitHub Container Registry
###############################################################################

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_header() {
    echo ""
    echo -e "${BLUE}================================================${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}================================================${NC}"
    echo ""
}

print_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

print_error() {
    echo -e "${RED}❌ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

print_info() {
    echo -e "${BLUE}ℹ️  $1${NC}"
}

check_root() {
    if [[ $EUID -ne 0 ]]; then
        print_error "This script must be run as root or with sudo"
        exit 1
    fi
}

print_header "Nyyu Market - Manual Deployment"
echo "Started: $(date)"
echo ""

# Check if running as root
check_root

###############################################################################
# Step 1: Install Docker
###############################################################################

print_header "Step 1: Installing Docker"

if command -v docker &> /dev/null; then
    print_warning "Docker is already installed"
    docker --version
else
    print_info "Installing Docker..."
    curl -fsSL https://get.docker.com | sh
    systemctl enable docker
    systemctl start docker
    print_success "Docker installed"
fi

# Add user to docker group
if [[ -n "$SUDO_USER" ]]; then
    usermod -aG docker $SUDO_USER
    print_success "Added $SUDO_USER to docker group"
fi

docker --version

###############################################################################
# Step 2: Install System Dependencies
###############################################################################

print_header "Step 2: Installing System Dependencies"

apt update
apt install -y nginx certbot python3-certbot-nginx ufw git

print_success "System dependencies installed"

###############################################################################
# Step 3: Setup Project Directory
###############################################################################

print_header "Step 3: Setting Up Project Directory"

PROJECT_DIR="/opt/nyyu-market"

if [ ! -d "$PROJECT_DIR" ]; then
    print_info "Cloning repository..."
    cd /opt
    git clone https://github.com/Caqil/nyyu-market.git
    cd nyyu-market
else
    print_info "Project directory exists, pulling latest changes..."
    cd "$PROJECT_DIR"
    git pull
fi

if [[ -n "$SUDO_USER" ]]; then
    chown -R $SUDO_USER:$SUDO_USER "$PROJECT_DIR"
fi

print_success "Project directory ready"

###############################################################################
# Step 4: Generate Passwords and Create .env
###############################################################################

print_header "Step 4: Creating Environment Configuration"

if [ -f .env ]; then
    print_warning ".env file already exists"
    read -p "Do you want to overwrite it? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        print_info "Keeping existing .env file"
        SKIP_ENV=true
    else
        cp .env .env.backup.$(date +%Y%m%d_%H%M%S)
        SKIP_ENV=false
    fi
else
    SKIP_ENV=false
fi

if [ "$SKIP_ENV" = false ]; then
    # Generate secure passwords
    CLICKHOUSE_PASSWORD=$(openssl rand -base64 32)
    REDIS_PASSWORD=$(openssl rand -base64 32)

    # Create .env file
    cat > .env <<EOF
# Server Configuration
SERVER_PORT=50051
HTTP_PORT=8080
ENVIRONMENT=production

# ClickHouse Configuration
CLICKHOUSE_HOST=clickhouse
CLICKHOUSE_PORT=9000
CLICKHOUSE_DATABASE=trade
CLICKHOUSE_USERNAME=default
CLICKHOUSE_PASSWORD=${CLICKHOUSE_PASSWORD}

# Redis Configuration
REDIS_HOST=redis
REDIS_PORT=6379
REDIS_PASSWORD=${REDIS_PASSWORD}
REDIS_DB=0
REDIS_PUBSUB_CHANNEL=nyyu:market:updates

# Cache TTL (seconds)
CACHE_TTL_PRICE=60
CACHE_TTL_MARK_PRICE=3
CACHE_TTL_CANDLE=5

# Service Configuration
MAX_CANDLES_LIMIT=1000
DEFAULT_CANDLES_LIMIT=100
BATCH_WRITE_SIZE=100
BATCH_WRITE_INTERVAL=1s

# Exchange Configuration
ENABLE_BINANCE=true
ENABLE_KRAKEN=true
ENABLE_COINBASE=true
ENABLE_BYBIT=true
ENABLE_OKX=true
ENABLE_GATEIO=true

# Mark Price Configuration
MARK_PRICE_UPDATE_INTERVAL=3s
FUNDING_RATE_EMA_PERIOD=20

# Logging
LOG_LEVEL=info
LOG_FORMAT=json
EOF

    chmod 600 .env

    # Update docker-compose.prod.yml with passwords
    sed -i.bak "s|CLICKHOUSE_PASSWORD=|CLICKHOUSE_PASSWORD=${CLICKHOUSE_PASSWORD}|g" docker-compose.prod.yml
    sed -i.bak "s|REDIS_PASSWORD=|REDIS_PASSWORD=${REDIS_PASSWORD}|g" docker-compose.prod.yml

    # Save passwords
    cat > .passwords.txt <<EOF
NYYU MARKET - PASSWORDS
Generated: $(date)

ClickHouse Password: ${CLICKHOUSE_PASSWORD}
Redis Password: ${REDIS_PASSWORD}

IMPORTANT: Store these passwords securely!
EOF
    chmod 600 .passwords.txt

    print_success "Environment configuration created"
    print_warning "Passwords saved to .passwords.txt"
fi

###############################################################################
# Step 5: Build Docker Image
###############################################################################

print_header "Step 5: Building Docker Image"

print_info "Building Docker image (this may take a few minutes)..."
docker compose build

print_success "Docker image built"

###############################################################################
# Step 6: Configure Firewall
###############################################################################

print_header "Step 6: Configuring Firewall"

ufw --force enable
ufw allow 22/tcp
ufw allow 80/tcp
ufw allow 443/tcp

print_success "Firewall configured"
ufw status

###############################################################################
# Step 7: Configure Nginx
###############################################################################

print_header "Step 7: Configuring Nginx"

# Ask for domain
read -p "Enter your domain (e.g., market.nyyu.io): " DOMAIN

if [[ -z "$DOMAIN" ]]; then
    print_error "Domain is required"
    exit 1
fi

# Create Nginx configuration
cat > /etc/nginx/sites-available/nyyu-market <<EOF
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

# HTTP API Server
server {
    listen 80;
    server_name ${DOMAIN};

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

    location /health {
        proxy_pass http://nyyu_http_api/health;
        access_log off;
    }

    access_log /var/log/nginx/nyyu_http_access.log;
    error_log /var/log/nginx/nyyu_http_error.log;
}
EOF

# Enable site
ln -sf /etc/nginx/sites-available/nyyu-market /etc/nginx/sites-enabled/
rm -f /etc/nginx/sites-enabled/default

# Test configuration
nginx -t

# Restart Nginx
systemctl restart nginx
systemctl enable nginx

print_success "Nginx configured for ${DOMAIN}"

###############################################################################
# Step 8: Setup SSL
###############################################################################

print_header "Step 8: Setting Up SSL Certificate"

read -p "Do you want to set up SSL with Let's Encrypt? (y/N): " -n 1 -r
echo

if [[ $REPLY =~ ^[Yy]$ ]]; then
    read -p "Enter your email for SSL notifications: " SSL_EMAIL

    if [[ -z "$SSL_EMAIL" ]]; then
        print_warning "Email required for SSL. Skipping SSL setup."
    else
        print_info "Requesting SSL certificate..."

        certbot --nginx \
            -d "$DOMAIN" \
            --non-interactive \
            --agree-tos \
            -m "$SSL_EMAIL" \
            --redirect || {
                print_warning "SSL setup failed. You can retry later with:"
                print_info "sudo certbot --nginx -d $DOMAIN"
            }

        # Enable auto-renewal
        systemctl enable certbot.timer
        systemctl start certbot.timer

        print_success "SSL certificate installed"
    fi
else
    print_info "Skipping SSL setup"
fi

###############################################################################
# Step 9: Start Services
###############################################################################

print_header "Step 9: Starting Services"

cd "$PROJECT_DIR"

# Stop existing containers
docker compose down 2>/dev/null || true

# Start services
print_info "Starting Docker containers..."
docker compose up -d

print_success "Services started"

###############################################################################
# Step 10: Wait for Services
###############################################################################

print_header "Step 10: Waiting for Services"

print_info "Waiting for services to be healthy..."
sleep 15

# Check container status
docker compose ps

# Test health endpoint
print_info "Testing health endpoint..."
sleep 5
HEALTH_RESPONSE=$(curl -s http://localhost:8080/health || echo "Failed")

if [[ $HEALTH_RESPONSE == *"healthy"* ]]; then
    print_success "Health check passed"
    echo "$HEALTH_RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$HEALTH_RESPONSE"
else
    print_warning "Health check pending, service might still be starting..."
    print_info "Check logs with: docker compose logs -f"
fi

###############################################################################
# Step 11: Setup Auto-start
###############################################################################

print_header "Step 11: Setting Up Auto-start"

cat > /etc/systemd/system/nyyu-market.service <<EOF
[Unit]
Description=Nyyu Market Service
Requires=docker.service
After=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=$PROJECT_DIR
ExecStart=/usr/bin/docker compose up -d
ExecStop=/usr/bin/docker compose down
TimeoutStartSec=0

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable nyyu-market.service

print_success "Auto-start on boot enabled"

###############################################################################
# Summary
###############################################################################

print_header "Installation Complete! 🎉"

echo -e "${GREEN}"
echo "Nyyu Market has been successfully deployed!"
echo -e "${NC}"

echo ""
echo -e "${BLUE}=== Service Information ===${NC}"
if [[ $REPLY =~ ^[Yy]$ ]] && [[ -n "$SSL_EMAIL" ]]; then
    echo "HTTP API:      https://${DOMAIN}"
    echo "gRPC API:      grpc://${DOMAIN}:443 (with TLS)"
    echo "Health Check:  https://${DOMAIN}/health"
    echo "Stats:         https://${DOMAIN}/api/v1/stats"
else
    echo "HTTP API:      http://${DOMAIN}"
    echo "gRPC API:      ${DOMAIN}:50051 (direct)"
    echo "Health Check:  http://${DOMAIN}/health"
    echo "Stats:         http://${DOMAIN}/api/v1/stats"
fi

echo ""
echo -e "${BLUE}=== Management Commands ===${NC}"
echo "View logs:     docker compose logs -f"
echo "Restart:       docker compose restart"
echo "Stop:          docker compose down"
echo "Start:         docker compose up -d"
echo "Status:        docker compose ps"

echo ""
echo -e "${BLUE}=== Update Deployment ===${NC}"
echo "cd $PROJECT_DIR"
echo "git pull"
echo "docker compose build"
echo "docker compose up -d"

echo ""
echo -e "${YELLOW}=== Important ===${NC}"
echo "1. Passwords saved in: $PROJECT_DIR/.passwords.txt"
echo "2. Service will auto-start on boot"
echo "3. Test your API: curl http://localhost:8080/health"

if [[ -n "$SUDO_USER" ]]; then
    echo ""
    echo -e "${YELLOW}⚠️  Log out and back in for docker group changes${NC}"
fi

echo ""
echo -e "${GREEN}Deployment complete! 🚀${NC}"
echo ""
