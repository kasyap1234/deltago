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

func createTestOptionsStraddle(spot float64) []delta.Ticker {
	var options []delta.Ticker
	strikes := []float64{90, 95, 100, 105, 110}
	
	// Call Deltas
	callDeltas := []string{"0.7", "0.6", "0.5", "0.4", "0.3"}
	
	for i, strike := range strikes {
		// Call
		options = append(options, delta.Ticker{
			ProductID:    int64(1000 + i),
			Symbol:       fmt.Sprintf("CALL_%d", int(strike)),
			ContractType: "call_options",
			StrikePrice:  fmt.Sprintf("%.2f", strike),
			Quotes: delta.Quotes{
				BestBid: fmt.Sprintf("%.2f", 5.0),
				BestAsk: fmt.Sprintf("%.2f", 5.2),
			},
			Greeks: delta.Greeks{
				Delta: callDeltas[i],
			},
		})
		
		// Put
		// Strike 100 (ATM) -> Delta -0.5
		putDelta := "-0.5"
		if strike < 100 { putDelta = "-0.3" }
		if strike > 100 { putDelta = "-0.7" }
		
		options = append(options, delta.Ticker{
			ProductID:    int64(2000 + i),
			Symbol:       fmt.Sprintf("PUT_%d", int(strike)),
			ContractType: "put_options",
			StrikePrice:  fmt.Sprintf("%.2f", strike),
			Quotes: delta.Quotes{
				BestBid: fmt.Sprintf("%.2f", 5.0),
				BestAsk: fmt.Sprintf("%.2f", 5.2),
			},
			Greeks: delta.Greeks{
				Delta: putDelta,
			},
		})
	}
	return options
}

func TestLongStraddle_ShouldEnter(t *testing.T) {
	strategy := NewLongStraddle(nil, 1)
	
	baseInput := Input{
		Regime: &regime.Regime{
			Trend:  regime.TrendSideways,
			Vol:    regime.VolLow,
			Stress: regime.StressNormal,
			Score:  0.8,
		},
		Snapshot: &MarketSnapshot{
			SpotPrice: 100.0,
			Options:   createTestOptionsStraddle(100.0),
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
			name:        "Valid Entry",
			setup:       func(in *Input) {},
			shouldEnter: true,
			msgContains: "low IV",
		},
		{
			name: "High Volatility",
			setup: func(in *Input) {
				in.Regime.Vol = regime.VolHigh
			},
			shouldEnter: false,
			msgContains: "IV not low enough",
		},
		{
			name: "Low Confidence",
			setup: func(in *Input) {
				in.Regime.Score = 0.4
			},
			shouldEnter: false,
			msgContains: "confidence too low",
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

func TestLongStraddle_BuildEntryOrders(t *testing.T) {
	strategy := NewLongStraddle(nil, 1)
	options := createTestOptionsStraddle(100.0)
	
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

	if len(order.Legs) != 2 {
		t.Errorf("Expected 2 legs, got %d", len(order.Legs))
	}

	// Verify Legs: Both Long, ATM Strike 100
	for _, leg := range order.Legs {
		if leg.Side != execution.Buy {
			t.Error("Leg must be Buy")
		}
		// Strike 100 corresponds to price 5.2
		if leg.Price != 5.2 {
			t.Errorf("Expected price 5.2, got %.2f", leg.Price)
		}
		if leg.Symbol != "CALL_100" && leg.Symbol != "PUT_100" {
			t.Errorf("Unexpected symbol %s", leg.Symbol)
		}
	}
	
	meta, ok := order.Metadata.(*StrategyPositionMetadata)
	if !ok {
		t.Fatal("Metadata type assertion failed")
	}

	// Total debit: (5.2 + 5.2) * 0.001 (BTC options multiplier) = 0.0104
	multiplier := 0.001
	expectedDebit := 10.4 * multiplier
	if math.Abs(meta.NetPremium-(-expectedDebit)) > 0.0001 {
		t.Errorf("Expected NetPremium %.4f, got %.4f", -expectedDebit, meta.NetPremium)
	}
}

func TestLongStraddle_Manage(t *testing.T) {
	strategy := NewLongStraddle(nil, 1)

	// Use leg IDs that match the strategy code: "lc" and "lp"
	// All values need to account for the 0.001 multiplier
	multiplier := 0.001
	// Net premium = -10.0 * 0.001 = -0.01 (paid 10 in raw terms)
	strategy.SetPosition(&StrategyPosition{
		StrategyID: "straddle_1",
		NetPremium: -10.0 * multiplier, // -0.01 (debit)
		Legs: []Leg{
			{ID: "lc", Symbol: "CALL_100", Side: execution.Buy, Qty: 1, EntryPrice: 5.0, InstrumentID: 100},
			{ID: "lp", Symbol: "PUT_100", Side: execution.Buy, Qty: 1, EntryPrice: 5.0, InstrumentID: 101},
		},
	})

	// Scenario 1: Price unchanged, Vol increases (Good). Value 12.0.
	// CurrentValue = (6+6) * 0.001 = 0.012
	// totalPaid = 0.01
	// PnL = 0.012 - 0.01 = 0.002 > 0.
	// VolHigh -> Close (rule: close if IV high and PnL > 0).

	in := Input{
		Regime: &regime.Regime{Trend: regime.TrendSideways, Vol: regime.VolHigh},
		Snapshot: &MarketSnapshot{
			SpotPrice: 100.0,
			Options: []delta.Ticker{
				{ProductID: 100, Quotes: delta.Quotes{BestBid: "6.0"}},
				{ProductID: 101, Quotes: delta.Quotes{BestBid: "6.0"}},
			},
		},
	}

	orders, err := strategy.Manage(context.Background(), in)
	if err != nil {
		t.Fatalf("Manage failed: %v", err)
	}
	if len(orders) != 2 {
		t.Errorf("Expected 2 close orders (VolHigh), got %d", len(orders))
	}

	// Scenario 2: Vol Low, Price moves significantly.
	// Call -> 15. Put -> 1.
	// CurrentValue = 16 * 0.001 = 0.016
	// PnL = 0.016 - 0.01 = 0.006.
	// Trend emerging logic: if trend != Sideways and PnL > totalPaid * 0.3 = 0.003.
	// 0.006 > 0.003. Close.

	in.Regime.Vol = regime.VolLow
	in.Regime.Trend = regime.TrendUp
	in.Snapshot.Options[0].Quotes.BestBid = "15.0"
	in.Snapshot.Options[1].Quotes.BestBid = "1.0"

	orders, err = strategy.Manage(context.Background(), in)
	if err != nil {
		t.Fatalf("Manage failed: %v", err)
	}
	if len(orders) != 2 {
		t.Errorf("Expected 2 close orders (Trend Emerging), got %d", len(orders))
	}
}
