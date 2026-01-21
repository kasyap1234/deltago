package backtest

import (
	"math"
)

const (
	RiskFreeRate     = 0.05        // 5% annual risk-free rate
	DailyExpiryHours = 6           // 6 hours for daily crypto options
	DailyExpiryDays  = 1.0         // Use 1 day since we have daily data (not intraday)
	TradingDaysYear  = 365.0       // Crypto trades 24/7
	RollingWindow    = 30          // 30-day rolling window for IV calculation
	IVMultiplier     = 1.3         // Implied vol is typically 30% higher than realized vol
	
	// Trading costs
	MakerFee         = 0.0001      // 0.01% maker fee on Delta Exchange
	SlippagePct      = 0.001       // 0.1% slippage assumption
	MarginPctStraddle = 0.15       // 15% margin for naked straddle
	MarginPctCondor   = 0.05       // 5% margin for defined-risk condor (spread width)
)

// NormCDF computes the standard normal cumulative distribution function
func NormCDF(x float64) float64 {
	return 0.5 * (1 + math.Erf(x/math.Sqrt2))
}

// NormPDF computes the standard normal probability density function
func NormPDF(x float64) float64 {
	return math.Exp(-0.5*x*x) / math.Sqrt(2*math.Pi)
}

// BlackScholesCall calculates the call option price using Black-Scholes
// S: spot price, K: strike price, T: time to expiry in years, r: risk-free rate, sigma: volatility
func BlackScholesCall(S, K, T, r, sigma float64) float64 {
	if T <= 0 {
		return math.Max(S-K, 0)
	}

	d1 := (math.Log(S/K) + (r+0.5*sigma*sigma)*T) / (sigma * math.Sqrt(T))
	d2 := d1 - sigma*math.Sqrt(T)

	return S*NormCDF(d1) - K*math.Exp(-r*T)*NormCDF(d2)
}

// BlackScholesPut calculates the put option price using Black-Scholes
func BlackScholesPut(S, K, T, r, sigma float64) float64 {
	if T <= 0 {
		return math.Max(K-S, 0)
	}

	d1 := (math.Log(S/K) + (r+0.5*sigma*sigma)*T) / (sigma * math.Sqrt(T))
	d2 := d1 - sigma*math.Sqrt(T)

	return K*math.Exp(-r*T)*NormCDF(-d2) - S*NormCDF(-d1)
}

// DeltaCall calculates the delta of a call option
func DeltaCall(S, K, T, r, sigma float64) float64 {
	if T <= 0 {
		if S > K {
			return 1.0
		}
		return 0.0
	}

	d1 := (math.Log(S/K) + (r+0.5*sigma*sigma)*T) / (sigma * math.Sqrt(T))
	return NormCDF(d1)
}

// DeltaPut calculates the delta of a put option
func DeltaPut(S, K, T, r, sigma float64) float64 {
	if T <= 0 {
		if S < K {
			return -1.0
		}
		return 0.0
	}

	d1 := (math.Log(S/K) + (r+0.5*sigma*sigma)*T) / (sigma * math.Sqrt(T))
	return NormCDF(d1) - 1.0
}

// CalculateHistoricalVolatility computes annualized volatility from price history
// Uses 30-day rolling standard deviation of log returns * sqrt(365)
func CalculateHistoricalVolatility(prices []float64) float64 {
	if len(prices) < 2 {
		return 0
	}

	window := RollingWindow
	if len(prices) < window+1 {
		window = len(prices) - 1
	}

	recentPrices := prices[len(prices)-window-1:]

	var logReturns []float64
	for i := 1; i < len(recentPrices); i++ {
		if recentPrices[i-1] > 0 && recentPrices[i] > 0 {
			logReturn := math.Log(recentPrices[i] / recentPrices[i-1])
			logReturns = append(logReturns, logReturn)
		}
	}

	if len(logReturns) < 2 {
		return 0
	}

	var sum, sumSq float64
	for _, r := range logReturns {
		sum += r
		sumSq += r * r
	}

	n := float64(len(logReturns))
	mean := sum / n
	variance := (sumSq / n) - (mean * mean)

	if variance < 0 {
		variance = 0
	}

	dailyStdDev := math.Sqrt(variance)
	annualizedVol := dailyStdDev * math.Sqrt(TradingDaysYear)

	return annualizedVol
}

// FindStrikeByDelta finds the strike price that gives approximately the target delta
// For calls, delta is positive (0 to 1). For puts, delta is negative (-1 to 0).
// Returns the strike that produces the closest delta to target.
func FindStrikeByDelta(S, T, r, sigma, targetDelta float64, isCall bool, strikeStep float64) float64 {
	if strikeStep <= 0 {
		strikeStep = 100 // Default $100 step for BTC
	}

	minStrike := S * 0.7
	maxStrike := S * 1.3

	var bestStrike float64
	bestDiff := math.MaxFloat64

	for strike := minStrike; strike <= maxStrike; strike += strikeStep {
		var delta float64
		if isCall {
			delta = DeltaCall(S, strike, T, r, sigma)
		} else {
			delta = DeltaPut(S, strike, T, r, sigma)
		}

		diff := math.Abs(delta - targetDelta)
		if diff < bestDiff {
			bestDiff = diff
			bestStrike = strike
		}
	}

	return bestStrike
}

// ImpliedVolatilityCall calculates IV from market price using Newton-Raphson
func ImpliedVolatilityCall(S, K, T, r, marketPrice float64) float64 {
	if T <= 0 || marketPrice <= 0 {
		return 0
	}

	sigma := 0.5
	for i := 0; i < 100; i++ {
		price := BlackScholesCall(S, K, T, r, sigma)
		vega := S * NormPDF((math.Log(S/K)+(r+0.5*sigma*sigma)*T)/(sigma*math.Sqrt(T))) * math.Sqrt(T)

		if vega < 1e-10 {
			break
		}

		diff := price - marketPrice
		if math.Abs(diff) < 1e-8 {
			break
		}

		sigma = sigma - diff/vega
		if sigma <= 0 {
			sigma = 0.01
		}
		if sigma > 5 {
			sigma = 5
		}
	}

	return sigma
}

// ImpliedVolatilityPut calculates IV from market price using Newton-Raphson
func ImpliedVolatilityPut(S, K, T, r, marketPrice float64) float64 {
	if T <= 0 || marketPrice <= 0 {
		return 0
	}

	sigma := 0.5
	for i := 0; i < 100; i++ {
		price := BlackScholesPut(S, K, T, r, sigma)
		vega := S * NormPDF((math.Log(S/K)+(r+0.5*sigma*sigma)*T)/(sigma*math.Sqrt(T))) * math.Sqrt(T)

		if vega < 1e-10 {
			break
		}

		diff := price - marketPrice
		if math.Abs(diff) < 1e-8 {
			break
		}

		sigma = sigma - diff/vega
		if sigma <= 0 {
			sigma = 0.01
		}
		if sigma > 5 {
			sigma = 5
		}
	}

	return sigma
}
