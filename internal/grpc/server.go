package grpc

import (
	"context"
	"fmt"
	"net"
	"time"

	"nyyu-market/internal/config"
	candleService "nyyu-market/internal/services/candle"
	pb "nyyu-market/proto/marketpb"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	pb.UnimplementedMarketServiceServer
	config      *config.Config
	candleSvc   *candleService.Service
	logger      *logrus.Logger
	grpcServer  *grpc.Server
	startTime   time.Time
}

func NewServer(
	cfg *config.Config,
	candleSvc *candleService.Service,
	logger *logrus.Logger,
) *Server {
	return &Server{
		config:     cfg,
		candleSvc:  candleSvc,
		logger:     logger,
		startTime:  time.Now(),
	}
}

func (s *Server) Start() error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", s.config.Server.GRPCPort))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	// Create gRPC server with options
	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(100 * 1024 * 1024), // 100MB
		grpc.MaxSendMsgSize(100 * 1024 * 1024), // 100MB
		grpc.UnaryInterceptor(s.unaryInterceptor),
		grpc.StreamInterceptor(s.streamInterceptor),
	}

	s.grpcServer = grpc.NewServer(opts...)
	pb.RegisterMarketServiceServer(s.grpcServer, s)

	s.logger.Infof("gRPC server listening on :%d", s.config.Server.GRPCPort)

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
