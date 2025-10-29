package grpc

import (
	"context"
	"time"

	"nyyu-market/internal/models"
	pb "nyyu-market/proto/marketpb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GetCandles retrieves historical candles
func (s *Server) GetCandles(ctx context.Context, req *pb.GetCandlesRequest) (*pb.GetCandlesResponse, error) {
	// Validate request
	if req.Symbol == "" {
		return nil, status.Error(codes.InvalidArgument, "symbol is required")
	}
	if req.Interval == "" {
		return nil, status.Error(codes.InvalidArgument, "interval is required")
	}

	// Parse times
	var startTime, endTime time.Time
	if req.StartTime != nil {
		startTime = time.UnixMilli(*req.StartTime)
	}
	if req.EndTime != nil {
		endTime = time.UnixMilli(*req.EndTime)
	}

	// Get limit
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = s.config.Service.DefaultCandlesLimit
	}

	// Get source and contract type
	source := req.GetSource()
	contractType := req.GetContractType()
	if contractType == "" {
		contractType = "spot"
	}

	// Fetch candles
	candles, err := s.candleSvc.GetCandles(ctx, req.Symbol, req.Interval, source, contractType, startTime, endTime, limit)
	if err != nil {
		s.logger.WithError(err).Error("Failed to get candles")
		return nil, toGRPCError(err)
	}

	// Convert to protobuf
	pbCandles := make([]*pb.Candle, len(candles))
	for i, candle := range candles {
		pbCandles[i] = candleToProto(&candle)
	}

	return &pb.GetCandlesResponse{
		Candles: pbCandles,
	}, nil
}

// GetLatestCandle retrieves the most recent candle
func (s *Server) GetLatestCandle(ctx context.Context, req *pb.GetLatestCandleRequest) (*pb.Candle, error) {
	// Validate request
	if req.Symbol == "" {
		return nil, status.Error(codes.InvalidArgument, "symbol is required")
	}
	if req.Interval == "" {
		return nil, status.Error(codes.InvalidArgument, "interval is required")
	}

	// Get source and contract type
	source := req.GetSource()
	contractType := req.GetContractType()
	if contractType == "" {
		contractType = "spot"
	}

	// Fetch latest candle
	candle, err := s.candleSvc.GetLatestCandle(ctx, req.Symbol, req.Interval, source, contractType)
	if err != nil {
		s.logger.WithError(err).Error("Failed to get latest candle")
		return nil, toGRPCError(err)
	}

	if candle == nil {
		return nil, status.Error(codes.NotFound, "candle not found")
	}

	return candleToProto(candle), nil
}

// SubscribeCandles streams real-time candle updates
func (s *Server) SubscribeCandles(req *pb.SubscribeCandlesRequest, stream pb.MarketService_SubscribeCandlesServer) error {
	// Validate request
	if req.Symbol == "" {
		return status.Error(codes.InvalidArgument, "symbol is required")
	}
	if req.Interval == "" {
		return status.Error(codes.InvalidArgument, "interval is required")
	}

	s.logger.Infof("Client subscribed to candles: %s %s", req.Symbol, req.Interval)

	// TODO: Implement Redis pub/sub subscription
	// For now, we'll poll the latest candle every 1 second
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	source := req.GetSource()
	contractType := "spot"

	var lastCandle *models.Candle

	for {
		select {
		case <-stream.Context().Done():
			s.logger.Infof("Client unsubscribed from candles: %s %s", req.Symbol, req.Interval)
			return nil

		case <-ticker.C:
			// Get latest candle
			candle, err := s.candleSvc.GetLatestCandle(stream.Context(), req.Symbol, req.Interval, source, contractType)
			if err != nil {
				s.logger.WithError(err).Warn("Failed to get latest candle for subscription")
				continue
			}

			// Skip if no change
			if lastCandle != nil && candle.OpenTime.Equal(lastCandle.OpenTime) && candle.Close.Equal(lastCandle.Close) {
				continue
			}

			lastCandle = candle

			// Send update
			update := &pb.CandleUpdate{
				Candle:    candleToProto(candle),
				Timestamp: timestamppb.Now(),
			}

			if err := stream.Send(update); err != nil {
				s.logger.WithError(err).Error("Failed to send candle update")
				return err
			}
		}
	}
}

// Helper function to convert model to protobuf
func candleToProto(candle *models.Candle) *pb.Candle {
	return &pb.Candle{
		Symbol:       candle.Symbol,
		Interval:     candle.Interval,
		OpenTime:     candle.OpenTime.UnixMilli(),
		CloseTime:    candle.CloseTime.UnixMilli(),
		Open:         candle.Open.String(),
		High:         candle.High.String(),
		Low:          candle.Low.String(),
		Close:        candle.Close.String(),
		Volume:       candle.Volume.String(),
		QuoteVolume:  candle.QuoteVolume.String(),
		TradeCount:   int32(candle.TradeCount),
		IsClosed:     candle.IsClosed,
		Source:       candle.Source,
		ContractType: candle.ContractType,
	}
}
