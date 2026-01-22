package delta

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// PlaceOrder places a single order
// PostOnly is controlled by the caller - do not override
func (c *Client) PlaceOrder(req CreateOrderRequest) (*Order, error) {
	resp, err := c.doRequest("POST", "/v2/orders", nil, req)
	if err != nil {
		return nil, err
	}

	var result APIResponse[Order]
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse order response: %w", err)
	}

	if !result.Success {
		if result.Error != nil {
			return nil, fmt.Errorf("order failed: %s (code: %d)", result.Error.Message, result.Error.Code)
		}
		return nil, fmt.Errorf("order failed: unknown error")
	}

	return &result.Result, nil
}

// PlaceBatchOrders places multiple orders atomically (max 50)
// All orders use post_only=true for maker fees
func (c *Client) PlaceBatchOrders(req BatchOrderRequest) ([]Order, error) {
	// Ensure all orders have post_only=true
	for i := range req.Orders {
		req.Orders[i].PostOnly = true
	}

	resp, err := c.doRequest("POST", "/v2/orders/batch", nil, req)
	if err != nil {
		return nil, err
	}

	var result APIResponse[[]Order]
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse batch order response: %w", err)
	}

	if !result.Success {
		if result.Error != nil {
			return nil, fmt.Errorf("batch order failed: %s (code: %d)", result.Error.Message, result.Error.Code)
		}
		return nil, fmt.Errorf("batch order failed: unknown error")
	}

	return result.Result, nil
}

// CancelOrder cancels an order by ID
func (c *Client) CancelOrder(orderID int64, productID int64) error {
	query := url.Values{}
	query.Set("id", fmt.Sprintf("%d", orderID))
	query.Set("product_id", fmt.Sprintf("%d", productID))

	resp, err := c.doRequest("DELETE", "/v2/orders", query, nil)
	if err != nil {
		return err
	}

	var result APIResponse[Order]
	if err := json.Unmarshal(resp, &result); err != nil {
		return fmt.Errorf("failed to parse cancel response: %w", err)
	}

	if !result.Success {
		if result.Error != nil {
			return fmt.Errorf("cancel failed: %s (code: %d)", result.Error.Message, result.Error.Code)
		}
	}

	return nil
}

// CancelAllOrders cancels all open orders, optionally filtered by product
func (c *Client) CancelAllOrders(productID *int64) error {
	body := map[string]interface{}{}
	if productID != nil {
		body["product_id"] = *productID
	}

	_, err := c.doRequest("DELETE", "/v2/orders/all", nil, body)
	return err
}

// GetActiveOrders returns all active orders
func (c *Client) GetActiveOrders(productID *int64) ([]Order, error) {
	query := url.Values{}
	query.Set("state", "open")
	if productID != nil {
		query.Set("product_id", fmt.Sprintf("%d", *productID))
	}

	resp, err := c.doRequest("GET", "/v2/orders", query, nil)
	if err != nil {
		return nil, err
	}

	var result APIResponse[[]Order]
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse orders response: %w", err)
	}

	return result.Result, nil
}

// SellOption places a sell order for an option
// Uses limit order at best bid for maker execution
func (c *Client) SellOption(productSymbol string, productID int64, size int, limitPrice string) (*Order, error) {
	return c.PlaceOrder(CreateOrderRequest{
		ProductID:     productID,
		ProductSymbol: productSymbol,
		Size:          size,
		Side:          OrderSideSell,
		OrderType:     OrderTypeLimit,
		LimitPrice:    limitPrice,
		TimeInForce:   TimeInForceGTC,
		PostOnly:      true,
	})
}

// BuyOption places a buy order for an option
// Uses limit order at best ask for maker execution
func (c *Client) BuyOption(productSymbol string, productID int64, size int, limitPrice string) (*Order, error) {
	return c.PlaceOrder(CreateOrderRequest{
		ProductID:     productID,
		ProductSymbol: productSymbol,
		Size:          size,
		Side:          OrderSideBuy,
		OrderType:     OrderTypeLimit,
		LimitPrice:    limitPrice,
		TimeInForce:   TimeInForceGTC,
		PostOnly:      true,
	})
}

// ClosePosition places a reduce-only order to close a position
// Uses IOC (Immediate Or Cancel) and NO post_only to ensure execution
func (c *Client) ClosePosition(productSymbol string, productID int64, size int, side OrderSide, limitPrice string) (*Order, error) {
	return c.PlaceOrder(CreateOrderRequest{
		ProductID:     productID,
		ProductSymbol: productSymbol,
		Size:          size,
		Side:          side,
		OrderType:     OrderTypeLimit,
		LimitPrice:    limitPrice,
		TimeInForce:   TimeInForceIOC, // IOC for immediate execution
		PostOnly:      false,          // Allow crossing spread for exits
		ReduceOnly:    true,
	})
}

// GetOrderHistory fetches the final state of an order from history
// This is critical for verifying if an order was filled, cancelled, or rejected
func (c *Client) GetOrderHistory(orderID int64) (*Order, error) {
	query := url.Values{}
	query.Set("order_id", fmt.Sprintf("%d", orderID))

	resp, err := c.doRequest("GET", "/v2/orders/history", query, nil)
	if err != nil {
		return nil, err
	}

	var result APIResponse[[]Order]
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse order history response: %w", err)
	}

	if !result.Success || len(result.Result) == 0 {
		return nil, fmt.Errorf("order %d not found in history", orderID)
	}

	return &result.Result[0], nil
}

// GetFills fetches fill records for verification
// productID can be nil to get all fills
func (c *Client) GetFills(productID *int64, since int64) ([]Fill, error) {
	query := url.Values{}
	if since > 0 {
		query.Set("start_time", fmt.Sprintf("%d", since))
	}
	if productID != nil {
		query.Set("product_id", fmt.Sprintf("%d", *productID))
	}

	resp, err := c.doRequest("GET", "/v2/fills", query, nil)
	if err != nil {
		return nil, err
	}

	var result APIResponse[[]Fill]
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse fills response: %w", err)
	}

	return result.Result, nil
}
