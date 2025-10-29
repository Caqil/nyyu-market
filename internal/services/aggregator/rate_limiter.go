package aggregator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// SubscriptionRequest represents a subscription request
type SubscriptionRequest struct {
	Exchange string
	Symbol   string
	Interval string
	Priority int // Higher priority = processed first
	Callback func(error)
}

// RateLimiterManager manages rate limiters for all exchanges
type RateLimiterManager struct {
	limiters map[string]*ExchangeRateLimiter
	mu       sync.RWMutex
}

// ExchangeRateLimiter manages rate limiting for a single exchange
type ExchangeRateLimiter struct {
	name     string
	limiter  *rate.Limiter
	queue    chan *SubscriptionRequest
	mu       sync.RWMutex

	// Rate limit tracking
	requestCount     int64
	rateLimitHits    int64
	lastRateLimitHit time.Time

	// Adaptive backoff
	backoffDuration  time.Duration
	maxBackoff       time.Duration
	backoffMultiplier float64
}

// NewRateLimiterManager creates a new rate limiter manager
func NewRateLimiterManager() *RateLimiterManager {
	return &RateLimiterManager{
		limiters: make(map[string]*ExchangeRateLimiter),
	}
}

// RegisterExchange registers a rate limiter for an exchange
func (m *RateLimiterManager) RegisterExchange(name string, rps float64, burst int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.limiters[name] = &ExchangeRateLimiter{
		name:              name,
		limiter:           rate.NewLimiter(rate.Limit(rps), burst),
		queue:             make(chan *SubscriptionRequest, 1000),
		maxBackoff:        5 * time.Minute,
		backoffMultiplier: 1.5,
	}
}

// GetLimiter returns the rate limiter for an exchange
func (m *RateLimiterManager) GetLimiter(exchange string) (*ExchangeRateLimiter, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	limiter, ok := m.limiters[exchange]
	if !ok {
		return nil, fmt.Errorf("rate limiter not found for %s", exchange)
	}

	return limiter, nil
}

// Wait waits for permission to make a request (with context)
func (e *ExchangeRateLimiter) Wait(ctx context.Context) error {
	e.mu.RLock()
	backoffDuration := e.backoffDuration
	e.mu.RUnlock()

	// Apply adaptive backoff if we hit rate limits recently
	if backoffDuration > 0 {
		select {
		case <-time.After(backoffDuration):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Wait for rate limiter
	return e.limiter.Wait(ctx)
}

// Allow checks if a request can be made immediately without blocking
func (e *ExchangeRateLimiter) Allow() bool {
	e.mu.RLock()
	backoffDuration := e.backoffDuration
	lastHit := e.lastRateLimitHit
	e.mu.RUnlock()

	// Check if we're still in backoff period
	if backoffDuration > 0 && time.Since(lastHit) < backoffDuration {
		return false
	}

	return e.limiter.Allow()
}

// RecordRateLimitHit records a rate limit hit and applies adaptive backoff
func (e *ExchangeRateLimiter) RecordRateLimitHit() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.rateLimitHits++
	e.lastRateLimitHit = time.Now()

	// Adaptive backoff - increase duration on each hit
	if e.backoffDuration == 0 {
		e.backoffDuration = 1 * time.Second
	} else {
		e.backoffDuration = time.Duration(float64(e.backoffDuration) * e.backoffMultiplier)
		if e.backoffDuration > e.maxBackoff {
			e.backoffDuration = e.maxBackoff
		}
	}
}

// RecordSuccess records a successful request (reduces backoff)
func (e *ExchangeRateLimiter) RecordSuccess() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.requestCount++

	// Gradually reduce backoff on successful requests
	if e.backoffDuration > 0 {
		// Check if it's been long enough since last rate limit
		if time.Since(e.lastRateLimitHit) > 5*time.Minute {
			e.backoffDuration = 0 // Reset backoff
		} else {
			// Reduce backoff by 10% on each success
			e.backoffDuration = time.Duration(float64(e.backoffDuration) * 0.9)
			if e.backoffDuration < time.Second {
				e.backoffDuration = 0
			}
		}
	}
}

// GetStats returns rate limiter statistics
func (e *ExchangeRateLimiter) GetStats() map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return map[string]interface{}{
		"name":               e.name,
		"request_count":      e.requestCount,
		"rate_limit_hits":    e.rateLimitHits,
		"last_rate_limit":    e.lastRateLimitHit,
		"current_backoff_ms": e.backoffDuration.Milliseconds(),
		"queue_size":         len(e.queue),
	}
}

// SubscriptionBatcher batches subscription requests to reduce API calls
type SubscriptionBatcher struct {
	requests      map[string]*SubscriptionRequest // key: symbol@interval
	mu            sync.Mutex
	flushInterval time.Duration
	maxBatchSize  int
	flushCallback func([]*SubscriptionRequest)
}

// NewSubscriptionBatcher creates a new subscription batcher
func NewSubscriptionBatcher(flushInterval time.Duration, maxSize int, callback func([]*SubscriptionRequest)) *SubscriptionBatcher {
	return &SubscriptionBatcher{
		requests:      make(map[string]*SubscriptionRequest),
		flushInterval: flushInterval,
		maxBatchSize:  maxSize,
		flushCallback: callback,
	}
}

// Add adds a subscription request to the batch
func (b *SubscriptionBatcher) Add(req *SubscriptionRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := fmt.Sprintf("%s@%s", req.Symbol, req.Interval)

	// Deduplicate - if already exists, keep higher priority
	if existing, ok := b.requests[key]; ok {
		if req.Priority > existing.Priority {
			b.requests[key] = req
		}
		return
	}

	b.requests[key] = req

	// Auto-flush if batch is full
	if len(b.requests) >= b.maxBatchSize {
		b.flushLocked()
	}
}

// Flush flushes all pending requests
func (b *SubscriptionBatcher) Flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushLocked()
}

func (b *SubscriptionBatcher) flushLocked() {
	if len(b.requests) == 0 {
		return
	}

	batch := make([]*SubscriptionRequest, 0, len(b.requests))
	for _, req := range b.requests {
		batch = append(batch, req)
	}

	// Clear requests
	b.requests = make(map[string]*SubscriptionRequest)

	// Call callback outside of lock
	go b.flushCallback(batch)
}

// Start starts the auto-flush timer
func (b *SubscriptionBatcher) Start(ctx context.Context) {
	ticker := time.NewTicker(b.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.Flush()
		}
	}
}
