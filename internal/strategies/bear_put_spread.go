package strategies

import (
	"context"
	"fmt"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
	"github.com/kiwhtas/deltago/internal/execution"
	"github.com/kiwhtas/deltago/internal/regime"
)

// BearPutSpread implements a bear put spread strategy
// Buy ATM put, sell OTM put - profits when price falls moderately
// Best for: TrendDown + any volatility (defined risk)
type BearPutSpread struct {
	BaseStrategy

	LongDelta  float64 // target delta for long put (e.g., -0.50)
	ShortDelta float64 // target delta for short put (e.g., -0.30)
}

func NewBearPutSpread(client *delta.Client, positionSize int, testnet bool) *BearPutSpread {
	return &BearPutSpread{
		BaseStrategy: BaseStrategy{
			id:                 "bear_put_spread",
			name:               "Bear Put Spread",
			client:             client,
			PositionSize:       positionSize,
			StopLossMultiplier: 1.0,
			TakeProfitPct:      0.5,
			MaxDTE:             7,
			Testnet:            testnet,
		},
		LongDelta:  0.50, // will be negative for puts
		ShortDelta: 0.30,
	}
}

func (s *BearPutSpread) SuitableRegimes() []regime.TrendState {
	return []regime.TrendState{regime.TrendDown}
}

func (s *BearPutSpread) PreferredVol() regime.VolState {
	return regime.VolNormal
}

func (s *BearPutSpread) ShouldEnter(ctx context.Context, in Input) (bool, string, error) {
	if s.HasPosition() {
		return false, "already in position", nil
	}

	if in.Regime.Trend != regime.TrendDown {
		return false, "not in downtrend", nil
	}

	if in.Regime.Score < 0.6 {
		return false, "regime confidence too low", nil
	}

	if len(in.Snapshot.Options) < 10 {
		return false, "insufficient options liquidity", nil
	}

	return true, "downtrend confirmed", nil
}

func (s *BearPutSpread) BuildEntryOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error) {
	puts := filterByType(in.Snapshot.Options, "put_options")

	// Find long put (ATM)
	longPut := FindOptionByDelta(puts, "put_options", -s.LongDelta)
	if longPut == nil {
		return nil, fmt.Errorf("no suitable long put found")
	}

	// Find short put (OTM - lower strike)
	shortPut := FindOptionByDelta(puts, "put_options", -s.ShortDelta)
	if shortPut == nil {
		return nil, fmt.Errorf("no suitable short put found")
	}

	// Ensure short is lower strike than long
	longStrike := parseFloat(longPut.StrikePrice)
	shortStrike := parseFloat(shortPut.StrikePrice)
	if shortStrike >= longStrike {
		shortPut = GetNextStrike(puts, "put_options", longStrike, false)
		if shortPut == nil {
			return nil, fmt.Errorf("no lower strike for short put")
		}
		shortStrike = parseFloat(shortPut.StrikePrice)
	}

	now := time.Now()
	strategyID := fmt.Sprintf("%s_%d", s.id, now.UnixMilli())

	// Calculate prices for orders
	// BUY orders use BestAsk (price we pay sellers)
	// SELL orders use BestBid (price buyers will pay us)
	longPrice := parseFloat(longPut.Quotes.BestAsk)
	shortPrice := parseFloat(shortPut.Quotes.BestBid)
	netDebit := longPrice - shortPrice

	// Price sanity check: long leg MUST be more expensive than short leg for a debit spread
	if longPrice <= shortPrice {
		return nil, fmt.Errorf("inverted spread: long=%.2f <= short=%.2f", longPrice, shortPrice)
	}

	if netDebit <= 0 {
		return nil, fmt.Errorf("negative net debit: %.2f", netDebit)
	}

	// Check transaction costs - use gross premium (sum of leg values) not net
	multiplier := 0.001
	grossPremium := (longPrice + shortPrice) * float64(s.PositionSize) * multiplier
	netDebit = (longPrice - shortPrice) * multiplier
	costs := in.Portfolio.Costs.EstimateCost(grossPremium, true, 2)
	if ((longStrike-shortStrike)*multiplier - netDebit) < costs*2 {
		return nil, fmt.Errorf("insufficient edge after costs: edge=%.2f costs=%.2f", (longStrike-shortStrike)*multiplier-netDebit, costs)
	}

	// Prepare metadata
	legs := []Leg{
		{
			ID: "lp", InstrumentID: longPut.ProductID, Symbol: longPut.Symbol,
			Side: execution.Buy, Qty: s.PositionSize, EntryPrice: longPrice,
			Strike: longStrike, OptionType: "put",
			Delta: parseFloat(longPut.Greeks.Delta), Gamma: parseFloat(longPut.Greeks.Gamma),
		},
		{
			ID: "sp", InstrumentID: shortPut.ProductID, Symbol: shortPut.Symbol,
			Side: execution.Sell, Qty: s.PositionSize, EntryPrice: shortPrice,
			Strike: shortStrike, OptionType: "put",
			Delta: parseFloat(shortPut.Greeks.Delta), Gamma: parseFloat(shortPut.Greeks.Gamma),
		},
	}

	metadata := &StrategyPositionMetadata{
		NetPremium: -netDebit * float64(s.PositionSize),
		MaxLoss:    netDebit * float64(s.PositionSize),
		MaxProfit:  ((longStrike-shortStrike)*multiplier - netDebit) * float64(s.PositionSize),
		Legs:       legs,
	}

	return &execution.MultiLegOrder{
		Metadata:   metadata,
		StrategyID: strategyID,
		Timeout:    60 * time.Second,
		AllOrNone:  true,
		UseRetry:   true,
		RetryCfg:   s.GetRetryCfg(),
		Legs: []execution.OrderRequest{
			{
				ClientOrderID: execution.GenerateClientOrderID(strategyID, "lp", now),
				InstrumentID:  longPut.ProductID,
				Symbol:        longPut.Symbol,
				Side:          execution.Buy,
				Qty:           s.PositionSize,
				Price:         longPrice,
				OrderType:     execution.Limit,
				PostOnly:      true,
				TimeInForce:   "gtc",
				StrategyID:    strategyID,
				LegID:         "lp",
			},
			{
				ClientOrderID: execution.GenerateClientOrderID(strategyID, "sp", now),
				InstrumentID:  shortPut.ProductID,
				Symbol:        shortPut.Symbol,
				Side:          execution.Sell,
				Qty:           s.PositionSize,
				Price:         shortPrice,
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
func (s *BearPutSpread) ConfirmEntry(ctx context.Context, result *execution.MultiLegResult, metadata *StrategyPositionMetadata) error {
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
	multiplier := 0.001
	var netPremium, maxLoss, maxProfit float64
	if metadata != nil {
		actualNet := 0.0
		for _, leg := range legs {
			if leg.Side == execution.Buy {
				actualNet -= leg.EntryPrice * float64(leg.Qty) * multiplier
			} else {
				actualNet += leg.EntryPrice * float64(leg.Qty) * multiplier
			}
		}
		netPremium = actualNet
		maxLoss = metadata.MaxLoss
		maxProfit = metadata.MaxProfit
	}

	// NOW set the position with actual fill data (thread-safe)
	s.SetPosition(&StrategyPosition{
		StrategyID: result.StrategyID,
		EntryTime:  result.CompletedAt,
		NetPremium: netPremium,
		MaxLoss:    maxLoss,
		MaxProfit:  maxProfit,
		Legs:       legs,
	})

	return nil
}

func (s *BearPutSpread) Manage(ctx context.Context, in Input) ([]execution.OrderRequest, error) {
	if !s.HasPosition() {
		return nil, nil
	}

	s.mu.Lock()
	pos := s.position
	if pos == nil {
		s.mu.Unlock()
		return nil, nil
	}

	// Update current prices and calculate P&L
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

	multiplier := 0.001
	currentValue := 0.0
	entryValue := 0.0
	for _, leg := range pos.Legs {
		if leg.Side == execution.Buy {
			currentValue += leg.CurrentPrice * float64(leg.Qty) * multiplier
			entryValue -= leg.EntryPrice * float64(leg.Qty) * multiplier
		} else {
			currentValue -= leg.CurrentPrice * float64(leg.Qty) * multiplier
			entryValue += leg.EntryPrice * float64(leg.Qty) * multiplier
		}
	}
	pos.CurrentPnL = currentValue + entryValue

	// Take profit
	if pos.CurrentPnL >= pos.MaxProfit*s.TakeProfitPct {
		s.mu.Unlock()
		return s.buildCloseOrderRequests(in)
	}

	// Regime reversal - close if trend changes
	if in.Regime.Trend == regime.TrendUp {
		s.mu.Unlock()
		return s.buildCloseOrderRequests(in)
	}

	s.mu.Unlock()
	return nil, nil
}

func (s *BearPutSpread) BuildCloseOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error) {
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

func (s *BearPutSpread) buildCloseOrderRequests(in Input) ([]execution.OrderRequest, error) {
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
					price = parseFloat(opt.Quotes.BestAsk)
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
