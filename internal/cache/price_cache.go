package cache

import (
	"context"
	"encoding/json"
	"time"

	"nyyu-market/internal/models"

	"github.com/go-redis/redis/v8"
	"github.com/sirupsen/logrus"
)

type PriceCache struct {
	client *redis.Client
	logger *logrus.Logger
}

func NewPriceCache(client *redis.Client, logger *logrus.Logger) *PriceCache {
	return &PriceCache{
		client: client,
		logger: logger,
	}
}

// SetPrice caches price data
func (c *PriceCache) SetPrice(ctx context.Context, symbol string, price *models.Price, ttl time.Duration) error {
	data, err := json.Marshal(price)
	if err != nil {
		return err
	}

	return c.client.Set(ctx, "price:"+symbol, data, ttl).Err()
}

// GetPrice retrieves cached price
func (c *PriceCache) GetPrice(ctx context.Context, symbol string) (*models.Price, error) {
	data, err := c.client.Get(ctx, "price:"+symbol).Result()
	if err != nil {
		return nil, err
	}

	var price models.Price
	if err := json.Unmarshal([]byte(data), &price); err != nil {
		return nil, err
	}

	return &price, nil
}

// SetMarkPrice caches mark price data
func (c *PriceCache) SetMarkPrice(ctx context.Context, symbol string, markPrice *models.MarkPrice, ttl time.Duration) error {
	data, err := json.Marshal(markPrice)
	if err != nil {
		return err
	}

	return c.client.Set(ctx, "markprice:"+symbol, data, ttl).Err()
}

// GetMarkPrice retrieves cached mark price
func (c *PriceCache) GetMarkPrice(ctx context.Context, symbol string) (*models.MarkPrice, error) {
	data, err := c.client.Get(ctx, "markprice:"+symbol).Result()
	if err != nil {
		return nil, err
	}

	var markPrice models.MarkPrice
	if err := json.Unmarshal([]byte(data), &markPrice); err != nil {
		return nil, err
	}

	return &markPrice, nil
}

// Delete removes from cache
func (c *PriceCache) Delete(ctx context.Context, key string) error {
	return c.client.Del(ctx, key).Err()
}
