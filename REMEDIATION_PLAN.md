# DeltaGo Trading Bot - Comprehensive Remediation Plan

## Executive Summary

This document provides a detailed plan to fix critical bugs, improve reliability, and make the DeltaGo adaptive trading bot production-ready. The codebase has **17 critical issues** that must be addressed before running with real money.

**Current State**: The bot will NOT work correctly and will lose money due to broken regime detection, unsafe order handling, and non-functional risk controls.

**Target State**: A reliable adaptive trading bot with proper market data, correct order tracking, functional risk management, and realistic profitability expectations.

---

## Table of Contents

1. [Priority Matrix](#priority-matrix)
2. [Phase 1: Critical Safety Fixes (P0)](#phase-1-critical-safety-fixes-p0)
3. [Phase 2: Core Functionality Fixes (P1)](#phase-2-core-functionality-fixes-p1)
4. [Phase 3: Reliability Improvements (P2)](#phase-3-reliability-improvements-p2)
5. [Phase 4: Strategy & Profitability (P3)](#phase-4-strategy--profitability-p3)
6. [Phase 5: Testing & Validation](#phase-5-testing--validation)
7. [Implementation Timeline](#implementation-timeline)
8. [File-by-File Change Summary](#file-by-file-change-summary)

---

## Priority Matrix

| Priority | Category | Issues | Effort | Risk if Not Fixed |
|----------|----------|--------|--------|-------------------|
| **P0** | Safety | 5 | 3-4 days | **Catastrophic** - naked shorts, wrong fills |
| **P1** | Correctness | 6 | 3-4 days | **High** - regime detection broken, P&L wrong |
| **P2** | Reliability | 4 | 2-3 days | **Medium** - race conditions, reconnection issues |
| **P3** | Profitability | 4 | 3-5 days | **Low** - suboptimal strategy selection |

---

## Phase 1: Critical Safety Fixes (P0)

### 1.1 Fix Order Fill State Detection

**File**: `internal/execution/manager.go`

**Current Bug** (Lines 105-111):
```go
if !found {
    // WRONG: Assumes order not in active list = filled
    state.FilledQty = req.Qty
    state.Status = StatusFilled
    return state, nil
}
```

**Problem**: Orders can disappear from active list due to:
- Cancellation
- Rejection  
- Expiration
- API pagination issues

This causes the bot to think protection legs are filled when they're not, leading to **naked short positions**.

**Fix Plan**:
1. Add `GetOrderHistory` method to Delta client:
   ```go
   // File: internal/delta/orders.go
   func (c *Client) GetOrderHistory(orderID int64) (*Order, error) {
       query := url.Values{}
       query.Set("order_id", fmt.Sprintf("%d", orderID))
       resp, err := c.doRequest("GET", "/v2/orders/history", query, nil)
       // Parse and return order with final state
   }
   ```

2. Add `GetFills` method to Delta client:
   ```go
   // File: internal/delta/orders.go
   func (c *Client) GetFills(productID *int64, since time.Time) ([]Fill, error) {
       query := url.Values{}
       query.Set("start_time", since.Format(time.RFC3339))
       if productID != nil {
           query.Set("product_id", fmt.Sprintf("%d", *productID))
       }
       resp, err := c.doRequest("GET", "/v2/fills", query, nil)
       // Parse and return fills
   }
   ```

3. Rewrite `PlaceAndWait` to verify fills:
   ```go
   // File: internal/execution/manager.go
   func (m *DeltaManager) PlaceAndWait(ctx context.Context, req OrderRequest, timeout time.Duration) (*OrderState, error) {
       ack, err := m.Place(ctx, req)
       // ... existing code ...
       
       // When order disappears from active list:
       if !found {
           // Check order history for final state
           histOrder, err := m.client.GetOrderHistory(ack.ExchangeOrderID)
           if err == nil {
               state.Status = mapOrderState(histOrder.State)
               state.FilledQty = histOrder.Size - histOrder.UnfilledSize
           }
           
           // Verify via fills endpoint
           fills, err := m.client.GetFills(&req.InstrumentID, state.PlacedAt)
           if err == nil {
               for _, fill := range fills {
                   if fill.OrderID == ack.ExchangeOrderID {
                       state.Fills = append(state.Fills, fill)
                       state.FilledQty = sumFillQty(state.Fills)
                   }
               }
           }
           
           // Verify via position change
           pos, _ := m.client.GetPosition(req.InstrumentID)
           // Compare position size before/after
           
           if state.FilledQty == 0 {
               state.Status = StatusCancelled // Safe default
           } else if state.FilledQty == req.Qty {
               state.Status = StatusFilled
           } else {
               state.Status = StatusPartial
           }
       }
       // ...
   }
   ```

4. Add pre-order position snapshot:
   ```go
   // Before placing order, snapshot current position
   prePosition, _ := m.client.GetPosition(req.InstrumentID)
   preSize := 0
   if prePosition != nil {
       preSize = prePosition.Size
   }
   ```

**Tests to Add**:
- Unit test: Order cancelled but bot treats as filled
- Unit test: Order rejected but bot treats as filled
- Integration test: Partial fill handling
- Integration test: Network timeout during order

---

### 1.2 Fix Strategy Position State Assignment

**Files**: All strategy files in `internal/strategies/`

**Current Bug**: Each strategy sets `s.position = ...` inside `BuildEntryOrders()` BEFORE orders are confirmed filled.

**Problem**: If execution fails or partially fills, strategy believes it has a position it doesn't have.

**Fix Plan**:

1. Remove position assignment from `BuildEntryOrders`:
   ```go
   // File: internal/strategies/iron_condor.go (and all others)
   func (s *IronCondor) BuildEntryOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error) {
       // ... build orders ...
       
       // REMOVE: s.position = &StrategyPosition{...}
       
       // Instead, return position metadata with order:
       order.Metadata = &StrategyPositionMetadata{
           NetPremium:    netCredit,
           MaxLoss:       maxLoss,
           MaxProfit:     netCredit,
           BreakevenLow:  ...,
           BreakevenHigh: ...,
           Legs:          legs,
       }
       return order, nil
   }
   ```

2. Add new method `ConfirmEntry` to Strategy interface:
   ```go
   // File: internal/strategies/interface.go
   type Strategy interface {
       // ... existing methods ...
       
       // ConfirmEntry is called after fills are verified
       ConfirmEntry(ctx context.Context, result *execution.MultiLegResult, metadata *StrategyPositionMetadata) error
   }
   ```

3. Implement `ConfirmEntry` for each strategy:
   ```go
   // File: internal/strategies/iron_condor.go
   func (s *IronCondor) ConfirmEntry(ctx context.Context, result *execution.MultiLegResult, metadata *StrategyPositionMetadata) error {
       if !result.FullyFilled {
           return fmt.Errorf("cannot confirm partial fill")
       }
       
       s.position = &StrategyPosition{
           StrategyID:    result.StrategyID,
           EntryTime:     result.CompletedAt,
           NetPremium:    metadata.NetPremium,
           MaxLoss:       metadata.MaxLoss,
           MaxProfit:     metadata.MaxProfit,
           BreakevenLow:  metadata.BreakevenLow,
           BreakevenHigh: metadata.BreakevenHigh,
           Legs:          make([]Leg, 0),
       }
       
       // Populate legs from actual fill data
       for legID, legState := range result.LegResults {
           leg := findLegMetadata(metadata.Legs, legID)
           leg.EntryPrice = legState.AvgFillPrice
           s.position.Legs = append(s.position.Legs, leg)
       }
       
       return nil
   }
   ```

4. Update `AdaptiveBot.checkNewEntries`:
   ```go
   // File: internal/bot/adaptive.go
   func (b *AdaptiveBot) checkNewEntries(ctx context.Context, input strategies.Input) {
       // ... build orders ...
       
       result, err := execution.ExecuteMultiLeg(ctx, b.execMgr, *multiLeg)
       if err != nil {
           log.Printf("Entry execution failed for %s: %v", strat.Name(), err)
           continue
       }
       
       if result.FullyFilled {
           // NOW confirm the position
           if err := strat.ConfirmEntry(ctx, result, multiLeg.Metadata); err != nil {
               log.Printf("Failed to confirm entry: %v", err)
               // Attempt to close the position we just entered
               b.emergencyCloseStrategy(ctx, strat, input)
           }
       } else {
           log.Printf("Partial fill - rolling back %s", strat.Name())
           // Rollback already handled by ExecuteMultiLeg if AllOrNone=true
       }
   }
   ```

---

### 1.3 Fix P&L Tracking for Daily Loss Limit

**Files**: `internal/bot/adaptive.go`, `internal/portfolio/types.go`

**Current Bug** (Line 448-452):
```go
func (b *AdaptiveBot) calculateTradePnL(strat strategies.Strategy, state *execution.OrderState) float64 {
    return 0 // Will be updated when position is closed - NEVER HAPPENS
}
```

**Problem**: Daily loss limit never triggers because `RecordTrade(0)` is always called.

**Fix Plan**:

1. Track entry prices per leg in portfolio:
   ```go
   // File: internal/portfolio/types.go
   type LegEntry struct {
       InstrumentID int64
       Side         string
       Qty          int
       EntryPrice   float64
       EntryTime    time.Time
   }
   
   type State struct {
       // ... existing fields ...
       
       // Track strategy entries for P&L calculation
       StrategyEntries map[string][]LegEntry // strategyID -> legs
   }
   ```

2. Record entries when confirmed:
   ```go
   // File: internal/bot/adaptive.go
   func (b *AdaptiveBot) recordStrategyEntry(strategyID string, result *execution.MultiLegResult) {
       b.portfolio.mu.Lock()
       defer b.portfolio.mu.Unlock()
       
       entries := make([]portfolio.LegEntry, 0)
       for legID, legState := range result.LegResults {
           entries = append(entries, portfolio.LegEntry{
               InstrumentID: legState.Request.InstrumentID,
               Side:         string(legState.Request.Side),
               Qty:          legState.FilledQty,
               EntryPrice:   legState.AvgFillPrice,
               EntryTime:    legState.UpdatedAt,
           })
       }
       b.portfolio.StrategyEntries[strategyID] = entries
   }
   ```

3. Calculate actual P&L on close:
   ```go
   // File: internal/bot/adaptive.go
   func (b *AdaptiveBot) calculateTradePnL(strategyID string, closeResults map[string]*execution.OrderState) float64 {
       b.portfolio.mu.RLock()
       entries, ok := b.portfolio.StrategyEntries[strategyID]
       b.portfolio.mu.RUnlock()
       
       if !ok {
           return 0
       }
       
       totalPnL := 0.0
       for _, entry := range entries {
           closeState, ok := closeResults[entry.InstrumentID]
           if !ok {
               continue
           }
           
           exitPrice := closeState.AvgFillPrice
           
           if entry.Side == "buy" {
               // Long position: P&L = (exit - entry) * qty
               totalPnL += (exitPrice - entry.EntryPrice) * float64(entry.Qty)
           } else {
               // Short position: P&L = (entry - exit) * qty
               totalPnL += (entry.EntryPrice - exitPrice) * float64(entry.Qty)
           }
       }
       
       return totalPnL
   }
   ```

4. Also track real-time unrealized P&L from exchange:
   ```go
   // File: internal/bot/adaptive.go
   func (b *AdaptiveBot) syncPortfolioFromExchange(ctx context.Context) error {
       positions, err := b.client.GetPositions()
       if err != nil {
           return err
       }
       
       totalUnrealized := 0.0
       for _, pos := range positions {
           unrealized, _ := strconv.ParseFloat(pos.UnrealizedPnL, 64)
           totalUnrealized += unrealized
       }
       
       b.portfolio.mu.Lock()
       b.portfolio.UnrealizedPnL = totalUnrealized
       b.portfolio.mu.Unlock()
       
       return nil
   }
   ```

---

### 1.4 Fix ParseFloat Silent Failures

**File**: `internal/strategies/interface.go`

**Current Bug** (Lines 222-225):
```go
func parseFloat(s string) float64 {
    val, _ := strconv.ParseFloat(s, 64) // Ignores errors!
    return val
}
```

**Problem**: Missing/invalid quotes become 0, causing:
- Wrong option selection by delta
- Zero prices in orders
- Wrong P&L calculations
- Wrong risk calculations

**Fix Plan**:

1. Create proper parsing with validation:
   ```go
   // File: internal/strategies/interface.go
   
   type ParseError struct {
       Field string
       Value string
       Err   error
   }
   
   func (e *ParseError) Error() string {
       return fmt.Sprintf("failed to parse %s '%s': %v", e.Field, e.Value, e.Err)
   }
   
   func parseFloatRequired(s string, fieldName string) (float64, error) {
       if s == "" {
           return 0, &ParseError{Field: fieldName, Value: s, Err: errors.New("empty value")}
       }
       val, err := strconv.ParseFloat(s, 64)
       if err != nil {
           return 0, &ParseError{Field: fieldName, Value: s, Err: err}
       }
       if math.IsNaN(val) || math.IsInf(val, 0) {
           return 0, &ParseError{Field: fieldName, Value: s, Err: errors.New("NaN or Inf")}
       }
       return val, nil
   }
   
   func parseFloatOptional(s string, defaultVal float64) float64 {
       if s == "" {
           return defaultVal
       }
       val, err := strconv.ParseFloat(s, 64)
       if err != nil || math.IsNaN(val) || math.IsInf(val, 0) {
           return defaultVal
       }
       return val
   }
   ```

2. Update all strategy files to use new parsers:
   ```go
   // File: internal/strategies/iron_condor.go
   func (s *IronCondor) BuildEntryOrders(ctx context.Context, in Input) (*execution.MultiLegOrder, error) {
       // ...
       shortCallPrice, err := parseFloatRequired(shortCall.Quotes.BestBid, "shortCall.BestBid")
       if err != nil {
           return nil, fmt.Errorf("invalid quote: %w", err)
       }
       
       if shortCallPrice <= 0 {
           return nil, fmt.Errorf("invalid shortCall price: %.4f", shortCallPrice)
       }
       // ...
   }
   ```

3. Add option validation helper:
   ```go
   // File: internal/strategies/interface.go
   func ValidateOption(opt *delta.Ticker, requireGreeks bool) error {
       if opt == nil {
           return errors.New("option is nil")
       }
       
       // Validate quotes
       bid, err := parseFloatRequired(opt.Quotes.BestBid, "BestBid")
       if err != nil {
           return err
       }
       ask, err := parseFloatRequired(opt.Quotes.BestAsk, "BestAsk")
       if err != nil {
           return err
       }
       
       if bid <= 0 || ask <= 0 {
           return fmt.Errorf("invalid quotes: bid=%.4f ask=%.4f", bid, ask)
       }
       
       if ask < bid {
           return fmt.Errorf("crossed market: bid=%.4f > ask=%.4f", bid, ask)
       }
       
       // Validate greeks if required
       if requireGreeks {
           delta, err := parseFloatRequired(opt.Greeks.Delta, "Delta")
           if err != nil {
               return err
           }
           if delta < -1 || delta > 1 {
               return fmt.Errorf("invalid delta: %.4f", delta)
           }
       }
       
       return nil
   }
   ```

---

### 1.5 Fix stopChan Double-Close Panic

**File**: `internal/bot/adaptive.go`

**Current Bug** (Lines 124-136):
```go
func (b *AdaptiveBot) Stop() {
    b.mu.Lock()
    defer b.mu.Unlock()
    
    if !b.running {
        return
    }
    
    b.running = false
    close(b.stopChan) // Can panic if called twice due to race
}
```

**Problem**: If `Stop()` is called twice (race condition or multiple shutdown signals), it will panic.

**Fix Plan**:
```go
// File: internal/bot/adaptive.go
type AdaptiveBot struct {
    // ... existing fields ...
    stopOnce sync.Once
}

func (b *AdaptiveBot) Stop() {
    b.stopOnce.Do(func() {
        b.mu.Lock()
        b.running = false
        b.mu.Unlock()
        
        close(b.stopChan)
        
        log.Println("🛑 Adaptive Bot Stopped")
    })
}

// Also fix Start to recreate stopChan if needed
func (b *AdaptiveBot) Start(ctx context.Context) error {
    b.mu.Lock()
    if b.running {
        b.mu.Unlock()
        return fmt.Errorf("bot already running")
    }
    b.running = true
    b.stopChan = make(chan struct{}) // Create new channel
    b.stopOnce = sync.Once{}          // Reset once
    b.mu.Unlock()
    // ...
}
```

---

## Phase 2: Core Functionality Fixes (P1)

### 2.1 Replace Fake Candles with Real OHLCV

**Files**: `internal/bot/adaptive.go`, `internal/delta/client.go`

**Current Bug** (Lines 295-317 of adaptive.go):
```go
func (b *AdaptiveBot) fetchRecentCandles(ctx context.Context) ([]regime.OHLCV, error) {
    // Uses GetTicker which returns 24h stats, NOT candle data
    ticker, err := b.client.GetTicker(perpSymbol)
    // Fabricates candle with Open == Close
}
```

**Problem**: All technical indicators (EMA, ADX, ATR, volatility) are meaningless because candles are fabricated.

**Fix Plan**:

1. Add OHLCV endpoint to Delta client:
   ```go
   // File: internal/delta/client.go
   
   // OHLCVCandle represents a candlestick
   type OHLCVCandle struct {
       Time   int64   `json:"time"`   // Unix timestamp
       Open   float64 `json:"open"`
       High   float64 `json:"high"`
       Low    float64 `json:"low"`
       Close  float64 `json:"close"`
       Volume float64 `json:"volume"`
   }
   
   // GetOHLCV fetches OHLCV candles
   // resolution: "1m", "5m", "15m", "1h", "4h", "1d"
   func (c *Client) GetOHLCV(symbol string, resolution string, startTime, endTime time.Time) ([]OHLCVCandle, error) {
       query := url.Values{}
       query.Set("symbol", symbol)
       query.Set("resolution", resolution)
       query.Set("start", fmt.Sprintf("%d", startTime.Unix()))
       query.Set("end", fmt.Sprintf("%d", endTime.Unix()))
       
       resp, err := c.doPublicRequest("GET", "/v2/history/candles", query)
       if err != nil {
           return nil, err
       }
       
       var result APIResponse[[]OHLCVCandle]
       if err := json.Unmarshal(resp, &result); err != nil {
           return nil, err
       }
       
       return result.Result, nil
   }
   ```

2. Create candle aggregator for multiple timeframes:
   ```go
   // File: internal/regime/candle_aggregator.go
   package regime
   
   import (
       "sync"
       "time"
   )
   
   type CandleAggregator struct {
       mu sync.RWMutex
       
       shortTF  []OHLCV // 5-minute candles
       mediumTF []OHLCV // 1-hour candles
       longTF   []OHLCV // 4-hour candles
       
       shortMax  int
       mediumMax int
       longMax   int
       
       lastShortUpdate  time.Time
       lastMediumUpdate time.Time
       lastLongUpdate   time.Time
   }
   
   func NewCandleAggregator() *CandleAggregator {
       return &CandleAggregator{
           shortTF:   make([]OHLCV, 0, 100),
           mediumTF:  make([]OHLCV, 0, 50),
           longTF:    make([]OHLCV, 0, 30),
           shortMax:  100,
           mediumMax: 50,
           longMax:   30,
       }
   }
   
   func (a *CandleAggregator) Add5MinCandle(c OHLCV) {
       a.mu.Lock()
       defer a.mu.Unlock()
       
       a.shortTF = append(a.shortTF, c)
       if len(a.shortTF) > a.shortMax {
           a.shortTF = a.shortTF[1:]
       }
       a.lastShortUpdate = c.Timestamp
       
       // Aggregate to 1h if needed
       a.tryAggregateMedium()
       
       // Aggregate to 4h if needed
       a.tryAggregateLong()
   }
   
   func (a *CandleAggregator) tryAggregateMedium() {
       // Aggregate 12 x 5-min candles into 1-hour
       if len(a.shortTF) < 12 {
           return
       }
       
       // Check if we need a new hourly candle
       lastShort := a.shortTF[len(a.shortTF)-1]
       hourStart := lastShort.Timestamp.Truncate(time.Hour)
       
       if len(a.mediumTF) > 0 && a.mediumTF[len(a.mediumTF)-1].Timestamp.Equal(hourStart) {
           // Update existing candle
           idx := len(a.mediumTF) - 1
           a.mediumTF[idx].High = max(a.mediumTF[idx].High, lastShort.High)
           a.mediumTF[idx].Low = min(a.mediumTF[idx].Low, lastShort.Low)
           a.mediumTF[idx].Close = lastShort.Close
           a.mediumTF[idx].Volume += lastShort.Volume
       } else if lastShort.Timestamp.Sub(hourStart) >= time.Hour-5*time.Minute {
           // New hourly candle
           a.mediumTF = append(a.mediumTF, OHLCV{
               Timestamp: hourStart,
               Open:      a.findOpenForHour(hourStart),
               High:      a.findHighForHour(hourStart),
               Low:       a.findLowForHour(hourStart),
               Close:     lastShort.Close,
               Volume:    a.findVolumeForHour(hourStart),
           })
           if len(a.mediumTF) > a.mediumMax {
               a.mediumTF = a.mediumTF[1:]
           }
       }
   }
   // ... similar for longTF
   ```

3. Update adaptive bot to fetch real candles:
   ```go
   // File: internal/bot/adaptive.go
   
   func (b *AdaptiveBot) fetchAndUpdateCandles(ctx context.Context) error {
       now := time.Now()
       perpSymbol := b.underlying + "USD"
       
       // Fetch 5-minute candles (last 8 hours)
       shortCandles, err := b.client.GetOHLCV(
           perpSymbol,
           "5m",
           now.Add(-8*time.Hour),
           now,
       )
       if err != nil {
           return fmt.Errorf("failed to fetch 5m candles: %w", err)
       }
       
       for _, c := range shortCandles {
           ohlcv := regime.OHLCV{
               Timestamp: time.Unix(c.Time, 0),
               Open:      c.Open,
               High:      c.High,
               Low:       c.Low,
               Close:     c.Close,
               Volume:    c.Volume,
           }
           b.detector.UpdateShortTF(ohlcv)
       }
       
       // Fetch 1-hour candles (last 3 days)
       mediumCandles, err := b.client.GetOHLCV(
           perpSymbol,
           "1h",
           now.Add(-72*time.Hour),
           now,
       )
       if err != nil {
           return fmt.Errorf("failed to fetch 1h candles: %w", err)
       }
       
       for _, c := range mediumCandles {
           b.detector.UpdateMediumTF(convertCandle(c))
       }
       
       // Fetch 4-hour candles (last 2 weeks)
       longCandles, err := b.client.GetOHLCV(
           perpSymbol,
           "4h",
           now.Add(-14*24*time.Hour),
           now,
       )
       if err != nil {
           return fmt.Errorf("failed to fetch 4h candles: %w", err)
       }
       
       for _, c := range longCandles {
           b.detector.UpdateLongTF(convertCandle(c))
       }
       
       return nil
   }
   ```

4. Subscribe to WebSocket for real-time candle updates:
   ```go
   // File: internal/delta/websocket.go
   
   // Add candle subscription
   func (w *WebSocketClient) SubscribeCandles(symbol, resolution string) error {
       return w.Subscribe("v2/candles", []string{fmt.Sprintf("%s:%s", symbol, resolution)})
   }
   
   func (w *WebSocketClient) OnCandle(handler func(OHLCVCandle)) {
       w.mu.Lock()
       defer w.mu.Unlock()
       
       w.handlers["v2/candles"] = append(w.handlers["v2/candles"], func(data json.RawMessage) {
           var candle OHLCVCandle
           if err := json.Unmarshal(data, &candle); err != nil {
               log.Printf("Failed to parse candle: %v", err)
               return
           }
           handler(candle)
       })
   }
   ```

---

### 2.2 Fix volHistory Never Updated

**File**: `internal/regime/detector.go`

**Current Bug**: `volHistory` is initialized but never appended to in `Update()`.

**Fix Plan**:
```go
// File: internal/regime/detector.go

func (d *Detector) Update(candle OHLCV) (*Regime, error) {
    d.priceHistory = append(d.priceHistory, candle.Close)
    d.highHistory = append(d.highHistory, candle.High)
    d.lowHistory = append(d.lowHistory, candle.Low)

    // Keep last 100 candles
    if len(d.priceHistory) > 100 {
        d.priceHistory = d.priceHistory[1:]
        d.highHistory = d.highHistory[1:]
        d.lowHistory = d.lowHistory[1:]
    }

    // ADD: Update volatility history
    if len(d.priceHistory) >= d.VolPeriod+1 {
        currentVol := d.computeRealizedVol()
        d.volHistory = append(d.volHistory, currentVol)
        if len(d.volHistory) > 50 {
            d.volHistory = d.volHistory[1:]
        }
    }

    features := d.computeFeatures()
    regime := d.classifyRegime(features)
    d.lastRegime = regime
    
    return regime, nil
}
```

---

### 2.3 Fix ADX Calculation

**File**: `internal/regime/detector.go`

**Current Bug** (Lines 229-263): The ADX calculation is incorrect - missing True Range normalization and Wilder smoothing.

**Fix Plan**:
```go
// File: internal/regime/detector.go

func (d *Detector) computeADX() float64 {
    if len(d.priceHistory) < d.ADXPeriod+1 {
        return 20 // neutral default
    }

    n := len(d.priceHistory)
    period := d.ADXPeriod
    
    // Calculate True Range, +DM, -DM series
    tr := make([]float64, n-1)
    plusDM := make([]float64, n-1)
    minusDM := make([]float64, n-1)
    
    for i := 1; i < n; i++ {
        high := d.highHistory[i]
        low := d.lowHistory[i]
        prevHigh := d.highHistory[i-1]
        prevLow := d.lowHistory[i-1]
        prevClose := d.priceHistory[i-1]
        
        // True Range
        tr[i-1] = max3(
            high-low,
            math.Abs(high-prevClose),
            math.Abs(low-prevClose),
        )
        
        // Directional Movement
        upMove := high - prevHigh
        downMove := prevLow - low
        
        if upMove > downMove && upMove > 0 {
            plusDM[i-1] = upMove
        }
        if downMove > upMove && downMove > 0 {
            minusDM[i-1] = downMove
        }
    }
    
    // Wilder smoothing
    atr := wilderSmooth(tr, period)
    smoothPlusDM := wilderSmooth(plusDM, period)
    smoothMinusDM := wilderSmooth(minusDM, period)
    
    if len(atr) == 0 || atr[len(atr)-1] == 0 {
        return 20
    }
    
    // Calculate +DI and -DI
    plusDI := (smoothPlusDM[len(smoothPlusDM)-1] / atr[len(atr)-1]) * 100
    minusDI := (smoothMinusDM[len(smoothMinusDM)-1] / atr[len(atr)-1]) * 100
    
    // Calculate DX
    diSum := plusDI + minusDI
    if diSum == 0 {
        return 20
    }
    dx := (math.Abs(plusDI-minusDI) / diSum) * 100
    
    return dx
}

func wilderSmooth(data []float64, period int) []float64 {
    if len(data) < period {
        return nil
    }
    
    result := make([]float64, len(data)-period+1)
    
    // First value is simple average
    sum := 0.0
    for i := 0; i < period; i++ {
        sum += data[i]
    }
    result[0] = sum / float64(period)
    
    // Subsequent values use Wilder smoothing
    for i := period; i < len(data); i++ {
        result[i-period+1] = result[i-period] - (result[i-period] / float64(period)) + data[i]
    }
    
    return result
}

func max3(a, b, c float64) float64 {
    return math.Max(a, math.Max(b, c))
}
```

---

### 2.4 Fix EMA Calculation

**File**: `internal/regime/detector.go`

**Current Bug** (Lines 215-227): EMA starts from `prices[0]` regardless of period, making it path-dependent.

**Fix Plan**:
```go
// File: internal/regime/detector.go

func ema(prices []float64, period int) float64 {
    if len(prices) < period {
        return 0
    }
    
    // Use only the last 'period * 3' prices for stable EMA
    // (EMA needs ~3x period to stabilize)
    startIdx := 0
    if len(prices) > period*3 {
        startIdx = len(prices) - period*3
    }
    
    data := prices[startIdx:]
    
    // Initialize with SMA of first 'period' values
    sum := 0.0
    for i := 0; i < period; i++ {
        sum += data[i]
    }
    emaVal := sum / float64(period)
    
    // Calculate EMA for remaining values
    multiplier := 2.0 / float64(period+1)
    for i := period; i < len(data); i++ {
        emaVal = (data[i]-emaVal)*multiplier + emaVal
    }
    
    return emaVal
}
```

---

### 2.5 Fix IVRank Calculation

**File**: `internal/regime/robust_detector.go`

**Current Bug** (Lines 570-601): The calculation passes wrong slice sizes to `calculateATR`, often getting 0.

**Fix Plan**:
```go
// File: internal/regime/robust_detector.go

func calculateIVRank(prices, highs, lows []float64, atrPeriod, historyPeriod int) float64 {
    minRequired := atrPeriod + historyPeriod
    if len(prices) < minRequired {
        return 0.5 // Default when insufficient data
    }

    // Calculate current ATR
    n := len(prices)
    currentATR := calculateATR(prices, highs, lows, atrPeriod)
    if currentATR == 0 {
        return 0.5
    }

    // Calculate historical ATRs
    var atrHistory []float64
    
    // Start from atrPeriod+1, calculate ATR at each historical point
    for endIdx := atrPeriod + 1; endIdx <= n-historyPeriod; endIdx++ {
        // Get the ATR as of that point in time
        histATR := calculateATR(
            prices[:endIdx],
            highs[:endIdx],
            lows[:endIdx],
            atrPeriod,
        )
        if histATR > 0 {
            atrHistory = append(atrHistory, histATR)
        }
    }

    if len(atrHistory) == 0 {
        return 0.5
    }

    // Percentile rank
    lower := 0
    for _, atr := range atrHistory {
        if atr < currentATR {
            lower++
        }
    }

    return float64(lower) / float64(len(atrHistory))
}
```

---

### 2.6 Fix Risk Limit Check Using Nil Position

**File**: `internal/bot/adaptive.go`

**Current Bug** (Lines 378-389): Risk checks compute delta/gamma from position BEFORE entry, which is nil.

**Fix Plan**:
```go
// File: internal/bot/adaptive.go

func (b *AdaptiveBot) checkNewEntries(ctx context.Context, input strategies.Input) {
    // ...
    
    for _, intent := range plan.Intents {
        strat := intent.Strategy
        
        if strat.HasPosition() {
            continue
        }
        
        shouldEnter, reason, err := strat.ShouldEnter(ctx, input)
        if err != nil || !shouldEnter {
            continue
        }
        
        // Build orders first to get expected greeks
        multiLeg, err := strat.BuildEntryOrders(ctx, input)
        if err != nil {
            log.Printf("Error building entry orders: %v", err)
            continue
        }
        
        // Calculate expected greeks from the ORDER, not existing position
        additionalDelta := 0.0
        additionalGamma := 0.0
        
        if multiLeg.Metadata != nil {
            for _, leg := range multiLeg.Metadata.Legs {
                qty := float64(leg.Qty)
                if leg.Side == execution.Sell {
                    qty = -qty // Short positions have negative greeks contribution
                }
                additionalDelta += leg.Delta * qty
                additionalGamma += leg.Gamma * qty
            }
        }
        
        // Now check risk limits with actual expected impact
        if err := b.portfolio.CheckLimits(b.limits, additionalDelta, additionalGamma); err != nil {
            log.Printf("Risk limit prevents entry for %s: %v (delta=%.2f gamma=%.4f)", 
                strat.Name(), err, additionalDelta, additionalGamma)
            continue
        }
        
        // Execute...
    }
}
```

---

## Phase 3: Reliability Improvements (P2)

### 3.1 Fix lastRegimeCheck Race Condition

**File**: `internal/bot/adaptive.go`

**Current Bug**: `b.lastRegimeCheck` is read/written without mutex protection.

**Fix Plan**:
```go
// File: internal/bot/adaptive.go

func (b *AdaptiveBot) runCycle(ctx context.Context) {
    // ...
    
    // WRONG: Reading without lock
    // if time.Since(b.lastRegimeCheck) >= b.regimeInterval {
    
    // CORRECT:
    b.mu.RLock()
    shouldUpdateRegime := time.Since(b.lastRegimeCheck) >= b.regimeInterval
    b.mu.RUnlock()
    
    if shouldUpdateRegime {
        if err := b.updateRegime(ctx); err != nil {
            log.Printf("Warning: regime update failed: %v", err)
        }
    }
    // ...
}

func (b *AdaptiveBot) updateRegime(ctx context.Context) error {
    // ... existing code ...
    
    b.mu.Lock()
    oldRegime := b.currentRegime
    b.currentRegime = r
    b.lastRegimeCheck = time.Now() // Now properly protected
    b.mu.Unlock()
    
    // ...
}
```

---

### 3.2 Fix Selector Map Iteration Race

**File**: `internal/selector/selector.go`

**Current Bug** (Lines 174-182): Iterating over map without synchronization.

**Fix Plan**:
```go
// File: internal/selector/selector.go

type RuleBasedSelector struct {
    mu         sync.RWMutex
    strategies map[string]strategies.Strategy
    
    MaxStrategiesActive int
    MinRegimeConfidence float64
}

func (s *RuleBasedSelector) GetActiveStrategies() []strategies.Strategy {
    s.mu.RLock()
    defer s.mu.RUnlock()
    
    var active []strategies.Strategy
    for _, strat := range s.strategies {
        if strat.HasPosition() {
            active = append(active, strat)
        }
    }
    return active
}

func (s *RuleBasedSelector) GetAllStrategies() []strategies.Strategy {
    s.mu.RLock()
    defer s.mu.RUnlock()
    
    all := make([]strategies.Strategy, 0, len(s.strategies))
    for _, strat := range s.strategies {
        all = append(all, strat)
    }
    return all
}

func (s *RuleBasedSelector) BuildPlan(ctx context.Context, r *regime.Regime, pf *portfolio.State) (*StrategyPlan, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    
    // ... existing code but now with lock held ...
}
```

---

### 3.3 Add Order Retry with Price Walking

**File**: `internal/execution/manager.go`

**Current Bug**: PostOnly orders often don't fill, but there's no re-pricing logic.

**Fix Plan**:
```go
// File: internal/execution/manager.go

type RetryConfig struct {
    MaxRetries     int
    PriceStepPct   float64 // How much to walk price each retry
    RetryInterval  time.Duration
    AllowCrossing  bool // Allow crossing spread on final retry
}

var DefaultRetryConfig = RetryConfig{
    MaxRetries:    3,
    PriceStepPct:  0.001, // 0.1% per retry
    RetryInterval: 2 * time.Second,
    AllowCrossing: true,
}

func (m *DeltaManager) PlaceWithRetry(ctx context.Context, req OrderRequest, timeout time.Duration, cfg RetryConfig) (*OrderState, error) {
    originalPrice := req.Price
    
    for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
        if attempt > 0 {
            // Wait before retry
            select {
            case <-ctx.Done():
                return nil, ctx.Err()
            case <-time.After(cfg.RetryInterval):
            }
            
            // Walk the price
            stepAmount := originalPrice * cfg.PriceStepPct * float64(attempt)
            if req.Side == Buy {
                req.Price = originalPrice + stepAmount // Bid higher
            } else {
                req.Price = originalPrice - stepAmount // Offer lower
            }
            
            // On final retry, allow crossing spread
            if attempt == cfg.MaxRetries && cfg.AllowCrossing {
                req.PostOnly = false
                req.TimeInForce = "ioc"
            }
            
            log.Printf("Retry %d/%d: adjusting price to %.4f", attempt, cfg.MaxRetries, req.Price)
        }
        
        state, err := m.PlaceAndWait(ctx, req, timeout/time.Duration(cfg.MaxRetries+1))
        if err != nil {
            continue // Try again
        }
        
        if state.Status == StatusFilled {
            return state, nil
        }
        
        if state.Status == StatusPartial && state.FilledQty > 0 {
            // Partial fill - update qty for next attempt
            req.Qty = state.RemainingQty()
        }
    }
    
    return nil, fmt.Errorf("failed after %d retries", cfg.MaxRetries)
}
```

---

### 3.4 Fix WebSocket Reconnection Issues

**File**: `internal/delta/websocket.go`

**Current Issues**:
- Race between reconnection attempts
- Lost messages during reconnection
- No exponential backoff

**Fix Plan**:
```go
// File: internal/delta/websocket.go

type WebSocketClient struct {
    // ... existing fields ...
    
    reconnectMu     sync.Mutex
    reconnectDelay  time.Duration
    maxReconnect    time.Duration
    reconnecting    bool
    messageBuffer   chan json.RawMessage
}

func NewWebSocketClient(wsURL string, auth *Auth) *WebSocketClient {
    return &WebSocketClient{
        url:            wsURL,
        auth:           auth,
        done:           make(chan struct{}),
        reconnect:      true,
        handlers:       make(map[string][]func(json.RawMessage)),
        reconnectDelay: 1 * time.Second,
        maxReconnect:   60 * time.Second,
        messageBuffer:  make(chan json.RawMessage, 100),
    }
}

func (w *WebSocketClient) scheduleReconnect() {
    w.reconnectMu.Lock()
    if w.reconnecting {
        w.reconnectMu.Unlock()
        return
    }
    w.reconnecting = true
    w.reconnectMu.Unlock()
    
    go func() {
        defer func() {
            w.reconnectMu.Lock()
            w.reconnecting = false
            w.reconnectMu.Unlock()
        }()
        
        delay := w.reconnectDelay
        for {
            select {
            case <-w.done:
                return
            case <-time.After(delay):
            }
            
            log.Printf("Attempting reconnection (delay: %v)...", delay)
            
            if err := w.connectInternal(true); err != nil {
                log.Printf("Reconnection failed: %v", err)
                
                // Exponential backoff
                delay = delay * 2
                if delay > w.maxReconnect {
                    delay = w.maxReconnect
                }
                continue
            }
            
            log.Println("Reconnected successfully")
            w.reconnectDelay = 1 * time.Second // Reset delay
            return
        }
    }()
}
```

---

## Phase 4: Strategy & Profitability (P3)

### 4.1 Add Regime Confidence Smoothing

**File**: `internal/regime/robust_detector.go`

**Problem**: Regime switches too frequently, causing excessive trading.

**Fix Plan**:
```go
// File: internal/regime/robust_detector.go

type RobustDetector struct {
    // ... existing fields ...
    
    // Confidence smoothing
    trendConfidence []float64 // Rolling window of trend confidence
    volConfidence   []float64
    smoothingWindow int
}

func (d *RobustDetector) smoothedConfidence(current float64, history []float64) float64 {
    if len(history) < d.smoothingWindow {
        return current
    }
    
    sum := current
    weight := 1.0
    totalWeight := 1.0
    
    for i := len(history) - 1; i >= 0 && i >= len(history)-d.smoothingWindow; i-- {
        weight *= 0.8 // Decay factor
        sum += history[i] * weight
        totalWeight += weight
    }
    
    return sum / totalWeight
}
```

---

### 4.2 Add Event Risk Detection

**File**: `internal/regime/types.go`

**Problem**: No detection of high-risk events (expiry day, major announcements).

**Fix Plan**:
```go
// File: internal/regime/types.go

type EventRisk string

const (
    EventRiskNone      EventRisk = "NONE"
    EventRiskExpiry    EventRisk = "EXPIRY"     // Expiry day
    EventRiskAnnounce  EventRisk = "ANNOUNCE"   // Major announcement expected
    EventRiskRollover  EventRisk = "ROLLOVER"   // Contract rollover
)

type Regime struct {
    Trend     TrendState
    Vol       VolState
    Stress    StressState
    EventRisk EventRisk  // ADD
    Score     float64
    Features  map[string]float64
    AsOf      time.Time
}

// File: internal/regime/event_detector.go
package regime

import "time"

type EventDetector struct {
    timezone *time.Location
}

func (d *EventDetector) DetectEventRisk(t time.Time) EventRisk {
    ist, _ := time.LoadLocation("Asia/Kolkata")
    local := t.In(ist)
    
    // Daily options expire at 5:30 PM IST
    expiryHour := 17
    expiryMinute := 30
    
    // Within 2 hours of expiry
    if local.Hour() >= expiryHour-2 && local.Hour() <= expiryHour {
        if local.Hour() == expiryHour && local.Minute() <= expiryMinute {
            return EventRiskExpiry
        }
        if local.Hour() < expiryHour {
            return EventRiskExpiry
        }
    }
    
    // Friday (potential weekly expiry impact)
    if local.Weekday() == time.Friday {
        return EventRiskExpiry
    }
    
    return EventRiskNone
}
```

---

### 4.3 Improve Strategy Selection with Scoring

**File**: `internal/selector/selector.go`

**Problem**: Current selection is too simplistic - just picks first matching strategy.

**Fix Plan**:
```go
// File: internal/selector/selector.go

type StrategyScore struct {
    Strategy strategies.Strategy
    Score    float64
    Reasons  []string
}

func (s *RuleBasedSelector) scoreStrategy(strat strategies.Strategy, r *regime.Regime, pf *portfolio.State) StrategyScore {
    score := StrategyScore{
        Strategy: strat,
        Score:    0,
        Reasons:  make([]string, 0),
    }
    
    // Base score from regime match
    suitableRegimes := strat.SuitableRegimes()
    for _, sr := range suitableRegimes {
        if sr == r.Trend {
            score.Score += 0.3
            score.Reasons = append(score.Reasons, fmt.Sprintf("matches trend %s", r.Trend))
        }
    }
    
    // Vol preference match
    prefVol := strat.PreferredVol()
    if prefVol == r.Vol || prefVol == regime.VolNormal {
        score.Score += 0.2
        score.Reasons = append(score.Reasons, fmt.Sprintf("vol preference matches"))
    }
    
    // Regime confidence boost
    score.Score *= r.Score
    
    // Penalize if we already have many positions
    positionCount := len(pf.GetPositionsByStrategy(strat.ID()))
    if positionCount > 0 {
        score.Score *= 0.5 // Reduce score for strategies with existing positions
        score.Reasons = append(score.Reasons, fmt.Sprintf("existing positions: %d", positionCount))
    }
    
    // Penalize near event risk
    if r.EventRisk != regime.EventRiskNone {
        if strat.ID() == "long_straddle" {
            // Long straddle is actually good before events
            score.Score *= 1.2
        } else if strat.ID() == "iron_condor" {
            // Dangerous to sell premium near events
            score.Score *= 0.3
            score.Reasons = append(score.Reasons, "event risk - avoid short premium")
        }
    }
    
    return score
}

func (s *RuleBasedSelector) BuildPlan(ctx context.Context, r *regime.Regime, pf *portfolio.State) (*StrategyPlan, error) {
    plan := &StrategyPlan{
        Regime:  r,
        Intents: make([]StrategyIntent, 0),
    }
    
    if r.Score < s.MinRegimeConfidence {
        return plan, nil
    }
    
    // Score all strategies
    var scores []StrategyScore
    for _, strat := range s.strategies {
        score := s.scoreStrategy(strat, r, pf)
        if score.Score > 0.3 { // Minimum threshold
            scores = append(scores, score)
        }
    }
    
    // Sort by score descending
    sort.Slice(scores, func(i, j int) bool {
        return scores[i].Score > scores[j].Score
    })
    
    // Take top N strategies
    for i := 0; i < min(s.MaxStrategiesActive, len(scores)); i++ {
        plan.Intents = append(plan.Intents, StrategyIntent{
            StrategyID: scores[i].Strategy.ID(),
            Strategy:   scores[i].Strategy,
            Weight:     scores[i].Score,
            Reason:     strings.Join(scores[i].Reasons, "; "),
        })
        plan.TotalWeight += scores[i].Score
    }
    
    return plan, nil
}
```

---

### 4.4 Add Transaction Cost Modeling

**File**: `internal/portfolio/types.go`

**Problem**: No consideration of fees, slippage in P&L calculations.

**Fix Plan**:
```go
// File: internal/portfolio/types.go

type TransactionCosts struct {
    MakerFeeRate float64 // e.g., 0.0001 (0.01%)
    TakerFeeRate float64 // e.g., 0.0005 (0.05%)
    SlippageBps  float64 // Expected slippage in basis points
}

func DefaultTransactionCosts() TransactionCosts {
    return TransactionCosts{
        MakerFeeRate: 0.0001, // 0.01%
        TakerFeeRate: 0.0005, // 0.05%
        SlippageBps:  5,      // 5 bps typical slippage
    }
}

func (tc TransactionCosts) EstimateCost(notional float64, isMaker bool, legs int) float64 {
    feeRate := tc.TakerFeeRate
    if isMaker {
        feeRate = tc.MakerFeeRate
    }
    
    // Fee per leg
    fees := notional * feeRate * float64(legs)
    
    // Slippage (both entry and exit)
    slippage := notional * (tc.SlippageBps / 10000) * 2 * float64(legs)
    
    return fees + slippage
}

// Use in strategy entry check
func (s *IronCondor) ShouldEnter(ctx context.Context, in Input) (bool, string, error) {
    // ... existing checks ...
    
    // Check if expected profit covers transaction costs
    expectedProfit := estimatedNetCredit
    costs := in.Portfolio.Costs.EstimateCost(expectedProfit*10, true, 4) // 4 legs
    
    if expectedProfit < costs*2 { // Require 2x costs for positive expectancy
        return false, fmt.Sprintf("insufficient edge after costs: profit=%.2f costs=%.2f", expectedProfit, costs), nil
    }
    
    return true, "all checks passed", nil
}
```

---

## Phase 5: Testing & Validation

### 5.1 Unit Tests Required

| Component | Test File | Priority |
|-----------|-----------|----------|
| Order fill detection | `internal/execution/manager_test.go` | P0 |
| Position state management | `internal/strategies/position_test.go` | P0 |
| P&L calculation | `internal/portfolio/pnl_test.go` | P0 |
| Float parsing | `internal/strategies/parse_test.go` | P0 |
| ADX calculation | `internal/regime/adx_test.go` | P1 |
| EMA calculation | `internal/regime/ema_test.go` | P1 |
| IVRank calculation | `internal/regime/ivrank_test.go` | P1 |
| Multi-leg execution | `internal/execution/multileg_test.go` | P1 |
| Regime detection | `internal/regime/detector_test.go` | P2 |
| Strategy selection | `internal/selector/selector_test.go` | P2 |

### 5.2 Integration Tests Required

| Scenario | Test File | Priority |
|----------|-----------|----------|
| Full order lifecycle | `tests/integration/order_lifecycle_test.go` | P0 |
| Partial fill handling | `tests/integration/partial_fill_test.go` | P0 |
| Network failure recovery | `tests/integration/network_recovery_test.go` | P1 |
| Multi-leg rollback | `tests/integration/rollback_test.go` | P1 |
| Regime change during position | `tests/integration/regime_change_test.go` | P2 |

### 5.3 Backtesting Improvements

**File**: `internal/backtest/engine.go`

```go
// Add realistic simulation features
type BacktestEngine struct {
    // ... existing fields ...
    
    // Add realistic features
    slippageModel  SlippageModel
    fillRateModel  FillRateModel
    costModel      TransactionCosts
    regimeDetector *regime.RobustDetector
}

type SlippageModel interface {
    EstimateSlippage(side execution.Side, size int, volatility float64) float64
}

type FillRateModel interface {
    EstimateFillProbability(price, bestBid, bestAsk float64, postOnly bool) float64
}
```

---

## Implementation Timeline

### Week 1: Critical Safety (P0)
| Day | Tasks |
|-----|-------|
| Mon | Fix order fill detection (#1.1) |
| Tue | Fix strategy position state (#1.2) |
| Wed | Fix P&L tracking (#1.3), parseFloat (#1.4) |
| Thu | Fix stopChan panic (#1.5), write P0 unit tests |
| Fri | Integration testing, code review |

### Week 2: Core Functionality (P1)
| Day | Tasks |
|-----|-------|
| Mon | Implement real OHLCV fetching (#2.1) |
| Tue | Fix volHistory (#2.2), ADX (#2.3), EMA (#2.4) |
| Wed | Fix IVRank (#2.5), risk limit check (#2.6) |
| Thu | Write P1 unit tests |
| Fri | Integration testing |

### Week 3: Reliability (P2) + Strategy (P3)
| Day | Tasks |
|-----|-------|
| Mon | Fix race conditions (#3.1, #3.2) |
| Tue | Add order retry logic (#3.3) |
| Wed | Fix WebSocket reconnection (#3.4) |
| Thu | Add confidence smoothing, event detection (#4.1, #4.2) |
| Fri | Improve strategy scoring, add cost modeling (#4.3, #4.4) |

### Week 4: Testing & Hardening
| Day | Tasks |
|-----|-------|
| Mon-Tue | Integration test suite |
| Wed | Backtest with new code |
| Thu | Testnet live testing |
| Fri | Documentation, final review |

---

## File-by-File Change Summary

| File | Changes | Lines Changed (Est.) |
|------|---------|---------------------|
| `internal/execution/manager.go` | Fix fill detection, add retry | ~150 |
| `internal/execution/types.go` | Add fill tracking types | ~30 |
| `internal/strategies/interface.go` | Add ConfirmEntry, fix parseFloat | ~80 |
| `internal/strategies/iron_condor.go` | Remove early position assignment | ~50 |
| `internal/strategies/long_straddle.go` | Remove early position assignment | ~40 |
| `internal/strategies/bull_call_spread.go` | Remove early position assignment | ~40 |
| `internal/strategies/bear_put_spread.go` | Remove early position assignment | ~40 |
| `internal/strategies/protective_put.go` | Remove early position assignment | ~30 |
| `internal/bot/adaptive.go` | Fix all identified issues | ~200 |
| `internal/portfolio/types.go` | Add entry tracking, costs | ~80 |
| `internal/regime/detector.go` | Fix EMA, ADX, volHistory | ~100 |
| `internal/regime/robust_detector.go` | Fix IVRank, add smoothing | ~100 |
| `internal/regime/candle_aggregator.go` | NEW: multi-TF candle aggregation | ~150 |
| `internal/regime/event_detector.go` | NEW: event risk detection | ~50 |
| `internal/delta/orders.go` | Add GetOrderHistory, GetFills | ~50 |
| `internal/delta/client.go` | Add GetOHLCV | ~40 |
| `internal/delta/websocket.go` | Fix reconnection, add candle sub | ~80 |
| `internal/selector/selector.go` | Add scoring, fix race condition | ~100 |

**Total estimated changes**: ~1,400 lines

---

## Success Criteria

Before going live with real money, verify:

1. ✅ Orders are never assumed filled without verification
2. ✅ Strategy positions only set after confirmed fills
3. ✅ Daily P&L is accurately tracked and limits trigger
4. ✅ All float parsing failures are logged and handled
5. ✅ No panics from channel operations
6. ✅ Real OHLCV data drives regime detection
7. ✅ ADX/EMA/IVRank calculations match industry-standard formulas
8. ✅ Race conditions eliminated
9. ✅ Partial fills are properly handled
10. ✅ Backtests show realistic (not overfitted) results

---

## Risk Disclaimer

Even after all fixes:
- **No strategy is profitable in ALL market conditions**
- Expect **30-50% win rate** on most strategies
- Maximum drawdown can exceed **3x daily loss limit** in gap scenarios
- Always start with **testnet** and **minimum size** on production

---

*Document Version: 1.0*  
*Created: January 2026*  
*Author: Automated Analysis*
