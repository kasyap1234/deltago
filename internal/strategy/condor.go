package strategy

import (
	"fmt"
	"strconv"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
)

// IronCondorPosition holds information about an active iron condor
type IronCondorPosition struct {
	ShortCallSymbol    string
	LongCallSymbol     string
	ShortPutSymbol     string
	LongPutSymbol      string
	ShortCallProductID int64
	LongCallProductID  int64
	ShortPutProductID  int64
	LongPutProductID   int64
	Size               int
	NetCredit          float64 // Net premium received
	MaxLoss            float64 // Max possible loss
	EntryTime          time.Time
	Underlying         string
	ShortCallStrike    float64
	LongCallStrike     float64
	ShortPutStrike     float64
	LongPutStrike      float64
}

// IronCondor implements the Iron Condor strategy
type IronCondor struct {
	client        *delta.Client
	underlying    string
	positionSize  int
	shortDelta    float64
	wingWidth     int // Number of strikes for wing
	stopLossMulti float64
	activePos     *IronCondorPosition
}

// NewIronCondor creates a new Iron Condor strategy
func NewIronCondor(client *delta.Client, underlying string, positionSize int, shortDelta float64, wingWidth int, stopLossMultiplier float64) *IronCondor {
	return &IronCondor{
		client:        client,
		underlying:    underlying,
		positionSize:  positionSize,
		shortDelta:    shortDelta,
		wingWidth:     wingWidth,
		stopLossMulti: stopLossMultiplier,
	}
}

// Execute runs the iron condor strategy using batch orders
func (ic *IronCondor) Execute() (*IronCondorPosition, error) {
	// 1. Get current spot price
	spotPrice, err := ic.getSpotPrice()
	if err != nil {
		return nil, fmt.Errorf("failed to get spot price: %w", err)
	}

	// 2. Find OTM options by delta
	shortCall, longCall, shortPut, longPut, err := ic.client.FindOTMOptionsByDelta(
		ic.underlying,
		ic.shortDelta,
		spotPrice,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to find OTM options: %w", err)
	}

	// 3. Get prices
	shortCallBid, _ := strconv.ParseFloat(shortCall.Quotes.BestBid, 64)
	longCallAsk, _ := strconv.ParseFloat(longCall.Quotes.BestAsk, 64)
	shortPutBid, _ := strconv.ParseFloat(shortPut.Quotes.BestBid, 64)
	longPutAsk, _ := strconv.ParseFloat(longPut.Quotes.BestAsk, 64)

	// 4. Calculate net credit and max loss
	// Net credit = premium from shorts - premium paid for longs
	netCredit := (shortCallBid + shortPutBid - longCallAsk - longPutAsk) * float64(ic.positionSize)

	// Max loss = width of spread - net credit
	shortCallStrike, _ := strconv.ParseFloat(shortCall.StrikePrice, 64)
	longCallStrike, _ := strconv.ParseFloat(longCall.StrikePrice, 64)
	shortPutStrike, _ := strconv.ParseFloat(shortPut.StrikePrice, 64)
	longPutStrike, _ := strconv.ParseFloat(longPut.StrikePrice, 64)

	callSpreadWidth := longCallStrike - shortCallStrike
	putSpreadWidth := shortPutStrike - longPutStrike
	maxWidth := callSpreadWidth
	if putSpreadWidth > maxWidth {
		maxWidth = putSpreadWidth
	}
	maxLoss := (maxWidth * float64(ic.positionSize)) - netCredit

	// 5. Place batch order for all 4 legs
	batchReq := delta.BatchOrderRequest{
		ProductSymbol: shortCall.Symbol, // Just for reference
		Orders: []delta.BatchOrderItem{
			// Sell short call
			{
				LimitPrice:  shortCall.Quotes.BestBid,
				Size:        ic.positionSize,
				Side:        delta.OrderSideSell,
				OrderType:   delta.OrderTypeLimit,
				TimeInForce: delta.TimeInForceGTC,
				PostOnly:    true,
			},
			// Buy long call (protection)
			{
				LimitPrice:  longCall.Quotes.BestAsk,
				Size:        ic.positionSize,
				Side:        delta.OrderSideBuy,
				OrderType:   delta.OrderTypeLimit,
				TimeInForce: delta.TimeInForceGTC,
				PostOnly:    true,
			},
			// Sell short put
			{
				LimitPrice:  shortPut.Quotes.BestBid,
				Size:        ic.positionSize,
				Side:        delta.OrderSideSell,
				OrderType:   delta.OrderTypeLimit,
				TimeInForce: delta.TimeInForceGTC,
				PostOnly:    true,
			},
			// Buy long put (protection)
			{
				LimitPrice:  longPut.Quotes.BestAsk,
				Size:        ic.positionSize,
				Side:        delta.OrderSideBuy,
				OrderType:   delta.OrderTypeLimit,
				TimeInForce: delta.TimeInForceGTC,
				PostOnly:    true,
			},
		},
	}

	// Note: Batch orders require same product, so we need individual orders
	// This is a limitation - we'll place them sequentially with maker flag
	_, err = ic.client.SellOption(shortCall.Symbol, shortCall.ProductID, ic.positionSize, shortCall.Quotes.BestBid)
	if err != nil {
		return nil, fmt.Errorf("failed to sell short call: %w", err)
	}

	_, err = ic.client.BuyOption(longCall.Symbol, longCall.ProductID, ic.positionSize, longCall.Quotes.BestAsk)
	if err != nil {
		return nil, fmt.Errorf("failed to buy long call: %w", err)
	}

	_, err = ic.client.SellOption(shortPut.Symbol, shortPut.ProductID, ic.positionSize, shortPut.Quotes.BestBid)
	if err != nil {
		return nil, fmt.Errorf("failed to sell short put: %w", err)
	}

	_, err = ic.client.BuyOption(longPut.Symbol, longPut.ProductID, ic.positionSize, longPut.Quotes.BestAsk)
	if err != nil {
		return nil, fmt.Errorf("failed to buy long put: %w", err)
	}

	// Suppress unused variable warning
	_ = batchReq

	ic.activePos = &IronCondorPosition{
		ShortCallSymbol:    shortCall.Symbol,
		LongCallSymbol:     longCall.Symbol,
		ShortPutSymbol:     shortPut.Symbol,
		LongPutSymbol:      longPut.Symbol,
		ShortCallProductID: shortCall.ProductID,
		LongCallProductID:  longCall.ProductID,
		ShortPutProductID:  shortPut.ProductID,
		LongPutProductID:   longPut.ProductID,
		Size:               ic.positionSize,
		NetCredit:          netCredit,
		MaxLoss:            maxLoss,
		EntryTime:          time.Now(),
		Underlying:         ic.underlying,
		ShortCallStrike:    shortCallStrike,
		LongCallStrike:     longCallStrike,
		ShortPutStrike:     shortPutStrike,
		LongPutStrike:      longPutStrike,
	}

	return ic.activePos, nil
}

// GetActivePosition returns the current active position
func (ic *IronCondor) GetActivePosition() interface{} {
	return ic.activePos
}

// CheckStopLoss checks if the position has breached max loss
func (ic *IronCondor) CheckStopLoss() (bool, float64, error) {
	if ic.activePos == nil {
		return false, 0, nil
	}

	// Calculate current P&L for all legs
	positions, err := ic.client.GetPositions()
	if err != nil {
		return false, 0, err
	}

	var totalUnrealizedPnL float64
	relevantSymbols := map[string]bool{
		ic.activePos.ShortCallSymbol: true,
		ic.activePos.LongCallSymbol:  true,
		ic.activePos.ShortPutSymbol:  true,
		ic.activePos.LongPutSymbol:   true,
	}

	for _, pos := range positions {
		if relevantSymbols[pos.ProductSymbol] {
			pnl, _ := strconv.ParseFloat(pos.RealizedPnL, 64)
			totalUnrealizedPnL += pnl
		}
	}

	currentLoss := -totalUnrealizedPnL
	if currentLoss < 0 {
		currentLoss = 0
	}

	// Use 1.5x of net credit as stop loss threshold
	stopThreshold := ic.activePos.NetCredit * ic.stopLossMulti
	shouldStop := currentLoss >= stopThreshold

	return shouldStop, currentLoss, nil
}

// ClosePosition closes all legs of the iron condor
func (ic *IronCondor) ClosePosition() error {
	if ic.activePos == nil {
		return fmt.Errorf("no active position to close")
	}

	// Close all 4 legs by placing opposite orders
	// Buy back short call
	ticker, _ := ic.client.GetTicker(ic.activePos.ShortCallSymbol)
	if ticker != nil {
		ic.client.BuyOption(ic.activePos.ShortCallSymbol, ic.activePos.ShortCallProductID, ic.activePos.Size, ticker.Quotes.BestAsk)
	}

	// Sell long call
	ticker, _ = ic.client.GetTicker(ic.activePos.LongCallSymbol)
	if ticker != nil {
		ic.client.SellOption(ic.activePos.LongCallSymbol, ic.activePos.LongCallProductID, ic.activePos.Size, ticker.Quotes.BestBid)
	}

	// Buy back short put
	ticker, _ = ic.client.GetTicker(ic.activePos.ShortPutSymbol)
	if ticker != nil {
		ic.client.BuyOption(ic.activePos.ShortPutSymbol, ic.activePos.ShortPutProductID, ic.activePos.Size, ticker.Quotes.BestAsk)
	}

	// Sell long put
	ticker, _ = ic.client.GetTicker(ic.activePos.LongPutSymbol)
	if ticker != nil {
		ic.client.SellOption(ic.activePos.LongPutSymbol, ic.activePos.LongPutProductID, ic.activePos.Size, ticker.Quotes.BestBid)
	}

	ic.activePos = nil
	return nil
}

func (ic *IronCondor) getSpotPrice() (float64, error) {
	perpSymbol := ic.underlying + "USD"
	ticker, err := ic.client.GetTicker(perpSymbol)
	if err != nil {
		return 0, err
	}

	spotPrice, err := strconv.ParseFloat(ticker.SpotPrice, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid spot price: %w", err)
	}

	return spotPrice, nil
}
