package aggregator

// Interval conversion helper functions for each exchange

// convertIntervalToKraken converts standard interval to Kraken format (minutes)
func convertIntervalToKraken(interval string) int {
	switch interval {
	case "1m":
		return 1
	case "5m":
		return 5
	case "15m":
		return 15
	case "30m":
		return 30
	case "1h":
		return 60
	case "4h":
		return 240
	case "1d":
		return 1440
	case "1w":
		return 10080
	default:
		return 1
	}
}

// convertIntervalToCoinbase converts standard interval to Coinbase format
func convertIntervalToCoinbase(interval string) string {
	switch interval {
	case "1m":
		return "ONE_MINUTE"
	case "5m":
		return "FIVE_MINUTE"
	case "15m":
		return "FIFTEEN_MINUTE"
	case "30m":
		return "THIRTY_MINUTE"
	case "1h":
		return "ONE_HOUR"
	case "2h":
		return "TWO_HOUR"
	case "6h":
		return "SIX_HOUR"
	case "1d":
		return "ONE_DAY"
	default:
		return "ONE_MINUTE"
	}
}

// convertIntervalToBybit converts standard interval to Bybit format
func convertIntervalToBybit(interval string) string {
	switch interval {
	case "1m":
		return "1"
	case "3m":
		return "3"
	case "5m":
		return "5"
	case "15m":
		return "15"
	case "30m":
		return "30"
	case "1h":
		return "60"
	case "2h":
		return "120"
	case "4h":
		return "240"
	case "6h":
		return "360"
	case "1d":
		return "D"
	case "1w":
		return "W"
	case "1M":
		return "M"
	default:
		return "1"
	}
}

// convertIntervalToOKX converts standard interval to OKX format
func convertIntervalToOKX(interval string) string {
	switch interval {
	case "1m":
		return "candle1m"
	case "3m":
		return "candle3m"
	case "5m":
		return "candle5m"
	case "15m":
		return "candle15m"
	case "30m":
		return "candle30m"
	case "1h":
		return "candle1H"
	case "2h":
		return "candle2H"
	case "4h":
		return "candle4H"
	case "6h":
		return "candle6H"
	case "12h":
		return "candle12H"
	case "1d":
		return "candle1D"
	case "1w":
		return "candle1W"
	case "1M":
		return "candle1M"
	default:
		return "candle1m"
	}
}

// convertIntervalToGateIO converts standard interval to Gate.io format
func convertIntervalToGateIO(interval string) string {
	switch interval {
	case "1m":
		return "1m"
	case "5m":
		return "5m"
	case "15m":
		return "15m"
	case "30m":
		return "30m"
	case "1h":
		return "1h"
	case "4h":
		return "4h"
	case "8h":
		return "8h"
	case "1d":
		return "1d"
	case "1w":
		return "7d" // Gate.io uses 7d instead of 1w
	default:
		return "1m"
	}
}

// convertGateIOIntervalToStandard converts Gate.io interval format back to standard
func convertGateIOIntervalToStandard(gateInterval string) string {
	switch gateInterval {
	case "1m":
		return "1m"
	case "5m":
		return "5m"
	case "15m":
		return "15m"
	case "30m":
		return "30m"
	case "1h":
		return "1h"
	case "4h":
		return "4h"
	case "8h":
		return "8h"
	case "1d":
		return "1d"
	case "7d":
		return "1w" // Gate.io uses 7d, we use 1w
	default:
		return "1m"
	}
}
