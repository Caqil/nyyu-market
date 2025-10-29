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

// GetPrice retrieves current price for a symbol
func (s *Server) GetPrice(ctx context.Context, req *pb.GetPriceRequest) (*pb.Price, error) {
	// Validate request
	if req.Symbol == "" {
		return nil, status.Error(codes.InvalidArgument, "symbol is required")
	}

	// Get price from service
	price, err := s.priceSvc.GetPrice(ctx, req.Symbol)
	if err != nil {
		s.logger.WithError(err).Errorf("Failed to get price for %s", req.Symbol)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to get price: %v", err))
	}

	return priceToProto(price), nil
}

// GetPrices retrieves prices for multiple symbols
func (s *Server) GetPrices(ctx context.Context, req *pb.GetPricesRequest) (*pb.GetPricesResponse, error) {
	symbols := req.Symbols

	// If no symbols specified, get popular symbols from API
	if len(symbols) == 0 {
		popularSymbols, err := s.symbolFetcher.GetPopularSymbols(ctx, 10)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to fetch popular symbols, using defaults")
			// Fallback to defaults
			symbols = []string{
				"BTCUSDT", "ETHUSDT", "BNBUSDT", "SOLUSDT", "XRPUSDT",
				"ADAUSDT", "DOGEUSDT", "AVAXUSDT", "DOTUSDT", "MATICUSDT",
			}
		} else {
			symbols = popularSymbols
		}
	}

	prices := make([]*pb.Price, 0, len(symbols))

	// Get prices concurrently
	type result struct {
		price *pb.Price
		err   error
	}
	results := make(chan result, len(symbols))

	for _, symbol := range symbols {
		go func(sym string) {
			p, err := s.priceSvc.GetPrice(ctx, sym)
			if err != nil {
				results <- result{err: err}
				return
			}
			results <- result{price: priceToProto(p)}
		}(symbol)
	}

	// Collect results
	for i := 0; i < len(symbols); i++ {
		res := <-results
		if res.err == nil && res.price != nil {
			prices = append(prices, res.price)
		}
	}

	return &pb.GetPricesResponse{
		Prices: prices,
	}, nil
}

// SubscribePrices streams real-time price updates
func (s *Server) SubscribePrices(req *pb.SubscribePricesRequest, stream pb.MarketService_SubscribePricesServer) error {
	symbols := req.Symbols

	// If no symbols specified, get popular symbols from API
	if len(symbols) == 0 {
		ctx := stream.Context()
		popularSymbols, err := s.symbolFetcher.GetPopularSymbols(ctx, 10)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to fetch popular symbols, using defaults")
			// Fallback to defaults
			symbols = []string{
				"BTCUSDT", "ETHUSDT", "BNBUSDT", "SOLUSDT", "XRPUSDT",
				"ADAUSDT", "DOGEUSDT", "AVAXUSDT", "DOTUSDT", "MATICUSDT",
			}
		} else {
			symbols = popularSymbols
		}
	}

	s.logger.Infof("Client subscribed to prices for %d symbols", len(symbols))

	ctx := stream.Context()

	// Subscribe to Redis channels for all requested symbols
	channels := make([]string, len(symbols))
	for i, symbol := range symbols {
		channels[i] = "nyyu:market:price:" + symbol
	}

	pubsub := s.redisClient.Subscribe(ctx, channels...)
	defer pubsub.Close()

	// Wait for confirmation that subscription is created
	if _, err := pubsub.Receive(ctx); err != nil {
		s.logger.WithError(err).Error("Failed to subscribe to price channels")
		return status.Error(codes.Internal, "failed to subscribe to prices")
	}

	// Get initial prices for all symbols and send them
	for _, symbol := range symbols {
		go func(sym string) {
			p, err := s.priceSvc.GetPrice(ctx, sym)
			if err == nil && p != nil {
				update := &pb.PriceUpdate{
					Price:     priceToProto(p),
					Timestamp: timestamppb.Now(),
				}
				if err := stream.Send(update); err != nil {
					s.logger.WithError(err).Debug("Failed to send initial price")
				}
			}
		}(symbol)
	}

	// Listen for updates
	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Client unsubscribed from prices")
			return nil

		case msg := <-ch:
			if msg == nil {
				continue
			}

			// Unmarshal price update
			var price models.Price
			if err := json.Unmarshal([]byte(msg.Payload), &price); err != nil {
				s.logger.WithError(err).Warn("Failed to unmarshal price update")
				continue
			}

			// Send to client
			update := &pb.PriceUpdate{
				Price:     priceToProto(&price),
				Timestamp: timestamppb.Now(),
			}
			if err := stream.Send(update); err != nil {
				s.logger.WithError(err).Debug("Failed to send price update")
				return err
			}
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
