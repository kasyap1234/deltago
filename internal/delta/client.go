package delta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is the Delta Exchange API client
type Client struct {
	baseURL    string
	httpClient *http.Client
	auth       *Auth
}

// NewClient creates a new Delta Exchange API client
func NewClient(baseURL, apiKey, apiSecret string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		auth: NewAuth(apiKey, apiSecret),
	}
}

// NewTestnetClient creates a client configured for the testnet
func NewTestnetClient(apiKey, apiSecret string) *Client {
	return NewClient("https://cdn-ind.testnet.deltaex.org", apiKey, apiSecret)
}

// NewProductionClient creates a client configured for production
func NewProductionClient(apiKey, apiSecret string) *Client {
	return NewClient("https://api.india.delta.exchange", apiKey, apiSecret)
}

// doRequest performs an HTTP request with authentication
func (c *Client) doRequest(method, path string, query url.Values, body interface{}) ([]byte, error) {
	var bodyBytes []byte
	var err error

	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	queryString := ""
	if query != nil && len(query) > 0 {
		queryString = query.Encode()
	}

	// Sign the request
	headers := c.auth.SignRequest(method, path, queryString, string(bodyBytes))

	// Build full URL
	fullURL := c.baseURL + path
	if queryString != "" {
		fullURL += "?" + queryString
	}

	var bodyReader io.Reader
	if bodyBytes != nil {
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequest(method, fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// doPublicRequest performs an unauthenticated HTTP request
func (c *Client) doPublicRequest(method, path string, query url.Values) ([]byte, error) {
	fullURL := c.baseURL + path
	if query != nil && len(query) > 0 {
		fullURL += "?" + query.Encode()
	}

	req, err := http.NewRequest(method, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "deltago-trading-bot/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// GetAuth returns the authentication handler
func (c *Client) GetAuth() *Auth {
	return c.auth
}

// GetBaseURL returns the base URL
func (c *Client) GetBaseURL() string {
	return c.baseURL
}
