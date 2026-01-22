package strategies

import (
	"context"
	"fmt"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
	"github.com/kiwhtas/deltago/internal/execution"
	"github.com/kiwhtas/deltago/internal/regime"
)

// IronCondor implements an iron condor strategy
// Sell OTM call + put, buy further OTM wings for protection
// Best for: Sideways + High volatility (sell premium, defined risk)
type IronCondor struct {
	BaseStrategy

	ShortDelta float64 // target delta for short strikes (e.g., 0.25)
	WingWidth  int     // number of strikes for wings
}

func NewIronCondor(client *delta.Client, positionSize int, shortDelta float64, wingWidth int) *IronCondor {
	return &IronCondor{
		BaseStrategy: BaseStrategy{
			id:                 "ic",
			name:               "Iron Condor",
			client:             client,
			PositionSize:       positionSize,
			StopLossMultiplier: 1.5, // exit at 1.5x premium collected
			TakeProfitPct:      0.5, // take profit at 50% of max profit
			MaxDTE:             7,
		},
		ShortDelta: shortDelta,
		WingWidth:  wingWidth,
	}
}

func (s *IronCondor) SuitableRegimes() []regime.TrendState {
	return []regime.TrendState{regime.TrendSideways}
}

func (s *IronCondor) PreferredVol() regime.VolState {
	return regime.VolHigh // best to sell when IV is high
}

func (s *IronCondor) ShouldEnter(ctx context.Context, in Input) (bool, string, error) {
	if s.HasPosition() {
		return false, "already in position", nil
	}

	if in.Regime.Trend != regime.TrendSideways {
		return false, "not in sideways market", nil
	}

	if in.Regime.Stress == regime.StressCrash {
		return false, "crash detected - avoid short premium", nil
	}

	// Prefer high IV environment for selling premium
	if in.Regime.Vol == regime.VolLow {
		return false, "IV too low for premium selling", nil
	}

	if in.Regime.Score < 0.6 {
		return false, "regime confidence too low", nil
	}

	if len(in.Snapshot.Options) < 20 {
		return false, "insufficient options liquidity", nil
	}

	return true, "sideways + high IV confirmed", nil
}

func (s *IronCondor) BuildEntryOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error) {
	calls := filterByType(in.Snapshot.Options, "call_options")
	puts := filterByType(in.Snapshot.Options, "put_options")

	// Find short call (OTM, ~0.25 delta)
	shortCall := FindOptionByDelta(calls, "call_options", s.ShortDelta)
	if shortCall == nil {
		return nil, fmt.Errorf("no suitable short call found")
	}

	// Find short put (OTM, ~-0.25 delta)
	shortPut := FindOptionByDelta(puts, "put_options", -s.ShortDelta)
	if shortPut == nil {
		return nil, fmt.Errorf("no suitable short put found")
	}

	shortCallStrike := parseFloat(shortCall.StrikePrice)
	shortPutStrike := parseFloat(shortPut.StrikePrice)

	// Find long call (further OTM - higher strike)
	var longCall *delta.Ticker
	currentStrike := shortCallStrike
	for i := 0; i < s.WingWidth; i++ {
		next := GetNextStrike(calls, "call_options", currentStrike, true)
		if next != nil {
			longCall = next
			currentStrike = parseFloat(next.StrikePrice)
		}
	}
	if longCall == nil {
		return nil, fmt.Errorf("no suitable long call wing found")
	}

	// Find long put (further OTM - lower strike)
	var longPut *delta.Ticker
	currentStrike = shortPutStrike
	for i := 0; i < s.WingWidth; i++ {
		next := GetNextStrike(puts, "put_options", currentStrike, false)
		if next != nil {
			longPut = next
			currentStrike = parseFloat(next.StrikePrice)
		}
	}
	if longPut == nil {
		return nil, fmt.Errorf("no suitable long put wing found")
	}

	now := time.Now()
	strategyID := fmt.Sprintf("%s_%d", s.id, now.UnixMilli())

	// Calculate prices
	shortCallPrice := parseFloat(shortCall.Quotes.BestAsk)
	shortPutPrice := parseFloat(shortPut.Quotes.BestAsk)
	longCallPrice := parseFloat(longCall.Quotes.BestBid)
	longPutPrice := parseFloat(longPut.Quotes.BestBid)

	// Net credit = premium received - premium paid
	netCredit := (shortCallPrice + shortPutPrice - longCallPrice - longPutPrice) * float64(s.PositionSize)

	if netCredit <= 0 {
		return nil, fmt.Errorf("negative net credit: %.2f", netCredit)
	}

	// Check if credit covers transaction costs (require 2x costs)
	costs := in.Portfolio.Costs.EstimateCost(netCredit, true, 4) // 4 legs, maker (PostOnly)
	if netCredit < costs*2 {
		return nil, fmt.Errorf("insufficient edge after costs: credit=%.2f costs=%.2f", netCredit, costs)
	}

	// Prepare metadata for risk checks
	longCallStrike := parseFloat(longCall.StrikePrice)
	longPutStrike := parseFloat(longPut.StrikePrice)

	legs := []Leg{
		{
			ID: "sc", InstrumentID: shortCall.ProductID, Symbol: shortCall.Symbol,
			Side: execution.Sell, Qty: s.PositionSize, EntryPrice: shortCallPrice,
			Strike: shortCallStrike,
			Delta:  parseFloat(shortCall.Greeks.Delta), Gamma: parseFloat(shortCall.Greeks.Gamma),
		},
		{
			ID: "sp", InstrumentID: shortPut.ProductID, Symbol: shortPut.Symbol,
			Side: execution.Sell, Qty: s.PositionSize, EntryPrice: shortPutPrice,
			Strike: shortPutStrike,
			Delta:  parseFloat(shortPut.Greeks.Delta), Gamma: parseFloat(shortPut.Greeks.Gamma),
		},
		{
			ID: "lc", InstrumentID: longCall.ProductID, Symbol: longCall.Symbol,
			Side: execution.Buy, Qty: s.PositionSize, EntryPrice: longCallPrice,
			Strike: longCallStrike,
			Delta:  parseFloat(longCall.Greeks.Delta), Gamma: parseFloat(longCall.Greeks.Gamma),
		},
		{
			ID: "lp", InstrumentID: longPut.ProductID, Symbol: longPut.Symbol,
			Side: execution.Buy, Qty: s.PositionSize, EntryPrice: longPutPrice,
			Strike: longPutStrike,
			Delta:  parseFloat(longPut.Greeks.Delta), Gamma: parseFloat(longPut.Greeks.Gamma),
		},
	}

	maxSpreadWidth := 0.0
	if width := longCallStrike - shortCallStrike; width > maxSpreadWidth {
		maxSpreadWidth = width
	}
	if width := shortPutStrike - longPutStrike; width > maxSpreadWidth {
		maxSpreadWidth = width
	}

	maxLoss := (maxSpreadWidth * float64(s.PositionSize)) - netCredit

	metadata := &StrategyPositionMetadata{
		NetPremium:    netCredit,
		MaxLoss:       maxLoss,
		MaxProfit:     netCredit,
		BreakevenLow:  shortPutStrike - netCredit/float64(s.PositionSize),
		BreakevenHigh: shortCallStrike + netCredit/float64(s.PositionSize),
		Legs:          legs,
	}

	// REMOVED: Position assignment moved to ConfirmEntry()
	// Position will only be set AFTER fills are verified

	// Build orders - BUY protection legs FIRST
	return &execution.MultiLegOrder{
		Metadata:   metadata,
		StrategyID: strategyID,
		Timeout:    120 * time.Second,
		AllOrNone:  true,
		UseRetry:   true,
		RetryCfg:   execution.DefaultRetryConfig,
		Legs: []execution.OrderRequest{
			// Long call (protection) - BUY FIRST
			{
				ClientOrderID: execution.GenerateClientOrderID(strategyID, "lc", now),
				InstrumentID:  longCall.ProductID,
				Symbol:        longCall.Symbol,
				Side:          execution.Buy,
				Qty:           s.PositionSize,
				Price:         longCallPrice,
				OrderType:     execution.Limit,
				PostOnly:      true,
				TimeInForce:   "gtc",
				StrategyID:    strategyID,
				LegID:         "lc",
			},
			// Long put (protection) - BUY FIRST
			{
				ClientOrderID: execution.GenerateClientOrderID(strategyID, "lp", now),
				InstrumentID:  longPut.ProductID,
				Symbol:        longPut.Symbol,
				Side:          execution.Buy,
				Qty:           s.PositionSize,
				Price:         longPutPrice,
				OrderType:     execution.Limit,
				PostOnly:      true,
				TimeInForce:   "gtc",
				StrategyID:    strategyID,
				LegID:         "lp",
			},
			// Short call - SELL after protection in place
			{
				ClientOrderID: execution.GenerateClientOrderID(strategyID, "sc", now),
				InstrumentID:  shortCall.ProductID,
				Symbol:        shortCall.Symbol,
				Side:          execution.Sell,
				Qty:           s.PositionSize,
				Price:         shortCallPrice,
				OrderType:     execution.Limit,
				PostOnly:      true,
				TimeInForce:   "gtc",
				StrategyID:    strategyID,
				LegID:         "sc",
			},
			// Short put - SELL after protection in place
			{
				ClientOrderID: execution.GenerateClientOrderID(strategyID, "sp", now),
				InstrumentID:  shortPut.ProductID,
				Symbol:        shortPut.Symbol,
				Side:          execution.Sell,
				Qty:           s.PositionSize,
				Price:         shortPutPrice,
				OrderType:     execution.Limit,
				PostOnly:      true,
				TimeInForce:   "gtc",
				StrategyID:    strategyID,
				LegID:         "sp",
			},
		},
	}, nil
}

// ConfirmEntry sets the position state AFTER fills are verified
func (s *IronCondor) ConfirmEntry(ctx context.Context, result *execution.MultiLegResult, metadata *StrategyPositionMetadata) error {
	if !result.FullyFilled {
		return fmt.Errorf("cannot confirm entry - not fully filled")
	}

	// Extract actual fill data from results
	legs := make([]Leg, 0, 4)
	for legID, legState := range result.LegResults {
		if legState.Status != execution.StatusFilled {
			return fmt.Errorf("leg %s not filled: %s", legID, legState.Status)
		}

		// Find leg in metadata to get greeks and strike
		var metaLeg *Leg
		if metadata != nil {
			for i := range metadata.Legs {
				if metadata.Legs[i].ID == legID {
					metaLeg = &metadata.Legs[i]
					break
				}
			}
		}

		// Build leg from actual fill data and metadata
		leg := Leg{
			ID:           legID,
			Symbol:       legState.Request.Symbol,
			InstrumentID: legState.Request.InstrumentID,
			Side:         legState.Request.Side,
			Qty:          legState.FilledQty,
			EntryPrice:   legState.AvgFillPrice,
		}

		if metaLeg != nil {
			leg.Strike = metaLeg.Strike
			leg.Delta = metaLeg.Delta
			leg.Gamma = metaLeg.Gamma
			leg.Theta = metaLeg.Theta
			leg.Vega = metaLeg.Vega
			leg.OptionType = metaLeg.OptionType
		}

		legs = append(legs, leg)
	}

	// Calculate actual net premium from fills
	netPremium := 0.0
	for _, leg := range legs {
		if leg.Side == execution.Sell {
			netPremium += leg.EntryPrice * float64(leg.Qty)
		} else {
			netPremium -= leg.EntryPrice * float64(leg.Qty)
		}
	}

	// Use metadata for breakevens if available, else calculate
	var maxLoss, breakevenLow, breakevenHigh float64
	if metadata != nil {
		maxLoss = metadata.MaxLoss
		breakevenLow = metadata.BreakevenLow
		breakevenHigh = metadata.BreakevenHigh
	} else {
		// Fallback calculation logic
		var shortCallStrike, shortPutStrike float64
		for _, leg := range legs {
			if leg.ID == "short_call" {
				shortCallStrike = leg.Strike
			} else if leg.ID == "short_put" {
				shortPutStrike = leg.Strike
			}
		}
		// ... existing fallback ...
		breakevenLow = shortPutStrike - netPremium/float64(s.PositionSize)
		breakevenHigh = shortCallStrike + netPremium/float64(s.PositionSize)
	}

	// NOW set the position with actual fill data
	s.position = &StrategyPosition{
		StrategyID:    result.StrategyID,
		EntryTime:     result.CompletedAt,
		NetPremium:    netPremium,
		MaxLoss:       maxLoss,
		MaxProfit:     netPremium,
		BreakevenLow:  breakevenLow,
		BreakevenHigh: breakevenHigh,
		Legs:          legs,
	}

	return nil
}

func (s *IronCondor) Manage(ctx context.Context, in Input) ([]execution.OrderRequest, error) {
	if !s.HasPosition() {
		return nil, nil
	}

	pos := s.position

	// Update current prices
	for i := range pos.Legs {
		leg := &pos.Legs[i]
		for _, opt := range in.Snapshot.Options {
			if opt.ProductID == leg.InstrumentID {
				if leg.Side == execution.Buy {
					leg.CurrentPrice = parseFloat(opt.Quotes.BestBid)
				} else {
					leg.CurrentPrice = parseFloat(opt.Quotes.BestAsk)
				}
				break
			}
		}
	}

	// Calculate current P&L
	// For iron condor: P&L = net credit - cost to close
	costToClose := 0.0
	for _, leg := range pos.Legs {
		if leg.Side == execution.Buy {
			// We'd sell to close
			costToClose -= leg.CurrentPrice * float64(leg.Qty)
		} else {
			// We'd buy to close
			costToClose += leg.CurrentPrice * float64(leg.Qty)
		}
	}
	pos.CurrentPnL = pos.NetPremium - costToClose

	// Take profit: close at 50% of max profit
	if pos.CurrentPnL >= pos.MaxProfit*s.TakeProfitPct {
		return s.buildCloseOrderRequests(in)
	}

	// Stop loss: close at 1.5x premium collected loss
	stopLossLevel := -pos.NetPremium * s.StopLossMultiplier
	if pos.CurrentPnL <= stopLossLevel {
		return s.buildCloseOrderRequests(in)
	}

	// Regime change: close if trend emerges or crash
	if in.Regime.Trend != regime.TrendSideways || in.Regime.Stress == regime.StressCrash {
		return s.buildCloseOrderRequests(in)
	}

	// Breach check: if price approaches short strikes, consider early exit
	spot := in.Snapshot.SpotPrice
	for _, leg := range pos.Legs {
		if leg.Side == execution.Sell {
			distance := abs(spot - leg.Strike)
			threshold := leg.Strike * 0.02 // 2% buffer
			if distance < threshold {
				// Price approaching short strike - close early
				return s.buildCloseOrderRequests(in)
			}
		}
	}

	return nil, nil
}

func (s *IronCondor) BuildCloseOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error) {
	orders, err := s.buildCloseOrderRequests(in)
	if err != nil {
		return nil, err
	}

	return &execution.MultiLegOrder{
		StrategyID: s.position.StrategyID,
		Legs:       orders,
		Timeout:    30 * time.Second,
		AllOrNone:  false,
	}, nil
}

func (s *IronCondor) buildCloseOrderRequests(in Input) ([]execution.OrderRequest, error) {
	if !s.HasPosition() {
		return nil, fmt.Errorf("no position to close")
	}

	var orders []execution.OrderRequest
	now := time.Now()

	// CRITICAL: Close short legs FIRST, then long legs
	// This ensures we never remove protection before closing shorts
	// The execution layer should respect this ordering

	// First: close shorts (buy to close) - PRIORITY
	shortOrders := make([]execution.OrderRequest, 0, 2)
	for _, leg := range s.position.Legs {
		if leg.Side != execution.Sell {
			continue
		}

		var price float64
		for _, opt := range in.Snapshot.Options {
			if opt.ProductID == leg.InstrumentID {
				// Use more aggressive pricing for shorts - we MUST close these
				price = parseFloat(opt.Quotes.BestAsk) * 1.02 // 2% buffer for urgency
				break
			}
		}

		shortOrders = append(shortOrders, execution.OrderRequest{
			ClientOrderID: execution.GenerateClientOrderID(s.position.StrategyID, leg.ID+"_close", now),
			InstrumentID:  leg.InstrumentID,
			Symbol:        leg.Symbol,
			Side:          execution.Buy,
			Qty:           leg.Qty,
			Price:         price,
			OrderType:     execution.Limit,
			ReduceOnly:    true,
			TimeInForce:   "ioc",
			StrategyID:    s.position.StrategyID,
			LegID:         leg.ID + "_close",
			Priority:      1, // Higher priority - close first
		})
	}

	// Second: close longs (sell to close) - only after shorts are closed
	longOrders := make([]execution.OrderRequest, 0, 2)
	for _, leg := range s.position.Legs {
		if leg.Side != execution.Buy {
			continue
		}

		var price float64
		for _, opt := range in.Snapshot.Options {
			if opt.ProductID == leg.InstrumentID {
				price = parseFloat(opt.Quotes.BestBid) * 0.99 // slight slippage buffer
				break
			}
		}

		longOrders = append(longOrders, execution.OrderRequest{
			ClientOrderID: execution.GenerateClientOrderID(s.position.StrategyID, leg.ID+"_close", now),
			InstrumentID:  leg.InstrumentID,
			Symbol:        leg.Symbol,
			Side:          execution.Sell,
			Qty:           leg.Qty,
			Price:         price,
			OrderType:     execution.Limit,
			ReduceOnly:    true,
			TimeInForce:   "ioc",
			StrategyID:    s.position.StrategyID,
			LegID:         leg.ID + "_close",
			Priority:      2, // Lower priority - close after shorts
		})
	}

	// Return shorts first, then longs
	orders = append(orders, shortOrders...)
	orders = append(orders, longOrders...)

	return orders, nil
}
