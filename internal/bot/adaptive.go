package bot

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
	"github.com/kiwhtas/deltago/internal/execution"
	"github.com/kiwhtas/deltago/internal/portfolio"
	"github.com/kiwhtas/deltago/internal/regime"
	"github.com/kiwhtas/deltago/internal/selector"
	"github.com/kiwhtas/deltago/internal/strategies"
)

// AdaptiveBot is a multi-strategy adaptive trading bot
type AdaptiveBot struct {
	client    *delta.Client
	execMgr   *execution.DeltaManager
	detector  *regime.RobustDetector // Use robust detector
	selector  *selector.RuleBasedSelector
	portfolio *portfolio.State
	limits    portfolio.RiskLimits

	// Configuration
	underlying     string
	loopInterval   time.Duration
	regimeInterval time.Duration
	minConfidence  float64 // Minimum confidence to trade

	// State
	mu              sync.RWMutex
	running         bool
	currentRegime   *regime.Regime
	stopChan        chan struct{}
	stopOnce        sync.Once // Prevents double-close panic
	lastRegimeCheck time.Time
	uncertainCount  int // Count of consecutive uncertain regimes
}

// Config for the adaptive bot
type Config struct {
	Underlying      string
	InitialEquity   float64
	MaxDailyLoss    float64
	PositionSize    int
	LoopInterval    time.Duration // main loop interval
	RegimeInterval  time.Duration // regime detection interval
	IronCondorDelta float64
	IronCondorWings int
}

// DefaultConfig returns default configuration
func DefaultConfig() Config {
	return Config{
		Underlying:      "BTC",
		InitialEquity:   10000,
		MaxDailyLoss:    1000,
		PositionSize:    1,
		LoopInterval:    30 * time.Second,
		RegimeInterval:  5 * time.Minute,
		IronCondorDelta: 0.25,
		IronCondorWings: 2,
	}
}

// NewAdaptiveBot creates a new adaptive trading bot
func NewAdaptiveBot(client *delta.Client, cfg Config) *AdaptiveBot {
	// Create strategies
	strats := []strategies.Strategy{
		strategies.NewBullCallSpread(client, cfg.PositionSize),
		strategies.NewBearPutSpread(client, cfg.PositionSize),
		strategies.NewIronCondor(client, cfg.PositionSize, cfg.IronCondorDelta, cfg.IronCondorWings),
		strategies.NewLongStraddle(client, cfg.PositionSize),
		strategies.NewProtectivePut(client, cfg.PositionSize),
	}

	// Create robust detector with sensible defaults
	detectorCfg := regime.DefaultRobustConfig()

	return &AdaptiveBot{
		client:         client,
		execMgr:        execution.NewDeltaManager(client),
		detector:       regime.NewRobustDetector(detectorCfg),
		selector:       selector.NewRuleBasedSelector(strats),
		portfolio:      portfolio.NewState(cfg.InitialEquity, cfg.MaxDailyLoss),
		limits:         portfolio.DefaultRiskLimits(),
		underlying:     cfg.Underlying,
		loopInterval:   cfg.LoopInterval,
		regimeInterval: cfg.RegimeInterval,
		minConfidence:  0.6, // Don't trade if confidence < 60%
		stopChan:       make(chan struct{}),
	}
}

// Start starts the adaptive bot
func (b *AdaptiveBot) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return fmt.Errorf("bot already running")
	}
	b.running = true
	b.stopChan = make(chan struct{}) // Recreate channel for restart
	b.stopOnce = sync.Once{}         // Reset once for restart
	b.mu.Unlock()

	log.Println("🤖 Adaptive Bot Starting...")
	log.Printf("   Underlying: %s", b.underlying)
	log.Printf("   Loop Interval: %v", b.loopInterval)
	log.Printf("   Regime Interval: %v", b.regimeInterval)

	// Initial regime detection
	if err := b.updateRegime(ctx); err != nil {
		log.Printf("Warning: initial regime detection failed: %v", err)
	}

	// Start main loop
	go b.mainLoop(ctx)

	return nil
}

// Stop stops the bot - safe to call multiple times
func (b *AdaptiveBot) Stop() {
	b.stopOnce.Do(func() {
		b.mu.Lock()
		b.running = false
		b.mu.Unlock()

		close(b.stopChan)

		log.Println("🛑 Adaptive Bot Stopped")
	})
}

// GetCurrentRegime returns the current market regime
func (b *AdaptiveBot) GetCurrentRegime() *regime.Regime {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.currentRegime
}

func (b *AdaptiveBot) mainLoop(ctx context.Context) {
	ticker := time.NewTicker(b.loopInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-b.stopChan:
			return
		case <-ticker.C:
			b.runCycle(ctx)
		}
	}
}

func (b *AdaptiveBot) runCycle(ctx context.Context) {
	// 1. Check if trading is allowed
	if !b.portfolio.CanTrade() {
		log.Println("⛔ Trading halted - skipping cycle")
		return
	}

	// 2. Update regime if needed
	b.mu.RLock()
	shouldUpdateRegime := time.Since(b.lastRegimeCheck) >= b.regimeInterval
	b.mu.RUnlock()

	if shouldUpdateRegime {
		if err := b.updateRegime(ctx); err != nil {
			log.Printf("Warning: regime update failed: %v", err)
		}
	}

	r := b.GetCurrentRegime()
	if r == nil {
		return
	}

	// 3. Check regime confidence - don't enter new positions if uncertain
	if r.Score < b.minConfidence {
		b.mu.Lock()
		b.uncertainCount++
		b.mu.Unlock()

		if b.uncertainCount%10 == 1 { // Log occasionally
			log.Printf("⚠️ Regime uncertain (score=%.2f) - no new entries", r.Score)
		}

		// Still manage existing positions, but no new entries
		snapshot, err := b.fetchMarketSnapshot(ctx)
		if err != nil {
			return
		}
		input := strategies.Input{
			Regime:    r,
			Snapshot:  snapshot,
			Portfolio: b.portfolio,
			Clock:     time.Now(),
		}
		b.manageExistingPositions(ctx, input)
		return
	}

	// Reset uncertain count when confident
	b.mu.Lock()
	b.uncertainCount = 0
	b.mu.Unlock()

	// 4. Fetch current market snapshot
	snapshot, err := b.fetchMarketSnapshot(ctx)
	if err != nil {
		log.Printf("Warning: failed to fetch market snapshot: %v", err)
		return
	}

	// 5. Build strategy input
	input := strategies.Input{
		Regime:    r,
		Snapshot:  snapshot,
		Portfolio: b.portfolio,
		Clock:     time.Now(),
	}

	// 6. Manage existing positions (stop loss, take profit, regime changes)
	b.manageExistingPositions(ctx, input)

	// 7. Check for new entry opportunities
	b.checkNewEntries(ctx, input)

	// 8. Reconcile positions with exchange
	b.reconcilePositions(ctx)
}

func (b *AdaptiveBot) updateRegime(ctx context.Context) error {
	// Fetch recent candles for all timeframes
	shortCandles, mediumCandles, longCandles, err := b.fetchAllTimeframeCandles(ctx)
	if err != nil {
		return err
	}

	// Update all timeframes
	for _, c := range shortCandles {
		b.detector.UpdateShortTF(c)
	}
	for _, c := range mediumCandles {
		b.detector.UpdateMediumTF(c)
	}
	for _, c := range longCandles {
		b.detector.UpdateLongTF(c)
	}

	// Detect regime using robust multi-timeframe detection
	r := b.detector.Detect()
	if r == nil {
		return fmt.Errorf("regime detection returned nil")
	}

	b.mu.Lock()
	oldRegime := b.currentRegime
	b.currentRegime = r
	b.lastRegimeCheck = time.Now()
	b.mu.Unlock()

	// Log regime changes
	if oldRegime == nil || oldRegime.Trend != r.Trend || oldRegime.Vol != r.Vol || oldRegime.Stress != r.Stress {
		regimeAge := b.detector.GetRegimeAge()
		switchCount := b.detector.GetSwitchCount()
		log.Printf("📊 Regime: Trend=%s Vol=%s Stress=%s Score=%.2f (age=%v, switches=%d)",
			r.Trend, r.Vol, r.Stress, r.Score, regimeAge.Round(time.Minute), switchCount)
	}

	return nil
}

func (b *AdaptiveBot) fetchMarketSnapshot(ctx context.Context) (*strategies.MarketSnapshot, error) {
	// Get spot price
	perpSymbol := b.underlying + "USD"
	ticker, err := b.client.GetTicker(perpSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get ticker: %w", err)
	}

	var spotPrice float64
	fmt.Sscanf(ticker.SpotPrice, "%f", &spotPrice)

	// Get option chain
	options, err := b.client.GetDailyExpiryOptions(b.underlying)
	if err != nil {
		return nil, fmt.Errorf("failed to get options: %w", err)
	}

	return &strategies.MarketSnapshot{
		Underlying: b.underlying,
		SpotPrice:  spotPrice,
		Options:    options,
		Timestamp:  time.Now(),
	}, nil
}

// fetchAllTimeframeCandles fetches candles for all three timeframes: 5m, 1h, 4h
func (b *AdaptiveBot) fetchAllTimeframeCandles(ctx context.Context) (short, medium, long []regime.OHLCV, err error) {
	perpSymbol := b.underlying + "USD"
	now := time.Now()

	// Fetch 5-minute candles for the last 8 hours (~96 candles)
	shortStart := now.Add(-8 * time.Hour)
	shortRaw, err := b.client.GetOHLCV(perpSymbol, "5m", shortStart, now)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to fetch 5m candles: %w", err)
	}
	short = b.convertCandles(shortRaw)

	// Fetch 1-hour candles for the last 50 hours (~50 candles)
	mediumStart := now.Add(-50 * time.Hour)
	mediumRaw, err := b.client.GetOHLCV(perpSymbol, "1h", mediumStart, now)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to fetch 1h candles: %w", err)
	}
	medium = b.convertCandles(mediumRaw)

	// Fetch 4-hour candles for the last 5 days (~30 candles)
	longStart := now.Add(-5 * 24 * time.Hour)
	longRaw, err := b.client.GetOHLCV(perpSymbol, "4h", longStart, now)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to fetch 4h candles: %w", err)
	}
	long = b.convertCandles(longRaw)

	if len(short) == 0 {
		return nil, nil, nil, fmt.Errorf("no 5m candles returned for %s", perpSymbol)
	}

	log.Printf("📈 Fetched candles: 5m=%d, 1h=%d, 4h=%d", len(short), len(medium), len(long))

	return short, medium, long, nil
}

// convertCandles converts Delta OHLCCandle to regime.OHLCV format
func (b *AdaptiveBot) convertCandles(candles []delta.OHLCCandle) []regime.OHLCV {
	ohlcvData := make([]regime.OHLCV, 0, len(candles))
	for _, c := range candles {
		ohlcvData = append(ohlcvData, regime.OHLCV{
			Timestamp: time.Unix(c.Time, 0),
			Open:      c.Open,
			High:      c.High,
			Low:       c.Low,
			Close:     c.Close,
			Volume:    c.Volume,
		})
	}
	return ohlcvData
}

func (b *AdaptiveBot) manageExistingPositions(ctx context.Context, input strategies.Input) {
	activeStrategies := b.selector.GetActiveStrategies()

	for _, strat := range activeStrategies {
		orders, err := strat.Manage(ctx, input)
		if err != nil {
			log.Printf("Error managing %s: %v", strat.Name(), err)
			continue
		}

		if len(orders) > 0 {
			log.Printf("📤 %s: executing %d management orders", strat.Name(), len(orders))

			// Track close prices for P&L calculation
			closeResults := make(map[int64]float64)
			allFilled := true

			for _, order := range orders {
				state, err := b.execMgr.PlaceAndWait(ctx, order, 30*time.Second)
				if err != nil {
					log.Printf("   Order failed: %v", err)
					allFilled = false
				} else if state.Status == execution.StatusFilled {
					log.Printf("   ✅ %s filled @ %.2f", order.Symbol, state.AvgFillPrice)
					closeResults[order.InstrumentID] = state.AvgFillPrice
				} else {
					allFilled = false
				}
			}

			// Calculate and record P&L if all legs closed
			if allFilled && len(closeResults) > 0 {
				pos := strat.GetPosition()
				if pos != nil {
					pnl := b.portfolio.CalculateStrategyPnL(pos.StrategyID, closeResults)
					b.portfolio.RecordTrade(pnl)
					log.Printf("   💰 Realized P&L: %.2f (Daily P&L: %.2f)",
						pnl, b.portfolio.DailyPnL)
				}
			}
		}
	}
}

func (b *AdaptiveBot) checkNewEntries(ctx context.Context, input strategies.Input) {
	// Get strategy plan for current regime
	plan, err := b.selector.BuildPlan(ctx, input.Regime, b.portfolio)
	if err != nil {
		log.Printf("Error building strategy plan: %v", err)
		return
	}

	if len(plan.Intents) == 0 {
		if input.Regime.Score >= b.minConfidence {
			log.Printf("📥 No strategy intents generated for regime: %s/%s", input.Regime.Trend, input.Regime.Vol)
		}
		return
	}

	for _, intent := range plan.Intents {
		strat := intent.Strategy

		// Skip if already in position
		if strat.HasPosition() {
			continue
		}

		// Check entry conditions
		shouldEnter, reason, err := strat.ShouldEnter(ctx, input)
		if err != nil {
			log.Printf("Error checking entry for %s: %v", strat.Name(), err)
			continue
		}

		if !shouldEnter {
			log.Printf("📥 Strategy %s entry skipped: %s", strat.Name(), reason)
			continue
		}

		// Build entry orders first to get expected greeks
		multiLeg, err := strat.BuildEntryOrders(ctx, input)
		if err != nil {
			log.Printf("Error building entry orders for %s: %v", strat.Name(), err)
			continue
		}

		// Calculate expected greeks and max loss from the ORDER
		additionalDelta := 0.0
		additionalGamma := 0.0
		additionalMaxLoss := 0.0

		if multiLeg.Metadata != nil {
			if meta, ok := multiLeg.Metadata.(*strategies.StrategyPositionMetadata); ok {
				for _, leg := range meta.Legs {
					qty := float64(leg.Qty)
					if leg.Side == execution.Sell {
						qty = -qty // Short positions have negative greeks contribution
					}
					additionalDelta += leg.Delta * qty
					additionalGamma += leg.Gamma * qty
				}
				additionalMaxLoss = meta.MaxLoss
			}
		}

		// Check risk limits with actual expected impact including max loss
		if err := b.portfolio.CheckLimitsWithRisk(b.limits, additionalDelta, additionalGamma, additionalMaxLoss); err != nil {
			log.Printf("Risk limit prevents entry for %s: %v (delta=%.2f gamma=%.4f maxLoss=%.2f)",
				strat.Name(), err, additionalDelta, additionalGamma, additionalMaxLoss)
			continue
		}

		// Execute entry orders
		log.Printf("📈 %s: entering position (%s)", strat.Name(), reason)

		result, err := execution.ExecuteMultiLeg(ctx, b.execMgr, *multiLeg)
		if err != nil {
			log.Printf("Entry execution failed for %s: %v", strat.Name(), err)
			continue
		}

		if result.FullyFilled {
			// Record entry prices for P&L calculation
			b.recordStrategyEntry(result)

			// Confirm the position
			var meta *strategies.StrategyPositionMetadata
			if multiLeg.Metadata != nil {
				meta, _ = multiLeg.Metadata.(*strategies.StrategyPositionMetadata)
			}

			if err := strat.ConfirmEntry(ctx, result, meta); err != nil {
				log.Printf("Failed to confirm entry for %s: %v", strat.Name(), err)
			}

			pos := strat.GetPosition()
			log.Printf("   ✅ Position opened: premium=%.2f max_loss=%.2f",
				pos.NetPremium, pos.MaxLoss)
		} else {
			log.Printf("   ⚠️ Partial fill or failed: %v", result.Error)
		}
	}
}

// recordStrategyEntry records entry prices for later P&L calculation
func (b *AdaptiveBot) recordStrategyEntry(result *execution.MultiLegResult) {
	entries := make([]portfolio.LegEntry, 0, len(result.LegResults))

	for legID, legState := range result.LegResults {
		if legState.Status != execution.StatusFilled {
			continue
		}

		entries = append(entries, portfolio.LegEntry{
			InstrumentID: legState.Request.InstrumentID,
			Symbol:       legState.Request.Symbol,
			Side:         string(legState.Request.Side),
			Qty:          legState.FilledQty,
			EntryPrice:   legState.AvgFillPrice,
			EntryTime:    legState.UpdatedAt,
		})

		log.Printf("   📝 Recorded entry: %s %s %d @ %.2f (leg: %s)",
			legState.Request.Side, legState.Request.Symbol,
			legState.FilledQty, legState.AvgFillPrice, legID)
	}

	b.portfolio.RecordStrategyEntry(result.StrategyID, entries)
}

func (b *AdaptiveBot) reconcilePositions(ctx context.Context) {
	// Get exchange positions
	positions, err := b.client.GetPositions()
	if err != nil {
		log.Printf("Warning: failed to reconcile positions: %v", err)
		return
	}

	seenIDs := make(map[int64]bool)
	// Update portfolio state
	for _, pos := range positions {
		if pos.Size == 0 {
			b.portfolio.RemovePosition(pos.ProductID)
			continue
		}
		seenIDs[pos.ProductID] = true

		var entryPrice float64
		if _, err := fmt.Sscanf(pos.EntryPrice, "%f", &entryPrice); err != nil {
			log.Printf("Warning: failed to parse entry price for %s: %v", pos.ProductSymbol, err)
		}

		// Fetch current mark price from ticker for accurate UPnL
		unrealizedPnL := 0.0
		var currentMarkPrice float64
		ticker, err := b.client.GetTicker(pos.ProductSymbol)
		if err == nil && ticker.MarkPrice != "" {
			if _, err := fmt.Sscanf(ticker.MarkPrice, "%f", &currentMarkPrice); err == nil && currentMarkPrice > 0 {
				// Calculate UPnL: (Mark - Entry) * Size * 0.001 (contract multiplier)
				unrealizedPnL = (currentMarkPrice - entryPrice) * float64(pos.Size) * 0.001
				log.Printf("DEBUG UPnL: %s mark=%.2f entry=%.2f size=%d upnl=%.4f",
					pos.ProductSymbol, currentMarkPrice, entryPrice, pos.Size, unrealizedPnL)
			} else {
				// Mark price is 0 or invalid - fall back to API value
				log.Printf("Warning: mark price is zero or invalid for %s (raw: %s, parsed: %.4f), using API UPnL",
					pos.ProductSymbol, ticker.MarkPrice, currentMarkPrice)
				fmt.Sscanf(pos.UnrealizedPnL, "%f", &unrealizedPnL)
			}
		} else {
			// Fallback to API value if ticker fetch fails, but log a warning
			log.Printf("Warning: failed to fetch ticker for %s (err: %v), falling back to API UPnL", pos.ProductSymbol, err)
			fmt.Sscanf(pos.UnrealizedPnL, "%f", &unrealizedPnL)
		}

		// Look up strategy ID from our mapping
		strategyID := b.portfolio.GetStrategyIDForInstrument(pos.ProductID)

		b.portfolio.UpdatePosition(&portfolio.Position{
			InstrumentID:  pos.ProductID,
			Symbol:        pos.ProductSymbol,
			Qty:           int64(pos.Size),
			AvgPrice:      entryPrice,
			CurrentPrice:  currentMarkPrice,
			UnrealizedPnL: unrealizedPnL,
			StrategyID:    strategyID,
		})
	}

	// Remove positions that are no longer on the exchange (manually closed)
	for id := range b.portfolio.GetPositions() {
		if !seenIDs[id] {
			b.portfolio.RemovePosition(id)
		}
	}

	// Health check logging
	b.logHealth()
}

func (b *AdaptiveBot) logHealth() {
	p := b.portfolio
	g := p.GetGreeks()
	positions := p.GetPositions()

	log.Printf("📊 Health Check [Portfolio]: Positions=%d UPnL=%.2f Delta=%.4f Gamma=%.4f",
		len(positions), p.TotalUnrealizedPnL(), g.NetDelta, g.NetGamma)

	if len(positions) > 0 {
		for _, pos := range positions {
			log.Printf("   📍 Position: %s Qty=%d Price=%.2f UPnL=%.2f Strategy=%s",
				pos.Symbol, pos.Qty, pos.AvgPrice, pos.UnrealizedPnL, pos.StrategyID)
		}
	}
}

// EmergencyClose closes all positions immediately
func (b *AdaptiveBot) EmergencyClose(ctx context.Context) error {
	log.Println("🚨 EMERGENCY CLOSE: Closing all positions...")

	snapshot, err := b.fetchMarketSnapshot(ctx)
	if err != nil {
		// Fall back to exchange close all
		return b.client.CloseAllPositions()
	}

	input := strategies.Input{
		Regime:    b.GetCurrentRegime(),
		Snapshot:  snapshot,
		Portfolio: b.portfolio,
		Clock:     time.Now(),
	}

	// Close all strategy positions
	for _, strat := range b.selector.GetActiveStrategies() {
		closeOrders, err := strat.BuildCloseOrders(ctx, input)
		if err != nil {
			log.Printf("Error building close orders for %s: %v", strat.Name(), err)
			continue
		}

		_, err = execution.ExecuteMultiLeg(ctx, b.execMgr, *closeOrders)
		if err != nil {
			log.Printf("Error closing %s: %v", strat.Name(), err)
		}
	}

	return nil
}
