package strategies

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/kiwhtas/deltago/internal/delta"
	"github.com/kiwhtas/deltago/internal/execution"
	"github.com/kiwhtas/deltago/internal/portfolio"
	"github.com/kiwhtas/deltago/internal/regime"
)

func createTestOptionsProtective(spot float64) []delta.Ticker {
	var options []delta.Ticker
	strikes := []float64{90, 95, 100, 105, 110}
	
	for i, strike := range strikes {
		// Put Deltas
		// 90: -0.15
		// 95: -0.2 (Target)
		// 100: -0.5
		deltaVal := "-0.5"
		if strike == 95 { deltaVal = "-0.2" }
		if strike == 90 { deltaVal = "-0.15" }
		
		options = append(options, delta.Ticker{
			ProductID:    int64(2000 + i),
			Symbol:       fmt.Sprintf("PUT_%d", int(strike)),
			ContractType: "put_options",
			StrikePrice:  fmt.Sprintf("%.2f", strike),
			Quotes: delta.Quotes{
				BestBid: fmt.Sprintf("%.2f", 2.0+float64(i)),
				BestAsk: fmt.Sprintf("%.2f", 2.2+float64(i)),
			},
			Greeks: delta.Greeks{
				Delta: deltaVal,
			},
		})
	}
	return options
}

func TestProtectivePut_ShouldEnter(t *testing.T) {
	strategy := NewProtectivePut(nil, 1, false)
	
	baseInput := Input{
		Regime: &regime.Regime{
			Trend:  regime.TrendDown,
			Vol:    regime.VolHigh,
			Stress: regime.StressNormal,
		},
		Snapshot: &MarketSnapshot{
			SpotPrice: 100.0,
			Options:   createTestOptionsProtective(100.0),
		},
		Portfolio: &portfolio.State{},
	}

	tests := []struct {
		name        string
		setup       func(*Input)
		shouldEnter bool
		msgContains string
	}{
		{
			name:        "Crash Detected",
			setup:       func(in *Input) {
				in.Regime.Stress = regime.StressCrash
			},
			shouldEnter: true,
			msgContains: "crash protection",
		},
		{
			name: "Downtrend High Vol",
			setup: func(in *Input) {
				in.Regime.Trend = regime.TrendDown
				in.Regime.Vol = regime.VolHigh
			},
			shouldEnter: true,
			msgContains: "downtrend with high vol",
		},
		{
			name: "Normal Market (No entry)",
			setup: func(in *Input) {
				in.Regime.Trend = regime.TrendUp
				in.Regime.Stress = regime.StressNormal
			},
			shouldEnter: false,
			msgContains: "no crash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := baseInput // copy
			r := *baseInput.Regime
			in.Regime = &r
			s := *baseInput.Snapshot
			opts := make([]delta.Ticker, len(baseInput.Snapshot.Options))
			copy(opts, baseInput.Snapshot.Options)
			s.Options = opts
			in.Snapshot = &s
			
			tt.setup(&in)

			enter, msg, err := strategy.ShouldEnter(context.Background(), in)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			
			if enter != tt.shouldEnter {
				t.Errorf("Expected ShouldEnter=%v, got %v (msg: %s)", tt.shouldEnter, enter, msg)
			}
		})
	}
}

func TestProtectivePut_BuildEntryOrders(t *testing.T) {
	strategy := NewProtectivePut(nil, 1, false)
	options := createTestOptionsProtective(100.0)
	
	in := Input{
		Snapshot: &MarketSnapshot{
			SpotPrice: 100.0,
			Options:   options,
		},
		Portfolio: &portfolio.State{},
	}

	order, err := strategy.BuildEntryOrders(context.Background(), in)
	if err != nil {
		t.Fatalf("BuildEntryOrders failed: %v", err)
	}

	if order == nil {
		t.Fatal("Order is nil")
	}

	if len(order.Legs) != 1 {
		t.Errorf("Expected 1 leg, got %d", len(order.Legs))
	}

	// Verify Leg: Put Strike 95 (Delta -0.2)
	leg := order.Legs[0]
	if leg.Symbol != "PUT_95" {
		t.Errorf("Expected PUT_95, got %s", leg.Symbol)
	}
	if leg.Side != execution.Buy {
		t.Error("Leg must be Buy")
	}
	
	// Price for Strike 95 (i=1): 2.2 + 1.0 = 3.2
	if leg.Price != 3.2 {
		t.Errorf("Expected price 3.2, got %.2f", leg.Price)
	}
	
	meta, ok := order.Metadata.(*StrategyPositionMetadata)
	if !ok {
		t.Fatal("Metadata type assertion failed")
	}

	// Net premium = -3.2 * 0.001 (BTC options multiplier) = -0.0032
	multiplier := 0.001
	expectedDebit := 3.2 * multiplier
	if math.Abs(meta.NetPremium-(-expectedDebit)) > 0.0001 {
		t.Errorf("Expected NetPremium %.4f, got %.4f", -expectedDebit, meta.NetPremium)
	}
}

func TestProtectivePut_Manage(t *testing.T) {
	strategy := NewProtectivePut(nil, 1, false)

	// Use leg ID that matches the strategy code: "lp"
	// All values need to account for the 0.001 multiplier
	multiplier := 0.001
	// Net premium = -5.0 * 0.001 = -0.005 (paid 5 in raw terms)
	strategy.SetPosition(&StrategyPosition{
		StrategyID: "protect_1",
		NetPremium: -5.0 * multiplier, // -0.005 (debit)
		Legs: []Leg{
			{ID: "lp", Symbol: "PUT_95", Side: execution.Buy, Qty: 1, EntryPrice: 5.0, InstrumentID: 2001},
		},
	})

	// Scenario: Market crashes. Put value 15.0.
	// CurrentValue = 15.0 * 0.001 = 0.015
	// paidPremium = 0.005
	// PnL = 0.015 - 0.005 = 0.01
	// Target Profit: 200% (0.005 * 2 = 0.01).
	// 0.01 >= 0.01. Close.
	
	in := Input{
		Regime: &regime.Regime{Stress: regime.StressCrash},
		Snapshot: &MarketSnapshot{
			SpotPrice: 90.0,
			Options: []delta.Ticker{
				{ProductID: 2001, Quotes: delta.Quotes{BestBid: "15.0"}},
			},
		},
	}
	
	orders, err := strategy.Manage(context.Background(), in)
	if err != nil {
		t.Fatalf("Manage failed: %v", err)
	}
	if len(orders) != 1 {
		t.Errorf("Expected close order (Profit target), got %d", len(orders))
	}
	
	// Scenario: Market normalizes. PnL > 0.
	// Value 6.0. PnL 1.0.
	// Regime Normal. Trend Up.
	// Logic: "Close if regime normalizes and we're still profitable"
	
	in.Regime.Stress = regime.StressNormal
	in.Regime.Trend = regime.TrendUp
	in.Snapshot.Options[0].Quotes.BestBid = "6.0"
	
	orders, err = strategy.Manage(context.Background(), in)
	if err != nil {
		t.Fatalf("Manage failed: %v", err)
	}
	if len(orders) != 1 {
		t.Errorf("Expected close order (Regime Normalized), got %d", len(orders))
	}
}
