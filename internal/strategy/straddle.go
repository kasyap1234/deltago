package strategy

import (
	"fmt"
	"strconv"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
)

// StraddlePosition holds information about an active straddle
type StraddlePosition struct {
	CallSymbol     string
	PutSymbol      string
	CallProductID  int64
	PutProductID   int64
	Size           int
	TotalPremium   float64 // Total premium collected
	MaxLoss        float64 // 1.5x premium
	EntryTime      time.Time
	Underlying     string
	CallEntryPrice float64
	PutEntryPrice  float64
}

// Straddle implements the Daily Straddle strategy
type Straddle struct {
	client         *delta.Client
	underlying     string
	positionSize   int
	stopLossMulti  float64
	activePosition *StraddlePosition
}

// NewStraddle creates a new Straddle strategy
func NewStraddle(client *delta.Client, underlying string, positionSize int, stopLossMultiplier float64) *Straddle {
	return &Straddle{
		client:        client,
		underlying:    underlying,
		positionSize:  positionSize,
		stopLossMulti: stopLossMultiplier,
	}
}

// Execute runs the straddle strategy
func (s *Straddle) Execute() (*StraddlePosition, error) {
	// 1. Get current spot price
	spotPrice, err := s.getSpotPrice()
	if err != nil {
		return nil, fmt.Errorf("failed to get spot price: %w", err)
	}

	// 2. Find ATM call and put for daily expiry
	atmCall, atmPut, err := s.client.FindATMOptions(s.underlying, spotPrice)
	if err != nil {
		return nil, fmt.Errorf("failed to find ATM options: %w", err)
	}

	// 3. Calculate entry prices
	// For SELL orders with post_only, we need to place AT or ABOVE the best ask
	// to ensure the order rests on the book (maker). Using best ask price.
	callAsk, err := strconv.ParseFloat(atmCall.Quotes.BestAsk, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid call ask price: %w", err)
	}
	putAsk, err := strconv.ParseFloat(atmPut.Quotes.BestAsk, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid put ask price: %w", err)
	}

	// 4. Place sell orders for both call and put
	// Sell at best ask to ensure maker execution (order rests on book)
	callOrder, err := s.client.SellOption(
		atmCall.Symbol,
		atmCall.ProductID,
		s.positionSize,
		atmCall.Quotes.BestAsk, // Sell at ask to be maker
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sell call: %w", err)
	}

	_, err = s.client.SellOption(
		atmPut.Symbol,
		atmPut.ProductID,
		s.positionSize,
		atmPut.Quotes.BestAsk, // Sell at ask to be maker
	)
	if err != nil {
		// Try to cancel the call order if put fails
		_ = s.client.CancelOrder(callOrder.ID, atmCall.ProductID)
		return nil, fmt.Errorf("failed to sell put: %w", err)
	}

	// 5. Calculate total premium and max loss (use ask price as expected fill)
	totalPremium := (callAsk + putAsk) * float64(s.positionSize)
	maxLoss := totalPremium * s.stopLossMulti

	s.activePosition = &StraddlePosition{
		CallSymbol:     atmCall.Symbol,
		PutSymbol:      atmPut.Symbol,
		CallProductID:  atmCall.ProductID,
		PutProductID:   atmPut.ProductID,
		Size:           s.positionSize,
		TotalPremium:   totalPremium,
		MaxLoss:        maxLoss,
		EntryTime:      time.Now(),
		Underlying:     s.underlying,
		CallEntryPrice: callAsk,
		PutEntryPrice:  putAsk,
	}

	return s.activePosition, nil
}

// GetActivePosition returns the current active position
func (s *Straddle) GetActivePosition() interface{} {
	return s.activePosition
}

// CheckStopLoss checks if stop loss should be triggered
func (s *Straddle) CheckStopLoss() (bool, float64, error) {
	if s.activePosition == nil {
		return false, 0, nil
	}

	// Get current mark prices
	callTicker, err := s.client.GetTicker(s.activePosition.CallSymbol)
	if err != nil {
		return false, 0, fmt.Errorf("failed to get call ticker: %w", err)
	}

	putTicker, err := s.client.GetTicker(s.activePosition.PutSymbol)
	if err != nil {
		return false, 0, fmt.Errorf("failed to get put ticker: %w", err)
	}

	callMark, _ := strconv.ParseFloat(callTicker.MarkPrice, 64)
	putMark, _ := strconv.ParseFloat(putTicker.MarkPrice, 64)

	// Calculate current loss
	// We sold at entry prices, current mark is what we'd buy back at
	callPnL := (s.activePosition.CallEntryPrice - callMark) * float64(s.activePosition.Size)
	putPnL := (s.activePosition.PutEntryPrice - putMark) * float64(s.activePosition.Size)
	totalPnL := callPnL + putPnL

	// Negative PnL = loss
	currentLoss := -totalPnL
	if currentLoss < 0 {
		currentLoss = 0 // We're in profit
	}

	shouldStop := currentLoss >= s.activePosition.MaxLoss
	return shouldStop, currentLoss, nil
}

// ClosePosition closes the active straddle position
func (s *Straddle) ClosePosition() error {
	if s.activePosition == nil {
		return fmt.Errorf("no active position to close")
	}

	var errs []string

	// Get current prices for closing
	callTicker, err := s.client.GetTicker(s.activePosition.CallSymbol)
	if err != nil {
		errs = append(errs, fmt.Sprintf("failed to get call ticker: %v", err))
	} else {
		// Close by buying back using ClosePosition (IOC, reduce-only, no post-only)
		_, err = s.client.ClosePosition(
			s.activePosition.CallSymbol,
			s.activePosition.CallProductID,
			s.activePosition.Size,
			delta.OrderSideBuy,
			callTicker.Quotes.BestAsk,
		)
		if err != nil {
			errs = append(errs, fmt.Sprintf("failed to buy back call: %v", err))
		}
	}

	putTicker, err := s.client.GetTicker(s.activePosition.PutSymbol)
	if err != nil {
		errs = append(errs, fmt.Sprintf("failed to get put ticker: %v", err))
	} else {
		_, err = s.client.ClosePosition(
			s.activePosition.PutSymbol,
			s.activePosition.PutProductID,
			s.activePosition.Size,
			delta.OrderSideBuy,
			putTicker.Quotes.BestAsk,
		)
		if err != nil {
			errs = append(errs, fmt.Sprintf("failed to buy back put: %v", err))
		}
	}

	// Only clear position if all closes succeeded
	if len(errs) > 0 {
		return fmt.Errorf("failed to close some legs: %v", errs)
	}

	s.activePosition = nil
	return nil
}

func (s *Straddle) getSpotPrice() (float64, error) {
	// Get a ticker for the underlying to get spot price
	// We'll use the perpetual futures ticker for spot reference
	perpSymbol := s.underlying + "USD"
	ticker, err := s.client.GetTicker(perpSymbol)
	if err != nil {
		return 0, err
	}

	spotPrice, err := strconv.ParseFloat(ticker.SpotPrice, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid spot price: %w", err)
	}

	return spotPrice, nil
}
