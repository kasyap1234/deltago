package strategies

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
	"github.com/kiwhtas/deltago/internal/execution"
	"github.com/kiwhtas/deltago/internal/portfolio"
	"github.com/kiwhtas/deltago/internal/regime"
)

// Input contains all context for strategy decisions
type Input struct {
	Regime    *regime.Regime
	Snapshot  *MarketSnapshot
	Portfolio *portfolio.State
	Clock     time.Time
}

// MarketSnapshot contains current market data
type MarketSnapshot struct {
	Underlying string
	SpotPrice  float64
	Options    []delta.Ticker // option chain
	Timestamp  time.Time
}

// Strategy interface for all options strategies
type Strategy interface {
	// Identification
	ID() string
	Name() string

	// Which regimes this strategy works in
	SuitableRegimes() []regime.TrendState
	PreferredVol() regime.VolState // HIGH, LOW, or NORMAL (any)

	// Entry decision
	ShouldEnter(ctx context.Context, in Input) (bool, string, error)

	// Build orders for entry
	BuildEntryOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error)

	// CRITICAL: ConfirmEntry is called AFTER fills are verified
	// This ensures position state is only set when we actually have the position
	ConfirmEntry(ctx context.Context, result *execution.MultiLegResult, metadata *StrategyPositionMetadata) error

	// Ongoing management (stop loss, take profit, rolling)
	Manage(ctx context.Context, in Input) ([]execution.OrderRequest, error)

	// Build orders to close position
	BuildCloseOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error)

	// Update internal state after fills
	OnFill(ctx context.Context, fill execution.Fill) error

	// Get current position info
	GetPosition() *StrategyPosition

	// Is strategy currently in a position?
	HasPosition() bool
}

// StrategyPosition tracks a strategy's current position
type StrategyPosition struct {
	StrategyID    string
	Legs          []Leg
	EntryTime     time.Time
	NetPremium    float64 // positive = credit, negative = debit
	MaxLoss       float64
	MaxProfit     float64
	CurrentPnL    float64
	BreakevenLow  float64
	BreakevenHigh float64
}

// StrategyPositionMetadata contains metadata about a proposed position
type StrategyPositionMetadata struct {
	NetPremium    float64
	MaxLoss       float64
	MaxProfit     float64
	BreakevenLow  float64
	BreakevenHigh float64
	Legs          []Leg
}

// Leg represents one leg of an options position
type Leg struct {
	ID           string
	Symbol       string
	InstrumentID int64
	OptionType   string // call, put
	Strike       float64
	Expiry       time.Time
	Side         execution.Side
	Qty          int
	EntryPrice   float64
	CurrentPrice float64
	Delta        float64
	Gamma        float64
	Theta        float64
	Vega         float64
}

// BaseStrategy provides common functionality
type BaseStrategy struct {
	id       string
	name     string
	client   *delta.Client
	position *StrategyPosition
	mu       sync.RWMutex // Protects position access

	// Configuration
	PositionSize       int
	StopLossMultiplier float64
	TakeProfitPct      float64
	MaxDTE             int // max days to expiry
}

func (b *BaseStrategy) ID() string   { return b.id }
func (b *BaseStrategy) Name() string { return b.name }

func (b *BaseStrategy) HasPosition() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.position != nil && len(b.position.Legs) > 0
}

func (b *BaseStrategy) GetPosition() *StrategyPosition {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.position
}

// SetPosition sets the position with proper locking
func (b *BaseStrategy) SetPosition(pos *StrategyPosition) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.position = pos
}

// ClearPosition clears the position with proper locking
func (b *BaseStrategy) ClearPosition() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.position = nil
}

func (b *BaseStrategy) OnFill(ctx context.Context, fill execution.Fill) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.position == nil {
		return nil
	}

	for i := range b.position.Legs {
		if b.position.Legs[i].InstrumentID == fill.InstrumentID {
			b.position.Legs[i].EntryPrice = fill.Price
		}
	}
	return nil
}

// FindOptionByDelta finds an option with delta closest to target
func FindOptionByDelta(options []delta.Ticker, optionType string, targetDelta float64) *delta.Ticker {
	var best *delta.Ticker
	bestDiff := 999.0

	for i := range options {
		opt := &options[i]
		if opt.ContractType != optionType {
			continue
		}

		delta := parseFloat(opt.Greeks.Delta)
		diff := abs(abs(delta) - abs(targetDelta))

		if diff < bestDiff {
			bestDiff = diff
			best = opt
		}
	}
	return best
}

// FindATMOption finds the ATM option
func FindATMOption(options []delta.Ticker, optionType string, spotPrice float64) *delta.Ticker {
	var best *delta.Ticker
	bestDiff := 999999.0

	for i := range options {
		opt := &options[i]
		if opt.ContractType != optionType {
			continue
		}

		strike := parseFloat(opt.StrikePrice)
		diff := abs(strike - spotPrice)

		if diff < bestDiff {
			bestDiff = diff
			best = opt
		}
	}
	return best
}

// FindOptionByStrike finds option at specific strike
func FindOptionByStrike(options []delta.Ticker, optionType string, strike float64) *delta.Ticker {
	for i := range options {
		opt := &options[i]
		if opt.ContractType != optionType {
			continue
		}

		optStrike := parseFloat(opt.StrikePrice)
		if abs(optStrike-strike) < 0.01 {
			return opt
		}
	}
	return nil
}

// GetNextStrike returns the next higher/lower strike
func GetNextStrike(options []delta.Ticker, optionType string, currentStrike float64, higher bool) *delta.Ticker {
	var best *delta.Ticker
	bestDiff := 999999.0

	for i := range options {
		opt := &options[i]
		if opt.ContractType != optionType {
			continue
		}

		strike := parseFloat(opt.StrikePrice)

		if higher && strike > currentStrike {
			diff := strike - currentStrike
			if diff < bestDiff {
				bestDiff = diff
				best = opt
			}
		} else if !higher && strike < currentStrike {
			diff := currentStrike - strike
			if diff < bestDiff {
				bestDiff = diff
				best = opt
			}
		}
	}
	return best
}

func parseFloat(s string) float64 {
	val, _ := strconv.ParseFloat(s, 64)
	return val
}

// ParseError represents a parsing error with context
type ParseError struct {
	Field string
	Value string
	Err   error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("failed to parse %s '%s': %v", e.Field, e.Value, e.Err)
}

// parseFloatRequired parses a float with error handling - use for critical fields
func parseFloatRequired(s string, fieldName string) (float64, error) {
	if s == "" {
		return 0, &ParseError{Field: fieldName, Value: s, Err: fmt.Errorf("empty value")}
	}
	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, &ParseError{Field: fieldName, Value: s, Err: err}
	}
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return 0, &ParseError{Field: fieldName, Value: s, Err: fmt.Errorf("NaN or Inf")}
	}
	return val, nil
}

// parseFloatOptional parses a float with a default fallback - use for non-critical fields
func parseFloatOptional(s string, defaultVal float64) float64 {
	if s == "" {
		return defaultVal
	}
	val, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(val) || math.IsInf(val, 0) {
		return defaultVal
	}
	return val
}

// ValidateOption validates an option has valid quotes and greeks
func ValidateOption(opt *delta.Ticker, requireGreeks bool) error {
	if opt == nil {
		return fmt.Errorf("option is nil")
	}

	// Validate quotes
	bid, err := parseFloatRequired(opt.Quotes.BestBid, "BestBid")
	if err != nil {
		return err
	}
	ask, err := parseFloatRequired(opt.Quotes.BestAsk, "BestAsk")
	if err != nil {
		return err
	}

	if bid <= 0 || ask <= 0 {
		return fmt.Errorf("invalid quotes: bid=%.4f ask=%.4f", bid, ask)
	}

	if ask < bid {
		return fmt.Errorf("crossed market: bid=%.4f > ask=%.4f", bid, ask)
	}

	// Validate greeks if required
	if requireGreeks {
		delta, err := parseFloatRequired(opt.Greeks.Delta, "Delta")
		if err != nil {
			return err
		}
		if delta < -1 || delta > 1 {
			return fmt.Errorf("invalid delta: %.4f", delta)
		}
	}

	return nil
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
