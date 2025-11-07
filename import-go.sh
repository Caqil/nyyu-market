#!/bin/bash

# Fast Go-based importer for Binance historical data
# Much faster than the shell script version!

cd "$(dirname "$0")"

echo "================================================"
echo "  Fast Binance Historical Data Importer (Go)"
echo "================================================"
echo ""

# Build if binary doesn't exist
if [ ! -f "bin/importer" ]; then
    echo "Building importer..."
    go build -o bin/importer cmd/importer/main.go
    echo ""
fi

# Run importer with arguments
./bin/importer "$@"
