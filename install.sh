#!/bin/bash

###############################################################################
# Nyyu Market - Automated Installation Script
# This script installs Docker, Docker Compose, and deploys the application
###############################################################################

set -e  # Exit on error

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Script directory
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR"

###############################################################################
# Helper Functions
###############################################################################

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

check_os() {
    if [[ -f /etc/os-release ]]; then
        . /etc/os-release
        OS=$ID
        VERSION=$VERSION_ID
    else
        print_error "Cannot detect OS. This script supports Ubuntu/Debian only."
        exit 1
    fi

    if [[ "$OS" != "ubuntu" && "$OS" != "debian" ]]; then
        print_error "This script only supports Ubuntu/Debian. Detected: $OS"
        exit 1
    fi

    print_success "Detected OS: $OS $VERSION"
}

###############################################################################
# Installation Functions
###############################################################################

install_dependencies() {
    print_header "Installing System Dependencies"

    apt update
    apt install -y \
        ca-certificates \
        curl \
        gnupg \
        lsb-release \
        git \
        wget \
        nano \
        htop \
        net-tools \
        software-properties-common

    print_success "System dependencies installed"
}

install_docker() {
    print_header "Installing Docker"

    # Check if Docker is already installed
    if command -v docker &> /dev/null; then
        DOCKER_VERSION=$(docker --version)
        print_warning "Docker is already installed: $DOCKER_VERSION"
        read -p "Do you want to reinstall Docker? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            return
        fi

        # Remove old Docker
        apt remove -y docker docker-engine docker.io containerd runc || true
    fi

    # Add Docker's official GPG key
    mkdir -p /etc/apt/keyrings
    curl -fsSL https://download.docker.com/linux/$OS/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg

    # Set up repository
    echo \
      "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/$OS \
      $(lsb_release -cs) stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null

    # Install Docker Engine
    apt update
    apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

    # Start and enable Docker
    systemctl start docker
    systemctl enable docker

    # Verify installation
    docker --version
    docker compose version

    print_success "Docker installed successfully"
}

configure_docker() {
    print_header "Configuring Docker"

    # Add current user to docker group (if not root)
    if [[ -n "$SUDO_USER" ]]; then
        usermod -aG docker $SUDO_USER
        print_success "Added $SUDO_USER to docker group"
        print_warning "You need to log out and back in for group changes to take effect"
    fi

    # Configure Docker daemon for production
    cat > /etc/docker/daemon.json <<EOF
{
  "log-driver": "json-file",
  "log-opts": {
    "max-size": "10m",
    "max-file": "3"
  },
  "storage-driver": "overlay2"
}
EOF

    systemctl restart docker
    print_success "Docker configured"
}

configure_firewall() {
    print_header "Configuring Firewall"

    if ! command -v ufw &> /dev/null; then
        apt install -y ufw
    fi

    # Enable UFW
    ufw --force enable

    # Allow SSH
    ufw allow 22/tcp
    print_success "Allowed SSH (port 22)"

    # Allow HTTP/HTTPS (always for Nginx)
    ufw allow 80/tcp
    ufw allow 443/tcp
    print_success "Allowed HTTP/HTTPS (ports 80, 443)"

    ufw status
    print_success "Firewall configured"
}

install_nginx() {
    print_header "Installing Nginx"

    if command -v nginx &> /dev/null; then
        print_warning "Nginx is already installed"
        nginx -v
    else
        apt install -y nginx
        print_success "Nginx installed successfully"
    fi

    # Enable and start Nginx
    systemctl enable nginx
    systemctl start nginx

    nginx -v
}

configure_nginx() {
    print_header "Configuring Nginx Reverse Proxy"

    # Ask if user wants to use domain names or just IP
    read -p "Do you have domain names for this server? (y/N): " -n 1 -r
    echo

    if [[ $REPLY =~ ^[Yy]$ ]]; then
        USE_DOMAINS=true
        read -p "Enter your HTTP API domain (e.g., api.yourdomain.com): " HTTP_DOMAIN
        read -p "Enter your gRPC API domain (e.g., grpc.yourdomain.com): " GRPC_DOMAIN

        if [[ -z "$HTTP_DOMAIN" ]] || [[ -z "$GRPC_DOMAIN" ]]; then
            print_error "Domain names are required"
            exit 1
        fi
    else
        USE_DOMAINS=false
        HTTP_DOMAIN="_"
        GRPC_DOMAIN="_"
    fi

    # Create Nginx configuration
    cat > /etc/nginx/sites-available/nyyu-market <<EOF
# Nyyu Market - Nginx Configuration
# Upstream for HTTP API
upstream nyyu_http_api {
    server localhost:8080;
    keepalive 32;
}

# Upstream for gRPC API
upstream nyyu_grpc_api {
    server localhost:50051;
    keepalive 32;
}

# HTTP API Server
server {
    listen 80;
    server_name ${HTTP_DOMAIN};

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
        proxy_buffering off;
        proxy_request_buffering off;
    }

    location /health {
        proxy_pass http://nyyu_http_api/health;
        access_log off;
    }

    location /api/v1/stats {
        proxy_pass http://nyyu_http_api/api/v1/stats;
    }

    access_log /var/log/nginx/nyyu_http_access.log;
    error_log /var/log/nginx/nyyu_http_error.log;
}
EOF

    # Create symlink
    ln -sf /etc/nginx/sites-available/nyyu-market /etc/nginx/sites-enabled/nyyu-market

    # Remove default site
    rm -f /etc/nginx/sites-enabled/default

    # Test config
    nginx -t

    # Reload Nginx
    systemctl reload nginx

    print_success "Nginx configured successfully"

    if [[ "$USE_DOMAINS" == true ]]; then
        print_info "HTTP API will be accessible at: http://${HTTP_DOMAIN}"
        print_info "After SSL setup: https://${HTTP_DOMAIN}"
    else
        print_info "HTTP API accessible at: http://your-server-ip"
    fi
}

setup_ssl() {
    print_header "Setting Up SSL with Let's Encrypt"

    if [[ "$USE_DOMAINS" != true ]]; then
        print_warning "SSL requires domain names. Skipping SSL setup."
        print_info "You can set up SSL later by running: sudo bash setup-ssl.sh"
        return
    fi

    # Install Certbot
    print_info "Installing Certbot..."
    apt install -y certbot python3-certbot-nginx

    # Ask for email
    read -p "Enter email for SSL certificate notifications: " SSL_EMAIL

    if [[ -z "$SSL_EMAIL" ]]; then
        print_warning "Email required for SSL. Skipping SSL setup."
        print_info "You can set up SSL later by running: sudo bash setup-ssl.sh"
        return
    fi

    # Check DNS
    print_info "Checking DNS configuration..."
    print_warning "Make sure your domains point to this server!"
    echo ""
    print_info "Checking ${HTTP_DOMAIN}..."
    if host "$HTTP_DOMAIN" > /dev/null 2>&1; then
        IP=$(host "$HTTP_DOMAIN" | grep "has address" | awk '{print $4}' | head -1)
        print_success "${HTTP_DOMAIN} resolves to ${IP}"
    else
        print_warning "${HTTP_DOMAIN} does not resolve"
    fi

    if [[ -n "$GRPC_DOMAIN" ]] && [[ "$GRPC_DOMAIN" != "_" ]]; then
        print_info "Checking ${GRPC_DOMAIN}..."
        if host "$GRPC_DOMAIN" > /dev/null 2>&1; then
            IP=$(host "$GRPC_DOMAIN" | grep "has address" | awk '{print $4}' | head -1)
            print_success "${GRPC_DOMAIN} resolves to ${IP}"
        else
            print_warning "${GRPC_DOMAIN} does not resolve"
        fi
    fi

    echo ""
    read -p "Continue with SSL certificate request? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        print_info "Skipping SSL setup. You can run it later: sudo bash setup-ssl.sh"
        return
    fi

    # Request certificates
    print_info "Requesting SSL certificates from Let's Encrypt..."

    if [[ -n "$GRPC_DOMAIN" ]] && [[ "$GRPC_DOMAIN" != "_" ]]; then
        # Both domains
        certbot --nginx \
            -d "$HTTP_DOMAIN" \
            -d "$GRPC_DOMAIN" \
            --non-interactive \
            --agree-tos \
            -m "$SSL_EMAIL" \
            --redirect || {
                print_warning "SSL setup failed. You can retry with: sudo bash setup-ssl.sh"
                return
            }
    else
        # Only HTTP domain
        certbot --nginx \
            -d "$HTTP_DOMAIN" \
            --non-interactive \
            --agree-tos \
            -m "$SSL_EMAIL" \
            --redirect || {
                print_warning "SSL setup failed. You can retry with: sudo bash setup-ssl.sh"
                return
            }
    fi

    print_success "SSL certificates installed successfully!"

    # Enable auto-renewal
    systemctl enable certbot.timer
    systemctl start certbot.timer

    print_success "SSL auto-renewal enabled"
    print_info "Certificates will automatically renew before expiration"
}

generate_passwords() {
    print_header "Generating Secure Passwords"

    CLICKHOUSE_PASSWORD=$(openssl rand -base64 32)
    REDIS_PASSWORD=$(openssl rand -base64 32)

    print_success "Secure passwords generated"
}

create_env_file() {
    print_header "Creating Environment Configuration"

    if [[ -f .env ]]; then
        print_warning ".env file already exists"
        read -p "Do you want to overwrite it? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            print_info "Keeping existing .env file"
            return
        fi
        cp .env .env.backup.$(date +%Y%m%d_%H%M%S)
        print_success "Backed up existing .env file"
    fi

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
    print_success "Environment file created (.env)"

    # Save passwords to a secure file
    cat > .passwords.txt <<EOF
NYYU MARKET - SECURE PASSWORDS
Generated: $(date)

ClickHouse Password: ${CLICKHOUSE_PASSWORD}
Redis Password: ${REDIS_PASSWORD}

IMPORTANT: Store these passwords securely and delete this file after saving them!
EOF
    chmod 600 .passwords.txt

    print_warning "Passwords saved to .passwords.txt - Save them securely and delete the file!"
}

update_docker_compose() {
    print_header "Updating docker-compose.yml with Passwords"

    # Update docker-compose.yml with generated passwords
    sed -i.bak "s/CLICKHOUSE_PASSWORD=/CLICKHOUSE_PASSWORD=${CLICKHOUSE_PASSWORD}/g" docker-compose.yml
    sed -i.bak "s/REDIS_PASSWORD=/REDIS_PASSWORD=${REDIS_PASSWORD}/g" docker-compose.yml

    print_success "docker-compose.yml updated with secure passwords"
}

create_directories() {
    print_header "Creating Required Directories"

    mkdir -p logs
    mkdir -p backups

    print_success "Directories created"
}

deploy_application() {
    print_header "Deploying Nyyu Market Application"

    # Pull latest images
    print_info "Pulling Docker images..."
    docker compose pull

    # Build application
    print_info "Building application..."
    docker compose build

    # Start services
    print_info "Starting services..."
    docker compose up -d

    print_success "Application deployed"
}

wait_for_services() {
    print_header "Waiting for Services to be Healthy"

    print_info "Waiting for ClickHouse..."
    timeout=60
    while [ $timeout -gt 0 ]; do
        if docker compose exec -T clickhouse clickhouse-client --query "SELECT 1" &> /dev/null; then
            print_success "ClickHouse is ready"
            break
        fi
        sleep 2
        timeout=$((timeout-2))
    done

    if [ $timeout -le 0 ]; then
        print_error "ClickHouse failed to start"
        docker compose logs clickhouse
        exit 1
    fi

    print_info "Waiting for Redis..."
    timeout=60
    while [ $timeout -gt 0 ]; do
        if docker compose exec -T redis redis-cli ping &> /dev/null; then
            print_success "Redis is ready"
            break
        fi
        sleep 2
        timeout=$((timeout-2))
    done

    if [ $timeout -le 0 ]; then
        print_error "Redis failed to start"
        docker compose logs redis
        exit 1
    fi

    print_info "Waiting for Nyyu Market service..."
    sleep 10

    timeout=60
    while [ $timeout -gt 0 ]; do
        if curl -s http://localhost:8080/health > /dev/null 2>&1; then
            print_success "Nyyu Market service is ready"
            break
        fi
        sleep 2
        timeout=$((timeout-2))
    done

    if [ $timeout -le 0 ]; then
        print_warning "Service health check timeout, but it might still be starting..."
        docker compose logs nyyu-market | tail -20
    fi
}

verify_installation() {
    print_header "Verifying Installation"

    # Check containers
    print_info "Container Status:"
    docker compose ps

    # Test health endpoint
    print_info "Testing health endpoint..."
    HEALTH_RESPONSE=$(curl -s http://localhost:8080/health || echo "Failed")

    if [[ $HEALTH_RESPONSE == *"healthy"* ]]; then
        print_success "Health check passed"
        echo "$HEALTH_RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$HEALTH_RESPONSE"
    else
        print_error "Health check failed"
        echo "$HEALTH_RESPONSE"
    fi

    # Check logs
    print_info "Recent logs:"
    docker compose logs --tail=20 nyyu-market
}

setup_systemd_service() {
    print_header "Setting Up Systemd Service for Auto-start"

    cat > /etc/systemd/system/nyyu-market.service <<EOF
[Unit]
Description=Nyyu Market Service
Requires=docker.service
After=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=$SCRIPT_DIR
ExecStart=/usr/bin/docker compose up -d
ExecStop=/usr/bin/docker compose down
TimeoutStartSec=0

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable nyyu-market.service

    print_success "Systemd service installed (auto-start on boot enabled)"
}

create_management_scripts() {
    print_header "Creating Management Scripts"

    # Start script
    cat > start.sh <<'EOF'
#!/bin/bash
docker compose up -d
echo "✅ Nyyu Market started"
EOF

    # Stop script
    cat > stop.sh <<'EOF'
#!/bin/bash
docker compose down
echo "✅ Nyyu Market stopped"
EOF

    # Restart script
    cat > restart.sh <<'EOF'
#!/bin/bash
docker compose restart
echo "✅ Nyyu Market restarted"
EOF

    # Logs script
    cat > logs.sh <<'EOF'
#!/bin/bash
docker compose logs -f "$@"
EOF

    # Status script
    cat > status.sh <<'EOF'
#!/bin/bash
echo "=== Container Status ==="
docker compose ps
echo ""
echo "=== Health Check ==="
curl -s http://localhost:8080/health | python3 -m json.tool 2>/dev/null || curl -s http://localhost:8080/health
echo ""
echo "=== Stats ==="
curl -s http://localhost:8080/api/v1/stats | python3 -m json.tool 2>/dev/null || curl -s http://localhost:8080/api/v1/stats
EOF

    # Update script
    cat > update.sh <<'EOF'
#!/bin/bash
echo "🔄 Updating Nyyu Market..."
git pull
docker compose build
docker compose up -d
echo "✅ Update complete"
docker compose logs -f nyyu-market
EOF

    # Backup script
    cat > backup.sh <<'EOF'
#!/bin/bash
BACKUP_DIR="./backups"
DATE=$(date +%Y%m%d_%H%M%S)

mkdir -p "$BACKUP_DIR"

echo "🔄 Backing up ClickHouse..."
docker run --rm \
  -v nyyu-market_clickhouse_data:/data \
  -v $(pwd)/backups:/backup \
  alpine tar czf /backup/clickhouse_$DATE.tar.gz -C /data .

echo "🔄 Backing up Redis..."
docker compose exec -T redis redis-cli SAVE
docker run --rm \
  -v nyyu-market_redis_data:/data \
  -v $(pwd)/backups:/backup \
  alpine tar czf /backup/redis_$DATE.tar.gz -C /data .

echo "✅ Backup complete: $BACKUP_DIR"
ls -lh "$BACKUP_DIR"
EOF

    chmod +x start.sh stop.sh restart.sh logs.sh status.sh update.sh backup.sh

    print_success "Management scripts created"
}

print_summary() {
    print_header "Installation Complete! 🎉"

    echo -e "${GREEN}"
    echo "Nyyu Market has been successfully installed and is now running!"
    echo -e "${NC}"

    echo ""
    echo -e "${BLUE}=== Service Information ===${NC}"
    if [[ "$USE_DOMAINS" == true ]] && [[ -n "$SSL_EMAIL" ]]; then
        echo "HTTP API:      https://${HTTP_DOMAIN}"
        echo "gRPC API:      grpc://${GRPC_DOMAIN}:443 (with TLS)"
        echo "Health Check:  https://${HTTP_DOMAIN}/health"
        echo "Stats:         https://${HTTP_DOMAIN}/api/v1/stats"
    elif [[ "$USE_DOMAINS" == true ]]; then
        echo "HTTP API:      http://${HTTP_DOMAIN}"
        echo "gRPC API:      localhost:50051 (direct)"
        echo "Health Check:  http://${HTTP_DOMAIN}/health"
        echo "Stats:         http://${HTTP_DOMAIN}/api/v1/stats"
    else
        echo "HTTP API:      http://your-server-ip"
        echo "gRPC API:      your-server-ip:50051 (direct)"
        echo "Health Check:  http://your-server-ip/health"
        echo "Stats:         http://your-server-ip/api/v1/stats"
    fi

    echo ""
    echo -e "${BLUE}=== Management Commands ===${NC}"
    echo "./start.sh          - Start all services"
    echo "./stop.sh           - Stop all services"
    echo "./restart.sh        - Restart all services"
    echo "./logs.sh           - View logs (add service name, e.g., ./logs.sh nyyu-market)"
    echo "./status.sh         - Check service status and health"
    echo "./update.sh         - Pull updates and restart"
    echo "./backup.sh         - Backup ClickHouse and Redis data"

    echo ""
    echo -e "${BLUE}=== Docker Commands ===${NC}"
    echo "docker compose ps                  - View containers"
    echo "docker compose logs -f             - View all logs"
    echo "docker compose logs -f nyyu-market - View app logs"
    echo "docker compose restart             - Restart all services"
    echo "docker compose down                - Stop and remove containers"
    echo "docker compose up -d --build       - Rebuild and start"

    echo ""
    echo -e "${YELLOW}=== Important Notes ===${NC}"
    echo "1. Passwords saved in .passwords.txt - SAVE THEM SECURELY!"
    echo "2. Service will auto-start on system boot"
    echo "3. Logs are stored in ./logs directory"
    echo "4. Backups can be created with ./backup.sh"

    if [[ -n "$SUDO_USER" ]]; then
        echo ""
        echo -e "${YELLOW}⚠️  IMPORTANT: Log out and back in for docker group changes to take effect${NC}"
        echo "   Or run: newgrp docker"
    fi

    echo ""
    echo -e "${BLUE}=== Next Steps ===${NC}"
    echo "1. Check service status: ./status.sh"
    echo "2. View logs: ./logs.sh"
    if [[ "$USE_DOMAINS" == true ]] && [[ -n "$SSL_EMAIL" ]]; then
        echo "3. Test HTTP API: curl https://${HTTP_DOMAIN}/health"
        echo "4. Test gRPC with Postman: grpc://${GRPC_DOMAIN}:443 (enable TLS)"
        echo "5. Verify SSL: https://www.ssllabs.com/ssltest/analyze.html?d=${HTTP_DOMAIN}"
    else
        echo "3. Test HTTP API: curl http://your-server-ip/health"
        echo "4. Test gRPC with Postman: your-server-ip:50051"
        if [[ "$USE_DOMAINS" == true ]]; then
            echo "5. Set up SSL: sudo bash setup-ssl.sh"
        else
            echo "5. Configure domain and SSL for production"
        fi
    fi

    echo ""
    echo -e "${GREEN}Installation log saved to: install.log${NC}"
    echo ""
}

###############################################################################
# Main Installation Flow
###############################################################################

main() {
    # Log everything
    exec > >(tee -a install.log)
    exec 2>&1

    print_header "Nyyu Market - Automated Installation"
    echo "Started: $(date)"
    echo ""

    # Pre-installation checks
    check_root
    check_os

    # Installation steps
    install_dependencies
    install_docker
    configure_docker
    install_nginx
    configure_nginx
    configure_firewall
    setup_ssl
    generate_passwords
    create_env_file
    update_docker_compose
    create_directories
    deploy_application
    wait_for_services
    verify_installation
    setup_systemd_service
    create_management_scripts

    # Summary
    print_summary
}

###############################################################################
# Run Installation
###############################################################################

# Check if running with sudo/root
if [[ $EUID -eq 0 ]]; then
    main "$@"
else
    print_error "This script must be run with sudo"
    echo "Usage: sudo bash install.sh"
    exit 1
fi
