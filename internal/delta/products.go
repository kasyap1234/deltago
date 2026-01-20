package delta

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GetProducts fetches all available products
func (c *Client) GetProducts() ([]Product, error) {
	resp, err := c.doPublicRequest("GET", "/v2/products", nil)
	if err != nil {
		return nil, err
	}

	var result ProductsResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse products response: %w", err)
	}

	return result.Result, nil
}

// GetOptionChain fetches the option chain for a given underlying and expiry date
// expiryDate format: "DD-MM-YYYY" (e.g., "21-01-2026")
func (c *Client) GetOptionChain(underlying, expiryDate string) ([]Ticker, error) {
	query := url.Values{}
	query.Set("contract_types", "call_options,put_options")
	query.Set("underlying_asset_symbols", underlying)
	query.Set("expiry_date", expiryDate)

	resp, err := c.doPublicRequest("GET", "/v2/tickers", query)
	if err != nil {
		return nil, err
	}

	var result TickersResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse option chain response: %w", err)
	}

	return result.Result, nil
}

// GetTicker fetches ticker data for a specific product symbol
func (c *Client) GetTicker(symbol string) (*Ticker, error) {
	query := url.Values{}
	query.Set("symbol", symbol)

	resp, err := c.doPublicRequest("GET", "/v2/tickers", query)
	if err != nil {
		return nil, err
	}

	var result TickersResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse ticker response: %w", err)
	}

	if len(result.Result) == 0 {
		return nil, fmt.Errorf("ticker not found for symbol: %s", symbol)
	}

	return &result.Result[0], nil
}

// GetDailyExpiryOptions returns options expiring today for the given underlying
// Falls back to the nearest available expiry if today's options don't exist (e.g., on testnet)
func (c *Client) GetDailyExpiryOptions(underlying string) ([]Ticker, error) {
	// Get today's date in the format expected by the API
	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return nil, fmt.Errorf("failed to load IST timezone: %w", err)
	}

	today := time.Now().In(ist)
	expiryDate := today.Format("02-01-2006") // DD-MM-YYYY

	options, err := c.GetOptionChain(underlying, expiryDate)
	if err != nil {
		return nil, err
	}

	// If we found options for today, return them
	if len(options) > 0 {
		return options, nil
	}

	// Fallback: Try the next few days (testnet may not have same-day expiry)
	for i := 1; i <= 7; i++ {
		nextDay := today.AddDate(0, 0, i)
		expiryDate = nextDay.Format("02-01-2006")
		options, err = c.GetOptionChain(underlying, expiryDate)
		if err != nil {
			continue
		}
		if len(options) > 0 {
			return options, nil
		}
	}

	return nil, fmt.Errorf("no options found for %s in the next 7 days", underlying)
}

// FindATMOptions finds the at-the-money call and put options for daily expiry
func (c *Client) FindATMOptions(underlying string, spotPrice float64) (*Ticker, *Ticker, error) {
	options, err := c.GetDailyExpiryOptions(underlying)
	if err != nil {
		return nil, nil, err
	}

	if len(options) == 0 {
		return nil, nil, fmt.Errorf("no daily expiry options found for %s", underlying)
	}

	var atmCall, atmPut *Ticker
	minCallDiff := float64(999999999)
	minPutDiff := float64(999999999)

	for i := range options {
		opt := &options[i]
		strikePrice, err := strconv.ParseFloat(opt.StrikePrice, 64)
		if err != nil {
			continue
		}

		diff := abs(strikePrice - spotPrice)

		if strings.Contains(opt.ContractType, "call") && diff < minCallDiff {
			minCallDiff = diff
			atmCall = opt
		}
		if strings.Contains(opt.ContractType, "put") && diff < minPutDiff {
			minPutDiff = diff
			atmPut = opt
		}
	}

	if atmCall == nil || atmPut == nil {
		return nil, nil, fmt.Errorf("could not find ATM options for %s at spot %.2f", underlying, spotPrice)
	}

	return atmCall, atmPut, nil
}

// FindOTMOptionsByDelta finds OTM options with delta close to the target
// For calls, look for positive delta < target
// For puts, look for negative delta > -target (absolute value)
func (c *Client) FindOTMOptionsByDelta(underlying string, targetDelta float64, spotPrice float64) (
	shortCall, longCall, shortPut, longPut *Ticker, err error,
) {
	options, err := c.GetDailyExpiryOptions(underlying)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// Separate calls and puts, sorted by strike
	var calls, puts []Ticker
	for _, opt := range options {
		if strings.Contains(opt.ContractType, "call") {
			calls = append(calls, opt)
		} else if strings.Contains(opt.ContractType, "put") {
			puts = append(puts, opt)
		}
	}

	// Sort calls by strike (ascending)
	sortByStrike(calls)
	// Sort puts by strike (descending for easier wing selection)
	sortByStrike(puts)

	// Find OTM call with delta closest to target (e.g., 0.25)
	var shortCallIdx int = -1
	minCallDeltaDiff := float64(999999)
	for i, call := range calls {
		delta, _ := strconv.ParseFloat(call.Greeks.Delta, 64)
		// OTM calls have delta < 0.5, we want delta close to target (e.g., 0.25)
		if delta > 0 && delta < 0.5 {
			diff := abs(delta - targetDelta)
			if diff < minCallDeltaDiff {
				minCallDeltaDiff = diff
				shortCallIdx = i
			}
		}
	}

	// Find OTM put with delta closest to -target (e.g., -0.25)
	var shortPutIdx int = -1
	minPutDeltaDiff := float64(999999)
	for i, put := range puts {
		delta, _ := strconv.ParseFloat(put.Greeks.Delta, 64)
		// OTM puts have delta > -0.5, we want delta close to -target
		if delta < 0 && delta > -0.5 {
			diff := abs(delta + targetDelta) // +targetDelta because delta is negative
			if diff < minPutDeltaDiff {
				minPutDeltaDiff = diff
				shortPutIdx = i
			}
		}
	}

	if shortCallIdx == -1 || shortPutIdx == -1 {
		return nil, nil, nil, nil, fmt.Errorf("could not find OTM options with delta ~%.2f", targetDelta)
	}

	shortCall = &calls[shortCallIdx]
	shortPut = &puts[shortPutIdx]

	// Find long (protective) options - further OTM
	// For calls: higher strike (lower delta)
	// For puts: lower strike (higher delta, closer to 0)
	shortCallStrike, _ := strconv.ParseFloat(shortCall.StrikePrice, 64)
	shortPutStrike, _ := strconv.ParseFloat(shortPut.StrikePrice, 64)

	for _, call := range calls {
		strike, _ := strconv.ParseFloat(call.StrikePrice, 64)
		if strike > shortCallStrike {
			longCallTicker := call
			longCall = &longCallTicker
			break
		}
	}

	for i := len(puts) - 1; i >= 0; i-- {
		strike, _ := strconv.ParseFloat(puts[i].StrikePrice, 64)
		if strike < shortPutStrike {
			longPutTicker := puts[i]
			longPut = &longPutTicker
			break
		}
	}

	if longCall == nil || longPut == nil {
		return nil, nil, nil, nil, fmt.Errorf("could not find protective wings for iron condor")
	}

	return shortCall, longCall, shortPut, longPut, nil
}

// GetProductBySymbol fetches a specific product by symbol
func (c *Client) GetProductBySymbol(symbol string) (*Product, error) {
	products, err := c.GetProducts()
	if err != nil {
		return nil, err
	}

	for i := range products {
		if products[i].Symbol == symbol {
			return &products[i], nil
		}
	}

	return nil, fmt.Errorf("product not found: %s", symbol)
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// sortByStrike sorts tickers by strike price in ascending order
func sortByStrike(tickers []Ticker) {
	sort.Slice(tickers, func(i, j int) bool {
		strikeI, _ := strconv.ParseFloat(tickers[i].StrikePrice, 64)
		strikeJ, _ := strconv.ParseFloat(tickers[j].StrikePrice, 64)
		return strikeI < strikeJ
	})
}
