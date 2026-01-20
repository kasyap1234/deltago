package delta

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// GetPositions returns all open margined positions
func (c *Client) GetPositions() ([]Position, error) {
	resp, err := c.doRequest("GET", "/v2/positions/margined", nil, nil)
	if err != nil {
		return nil, err
	}

	var result APIResponse[[]Position]
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse positions response: %w", err)
	}

	return result.Result, nil
}

// GetPosition returns a specific position by product ID
func (c *Client) GetPosition(productID int64) (*Position, error) {
	query := url.Values{}
	query.Set("product_id", fmt.Sprintf("%d", productID))

	resp, err := c.doRequest("GET", "/v2/positions", query, nil)
	if err != nil {
		return nil, err
	}

	var result APIResponse[Position]
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse position response: %w", err)
	}

	return &result.Result, nil
}

// CloseAllPositions closes all open positions
func (c *Client) CloseAllPositions() error {
	_, err := c.doRequest("POST", "/v2/positions/close_all", nil, map[string]interface{}{})
	return err
}

// GetWallets returns wallet balances
func (c *Client) GetWallets() ([]Wallet, error) {
	resp, err := c.doRequest("GET", "/v2/wallet/balances", nil, nil)
	if err != nil {
		return nil, err
	}

	var result APIResponse[[]Wallet]
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse wallets response: %w", err)
	}

	return result.Result, nil
}

// GetUSDBalance returns the USD wallet balance
func (c *Client) GetUSDBalance() (*Wallet, error) {
	wallets, err := c.GetWallets()
	if err != nil {
		return nil, err
	}

	for i := range wallets {
		if wallets[i].AssetSymbol == "USD" {
			return &wallets[i], nil
		}
	}

	return nil, fmt.Errorf("USD wallet not found")
}
