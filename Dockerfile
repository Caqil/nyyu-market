# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make protobuf-dev

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN echo "Downloading Go modules..." && go mod download && echo "Download complete!"

# Copy all source code
COPY . .

# Build the application with progress
RUN echo "Starting build..." && \
    CGO_ENABLED=0 GOOS=linux go build -v -ldflags="-w -s" -o nyyu-market ./cmd/server && \
    echo "Build complete!" && \
    ls -lh nyyu-market

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /root/

# Copy binary from builder
COPY --from=builder /app/nyyu-market .
COPY --from=builder /app/.env.example .env.example

# Expose ports
EXPOSE 50051 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

CMD ["./nyyu-market"]
