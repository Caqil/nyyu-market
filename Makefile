.PHONY: proto build run test clean docker-build docker-up docker-down migrate-up migrate-down

# Generate protobuf code
proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/market.proto

# Build the service
build:
	go build -o bin/nyyu-market cmd/server/main.go

# Run the service
run:
	go run cmd/server/main.go

# Run tests
test:
	go test -v ./...

# Run tests with coverage
test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

# Clean build artifacts
clean:
	rm -rf bin/
	rm -f coverage.out

# Docker commands
docker-build:
	docker build -t nyyu-market:latest .

docker-up:
	docker-compose up -d

docker-down:
	docker-compose down

docker-logs:
	docker-compose logs -f

# Database migrations
migrate-clickhouse:
	go run cmd/clickhouse-migrate/main.go

migrate-postgres:
	go run cmd/migrate/main.go up

migrate-postgres-down:
	go run cmd/migrate/main.go down

# Migrate all databases
migrate-all: migrate-clickhouse migrate-postgres

# Install dependencies
deps:
	go mod download
	go mod tidy

# Format code
fmt:
	go fmt ./...

# Lint code
lint:
	golangci-lint run

# Install tools
install-tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Development hot reload
dev:
	air
