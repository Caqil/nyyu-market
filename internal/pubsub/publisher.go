package pubsub

import (
	"context"
	"encoding/json"

	"nyyu-market/internal/models"

	"github.com/go-redis/redis/v8"
	"github.com/sirupsen/logrus"
)

type Publisher struct {
	client *redis.Client
	logger *logrus.Logger
}

func NewPublisher(client *redis.Client, logger *logrus.Logger) *Publisher {
	return &Publisher{
		client: client,
		logger: logger,
	}
}

// PublishCandle publishes candle update to Redis channel
func (p *Publisher) PublishCandle(ctx context.Context, channel string, candle *models.Candle) error {
	data, err := json.Marshal(candle)
	if err != nil {
		return err
	}

	return p.client.Publish(ctx, channel, data).Err()
}

// PublishPrice publishes price update to Redis channel
func (p *Publisher) PublishPrice(ctx context.Context, symbol string, price *models.Price) error {
	data, err := json.Marshal(price)
	if err != nil {
		return err
	}

	channel := "nyyu:market:price:" + symbol
	return p.client.Publish(ctx, channel, data).Err()
}

// PublishMarkPrice publishes mark price update to Redis channel
func (p *Publisher) PublishMarkPrice(ctx context.Context, symbol string, markPrice *models.MarkPrice) error {
	data, err := json.Marshal(markPrice)
	if err != nil {
		return err
	}

	channel := "nyyu:market:markprice:" + symbol
	return p.client.Publish(ctx, channel, data).Err()
}

// PublishAllPrices publishes all prices to ticker channel
func (p *Publisher) PublishAllPrices(ctx context.Context, prices []models.Price) error {
	data, err := json.Marshal(prices)
	if err != nil {
		return err
	}

	channel := "nyyu:market:ticker:all"
	return p.client.Publish(ctx, channel, data).Err()
}
