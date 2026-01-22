package risk

import (
	"errors"
	"testing"

	"github.com/kiwhtas/deltago/internal/delta"
)

type MockStrategy struct {
	shouldStop    bool
	currentLoss   float64
	checkError    error
	closeError    error
	closed        bool
	activePos     interface{}
}

func (m *MockStrategy) CheckStopLoss() (bool, float64, error) {
	return m.shouldStop, m.currentLoss, m.checkError
}

func (m *MockStrategy) ClosePosition() error {
	m.closed = true
	return m.closeError
}

func (m *MockStrategy) GetActivePosition() interface{} {
	return m.activePos
}

func TestManager_CheckStopLosses(t *testing.T) {
	// Setup dummy client
	client := delta.NewClient("http://dummy", "key", "secret")
	manager := NewManager(client, "ws://dummy", 1.5, 1000)

	// Strategy 1: Active, Stop Loss Triggered
	strat1 := &MockStrategy{
		activePos:   "pos1",
		shouldStop:  true,
		currentLoss: 200.0,
	}
	manager.AddStrategy(strat1)

	// Strategy 2: Active, Safe
	strat2 := &MockStrategy{
		activePos:   "pos2",
		shouldStop:  false,
		currentLoss: 50.0,
	}
	manager.AddStrategy(strat2)

	// Strategy 3: No position
	strat3 := &MockStrategy{
		activePos: nil,
	}
	manager.AddStrategy(strat3)

	// Run check
	manager.CheckAllStopLosses()

	// Verify Strat 1 closed
	if !strat1.closed {
		t.Error("Strategy 1 should have been closed")
	}
	
	// Verify Strat 2 not closed
	if strat2.closed {
		t.Error("Strategy 2 should not have been closed")
	}

	// Verify Daily PnL updated (loss subtracted)
	// Loss is 200. DailyPnL starts at 0. Should become -200.
	if manager.GetDailyPnL() != -200.0 {
		t.Errorf("Expected DailyPnL -200.0, got %.2f", manager.GetDailyPnL())
	}
}

func TestManager_CheckDailyLoss(t *testing.T) {
	client := delta.NewClient("http://dummy", "key", "secret")
	manager := NewManager(client, "ws://dummy", 1.5, 1000) // Max daily loss 1000

	strat := &MockStrategy{
		activePos: "pos1",
	}
	manager.AddStrategy(strat)

	// Case 1: Loss within limit
	manager.dailyPnL = -900
	manager.CheckDailyLoss()
	if strat.closed {
		t.Error("Strategy should not be closed yet (loss 900 < 1000)")
	}

	// Case 2: Loss exceeds limit
	manager.dailyPnL = -1001
	manager.CheckDailyLoss()
	if !strat.closed {
		t.Error("Strategy should be closed (loss 1001 > 1000)")
	}
}

func TestManager_ErrorHandling(t *testing.T) {
	client := delta.NewClient("http://dummy", "key", "secret")
	manager := NewManager(client, "ws://dummy", 1.5, 1000)

	// Strategy that errors on check
	strat := &MockStrategy{
		activePos:  "pos1",
		checkError: errors.New("check failed"),
	}
	manager.AddStrategy(strat)

	// Should not panic or close
	manager.CheckAllStopLosses()
	if strat.closed {
		t.Error("Strategy should not be closed on check error")
	}

	// Strategy that errors on close
	strat2 := &MockStrategy{
		activePos:   "pos2",
		shouldStop:  true,
		currentLoss: 100,
		closeError:  errors.New("close failed"),
	}
	manager.AddStrategy(strat2)

	// Should not panic
	manager.CheckAllStopLosses()
	if !strat2.closed {
		t.Error("ClosePosition should have been called despite error return")
	}
	// Note: If ClosePosition returns error, we might NOT deduct PnL?
	// Implementation:
	/*
		if err := s.ClosePosition(); err != nil {
			log.Printf("Error closing position: %v", err)
		} else {
			m.dailyPnL -= currentLoss
		}
	*/
	// So DailyPnL should NOT be updated if close fails
	if manager.GetDailyPnL() != 0 {
		t.Errorf("DailyPnL should be 0 if close failed, got %.2f", manager.GetDailyPnL())
	}
}
