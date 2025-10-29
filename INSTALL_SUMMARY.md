# Nyyu Market - Installation Summary

## ✅ Complete Installation Package Ready!

Your nyyu-market service now includes:

### 📦 What's Included

1. **Docker & Docker Compose Setup**
2. **Nginx Reverse Proxy** (automatic configuration)
3. **Let's Encrypt SSL/TLS** (automatic setup)
4. **6-Exchange Aggregation** (Binance, Kraken, Coinbase, Bybit, OKX, Gate.io)
5. **In-memory Candle Aggregation** (saves aggregated data only)
6. **ClickHouse + Redis** (time-series storage & caching)
7. **gRPC + HTTP APIs**
8. **Auto-start on boot**
9. **Management scripts**

---

## 🚀 One-Command Installation

### Full Installation (Production Server)

```bash
sudo bash install.sh
```

**This single command will:**
- ✅ Install Docker & Docker Compose
- ✅ Install & configure Nginx
- ✅ Set up Let's Encrypt SSL (if you have domains)
- ✅ Configure firewall (UFW)
- ✅ Generate secure passwords
- ✅ Deploy all services
- ✅ Enable auto-start
- ✅ Create management scripts

**Time:** ~10-15 minutes

**What it asks you:**
1. Do you have domain names? (y/n)
   - If yes: Enter HTTP API domain (e.g., api.yourdomain.com)
   - If yes: Enter gRPC API domain (e.g., grpc.yourdomain.com)
2. Do you want SSL now? (y/n)
   - If yes: Enter email for SSL notifications

---

## 📋 Installation Options

### Option 1: With Domain Names + SSL (Recommended)

**Requirements:**
- Domain names pointing to your server
- Ports 80, 443 open

**Result:**
- ✅ HTTPS enabled with valid SSL certificate
- ✅ Auto-renewal configured
- ✅ Production-ready
- ✅ Secure gRPC with TLS

**Access:**
```
HTTP API:  https://api.yourdomain.com
gRPC API:  grpc://grpc.yourdomain.com:443 (with TLS)
```

### Option 2: With Domain Names (No SSL yet)

**Requirements:**
- Domain names pointing to your server

**Result:**
- ✅ HTTP access via domain
- ⚠️ No SSL (can add later)

**Access:**
```
HTTP API:  http://api.yourdomain.com
gRPC API:  localhost:50051 (direct)
```

**Add SSL later:**
```bash
sudo bash setup-ssl.sh
```

### Option 3: IP Address Only (Development)

**Requirements:**
- Nothing, works immediately

**Result:**
- ✅ Works with server IP
- ⚠️ No SSL
- ⚠️ Not recommended for production

**Access:**
```
HTTP API:  http://your-server-ip
gRPC API:  your-server-ip:50051
```

---

## 🎯 Step-by-Step Guide

### 1. Upload Files to Server

```bash
# Via SCP
scp -r nyyu-market user@your-server:/opt/

# Or via Git
ssh user@your-server
cd /opt
git clone <your-repo-url> nyyu-market
cd nyyu-market
```

### 2. Run Installation

```bash
cd /opt/nyyu-market
sudo bash install.sh
```

### 3. Follow Prompts

The script will guide you through:
- Domain configuration (optional)
- SSL setup (optional)
- Service deployment

### 4. Wait for Completion

Installation takes ~10-15 minutes depending on:
- Server speed
- Internet connection
- Number of Docker images to download

### 5. Test Your Installation

```bash
# Check status
./status.sh

# View logs
./logs.sh

# Test HTTP API
curl http://your-domain-or-ip/health

# Test with Postman (gRPC)
# URL: your-domain-or-ip:50051
# Proto: proto/market.proto
```

---

## 🔧 After Installation

### Management Scripts Created

```bash
./start.sh      # Start all services
./stop.sh       # Stop all services
./restart.sh    # Restart all services
./logs.sh       # View logs
./status.sh     # Check status and health
./update.sh     # Pull updates and restart
./backup.sh     # Backup data
```

### Service Access

**With SSL:**
- HTTP API: `https://api.yourdomain.com`
- gRPC API: `grpc://grpc.yourdomain.com:443`
- Health: `https://api.yourdomain.com/health`

**Without SSL:**
- HTTP API: `http://your-server-ip`
- gRPC API: `your-server-ip:50051`
- Health: `http://your-server-ip/health`

### Important Files

```
.env                   # Configuration (passwords here!)
.passwords.txt         # Generated passwords (save & delete)
install.log            # Installation log
docker-compose.yml     # Docker configuration
```

---

## 🔒 SSL/TLS Setup

### Automatic SSL (During Installation)

If you have domains, the installer will:
1. Check DNS configuration
2. Request SSL certificates from Let's Encrypt
3. Configure auto-renewal
4. Enable HTTPS redirect

### Manual SSL Setup (After Installation)

```bash
sudo bash setup-ssl.sh
```

**Requirements:**
- Domains must point to server
- Ports 80, 443 open
- Nginx must be configured

**What it does:**
- Installs Certbot
- Requests Let's Encrypt certificates
- Configures Nginx for HTTPS
- Sets up auto-renewal (checks twice daily)
- Enables HTTP to HTTPS redirect

**Certificate Info:**
- Valid for 90 days
- Auto-renews at 30 days
- No cost, no limit

---

## 📊 What Gets Installed

### System Packages
- Docker Engine
- Docker Compose
- Nginx
- Certbot (if SSL enabled)
- UFW Firewall

### Docker Containers
- **nyyu-market** - Main application
- **nyyu-market-clickhouse** - Time-series database
- **nyyu-market-redis** - Cache & pub/sub

### Ports Configuration

**Firewall (UFW):**
- 22 (SSH)
- 80 (HTTP)
- 443 (HTTPS)

**Docker Containers:**
- 50051 (gRPC) - Internal, accessed via Nginx
- 8080 (HTTP API) - Internal, accessed via Nginx
- 9000 (ClickHouse) - Internal only
- 6379 (Redis) - Internal only

**Nginx Proxies:**
- 80 → Docker 8080 (HTTP API)
- 443 → Docker 8080 (HTTPS HTTP API)
- 443 → Docker 50051 (HTTPS gRPC with TLS)

---

## 🧪 Testing

### Test HTTP API

```bash
# Health check
curl http://your-domain-or-ip/health

# Stats
curl http://your-domain-or-ip/api/v1/stats

# With SSL
curl https://api.yourdomain.com/health
```

### Test gRPC with Postman

**Without SSL:**
1. Create new gRPC Request
2. URL: `your-server-ip:50051`
3. Uncheck "Use TLS connection"
4. Import proto: `proto/market.proto`
5. Test: Health, GetLatestCandle, GetPrice

**With SSL:**
1. Create new gRPC Request
2. URL: `grpc.yourdomain.com:443`
3. Check "Use TLS connection"
4. Import proto: `proto/market.proto`
5. Test: Health, GetLatestCandle, GetPrice

### Verify Aggregated Candles

Test with Postman:
```json
{
  "symbol": "BTCUSDT",
  "interval": "1m",
  "source": "aggregated"
}
```

Response should show:
- `source: "aggregated"`
- `trade_count: 6` (or number of exchanges)
- Volume is SUM of all exchanges

---

## 🔄 Updates

### Update Application

```bash
# Pull latest code
git pull

# Rebuild and restart
docker compose up -d --build

# Or use update script
./update.sh
```

### Update System

```bash
sudo apt update
sudo apt upgrade
```

### Renew SSL Manually

```bash
sudo certbot renew
```

---

## 🆘 Troubleshooting

### Installation Failed

```bash
# Check logs
cat install.log

# Try again
sudo bash install.sh
```

### SSL Failed

**Check:**
1. Domains point to server: `nslookup api.yourdomain.com`
2. Ports open: `sudo ufw status`
3. No other service on port 80: `sudo lsof -i :80`

**Retry:**
```bash
sudo bash setup-ssl.sh
```

### Service Not Starting

```bash
# Check container logs
docker compose logs nyyu-market

# Check all containers
docker compose ps

# Restart
docker compose restart
```

### Exchange Not Connecting

```bash
# Check logs for exchange errors
docker compose logs nyyu-market | grep -i "binance\|kraken"

# Restart service
docker compose restart nyyu-market
```

---

## 📚 Documentation

- **Installation:** `DEPLOYMENT_README.md`
- **Cloud Deployment:** `CLOUD_DEPLOYMENT_GUIDE.md`
- **Architecture:** `EXCHANGE_IMPLEMENTATION_COMPLETE.md`
- **API:** `proto/market.proto`

---

## ✅ Success Checklist

After installation, verify:

- [ ] All containers running: `docker compose ps`
- [ ] Health check passing: `curl http://your-server/health`
- [ ] Nginx working: `sudo systemctl status nginx`
- [ ] SSL active (if configured): `curl https://api.yourdomain.com/health`
- [ ] Exchanges connecting (check logs)
- [ ] Aggregated candles being written
- [ ] Auto-start enabled: `sudo systemctl status nyyu-market`
- [ ] Firewall configured: `sudo ufw status`
- [ ] Passwords saved securely

---

## 🎉 You're Done!

Your nyyu-market service is now:
- ✅ Running and aggregating from 6 exchanges
- ✅ Accessible via HTTP/HTTPS and gRPC
- ✅ Secured with SSL (if configured)
- ✅ Auto-restarting on failures
- ✅ Auto-starting on boot
- ✅ Ready for production

**Next:** Test your APIs with Postman!
