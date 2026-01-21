package backtest

import "math"

// StraddleResult contains the outcome of a straddle simulation
type StraddleResult struct {
	SpotEntry       float64 // Spot price at entry
	Strike          float64 // ATM strike
	SpotExpiry      float64 // Spot price at expiry
	CallPremium     float64 // Premium collected from call
	PutPremium      float64 // Premium collected from put
	TotalPremium    float64 // Total premium collected
	CallIntrinsic   float64 // Call intrinsic value at expiry
	PutIntrinsic    float64 // Put intrinsic value at expiry
	TotalIntrinsic  float64 // Total payout at expiry
	GrossPnL        float64 // P&L before costs
	TradingCosts    float64 // Fees + slippage
	PnL             float64 // Net P&L (after costs)
	MarginRequired  float64 // Margin required to hold position
	ReturnOnMargin  float64 // Return as % of margin
	StopLossHit     bool    // Whether stop-loss was triggered
	StopLossPrice   float64 // Price at which stop-loss was hit
	DayHigh         float64 // Day's high price
	DayLow          float64 // Day's low price
}

// SimulateStraddle simulates selling an ATM straddle
// spotEntry: current spot price (strike will be ATM)
// volatility: annualized volatility
// timeToExpiry: time to expiry in years (use DailyExpiryDays/TradingDaysYear for daily)
// dayHigh, dayLow: intraday price extremes for stop-loss simulation
// spotExpiry: spot price at expiry
// stopLossMultiplier: stop-loss threshold as multiplier of premium (e.g., 1.5)
func SimulateStraddle(spotEntry, volatility, timeToExpiry, dayHigh, dayLow, spotExpiry, stopLossMultiplier float64) StraddleResult {
	strike := spotEntry

	// Use implied vol (higher than realized) for more realistic premiums
	impliedVol := volatility * IVMultiplier
	
	callPremium := BlackScholesCall(spotEntry, strike, timeToExpiry, RiskFreeRate, impliedVol)
	putPremium := BlackScholesPut(spotEntry, strike, timeToExpiry, RiskFreeRate, impliedVol)
	totalPremium := callPremium + putPremium
	
	// Calculate margin required (% of notional)
	marginRequired := spotEntry * MarginPctStraddle
	
	// Trading costs: entry + exit fees and slippage
	// 2 options * (entry + exit) * (fee + slippage)
	tradingCosts := 2 * totalPremium * (MakerFee + SlippagePct) * 2

	stopLossThreshold := totalPremium * stopLossMultiplier

	result := StraddleResult{
		SpotEntry:      spotEntry,
		Strike:         strike,
		SpotExpiry:     spotExpiry,
		CallPremium:    callPremium,
		PutPremium:     putPremium,
		TotalPremium:   totalPremium,
		MarginRequired: marginRequired,
		TradingCosts:   tradingCosts,
		DayHigh:        dayHigh,
		DayLow:         dayLow,
	}

	stopLossHit, stopLossPrice := checkStraddleStopLoss(
		spotEntry, strike, volatility, timeToExpiry,
		dayHigh, dayLow, totalPremium, stopLossThreshold,
	)

	if stopLossHit {
		result.StopLossHit = true
		result.StopLossPrice = stopLossPrice
		result.GrossPnL = -stopLossThreshold
		result.PnL = -stopLossThreshold - tradingCosts
		result.ReturnOnMargin = (result.PnL / marginRequired) * 100
		return result
	}

	callIntrinsic := math.Max(spotExpiry-strike, 0)
	putIntrinsic := math.Max(strike-spotExpiry, 0)
	totalIntrinsic := callIntrinsic + putIntrinsic

	result.CallIntrinsic = callIntrinsic
	result.PutIntrinsic = putIntrinsic
	result.TotalIntrinsic = totalIntrinsic
	result.GrossPnL = totalPremium - totalIntrinsic
	result.PnL = result.GrossPnL - tradingCosts
	result.ReturnOnMargin = (result.PnL / marginRequired) * 100

	return result
}

// checkStraddleStopLoss checks if stop-loss would be triggered during intraday movement
// Uses day's high/low to estimate worst-case option values
// More conservative: only trigger stop-loss on extreme moves (>3% from entry)
func checkStraddleStopLoss(spotEntry, strike, volatility, timeToExpiry, dayHigh, dayLow, initialPremium, stopLossThreshold float64) (bool, float64) {
	halfTime := timeToExpiry / 2
	impliedVol := volatility * IVMultiplier

	priceToCheck := dayHigh
	moveFromEntry := math.Abs(dayHigh - spotEntry)
	if math.Abs(dayLow-spotEntry) > moveFromEntry {
		priceToCheck = dayLow
	}

	// Only check stop-loss if move is significant (>3%)
	movePercent := math.Abs(priceToCheck-spotEntry) / spotEntry
	if movePercent < 0.03 {
		return false, 0
	}

	callValueAtExtreme := BlackScholesCall(priceToCheck, strike, halfTime, RiskFreeRate, impliedVol)
	putValueAtExtreme := BlackScholesPut(priceToCheck, strike, halfTime, RiskFreeRate, impliedVol)
	totalValueAtExtreme := callValueAtExtreme + putValueAtExtreme

	loss := totalValueAtExtreme - initialPremium

	if loss >= stopLossThreshold {
		return true, priceToCheck
	}

	return false, 0
}

// BatchSimulateStraddles runs straddle simulation over historical data
// prices: daily close prices
// highs: daily high prices
// lows: daily low prices
// stopLossMultiplier: e.g., 1.5
func BatchSimulateStraddles(prices, highs, lows []float64, stopLossMultiplier float64) []StraddleResult {
	if len(prices) < RollingWindow+2 {
		return nil
	}

	var results []StraddleResult

	for i := RollingWindow; i < len(prices)-1; i++ {
		historicalPrices := prices[:i+1]
		vol := CalculateHistoricalVolatility(historicalPrices)

		if vol <= 0 {
			continue
		}

		spotEntry := prices[i]
		spotExpiry := prices[i+1]
		dayHigh := highs[i]
		dayLow := lows[i]
		timeToExpiry := DailyExpiryDays / TradingDaysYear

		result := SimulateStraddle(
			spotEntry, vol, timeToExpiry,
			dayHigh, dayLow, spotExpiry,
			stopLossMultiplier,
		)

		results = append(results, result)
	}

	return results
}

// StraddleStats contains aggregate statistics for straddle backtests
type StraddleStats struct {
	TotalTrades     int
	WinningTrades   int
	LosingTrades    int
	StopLossCount   int
	TotalPnL        float64
	AveragePnL      float64
	WinRate         float64
	MaxDrawdown     float64
	SharpeRatio     float64
	ProfitFactor    float64
}

// CalculateStraddleStats computes aggregate statistics from simulation results
func CalculateStraddleStats(results []StraddleResult) StraddleStats {
	if len(results) == 0 {
		return StraddleStats{}
	}

	stats := StraddleStats{
		TotalTrades: len(results),
	}

	var grossProfit, grossLoss float64
	var pnls []float64
	var runningPnL, maxPnL float64

	for _, r := range results {
		pnls = append(pnls, r.PnL)
		stats.TotalPnL += r.PnL

		if r.PnL > 0 {
			stats.WinningTrades++
			grossProfit += r.PnL
		} else {
			stats.LosingTrades++
			grossLoss += math.Abs(r.PnL)
		}

		if r.StopLossHit {
			stats.StopLossCount++
		}

		runningPnL += r.PnL
		if runningPnL > maxPnL {
			maxPnL = runningPnL
		}
		drawdown := maxPnL - runningPnL
		if drawdown > stats.MaxDrawdown {
			stats.MaxDrawdown = drawdown
		}
	}

	stats.AveragePnL = stats.TotalPnL / float64(len(results))
	stats.WinRate = float64(stats.WinningTrades) / float64(len(results))

	if grossLoss > 0 {
		stats.ProfitFactor = grossProfit / grossLoss
	}

	if len(pnls) > 1 {
		var sumSq float64
		for _, pnl := range pnls {
			diff := pnl - stats.AveragePnL
			sumSq += diff * diff
		}
		stdDev := math.Sqrt(sumSq / float64(len(pnls)))
		if stdDev > 0 {
			stats.SharpeRatio = (stats.AveragePnL / stdDev) * math.Sqrt(TradingDaysYear)
		}
	}

	return stats
}
