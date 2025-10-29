package aggregator

import (
	"encoding/json"
	"fmt"
	"nyyu-market/internal/models"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// ============================================================================
// KRAKEN EXCHANGE HANDLER
// ============================================================================

func (a *ExchangeAggregator) subscribeKraken(ex *Exchange) error {
	a.activeStreamsMu.RLock()
	streams := make([]string, 0, len(a.activeStreams))
	for stream := range a.activeStreams {
		streams = append(streams, stream)
	}
	a.activeStreamsMu.RUnlock()

	// Fallback to default symbols if no subscriptions yet
	if len(streams) == 0 {
		streams = []string{
			"btcusdt@1m", "ethusdt@1m", "bnbusdt@1m",
			"solusdt@1m", "xrpusdt@1m",
		}
	}

	for _, streamKey := range streams {
		parts := strings.Split(streamKey, "@")
		if len(parts) != 2 {
			continue
		}
		symbol := strings.ToUpper(parts[0])
		interval := parts[1]

		// Convert BTCUSDT -> BTC/USD for Kraken
		krakenSymbol := strings.ReplaceAll(symbol, "USDT", "USD")
		if len(krakenSymbol) > 3 {
			krakenSymbol = krakenSymbol[:len(krakenSymbol)-3] + "/" + krakenSymbol[len(krakenSymbol)-3:]
		}

		krakenInterval := convertIntervalToKraken(interval)

		msg := map[string]interface{}{
			"method": "subscribe",
			"params": map[string]interface{}{
				"channel":  "ohlc",
				"symbol":   []string{krakenSymbol},
				"interval": krakenInterval,
			},
		}

		if err := ex.conn.WriteJSON(msg); err != nil {
			return err
		}
	}

	return nil
}

func (a *ExchangeAggregator) processKrakenMessage(ex *Exchange, message []byte) {
	var msg map[string]interface{}
	if err := json.Unmarshal(message, &msg); err != nil {
		return
	}

	// Kraken OHLC message format
	if channel, ok := msg["channel"].(string); ok && channel == "ohlc" {
		if dataArray, ok := msg["data"].([]interface{}); ok {
			for _, item := range dataArray {
				if ohlcData, ok := item.(map[string]interface{}); ok {
					symbolStr, _ := ohlcData["symbol"].(string)
					// Convert BTC/USD -> BTCUSDT
					symbol := strings.ReplaceAll(symbolStr, "/", "")
					symbol = strings.ReplaceAll(symbol, "USD", "USDT")

					intervalMinutes, _ := ohlcData["interval"].(float64)
					interval := "1m"
					switch int(intervalMinutes) {
					case 1:
						interval = "1m"
					case 5:
						interval = "5m"
					case 15:
						interval = "15m"
					case 30:
						interval = "30m"
					case 60:
						interval = "1h"
					case 240:
						interval = "4h"
					case 1440:
						interval = "1d"
					}

					if ohlc, ok := ohlcData["ohlc"].(map[string]interface{}); ok {
						openStr, _ := ohlc["open"].(string)
						highStr, _ := ohlc["high"].(string)
						lowStr, _ := ohlc["low"].(string)
						closeStr, _ := ohlc["close"].(string)
						volumeStr, _ := ohlc["volume"].(string)

						open, _ := decimal.NewFromString(openStr)
						high, _ := decimal.NewFromString(highStr)
						low, _ := decimal.NewFromString(lowStr)
						closePrc, _ := decimal.NewFromString(closeStr)
						volume, _ := decimal.NewFromString(volumeStr)

						timeFloat, _ := ohlc["time"].(float64)
						openTime := time.Unix(int64(timeFloat), 0)

						candle := &models.Candle{
							Symbol:       symbol,
							Interval:     interval,
							OpenTime:     openTime,
							CloseTime:    openTime.Add(a.intervalToDuration(interval)),
							Open:         open,
							High:         high,
							Low:          low,
							Close:        closePrc,
							Volume:       volume,
							QuoteVolume:  decimal.Zero,
							TradeCount:   0,
							Source:       "kraken",
							IsClosed:     false,
							ContractType: "spot",
							CreatedAt:    time.Now(),
							UpdatedAt:    time.Now(),
						}

						a.updateAggregatedPrice(symbol, "Kraken", closePrc)

						// Add to pending candles for aggregation (NOT direct write to DB)
						a.addPendingCandle("Kraken", symbol, interval, candle)
					}
				}
			}
		}
	}
}

// ============================================================================
// COINBASE EXCHANGE HANDLER
// ============================================================================

func (a *ExchangeAggregator) subscribeCoinbase(ex *Exchange) error {
	a.activeStreamsMu.RLock()
	streams := make([]string, 0, len(a.activeStreams))
	for stream := range a.activeStreams {
		streams = append(streams, stream)
	}
	a.activeStreamsMu.RUnlock()

	// Fallback to default symbols if no subscriptions yet
	if len(streams) == 0 {
		streams = []string{
			"btcusdt@1m", "ethusdt@1m", "bnbusdt@1m",
			"solusdt@1m", "xrpusdt@1m",
		}
	}

	for _, streamKey := range streams {
		parts := strings.Split(streamKey, "@")
		if len(parts) != 2 {
			continue
		}
		symbol := strings.ToUpper(parts[0])
		interval := parts[1]

		// Convert BTCUSDT -> BTC-USDT for Coinbase
		pair := symbol
		if len(pair) > 4 {
			pair = pair[:len(pair)-4] + "-" + pair[len(pair)-4:]
		}

		coinbaseInterval := convertIntervalToCoinbase(interval)

		msg := map[string]interface{}{
			"type":        "subscribe",
			"product_ids": []string{pair},
			"channels": []map[string]interface{}{
				{
					"name":     "candles",
					"interval": coinbaseInterval,
				},
			},
		}

		if err := ex.conn.WriteJSON(msg); err != nil {
			return err
		}
	}

	return nil
}

func (a *ExchangeAggregator) processCoinbaseMessage(ex *Exchange, message []byte) {
	var msg map[string]interface{}
	if err := json.Unmarshal(message, &msg); err != nil {
		return
	}

	if msgType, ok := msg["type"].(string); ok && msgType == "candles" {
		if productID, ok := msg["product_id"].(string); ok {
			symbol := strings.ReplaceAll(productID, "-", "")

			if candles, ok := msg["candles"].([]interface{}); ok && len(candles) > 0 {
				if candleData, ok := candles[0].(map[string]interface{}); ok {
					interval := "1m"
					if granularity, ok := msg["granularity"].(float64); ok {
						switch int(granularity) {
						case 60:
							interval = "1m"
						case 300:
							interval = "5m"
						case 900:
							interval = "15m"
						case 3600:
							interval = "1h"
						case 21600:
							interval = "6h"
						case 86400:
							interval = "1d"
						}
					}

					startStr, _ := candleData["start"].(string)
					startInt, _ := strconv.ParseInt(startStr, 10, 64)
					openTime := time.Unix(startInt, 0)

					openStr, _ := candleData["open"].(string)
					highStr, _ := candleData["high"].(string)
					lowStr, _ := candleData["low"].(string)
					closeStr, _ := candleData["close"].(string)
					volumeStr, _ := candleData["volume"].(string)

					open, _ := decimal.NewFromString(openStr)
					high, _ := decimal.NewFromString(highStr)
					low, _ := decimal.NewFromString(lowStr)
					closePrc, _ := decimal.NewFromString(closeStr)
					volume, _ := decimal.NewFromString(volumeStr)

					candle := &models.Candle{
						Symbol:       symbol,
						Interval:     interval,
						OpenTime:     openTime,
						CloseTime:    openTime.Add(a.intervalToDuration(interval)),
						Open:         open,
						High:         high,
						Low:          low,
						Close:        closePrc,
						Volume:       volume,
						QuoteVolume:  decimal.Zero,
						TradeCount:   0,
						Source:       "coinbase",
						IsClosed:     false,
						ContractType: "spot",
						CreatedAt:    time.Now(),
						UpdatedAt:    time.Now(),
					}

					a.updateAggregatedPrice(symbol, "Coinbase", closePrc)

					// Add to pending candles for aggregation (NOT direct write to DB)
					a.addPendingCandle("Coinbase", symbol, interval, candle)
				}
			}
		}
	}
}

// ============================================================================
// BYBIT EXCHANGE HANDLER
// ============================================================================

func (a *ExchangeAggregator) subscribeBybit(ex *Exchange) error {
	a.activeStreamsMu.RLock()
	streams := make([]string, 0, len(a.activeStreams))
	for stream := range a.activeStreams {
		streams = append(streams, stream)
	}
	a.activeStreamsMu.RUnlock()

	// Fallback to default symbols if no subscriptions yet
	if len(streams) == 0 {
		streams = []string{
			"btcusdt@1m", "ethusdt@1m", "bnbusdt@1m",
			"solusdt@1m", "xrpusdt@1m",
		}
	}

	args := make([]string, 0)
	for _, streamKey := range streams {
		parts := strings.Split(streamKey, "@")
		if len(parts) != 2 {
			continue
		}
		symbol := strings.ToUpper(parts[0])
		interval := parts[1]

		bybitInterval := convertIntervalToBybit(interval)
		args = append(args, fmt.Sprintf("kline.%s.%s", bybitInterval, symbol))
	}

	msg := map[string]interface{}{
		"op":   "subscribe",
		"args": args,
	}

	return ex.conn.WriteJSON(msg)
}

func (a *ExchangeAggregator) processBybitMessage(ex *Exchange, message []byte) {
	var msg map[string]interface{}
	if err := json.Unmarshal(message, &msg); err != nil {
		return
	}

	if topic, ok := msg["topic"].(string); ok && strings.HasPrefix(topic, "kline.") {
		if dataArray, ok := msg["data"].([]interface{}); ok && len(dataArray) > 0 {
			if klineData, ok := dataArray[0].(map[string]interface{}); ok {
				parts := strings.Split(topic, ".")
				symbol := ""
				bybitInterval := ""
				if len(parts) >= 3 {
					bybitInterval = parts[1]
					symbol = parts[2]
				}

				interval := "1m"
				switch bybitInterval {
				case "1":
					interval = "1m"
				case "5":
					interval = "5m"
				case "15":
					interval = "15m"
				case "30":
					interval = "30m"
				case "60":
					interval = "1h"
				case "240":
					interval = "4h"
				case "D":
					interval = "1d"
				case "W":
					interval = "1w"
				}

				startMillis, _ := klineData["start"].(float64)
				openTime := time.Unix(int64(startMillis)/1000, 0)

				openStr, _ := klineData["open"].(string)
				highStr, _ := klineData["high"].(string)
				lowStr, _ := klineData["low"].(string)
				closeStr, _ := klineData["close"].(string)
				volumeStr, _ := klineData["volume"].(string)
				confirm, _ := klineData["confirm"].(bool)

				open, _ := decimal.NewFromString(openStr)
				high, _ := decimal.NewFromString(highStr)
				low, _ := decimal.NewFromString(lowStr)
				closePrc, _ := decimal.NewFromString(closeStr)
				volume, _ := decimal.NewFromString(volumeStr)

				candle := &models.Candle{
					Symbol:       symbol,
					Interval:     interval,
					OpenTime:     openTime,
					CloseTime:    openTime.Add(a.intervalToDuration(interval)),
					Open:         open,
					High:         high,
					Low:          low,
					Close:        closePrc,
					Volume:       volume,
					QuoteVolume:  decimal.Zero,
					TradeCount:   0,
					Source:       "bybit",
					IsClosed:     confirm,
					ContractType: "spot",
					CreatedAt:    time.Now(),
					UpdatedAt:    time.Now(),
				}

				a.updateAggregatedPrice(symbol, "Bybit", closePrc)

				// Add to pending candles for aggregation (NOT direct write to DB)
				a.addPendingCandle("Bybit", symbol, interval, candle)
			}
		}
	}
}

// ============================================================================
// OKX EXCHANGE HANDLER
// ============================================================================

func (a *ExchangeAggregator) subscribeOKX(ex *Exchange) error {
	a.activeStreamsMu.RLock()
	streams := make([]string, 0, len(a.activeStreams))
	for stream := range a.activeStreams {
		streams = append(streams, stream)
	}
	a.activeStreamsMu.RUnlock()

	// Fallback to default symbols if no subscriptions yet
	if len(streams) == 0 {
		streams = []string{
			"btcusdt@1m", "ethusdt@1m", "bnbusdt@1m",
			"solusdt@1m", "xrpusdt@1m",
		}
	}

	args := make([]map[string]string, 0)
	for _, streamKey := range streams {
		parts := strings.Split(streamKey, "@")
		if len(parts) != 2 {
			continue
		}
		symbol := strings.ToUpper(parts[0])
		interval := parts[1]

		pair := symbol
		if len(pair) > 4 {
			pair = pair[:len(pair)-4] + "-" + pair[len(pair)-4:]
		}

		okxChannel := convertIntervalToOKX(interval)
		args = append(args, map[string]string{
			"channel": okxChannel,
			"instId":  pair,
		})
	}

	msg := map[string]interface{}{
		"op":   "subscribe",
		"args": args,
	}

	return ex.conn.WriteJSON(msg)
}

func (a *ExchangeAggregator) processOKXMessage(ex *Exchange, message []byte) {
	var msg map[string]interface{}
	if err := json.Unmarshal(message, &msg); err != nil {
		return
	}

	if dataArray, ok := msg["data"].([]interface{}); ok {
		for _, item := range dataArray {
			if candleArray, ok := item.([]interface{}); ok && len(candleArray) >= 7 {
				symbol := ""
				okxChannel := ""
				if arg, ok := msg["arg"].(map[string]interface{}); ok {
					if instID, ok := arg["instId"].(string); ok {
						symbol = strings.ReplaceAll(instID, "-", "")
					}
					if channel, ok := arg["channel"].(string); ok {
						okxChannel = channel
					}
				}

				interval := "1m"
				if strings.HasPrefix(okxChannel, "candle") {
					okxInterval := strings.TrimPrefix(okxChannel, "candle")
					switch okxInterval {
					case "1m":
						interval = "1m"
					case "5m":
						interval = "5m"
					case "15m":
						interval = "15m"
					case "30m":
						interval = "30m"
					case "1H":
						interval = "1h"
					case "4H":
						interval = "4h"
					case "1D":
						interval = "1d"
					case "1W":
						interval = "1w"
					}
				}

				timestampStr, _ := candleArray[0].(string)
				timestampMillis, _ := strconv.ParseInt(timestampStr, 10, 64)
				openTime := time.Unix(timestampMillis/1000, 0)

				openStr, _ := candleArray[1].(string)
				highStr, _ := candleArray[2].(string)
				lowStr, _ := candleArray[3].(string)
				closeStr, _ := candleArray[4].(string)
				volumeStr, _ := candleArray[5].(string)

				open, _ := decimal.NewFromString(openStr)
				high, _ := decimal.NewFromString(highStr)
				low, _ := decimal.NewFromString(lowStr)
				closePrc, _ := decimal.NewFromString(closeStr)
				volume, _ := decimal.NewFromString(volumeStr)

				candle := &models.Candle{
					Symbol:       symbol,
					Interval:     interval,
					OpenTime:     openTime,
					CloseTime:    openTime.Add(a.intervalToDuration(interval)),
					Open:         open,
					High:         high,
					Low:          low,
					Close:        closePrc,
					Volume:       volume,
					QuoteVolume:  decimal.Zero,
					TradeCount:   0,
					Source:       "okx",
					IsClosed:     false,
					ContractType: "spot",
					CreatedAt:    time.Now(),
					UpdatedAt:    time.Now(),
				}

				a.updateAggregatedPrice(symbol, "OKX", closePrc)

				// Add to pending candles for aggregation (NOT direct write to DB)
				a.addPendingCandle("OKX", symbol, interval, candle)
			}
		}
	}
}

// ============================================================================
// GATE.IO EXCHANGE HANDLER
// ============================================================================

func (a *ExchangeAggregator) subscribeGateIO(ex *Exchange) error {
	a.activeStreamsMu.RLock()
	streams := make([]string, 0, len(a.activeStreams))
	for stream := range a.activeStreams {
		streams = append(streams, stream)
	}
	a.activeStreamsMu.RUnlock()

	// Fallback to default symbols if no subscriptions yet
	if len(streams) == 0 {
		streams = []string{
			"btcusdt@1m", "ethusdt@1m", "bnbusdt@1m",
			"solusdt@1m", "xrpusdt@1m",
		}
	}

	for _, streamKey := range streams {
		parts := strings.Split(streamKey, "@")
		if len(parts) != 2 {
			continue
		}
		symbol := strings.ToUpper(parts[0])
		interval := parts[1]

		pair := symbol
		if len(pair) > 4 {
			pair = pair[:len(pair)-4] + "_" + pair[len(pair)-4:]
		}

		gateInterval := convertIntervalToGateIO(interval)

		msg := map[string]interface{}{
			"time":    time.Now().Unix(),
			"channel": "spot.candlesticks",
			"event":   "subscribe",
			"payload": []string{gateInterval, pair},
		}

		if err := ex.conn.WriteJSON(msg); err != nil {
			return err
		}
	}

	return nil
}

func (a *ExchangeAggregator) processGateIOMessage(ex *Exchange, message []byte) {
	var msg map[string]interface{}
	if err := json.Unmarshal(message, &msg); err != nil {
		return
	}

	if event, ok := msg["event"].(string); ok && event == "update" {
		if result, ok := msg["result"].(map[string]interface{}); ok {
			if currencyPair, ok := result["n"].(string); ok {
				parts := strings.SplitN(currencyPair, "_", 2)
				if len(parts) != 2 {
					return
				}

				gateInterval := parts[0]
				gatePair := parts[1]

				interval := convertGateIOIntervalToStandard(gateInterval)
				symbol := strings.ReplaceAll(gatePair, "_", "")

				timestampStr, _ := result["t"].(string)
				timestampInt, _ := strconv.ParseInt(timestampStr, 10, 64)
				openTime := time.Unix(timestampInt, 0)

				openStr, _ := result["o"].(string)
				highStr, _ := result["h"].(string)
				lowStr, _ := result["l"].(string)
				closeStr, _ := result["c"].(string)
				volumeStr, _ := result["v"].(string)
				windowClosed, _ := result["w"].(bool)

				open, _ := decimal.NewFromString(openStr)
				high, _ := decimal.NewFromString(highStr)
				low, _ := decimal.NewFromString(lowStr)
				closePrc, _ := decimal.NewFromString(closeStr)
				volume, _ := decimal.NewFromString(volumeStr)

				candle := &models.Candle{
					Symbol:       symbol,
					Interval:     interval,
					OpenTime:     openTime,
					CloseTime:    openTime.Add(a.intervalToDuration(interval)),
					Open:         open,
					High:         high,
					Low:          low,
					Close:        closePrc,
					Volume:       volume,
					QuoteVolume:  decimal.Zero,
					TradeCount:   0,
					Source:       "gateio",
					IsClosed:     windowClosed,
					ContractType: "spot",
					CreatedAt:    time.Now(),
					UpdatedAt:    time.Now(),
				}

				a.updateAggregatedPrice(symbol, "Gate.io", closePrc)

				// Add to pending candles for aggregation (NOT direct write to DB)
				a.addPendingCandle("Gate.io", symbol, interval, candle)
			}
		}
	}
}

// ============================================================================
// UTILITY METHODS
// ============================================================================

func (a *ExchangeAggregator) intervalToDuration(interval string) time.Duration {
	switch interval {
	case "1m":
		return time.Minute
	case "3m":
		return 3 * time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return time.Hour
	case "2h":
		return 2 * time.Hour
	case "4h":
		return 4 * time.Hour
	case "6h":
		return 6 * time.Hour
	case "8h":
		return 8 * time.Hour
	case "12h":
		return 12 * time.Hour
	case "1d":
		return 24 * time.Hour
	case "3d":
		return 72 * time.Hour
	case "1w":
		return 7 * 24 * time.Hour
	case "1M":
		return 30 * 24 * time.Hour
	default:
		return time.Minute
	}
}
