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
	// Fetch recent candles for each timeframe
	candles, err := b.fetchRecentCandles(ctx)
	if err != nil {
		return err
	}

	// Update short timeframe
	for _, c := range candles {
		b.detector.UpdateShortTF(c)
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

func (b *AdaptiveBot) fetchRecentCandles(ctx context.Context) ([]regime.OHLCV, error) {
	perpSymbol := b.underlying + "USD"
	now := time.Now()

	// Fetch 5-minute candles for the last 8 hours (96 candles)
	// This provides enough data for short-term indicators
	startTime := now.Add(-8 * time.Hour)

	candles, err := b.client.GetOHLCV(perpSymbol, "5m", startTime, now)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OHLCV candles: %w", err)
	}

	if len(candles) == 0 {
		return nil, fmt.Errorf("no candles returned for %s", perpSymbol)
	}

	// Convert Delta candles to regime.OHLCV format
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

	return ohlcvData, nil
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

	// Update portfolio state
	for _, pos := range positions {
		if pos.Size == 0 {
			b.portfolio.RemovePosition(pos.ProductID)
			continue
		}

		var entryPrice, unrealizedPnL float64
		fmt.Sscanf(pos.EntryPrice, "%f", &entryPrice)
		fmt.Sscanf(pos.UnrealizedPnL, "%f", &unrealizedPnL)

		b.portfolio.UpdatePosition(&portfolio.Position{
			InstrumentID:  pos.ProductID,
			Symbol:        pos.ProductSymbol,
			Qty:           int64(pos.Size),
			AvgPrice:      entryPrice,
			UnrealizedPnL: unrealizedPnL,
			UpdatedAt:     time.Now(),
		})
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
