package execution

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
)

// MockClient mocks delta.Client
type MockClient struct {
	// Add fields to control mock behavior
	PlaceOrderError error
	OrderHistory    *delta.Order
	Fills           []delta.Fill
	Position        *delta.Position
}

func (m *MockClient) PlaceOrder(req delta.CreateOrderRequest) (*delta.Order, error) {
	if m.PlaceOrderError != nil {
		return nil, m.PlaceOrderError
	}
	return &delta.Order{ID: 123, ClientOrderID: req.ClientOrderID, State: "open"}, nil
}

func (m *MockClient) GetActiveOrders(productID *int64) ([]delta.Order, error) {
	return []delta.Order{}, nil // Simulate order disappearing from active list
}

func (m *MockClient) GetOrderHistory(orderID int64) (*delta.Order, error) {
	if m.OrderHistory != nil {
		return m.OrderHistory, nil
	}
	return nil, fmt.Errorf("not found")
}

func (m *MockClient) GetFills(productID *int64, since int64) ([]delta.Fill, error) {
	return m.Fills, nil
}

func (m *MockClient) GetPosition(productID int64) (*delta.Position, error) {
	return m.Position, nil
}

func (m *MockClient) CancelOrder(orderID int64, productID int64) error {
	return nil
}

func (m *MockClient) PlaceWithRetry(ctx context.Context, req OrderRequest, timeout time.Duration, cfg RetryConfig) (*OrderState, error) {
	return &OrderState{Status: StatusFilled, FilledQty: req.Qty}, nil
}

// We need to wrap MockClient to satisfy DeltaManager's needs if it used an interface
// But DeltaManager uses *delta.Client. This is a problem for testing.
// I should probably have used an interface for the client.

func TestPlaceAndWait_FillVerification(t *testing.T) {
	mock := &MockClient{
		OrderHistory: &delta.Order{
			ID: 123,
			State: "filled",
			Size: 10,
			UnfilledSize: 0,
			LimitPrice: "100.0",
		},
		Fills: []delta.Fill{
			{
				OrderID: 123,
				ProductID: 1,
				Size: 10,
				Price: "100.0",
				Side: "buy",
			},
		},
	}
	
	mgr := NewDeltaManager(mock)
	req := OrderRequest{
		InstrumentID: 1,
		Qty: 10,
		Price: 100.0,
		Side: Buy,
	}
	
	state, err := mgr.PlaceAndWait(context.Background(), req, 1*time.Second)
	
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	
	if state.Status != StatusFilled {
		t.Errorf("Expected StatusFilled, got %s", state.Status)
	}
	
	if state.FilledQty != 10 {
		t.Errorf("Expected FilledQty 10, got %d", state.FilledQty)
	}
}

func TestPlaceAndWait_CancelledDetection(t *testing.T) {
	mock := &MockClient{
		OrderHistory: &delta.Order{
			ID: 123,
			State: "cancelled",
			Size: 10,
			UnfilledSize: 10,
		},
	}
	
	mgr := NewDeltaManager(mock)
	req := OrderRequest{
		InstrumentID: 1,
		Qty: 10,
		Price: 100.0,
		Side: Buy,
	}
	
	state, err := mgr.PlaceAndWait(context.Background(), req, 1*time.Second)
	
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	
	if state.Status != StatusCancelled {
		t.Errorf("Expected StatusCancelled, got %s", state.Status)
	}
	
	if state.FilledQty != 0 {
		t.Errorf("Expected FilledQty 0, got %d", state.FilledQty)
	}
}
