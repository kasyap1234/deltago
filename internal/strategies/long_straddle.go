package strategies

import (
	"context"
	"fmt"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
	"github.com/kiwhtas/deltago/internal/execution"
	"github.com/kiwhtas/deltago/internal/regime"
)

// LongStraddle implements a long straddle strategy
// Buy ATM call + put - profits from large moves in either direction
// Best for: Sideways + Low volatility (expecting vol expansion)
type LongStraddle struct {
	BaseStrategy
}

func NewLongStraddle(client *delta.Client, positionSize int) *LongStraddle {
	return &LongStraddle{
		BaseStrategy: BaseStrategy{
			id:                 "long_straddle",
			name:               "Long Straddle",
			client:             client,
			PositionSize:       positionSize,
			StopLossMultiplier: 0.5, // exit if lose 50% of premium paid
			TakeProfitPct:      1.0, // take profit at 100% gain
			MaxDTE:             7,
		},
	}
}

func (s *LongStraddle) SuitableRegimes() []regime.TrendState {
	return []regime.TrendState{regime.TrendSideways}
}

func (s *LongStraddle) PreferredVol() regime.VolState {
	return regime.VolLow // buy when IV is low, expecting expansion
}

func (s *LongStraddle) ShouldEnter(ctx context.Context, in Input) (bool, string, error) {
	if s.HasPosition() {
		return false, "already in position", nil
	}

	// Only enter when vol is low (cheap options)
	if in.Regime.Vol != regime.VolLow {
		return false, "IV not low enough", nil
	}

	if in.Regime.Score < 0.5 {
		return false, "regime confidence too low", nil
	}

	if len(in.Snapshot.Options) < 10 {
		return false, "insufficient options liquidity", nil
	}

	return true, "low IV - vol expansion opportunity", nil
}

func (s *LongStraddle) BuildEntryOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error) {
	spot := in.Snapshot.SpotPrice

	// Find ATM call
	atmCall := FindATMOption(in.Snapshot.Options, "call_options", spot)
	if atmCall == nil {
		return nil, fmt.Errorf("no ATM call found")
	}

	// Find ATM put at same strike
	atmStrike := parseFloat(atmCall.StrikePrice)
	atmPut := FindOptionByStrike(in.Snapshot.Options, "put_options", atmStrike)
	if atmPut == nil {
		// Fallback to closest put
		atmPut = FindATMOption(in.Snapshot.Options, "put_options", spot)
		if atmPut == nil {
			return nil, fmt.Errorf("no ATM put found")
		}
	}

	now := time.Now()
	strategyID := fmt.Sprintf("%s_%d", s.id, now.UnixMilli())

	callPrice := parseFloat(atmCall.Quotes.BestAsk)
	putPrice := parseFloat(atmPut.Quotes.BestAsk)
	totalDebit := (callPrice + putPrice) * float64(s.PositionSize)

	// Prepare metadata
	legs := []Leg{
		{
			ID: "long_call", InstrumentID: atmCall.ProductID, Symbol: atmCall.Symbol,
			Side: execution.Buy, Qty: s.PositionSize, EntryPrice: callPrice,
			Strike: atmStrike, OptionType: "call",
			Delta: parseFloat(atmCall.Greeks.Delta), Gamma: parseFloat(atmCall.Greeks.Gamma),
		},
		{
			ID: "long_put", InstrumentID: atmPut.ProductID, Symbol: atmPut.Symbol,
			Side: execution.Buy, Qty: s.PositionSize, EntryPrice: putPrice,
			Strike: atmStrike, OptionType: "put",
			Delta: parseFloat(atmPut.Greeks.Delta), Gamma: parseFloat(atmPut.Greeks.Gamma),
		},
	}

	metadata := &StrategyPositionMetadata{
		NetPremium:    -totalDebit,
		MaxLoss:       totalDebit,
		MaxProfit:     999999,
		BreakevenLow:  atmStrike - (totalDebit / float64(s.PositionSize)),
		BreakevenHigh: atmStrike + (totalDebit / float64(s.PositionSize)),
		Legs:          legs,
	}

	return &execution.MultiLegOrder{
		Metadata:   metadata,
		StrategyID: strategyID,
		Timeout:    60 * time.Second,
		AllOrNone:  true,
		Legs: []execution.OrderRequest{
			{
				ClientOrderID: execution.GenerateClientOrderID(strategyID, "long_call", now),
				InstrumentID:  atmCall.ProductID,
				Symbol:        atmCall.Symbol,
				Side:          execution.Buy,
				Qty:           s.PositionSize,
				Price:         callPrice,
				OrderType:     execution.Limit,
				PostOnly:      true,
				TimeInForce:   "gtc",
				StrategyID:    strategyID,
				LegID:         "long_call",
			},
			{
				ClientOrderID: execution.GenerateClientOrderID(strategyID, "long_put", now),
				InstrumentID:  atmPut.ProductID,
				Symbol:        atmPut.Symbol,
				Side:          execution.Buy,
				Qty:           s.PositionSize,
				Price:         putPrice,
				OrderType:     execution.Limit,
				PostOnly:      true,
				TimeInForce:   "gtc",
				StrategyID:    strategyID,
				LegID:         "long_put",
			},
		},
	}, nil
}

// ConfirmEntry sets the position state AFTER fills are verified
func (s *LongStraddle) ConfirmEntry(ctx context.Context, result *execution.MultiLegResult, metadata *StrategyPositionMetadata) error {
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
	var netPremium, maxLoss, maxProfit, breakevenLow, breakevenHigh float64
	if metadata != nil {
		actualNet := 0.0
		for _, leg := range legs {
			actualNet -= leg.EntryPrice * float64(leg.Qty)
		}
		netPremium = actualNet
		maxLoss = -actualNet
		maxProfit = metadata.MaxProfit
		
		atmStrike := legs[0].Strike
		premiumPerContract := -actualNet / float64(s.PositionSize)
		breakevenLow = atmStrike - premiumPerContract
		breakevenHigh = atmStrike + premiumPerContract
	}

	// NOW set the position with actual fill data
	s.position = &StrategyPosition{
		StrategyID:    result.StrategyID,
		EntryTime:     result.CompletedAt,
		NetPremium:    netPremium,
		MaxLoss:       maxLoss,
		MaxProfit:     maxProfit,
		BreakevenLow:  breakevenLow,
		BreakevenHigh: breakevenHigh,
		Legs:          legs,
	}

	return nil
}

func (s *LongStraddle) Manage(ctx context.Context, in Input) ([]execution.OrderRequest, error) {
	if !s.HasPosition() {
		return nil, nil
	}

	pos := s.position

	// Update current prices
	for i := range pos.Legs {
		leg := &pos.Legs[i]
		for _, opt := range in.Snapshot.Options {
			if opt.ProductID == leg.InstrumentID {
				leg.CurrentPrice = parseFloat(opt.Quotes.BestBid)
				break
			}
		}
	}

	// Calculate current value
	currentValue := 0.0
	for _, leg := range pos.Legs {
		currentValue += leg.CurrentPrice * float64(leg.Qty)
	}

	totalPaid := -pos.NetPremium
	pos.CurrentPnL = currentValue - totalPaid

	// Take profit: 100% gain
	if pos.CurrentPnL >= totalPaid*s.TakeProfitPct {
		return s.buildCloseOrderRequests(in)
	}

	// Stop loss: lose 50% of premium
	if pos.CurrentPnL <= -totalPaid*s.StopLossMultiplier {
		return s.buildCloseOrderRequests(in)
	}

	// Vol expansion achieved - close if IV becomes high
	if in.Regime.Vol == regime.VolHigh && pos.CurrentPnL > 0 {
		return s.buildCloseOrderRequests(in)
	}

	// If trend emerges strongly, let winners run but protect profits
	if in.Regime.Trend != regime.TrendSideways && pos.CurrentPnL > totalPaid*0.3 {
		// Close losing leg to lock in profits
		// For simplicity, close entire position
		return s.buildCloseOrderRequests(in)
	}

	return nil, nil
}

func (s *LongStraddle) BuildCloseOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error) {
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

func (s *LongStraddle) buildCloseOrderRequests(in Input) ([]execution.OrderRequest, error) {
	if !s.HasPosition() {
		return nil, fmt.Errorf("no position to close")
	}

	var orders []execution.OrderRequest
	now := time.Now()

	for _, leg := range s.position.Legs {
		var price float64
		for _, opt := range in.Snapshot.Options {
			if opt.ProductID == leg.InstrumentID {
				price = parseFloat(opt.Quotes.BestBid) * 0.99 // slight slippage
				break
			}
		}

		orders = append(orders, execution.OrderRequest{
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
		})
	}

	return orders, nil
}
