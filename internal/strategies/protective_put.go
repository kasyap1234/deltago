package strategies

import (
	"context"
	"fmt"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
	"github.com/kiwhtas/deltago/internal/execution"
	"github.com/kiwhtas/deltago/internal/regime"
)

// ProtectivePut implements a protective put / crash alpha strategy
// Buy OTM puts for crash protection - profits in crashes
// Best for: Crash detected or high stress
type ProtectivePut struct {
	BaseStrategy

	TargetDelta float64 // target delta for puts (e.g., 0.20)
}

func NewProtectivePut(client *delta.Client, positionSize int) *ProtectivePut {
	return &ProtectivePut{
		BaseStrategy: BaseStrategy{
			id:                 "protective_put",
			name:               "Protective Put",
			client:             client,
			PositionSize:       positionSize,
			StopLossMultiplier: 0.7, // exit if lose 70% of premium
			TakeProfitPct:      2.0, // take profit at 200% gain
			MaxDTE:             14,
		},
		TargetDelta: 0.20, // slightly OTM
	}
}

func (s *ProtectivePut) SuitableRegimes() []regime.TrendState {
	return []regime.TrendState{regime.TrendDown, regime.TrendSideways}
}

func (s *ProtectivePut) PreferredVol() regime.VolState {
	return regime.VolNormal // enter before vol spikes if possible
}

func (s *ProtectivePut) ShouldEnter(ctx context.Context, in Input) (bool, string, error) {
	if s.HasPosition() {
		return false, "already in position", nil
	}

	// Enter in crash or high stress situations
	if in.Regime.Stress == regime.StressCrash {
		return true, "crash protection activated", nil
	}

	// Also enter in strong downtrend with high vol
	if in.Regime.Trend == regime.TrendDown && in.Regime.Vol == regime.VolHigh {
		return true, "downtrend with high vol - protection needed", nil
	}

	if len(in.Snapshot.Options) < 10 {
		return false, "insufficient options liquidity", nil
	}

	return false, "no crash/stress conditions", nil
}

func (s *ProtectivePut) BuildEntryOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error) {
	puts := filterByType(in.Snapshot.Options, "put_options")

	// Find OTM put with target delta
	put := FindOptionByDelta(puts, "put_options", -s.TargetDelta)
	if put == nil {
		return nil, fmt.Errorf("no suitable put found")
	}

	now := time.Now()
	strategyID := fmt.Sprintf("%s_%d", s.id, now.UnixMilli())

	// BUY orders use BestAsk (price we pay sellers)
	multiplier := 0.001
	putPrice := parseFloat(put.Quotes.BestAsk)
	totalDebit := putPrice * float64(s.PositionSize) * multiplier

	// Prepare metadata
	legs := []Leg{
		{
			ID: "lp", InstrumentID: put.ProductID, Symbol: put.Symbol,
			Side: execution.Buy, Qty: s.PositionSize, EntryPrice: putPrice,
			Strike: parseFloat(put.StrikePrice), OptionType: "put",
			Delta: parseFloat(put.Greeks.Delta), Gamma: parseFloat(put.Greeks.Gamma),
		},
	}

	metadata := &StrategyPositionMetadata{
		NetPremium:   -totalDebit,
		MaxLoss:      totalDebit,
		MaxProfit:    parseFloat(put.StrikePrice) * float64(s.PositionSize) * multiplier,
		BreakevenLow: parseFloat(put.StrikePrice) - putPrice,
		Legs:         legs,
	}

	return &execution.MultiLegOrder{
		Metadata:   metadata,
		StrategyID: strategyID,
		Timeout:    60 * time.Second,
		AllOrNone:  true,
		UseRetry:   true,
		RetryCfg:   execution.DefaultRetryConfig,
		Legs: []execution.OrderRequest{
			{
				ClientOrderID: execution.GenerateClientOrderID(strategyID, "lp", now),
				InstrumentID:  put.ProductID,
				Symbol:        put.Symbol,
				Side:          execution.Buy,
				Qty:           s.PositionSize,
				Price:         putPrice,
				OrderType:     execution.Limit,
				PostOnly:      true,
				TimeInForce:   "gtc",
				StrategyID:    strategyID,
				LegID:         "lp",
			},
		},
	}, nil
}

// ConfirmEntry sets the position state AFTER fills are verified
func (s *ProtectivePut) ConfirmEntry(ctx context.Context, result *execution.MultiLegResult, metadata *StrategyPositionMetadata) error {
	if !result.FullyFilled {
		return fmt.Errorf("cannot confirm entry - not fully filled")
	}

	// Extract actual fill data (single leg)
	legState, ok := result.LegResults["lp"]
	if !ok || legState.Status != execution.StatusFilled {
		return fmt.Errorf("lp leg not filled")
	}

	var metaLeg *Leg
	if metadata != nil {
		metaLeg = &metadata.Legs[0]
	}

	leg := Leg{
		ID:           "lp",
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

	multiplier := 0.001
	totalPremium := legState.AvgFillPrice * float64(legState.FilledQty) * multiplier

	// NOW set the position with actual fill data (thread-safe)
	s.SetPosition(&StrategyPosition{
		StrategyID:   result.StrategyID,
		EntryTime:    result.CompletedAt,
		NetPremium:   -totalPremium,
		MaxLoss:      totalPremium,
		MaxProfit:    leg.Strike * float64(s.PositionSize) * multiplier,
		BreakevenLow: leg.Strike - legState.AvgFillPrice,
		Legs:         []Leg{leg},
	})

	return nil
}

func (s *ProtectivePut) Manage(ctx context.Context, in Input) ([]execution.OrderRequest, error) {
	if !s.HasPosition() {
		return nil, nil
	}

	pos := s.position

	// Update current price
	for i := range pos.Legs {
		leg := &pos.Legs[i]
		for _, opt := range in.Snapshot.Options {
			if opt.ProductID == leg.InstrumentID {
				leg.CurrentPrice = parseFloat(opt.Quotes.BestBid)
				break
			}
		}
	}

	// Calculate P&L
	multiplier := 0.001
	currentValue := pos.Legs[0].CurrentPrice * float64(pos.Legs[0].Qty) * multiplier
	paidPremium := -pos.NetPremium
	pos.CurrentPnL = currentValue - paidPremium

	// Take profit at 200% gain
	if pos.CurrentPnL >= paidPremium*s.TakeProfitPct {
		return s.buildCloseOrderRequests(in)
	}

	// Stop loss at 70% premium decay
	if pos.CurrentPnL <= -paidPremium*s.StopLossMultiplier {
		return s.buildCloseOrderRequests(in)
	}

	// Close if regime normalizes and we're still profitable
	if in.Regime.Stress == regime.StressNormal &&
		in.Regime.Trend != regime.TrendDown &&
		pos.CurrentPnL > 0 {
		return s.buildCloseOrderRequests(in)
	}

	return nil, nil
}

func (s *ProtectivePut) BuildCloseOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error) {
	orders, err := s.buildCloseOrderRequests(in)
	if err != nil {
		return nil, err
	}

	return &execution.MultiLegOrder{
		StrategyID: s.position.StrategyID,
		Legs:       orders,
		Timeout:    30 * time.Second,
		AllOrNone:  true,
	}, nil
}

func (s *ProtectivePut) buildCloseOrderRequests(in Input) ([]execution.OrderRequest, error) {
	if !s.HasPosition() {
		return nil, fmt.Errorf("no position to close")
	}

	now := time.Now()
	leg := s.position.Legs[0]

	var price float64
	for _, opt := range in.Snapshot.Options {
		if opt.ProductID == leg.InstrumentID {
			price = parseFloat(opt.Quotes.BestBid) * 0.99
			break
		}
	}

	return []execution.OrderRequest{
		{
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
		},
	}, nil
}
