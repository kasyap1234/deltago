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

	// 3. Get prices - use the SAME prices we'll use for orders
	// Shorts: sell at ask (maker), Longs: buy at bid (maker)
	shortCallPx, err := strconv.ParseFloat(shortCall.Quotes.BestAsk, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid shortCall ask: %w", err)
	}
	longCallPx, err := strconv.ParseFloat(longCall.Quotes.BestBid, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid longCall bid: %w", err)
	}
	shortPutPx, err := strconv.ParseFloat(shortPut.Quotes.BestAsk, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid shortPut ask: %w", err)
	}
	longPutPx, err := strconv.ParseFloat(longPut.Quotes.BestBid, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid longPut bid: %w", err)
	}

	// 4. Calculate net credit and max loss using actual order prices
	// Net credit = premium from shorts - premium paid for longs
	netCredit := (shortCallPx + shortPutPx - longCallPx - longPutPx) * float64(ic.positionSize)

	// Refuse to open if net credit is not positive
	if netCredit <= 0 {
		return nil, fmt.Errorf("refusing to open condor with non-positive net credit: %.4f", netCredit)
	}

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
	// CRITICAL: Buy protection legs FIRST to avoid naked short exposure
	// For SELL: use best ask (order rests on book)
	// For BUY: use best bid (order rests on book)

	// Step 1: Buy long call (protection)
	longCallOrder, err := ic.client.BuyOption(longCall.Symbol, longCall.ProductID, ic.positionSize, longCall.Quotes.BestBid)
	if err != nil {
		return nil, fmt.Errorf("failed to buy long call: %w", err)
	}

	// Step 2: Buy long put (protection)
	longPutOrder, err := ic.client.BuyOption(longPut.Symbol, longPut.ProductID, ic.positionSize, longPut.Quotes.BestBid)
	if err != nil {
		// Rollback: close the long call we just opened
		_ = ic.client.CancelOrder(longCallOrder.ID, longCall.ProductID)
		return nil, fmt.Errorf("failed to buy long put: %w", err)
	}

	// Step 3: Sell short call (now protected by long call)
	shortCallOrder, err := ic.client.SellOption(shortCall.Symbol, shortCall.ProductID, ic.positionSize, shortCall.Quotes.BestAsk)
	if err != nil {
		// Rollback: close both longs
		_ = ic.client.CancelOrder(longCallOrder.ID, longCall.ProductID)
		_ = ic.client.CancelOrder(longPutOrder.ID, longPut.ProductID)
		return nil, fmt.Errorf("failed to sell short call: %w", err)
	}

	// Step 4: Sell short put (now protected by long put)
	_, err = ic.client.SellOption(shortPut.Symbol, shortPut.ProductID, ic.positionSize, shortPut.Quotes.BestAsk)
	if err != nil {
		// Rollback: close all opened positions
		_ = ic.client.CancelOrder(longCallOrder.ID, longCall.ProductID)
		_ = ic.client.CancelOrder(longPutOrder.ID, longPut.ProductID)
		_ = ic.client.CancelOrder(shortCallOrder.ID, shortCall.ProductID)
		return nil, fmt.Errorf("failed to sell short put: %w", err)
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

	// Calculate current P&L for all legs using mark prices (like straddle.go)
	// This is more reliable than relying on API's unrealized_pnl field
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
			entry, err := strconv.ParseFloat(pos.EntryPrice, 64)
			if err != nil {
				return false, 0, fmt.Errorf("invalid entry_price for %s: %q: %w", pos.ProductSymbol, pos.EntryPrice, err)
			}

			ticker, err := ic.client.GetTicker(pos.ProductSymbol)
			if err != nil {
				return false, 0, fmt.Errorf("failed to get ticker for %s: %w", pos.ProductSymbol, err)
			}

			mark, err := strconv.ParseFloat(ticker.MarkPrice, 64)
			if err != nil {
				return false, 0, fmt.Errorf("invalid mark_price for %s: %q: %w", pos.ProductSymbol, ticker.MarkPrice, err)
			}

			// Sign-correct PnL using signed position size:
			// long (size>0): (mark-entry)*size => positive when mark>entry (profit)
			// short(size<0): (mark-entry)*size => negative when mark>entry (loss)
			pnl := (mark - entry) * float64(pos.Size)
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
// Uses ClosePosition helper (IOC, reduce-only, no post-only) for reliable execution
func (ic *IronCondor) ClosePosition() error {
	if ic.activePos == nil {
		return fmt.Errorf("no active position to close")
	}

	var errs []string

	// Close all 4 legs by placing opposite orders
	// Buy back short call (we are short, so buy to close)
	ticker, err := ic.client.GetTicker(ic.activePos.ShortCallSymbol)
	if err != nil {
		errs = append(errs, fmt.Sprintf("failed to get short call ticker: %v", err))
	} else if ticker != nil {
		_, err = ic.client.ClosePosition(ic.activePos.ShortCallSymbol, ic.activePos.ShortCallProductID, ic.activePos.Size, delta.OrderSideBuy, ticker.Quotes.BestAsk)
		if err != nil {
			errs = append(errs, fmt.Sprintf("failed to buy back short call: %v", err))
		}
	}

	// Sell long call (we are long, so sell to close)
	ticker, err = ic.client.GetTicker(ic.activePos.LongCallSymbol)
	if err != nil {
		errs = append(errs, fmt.Sprintf("failed to get long call ticker: %v", err))
	} else if ticker != nil {
		_, err = ic.client.ClosePosition(ic.activePos.LongCallSymbol, ic.activePos.LongCallProductID, ic.activePos.Size, delta.OrderSideSell, ticker.Quotes.BestBid)
		if err != nil {
			errs = append(errs, fmt.Sprintf("failed to sell long call: %v", err))
		}
	}

	// Buy back short put (we are short, so buy to close)
	ticker, err = ic.client.GetTicker(ic.activePos.ShortPutSymbol)
	if err != nil {
		errs = append(errs, fmt.Sprintf("failed to get short put ticker: %v", err))
	} else if ticker != nil {
		_, err = ic.client.ClosePosition(ic.activePos.ShortPutSymbol, ic.activePos.ShortPutProductID, ic.activePos.Size, delta.OrderSideBuy, ticker.Quotes.BestAsk)
		if err != nil {
			errs = append(errs, fmt.Sprintf("failed to buy back short put: %v", err))
		}
	}

	// Sell long put (we are long, so sell to close)
	ticker, err = ic.client.GetTicker(ic.activePos.LongPutSymbol)
	if err != nil {
		errs = append(errs, fmt.Sprintf("failed to get long put ticker: %v", err))
	} else if ticker != nil {
		_, err = ic.client.ClosePosition(ic.activePos.LongPutSymbol, ic.activePos.LongPutProductID, ic.activePos.Size, delta.OrderSideSell, ticker.Quotes.BestBid)
		if err != nil {
			errs = append(errs, fmt.Sprintf("failed to sell long put: %v", err))
		}
	}

	// Only clear position if all closes succeeded
	if len(errs) > 0 {
		return fmt.Errorf("failed to close some legs: %v", errs)
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
