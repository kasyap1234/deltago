package backtest

import "math"

// IronCondorResult contains the outcome of an iron condor simulation
type IronCondorResult struct {
	SpotEntry       float64 // Spot price at entry
	SpotExpiry      float64 // Spot price at expiry
	ShortCallStrike float64 // Short call strike (~0.25 delta)
	LongCallStrike  float64 // Long call strike (protection)
	ShortPutStrike  float64 // Short put strike (~-0.25 delta)
	LongPutStrike   float64 // Long put strike (protection)
	NetCredit       float64 // Net premium received
	MaxLoss         float64 // Maximum possible loss
	GrossPnL        float64 // P&L before costs
	TradingCosts    float64 // Fees + slippage
	PnL             float64 // Actual P&L at expiry (after costs)
	MarginRequired  float64 // Margin required (max loss)
	ReturnOnMargin  float64 // Return as % of margin
	BreachedCall    bool    // Whether price breached call spread
	BreachedPut     bool    // Whether price breached put spread
}

// SimulateIronCondor simulates an iron condor position
// spotEntry: current spot price
// volatility: annualized volatility
// timeToExpiry: time to expiry in years
// spotExpiry: spot price at expiry
// shortDelta: target delta for short options (e.g., 0.25)
// wingWidth: distance between short and long strikes (e.g., 500 for $500)
// strikeStep: minimum strike increment (e.g., 100 for $100 steps)
func SimulateIronCondor(spotEntry, volatility, timeToExpiry, spotExpiry, shortDelta, wingWidth, strikeStep float64) IronCondorResult {
	// Use implied vol for more realistic premiums
	impliedVol := volatility * IVMultiplier
	
	shortCallStrike := FindStrikeByDelta(spotEntry, timeToExpiry, RiskFreeRate, impliedVol, shortDelta, true, strikeStep)
	longCallStrike := shortCallStrike + wingWidth

	shortPutStrike := FindStrikeByDelta(spotEntry, timeToExpiry, RiskFreeRate, impliedVol, -shortDelta, false, strikeStep)
	longPutStrike := shortPutStrike - wingWidth

	shortCallPremium := BlackScholesCall(spotEntry, shortCallStrike, timeToExpiry, RiskFreeRate, impliedVol)
	longCallPremium := BlackScholesCall(spotEntry, longCallStrike, timeToExpiry, RiskFreeRate, impliedVol)
	shortPutPremium := BlackScholesPut(spotEntry, shortPutStrike, timeToExpiry, RiskFreeRate, impliedVol)
	longPutPremium := BlackScholesPut(spotEntry, longPutStrike, timeToExpiry, RiskFreeRate, impliedVol)

	netCredit := (shortCallPremium - longCallPremium) + (shortPutPremium - longPutPremium)

	maxLoss := wingWidth - netCredit
	if maxLoss < 0 {
		maxLoss = 0
	}
	
	// Margin required is the max loss (defined risk)
	marginRequired := maxLoss
	if marginRequired < 100 {
		marginRequired = 100 // Minimum margin
	}
	
	// Trading costs: 4 options * (entry + exit) * (fee + slippage)
	totalPremiums := shortCallPremium + longCallPremium + shortPutPremium + longPutPremium
	tradingCosts := 4 * totalPremiums * (MakerFee + SlippagePct) * 2

	result := IronCondorResult{
		SpotEntry:       spotEntry,
		SpotExpiry:      spotExpiry,
		ShortCallStrike: shortCallStrike,
		LongCallStrike:  longCallStrike,
		ShortPutStrike:  shortPutStrike,
		LongPutStrike:   longPutStrike,
		NetCredit:       netCredit,
		MaxLoss:         maxLoss,
		MarginRequired:  marginRequired,
		TradingCosts:    tradingCosts,
	}

	grossPnL := calculateCondorPnL(spotExpiry, shortCallStrike, longCallStrike, shortPutStrike, longPutStrike, netCredit)
	result.GrossPnL = grossPnL
	result.PnL = grossPnL - tradingCosts
	result.ReturnOnMargin = (result.PnL / marginRequired) * 100

	result.BreachedCall = spotExpiry > shortCallStrike
	result.BreachedPut = spotExpiry < shortPutStrike

	return result
}

// calculateCondorPnL calculates the P&L of an iron condor at expiry
func calculateCondorPnL(spotExpiry, shortCallStrike, longCallStrike, shortPutStrike, longPutStrike, netCredit float64) float64 {
	shortCallValue := math.Max(spotExpiry-shortCallStrike, 0)
	longCallValue := math.Max(spotExpiry-longCallStrike, 0)
	shortPutValue := math.Max(shortPutStrike-spotExpiry, 0)
	longPutValue := math.Max(longPutStrike-spotExpiry, 0)

	callSpreadLoss := shortCallValue - longCallValue
	putSpreadLoss := shortPutValue - longPutValue

	totalLoss := callSpreadLoss + putSpreadLoss
	pnl := netCredit - totalLoss

	return pnl
}

// BatchSimulateCondors runs iron condor simulation over historical data
// prices: daily close prices
// shortDelta: target delta for short options (e.g., 0.25)
// wingWidth: distance between short and long strikes
// strikeStep: minimum strike increment
func BatchSimulateCondors(prices []float64, shortDelta, wingWidth, strikeStep float64) []IronCondorResult {
	if len(prices) < RollingWindow+2 {
		return nil
	}

	var results []IronCondorResult

	for i := RollingWindow; i < len(prices)-1; i++ {
		historicalPrices := prices[:i+1]
		vol := CalculateHistoricalVolatility(historicalPrices)

		if vol <= 0 {
			continue
		}

		spotEntry := prices[i]
		spotExpiry := prices[i+1]
		timeToExpiry := DailyExpiryDays / TradingDaysYear

		result := SimulateIronCondor(
			spotEntry, vol, timeToExpiry, spotExpiry,
			shortDelta, wingWidth, strikeStep,
		)

		results = append(results, result)
	}

	return results
}

// CondorStats contains aggregate statistics for iron condor backtests
type CondorStats struct {
	TotalTrades      int
	WinningTrades    int
	LosingTrades     int
	CallBreachCount  int
	PutBreachCount   int
	TotalPnL         float64
	AveragePnL       float64
	WinRate          float64
	MaxDrawdown      float64
	SharpeRatio      float64
	ProfitFactor     float64
	AverageNetCredit float64
	AverageMaxLoss   float64
}

// CalculateCondorStats computes aggregate statistics from simulation results
func CalculateCondorStats(results []IronCondorResult) CondorStats {
	if len(results) == 0 {
		return CondorStats{}
	}

	stats := CondorStats{
		TotalTrades: len(results),
	}

	var grossProfit, grossLoss float64
	var pnls []float64
	var runningPnL, maxPnL float64
	var totalNetCredit, totalMaxLoss float64

	for _, r := range results {
		pnls = append(pnls, r.PnL)
		stats.TotalPnL += r.PnL
		totalNetCredit += r.NetCredit
		totalMaxLoss += r.MaxLoss

		if r.PnL > 0 {
			stats.WinningTrades++
			grossProfit += r.PnL
		} else {
			stats.LosingTrades++
			grossLoss += math.Abs(r.PnL)
		}

		if r.BreachedCall {
			stats.CallBreachCount++
		}
		if r.BreachedPut {
			stats.PutBreachCount++
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

	n := float64(len(results))
	stats.AveragePnL = stats.TotalPnL / n
	stats.WinRate = float64(stats.WinningTrades) / n
	stats.AverageNetCredit = totalNetCredit / n
	stats.AverageMaxLoss = totalMaxLoss / n

	if grossLoss > 0 {
		stats.ProfitFactor = grossProfit / grossLoss
	}

	if len(pnls) > 1 {
		var sumSq float64
		for _, pnl := range pnls {
			diff := pnl - stats.AveragePnL
			sumSq += diff * diff
		}
		stdDev := math.Sqrt(sumSq / n)
		if stdDev > 0 {
			stats.SharpeRatio = (stats.AveragePnL / stdDev) * math.Sqrt(TradingDaysYear)
		}
	}

	return stats
}
