# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make protobuf-dev

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy all source code
COPY . .

# Debug: Show what files we have
RUN echo "=== Working directory ===" && pwd && \
    echo "=== Listing current dir ===" && ls -la && \
    echo "=== Checking cmd directory ===" && ls -la cmd/ && \
    echo "=== Checking cmd/server ===" && ls -la cmd/server/ && \
    echo "=== Checking for main.go ===" && find . -name "main.go"

# Build the application using module path
RUN CGO_ENABLED=0 GOOS=linux go build -o nyyu-market ./cmd/server

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
