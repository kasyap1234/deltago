package delta

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// PlaceOrder places a single order
// Uses post_only=true to ensure maker fees
func (c *Client) PlaceOrder(req CreateOrderRequest) (*Order, error) {
	// Ensure post_only is true for lowest fees
	req.PostOnly = true

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
func (c *Client) ClosePosition(productSymbol string, productID int64, size int, side OrderSide, limitPrice string) (*Order, error) {
	return c.PlaceOrder(CreateOrderRequest{
		ProductID:     productID,
		ProductSymbol: productSymbol,
		Size:          size,
		Side:          side,
		OrderType:     OrderTypeLimit,
		LimitPrice:    limitPrice,
		TimeInForce:   TimeInForceGTC,
		PostOnly:      true,
		ReduceOnly:    true,
	})
}
