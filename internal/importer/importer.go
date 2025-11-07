package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"nyyu-market/internal/config"
	"nyyu-market/internal/models"
	"nyyu-market/internal/repository"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/schollz/progressbar/v3"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

const (
	binanceDataURL = "https://data.binance.vision/data/spot/monthly/klines"
	workDir        = "/tmp/binance_import"
)

type Importer struct {
	repo   *repository.CandleRepository
	logger *logrus.Logger
	conn   clickhouse.Conn
}

type ImportJob struct {
	Symbol     string
	Intervals  []string
	StartYear  int
	StartMonth int
	EndYear    int
	EndMonth   int
	Workers    int
}

func (j *ImportJob) String() string {
	return fmt.Sprintf("%s (%d intervals) from %04d-%02d to %04d-%02d",
		j.Symbol, len(j.Intervals), j.StartYear, j.StartMonth, j.EndYear, j.EndMonth)
}

type monthTask struct {
	Symbol   string
	Interval string
	Year     int
	Month    int
}

type importResult struct {
	Task    monthTask
	Count   int
	Error   error
	Skipped bool
}

func New(cfg *config.Config, logger *logrus.Logger) (*Importer, error) {
	// Connect to ClickHouse
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%d", cfg.ClickHouse.Host, cfg.ClickHouse.Port)},
		Auth: clickhouse.Auth{
			Database: cfg.ClickHouse.Database,
			Username: cfg.ClickHouse.Username,
			Password: cfg.ClickHouse.Password,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to ClickHouse: %w", err)
	}

	// Create repository
	repo := repository.NewCandleRepository(conn, logger)

	// Ensure work directory exists
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create work directory: %w", err)
	}

	return &Importer{
		repo:   repo,
		logger: logger,
		conn:   conn,
	}, nil
}

func (imp *Importer) Close() {
	if imp.conn != nil {
		imp.conn.Close()
	}
}

func (imp *Importer) Import(ctx context.Context, job *ImportJob) error {
	totalTasks := 0
	successCount := 0
	failCount := 0
	skippedCount := 0

	for _, interval := range job.Intervals {
		imp.logger.Infof("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		imp.logger.Infof("📊 Processing interval: %s", interval)
		imp.logger.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		// Generate tasks for this interval
		tasks := imp.generateTasks(job.Symbol, interval, job.StartYear, job.StartMonth, job.EndYear, job.EndMonth)
		totalTasks += len(tasks)

		// Create worker pool
		taskChan := make(chan monthTask, len(tasks))
		resultChan := make(chan importResult, len(tasks))

		// Start workers
		var wg sync.WaitGroup
		for i := 0; i < job.Workers; i++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				for task := range taskChan {
					result := imp.processMonth(ctx, task)
					resultChan <- result
				}
			}(i)
		}

		// Send tasks
		for _, task := range tasks {
			taskChan <- task
		}
		close(taskChan)

		// Create progress bar
		bar := progressbar.NewOptions(len(tasks),
			progressbar.OptionSetDescription(fmt.Sprintf("Importing %s %s", job.Symbol, interval)),
			progressbar.OptionSetWidth(50),
			progressbar.OptionShowCount(),
			progressbar.OptionShowIts(),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "=",
				SaucerHead:    ">",
				SaucerPadding: " ",
				BarStart:      "[",
				BarEnd:        "]",
			}),
		)

		// Collect results
		go func() {
			wg.Wait()
			close(resultChan)
		}()

		intervalSuccess := 0
		intervalFail := 0
		intervalSkipped := 0

		for result := range resultChan {
			bar.Add(1)
			if result.Error != nil {
				intervalFail++
				imp.logger.Warnf("  ❌ %04d-%02d: %v", result.Task.Year, result.Task.Month, result.Error)
			} else if result.Skipped {
				intervalSkipped++
			} else {
				intervalSuccess++
				imp.logger.Debugf("  ✅ %04d-%02d: %d candles", result.Task.Year, result.Task.Month, result.Count)
			}
		}

		successCount += intervalSuccess
		failCount += intervalFail
		skippedCount += intervalSkipped

		imp.logger.Infof("\n✅ %s: %d succeeded, %d skipped, %d failed\n",
			interval, intervalSuccess, intervalSkipped, intervalFail)
	}

	// Final summary
	imp.logger.Info("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	imp.logger.Info("📈 Import Summary")
	imp.logger.Info("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	imp.logger.Infof("Total tasks:    %d", totalTasks)
	imp.logger.Infof("✅ Successful:  %d", successCount)
	imp.logger.Infof("⏭️  Skipped:     %d", skippedCount)
	imp.logger.Infof("❌ Failed:      %d", failCount)
	imp.logger.Info("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

	if failCount > 0 {
		return fmt.Errorf("import completed with %d failures", failCount)
	}

	return nil
}

func (imp *Importer) generateTasks(symbol, interval string, startYear, startMonth, endYear, endMonth int) []monthTask {
	var tasks []monthTask

	currentYear := startYear
	currentMonth := startMonth

	for {
		tasks = append(tasks, monthTask{
			Symbol:   symbol,
			Interval: interval,
			Year:     currentYear,
			Month:    currentMonth,
		})

		if currentYear == endYear && currentMonth == endMonth {
			break
		}

		currentMonth++
		if currentMonth > 12 {
			currentMonth = 1
			currentYear++
		}
	}

	return tasks
}

func (imp *Importer) processMonth(ctx context.Context, task monthTask) importResult {
	filename := fmt.Sprintf("%s-%s-%04d-%02d.zip", task.Symbol, task.Interval, task.Year, task.Month)
	url := fmt.Sprintf("%s/%s/%s/%s", binanceDataURL, task.Symbol, task.Interval, filename)

	// Download
	zipData, err := imp.downloadFile(url)
	if err != nil {
		// Try daily fallback
		candles, err := imp.downloadDailyFiles(task)
		if err != nil {
			return importResult{Task: task, Error: err}
		}
		if len(candles) == 0 {
			return importResult{Task: task, Skipped: true}
		}

		// Batch insert
		if err := imp.repo.BatchCreateCandles(ctx, candles); err != nil {
			return importResult{Task: task, Error: fmt.Errorf("failed to insert candles: %w", err)}
		}

		return importResult{Task: task, Count: len(candles)}
	}

	// Parse ZIP
	candles, err := imp.parseZipData(zipData, task.Symbol, task.Interval)
	if err != nil {
		return importResult{Task: task, Error: fmt.Errorf("failed to parse data: %w", err)}
	}

	// Batch insert
	if err := imp.repo.BatchCreateCandles(ctx, candles); err != nil {
		return importResult{Task: task, Error: fmt.Errorf("failed to insert candles: %w", err)}
	}

	return importResult{Task: task, Count: len(candles)}
}

func (imp *Importer) downloadFile(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func (imp *Importer) parseZipData(zipData []byte, symbol, interval string) ([]*models.Candle, error) {
	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, err
	}

	if len(reader.File) == 0 {
		return nil, fmt.Errorf("empty zip file")
	}

	// Read first file
	file := reader.File[0]
	rc, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	csvReader := csv.NewReader(rc)
	var candles []*models.Candle

	for {
		record, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		candle, err := imp.parseCSVRecord(record, symbol, interval)
		if err != nil {
			imp.logger.Warnf("Failed to parse record: %v", err)
			continue
		}

		candles = append(candles, candle)
	}

	return candles, nil
}

func (imp *Importer) parseCSVRecord(record []string, symbol, interval string) (*models.Candle, error) {
	if len(record) < 11 {
		return nil, fmt.Errorf("invalid record length: %d", len(record))
	}

	// Parse timestamps (Binance changed format in 2025)
	// Old format (2024 and earlier): milliseconds (13 digits)
	// New format (2025+): microseconds (16 digits)
	openTimeRaw, _ := strconv.ParseInt(record[0], 10, 64)
	closeTimeRaw, _ := strconv.ParseInt(record[6], 10, 64)

	// Convert to milliseconds if needed (detect format by digit count)
	var openTimeMs, closeTimeMs int64
	if openTimeRaw > 9999999999999 { // 14+ digits = microseconds
		openTimeMs = openTimeRaw / 1000  // Convert microseconds to milliseconds
		closeTimeMs = closeTimeRaw / 1000
	} else {
		openTimeMs = openTimeRaw  // Already in milliseconds
		closeTimeMs = closeTimeRaw
	}

	// Convert milliseconds to time.Time in UTC
	// CRITICAL FIX: Ensure proper timezone handling for ClickHouse DateTime64(3)
	openTime := time.Unix(openTimeMs/1000, (openTimeMs%1000)*1_000_000).UTC()
	closeTime := time.Unix(closeTimeMs/1000, (closeTimeMs%1000)*1_000_000).UTC()

	// Parse decimals
	open, _ := decimal.NewFromString(record[1])
	high, _ := decimal.NewFromString(record[2])
	low, _ := decimal.NewFromString(record[3])
	close, _ := decimal.NewFromString(record[4])
	volume, _ := decimal.NewFromString(record[5])
	quoteVolume, _ := decimal.NewFromString(record[7])
	tradeCount, _ := strconv.ParseInt(record[8], 10, 32)
	takerBuyBase, _ := decimal.NewFromString(record[9])
	takerBuyQuote, _ := decimal.NewFromString(record[10])

	now := time.Now()

	return &models.Candle{
		Symbol:              symbol,
		Interval:            interval,
		OpenTime:            openTime,
		CloseTime:           closeTime,
		Open:                open,
		High:                high,
		Low:                 low,
		Close:               close,
		Volume:              volume,
		QuoteVolume:         quoteVolume,
		TradeCount:          int(tradeCount),
		TakerBuyBaseVolume:  takerBuyBase,
		TakerBuyQuoteVolume: takerBuyQuote,
		Source:              "aggregated",
		IsClosed:            true,
		ContractType:        "spot",
		CreatedAt:           now,
		UpdatedAt:           now,
	}, nil
}

func (imp *Importer) downloadDailyFiles(task monthTask) ([]*models.Candle, error) {
	// Try downloading daily files as fallback
	dailyURL := "https://data.binance.vision/data/spot/daily/klines"

	daysInMonth := time.Date(task.Year, time.Month(task.Month+1), 0, 0, 0, 0, 0, time.UTC).Day()
	var allCandles []*models.Candle

	for day := 1; day <= daysInMonth; day++ {
		filename := fmt.Sprintf("%s-%s-%04d-%02d-%02d.zip", task.Symbol, task.Interval, task.Year, task.Month, day)
		url := fmt.Sprintf("%s/%s/%s/%s", dailyURL, task.Symbol, task.Interval, filename)

		zipData, err := imp.downloadFile(url)
		if err != nil {
			continue // Skip missing days
		}

		candles, err := imp.parseZipData(zipData, task.Symbol, task.Interval)
		if err != nil {
			continue
		}

		allCandles = append(allCandles, candles...)
	}

	return allCandles, nil
}
