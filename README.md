# Nyyu Market Service

High-performance candle, kline, and price data service optimized for ultra-low latency market data delivery.

## Features

- **Multi-Exchange WebSocket Aggregation**: Real-time candle data from 6+ exchanges
- **ClickHouse Storage**: Sub-5ms query performance for OHLCV data
- **gRPC API**: High-performance RPC for queries
- **Redis Pub/Sub**: Real-time streaming to clients
- **Mark Price Calculation**: Binance-style mark price for futures
- **Optimized Caching**: Multi-layer caching strategy
- **Circuit Breaker**: Automatic exchange reconnection

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

## Quick Start

### Prerequisites

- Go 1.22+
- ClickHouse (separate database for nyyu-market)
- Redis 6+

### Installation

```bash
# Clone or navigate to the project
cd nyyu-market

# Copy environment file
cp .env.example .env

# Edit configuration
nano .env

# Install dependencies
go mod download

# Run migrations
go run cmd/migrate/main.go up

# Start the server
go run cmd/server/main.go
```

### Docker Deployment

```bash
# Build and start all services
docker-compose up -d

# View logs
docker-compose logs -f

# Stop services
docker-compose down
```

## API Documentation

### gRPC Endpoints

#### GetCandles
Get historical candles for a symbol and interval.

```protobuf
rpc GetCandles(GetCandlesRequest) returns (GetCandlesResponse);
```

**Request:**
```json
{
  "symbol": "BTCUSDT",
  "interval": "1m",
  "limit": 100,
  "start_time": 1234567890,
  "end_time": 1234567999
}
```

**Response:**
```json
{
  "candles": [
    {
      "symbol": "BTCUSDT",
      "interval": "1m",
      "open_time": 1234567890000,
      "close_time": 1234567899999,
      "open": "50000.00",
      "high": "50100.00",
      "low": "49900.00",
      "close": "50050.00",
      "volume": "123.45",
      "quote_volume": "6172500.00",
      "trade_count": 1234,
      "is_closed": true
    }
  ]
}
```

#### GetPrice
Get current price for a symbol.

```protobuf
rpc GetPrice(GetPriceRequest) returns (GetPriceResponse);
```

#### GetMarkPrice
Get mark price for futures symbol.

```protobuf
rpc GetMarkPrice(GetMarkPriceRequest) returns (GetMarkPriceResponse);
```

#### SubscribeCandles
Real-time candle updates via streaming.

```protobuf
rpc SubscribeCandles(SubscribeCandlesRequest) returns (stream CandleUpdate);
```

### REST Endpoints

#### Health Check
```
GET /health
```

#### Get Candles
```
GET /api/v1/candles/:symbol?interval=1m&limit=100
```

#### Get Price
```
GET /api/v1/price/:symbol
```

#### Get Mark Price
```
GET /api/v1/mark-price/:symbol
```

### Redis Pub/Sub Channels

Subscribe to real-time updates:

```
nyyu:market:candle:{symbol}:{interval}
nyyu:market:price:{symbol}
nyyu:market:markprice:{symbol}
nyyu:market:ticker:all
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
