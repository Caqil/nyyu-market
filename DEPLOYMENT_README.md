# Nyyu Market - Deployment Guide

Quick deployment guide for nyyu-market service with Docker.

## 📦 What's Included

- ✅ 6-Exchange WebSocket aggregation (Binance, Kraken, Coinbase, Bybit, OKX, Gate.io)
- ✅ In-memory candle aggregation (saves aggregated data, not individual exchanges)
- ✅ ClickHouse for time-series storage
- ✅ Redis for caching and pub/sub
- ✅ gRPC API (port 50051)
- ✅ HTTP REST API (port 8080)
- ✅ Auto-restart and health checks
- ✅ Docker containerization

## 🚀 Quick Start (3 Methods)

### Method 1: Full Automated Installation (Recommended for Production)

For fresh Ubuntu/Debian servers. Installs everything including Docker:

```bash
# Make script executable
chmod +x install.sh

# Run installation (requires sudo)
sudo bash install.sh
```

**This will:**
- ✅ Install Docker and Docker Compose
- ✅ Configure firewall (UFW)
- ✅ Generate secure passwords
- ✅ Create .env configuration
- ✅ Deploy all services
- ✅ Set up systemd auto-start
- ✅ Create management scripts

**Time:** ~5-10 minutes

---

### Method 2: Quick Start (If Docker Already Installed)

If you already have Docker installed:

```bash
# Make script executable
chmod +x quick-start.sh

# Run quick start
bash quick-start.sh
```

**This will:**
- ✅ Build the application
- ✅ Start all services (ClickHouse, Redis, Nyyu Market)
- ✅ Verify health

**Time:** ~2-3 minutes

---

### Method 3: Manual Docker Compose

For full control:

```bash
# 1. Create .env file
cp .env.example .env
nano .env  # Edit configuration

# 2. Start services
docker compose up -d

# 3. Check status
docker compose ps

# 4. View logs
docker compose logs -f

# 5. Test health
curl http://localhost:8080/health
```

---

## 🖥️ System Requirements

### Minimum
- **OS:** Ubuntu 20.04+ / Debian 11+
- **RAM:** 2GB
- **CPU:** 2 cores
- **Disk:** 20GB
- **Ports:** 50051 (gRPC), 8080 (HTTP)

### Recommended (Production)
- **OS:** Ubuntu 22.04 LTS
- **RAM:** 4GB+
- **CPU:** 4 cores+
- **Disk:** 50GB+ SSD
- **Network:** Stable internet connection

---

## 📋 After Installation

### Check Service Status

```bash
# Using management script (if used install.sh)
./status.sh

# Or using Docker directly
docker compose ps
docker compose logs -f nyyu-market
```

### Test Endpoints

```bash
# Health check
curl http://localhost:8080/health

# Stats
curl http://localhost:8080/api/v1/stats

# Using Postman (gRPC)
# URL: localhost:50051
# Proto file: proto/market.proto
```

### Management Commands

If you used `install.sh`, these scripts are available:

```bash
./start.sh      # Start all services
./stop.sh       # Stop all services
./restart.sh    # Restart all services
./logs.sh       # View logs
./status.sh     # Check status and health
./update.sh     # Pull updates and restart
./backup.sh     # Backup data
```

---

## 🔧 Configuration

### Environment Variables (.env)

Key settings to configure:

```env
# Server
SERVER_PORT=50051       # gRPC port
HTTP_PORT=8080          # HTTP API port
ENVIRONMENT=production  # or development

# Database (ClickHouse)
CLICKHOUSE_HOST=clickhouse
CLICKHOUSE_DATABASE=trade
CLICKHOUSE_PASSWORD=your-secure-password

# Cache (Redis)
REDIS_HOST=redis
REDIS_PASSWORD=your-secure-password

# Exchange Selection (enable/disable)
ENABLE_BINANCE=true
ENABLE_KRAKEN=true
ENABLE_COINBASE=true
ENABLE_BYBIT=true
ENABLE_OKX=true
ENABLE_GATEIO=true

# Performance Tuning
BATCH_WRITE_SIZE=100        # Increase for high volume
BATCH_WRITE_INTERVAL=1s     # Batch write frequency
MAX_CANDLES_LIMIT=1000      # API limit
```

### Port Customization

Edit `docker-compose.yml` to change external ports:

```yaml
ports:
  - "50051:50051"  # Change first number for external port
  - "8080:8080"
```

---

## 🔒 Security Best Practices

### For Production Deployment:

1. **Use Strong Passwords**
   ```bash
   # Generate secure password
   openssl rand -base64 32
   ```

2. **Enable Firewall**
   ```bash
   sudo ufw enable
   sudo ufw allow 22/tcp   # SSH
   sudo ufw allow 80/tcp   # HTTP
   sudo ufw allow 443/tcp  # HTTPS
   ```

3. **Use Reverse Proxy (Nginx)**
   - Don't expose ports 50051/8080 directly
   - Use Nginx with SSL (Let's Encrypt)
   - See: `CLOUD_DEPLOYMENT_GUIDE.md`

4. **Regular Updates**
   ```bash
   # Update system
   sudo apt update && sudo apt upgrade

   # Update application
   git pull
   docker compose up -d --build
   ```

5. **Enable Monitoring**
   - Use Prometheus + Grafana
   - Monitor logs: `docker compose logs -f`
   - Set up alerts for errors

---

## 🔍 Troubleshooting

### Services Won't Start

```bash
# Check logs
docker compose logs

# Check specific service
docker compose logs clickhouse
docker compose logs redis
docker compose logs nyyu-market

# Recreate containers
docker compose down
docker compose up -d --force-recreate
```

### ClickHouse Connection Error

```bash
# Test connection
docker compose exec clickhouse clickhouse-client

# Check if running
docker compose ps clickhouse

# Restart
docker compose restart clickhouse
```

### Redis Connection Error

```bash
# Test connection
docker compose exec redis redis-cli ping

# Check logs
docker compose logs redis

# Restart
docker compose restart redis
```

### Exchange WebSocket Errors

```bash
# Check logs for specific exchange
docker compose logs nyyu-market | grep -i binance

# Verify internet connectivity
docker compose exec nyyu-market ping -c 3 stream.binance.com

# Restart service
docker compose restart nyyu-market
```

### Port Already in Use

```bash
# Find what's using the port
sudo lsof -i :50051

# Kill the process
sudo kill -9 <PID>

# Or change port in docker-compose.yml
```

### Out of Memory

```bash
# Check memory usage
docker stats

# Reduce batch size in .env
BATCH_WRITE_SIZE=50
BATCH_WRITE_INTERVAL=2s

# Restart
docker compose restart nyyu-market
```

---

## 📊 Monitoring

### View Real-time Stats

```bash
# Container resources
docker stats

# Application logs
docker compose logs -f nyyu-market

# Exchange stats (look for connection status)
docker compose logs nyyu-market | grep "connected successfully"
```

### Health Checks

```bash
# HTTP health check
curl http://localhost:8080/health

# Service stats
curl http://localhost:8080/api/v1/stats

# gRPC health check (requires grpcurl)
grpcurl -plaintext localhost:50051 nyyu.market.v1.MarketService/Health
```

---

## 💾 Backup & Recovery

### Create Backup

```bash
# Using backup script (if installed)
./backup.sh

# Manual backup
docker run --rm \
  -v nyyu-market_clickhouse_data:/data \
  -v $(pwd)/backups:/backup \
  alpine tar czf /backup/clickhouse_$(date +%Y%m%d).tar.gz -C /data .
```

### Restore Backup

```bash
# Stop services
docker compose down

# Restore ClickHouse
docker run --rm \
  -v nyyu-market_clickhouse_data:/data \
  -v $(pwd)/backups:/backup \
  alpine tar xzf /backup/clickhouse_YYYYMMDD.tar.gz -C /data

# Start services
docker compose up -d
```

---

## 🔄 Updates

### Update Application

```bash
# Pull latest code
git pull

# Rebuild and restart
docker compose up -d --build

# Check logs
docker compose logs -f nyyu-market
```

### Update Docker Images

```bash
# Pull latest base images
docker compose pull

# Restart
docker compose up -d
```

---

## 📚 Additional Resources

- **Full Deployment Guide:** `CLOUD_DEPLOYMENT_GUIDE.md`
- **gRPC API Docs:** `proto/market.proto`
- **Architecture:** `EXCHANGE_IMPLEMENTATION_COMPLETE.md`

---

## 📞 Support

If you encounter issues:

1. Check logs: `docker compose logs -f`
2. Verify health: `curl http://localhost:8080/health`
3. Check this troubleshooting guide
4. Review `CLOUD_DEPLOYMENT_GUIDE.md` for detailed info

---

## ✅ Success Checklist

After deployment, verify:

- [ ] All containers running: `docker compose ps`
- [ ] Health check passing: `curl http://localhost:8080/health`
- [ ] Exchanges connecting (check logs)
- [ ] Aggregated candles being written (source="aggregated")
- [ ] gRPC accessible via Postman
- [ ] Auto-start enabled (if production)
- [ ] Firewall configured
- [ ] Passwords secured
- [ ] Backups configured

---

## 🎉 You're Done!

Your nyyu-market service is now running and aggregating price data from 6 exchanges!

**Service URLs:**
- gRPC: `your-server-ip:50051`
- HTTP API: `http://your-server-ip:8080`
- Health: `http://your-server-ip:8080/health`

**Test with Postman:**
1. Create new gRPC request
2. URL: `your-server-ip:50051`
3. Import proto: `proto/market.proto`
4. Test methods: Health, GetLatestCandle, GetPrice, etc.
