package backtest

import (
	"math"
	"testing"
)

func TestBlackScholes(t *testing.T) {
	// Standard test case
	S := 100.0
	K := 100.0
	T := 1.0
	r := 0.05
	sigma := 0.2

	call := BlackScholesCall(S, K, T, r, sigma)
	put := BlackScholesPut(S, K, T, r, sigma)

	// Call price should be ~10.45
	if math.Abs(call-10.45) > 0.1 {
		t.Errorf("BlackScholesCall expected ~10.45, got %.2f", call)
	}

	// Put-Call Parity: C - P = S - K*exp(-rT)
	// 10.45 - P = 100 - 100*exp(-0.05)
	// 10.45 - P = 100 - 95.12 = 4.88
	// P = 10.45 - 4.88 = 5.57
	if math.Abs(put-5.57) > 0.1 {
		t.Errorf("BlackScholesPut expected ~5.57, got %.2f", put)
	}
}

func TestCalculateHistoricalVolatility(t *testing.T) {
	// Create a stable price series (0 vol)
	stablePrices := make([]float64, 50)
	for i := range stablePrices {
		stablePrices[i] = 100.0
	}
	vol := CalculateHistoricalVolatility(stablePrices)
	if vol != 0 {
		t.Errorf("Expected 0 vol for stable prices, got %.4f", vol)
	}

	// Create a volatile series
	// 100, 101, 100, 101...
	volatilePrices := make([]float64, 50)
	for i := range volatilePrices {
		if i%2 == 0 {
			volatilePrices[i] = 100.0
		} else {
			volatilePrices[i] = 101.0 // 1% move
		}
	}
	vol = CalculateHistoricalVolatility(volatilePrices)
	// 1% daily move ~ 16% annualized (1 * sqrt(252)? Actually sqrt(365) here ~ 19%)
	if vol < 0.1 || vol > 0.3 {
		t.Errorf("Expected vol between 0.1 and 0.3, got %.4f", vol)
	}
}

func TestSimulateStraddle(t *testing.T) {
	spotEntry := 10000.0
	vol := 0.5 // 50% vol
	timeToExpiry := 1.0 / 365.0
	dayHigh := 10100.0
	dayLow := 9900.0
	spotExpiry := 10000.0 // No move
	stopLossMult := 1.5

	// Case 1: No Move (Max Profit)
	result := SimulateStraddle(spotEntry, vol, timeToExpiry, dayHigh, dayLow, spotExpiry, stopLossMult)
	
	if result.TotalPremium <= 0 {
		t.Error("Premium should be positive")
	}
	if result.TotalIntrinsic != 0 {
		t.Errorf("Expected 0 intrinsic at strike, got %.2f", result.TotalIntrinsic)
	}
	if result.PnL <= 0 {
		t.Errorf("Expected positive PnL (collected premium), got %.2f", result.PnL)
	}

	// Case 2: Big Move (Loss)
	spotExpiry = 11000.0 // 10% move up
	result = SimulateStraddle(spotEntry, vol, timeToExpiry, dayHigh, dayLow, spotExpiry, stopLossMult)
	
	expectedIntrinsic := 1000.0 // 11000 - 10000
	if math.Abs(result.TotalIntrinsic - expectedIntrinsic) > 1.0 {
		t.Errorf("Expected intrinsic %.2f, got %.2f", expectedIntrinsic, result.TotalIntrinsic)
	}
	if result.PnL >= 0 {
		t.Errorf("Expected negative PnL for big move, got %.2f", result.PnL)
	}

	// Case 3: Stop Loss Hit
	dayHigh = 12000.0 // Intraday spike
	// Should trigger stop loss
	result = SimulateStraddle(spotEntry, vol, timeToExpiry, dayHigh, dayLow, spotExpiry, stopLossMult)
	
	if !result.StopLossHit {
		t.Error("Stop loss should have been hit")
	}
	// PnL should be capped at -StopLossThreshold - costs
	expectedLoss := -result.TotalPremium * stopLossMult
	// Allow for costs diff
	if result.GrossPnL > expectedLoss + 1.0 { // GrossPnL should be approx -Threshold
		t.Errorf("GrossPnL %.2f should be close to stop loss limit %.2f", result.GrossPnL, expectedLoss)
	}
}

func TestSimulateIronCondor(t *testing.T) {
	spotEntry := 10000.0
	vol := 0.5
	timeToExpiry := 1.0 / 365.0
	spotExpiry := 10000.0
	shortDelta := 0.25
	wingWidth := 500.0
	strikeStep := 100.0

	// Case 1: No Move (Max Profit)
	result := SimulateIronCondor(spotEntry, vol, timeToExpiry, spotExpiry, shortDelta, wingWidth, strikeStep)
	
	if result.NetCredit <= 0 {
		t.Error("Net credit should be positive")
	}
	if result.GrossPnL != result.NetCredit {
		t.Errorf("Expected GrossPnL = NetCredit (%.2f), got %.2f", result.NetCredit, result.GrossPnL)
	}

	// Case 2: Breach Short Call but not Long Call (Partial Loss)
	// Short Strike is roughly where Delta 0.25 is.
	// 1 day, 50% vol.
	// FindStrikeByDelta should find it.
	// Let's assume strike found is ~10200.
	
	// Force a breach
	spotExpiry = result.ShortCallStrike + 100.0 // 100 points ITM on short call
	
	resultBreach := SimulateIronCondor(spotEntry, vol, timeToExpiry, spotExpiry, shortDelta, wingWidth, strikeStep)
	
	if !resultBreach.BreachedCall {
		t.Error("Should have breached call")
	}
	
	// Loss on call spread: (Expiry - Short) = 100.
	// PnL = Credit - 100.
	expectedPnL := resultBreach.NetCredit - 100.0
	if math.Abs(resultBreach.GrossPnL - expectedPnL) > 1.0 {
		t.Errorf("Expected GrossPnL %.2f, got %.2f", expectedPnL, resultBreach.GrossPnL)
	}

	// Case 3: Max Loss (Breach Long Wing)
	spotExpiry = result.ShortCallStrike + wingWidth + 100.0 // Past long wing
	
	resultMaxLoss := SimulateIronCondor(spotEntry, vol, timeToExpiry, spotExpiry, shortDelta, wingWidth, strikeStep)
	
	// Max Loss = WingWidth - Credit
	expectedMaxLossPnL := -(wingWidth - resultMaxLoss.NetCredit)
	
	if math.Abs(resultMaxLoss.GrossPnL - expectedMaxLossPnL) > 1.0 {
		t.Errorf("Expected Max Loss PnL %.2f, got %.2f", expectedMaxLossPnL, resultMaxLoss.GrossPnL)
	}
}
