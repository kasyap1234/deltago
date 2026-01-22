package execution

import (
	"context"
	"fmt"
	"time"
)

type Side string

const (
	Buy  Side = "buy"
	Sell Side = "sell"
)

type OrderType string

const (
	Limit  OrderType = "limit_order"
	Market OrderType = "market_order"
)

type OrderStatus string

const (
	StatusPending   OrderStatus = "pending"
	StatusOpen      OrderStatus = "open"
	StatusFilled    OrderStatus = "filled"
	StatusPartial   OrderStatus = "partial"
	StatusCancelled OrderStatus = "cancelled"
	StatusRejected  OrderStatus = "rejected"
)

// OrderRequest represents an order to place
type OrderRequest struct {
	ClientOrderID string
	InstrumentID  int64
	Symbol        string
	Side          Side
	Qty           int
	Price         float64
	OrderType     OrderType
	PostOnly      bool
	ReduceOnly    bool
	TimeInForce   string // gtc, ioc, fok
	StrategyID    string // for tracking
	LegID         string // for multi-leg strategies
	Priority      int    // execution priority (1=highest, close shorts before longs)
}

// OrderAck is the acknowledgment after placing
type OrderAck struct {
	ExchangeOrderID int64
	ClientOrderID   string
	Status          OrderStatus
	Message         string
}

// Fill represents a filled order
type Fill struct {
	ExchangeOrderID int64
	ClientOrderID   string
	InstrumentID    int64
	Symbol          string
	Side            Side
	Qty             int
	Price           float64
	Fee             float64
	Timestamp       time.Time
}

// OrderState tracks an order through its lifecycle
type OrderState struct {
	Request         OrderRequest
	ExchangeOrderID int64
	Status          OrderStatus
	FilledQty       int
	AvgFillPrice    float64
	Fills           []Fill
	PlacedAt        time.Time
	UpdatedAt       time.Time
	Error           error
}

// IsTerminal returns true if order is in a final state
func (o *OrderState) IsTerminal() bool {
	return o.Status == StatusFilled ||
		o.Status == StatusCancelled ||
		o.Status == StatusRejected
}

// RemainingQty returns unfilled quantity
func (o *OrderState) RemainingQty() int {
	return o.Request.Qty - o.FilledQty
}

// Manager handles order execution with verification
type Manager interface {
	// Place places an order and returns immediately
	Place(ctx context.Context, req OrderRequest) (*OrderAck, error)

	// PlaceAndWait places an order and waits for fill or timeout
	PlaceAndWait(ctx context.Context, req OrderRequest, timeout time.Duration) (*OrderState, error)

	// Cancel cancels an order
	Cancel(ctx context.Context, exchangeOrderID int64, instrumentID int64) error

	// GetOrder gets current order state
	GetOrder(ctx context.Context, exchangeOrderID int64) (*OrderState, error)

	// GetFills gets recent fills
	GetFills(ctx context.Context, since time.Time) ([]Fill, error)

	// PlaceWithRetry places an order with retry logic and price walking
	PlaceWithRetry(ctx context.Context, req OrderRequest, timeout time.Duration, cfg RetryConfig) (*OrderState, error)
}

// MultiLegOrder represents a multi-leg options order
type MultiLegOrder struct {
	StrategyID string
	Legs       []OrderRequest
	Timeout    time.Duration
	AllOrNone  bool        // If true, cancel all if any leg fails
	Metadata   any         // Strategy-specific metadata (e.g., expected greeks)
	UseRetry   bool        // If true, use price walking retry logic
	RetryCfg   RetryConfig // Custom retry config
}

// MultiLegResult tracks the result of a multi-leg execution
type MultiLegResult struct {
	StrategyID  string
	LegResults  map[string]*OrderState // by LegID
	FullyFilled bool
	Error       error
	StartedAt   time.Time
	CompletedAt time.Time
}

// ExecuteMultiLeg executes a multi-leg order with proper sequencing
// Buys protection legs first, then sells short legs
func ExecuteMultiLeg(ctx context.Context, mgr Manager, order MultiLegOrder) (*MultiLegResult, error) {
	result := &MultiLegResult{
		StrategyID: order.StrategyID,
		LegResults: make(map[string]*OrderState),
		StartedAt:  time.Now(),
	}

	// Separate buy and sell legs
	var buyLegs, sellLegs []OrderRequest
	for _, leg := range order.Legs {
		if leg.Side == Buy {
			buyLegs = append(buyLegs, leg)
		} else {
			sellLegs = append(sellLegs, leg)
		}
	}

	// Execute buy legs first (protection)
	for _, leg := range buyLegs {
		var state *OrderState
		var err error

		if order.UseRetry {
			state, err = mgr.PlaceWithRetry(ctx, leg, order.Timeout, order.RetryCfg)
		} else {
			state, err = mgr.PlaceAndWait(ctx, leg, order.Timeout)
		}

		result.LegResults[leg.LegID] = state

		if err != nil || (state != nil && state.Status != StatusFilled) {
			result.Error = fmt.Errorf("buy leg %s failed: %w", leg.LegID, err)
			if order.AllOrNone {
				// Rollback: close all filled buy legs
				for legID, legState := range result.LegResults {
					if legState != nil && legState.Status == StatusFilled {
						closeReq := OrderRequest{
							InstrumentID: legState.Request.InstrumentID,
							Symbol:       legState.Request.Symbol,
							Side:         Sell,
							Qty:          legState.FilledQty,
							OrderType:    Market,
							ReduceOnly:   true,
							LegID:        legID + "_close",
						}
						mgr.PlaceAndWait(ctx, closeReq, 30*time.Second)
					}
				}
			}
			result.CompletedAt = time.Now()
			return result, result.Error
		}
	}

	// Execute sell legs (now protected by buy legs)
	for _, leg := range sellLegs {
		var state *OrderState
		var err error

		if order.UseRetry {
			state, err = mgr.PlaceWithRetry(ctx, leg, order.Timeout, order.RetryCfg)
		} else {
			state, err = mgr.PlaceAndWait(ctx, leg, order.Timeout)
		}

		result.LegResults[leg.LegID] = state

		if err != nil || (state != nil && state.Status != StatusFilled) {
			result.Error = fmt.Errorf("sell leg %s failed: %w", leg.LegID, err)
			if order.AllOrNone {
				// Rollback all legs
				for legID, legState := range result.LegResults {
					if legState != nil && legState.Status == StatusFilled {
						closeSide := Buy
						if legState.Request.Side == Buy {
							closeSide = Sell
						}
						closeReq := OrderRequest{
							InstrumentID: legState.Request.InstrumentID,
							Symbol:       legState.Request.Symbol,
							Side:         closeSide,
							Qty:          legState.FilledQty,
							OrderType:    Market,
							ReduceOnly:   true,
							LegID:        legID + "_close",
						}
						mgr.PlaceAndWait(ctx, closeReq, 30*time.Second)
					}
				}
			}
			result.CompletedAt = time.Now()
			return result, result.Error
		}
	}

	result.FullyFilled = true
	result.CompletedAt = time.Now()
	return result, nil
}

// ExecutionMode defines whether to prioritize Maker or Taker fills
type ExecutionMode string

const (
	ModeMaker ExecutionMode = "maker"
	ModeTaker ExecutionMode = "taker"
)

// RetryConfig configures order retry behavior
type RetryConfig struct {
	MaxRetries    int
	PriceStepPct  float64 // How much to walk price each retry
	RetryInterval time.Duration
	AllowCrossing bool          // Allow crossing spread on final retry
	Mode          ExecutionMode // maker or taker
}

// GenerateClientOrderID creates a deterministic order ID
func GenerateClientOrderID(strategyID, legID string, timestamp time.Time) string {
	return fmt.Sprintf("%s_%s", strategyID, legID)
}
