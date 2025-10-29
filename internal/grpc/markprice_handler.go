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

// GetMarkPrice retrieves mark price for a futures symbol
func (s *Server) GetMarkPrice(ctx context.Context, req *pb.GetMarkPriceRequest) (*pb.MarkPrice, error) {
	// Validate request
	if req.Symbol == "" {
		return nil, status.Error(codes.InvalidArgument, "symbol is required")
	}

	// TODO: Implement mark price service
	// For now, return mock data
	return &pb.MarkPrice{
		Symbol:               req.Symbol,
		MarkPrice:            "0",
		IndexPrice:           "0",
		LastPrice:            "0",
		FundingBasis:         "0",
		EstimatedFundingRate: "0",
		NextFundingTime:      timestamppb.Now(),
		Timestamp:            timestamppb.Now(),
	}, status.Error(codes.Unimplemented, "mark price service not yet implemented")
}

// GetMarkPrices retrieves mark prices for multiple symbols
func (s *Server) GetMarkPrices(ctx context.Context, req *pb.GetMarkPricesRequest) (*pb.GetMarkPricesResponse, error) {
	// TODO: Implement batch mark price retrieval
	return &pb.GetMarkPricesResponse{
		MarkPrices: []*pb.MarkPrice{},
	}, status.Error(codes.Unimplemented, "mark prices service not yet implemented")
}

// SubscribeMarkPrices streams real-time mark price updates
func (s *Server) SubscribeMarkPrices(req *pb.SubscribeMarkPricesRequest, stream pb.MarketService_SubscribeMarkPricesServer) error {
	s.logger.Info("Client subscribed to mark prices")

	// TODO: Implement Redis pub/sub subscription for mark prices
	// For now, just keep connection alive
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stream.Context().Done():
			s.logger.Info("Client unsubscribed from mark prices")
			return nil

		case <-ticker.C:
			// TODO: Send actual mark price updates
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
