package grpc

import (
	"context"
	"fmt"
	"net"
	"time"

	"nyyu-market/internal/config"
	candleService "nyyu-market/internal/services/candle"
	markpriceService "nyyu-market/internal/services/markprice"
	"nyyu-market/internal/services/symbols"
	pb "nyyu-market/proto/marketpb"

	"github.com/go-redis/redis/v8"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

type Server struct {
	pb.UnimplementedMarketServiceServer
	config        *config.Config
	candleSvc     *candleService.Service
	markPriceSvc  *markpriceService.Service
	symbolFetcher *symbols.SymbolFetcher
	redisClient   *redis.Client
	logger        *logrus.Logger
	grpcServer    *grpc.Server
	startTime     time.Time
}

func NewServer(
	cfg *config.Config,
	candleSvc *candleService.Service,
	markPriceSvc *markpriceService.Service,
	symbolFetcher *symbols.SymbolFetcher,
	redisClient *redis.Client,
	logger *logrus.Logger,
) *Server {
	return &Server{
		config:        cfg,
		candleSvc:     candleSvc,
		markPriceSvc:  markPriceSvc,
		symbolFetcher: symbolFetcher,
		redisClient:   redisClient,
		logger:        logger,
		startTime:     time.Now(),
	}
}

func (s *Server) Start() error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", s.config.Server.GRPCPort))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	// ⚡ PRODUCTION TUNING: Optimized for high concurrency (100k+ users)
	opts := []grpc.ServerOption{
		// Message size limits (already set)
		grpc.MaxRecvMsgSize(100 * 1024 * 1024), // 100MB
		grpc.MaxSendMsgSize(100 * 1024 * 1024), // 100MB

		// ⚡ NEW: Concurrent streams limit
		// Allows 50,000 concurrent streaming connections per server
		// For 100k users, deploy 2-4 servers (25k-50k streams each)
		grpc.MaxConcurrentStreams(50000),

		// ⚡ NEW: Keepalive parameters
		// Prevents connection timeouts and detects dead clients
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionAge:      30 * time.Minute, // Force reconnect after 30min (load balancing)
			MaxConnectionAgeGrace: 5 * time.Minute,  // Allow 5min for graceful close
			Time:                  20 * time.Second, // Send keepalive ping every 20s
			Timeout:               10 * time.Second, // Wait 10s for ping response
		}),

		// ⚡ NEW: Keepalive enforcement
		// Protect against aggressive clients
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             5 * time.Second, // Min time between client pings
			PermitWithoutStream: true,            // Allow pings without active streams
		}),

		// ⚡ NEW: Write buffer size
		// Larger buffer = better throughput for streaming
		grpc.WriteBufferSize(256 * 1024), // 256KB (default 32KB)

		// ⚡ NEW: Read buffer size
		grpc.ReadBufferSize(256 * 1024), // 256KB (default 32KB)

		// Interceptors for logging and error handling
		grpc.UnaryInterceptor(s.unaryInterceptor),
		grpc.StreamInterceptor(s.streamInterceptor),
	}

	s.grpcServer = grpc.NewServer(opts...)
	pb.RegisterMarketServiceServer(s.grpcServer, s)

	s.logger.Infof("🚀 gRPC server listening on :%d (MaxConcurrentStreams: 50000)", s.config.Server.GRPCPort)

	return s.grpcServer.Serve(lis)
}

func (s *Server) Stop() {
	if s.grpcServer != nil {
		s.logger.Info("Stopping gRPC server...")
		s.grpcServer.GracefulStop()
	}
}

// Interceptors for logging and error handling
func (s *Server) unaryInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	start := time.Now()

	// Call handler
	resp, err := handler(ctx, req)

	// Log
	duration := time.Since(start)
	s.logger.WithFields(logrus.Fields{
		"method":   info.FullMethod,
		"duration": duration.Milliseconds(),
		"error":    err != nil,
	}).Debug("gRPC unary call")

	return resp, err
}

func (s *Server) streamInterceptor(
	srv interface{},
	ss grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	start := time.Now()

	// Call handler
	err := handler(srv, ss)

	// Log
	duration := time.Since(start)
	s.logger.WithFields(logrus.Fields{
		"method":   info.FullMethod,
		"duration": duration.Milliseconds(),
		"error":    err != nil,
	}).Debug("gRPC stream call")

	return err
}

// Helper to convert errors to gRPC status
func toGRPCError(err error) error {
	if err == nil {
		return nil
	}

	// Map common errors to gRPC codes
	switch err.Error() {
	case "not found", "record not found":
		return status.Error(codes.NotFound, err.Error())
	case "invalid argument", "invalid input":
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
