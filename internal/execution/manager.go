package execution

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
)

// DeltaManager implements Manager for Delta Exchange
type DeltaManager struct {
	client *delta.Client
}

// NewDeltaManager creates a new execution manager
func NewDeltaManager(client *delta.Client) *DeltaManager {
	return &DeltaManager{client: client}
}

func (m *DeltaManager) Place(ctx context.Context, req OrderRequest) (*OrderAck, error) {
	tif := delta.TimeInForceGTC
	if req.TimeInForce == "ioc" {
		tif = delta.TimeInForceIOC
	}

	orderReq := delta.CreateOrderRequest{
		ProductID:     req.InstrumentID,
		ProductSymbol: req.Symbol,
		Size:          req.Qty,
		Side:          delta.OrderSide(req.Side),
		OrderType:     delta.OrderType(req.OrderType),
		LimitPrice:    fmt.Sprintf("%.2f", req.Price),
		TimeInForce:   tif,
		PostOnly:      req.PostOnly,
		ReduceOnly:    req.ReduceOnly,
		ClientOrderID: req.ClientOrderID,
	}

	order, err := m.client.PlaceOrder(orderReq)
	if err != nil {
		return nil, fmt.Errorf("place order failed: %w", err)
	}

	return &OrderAck{
		ExchangeOrderID: order.ID,
		ClientOrderID:   order.ClientOrderID,
		Status:          mapOrderState(order.State),
	}, nil
}

func (m *DeltaManager) PlaceAndWait(ctx context.Context, req OrderRequest, timeout time.Duration) (*OrderState, error) {
	// Snapshot position before placing order for verification
	prePosition, _ := m.client.GetPosition(req.InstrumentID)
	preSize := 0
	if prePosition != nil {
		preSize = prePosition.Size
	}

	ack, err := m.Place(ctx, req)
	if err != nil {
		return &OrderState{
			Request: req,
			Status:  StatusRejected,
			Error:   err,
		}, err
	}

	state := &OrderState{
		Request:         req,
		ExchangeOrderID: ack.ExchangeOrderID,
		Status:          ack.Status,
		PlacedAt:        time.Now(),
		Fills:           make([]Fill, 0),
	}

	// Poll until filled or timeout
	deadline := time.Now().Add(timeout)
	pollInterval := 500 * time.Millisecond

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			// Context cancelled - try to cancel order
			_ = m.Cancel(ctx, ack.ExchangeOrderID, req.InstrumentID)
			state.Status = StatusCancelled
			state.Error = ctx.Err()
			return state, ctx.Err()
		case <-time.After(pollInterval):
		}

		// Get order status from active orders
		orders, err := m.client.GetActiveOrders(&req.InstrumentID)
		if err != nil {
			continue // retry
		}

		// Check if order is still active
		found := false
		for _, o := range orders {
			if o.ID == ack.ExchangeOrderID {
				found = true
				state.FilledQty = o.Size - o.UnfilledSize
				state.Status = mapOrderState(o.State)
				if price, err := strconv.ParseFloat(o.LimitPrice, 64); err == nil {
					state.AvgFillPrice = price
				}
				break
			}
		}

		if !found {
			// CRITICAL: Order no longer in active list
			// DO NOT assume filled - verify through multiple sources

			// Method 1: Check order history for final state
			histOrder, err := m.client.GetOrderHistory(ack.ExchangeOrderID)
			if err == nil {
				state.Status = mapOrderState(histOrder.State)
				state.FilledQty = histOrder.Size - histOrder.UnfilledSize
				if price, err := strconv.ParseFloat(histOrder.LimitPrice, 64); err == nil {
					state.AvgFillPrice = price
				}
			}

			// Method 2: Verify via fills endpoint
			fills, err := m.client.GetFills(&req.InstrumentID, state.PlacedAt.Unix())
			if err == nil {
				totalFilled := 0
				totalValue := 0.0
				for _, fill := range fills {
					if fill.OrderID == ack.ExchangeOrderID {
						totalFilled += fill.Size
						if price, err := strconv.ParseFloat(fill.Price, 64); err == nil {
							totalValue += float64(fill.Size) * price
						}

						// Convert delta.Fill to execution.Fill
						execFill := Fill{
							ExchangeOrderID: fill.OrderID,
							InstrumentID:    fill.ProductID,
							Symbol:          fill.ProductSymbol,
							Side:            Side(fill.Side),
							Qty:             fill.Size,
							Timestamp:       time.Unix(fill.Timestamp, 0),
						}
						if price, err := strconv.ParseFloat(fill.Price, 64); err == nil {
							execFill.Price = price
						}
						if fee, err := strconv.ParseFloat(fill.Commission, 64); err == nil {
							execFill.Fee = fee
						}
						state.Fills = append(state.Fills, execFill)
					}
				}
				if totalFilled > 0 {
					state.FilledQty = totalFilled
					state.AvgFillPrice = totalValue / float64(totalFilled)
				}
			}

			// Method 3: Verify via position change
			postPosition, _ := m.client.GetPosition(req.InstrumentID)
			postSize := 0
			if postPosition != nil {
				postSize = postPosition.Size
			}

			positionChange := postSize - preSize
			expectedChange := req.Qty
			if req.Side == Sell {
				expectedChange = -req.Qty
			}

			// Cross-validate: if position changed as expected, order likely filled
			if positionChange == expectedChange && state.FilledQty == 0 {
				// Position confirms fill but we have no fill records - trust position
				state.FilledQty = req.Qty
				state.Status = StatusFilled
			}

			// Final determination
			if state.FilledQty == 0 {
				// No evidence of fills - order was cancelled or rejected
				if state.Status == StatusOpen || state.Status == StatusPending {
					state.Status = StatusCancelled // Safe default
				}
			} else if state.FilledQty == req.Qty {
				state.Status = StatusFilled
			} else {
				state.Status = StatusPartial
			}

			state.UpdatedAt = time.Now()
			return state, nil
		}

		if state.Status == StatusFilled {
			state.UpdatedAt = time.Now()
			return state, nil
		}
	}

	// Timeout - cancel remaining and return partial
	if state.RemainingQty() > 0 {
		_ = m.Cancel(ctx, ack.ExchangeOrderID, req.InstrumentID)
		if state.FilledQty > 0 {
			state.Status = StatusPartial
		} else {
			state.Status = StatusCancelled
		}
	}

	state.UpdatedAt = time.Now()
	state.Error = fmt.Errorf("order timeout after %v", timeout)
	return state, state.Error
}

func (m *DeltaManager) Cancel(ctx context.Context, exchangeOrderID int64, instrumentID int64) error {
	return m.client.CancelOrder(exchangeOrderID, instrumentID)
}

func (m *DeltaManager) GetOrder(ctx context.Context, exchangeOrderID int64) (*OrderState, error) {
	// Delta doesn't have a direct get-order-by-ID, so we check active orders
	orders, err := m.client.GetActiveOrders(nil)
	if err != nil {
		return nil, err
	}

	for _, o := range orders {
		if o.ID == exchangeOrderID {
			return &OrderState{
				ExchangeOrderID: o.ID,
				Status:          mapOrderState(o.State),
				FilledQty:       o.Size - o.UnfilledSize,
			}, nil
		}
	}

	return nil, fmt.Errorf("order %d not found", exchangeOrderID)
}

func (m *DeltaManager) GetFills(ctx context.Context, since time.Time) ([]Fill, error) {
	// TODO: implement via Delta API fills endpoint
	return nil, nil
}

func mapOrderState(state string) OrderStatus {
	switch state {
	case "open":
		return StatusOpen
	case "pending":
		return StatusPending
	case "filled":
		return StatusFilled
	case "cancelled":
		return StatusCancelled
	default:
		return StatusPending
	}
}
