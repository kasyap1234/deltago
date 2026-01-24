package strategies

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
	"github.com/kiwhtas/deltago/internal/execution"
	"github.com/kiwhtas/deltago/internal/portfolio"
	"github.com/kiwhtas/deltago/internal/regime"
)

func createTestOptionsBear(spot float64) []delta.Ticker {
	var options []delta.Ticker
	strikes := []float64{90, 95, 100, 105, 110}
	
	// Put Deltas: Lower strike (OTM) -> closer to 0. Higher strike (ITM) -> closer to -1.
	// 90 (OTM): -0.3
	// 95 (OTM): -0.4
	// 100 (ATM): -0.5
	// 105 (ITM): -0.6
	// 110 (ITM): -0.7
	putDeltas := []string{"-0.3", "-0.4", "-0.5", "-0.6", "-0.7"}
	
	for i, strike := range strikes {
		// Call (Dummy values)
		options = append(options, delta.Ticker{
			ProductID:    int64(1000 + i),
			Symbol:       fmt.Sprintf("CALL_%d", int(strike)),
			ContractType: "call_options",
			StrikePrice:  fmt.Sprintf("%.2f", strike),
			Quotes: delta.Quotes{
				BestBid: "1.0", BestAsk: "1.2",
			},
			Greeks: delta.Greeks{
				Delta: "0.5", Gamma: "0.05",
			},
		})
		
		// Put
		// Strike 90 (OTM): Cheap. Bid 2.0 Ask 2.2
		// Strike 100 (ATM): Medium. Bid 5.0 Ask 5.2
		// Strike 110 (ITM): Expensive. Bid 12.0 Ask 12.2
		priceBase := 2.0 + float64(i)*2.5 // 2.0, 4.5, 7.0, 9.5, 12.0
		
		options = append(options, delta.Ticker{
			ProductID:    int64(2000 + i),
			Symbol:       fmt.Sprintf("PUT_%d", int(strike)),
			ContractType: "put_options",
			StrikePrice:  fmt.Sprintf("%.2f", strike),
			Quotes: delta.Quotes{
				BestBid: fmt.Sprintf("%.2f", priceBase),
				BestAsk: fmt.Sprintf("%.2f", priceBase+0.2),
			},
			Greeks: delta.Greeks{
				Delta: putDeltas[i],
				Gamma: "0.05",
				Theta: "-0.1",
				Vega:  "0.2",
			},
		})
	}
	return options
}

func TestBearPutSpread_ShouldEnter(t *testing.T) {
	strategy := NewBearPutSpread(nil, 1)
	
	baseInput := Input{
		Regime: &regime.Regime{
			Trend:  regime.TrendDown,
			Stress: regime.StressNormal,
			Score:  0.8,
		},
		Snapshot: &MarketSnapshot{
			SpotPrice: 100.0,
			Options:   createTestOptionsBear(100.0),
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
			msgContains: "downtrend confirmed",
		},
		{
			name: "Wrong Trend",
			setup: func(in *Input) {
				in.Regime.Trend = regime.TrendUp
			},
			shouldEnter: false,
			msgContains: "not in downtrend",
		},
		{
			name: "Low Confidence",
			setup: func(in *Input) {
				in.Regime.Score = 0.5
			},
			shouldEnter: false,
			msgContains: "confidence too low",
		},
		{
			name: "Low Liquidity",
			setup: func(in *Input) {
				in.Snapshot.Options = in.Snapshot.Options[:5] 
			},
			shouldEnter: false,
			msgContains: "insufficient options liquidity",
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

func TestBearPutSpread_BuildEntryOrders(t *testing.T) {
	strategy := NewBearPutSpread(nil, 1)
	options := createTestOptionsBear(100.0)
	
	in := Input{
		Snapshot: &MarketSnapshot{
			SpotPrice: 100.0,
			Options:   options,
		},
		Portfolio: &portfolio.State{
			Costs: portfolio.TransactionCosts{
				TakerFeeRate: 0.0003,
				MakerFeeRate: -0.0001,
			},
		},
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

	// Verify Legs
	var longLeg, shortLeg execution.OrderRequest
	for _, leg := range order.Legs {
		if leg.Side == execution.Buy {
			longLeg = leg
		} else {
			shortLeg = leg
		}
	}

	if longLeg.Side != execution.Buy {
		t.Error("Missing long leg")
	}
	if shortLeg.Side != execution.Sell {
		t.Error("Missing short leg")
	}

	// Strategy: 
	// Long Delta 0.5 -> Strike 100 (Delta -0.5). Price ~7.0/7.2
	// Short Delta 0.3 -> Strike 90 (Delta -0.3). Price ~2.0/2.2
	// Check strikes
	// Long leg (Buy): Should be Strike 100. Ask 7.2.
	// Short leg (Sell): Should be Strike 90. Bid 2.0.
	
	// Wait, code says:
	// longPut := FindOptionByDelta(..., -s.LongDelta) -> -0.5. Strike 100.
	// shortPut := FindOptionByDelta(..., -s.ShortDelta) -> -0.3. Strike 90.
	// Ensure short < long. 90 < 100. OK.
	
	if longLeg.Symbol != "PUT_100" {
		t.Errorf("Expected long leg PUT_100, got %s", longLeg.Symbol)
	}
	if shortLeg.Symbol != "PUT_90" {
		t.Errorf("Expected short leg PUT_90, got %s", shortLeg.Symbol)
	}
	
	if longLeg.Price != 7.2 {
		t.Errorf("Expected long leg price 7.2, got %.2f", longLeg.Price)
	}
	if shortLeg.Price != 2.0 {
		t.Errorf("Expected short leg price 2.0, got %.2f", shortLeg.Price)
	}
	
	meta, ok := order.Metadata.(*StrategyPositionMetadata)
	if !ok {
		t.Fatal("Metadata type assertion failed")
	}
	
	// Net debit = 7.2 - 2.0 = 5.2, multiplied by 0.001 (BTC options multiplier)
	multiplier := 0.001
	expectedDebit := (7.2 - 2.0) * multiplier // 0.0052
	if abs(meta.NetPremium-(-expectedDebit)) > 0.0001 {
		t.Errorf("Expected NetPremium %.4f, got %.4f", -expectedDebit, meta.NetPremium)
	}
}

func TestBearPutSpread_ConfirmEntry(t *testing.T) {
	strategy := NewBearPutSpread(nil, 1)
	
	now := time.Now()
	result := &execution.MultiLegResult{
		StrategyID:  "test_strat",
		CompletedAt: now,
		FullyFilled: true,
		LegResults: map[string]*execution.OrderState{
			"lp": {
				Request: execution.OrderRequest{
					Symbol:       "PUT_100",
					Side:         execution.Buy,
					InstrumentID: 2002,
				},
				Status:       execution.StatusFilled,
				FilledQty:    1,
				AvgFillPrice: 7.2,
			},
			"sp": {
				Request: execution.OrderRequest{
					Symbol:       "PUT_90",
					Side:         execution.Sell,
					InstrumentID: 2000,
				},
				Status:       execution.StatusFilled,
				FilledQty:    1,
				AvgFillPrice: 2.0,
			},
		},
	}

	meta := &StrategyPositionMetadata{
		MaxLoss:   5.2,
		MaxProfit: 4.8, // Width 10 - Debit 5.2 = 4.8
		Legs: []Leg{
			{ID: "lp", Strike: 100, OptionType: "put"},
			{ID: "sp", Strike: 90, OptionType: "put"},
		},
	}
	
	err := strategy.ConfirmEntry(context.Background(), result, meta)
	if err != nil {
		t.Fatalf("ConfirmEntry failed: %v", err)
	}
	
	pos := strategy.GetPosition()
	if pos == nil {
		t.Fatal("Position not set")
	}
	
	// Expected net premium = -(7.2 - 2.0) * 0.001 (BTC options multiplier)
	multiplier := 0.001
	expectedNet := -(7.2 - 2.0) * multiplier
	if abs(pos.NetPremium-expectedNet) > 0.0001 {
		t.Errorf("Expected NetPremium %.4f, got %.4f", expectedNet, pos.NetPremium)
	}
}

func TestBearPutSpread_Manage(t *testing.T) {
	strategy := NewBearPutSpread(nil, 1)

	// Use leg IDs that match the strategy code: "lp" and "sp"
	// Values need to be in real terms (with multiplier already applied)
	// Entry: long @ 7.0, short @ 2.0, net debit = 5.0 * 0.001 = 0.005
	multiplier := 0.001
	strategy.SetPosition(&StrategyPosition{
		StrategyID: "test_strat",
		MaxProfit:  5.0 * multiplier,  // 0.005
		NetPremium: -5.0 * multiplier, // -0.005 (debit)
		Legs: []Leg{
			{
				ID: "lp", InstrumentID: 2002, Symbol: "PUT_100", Side: execution.Buy,
				Qty: 1, EntryPrice: 7.0,
			},
			{
				ID: "sp", InstrumentID: 2000, Symbol: "PUT_90", Side: execution.Sell,
				Qty: 1, EntryPrice: 2.0,
			},
		},
	})

	// Scenario: Market moves down (favorable).
	// Long Put (100) value increases -> 12.0
	// Short Put (90) value increases -> 4.0
	// PnL (with multiplier):
	// Long: (12.0 - 7.0) * 1 * 0.001 = +0.005
	// Short: (2.0 - 4.0) * 1 * 0.001 = -0.002
	// Total PnL: 0.003
	// MaxProfit = 0.005, TakeProfitPct = 0.5, threshold = 0.0025
	// 0.003 > 0.0025 -> should trigger close

	in := Input{
		Regime: &regime.Regime{Trend: regime.TrendDown},
		Snapshot: &MarketSnapshot{
			Options: []delta.Ticker{
				{
					ProductID: 2002,
					Quotes:    delta.Quotes{BestBid: "12.0", BestAsk: "12.2"},
				},
				{
					ProductID: 2000,
					Quotes:    delta.Quotes{BestBid: "4.0", BestAsk: "4.2"},
				},
			},
		},
	}

	orders, err := strategy.Manage(context.Background(), in)
	if err != nil {
		t.Fatalf("Manage failed: %v", err)
	}

	if len(orders) != 2 {
		t.Errorf("Expected 2 closing orders, got %d", len(orders))
	}
}
