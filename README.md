# Nyyu Market - Cryptocurrency Market Data Aggregator

Real-time cryptocurrency market data aggregation service supporting 6 major exchanges.

## Features

- ✅ **Multi-Exchange Support**: Binance, Kraken, Coinbase, Bybit, OKX, Gate.io
- ✅ **Real-time Data**: WebSocket connections to all exchanges
- ✅ **Data Aggregation**: Combines candles from multiple exchanges
- ✅ **High Performance**: In-memory aggregation, ClickHouse storage
- ✅ **Dual API**: Both gRPC and HTTP REST APIs
- ✅ **Production Ready**: Docker-based deployment with auto-restart

## Tech Stack

- **Language**: Go 1.24
- **Database**: ClickHouse (time-series data)
- **Cache**: Redis (pub/sub & caching)
- **APIs**: gRPC + HTTP REST
- **Deployment**: Docker Compose

## Architecture

```
┌──────────────────────────┐         ┌─────────────────────────┐
│   Main Trade System      │         │  Nyyu Market Service     │
│                          │         │  (Separate & Optimized)  │
└────────┬─────────────────┘         └──────────┬──────────────┘
         │                                      │
         │                                      │
         │  Can communicate via:                │
         │  • gRPC API calls                    │
         │  • Redis Pub/Sub                     │
         └──────────────────────────────────────┘

           ┌───────────────────────────────────┐
           │  Nyyu Market Databases (SEPARATE) │
           ├───────────────────────────────────┤
           │  ClickHouse: trade (separate)     │
           │  • candles table                  │
           │  • OHLCV data from 6 exchanges    │
           └────────────┬──────────────────────┘
                        │
           ┌────────────▼──────────────────────┐
           │  Redis: localhost:6379            │
           │  • Price & Mark Price caching     │
           │  • Real-time Pub/Sub streaming    │
           └───────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│              Nyyu Market Service Features                    │
├─────────────────────────────────────────────────────────────┤
│  • 6 Exchange WebSocket Aggregation                          │
│  • gRPC API (Port 50051)                                     │
│  • HTTP API (Port 8080)                                      │
│  • Real-time Price Calculation                               │
│  • Mark Price Service (3s updates)                           │
│  • Batch Processing (5 workers)                              │
│  • Redis Pub/Sub Streaming                                   │
└─────────────────────────────────────────────────────────────┘
```

## Performance Characteristics

- **Candle Query**: 1-5ms (ClickHouse)
- **Price Cache Hit**: < 1ms (Redis)
- **Mark Price Update**: Every 3 seconds
- **WebSocket Updates**: Real-time (sub-100ms)
- **gRPC Latency**: < 10ms (average)

---

## Quick Deployment to Azure

### Prerequisites

- Go 1.24+ (on your local machine)
- Azure server with SSH access
- SSH key: `~/Downloads/Azure-NYYU.pem`

### Deploy

Run this script on your **local machine** (Mac):

```bash
./push-to-azure.sh
```

**What it does:**
1. ✅ Builds the binary for Linux (on your Mac - fast!)
2. ✅ Uploads binary and config files to Azure server
3. ✅ Builds Docker image (no compilation on server)
4. ✅ Starts all services (ClickHouse, Redis, API)

**Deploy time:** ~2-3 minutes

---

## Server Configuration

**Domain:** market.nyyu.io
**Server:** nyyu-market.uksouth.cloudapp.azure.com
**User:** azureuser

### Services

| Service | Port | Access |
|---------|------|--------|
| HTTP API | 8080 | Via Nginx (https://market.nyyu.io) |
| gRPC API | 50051 | Via Nginx (market.nyyu.io:443) |
| ClickHouse | 9000 | Internal only (127.0.0.1) |
| Redis | 6379 | Internal only (127.0.0.1) |

---

## API Endpoints

### HTTP API

**Health Check:**
```bash
curl https://market.nyyu.io/health
```

**Get Stats:**
```bash
curl https://market.nyyu.io/api/v1/stats
```

**Get Latest Candle:**
```bash
curl https://market.nyyu.io/api/v1/candles/latest?symbol=BTCUSDT&interval=1m&source=binance
```

**Get Aggregated Candle:**
```bash
curl https://market.nyyu.io/api/v1/candles/latest?symbol=BTCUSDT&interval=1m&source=aggregated
```

### gRPC API

Use **Postman** or **grpcurl**:

**Server:** market.nyyu.io:443
**Proto file:** `proto/market.proto`
**Enable TLS:** Yes

---

## Management

### View Logs

```bash
ssh -i ~/Downloads/Azure-NYYU.pem azureuser@market.nyyu.io
cd /opt/nyyu-market
docker compose logs -f
```

### Check Status

```bash
ssh -i ~/Downloads/Azure-NYYU.pem azureuser@market.nyyu.io
cd /opt/nyyu-market
docker compose ps
```

### Restart Service

```bash
ssh -i ~/Downloads/Azure-NYYU.pem azureuser@market.nyyu.io
cd /opt/nyyu-market
docker compose restart
```

### Update Deployment

Just run the deployment script again:

```bash
./push-to-azure.sh
```

## Configuration

### Environment Variables

See `.env.example` for all available configuration options.

### Database Setup

**ClickHouse** (Separate instance for nyyu-market):
```bash
# Run ClickHouse migrations to create database and tables
go run cmd/clickhouse-migrate/main.go
```
This creates:
- Database: `trade` (separate ClickHouse instance from main system)
- Table: `candles` (for OHLCV data from 6 exchanges)

**Note**: While the database is named "trade", this is a **completely separate ClickHouse instance** dedicated to nyyu-market. It does not share any data with the main trade system.

**Redis**:
- Uses localhost:6379 by default
- Configure different instance via `REDIS_HOST` and `REDIS_PORT` if needed
- Used for caching and pub/sub only (no persistent data)

## Development

### Project Structure

```
nyyu-market/
├── cmd/
│   ├── server/          # Main application entry point
│   └── migrate/         # Database migration tool
├── internal/
│   ├── grpc/            # gRPC server and handlers
│   ├── http/            # HTTP/REST server
│   ├── services/        # Business logic
│   │   ├── candle/
│   │   ├── price/
│   │   └── markprice/
│   ├── models/          # Data models
│   ├── repository/      # Database operations
│   ├── cache/           # Redis caching
│   ├── pubsub/          # Redis pub/sub
│   └── config/          # Configuration management
├── proto/               # Protocol buffer definitions
├── migrations/          # Database migrations
├── docker/              # Docker configurations
├── go.mod
├── go.sum
├── Dockerfile
├── docker-compose.yml
└── README.md
```

### Building

```bash
# Build binary
go build -o nyyu-market cmd/server/main.go

# Run tests
go test ./...

# Generate protobuf
protoc --go_out=. --go-grpc_out=. proto/*.proto
```

## Integration with Main Trade System

### Data Flow

Nyyu-market is a **completely separate service** with its own databases. The main trade system can integrate via:

1. **gRPC API**: High-performance RPC calls for querying data
   - `GetCandles()` - Retrieve historical candle data
   - `GetPrice()` - Get current price for a symbol
   - `GetMarkPrice()` - Get mark price for futures
   - `SubscribeCandles()` - Real-time candle streaming

2. **Redis Pub/Sub**: Real-time price updates
   - Main system subscribes to `nyyu:market:price:{symbol}` channels
   - Mark prices: `nyyu:market:markprice:{symbol}`
   - Ticker data: `nyyu:market:ticker:all`

### Why Separate Databases?

- **Independent scaling**: Scale candle/price service separately from trading engine
- **No conflicts**: No table locking or query interference with main system
- **Dedicated optimization**: ClickHouse tuned specifically for OHLCV queries
- **Clean separation**: Easy to deploy, update, and maintain independently

### Migration from Main System

To migrate from embedded candle/price services to nyyu-market:

1. Deploy nyyu-market service with its own databases
2. Update main system to use gRPC client for candle queries
3. Subscribe to Redis pub/sub channels for real-time updates
4. Disable old candle/price workers in main system
5. Both systems run independently with no shared databases

Example Go client:

```go
// Connect to nyyu-market gRPC
conn, err := grpc.Dial("localhost:50051", grpc.WithInsecure())
client := pb.NewMarketServiceClient(conn)

// Get candles
resp, err := client.GetCandles(ctx, &pb.GetCandlesRequest{
    Symbol: "BTCUSDT",
    Interval: "1m",
    Limit: 100,
})

// Subscribe to real-time updates
stream, err := client.SubscribeCandles(ctx, &pb.SubscribeCandlesRequest{
    Symbol: "BTCUSDT",
    Interval: "1m",
})
for {
    update, err := stream.Recv()
    // Handle candle update
}
```

## Performance Tuning

### ClickHouse Optimization

- Uses `ReplacingMergeTree` for automatic deduplication
- No `FINAL` clause needed (sub-5ms queries)
- Indexed by symbol, interval, and time

### Redis Caching Strategy

- **Price Cache**: 60s TTL, hash storage
- **Mark Price Cache**: 3s TTL, fast updates
- **Candle Cache**: 5s TTL for latest candles

### gRPC Connection Pooling

Configure client connection pooling:

```go
grpc.WithDefaultCallOptions(
    grpc.MaxCallRecvMsgSize(10 * 1024 * 1024),
    grpc.MaxCallSendMsgSize(10 * 1024 * 1024),
)
```

## Monitoring

### Metrics

- gRPC request latency
- Cache hit/miss rates
- Exchange WebSocket health
- Database query performance
- Message queue depth

### Logging

Structured logging with configurable levels:

```bash
LOG_LEVEL=info     # debug, info, warn, error
LOG_FORMAT=json    # json, text
```

## Troubleshooting

### Common Issues

**1. ClickHouse connection failed**
- Verify ClickHouse is running
- Check `CLICKHOUSE_HOST` and `CLICKHOUSE_PORT`
- Ensure `candles` table exists

**2. High memory usage**
- Reduce `MAX_CANDLES_LIMIT`
- Adjust cache TTLs
- Check for WebSocket connection leaks

**3. Slow queries**
- Check ClickHouse indexes
- Verify cache is working
- Monitor Redis connection pool

## License

Proprietary - Part of the Trade platform

## Support

For issues or questions, contact the development team.
