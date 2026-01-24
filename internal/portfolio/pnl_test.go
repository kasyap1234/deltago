package portfolio

import (
	"testing"
	"time"
)

func TestCalculateStrategyPnL(t *testing.T) {
	s := NewState(10000, 1000)
	
	strategyID := "test_strategy"
	entries := []LegEntry{
		{
			InstrumentID: 1,
			Symbol:       "BTC-CALL",
			Side:         "buy",
			Qty:          2,
			EntryPrice:   100.0,
			EntryTime:    time.Now(),
		},
		{
			InstrumentID: 2,
			Symbol:       "BTC-PUT",
			Side:         "sell",
			Qty:          5,
			EntryPrice:   50.0,
			EntryTime:    time.Now(),
		},
	}
	
	s.RecordStrategyEntry(strategyID, entries)
	
	// Simulate closing
	closeResults := map[int64]float64{
		1: 120.0, // long leg profit: (120-100)*2 = 40
		2: 40.0,  // short leg profit: (50-40)*5 = 50
	}
	
	pnl := s.CalculateStrategyPnL(strategyID, closeResults)
	
	// Expected PnL = 90.0 * 0.001 (BTC options multiplier) = 0.09
	multiplier := 0.001
	expectedPnL := 90.0 * multiplier
	if abs(pnl-expectedPnL) > 0.0001 {
		t.Errorf("Expected PnL %.4f, got %.4f", expectedPnL, pnl)
	}
	
	// Check if entry records were cleaned up
	if _, ok := s.StrategyEntries[strategyID]; ok {
		t.Errorf("Strategy entry record should have been deleted after PnL calculation")
	}
}

func TestDailyLossLimit(t *testing.T) {
	s := NewState(10000, 100) // Max loss 100
	
	s.RecordTrade(-60)
	if s.TradingHalted {
		t.Errorf("Trading should NOT be halted after 60 loss (limit 100)")
	}
	
	s.RecordTrade(-50)
	if !s.TradingHalted {
		t.Errorf("Trading SHOULD be halted after 110 total loss (limit 100)")
	}
	
	if s.HaltReason != "max daily loss exceeded" {
		t.Errorf("Unexpected halt reason: %s", s.HaltReason)
	}
}
