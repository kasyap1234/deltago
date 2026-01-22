package strategies

import (
	"context"
	"fmt"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
	"github.com/kiwhtas/deltago/internal/execution"
	"github.com/kiwhtas/deltago/internal/regime"
)

// BullCallSpread implements a bull call spread strategy
// Buy ATM call, sell OTM call - profits when price rises moderately
// Best for: TrendUp + any volatility (defined risk)
type BullCallSpread struct {
	BaseStrategy

	// Configuration
	LongDelta  float64 // target delta for long call (e.g., 0.50)
	ShortDelta float64 // target delta for short call (e.g., 0.30)
}

func NewBullCallSpread(client *delta.Client, positionSize int) *BullCallSpread {
	return &BullCallSpread{
		BaseStrategy: BaseStrategy{
			id:                 "bcs",
			name:               "Bull Call Spread",
			client:             client,
			PositionSize:       positionSize,
			StopLossMultiplier: 1.0, // risk max debit paid
			TakeProfitPct:      0.5, // take profit at 50% of max profit
			MaxDTE:             7,
		},
		LongDelta:  0.50,
		ShortDelta: 0.30,
	}
}

func (s *BullCallSpread) SuitableRegimes() []regime.TrendState {
	return []regime.TrendState{regime.TrendUp}
}

func (s *BullCallSpread) PreferredVol() regime.VolState {
	return regime.VolNormal // works in any vol, but better in low-normal vol
}

func (s *BullCallSpread) ShouldEnter(ctx context.Context, in Input) (bool, string, error) {
	if s.HasPosition() {
		return false, "already in position", nil
	}

	if in.Regime.Trend != regime.TrendUp {
		return false, "not in uptrend", nil
	}

	if in.Regime.Stress == regime.StressCrash {
		return false, "crash detected", nil
	}

	if in.Regime.Score < 0.6 {
		return false, "regime confidence too low", nil
	}

	// Check if we have options
	if len(in.Snapshot.Options) < 10 {
		return false, "insufficient options liquidity", nil
	}

	return true, "uptrend confirmed", nil
}

func (s *BullCallSpread) BuildEntryOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error) {
	calls := filterByType(in.Snapshot.Options, "call_options")

	// Find long call (ATM or slightly ITM)
	longCall := FindOptionByDelta(calls, "call_options", s.LongDelta)
	if longCall == nil {
		return nil, fmt.Errorf("no suitable long call found")
	}

	// Find short call (OTM)
	shortCall := FindOptionByDelta(calls, "call_options", s.ShortDelta)
	if shortCall == nil {
		return nil, fmt.Errorf("no suitable short call found")
	}

	// Ensure short is higher strike than long
	longStrike := parseFloat(longCall.StrikePrice)
	shortStrike := parseFloat(shortCall.StrikePrice)
	if shortStrike <= longStrike {
		shortCall = GetNextStrike(calls, "call_options", longStrike, true)
		if shortCall == nil {
			return nil, fmt.Errorf("no higher strike for short call")
		}
	}

	now := time.Now()
	strategyID := fmt.Sprintf("%s_%d", s.id, now.UnixMilli())

	longPrice := parseFloat(longCall.Quotes.BestBid)
	shortPrice := parseFloat(shortCall.Quotes.BestAsk)
	netDebit := longPrice - shortPrice

	// Check if debit is positive
	if netDebit <= 0 {
		return nil, fmt.Errorf("negative net debit: %.2f", netDebit)
	}

	// Check transaction costs
	costs := in.Portfolio.Costs.EstimateCost(netDebit, true, 2)
	if (shortStrike - longStrike - netDebit) < costs*2 {
		return nil, fmt.Errorf("insufficient edge after costs: edge=%.2f costs=%.2f", shortStrike-longStrike-netDebit, costs)
	}

	// Prepare metadata
	legs := []Leg{
		{
			ID: "lc", InstrumentID: longCall.ProductID, Symbol: longCall.Symbol,
			Side: execution.Buy, Qty: s.PositionSize, EntryPrice: longPrice,
			Strike: longStrike, OptionType: "call",
			Delta: parseFloat(longCall.Greeks.Delta), Gamma: parseFloat(longCall.Greeks.Gamma),
		},
		{
			ID: "sc", InstrumentID: shortCall.ProductID, Symbol: shortCall.Symbol,
			Side: execution.Sell, Qty: s.PositionSize, EntryPrice: shortPrice,
			Strike: shortStrike, OptionType: "call",
			Delta: parseFloat(shortCall.Greeks.Delta), Gamma: parseFloat(shortCall.Greeks.Gamma),
		},
	}

	metadata := &StrategyPositionMetadata{
		NetPremium: -netDebit,
		MaxLoss:    netDebit * float64(s.PositionSize),
		MaxProfit:  (shortStrike - longStrike - netDebit) * float64(s.PositionSize),
		Legs:       legs,
	}

	order := &execution.MultiLegOrder{
		Metadata:   metadata,
		StrategyID: strategyID,
		Timeout:    60 * time.Second,
		AllOrNone:  true,
		Legs: []execution.OrderRequest{
			{
				ClientOrderID: execution.GenerateClientOrderID(strategyID, "lc", now),
				InstrumentID:  longCall.ProductID,
				Symbol:        longCall.Symbol,
				Side:          execution.Buy,
				Qty:           s.PositionSize,
				Price:         longPrice,
				OrderType:     execution.Limit,
				PostOnly:      true,
				TimeInForce:   "gtc",
				StrategyID:    strategyID,
				LegID:         "lc",
			},
			{
				ClientOrderID: execution.GenerateClientOrderID(strategyID, "sc", now),
				InstrumentID:  shortCall.ProductID,
				Symbol:        shortCall.Symbol,
				Side:          execution.Sell,
				Qty:           s.PositionSize,
				Price:         shortPrice,
				OrderType:     execution.Limit,
				PostOnly:      true,
				TimeInForce:   "gtc",
				StrategyID:    strategyID,
				LegID:         "sc",
			},
		},
	}

	// REMOVED: Position assignment moved to ConfirmEntry()
	// Position will only be set AFTER fills are verified

	return order, nil
}

// ConfirmEntry sets the position state AFTER fills are verified
func (s *BullCallSpread) ConfirmEntry(ctx context.Context, result *execution.MultiLegResult, metadata *StrategyPositionMetadata) error {
	if !result.FullyFilled {
		return fmt.Errorf("cannot confirm entry - not fully filled")
	}

	// Extract actual fill data
	legs := make([]Leg, 0, 2)
	for legID, legState := range result.LegResults {
		if legState.Status != execution.StatusFilled {
			return fmt.Errorf("leg %s not filled: %s", legID, legState.Status)
		}

		var metaLeg *Leg
		if metadata != nil {
			for i := range metadata.Legs {
				if metadata.Legs[i].ID == legID {
					metaLeg = &metadata.Legs[i]
					break
				}
			}
		}

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
			leg.OptionType = metaLeg.OptionType
			leg.Delta = metaLeg.Delta
			leg.Gamma = metaLeg.Gamma
		}

		legs = append(legs, leg)
	}

	// Calculate actual metrics from fills
	var netPremium, maxLoss, maxProfit float64
	if metadata != nil {
		// Re-calculate net premium from actual fills
		actualNet := 0.0
		for _, leg := range legs {
			if leg.Side == execution.Buy {
				actualNet -= leg.EntryPrice * float64(leg.Qty)
			} else {
				actualNet += leg.EntryPrice * float64(leg.Qty)
			}
		}
		netPremium = actualNet
		maxLoss = metadata.MaxLoss // Spread width doesn't change
		maxProfit = metadata.MaxProfit
	}

	// NOW set the position with actual fill data
	s.position = &StrategyPosition{
		StrategyID: result.StrategyID,
		EntryTime:  result.CompletedAt,
		NetPremium: netPremium,
		MaxLoss:    maxLoss,
		MaxProfit:  maxProfit,
		Legs:       legs,
	}

	return nil
}

func (s *BullCallSpread) Manage(ctx context.Context, in Input) ([]execution.OrderRequest, error) {
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
	currentValue := 0.0
	for _, leg := range pos.Legs {
		if leg.Side == execution.Buy {
			currentValue += leg.CurrentPrice * float64(leg.Qty)
		} else {
			currentValue -= leg.CurrentPrice * float64(leg.Qty)
		}
	}

	entryValue := 0.0
	for _, leg := range pos.Legs {
		if leg.Side == execution.Buy {
			entryValue -= leg.EntryPrice * float64(leg.Qty)
		} else {
			entryValue += leg.EntryPrice * float64(leg.Qty)
		}
	}

	pos.CurrentPnL = currentValue + entryValue

	// Take profit check
	if pos.CurrentPnL >= pos.MaxProfit*s.TakeProfitPct {
		// Close position for profit
		return s.buildCloseOrderRequests(in)
	}

	// Regime change check - close if trend reverses
	if in.Regime.Trend == regime.TrendDown || in.Regime.Stress == regime.StressCrash {
		return s.buildCloseOrderRequests(in)
	}

	return nil, nil
}

func (s *BullCallSpread) BuildCloseOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error) {
	orders, err := s.buildCloseOrderRequests(in)
	if err != nil {
		return nil, err
	}

	return &execution.MultiLegOrder{
		StrategyID: s.position.StrategyID,
		Legs:       orders,
		Timeout:    30 * time.Second,
		AllOrNone:  false, // Close as much as possible
	}, nil
}

func (s *BullCallSpread) buildCloseOrderRequests(in Input) ([]execution.OrderRequest, error) {
	if !s.HasPosition() {
		return nil, fmt.Errorf("no position to close")
	}

	var orders []execution.OrderRequest
	now := time.Now()

	for _, leg := range s.position.Legs {
		closeSide := execution.Sell
		if leg.Side == execution.Sell {
			closeSide = execution.Buy
		}

		var price float64
		for _, opt := range in.Snapshot.Options {
			if opt.ProductID == leg.InstrumentID {
				if closeSide == execution.Buy {
					price = parseFloat(opt.Quotes.BestAsk) // Use aggressive price for exits
				} else {
					price = parseFloat(opt.Quotes.BestBid)
				}
				break
			}
		}

		orders = append(orders, execution.OrderRequest{
			ClientOrderID: execution.GenerateClientOrderID(s.position.StrategyID, leg.ID+"_close", now),
			InstrumentID:  leg.InstrumentID,
			Symbol:        leg.Symbol,
			Side:          closeSide,
			Qty:           leg.Qty,
			Price:         price,
			OrderType:     execution.Limit,
			ReduceOnly:    true,
			TimeInForce:   "ioc",
			StrategyID:    s.position.StrategyID,
			LegID:         leg.ID + "_close",
		})
	}

	return orders, nil
}

func filterByType(options []delta.Ticker, optionType string) []delta.Ticker {
	var filtered []delta.Ticker
	for _, opt := range options {
		if opt.ContractType == optionType {
			filtered = append(filtered, opt)
		}
	}
	return filtered
}
