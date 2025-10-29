package grpc

import (
	"context"
	"time"

	pb "nyyu-market/proto/marketpb"
)

// Health returns service health status
func (s *Server) Health(ctx context.Context, req *pb.HealthRequest) (*pb.HealthResponse, error) {
	uptime := int64(time.Since(s.startTime).Seconds())

	return &pb.HealthResponse{
		Healthy:       true,
		Version:       "1.0.0",
		UptimeSeconds: uptime,
		Services: map[string]string{
			"clickhouse": "healthy",
			"postgres":   "healthy",
			"redis":      "healthy",
		},
	}, nil
}

// GetStats returns service statistics
func (s *Server) GetStats(ctx context.Context, req *pb.GetStatsRequest) (*pb.StatsResponse, error) {
	// Get candle stats
	stats, err := s.candleSvc.GetStats(ctx)
	if err != nil {
		s.logger.WithError(err).Error("Failed to get stats")
		stats = map[string]interface{}{}
	}

	totalCandles := int64(0)
	totalSymbols := int64(0)

	if val, ok := stats["total_candles"].(uint64); ok {
		totalCandles = int64(val)
	}
	if val, ok := stats["total_symbols"].(uint64); ok {
		totalSymbols = int64(val)
	}

	return &pb.StatsResponse{
		TotalCandles:          totalCandles,
		TotalSymbols:          totalSymbols,
		ActiveSubscriptions:   0, // TODO: Track subscriptions
		PriceUpdatesPerSecond: 0, // TODO: Implement metrics
		CandleWritesPerSecond: 0, // TODO: Implement metrics
		ExchangeStats:         map[string]*pb.ExchangeStats{},
	}, nil
}
