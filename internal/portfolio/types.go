package portfolio

import (
	"sync"
	"time"
)

// LegEntry tracks entry details for a single leg of a strategy
type LegEntry struct {
	InstrumentID int64
	Symbol       string
	Side         string // "buy" or "sell"
	Qty          int
	EntryPrice   float64
	EntryTime    time.Time
}

// Position represents an open position
type Position struct {
	InstrumentID  int64
	Symbol        string
	Qty           int64 // Positive = long, Negative = short
	AvgPrice      float64
	CurrentPrice  float64
	UnrealizedPnL float64
	RealizedPnL   float64

	// Greeks (if available)
	Delta float64
	Gamma float64
	Theta float64
	Vega  float64

	// Metadata
	StrategyID string
	OpenedAt   time.Time
	UpdatedAt  time.Time
}

// Greeks represents portfolio-level Greeks
type Greeks struct {
	NetDelta float64
	NetGamma float64
	NetTheta float64
	NetVega  float64
}

// State represents the current portfolio state
type State struct {
	mu sync.RWMutex

	Equity          float64
	AvailableMargin float64
	UsedMargin      float64
	MarginPct       float64 // UsedMargin / Equity

	Positions map[int64]*Position // by instrument ID
	Greeks    Greeks

	DailyPnL      float64
	DailyTrades   int
	MaxDailyLoss  float64
	TradingHalted bool
	HaltReason    string

	// Track strategy entries for P&L calculation
	StrategyEntries map[string][]LegEntry // strategyID -> legs

	Costs TransactionCosts

	AsOf time.Time
}

// NewState creates a new portfolio state
func NewState(initialEquity float64, maxDailyLoss float64) *State {
	return &State{
		Equity:          initialEquity,
		MaxDailyLoss:    maxDailyLoss,
		Positions:       make(map[int64]*Position),
		StrategyEntries: make(map[string][]LegEntry),
		Costs:           DefaultTransactionCosts(),
		AsOf:            time.Now(),
	}
}

// UpdatePosition updates or adds a position
func (s *State) UpdatePosition(pos *Position) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pos.UpdatedAt = time.Now()
	s.Positions[pos.InstrumentID] = pos
	s.recalculateGreeks()
}

// RemovePosition removes a position
func (s *State) RemovePosition(instrumentID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.Positions, instrumentID)
	s.recalculateGreeks()
}

// GetPosition returns a position by ID
func (s *State) GetPosition(instrumentID int64) *Position {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Positions[instrumentID]
}

// GetPositions returns all open positions
func (s *State) GetPositions() map[int64]*Position {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Create copy
	positions := make(map[int64]*Position)
	for k, v := range s.Positions {
		positions[k] = v
	}
	return positions
}

// GetPositionsByStrategy returns positions for a strategy
func (s *State) GetPositionsByStrategy(strategyID string) []*Position {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var positions []*Position
	for _, p := range s.Positions {
		if p.StrategyID == strategyID {
			positions = append(positions, p)
		}
	}
	return positions
}

// RecordTrade records a completed trade
func (s *State) RecordTrade(pnl float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.DailyPnL += pnl
	s.DailyTrades++

	// Check daily loss limit
	if s.DailyPnL < -s.MaxDailyLoss && !s.TradingHalted {
		s.TradingHalted = true
		s.HaltReason = "max daily loss exceeded"
	}
}

// RecordStrategyEntry records entry prices for a strategy's legs
func (s *State) RecordStrategyEntry(strategyID string, entries []LegEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StrategyEntries[strategyID] = entries
}

// CalculateStrategyPnL calculates realized P&L for a closed strategy
// closeResults maps InstrumentID -> exit price
func (s *State) CalculateStrategyPnL(strategyID string, closeResults map[int64]float64) float64 {
	s.mu.RLock()
	entries, ok := s.StrategyEntries[strategyID]
	s.mu.RUnlock()

	if !ok {
		return 0
	}

	totalPnL := 0.0
	for _, entry := range entries {
		exitPrice, ok := closeResults[entry.InstrumentID]
		if !ok {
			continue
		}

		if entry.Side == "buy" {
			// Long position: P&L = (exit - entry) * qty
			totalPnL += (exitPrice - entry.EntryPrice) * float64(entry.Qty)
		} else {
			// Short position: P&L = (entry - exit) * qty
			totalPnL += (entry.EntryPrice - exitPrice) * float64(entry.Qty)
		}
	}

	// Clean up entry record
	s.mu.Lock()
	delete(s.StrategyEntries, strategyID)
	s.mu.Unlock()

	return totalPnL
}

// ResetDaily resets daily counters (call at start of trading day)
func (s *State) ResetDaily() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.DailyPnL = 0
	s.DailyTrades = 0
	s.TradingHalted = false
	s.HaltReason = ""
}

// CanTrade returns true if trading is allowed
func (s *State) CanTrade() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.TradingHalted
}

// GetGreeks returns current portfolio Greeks
func (s *State) GetGreeks() Greeks {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Greeks
}

// TotalUnrealizedPnL returns sum of unrealized P&L
func (s *State) TotalUnrealizedPnL() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := 0.0
	for _, p := range s.Positions {
		total += p.UnrealizedPnL
	}
	return total
}

// HasOpenPositions returns true if there are open positions
func (s *State) HasOpenPositions() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Positions) > 0
}

func (s *State) recalculateGreeks() {
	s.Greeks = Greeks{}
	for _, p := range s.Positions {
		qty := float64(p.Qty)
		s.Greeks.NetDelta += p.Delta * qty
		s.Greeks.NetGamma += p.Gamma * qty
		s.Greeks.NetTheta += p.Theta * qty
		s.Greeks.NetVega += p.Vega * qty
	}
}

// TransactionCosts models fees and slippage
type TransactionCosts struct {
	MakerFeeRate float64 // e.g., 0.0001 (0.01%)
	TakerFeeRate float64 // e.g., 0.0005 (0.05%)
	SlippageBps  float64 // Expected slippage in basis points
}

func DefaultTransactionCosts() TransactionCosts {
	return TransactionCosts{
		MakerFeeRate: 0.0001, // 0.01%
		TakerFeeRate: 0.0005, // 0.05%
		SlippageBps:  5,      // 5 bps typical slippage
	}
}

func (tc TransactionCosts) EstimateCost(notional float64, isMaker bool, legs int) float64 {
	feeRate := tc.TakerFeeRate
	if isMaker {
		feeRate = tc.MakerFeeRate
	}

	// Fee per leg
	fees := notional * feeRate * float64(legs)

	// Slippage (both entry and exit, so 2x)
	slippage := notional * (tc.SlippageBps / 10000) * 2 * float64(legs)

	return fees + slippage
}

// RiskLimits defines portfolio risk constraints
type RiskLimits struct {
	MaxMarginPct            float64 // max margin usage %
	MaxDeltaAbs             float64 // max absolute delta
	MaxShortGamma           float64 // max short gamma exposure
	MaxVegaAbs              float64 // max absolute vega
	MaxPositionsTotal       int     // max number of positions
	MaxPositionsPerStrategy int
	MaxDailyLoss            float64
	CooldownAfterLoss       time.Duration
	MaxTotalRisk            float64 // max total portfolio risk (sum of max losses)
	MaxStrategyRisk         float64 // max risk per strategy
}

// DefaultRiskLimits returns conservative defaults
func DefaultRiskLimits() RiskLimits {
	return RiskLimits{
		MaxMarginPct:            0.5, // 50% max margin
		MaxDeltaAbs:             5.0, // max 5 delta exposure
		MaxShortGamma:           1.0, // limit short gamma
		MaxVegaAbs:              100, // max vega exposure
		MaxPositionsTotal:       20,
		MaxPositionsPerStrategy: 4,
		MaxDailyLoss:            1000,
		CooldownAfterLoss:       30 * time.Minute,
		MaxTotalRisk:            5000, // max $5000 total portfolio risk
		MaxStrategyRisk:         1000, // max $1000 risk per strategy
	}
}

// CheckLimits checks if an action would violate limits
func (s *State) CheckLimits(limits RiskLimits, additionalDelta, additionalGamma float64) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Already halted?
	if s.TradingHalted {
		return &LimitError{Limit: "trading_halted", Message: s.HaltReason}
	}

	// Margin check
	if s.MarginPct > limits.MaxMarginPct {
		return &LimitError{Limit: "margin", Message: "margin usage too high"}
	}

	// Delta check
	newDelta := s.Greeks.NetDelta + additionalDelta
	if abs(newDelta) > limits.MaxDeltaAbs {
		return &LimitError{Limit: "delta", Message: "would exceed max delta"}
	}

	// Short gamma check
	newGamma := s.Greeks.NetGamma + additionalGamma
	if newGamma < -limits.MaxShortGamma {
		return &LimitError{Limit: "gamma", Message: "would exceed max short gamma"}
	}

	// Position count
	if len(s.Positions) >= limits.MaxPositionsTotal {
		return &LimitError{Limit: "positions", Message: "too many positions"}
	}

	return nil
}

// CheckLimitsWithRisk checks limits including max loss budget
func (s *State) CheckLimitsWithRisk(limits RiskLimits, additionalDelta, additionalGamma, additionalMaxLoss float64) error {
	// First check standard limits
	if err := s.CheckLimits(limits, additionalDelta, additionalGamma); err != nil {
		return err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check per-strategy risk limit
	if additionalMaxLoss > limits.MaxStrategyRisk {
		return &LimitError{Limit: "strategy_risk", Message: "strategy max loss exceeds limit"}
	}

	// Check total portfolio risk limit
	currentTotalRisk := s.GetTotalRiskLocked()
	if currentTotalRisk+additionalMaxLoss > limits.MaxTotalRisk {
		return &LimitError{Limit: "total_risk", Message: "would exceed portfolio max risk"}
	}

	return nil
}

// GetTotalRiskLocked returns total risk - caller must hold lock
func (s *State) GetTotalRiskLocked() float64 {
	totalRisk := 0.0
	// Sum up unrealized losses as a proxy for current risk
	for _, p := range s.Positions {
		if p.UnrealizedPnL < 0 {
			totalRisk -= p.UnrealizedPnL
		}
	}
	// Also add potential max loss from strategy entries
	// (This would require tracking strategy metadata - simplified here)
	return totalRisk
}

type LimitError struct {
	Limit   string
	Message string
}

func (e *LimitError) Error() string {
	return e.Limit + ": " + e.Message
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
