package metrics

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	dto "github.com/prometheus/client_model/go"
)

var (
	// Real-time candle metrics
	RealtimeCandleUpdates = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nyyu_realtime_candle_updates_total",
			Help: "Total real-time candle updates processed by exchange",
		},
		[]string{"exchange", "symbol", "interval"},
	)

	RealtimeCandleLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nyyu_realtime_candle_latency_ms",
			Help:    "Real-time candle processing latency in milliseconds",
			Buckets: []float64{0.1, 0.5, 1, 5, 10, 50, 100, 500, 1000},
		},
		[]string{"exchange"},
	)

	RealtimeAggregationErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nyyu_realtime_aggregation_errors_total",
			Help: "Total real-time aggregation errors",
		},
		[]string{"exchange", "error_type"},
	)

	// Price metrics
	PriceUpdates = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nyyu_price_updates_total",
			Help: "Total price updates published",
		},
		[]string{"symbol"},
	)

	PriceCalculationLatency = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "nyyu_price_calculation_latency_ms",
			Help:    "Price calculation latency in milliseconds",
			Buckets: []float64{0.1, 0.5, 1, 5, 10, 50, 100, 500},
		},
	)

	// Subscription metrics
	ActiveSubscriptions = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "nyyu_active_subscriptions",
			Help: "Number of active client subscriptions",
		},
		[]string{"type"}, // candle, price, markprice
	)

	TotalSubscriptions = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nyyu_subscriptions_total",
			Help: "Total subscriptions created",
		},
		[]string{"type"},
	)

	// Cache metrics
	CacheHits = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nyyu_cache_hits_total",
			Help: "Total cache hits by tier",
		},
		[]string{"tier"}, // memory, redis, db
	)

	CacheMisses = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nyyu_cache_misses_total",
			Help: "Total cache misses by tier",
		},
		[]string{"tier"},
	)

	CacheHitRatio = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "nyyu_cache_hit_ratio",
			Help: "Cache hit ratio by tier (0-1)",
		},
		[]string{"tier"},
	)

	// Database metrics
	DatabaseQueries = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nyyu_database_queries_total",
			Help: "Total database queries executed",
		},
		[]string{"operation"}, // select, insert, update
	)

	DatabaseQueryLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nyyu_database_query_latency_ms",
			Help:    "Database query latency in milliseconds",
			Buckets: []float64{1, 5, 10, 50, 100, 500, 1000, 5000},
		},
		[]string{"operation"},
	)

	// Exchange connection metrics
	ExchangeConnections = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "nyyu_exchange_connections",
			Help: "Number of active exchange WebSocket connections",
		},
		[]string{"exchange"},
	)

	ExchangeMessages = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nyyu_exchange_messages_total",
			Help: "Total messages received from exchanges",
		},
		[]string{"exchange"},
	)

	ExchangeErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nyyu_exchange_errors_total",
			Help: "Total exchange connection errors",
		},
		[]string{"exchange", "error_type"},
	)

	// System metrics
	GoroutinesActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "nyyu_goroutines_active",
			Help: "Number of active goroutines",
		},
	)

	MemoryAllocated = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "nyyu_memory_allocated_bytes",
			Help: "Total memory allocated in bytes",
		},
	)

	// Publishing metrics
	PublishSuccess = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nyyu_publish_success_total",
			Help: "Total successful Redis publishes",
		},
		[]string{"channel_type"}, // candle, price
	)

	PublishFailures = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nyyu_publish_failures_total",
			Help: "Total failed Redis publishes",
		},
		[]string{"channel_type"},
	)

	PublishLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nyyu_publish_latency_ms",
			Help:    "Redis publish latency in milliseconds",
			Buckets: []float64{0.1, 0.5, 1, 5, 10, 50, 100},
		},
		[]string{"channel_type"},
	)
)

// RateTracker tracks rate per second for dynamic metrics
type RateTracker struct {
	count       int64
	lastCount   int64
	lastUpdated time.Time
	mu          sync.RWMutex
}

func NewRateTracker() *RateTracker {
	return &RateTracker{
		lastUpdated: time.Now(),
	}
}

func (rt *RateTracker) Increment() {
	atomic.AddInt64(&rt.count, 1)
}

func (rt *RateTracker) GetRate() float64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rt.lastUpdated).Seconds()

	if elapsed < 1.0 {
		return 0 // Not enough time passed
	}

	current := atomic.LoadInt64(&rt.count)
	diff := current - rt.lastCount
	rate := float64(diff) / elapsed

	rt.lastCount = current
	rt.lastUpdated = now

	return rate
}

// Global rate trackers
var (
	priceUpdatesTracker  = NewRateTracker()
	candleWritesTracker  = NewRateTracker()
)

// TrackPriceUpdate increments price update counter
func TrackPriceUpdate(symbol string) {
	PriceUpdates.WithLabelValues(symbol).Inc()
	priceUpdatesTracker.Increment()
}

// TrackCandleUpdate increments candle update counter
func TrackCandleUpdate(exchange, symbol, interval string) {
	RealtimeCandleUpdates.WithLabelValues(exchange, symbol, interval).Inc()
	candleWritesTracker.Increment()
}

// GetPriceUpdatesPerSecond returns current price updates/sec
func GetPriceUpdatesPerSecond() float64 {
	return priceUpdatesTracker.GetRate()
}

// GetCandleWritesPerSecond returns current candle writes/sec
func GetCandleWritesPerSecond() float64 {
	return candleWritesTracker.GetRate()
}

// RecordCacheAccess records a cache hit or miss
func RecordCacheAccess(tier string, hit bool) {
	if hit {
		CacheHits.WithLabelValues(tier).Inc()
	} else {
		CacheMisses.WithLabelValues(tier).Inc()
	}
	updateCacheHitRatio(tier)
}

// updateCacheHitRatio calculates and updates cache hit ratio
func updateCacheHitRatio(tier string) {
	// Get metric values (simplified - in production use promql)
	// This is an approximation for real-time display
	hits, _ := CacheHits.GetMetricWithLabelValues(tier)
	misses, _ := CacheMisses.GetMetricWithLabelValues(tier)

	if hits != nil && misses != nil {
		var hitsVal, missesVal float64
		hitsMetric := &dto.Metric{}
		missesMetric := &dto.Metric{}

		if hits.Write(hitsMetric) == nil && misses.Write(missesMetric) == nil {
			hitsVal = hitsMetric.Counter.GetValue()
			missesVal = missesMetric.Counter.GetValue()

			total := hitsVal + missesVal
			if total > 0 {
				ratio := hitsVal / total
				CacheHitRatio.WithLabelValues(tier).Set(ratio)
			}
		}
	}
}

// TrackLatency is a helper to measure and record latency
func TrackLatency(start time.Time, histogram prometheus.Observer) {
	duration := time.Since(start).Milliseconds()
	histogram.Observe(float64(duration))
}

// Note: The updateCacheHitRatio function uses dto.Metric for accessing counter values
// This is a simplified implementation - in production, use Prometheus queries for accurate ratios
