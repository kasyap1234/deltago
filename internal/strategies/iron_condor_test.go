package strategies

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
	"github.com/kiwhtas/deltago/internal/execution"
	"github.com/kiwhtas/deltago/internal/portfolio"
	"github.com/kiwhtas/deltago/internal/regime"
)

func createTestOptionsCondor(spot float64) []delta.Ticker {
	var options []delta.Ticker
	strikes := []float64{80, 85, 90, 95, 100, 105, 110, 115, 120}
	
	// Calls: High strike = Low Delta. 120 -> 0.1. 100 -> 0.5.
	callDeltas := []string{"0.9", "0.8", "0.7", "0.6", "0.5", "0.4", "0.25", "0.15", "0.1"}
	
	// Puts: Low strike = Low Delta (closer to 0). 80 -> -0.1. 100 -> -0.5.
	putDeltas := []string{"-0.1", "-0.15", "-0.25", "-0.4", "-0.5", "-0.6", "-0.7", "-0.8", "-0.9"}
	
	for i, strike := range strikes {
		// Call
		options = append(options, delta.Ticker{
			ProductID:    int64(1000 + i),
			Symbol:       fmt.Sprintf("CALL_%d", int(strike)),
			ContractType: "call_options",
			StrikePrice:  fmt.Sprintf("%.2f", strike),
			Quotes: delta.Quotes{
				BestBid: fmt.Sprintf("%.2f", 10.0-float64(i)*1.0),
				BestAsk: fmt.Sprintf("%.2f", 10.2-float64(i)*1.0),
			},
			Greeks: delta.Greeks{
				Delta: callDeltas[i],
			},
		})
		
		// Put
		options = append(options, delta.Ticker{
			ProductID:    int64(2000 + i),
			Symbol:       fmt.Sprintf("PUT_%d", int(strike)),
			ContractType: "put_options",
			StrikePrice:  fmt.Sprintf("%.2f", strike),
			Quotes: delta.Quotes{
				BestBid: fmt.Sprintf("%.2f", 2.0+float64(i)*1.0),
				BestAsk: fmt.Sprintf("%.2f", 2.2+float64(i)*1.0),
			},
			Greeks: delta.Greeks{
				Delta: putDeltas[i],
			},
		})
	}
	return options
}

func TestIronCondor_ShouldEnter(t *testing.T) {
	strategy := NewIronCondor(nil, 1, 0.25, 1)
	
	baseInput := Input{
		Regime: &regime.Regime{
			Trend:  regime.TrendSideways,
			Vol:    regime.VolHigh,
			Stress: regime.StressNormal,
			Score:  0.8,
		},
		Snapshot: &MarketSnapshot{
			SpotPrice: 100.0,
			Options:   createTestOptionsCondor(100.0), // Need >= 20 options
		},
		Portfolio: &portfolio.State{},
	}
	
	// Ensure enough options for "insufficient liquidity" check (needs >= 20)
	// createTestOptionsCondor creates 9 strikes * 2 types = 18 options.
	// Need to add more dummy options.
	for i := 0; i < 5; i++ {
		baseInput.Snapshot.Options = append(baseInput.Snapshot.Options, delta.Ticker{ProductID: int64(3000+i)})
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
			msgContains: "sideways + high IV confirmed",
		},
		{
			name: "Wrong Trend",
			setup: func(in *Input) {
				in.Regime.Trend = regime.TrendUp
			},
			shouldEnter: false,
			msgContains: "not in sideways",
		},
		{
			name: "Low Volatility",
			setup: func(in *Input) {
				in.Regime.Vol = regime.VolLow
			},
			shouldEnter: false,
			msgContains: "IV too low",
		},
		{
			name: "Crash Detected",
			setup: func(in *Input) {
				in.Regime.Stress = regime.StressCrash
			},
			shouldEnter: false,
			msgContains: "crash detected",
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

func TestIronCondor_BuildEntryOrders(t *testing.T) {
	strategy := NewIronCondor(nil, 1, 0.25, 1)
	options := createTestOptionsCondor(100.0)
	
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

	if len(order.Legs) != 4 {
		t.Errorf("Expected 4 legs, got %d", len(order.Legs))
	}
	
	// Verify Sequence: Buy Longs first, then Sell Shorts
	if order.Legs[0].Side != execution.Buy || order.Legs[1].Side != execution.Buy {
		t.Error("First legs must be Buys")
	}
	if order.Legs[2].Side != execution.Sell || order.Legs[3].Side != execution.Sell {
		t.Error("Last legs must be Sells")
	}

	// Verify Strikes
	// Short Call: Delta 0.25 -> Strike 110
	// Short Put: Delta -0.25 -> Strike 90
	// Long Call (Wing 1): Next Strike > 110 -> 115
	// Long Put (Wing 1): Next Strike < 90 -> 85
	
	legs := make(map[string]execution.OrderRequest)
	for _, l := range order.Legs {
		legs[l.LegID] = l
	}
	
	if legs["sc"].Symbol != "CALL_110" {
		t.Errorf("Expected sc (short call) CALL_110, got %s", legs["sc"].Symbol)
	}
	if legs["sp"].Symbol != "PUT_90" {
		t.Errorf("Expected sp (short put) PUT_90, got %s", legs["sp"].Symbol)
	}
	if legs["lc"].Symbol != "CALL_115" {
		t.Errorf("Expected lc (long call) CALL_115, got %s", legs["lc"].Symbol)
	}
	if legs["lp"].Symbol != "PUT_85" {
		t.Errorf("Expected lp (long put) PUT_85, got %s", legs["lp"].Symbol)
	}
	
	// Credit Check
	// Short Call 110 Bid: 10.0 - 6*1.0 = 4.0? No.
	// Index for 110 is 6 (0=80, 6=110). Bid = 10 - 6 = 4.0.
	// Short Put 90 Bid: 2.0 + 2*1.0 = 4.0. (Index 2).
	// Long Call 115 Ask: 10.2 - 7*1.0 = 3.2.
	// Long Put 85 Ask: 2.2 + 1*1.0 = 3.2.
	
	// Credit = (4.0 + 4.0) - (3.2 + 3.2) = 8.0 - 6.4 = 1.6.
	
	meta, ok := order.Metadata.(*StrategyPositionMetadata)
	if !ok {
		t.Fatal("Metadata type assertion failed")
	}

	// Credit = (4.0 + 4.0) - (3.2 + 3.2) = 1.6, multiplied by 0.001 (BTC options multiplier)
	multiplier := 0.001
	expectedCredit := 1.6 * multiplier
	if math.Abs(meta.NetPremium-expectedCredit) > 0.0001 {
		t.Errorf("Expected NetPremium %.4f, got %.4f", expectedCredit, meta.NetPremium)
	}
}

func TestIronCondor_ConfirmEntry(t *testing.T) {
	strategy := NewIronCondor(nil, 1, 0.25, 1)
	now := time.Now()

	// Use leg IDs that match the strategy code: "sc", "sp", "lc", "lp"
	result := &execution.MultiLegResult{
		StrategyID:  "condor_1",
		CompletedAt: now,
		FullyFilled: true,
		LegResults: map[string]*execution.OrderState{
			"sc": {
				Request: execution.OrderRequest{Symbol: "CALL_110", Side: execution.Sell, InstrumentID: 100},
				Status:  execution.StatusFilled, FilledQty: 1, AvgFillPrice: 4.0,
			},
			"sp": {
				Request: execution.OrderRequest{Symbol: "PUT_90", Side: execution.Sell, InstrumentID: 101},
				Status:  execution.StatusFilled, FilledQty: 1, AvgFillPrice: 4.0,
			},
			"lc": {
				Request: execution.OrderRequest{Symbol: "CALL_115", Side: execution.Buy, InstrumentID: 102},
				Status:  execution.StatusFilled, FilledQty: 1, AvgFillPrice: 3.2,
			},
			"lp": {
				Request: execution.OrderRequest{Symbol: "PUT_85", Side: execution.Buy, InstrumentID: 103},
				Status:  execution.StatusFilled, FilledQty: 1, AvgFillPrice: 3.2,
			},
		},
	}

	meta := &StrategyPositionMetadata{
		MaxLoss:   8.4,
		MaxProfit: 1.6,
		Legs: []Leg{
			{ID: "sc", Strike: 110},
			{ID: "sp", Strike: 90},
			{ID: "lc", Strike: 115},
			{ID: "lp", Strike: 85},
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

	if len(pos.Legs) != 4 {
		t.Errorf("Expected 4 legs, got %d", len(pos.Legs))
	}

	// Verify Net Premium: (4+4) - (3.2+3.2) = 1.6, multiplied by 0.001 (BTC options multiplier)
	multiplier := 0.001
	expectedCredit := 1.6 * multiplier
	if math.Abs(pos.NetPremium-expectedCredit) > 0.0001 {
		t.Errorf("Expected NetPremium %.4f, got %.4f", expectedCredit, pos.NetPremium)
	}
}

func TestIronCondor_Manage(t *testing.T) {
	strategy := NewIronCondor(nil, 1, 0.25, 1)

	// Use leg IDs that match the strategy code: "sc", "sp", "lc", "lp"
	// All values need to account for the 0.001 multiplier
	multiplier := 0.001
	// Net premium = 1.6 * 0.001 = 0.0016
	strategy.SetPosition(&StrategyPosition{
		StrategyID: "condor_1",
		MaxProfit:  1.6 * multiplier,  // 0.0016
		NetPremium: 1.6 * multiplier,  // 0.0016 (credit)
		Legs: []Leg{
			{ID: "sc", Symbol: "CALL_110", Side: execution.Sell, Qty: 1, EntryPrice: 4.0, Strike: 110, InstrumentID: 100},
			{ID: "sp", Symbol: "PUT_90", Side: execution.Sell, Qty: 1, EntryPrice: 4.0, Strike: 90, InstrumentID: 101},
			{ID: "lc", Symbol: "CALL_115", Side: execution.Buy, Qty: 1, EntryPrice: 3.2, Strike: 115, InstrumentID: 102},
			{ID: "lp", Symbol: "PUT_85", Side: execution.Buy, Qty: 1, EntryPrice: 3.2, Strike: 85, InstrumentID: 103},
		},
	})

	// Scenario: Market stays flat, volatility drops (Perfect).
	// Short Call Value drops to 1.0 (Ask to buy back)
	// Short Put Value drops to 1.0
	// Long Call Value drops to 0.5 (Bid to sell)
	// Long Put Value drops to 0.5
	//
	// Cost to close (with multiplier):
	// Buy Shorts: (1.0 + 1.0) * 0.001 = 0.002
	// Sell Longs: (0.5 + 0.5) * 0.001 = 0.001
	// Net Cost to close: 0.001.
	// PnL = NetPremium (0.0016) - NetCost (0.001) = 0.0006.
	//
	// Target Profit 50% of 0.0016 = 0.0008.
	// 0.0006 < 0.0008. Hold.

	in := Input{
		Regime: &regime.Regime{Trend: regime.TrendSideways, Stress: regime.StressNormal},
		Snapshot: &MarketSnapshot{
			SpotPrice: 100.0,
			Options: []delta.Ticker{
				{ProductID: 100, Symbol: "CALL_110", Quotes: delta.Quotes{BestAsk: "1.0"}},
				{ProductID: 101, Symbol: "PUT_90", Quotes: delta.Quotes{BestAsk: "1.0"}},
				{ProductID: 102, Symbol: "CALL_115", Quotes: delta.Quotes{BestBid: "0.5"}},
				{ProductID: 103, Symbol: "PUT_85", Quotes: delta.Quotes{BestBid: "0.5"}},
			},
		},
	}

	orders, err := strategy.Manage(context.Background(), in)
	if err != nil {
		t.Fatalf("Manage failed: %v", err)
	}
	if orders != nil {
		t.Errorf("Expected hold, got orders")
	}

	// Decrease value more -> Profit hit
	// Shorts -> 0.1 each
	// Longs -> 0.05 each
	// Cost to close: (0.1 + 0.1 - 0.05 - 0.05) * 0.001 = 0.0001.
	// PnL: 0.0016 - 0.0001 = 0.0015. > 0.0008. Close.

	in.Snapshot.Options[0].Quotes.BestAsk = "0.1"
	in.Snapshot.Options[1].Quotes.BestAsk = "0.1"
	in.Snapshot.Options[2].Quotes.BestBid = "0.05"
	in.Snapshot.Options[3].Quotes.BestBid = "0.05"

	orders, err = strategy.Manage(context.Background(), in)
	if err != nil {
		t.Fatalf("Manage failed: %v", err)
	}
	if len(orders) != 4 {
		t.Errorf("Expected 4 close orders, got %d", len(orders))
	}

	// Verify Close Sequence: Buy Shorts First, Sell Longs Last
	if orders[0].Side != execution.Buy || orders[1].Side != execution.Buy {
		t.Error("First close orders must be Buys (covering shorts)")
	}
	if orders[2].Side != execution.Sell || orders[3].Side != execution.Sell {
		t.Error("Last close orders must be Sells (closing longs)")
	}
}
