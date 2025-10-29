# Cloud Deployment Guide - Nyyu Market Service

Complete guide to deploy nyyu-market service to cloud servers (AWS, GCP, DigitalOcean, etc.)

## Prerequisites

- Ubuntu/Debian server (20.04+ recommended)
- At least 2GB RAM, 2 vCPU
- Root or sudo access
- Domain name (optional but recommended)

## Table of Contents

1. [Server Setup](#server-setup)
2. [Install Docker & Docker Compose](#install-docker--docker-compose)
3. [Deploy Application](#deploy-application)
4. [Configure Firewall](#configure-firewall)
5. [Set Up SSL (Optional)](#set-up-ssl-optional)
6. [Monitoring & Maintenance](#monitoring--maintenance)
7. [Troubleshooting](#troubleshooting)

---

## Server Setup

### 1. Connect to Your Server

```bash
ssh root@your-server-ip
# or
ssh user@your-server-ip
```

### 2. Update System

```bash
sudo apt update
sudo apt upgrade -y
```

### 3. Install Basic Tools

```bash
sudo apt install -y git curl wget nano htop net-tools
```

---

## Install Docker & Docker Compose

### Install Docker

```bash
# Remove old versions
sudo apt remove docker docker-engine docker.io containerd runc

# Install dependencies
sudo apt install -y \
    ca-certificates \
    curl \
    gnupg \
    lsb-release

# Add Docker's official GPG key
sudo mkdir -p /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg

# Set up repository
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \
  $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

# Install Docker Engine
sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

# Verify installation
sudo docker --version
sudo docker compose version
```

### Configure Docker (Optional but Recommended)

```bash
# Add your user to docker group (avoid using sudo)
sudo usermod -aG docker $USER

# Apply group changes
newgrp docker

# Test without sudo
docker ps
```

---

## Deploy Application

### 1. Clone Repository

```bash
# Create directory for the application
sudo mkdir -p /opt/nyyu-market
cd /opt/nyyu-market

# Clone your repository
git clone <your-repo-url> .
# or upload files via scp/rsync
```

### 2. Create Production Environment File

```bash
cd /opt/nyyu-market
nano .env
```

Add the following configuration:

```env
# Server Configuration
SERVER_PORT=50051
HTTP_PORT=8080
ENVIRONMENT=production

# ClickHouse Configuration (uses Docker container)
CLICKHOUSE_HOST=clickhouse
CLICKHOUSE_PORT=9000
CLICKHOUSE_DATABASE=trade
CLICKHOUSE_USERNAME=default
CLICKHOUSE_PASSWORD=your-secure-password-here

# Redis Configuration (uses Docker container)
REDIS_HOST=redis
REDIS_PORT=6379
REDIS_PASSWORD=your-redis-password-here
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

# Exchange Configuration (enable/disable exchanges)
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
```

**IMPORTANT**: Replace passwords with strong random values:
```bash
# Generate secure passwords
openssl rand -base64 32
```

### 3. Update docker-compose.yml for Production

Edit `docker-compose.yml`:

```bash
nano docker-compose.yml
```

Update ClickHouse password:

```yaml
services:
  clickhouse:
    # ... existing config ...
    environment:
      - CLICKHOUSE_DB=trade
      - CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1
      - CLICKHOUSE_PASSWORD=your-secure-password-here  # Add this
```

Update nyyu-market service:

```yaml
  nyyu-market:
    # ... existing config ...
    environment:
      - CLICKHOUSE_PASSWORD=your-secure-password-here  # Same as above
      - REDIS_PASSWORD=your-redis-password-here        # If using Redis password
```

### 4. Build and Start Services

```bash
cd /opt/nyyu-market

# Build the application
docker compose build

# Start all services
docker compose up -d

# Check status
docker compose ps

# View logs
docker compose logs -f
```

Expected output:
```
✔ Container nyyu-market-clickhouse  Started
✔ Container nyyu-market-redis       Started
✔ Container nyyu-market             Started
```

### 5. Verify Deployment

```bash
# Check all containers are running
docker compose ps

# Check logs for errors
docker compose logs nyyu-market

# Test health endpoint
curl http://localhost:8080/health

# Test gRPC endpoint
docker compose exec nyyu-market wget -O- http://localhost:8080/health
```

Expected health response:
```json
{"healthy":true,"version":"1.0.0","uptime_seconds":120,"services":{"clickhouse":"healthy","redis":"healthy"}}
```

---

## Configure Firewall

### Using UFW (Ubuntu Firewall)

```bash
# Enable firewall
sudo ufw enable

# Allow SSH (IMPORTANT - do this first!)
sudo ufw allow 22/tcp

# Allow HTTP/HTTPS (if using reverse proxy)
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp

# Allow gRPC port (if accessing directly)
sudo ufw allow 50051/tcp

# Allow HTTP API port (if accessing directly)
sudo ufw allow 8080/tcp

# Check status
sudo ufw status
```

### Using iptables

```bash
# Allow gRPC
sudo iptables -A INPUT -p tcp --dport 50051 -j ACCEPT

# Allow HTTP API
sudo iptables -A INPUT -p tcp --dport 8080 -j ACCEPT

# Save rules
sudo netfilter-persistent save
```

**IMPORTANT**: For production, use a reverse proxy (Nginx) instead of exposing ports directly.

---

## Set Up Reverse Proxy (Nginx) - Recommended

### 1. Install Nginx

```bash
sudo apt install -y nginx
```

### 2. Configure Nginx for gRPC

Create configuration file:

```bash
sudo nano /etc/nginx/sites-available/nyyu-market
```

Add this configuration:

```nginx
# HTTP API
server {
    listen 80;
    server_name api.yourdomain.com;

    location / {
        proxy_pass http://localhost:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /health {
        proxy_pass http://localhost:8080/health;
        access_log off;
    }
}

# gRPC Service
server {
    listen 443 ssl http2;
    server_name grpc.yourdomain.com;

    ssl_certificate /etc/letsencrypt/live/grpc.yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/grpc.yourdomain.com/privkey.pem;

    location / {
        grpc_pass grpc://localhost:50051;
        grpc_set_header Host $host;
        grpc_set_header X-Real-IP $remote_addr;
        grpc_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

Enable site:

```bash
sudo ln -s /etc/nginx/sites-available/nyyu-market /etc/nginx/sites-enabled/
sudo nginx -t
sudo systemctl reload nginx
```

### 3. Install SSL Certificate (Let's Encrypt)

```bash
# Install Certbot
sudo apt install -y certbot python3-certbot-nginx

# Get certificate
sudo certbot --nginx -d api.yourdomain.com -d grpc.yourdomain.com

# Auto-renewal is configured automatically
sudo systemctl status certbot.timer
```

---

## Monitoring & Maintenance

### View Logs

```bash
# All services
docker compose logs -f

# Specific service
docker compose logs -f nyyu-market

# Last 100 lines
docker compose logs --tail=100 nyyu-market

# Follow with timestamps
docker compose logs -f -t nyyu-market
```

### Check Resource Usage

```bash
# Container stats
docker stats

# Server resources
htop

# Disk usage
df -h
docker system df
```

### Restart Services

```bash
# Restart all
docker compose restart

# Restart specific service
docker compose restart nyyu-market

# Restart with rebuild
docker compose up -d --build nyyu-market
```

### Update Application

```bash
cd /opt/nyyu-market

# Pull latest code
git pull

# Rebuild and restart
docker compose up -d --build

# Check logs
docker compose logs -f nyyu-market
```

### Backup Data

```bash
# Backup ClickHouse data
docker compose exec clickhouse clickhouse-client --query "BACKUP DATABASE trade TO Disk('backups', 'trade_backup.zip')"

# Or backup Docker volume
docker run --rm -v nyyu-market_clickhouse_data:/data -v $(pwd)/backups:/backup alpine tar czf /backup/clickhouse_backup_$(date +%Y%m%d).tar.gz -C /data .

# Backup Redis
docker compose exec redis redis-cli SAVE
docker run --rm -v nyyu-market_redis_data:/data -v $(pwd)/backups:/backup alpine tar czf /backup/redis_backup_$(date +%Y%m%d).tar.gz -C /data .
```

### Clean Up

```bash
# Remove unused images
docker image prune -a

# Remove unused volumes
docker volume prune

# Full cleanup
docker system prune -a --volumes
```

---

## Systemd Service (Auto-start on Boot)

Create systemd service:

```bash
sudo nano /etc/systemd/system/nyyu-market.service
```

Add:

```ini
[Unit]
Description=Nyyu Market Service
Requires=docker.service
After=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/opt/nyyu-market
ExecStart=/usr/bin/docker compose up -d
ExecStop=/usr/bin/docker compose down
TimeoutStartSec=0

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable nyyu-market.service
sudo systemctl start nyyu-market.service
sudo systemctl status nyyu-market.service
```

---

## Troubleshooting

### Container Won't Start

```bash
# Check logs
docker compose logs nyyu-market

# Check container status
docker compose ps

# Recreate container
docker compose up -d --force-recreate nyyu-market
```

### ClickHouse Connection Failed

```bash
# Test ClickHouse connection
docker compose exec clickhouse clickhouse-client

# Check if ClickHouse is running
docker compose ps clickhouse

# Check ClickHouse logs
docker compose logs clickhouse
```

### Redis Connection Failed

```bash
# Test Redis connection
docker compose exec redis redis-cli ping

# Check Redis logs
docker compose logs redis
```

### Exchange WebSocket Errors

```bash
# Check logs for specific exchange errors
docker compose logs nyyu-market | grep -i "kraken\|binance\|coinbase"

# Verify internet connectivity from container
docker compose exec nyyu-market ping -c 3 stream.binance.com
```

### High Memory Usage

```bash
# Check memory usage
docker stats

# Restart service
docker compose restart nyyu-market

# Adjust batch sizes in .env
BATCH_WRITE_SIZE=50
BATCH_WRITE_INTERVAL=2s
```

### Port Already in Use

```bash
# Find what's using the port
sudo lsof -i :50051

# Kill the process
sudo kill -9 <PID>

# Or change port in docker-compose.yml
ports:
  - "50052:50051"  # External:Internal
```

---

## Performance Tuning

### For High Traffic

Edit `.env`:

```env
# Increase batch size
BATCH_WRITE_SIZE=500
BATCH_WRITE_INTERVAL=2s

# Increase cache TTL
CACHE_TTL_PRICE=120
CACHE_TTL_CANDLE=10

# Adjust limits
MAX_CANDLES_LIMIT=5000
```

### ClickHouse Optimization

```bash
# Increase memory limit in docker-compose.yml
clickhouse:
  # ... existing config ...
  environment:
    - CLICKHOUSE_MAX_MEMORY_USAGE=4000000000  # 4GB
```

### Docker Resource Limits

Add to `docker-compose.yml`:

```yaml
nyyu-market:
  # ... existing config ...
  deploy:
    resources:
      limits:
        cpus: '2'
        memory: 2G
      reservations:
        cpus: '1'
        memory: 1G
```

---

## Security Checklist

- ✅ Use strong passwords for ClickHouse and Redis
- ✅ Enable firewall (UFW)
- ✅ Use SSL/TLS for external access
- ✅ Use reverse proxy (Nginx) instead of direct port exposure
- ✅ Keep Docker and system updated
- ✅ Regular backups
- ✅ Monitor logs for suspicious activity
- ✅ Use environment variables, not hardcoded secrets
- ✅ Restrict SSH access (key-only, no root login)
- ✅ Enable fail2ban for brute force protection

---

## Quick Commands Cheat Sheet

```bash
# Start services
docker compose up -d

# Stop services
docker compose down

# Restart services
docker compose restart

# View logs
docker compose logs -f

# Check status
docker compose ps

# Update and restart
git pull && docker compose up -d --build

# Backup
./scripts/backup.sh  # Create this script

# Monitor
docker stats

# Health check
curl http://localhost:8080/health
```

---

## Support

For issues or questions:
1. Check logs: `docker compose logs -f`
2. Verify health: `curl http://localhost:8080/health`
3. Check GitHub issues
4. Contact support

---

## Summary

Your nyyu-market service is now deployed with:
- ✅ Docker containerization
- ✅ ClickHouse for time-series data
- ✅ Redis for caching and pub/sub
- ✅ 6-exchange WebSocket aggregation
- ✅ gRPC API on port 50051
- ✅ HTTP API on port 8080
- ✅ Auto-restart on failure
- ✅ Health checks
- ✅ Production-ready configuration

**Service URLs:**
- gRPC: `your-server-ip:50051`
- HTTP API: `http://your-server-ip:8080`
- Health: `http://your-server-ip:8080/health`
- Stats: `http://your-server-ip:8080/api/v1/stats`
