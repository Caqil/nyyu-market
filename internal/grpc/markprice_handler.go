package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"nyyu-market/internal/models"
	pb "nyyu-market/proto/marketpb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GetMarkPrice retrieves mark price for a futures symbol
func (s *Server) GetMarkPrice(ctx context.Context, req *pb.GetMarkPriceRequest) (*pb.MarkPrice, error) {
	// Validate request
	if req.Symbol == "" {
		return nil, status.Error(codes.InvalidArgument, "symbol is required")
	}

	// Get mark price from service
	markPrice, err := s.markPriceSvc.GetMarkPrice(ctx, req.Symbol)
	if err != nil {
		s.logger.WithError(err).Errorf("Failed to get mark price for %s", req.Symbol)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to get mark price: %v", err))
	}

	return markPriceToProto(markPrice), nil
}

// GetMarkPrices retrieves mark prices for multiple symbols
func (s *Server) GetMarkPrices(ctx context.Context, req *pb.GetMarkPricesRequest) (*pb.GetMarkPricesResponse, error) {
	symbols := req.Symbols

	// If no symbols specified, get popular symbols from API
	if len(symbols) == 0 {
		popularSymbols, err := s.symbolFetcher.GetPopularSymbols(ctx, 10)
		if err != nil {
			s.logger.WithError(err).Error("Failed to fetch popular symbols")
			return nil, status.Error(codes.Internal, "failed to fetch symbols from API")
		}
		symbols = popularSymbols
	}

	markPrices := make([]*pb.MarkPrice, 0, len(symbols))

	// Get mark prices concurrently
	type result struct {
		mp  *pb.MarkPrice
		err error
	}
	results := make(chan result, len(symbols))

	for _, symbol := range symbols {
		go func(sym string) {
			mp, err := s.markPriceSvc.GetMarkPrice(ctx, sym)
			if err != nil {
				results <- result{err: err}
				return
			}
			results <- result{mp: markPriceToProto(mp)}
		}(symbol)
	}

	// Collect results
	for i := 0; i < len(symbols); i++ {
		res := <-results
		if res.err == nil && res.mp != nil {
			markPrices = append(markPrices, res.mp)
		}
	}

	return &pb.GetMarkPricesResponse{
		MarkPrices: markPrices,
	}, nil
}

// SubscribeMarkPrices streams real-time mark price updates
func (s *Server) SubscribeMarkPrices(req *pb.SubscribeMarkPricesRequest, stream pb.MarketService_SubscribeMarkPricesServer) error {
	symbols := req.Symbols

	// If no symbols specified, get popular symbols from API
	if len(symbols) == 0 {
		ctx := stream.Context()
		popularSymbols, err := s.symbolFetcher.GetPopularSymbols(ctx, 10)
		if err != nil {
			s.logger.WithError(err).Error("Failed to fetch popular symbols")
			return status.Error(codes.Internal, "failed to fetch symbols from API")
		}
		symbols = popularSymbols
	}

	s.logger.Infof("Client subscribed to mark prices for %d symbols", len(symbols))

	ctx := stream.Context()

	// Subscribe to Redis channels for all requested symbols
	channels := make([]string, len(symbols))
	for i, symbol := range symbols {
		channels[i] = "nyyu:market:markprice:" + symbol
	}

	pubsub := s.redisClient.Subscribe(ctx, channels...)
	defer pubsub.Close()

	// Wait for confirmation that subscription is created
	if _, err := pubsub.Receive(ctx); err != nil {
		s.logger.WithError(err).Error("Failed to subscribe to mark price channels")
		return status.Error(codes.Internal, "failed to subscribe to mark prices")
	}

	// Get initial mark prices for all symbols and send them
	for _, symbol := range symbols {
		go func(sym string) {
			mp, err := s.markPriceSvc.GetMarkPrice(ctx, sym)
			if err == nil && mp != nil {
				update := &pb.MarkPriceUpdate{
					MarkPrice: markPriceToProto(mp),
					Timestamp: timestamppb.Now(),
				}
				if err := stream.Send(update); err != nil {
					s.logger.WithError(err).Debug("Failed to send initial mark price")
				}
			}
		}(symbol)
	}

	// Listen for updates
	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Client unsubscribed from mark prices")
			return nil

		case msg := <-ch:
			if msg == nil {
				continue
			}

			// Unmarshal mark price update
			var markPrice models.MarkPrice
			if err := json.Unmarshal([]byte(msg.Payload), &markPrice); err != nil {
				s.logger.WithError(err).Warn("Failed to unmarshal mark price update")
				continue
			}

			// Send to client
			update := &pb.MarkPriceUpdate{
				MarkPrice: markPriceToProto(&markPrice),
				Timestamp: timestamppb.Now(),
			}
			if err := stream.Send(update); err != nil {
				s.logger.WithError(err).Debug("Failed to send mark price update")
				return err
			}
		}
	}
}

// Helper function to convert model to protobuf
func markPriceToProto(mp *models.MarkPrice) *pb.MarkPrice {
	return &pb.MarkPrice{
		Symbol:               mp.Symbol,
		MarkPrice:            mp.MarkPrice.String(),
		IndexPrice:           mp.IndexPrice.String(),
		LastPrice:            mp.LastPrice.String(),
		FundingBasis:         mp.FundingBasis.String(),
		EstimatedFundingRate: mp.EstimatedFundingRate.String(),
		NextFundingTime:      timestamppb.New(mp.NextFundingTime),
		Timestamp:            timestamppb.New(mp.Timestamp),
	}
}
