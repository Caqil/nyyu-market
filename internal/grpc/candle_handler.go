package grpc

import (
	"context"
	"encoding/json"
	"fmt"
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

	// ⚡ ON-DEMAND SUBSCRIPTION: Subscribe to exchange feeds when user requests candles
	// This creates a websocket subscription only if it doesn't exist yet
	// Multiple users requesting the same symbol@interval share ONE subscription
	if err := s.candleSvc.SubscribeToInterval(ctx, req.Symbol, req.Interval); err != nil {
		s.logger.WithError(err).Warn("Failed to subscribe to interval (non-fatal)")
		// Don't fail the request, just log the warning
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

	// ⚡ ON-DEMAND SUBSCRIPTION: Subscribe to exchange feeds when user requests candles
	if err := s.candleSvc.SubscribeToInterval(ctx, req.Symbol, req.Interval); err != nil {
		s.logger.WithError(err).Warn("Failed to subscribe to interval (non-fatal)")
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

	ctx := stream.Context()
	source := req.GetSource()
	contractType := "spot"

	// ⚡ ON-DEMAND SUBSCRIPTION: Create websocket subscription to exchanges
	// This increments the reference count for this symbol@interval
	if err := s.candleSvc.SubscribeToInterval(ctx, req.Symbol, req.Interval); err != nil {
		s.logger.WithError(err).Warn("Failed to subscribe to interval (non-fatal)")
	}

	// ⚡ CLEANUP: Unsubscribe when client disconnects
	// This decrements the reference count, removing the websocket subscription if it reaches 0
	defer func() {
		if err := s.candleSvc.UnsubscribeFromInterval(context.Background(), req.Symbol, req.Interval); err != nil {
			s.logger.WithError(err).Warn("Failed to unsubscribe from interval")
		}
	}()

	// Subscribe to Redis pub/sub channel for real-time updates
	channel := fmt.Sprintf("nyyu:market:candle:%s:%s", req.Symbol, req.Interval)
	pubsub := s.redisClient.Subscribe(ctx, channel)
	defer pubsub.Close()

	// Wait for confirmation that subscription is created
	if _, err := pubsub.Receive(ctx); err != nil {
		s.logger.WithError(err).Error("Failed to subscribe to candle channel")
		return status.Error(codes.Internal, "failed to subscribe to candles")
	}

	// Get initial candle and send it
	initialCandle, err := s.candleSvc.GetLatestCandle(ctx, req.Symbol, req.Interval, source, contractType)
	if err == nil && initialCandle != nil {
		update := &pb.CandleUpdate{
			Candle:    candleToProto(initialCandle),
			Timestamp: timestamppb.Now(),
		}
		if err := stream.Send(update); err != nil {
			s.logger.WithError(err).Error("Failed to send initial candle")
			return err
		}
	}

	// Listen for real-time updates from Redis pub/sub
	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			s.logger.Infof("Client unsubscribed from candles: %s %s", req.Symbol, req.Interval)
			return nil

		case msg := <-ch:
			if msg == nil {
				continue
			}

			// Parse candle from Redis message
			var candle models.Candle
			if err := json.Unmarshal([]byte(msg.Payload), &candle); err != nil {
				s.logger.WithError(err).Error("Failed to parse candle from Redis")
				continue
			}

			// Send update
			update := &pb.CandleUpdate{
				Candle:    candleToProto(&candle),
				Timestamp: timestamppb.Now(),
			}

			if err := stream.Send(update); err != nil {
				s.logger.WithError(err).Error("Failed to send candle update")
				return err
			}
		}
	}
}

// ========================================================================
// PRICE TICKER HANDLERS (using candle data)
// ========================================================================

// GetPrice retrieves price ticker from candle data
func (s *Server) GetPrice(ctx context.Context, req *pb.GetPriceRequest) (*pb.Price, error) {
	if req.Symbol == "" {
		return nil, status.Error(codes.InvalidArgument, "symbol is required")
	}

	// Subscribe to 1m candles to ensure we get real-time data
	if err := s.candleSvc.SubscribeToInterval(ctx, req.Symbol, "1m"); err != nil {
		s.logger.WithError(err).Warn("Failed to subscribe to 1m candles (non-fatal)")
	}

	// Get price ticker from candle service
	price, err := s.candleSvc.GetPriceTicker(ctx, req.Symbol)
	if err != nil {
		s.logger.WithError(err).Error("Failed to get price ticker")
		return nil, toGRPCError(err)
	}

	return priceToProto(price), nil
}

// GetPrices retrieves price tickers for multiple symbols
func (s *Server) GetPrices(ctx context.Context, req *pb.GetPricesRequest) (*pb.GetPricesResponse, error) {
	// Subscribe to 1m candles for all requested symbols
	for _, symbol := range req.Symbols {
		if err := s.candleSvc.SubscribeToInterval(ctx, symbol, "1m"); err != nil {
			s.logger.WithError(err).Warnf("Failed to subscribe to 1m candles for %s", symbol)
		}
	}

	// Get price tickers from candle service
	prices, err := s.candleSvc.GetPriceTickers(ctx, req.Symbols)
	if err != nil {
		s.logger.WithError(err).Error("Failed to get price tickers")
		return nil, toGRPCError(err)
	}

	// Convert to protobuf
	pbPrices := make([]*pb.Price, len(prices))
	for i, price := range prices {
		pbPrices[i] = priceToProto(price)
	}

	return &pb.GetPricesResponse{
		Prices: pbPrices,
	}, nil
}

// SubscribePrices streams real-time price updates from candle updates
func (s *Server) SubscribePrices(req *pb.SubscribePricesRequest, stream pb.MarketService_SubscribePricesServer) error {
	if len(req.Symbols) == 0 {
		return status.Error(codes.InvalidArgument, "at least one symbol is required")
	}

	s.logger.Infof("Client subscribed to prices for %d symbols", len(req.Symbols))

	ctx := stream.Context()

	// ⚡ ON-DEMAND SUBSCRIPTION: Subscribe to 1m candles for all requested symbols
	// This triggers the exchange aggregator to start receiving price data
	for _, symbol := range req.Symbols {
		if err := s.candleSvc.SubscribeToInterval(ctx, symbol, "1m"); err != nil {
			s.logger.WithError(err).Warnf("Failed to subscribe to candles for %s", symbol)
		}
	}

	// ⚡ CLEANUP: Unsubscribe when client disconnects
	defer func() {
		for _, symbol := range req.Symbols {
			if err := s.candleSvc.UnsubscribeFromInterval(context.Background(), symbol, "1m"); err != nil {
				s.logger.WithError(err).Warnf("Failed to unsubscribe from %s", symbol)
			}
		}
	}()

	// Subscribe to Redis pub/sub channels for all symbols (candle updates)
	channels := make([]string, len(req.Symbols))
	for i, symbol := range req.Symbols {
		channels[i] = fmt.Sprintf("nyyu:market:candle:%s:1m", symbol)
	}

	pubsub := s.redisClient.Subscribe(ctx, channels...)
	defer pubsub.Close()

	// Wait for subscription confirmation
	if _, err := pubsub.Receive(ctx); err != nil {
		s.logger.WithError(err).Error("Failed to subscribe to price channels")
		return status.Error(codes.Internal, "failed to subscribe to prices")
	}

	// Send initial prices for all symbols
	for _, symbol := range req.Symbols {
		if price, err := s.candleSvc.GetPriceTicker(ctx, symbol); err == nil && price != nil {
			update := &pb.PriceUpdate{
				Price:     priceToProto(price),
				Timestamp: timestamppb.Now(),
			}
			if err := stream.Send(update); err != nil {
				s.logger.WithError(err).Error("Failed to send initial price")
				return err
			}
		}
	}

	// Listen for real-time candle updates from Redis pub/sub
	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			s.logger.Infof("Client unsubscribed from prices")
			return nil

		case msg := <-ch:
			if msg == nil {
				continue
			}

			// Parse candle from Redis message
			var candle models.Candle
			if err := json.Unmarshal([]byte(msg.Payload), &candle); err != nil {
				s.logger.WithError(err).Error("Failed to parse candle from Redis")
				continue
			}

			// Convert candle to price ticker
			price := s.candleSvc.ConvertCandleToPrice(ctx, &candle)
			if price == nil {
				continue
			}

			// Send price update
			update := &pb.PriceUpdate{
				Price:     priceToProto(price),
				Timestamp: timestamppb.Now(),
			}

			if err := stream.Send(update); err != nil {
				s.logger.WithError(err).Error("Failed to send price update")
				return err
			}
		}
	}
}

// ========================================================================
// HELPER FUNCTIONS
// ========================================================================

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

// Helper function to convert price model to protobuf
func priceToProto(price *models.Price) *pb.Price {
	return &pb.Price{
		Symbol:                price.Symbol,
		LastPrice:             price.LastPrice.String(),
		PriceChange_24H:       price.PriceChange24h.String(),
		PriceChange_24HPercent: price.PriceChange24P.String(),
		High_24H:              price.High24h.String(),
		Low_24H:               price.Low24h.String(),
		Volume_24H:            price.Volume24h.String(),
		QuoteVolume_24H:       price.QuoteVolume24h.String(),
		UpdatedAt:             timestamppb.New(price.UpdatedAt),
	}
}
