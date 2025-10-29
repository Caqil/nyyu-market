package cache

import (
	"context"
	"encoding/json"
	"time"

	"nyyu-market/internal/models"

	"github.com/go-redis/redis/v8"
	"github.com/sirupsen/logrus"
)

type CandleCache struct {
	client *redis.Client
	logger *logrus.Logger
}

func NewCandleCache(client *redis.Client, logger *logrus.Logger) *CandleCache {
	return &CandleCache{
		client: client,
		logger: logger,
	}
}

// Set caches candles
func (c *CandleCache) Set(ctx context.Context, key string, candles []models.Candle, ttl time.Duration) error {
	data, err := json.Marshal(candles)
	if err != nil {
		return err
	}

	return c.client.Set(ctx, "candle:"+key, data, ttl).Err()
}

// Get retrieves cached candles
func (c *CandleCache) Get(ctx context.Context, key string) ([]models.Candle, error) {
	data, err := c.client.Get(ctx, "candle:"+key).Result()
	if err != nil {
		return nil, err
	}

	var candles []models.Candle
	if err := json.Unmarshal([]byte(data), &candles); err != nil {
		return nil, err
	}

	return candles, nil
}

// SetLatest caches a single latest candle
func (c *CandleCache) SetLatest(ctx context.Context, key string, candle *models.Candle, ttl time.Duration) error {
	data, err := json.Marshal(candle)
	if err != nil {
		return err
	}

	return c.client.Set(ctx, "candle:"+key, data, ttl).Err()
}

// GetLatest retrieves cached latest candle
func (c *CandleCache) GetLatest(ctx context.Context, key string) (*models.Candle, error) {
	data, err := c.client.Get(ctx, "candle:"+key).Result()
	if err != nil {
		return nil, err
	}

	var candle models.Candle
	if err := json.Unmarshal([]byte(data), &candle); err != nil {
		return nil, err
	}

	return &candle, nil
}

// Delete removes from cache
func (c *CandleCache) Delete(ctx context.Context, key string) error {
	return c.client.Del(ctx, "candle:"+key).Err()
}
