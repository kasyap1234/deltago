package delta

import (
	"time"
)

// Product represents a tradable product on Delta Exchange
type Product struct {
	ID                   int64   `json:"id"`
	Symbol               string  `json:"symbol"`
	Description          string  `json:"description"`
	ContractType         string  `json:"contract_type"` // "call_options", "put_options", "perpetual_futures"
	ContractValue        string  `json:"contract_value"`
	ContractUnitCurrency string  `json:"contract_unit_currency"`
	StrikePrice          string  `json:"strike_price,omitempty"`
	SettlementTime       *string `json:"settlement_time,omitempty"`
	State                string  `json:"state"`
	TradingStatus        string  `json:"trading_status"`
	TickSize             string  `json:"tick_size"`
	MakerCommissionRate  string  `json:"maker_commission_rate"`
	TakerCommissionRate  string  `json:"taker_commission_rate"`
	UnderlyingAsset      Asset   `json:"underlying_asset"`
	QuotingAsset         Asset   `json:"quoting_asset"`
	SettlingAsset        Asset   `json:"settling_asset"`
	SpotIndex            Index   `json:"spot_index"`
}

// Asset represents an asset (BTC, ETH, USD, etc.)
type Asset struct {
	ID        int64  `json:"id"`
	Symbol    string `json:"symbol"`
	Name      string `json:"name"`
	Precision int    `json:"precision"`
}

// Index represents a spot price index
type Index struct {
	ID       int64  `json:"id"`
	Symbol   string `json:"symbol"`
	TickSize string `json:"tick_size"`
}

// Greeks represents option Greeks
type Greeks struct {
	Delta string `json:"delta"`
	Gamma string `json:"gamma"`
	Theta string `json:"theta"`
	Vega  string `json:"vega"`
	Rho   string `json:"rho"`
}

// Quotes represents bid/ask quotes
type Quotes struct {
	BestBid string `json:"best_bid"`
	BestAsk string `json:"best_ask"`
	BidSize string `json:"bid_size"`
	AskSize string `json:"ask_size"`
	BidIV   string `json:"bid_iv"`
	AskIV   string `json:"ask_iv"`
}

// Ticker represents real-time ticker data
type Ticker struct {
	ProductID    int64   `json:"product_id"`
	Symbol       string  `json:"symbol"`
	ContractType string  `json:"contract_type"`
	MarkPrice    string  `json:"mark_price"`
	SpotPrice    string  `json:"spot_price"`
	StrikePrice  string  `json:"strike_price,omitempty"`
	Open         float64 `json:"open"`
	High         float64 `json:"high"`
	Low          float64 `json:"low"`
	Close        float64 `json:"close"`
	Volume       float64 `json:"volume"`
	OI           string  `json:"oi"`
	Greeks       Greeks  `json:"greeks"`
	Quotes       Quotes  `json:"quotes"`
	Timestamp    int64   `json:"timestamp"`
}

// OrderSide represents buy or sell
type OrderSide string

const (
	OrderSideBuy  OrderSide = "buy"
	OrderSideSell OrderSide = "sell"
)

// OrderType represents order types
type OrderType string

const (
	OrderTypeLimit  OrderType = "limit_order"
	OrderTypeMarket OrderType = "market_order"
)

// TimeInForce represents order time in force
type TimeInForce string

const (
	TimeInForceGTC TimeInForce = "gtc" // Good Till Cancelled
	TimeInForceIOC TimeInForce = "ioc" // Immediate Or Cancel
	TimeInForceFOK TimeInForce = "fok" // Fill Or Kill
)

// CreateOrderRequest represents an order creation request
type CreateOrderRequest struct {
	ProductID     int64       `json:"product_id,omitempty"`
	ProductSymbol string      `json:"product_symbol,omitempty"`
	LimitPrice    string      `json:"limit_price,omitempty"`
	Size          int         `json:"size"`
	Side          OrderSide   `json:"side"`
	OrderType     OrderType   `json:"order_type"`
	TimeInForce   TimeInForce `json:"time_in_force,omitempty"`
	PostOnly      bool        `json:"post_only"` // Important for maker fees!
	ReduceOnly    bool        `json:"reduce_only"`
	ClientOrderID string      `json:"client_order_id,omitempty"`
}

// BatchOrderRequest represents a batch order request
type BatchOrderRequest struct {
	ProductID     int64            `json:"product_id,omitempty"`
	ProductSymbol string           `json:"product_symbol,omitempty"`
	Orders        []BatchOrderItem `json:"orders"`
}

// BatchOrderItem represents a single order in a batch
type BatchOrderItem struct {
	LimitPrice    string      `json:"limit_price"`
	Size          int         `json:"size"`
	Side          OrderSide   `json:"side"`
	OrderType     OrderType   `json:"order_type"`
	TimeInForce   TimeInForce `json:"time_in_force,omitempty"`
	PostOnly      bool        `json:"post_only"`
	ClientOrderID string      `json:"client_order_id,omitempty"`
}

// Order represents an order response
type Order struct {
	ID             int64     `json:"id"`
	UserID         int64     `json:"user_id"`
	ProductID      int64     `json:"product_id"`
	ProductSymbol  string    `json:"product_symbol"`
	Size           int       `json:"size"`
	UnfilledSize   int       `json:"unfilled_size"`
	Side           OrderSide `json:"side"`
	OrderType      OrderType `json:"order_type"`
	LimitPrice     string    `json:"limit_price"`
	State          string    `json:"state"`
	PaidCommission string    `json:"paid_commission"`
	Commission     string    `json:"commission"`
	CreatedAt      string    `json:"created_at"`
	ClientOrderID  string    `json:"client_order_id,omitempty"`
}

// Position represents an open position
type Position struct {
	ProductID        int64  `json:"product_id"`
	ProductSymbol    string `json:"product_symbol"`
	Size             int    `json:"size"` // Positive = long, Negative = short
	EntryPrice       string `json:"entry_price"`
	Margin           string `json:"margin"`
	LiquidationPrice string `json:"liquidation_price"`
	BankruptcyPrice  string `json:"bankruptcy_price"`
	RealizedPnL      string `json:"realized_pnl"`
	UnrealizedPnL    string `json:"unrealized_pnl"`
	Commission       string `json:"commission"`
	AutoTopup        bool   `json:"auto_topup"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// Wallet represents wallet balance
type Wallet struct {
	AssetID          int64  `json:"asset_id"`
	AssetSymbol      string `json:"asset_symbol"`
	AvailableBalance string `json:"available_balance"`
	Balance          string `json:"balance"`
	PositionMargin   string `json:"position_margin"`
	OrderMargin      string `json:"order_margin"`
	UnrealizedPnL    string `json:"unrealized_pnl"`
}

// APIResponse is a generic API response wrapper
type APIResponse[T any] struct {
	Success bool   `json:"success"`
	Result  T      `json:"result"`
	Error   *Error `json:"error,omitempty"`
}

// Error represents an API error
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ProductsResponse is the response for GET /v2/products
type ProductsResponse struct {
	Meta   Meta      `json:"meta"`
	Result []Product `json:"result"`
}

// TickersResponse is the response for GET /v2/tickers
type TickersResponse struct {
	Success bool     `json:"success"`
	Result  []Ticker `json:"result"`
}

// Meta contains pagination metadata
type Meta struct {
	After      *string `json:"after"`
	Before     *string `json:"before"`
	Limit      int     `json:"limit"`
	TotalCount int     `json:"total_count"`
}

// OptionChainEntry represents a single option in the chain
type OptionChainEntry struct {
	Product Product
	Ticker  Ticker
}

// ExpiryDate returns the settlement time as a Go time.Time
func (p *Product) ExpiryDate() (time.Time, error) {
	if p.SettlementTime == nil {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, *p.SettlementTime)
}

// IsOption returns true if the product is an option
func (p *Product) IsOption() bool {
	return p.ContractType == "call_options" || p.ContractType == "put_options"
}

// IsCall returns true if the product is a call option
func (p *Product) IsCall() bool {
	return p.ContractType == "call_options"
}

// IsPut returns true if the product is a put option
func (p *Product) IsPut() bool {
	return p.ContractType == "put_options"
}
