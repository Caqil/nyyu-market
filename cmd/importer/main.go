package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"nyyu-market/internal/config"
	"nyyu-market/internal/importer"

	"github.com/sirupsen/logrus"
)

func main() {
	// Command line flags
	symbol := flag.String("symbol", "", "Trading symbol (e.g., BTCUSDT)")
	intervals := flag.String("intervals", "1h,4h,1d", "Comma-separated intervals or 'all' (e.g., 1h,4h,1d)")
	startYear := flag.Int("start-year", 2018, "Start year")
	startMonth := flag.Int("start-month", 1, "Start month")
	endYear := flag.Int("end-year", time.Now().Year(), "End year")
	endMonth := flag.Int("end-month", int(time.Now().Month()), "End month")
	workers := flag.Int("workers", 5, "Number of parallel workers")
	flag.Parse()

	// Validate required flags
	if *symbol == "" {
		fmt.Println("Error: -symbol is required")
		flag.Usage()
		os.Exit(1)
	}

	// Setup logger
	logger := logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	// Load config
	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("Failed to load config: %v", err)
	}

	// Parse intervals
	intervalList := parseIntervals(*intervals)

	// Create importer
	imp, err := importer.New(cfg, logger)
	if err != nil {
		logger.Fatalf("Failed to create importer: %v", err)
	}
	defer imp.Close()

	// Create import job
	job := &importer.ImportJob{
		Symbol:     *symbol,
		Intervals:  intervalList,
		StartYear:  *startYear,
		StartMonth: *startMonth,
		EndYear:    *endYear,
		EndMonth:   *endMonth,
		Workers:    *workers,
	}

	logger.Infof("🚀 Starting import: %s", job.String())
	logger.Infof("📊 Intervals: %v", intervalList)
	logger.Infof("📅 Period: %04d-%02d to %04d-%02d", *startYear, *startMonth, *endYear, *endMonth)
	logger.Infof("⚡ Workers: %d", *workers)

	// Run import
	ctx := context.Background()
	if err := imp.Import(ctx, job); err != nil {
		logger.Fatalf("Import failed: %v", err)
	}

	logger.Info("✅ Import completed successfully!")
}

func parseIntervals(input string) []string {
	if input == "all" {
		return []string{
			"1m", "3m", "5m", "15m", "30m",
			"1h", "2h", "4h", "6h", "8h", "12h",
			"1d", "3d", "1w", "1M",
		}
	}

	intervals := strings.Split(input, ",")
	for i := range intervals {
		intervals[i] = strings.TrimSpace(intervals[i])
	}
	return intervals
}
