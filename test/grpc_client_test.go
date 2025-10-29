package test

import (
	"context"
	"testing"
	"time"

	pb "nyyu-market/proto/marketpb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestGRPCServer(t *testing.T) {
	// Connect to gRPC server
	conn, err := grpc.Dial("localhost:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	client := pb.NewMarketServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Run("Health Check", func(t *testing.T) {
		resp, err := client.Health(ctx, &pb.HealthRequest{})
		if err != nil {
			t.Fatalf("Health check failed: %v", err)
		}

		if !resp.Healthy {
			t.Error("Service is not healthy")
		}

		t.Logf("Health: %+v", resp)
	})

	t.Run("Get Stats", func(t *testing.T) {
		resp, err := client.GetStats(ctx, &pb.GetStatsRequest{})
		if err != nil {
			t.Fatalf("Get stats failed: %v", err)
		}

		t.Logf("Stats: Total Candles=%d, Total Symbols=%d", resp.TotalCandles, resp.TotalSymbols)
	})

	t.Run("Get Candles", func(t *testing.T) {
		resp, err := client.GetCandles(ctx, &pb.GetCandlesRequest{
			Symbol:   "BTCUSDT",
			Interval: "1m",
			Limit:    func() *int32 { l := int32(10); return &l }(),
		})
		if err != nil {
			t.Logf("Get candles failed (expected if no data): %v", err)
			return
		}

		t.Logf("Retrieved %d candles", len(resp.Candles))
		for i, candle := range resp.Candles {
			if i >= 3 {
				break
			}
			t.Logf("  Candle: %s %s O=%s H=%s L=%s C=%s V=%s",
				candle.Symbol, candle.Interval,
				candle.Open, candle.High, candle.Low, candle.Close, candle.Volume)
		}
	})

	t.Run("Get Latest Candle", func(t *testing.T) {
		candle, err := client.GetLatestCandle(ctx, &pb.GetLatestCandleRequest{
			Symbol:   "BTCUSDT",
			Interval: "1m",
		})
		if err != nil {
			t.Logf("Get latest candle failed (expected if no data): %v", err)
			return
		}

		t.Logf("Latest Candle: %s %s Close=%s Time=%d",
			candle.Symbol, candle.Interval, candle.Close, candle.OpenTime)
	})
}
