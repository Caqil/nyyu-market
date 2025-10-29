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

// GetPrice retrieves current price for a symbol
func (s *Server) GetPrice(ctx context.Context, req *pb.GetPriceRequest) (*pb.Price, error) {
	// Validate request
	if req.Symbol == "" {
		return nil, status.Error(codes.InvalidArgument, "symbol is required")
	}

	// TODO: Implement price service
	// For now, return a mock price calculated from latest candle
	candle, err := s.candleSvc.GetLatestCandle(ctx, req.Symbol, "1m", "", "spot")
	if err != nil {
		return nil, toGRPCError(err)
	}

	if candle == nil {
		return nil, status.Error(codes.NotFound, "price not found")
	}

	// Create price from candle
	price := &models.Price{
		Symbol:    req.Symbol,
		LastPrice: candle.Close,
		UpdatedAt: candle.CloseTime,
	}

	return priceToProto(price), nil
}

// GetPrices retrieves prices for multiple symbols
func (s *Server) GetPrices(ctx context.Context, req *pb.GetPricesRequest) (*pb.GetPricesResponse, error) {
	// TODO: Implement batch price retrieval
	// For now, return empty response
	return &pb.GetPricesResponse{
		Prices: []*pb.Price{},
	}, nil
}

// SubscribePrices streams real-time price updates
func (s *Server) SubscribePrices(req *pb.SubscribePricesRequest, stream pb.MarketService_SubscribePricesServer) error {
	s.logger.Info("Client subscribed to prices")

	// TODO: Implement Redis pub/sub subscription for prices
	// For now, poll every 1 second
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stream.Context().Done():
			s.logger.Info("Client unsubscribed from prices")
			return nil

		case <-ticker.C:
			// TODO: Send actual price updates
			// For now, just keep connection alive
		}
	}
}

// Helper function to convert model to protobuf
func priceToProto(price *models.Price) *pb.Price {
	return &pb.Price{
		Symbol:               price.Symbol,
		LastPrice:            price.LastPrice.String(),
		PriceChange_24H:      price.PriceChange24h.String(),
		PriceChange_24HPercent: price.PriceChange24P.String(),
		High_24H:             price.High24h.String(),
		Low_24H:              price.Low24h.String(),
		Volume_24H:           price.Volume24h.String(),
		QuoteVolume_24H:      price.QuoteVolume24h.String(),
		UpdatedAt:            timestamppb.New(price.UpdatedAt),
	}
}
