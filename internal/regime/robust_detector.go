package regime

import (
	"math"
	"sync"
	"time"
)

// RobustDetector uses multiple confirmation methods to avoid misclassification
type RobustDetector struct {
	mu sync.RWMutex

	// Configuration
	config RobustConfig

	// Multi-timeframe data
	shortTF  *TimeframeData // 5-min candles
	mediumTF *TimeframeData // 1-hour candles
	longTF   *TimeframeData // 4-hour candles

	// State
	currentRegime    *Regime
	regimeStartTime  time.Time
	regimeSwitchCount int
	lastSwitchTime   time.Time
	
	// Regime history for persistence check
	regimeHistory []RegimeSnapshot
	
	// Smoothing
	trendConfidence []float64
	eventDetector   *EventDetector
}

type RobustConfig struct {
	// Minimum time a regime must persist before switching
	MinRegimeDuration time.Duration
	
	// Cooldown after regime switch before allowing another
	SwitchCooldown time.Duration
	
	// Minimum confidence to classify (else UNCERTAIN)
	MinConfidence float64
	
	// Number of timeframes that must agree
	MinTimeframeAgreement int
	
	// Trend thresholds
	TrendADXThreshold     float64 // ADX above this = trending
	TrendSlopeThreshold   float64 // EMA slope threshold
	
	// Vol thresholds
	VolHighPercentile float64 // IV rank above this = HIGH
	VolLowPercentile  float64 // IV rank below this = LOW
	
	// Crash thresholds
	CrashDrawdownPct  float64 // drawdown to trigger crash
	CrashVolSpikeMult float64 // vol spike multiplier
	
	// History size for stability check
	HistorySize int
}

func DefaultRobustConfig() RobustConfig {
	return RobustConfig{
		MinRegimeDuration:     5 * time.Minute,
		SwitchCooldown:        10 * time.Minute,
		MinConfidence:         0.6,
		MinTimeframeAgreement: 2, // at least 2 of 3 timeframes
		TrendADXThreshold:     25,
		TrendSlopeThreshold:   0.002,
		VolHighPercentile:     0.70,
		VolLowPercentile:      0.30,
		CrashDrawdownPct:      0.05,
		CrashVolSpikeMult:     2.0,
		HistorySize:           20,
	}
}

type TimeframeData struct {
	name          string
	prices        []float64
	highs         []float64
	lows          []float64
	volumes       []float64
	lastUpdate    time.Time
	lastTimestamp time.Time // Track last seen candle timestamp to prevent duplicates
	maxCandles    int
}

type RegimeSnapshot struct {
	Regime    Regime
	Timestamp time.Time
}

func NewRobustDetector(cfg RobustConfig) *RobustDetector {
	return &RobustDetector{
		config:          cfg,
		shortTF:         newTimeframeData("5m", 100),
		mediumTF:        newTimeframeData("1h", 50),
		longTF:          newTimeframeData("4h", 30),
		regimeHistory:   make([]RegimeSnapshot, 0, cfg.HistorySize),
		trendConfidence: make([]float64, 0, 20),
		eventDetector:   NewEventDetector(),
	}
}

func newTimeframeData(name string, maxCandles int) *TimeframeData {
	return &TimeframeData{
		name:       name,
		prices:     make([]float64, 0, maxCandles),
		highs:      make([]float64, 0, maxCandles),
		lows:       make([]float64, 0, maxCandles),
		volumes:    make([]float64, 0, maxCandles),
		maxCandles: maxCandles,
	}
}

// UpdateShortTF updates 5-minute timeframe data
func (d *RobustDetector) UpdateShortTF(candle OHLCV) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.shortTF.addCandle(candle)
}

// UpdateMediumTF updates 1-hour timeframe data
func (d *RobustDetector) UpdateMediumTF(candle OHLCV) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mediumTF.addCandle(candle)
}

// UpdateLongTF updates 4-hour timeframe data
func (d *RobustDetector) UpdateLongTF(candle OHLCV) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.longTF.addCandle(candle)
}

func (tf *TimeframeData) addCandle(c OHLCV) {
	// Skip candles that are not newer than the last seen timestamp to prevent duplicates
	if !tf.lastTimestamp.IsZero() && !c.Timestamp.After(tf.lastTimestamp) {
		return
	}

	tf.prices = append(tf.prices, c.Close)
	tf.highs = append(tf.highs, c.High)
	tf.lows = append(tf.lows, c.Low)
	tf.volumes = append(tf.volumes, c.Volume)
	tf.lastUpdate = c.Timestamp
	tf.lastTimestamp = c.Timestamp

	// Trim to max size
	if len(tf.prices) > tf.maxCandles {
		tf.prices = tf.prices[1:]
		tf.highs = tf.highs[1:]
		tf.lows = tf.lows[1:]
		tf.volumes = tf.volumes[1:]
	}
}

// ClearData resets all timeframe data and timestamps
func (d *RobustDetector) ClearData() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.shortTF.clear()
	d.mediumTF.clear()
	d.longTF.clear()
	d.currentRegime = nil
	d.regimeHistory = make([]RegimeSnapshot, 0, d.config.HistorySize)
	d.trendConfidence = make([]float64, 0, 20)
}

func (tf *TimeframeData) clear() {
	tf.prices = tf.prices[:0]
	tf.highs = tf.highs[:0]
	tf.lows = tf.lows[:0]
	tf.volumes = tf.volumes[:0]
	tf.lastUpdate = time.Time{}
	tf.lastTimestamp = time.Time{}
}

// Detect performs robust regime detection with multiple confirmations
func (d *RobustDetector) Detect() *Regime {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check if we have enough data
	if len(d.shortTF.prices) < 20 || len(d.mediumTF.prices) < 10 {
		return d.uncertainRegime("insufficient data")
	}

	// 1. Detect regime on each timeframe independently
	shortRegime := d.detectTimeframe(d.shortTF)
	mediumRegime := d.detectTimeframe(d.mediumTF)
	longRegime := d.detectTimeframe(d.longTF)

	// 2. Check for crash on ANY timeframe (crash overrides everything)
	if shortRegime.Stress == StressCrash || 
	   mediumRegime.Stress == StressCrash || 
	   longRegime.Stress == StressCrash {
		return d.createRegime(TrendDown, VolHigh, StressCrash, 0.9, 
			map[string]float64{"crash_detected": 1})
	}

	// 3. Consensus on trend - use deterministic ordering to avoid map iteration randomness
	trendVotes := make(map[TrendState]int)
	trendVotes[shortRegime.Trend]++
	trendVotes[mediumRegime.Trend]++
	if len(d.longTF.prices) >= 10 {
		trendVotes[longRegime.Trend]++
	}

	// Use deterministic order: prefer Sideways in ties (most conservative)
	// Order: Sideways > Up > Down (in case of equal votes)
	trendOrder := []TrendState{TrendSideways, TrendUp, TrendDown}
	consensusTrend := TrendSideways
	maxVotes := 0
	for _, trend := range trendOrder {
		votes := trendVotes[trend]
		if votes > maxVotes {
			maxVotes = votes
			consensusTrend = trend
		}
	}

	// 4. Check timeframe agreement
	totalTFs := 2
	if len(d.longTF.prices) >= 10 {
		totalTFs = 3
	}
	
	if maxVotes < d.config.MinTimeframeAgreement {
		// Timeframes disagree - uncertain
		return d.uncertainRegime("timeframe disagreement")
	}

	// 5. Consensus on volatility (use medium TF as primary)
	consensusVol := mediumRegime.Vol
	if shortRegime.Vol == VolHigh && mediumRegime.Vol == VolHigh {
		consensusVol = VolHigh
	} else if shortRegime.Vol == VolLow && mediumRegime.Vol == VolLow {
		consensusVol = VolLow
	}

	// 6. Calculate confidence based on agreement
	rawConfidence := float64(maxVotes) / float64(totalTFs)
	
	// Boost confidence if all timeframes agree
	if maxVotes == totalTFs {
		rawConfidence = 0.9
	}
	
	// Smooth confidence
	d.trendConfidence = append(d.trendConfidence, rawConfidence)
	if len(d.trendConfidence) > 10 {
		d.trendConfidence = d.trendConfidence[1:]
	}
	smoothedConf := d.smoothedConfidence(rawConfidence, d.trendConfidence)

	// Detect event risk
	eventRisk := d.eventDetector.DetectEventRisk(time.Now())

	// 7. Check regime persistence
	proposedRegime := d.createRegime(consensusTrend, consensusVol, StressNormal, smoothedConf,
		map[string]float64{
			"trend_votes": float64(maxVotes),
			"total_tfs":   float64(totalTFs),
		})
	proposedRegime.EventRisk = eventRisk

	// 8. Apply hysteresis - don't switch if recent switch or persistence not met
	if d.currentRegime != nil {
		// Check cooldown
		if time.Since(d.lastSwitchTime) < d.config.SwitchCooldown {
			if proposedRegime.Trend != d.currentRegime.Trend {
				// Still in cooldown, keep current regime
				// But update EventRisk as that is real-time
				d.currentRegime.EventRisk = eventRisk
				return d.currentRegime
			}
		}

		// Check if proposed regime differs
		if proposedRegime.Trend != d.currentRegime.Trend ||
		   proposedRegime.Vol != d.currentRegime.Vol {
			
			// Check persistence requirement
			if !d.checkRegimePersistence(proposedRegime) {
				// Proposed regime hasn't persisted long enough
				d.currentRegime.EventRisk = eventRisk
				return d.currentRegime
			}
		}
	}

	// 9. Update state if regime changed
	if d.currentRegime == nil || 
	   proposedRegime.Trend != d.currentRegime.Trend ||
	   proposedRegime.Vol != d.currentRegime.Vol {
		d.lastSwitchTime = time.Now()
		d.regimeSwitchCount++
		d.regimeStartTime = time.Now()
	}

	d.currentRegime = proposedRegime
	d.addToHistory(proposedRegime)
	
	return proposedRegime
}

func (d *RobustDetector) smoothedConfidence(current float64, history []float64) float64 {
	sum := current
	weight := 1.0
	totalWeight := 1.0
	
	// Exponential moving average over history
	for i := len(history) - 1; i >= 0; i-- {
		weight *= 0.8 // Decay factor
		sum += history[i] * weight
		totalWeight += weight
	}
	
	return sum / totalWeight
}

func (d *RobustDetector) detectTimeframe(tf *TimeframeData) *Regime {
	if len(tf.prices) < 20 {
		return &Regime{Trend: TrendSideways, Vol: VolNormal, Stress: StressNormal}
	}

	n := len(tf.prices)
	prices := tf.prices
	highs := tf.highs
	lows := tf.lows

	// Calculate indicators
	emaFast := ema(prices, 8)
	emaSlow := ema(prices, 21)
	emaSlope := (emaFast - emaSlow) / emaSlow

	adx := calculateADX(prices, highs, lows, 14)
	atr := calculateATR(prices, highs, lows, 14)
	atrPct := atr / prices[n-1]

	// Recent return for crash detection
	recentReturn := 0.0
	if n >= 5 {
		recentReturn = (prices[n-1] - prices[n-5]) / prices[n-5]
	}

	// Drawdown
	recentHigh := maxSlice(highs[maxInt(0, n-20):])
	drawdown := (recentHigh - prices[n-1]) / recentHigh

	// Realized vol
	realizedVol := calculateRealizedVol(prices, 20)
	avgVol := calculateRealizedVol(prices[:maxInt(20, n-20)], 20)
	volSpike := 1.0
	if avgVol > 0 {
		volSpike = realizedVol / avgVol
	}

	// Classify
	regime := &Regime{
		Trend:  TrendSideways,
		Vol:    VolNormal,
		Stress: StressNormal,
		Features: map[string]float64{
			"ema_slope":     emaSlope,
			"adx":           adx,
			"atr_pct":       atrPct,
			"recent_return": recentReturn,
			"drawdown":      drawdown,
			"vol_spike":     volSpike,
		},
	}

	// Crash detection (highest priority)
	if recentReturn < -d.config.CrashDrawdownPct ||
	   (drawdown > d.config.CrashDrawdownPct && volSpike > d.config.CrashVolSpikeMult) {
		regime.Stress = StressCrash
		regime.Score = 0.95
		return regime
	}

	// Trend detection using multiple methods
	trendSignals := 0
	totalSignals := 0

	// Signal 1: ADX + EMA slope (directional strength)
	if adx > d.config.TrendADXThreshold {
		totalSignals++
		if emaSlope > d.config.TrendSlopeThreshold {
			trendSignals++ // uptrend signal
		} else if emaSlope < -d.config.TrendSlopeThreshold {
			trendSignals-- // downtrend signal
		}
	}

	// Signal 2: Price position relative to EMAs (trend alignment)
	totalSignals++
	if prices[n-1] > emaFast && emaFast > emaSlow {
		trendSignals++ // uptrend
	} else if prices[n-1] < emaFast && emaFast < emaSlow {
		trendSignals-- // downtrend
	}

	// Signal 3: Higher highs / lower lows (price structure)
	if n >= 10 {
		totalSignals++
		hh := highs[n-1] > highs[n-5] && highs[n-5] > highs[n-10]
		ll := lows[n-1] < lows[n-5] && lows[n-5] < lows[n-10]
		if hh && !ll {
			trendSignals++
		} else if ll && !hh {
			trendSignals--
		}
	}

	// Signal 4: Momentum (ROC)
	if n >= 10 {
		totalSignals++
		roc := (prices[n-1] - prices[n-10]) / prices[n-10]
		if roc > 0.02 { // 2% up
			trendSignals++
		} else if roc < -0.02 { // 2% down
			trendSignals--
		}
	}

	// Signal 5: Efficiency Ratio (trend quality) - new!
	// ER = |Price Change| / Sum of |Daily Changes|
	// High ER (>0.5) = strong trend, Low ER (<0.2) = choppy
	if n >= 20 {
		priceChange := math.Abs(prices[n-1] - prices[n-20])
		sumChanges := 0.0
		for i := n - 19; i < n; i++ {
			sumChanges += math.Abs(prices[i] - prices[i-1])
		}
		if sumChanges > 0 {
			efficiencyRatio := priceChange / sumChanges
			regime.Features["efficiency_ratio"] = efficiencyRatio
			
			totalSignals++
			if efficiencyRatio > 0.5 {
				// Strong directional move - add to trend direction
				if prices[n-1] > prices[n-20] {
					trendSignals++
				} else {
					trendSignals--
				}
			}
			// Low ER = choppy, no signal (implicit sideways)
		}
	}

	// Signal 6: Choppiness Index (inverse trend indicator) - new!
	// CI = 100 * log10(ATR_sum / (HighestHigh - LowestLow)) / log10(n)
	// High CI (>60) = choppy/sideways, Low CI (<40) = trending
	if n >= 14 {
		ci := calculateChoppinessIndex(prices, highs, lows, 14)
		regime.Features["choppiness_index"] = ci
		
		// If CI is very high, penalize trend signals (market is choppy)
		if ci > 60 {
			// Choppy market - halve the trend signals
			if trendSignals > 0 {
				trendSignals = trendSignals / 2
			} else if trendSignals < 0 {
				trendSignals = trendSignals / 2
			}
		}
	}

	// Determine trend from signals
	if totalSignals > 0 {
		signalRatio := float64(trendSignals) / float64(totalSignals)
		if signalRatio >= 0.5 {
			regime.Trend = TrendUp
			regime.Score = 0.5 + signalRatio*0.4
		} else if signalRatio <= -0.5 {
			regime.Trend = TrendDown
			regime.Score = 0.5 + math.Abs(signalRatio)*0.4
		} else {
			regime.Trend = TrendSideways
			regime.Score = 0.7
		}
	}

	// Volatility classification
	ivRank := calculateIVRank(prices, highs, lows, 14, 50)
	if ivRank > d.config.VolHighPercentile || atrPct > 0.04 {
		regime.Vol = VolHigh
	} else if ivRank < d.config.VolLowPercentile && atrPct < 0.015 {
		regime.Vol = VolLow
	}

	regime.Features["iv_rank"] = ivRank
	regime.Features["trend_signals"] = float64(trendSignals)
	regime.Features["total_signals"] = float64(totalSignals)

	return regime
}

func (d *RobustDetector) checkRegimePersistence(proposed *Regime) bool {
	if len(d.regimeHistory) < 3 {
		return true // Not enough history, allow switch
	}

	// Count how many recent snapshots match proposed regime (both trend AND vol)
	trendMatches := 0
	volMatches := 0
	checkCount := minInt(5, len(d.regimeHistory))
	
	for i := len(d.regimeHistory) - checkCount; i < len(d.regimeHistory); i++ {
		if d.regimeHistory[i].Regime.Trend == proposed.Trend {
			trendMatches++
		}
		if d.regimeHistory[i].Regime.Vol == proposed.Vol {
			volMatches++
		}
	}

	// Require majority of recent snapshots to agree on BOTH trend and vol
	// This prevents rapid flipping between VolHigh/VolLow and strategy churn
	trendPersists := trendMatches >= (checkCount+1)/2
	volPersists := volMatches >= (checkCount+1)/2
	
	return trendPersists && volPersists
}

func (d *RobustDetector) addToHistory(r *Regime) {
	d.regimeHistory = append(d.regimeHistory, RegimeSnapshot{
		Regime:    *r,
		Timestamp: time.Now(),
	})
	
	if len(d.regimeHistory) > d.config.HistorySize {
		d.regimeHistory = d.regimeHistory[1:]
	}
}

func (d *RobustDetector) uncertainRegime(reason string) *Regime {
	return &Regime{
		Trend:  TrendSideways,
		Vol:    VolNormal,
		Stress: StressNormal,
		Score:  0.3, // Low confidence = uncertain
		Features: map[string]float64{
			"uncertain": 1,
		},
		AsOf: time.Now(),
	}
}

func (d *RobustDetector) createRegime(trend TrendState, vol VolState, stress StressState, 
	score float64, features map[string]float64) *Regime {
	return &Regime{
		Trend:    trend,
		Vol:      vol,
		Stress:   stress,
		Score:    score,
		Features: features,
		AsOf:     time.Now(),
	}
}

// GetCurrentRegime returns the current regime
func (d *RobustDetector) GetCurrentRegime() *Regime {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.currentRegime
}

// GetSwitchCount returns number of regime switches
func (d *RobustDetector) GetSwitchCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.regimeSwitchCount
}

// GetRegimeAge returns how long current regime has been active
func (d *RobustDetector) GetRegimeAge() time.Duration {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.currentRegime == nil {
		return 0
	}
	return time.Since(d.regimeStartTime)
}

// Helper functions

func calculateADX(prices, highs, lows []float64, period int) float64 {
	if len(prices) < period+1 {
		return 20 // neutral default
	}

	n := len(prices)
	var sumDX float64

	for i := n - period; i < n; i++ {
		if i < 1 {
			continue
		}
		upMove := highs[i] - highs[i-1]
		downMove := lows[i-1] - lows[i]

		var plusDM, minusDM float64
		if upMove > downMove && upMove > 0 {
			plusDM = upMove
		}
		if downMove > upMove && downMove > 0 {
			minusDM = downMove
		}

		tr := math.Max(highs[i]-lows[i],
			math.Max(math.Abs(highs[i]-prices[i-1]),
				math.Abs(lows[i]-prices[i-1])))

		if tr > 0 {
			dx := math.Abs(plusDM-minusDM) / (plusDM + minusDM + 0.0001) * 100
			sumDX += dx
		}
	}

	return sumDX / float64(period)
}

func calculateATR(prices, highs, lows []float64, period int) float64 {
	if len(prices) < period+1 {
		return 0
	}

	n := len(prices)
	sumTR := 0.0

	for i := n - period; i < n; i++ {
		if i < 1 {
			continue
		}
		tr := math.Max(highs[i]-lows[i],
			math.Max(math.Abs(highs[i]-prices[i-1]),
				math.Abs(lows[i]-prices[i-1])))
		sumTR += tr
	}

	return sumTR / float64(period)
}

func calculateRealizedVol(prices []float64, period int) float64 {
	if len(prices) < period+1 {
		return 0.02
	}

	n := len(prices)
	start := n - period
	if start < 1 {
		start = 1
	}

	returns := make([]float64, 0, period)
	for i := start; i < n; i++ {
		if prices[i-1] > 0 && prices[i] > 0 {
			logRet := math.Log(prices[i] / prices[i-1])
			// Guard against NaN/Inf
			if !math.IsNaN(logRet) && !math.IsInf(logRet, 0) {
				returns = append(returns, logRet)
			}
		}
	}

	if len(returns) < 5 {
		return 0.02 // Not enough valid returns
	}

	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))

	variance := 0.0
	for _, r := range returns {
		variance += (r - mean) * (r - mean)
	}
	variance /= float64(len(returns))

	// Annualize using 365 days for crypto (24/7 market)
	vol := math.Sqrt(variance) * math.Sqrt(365)
	
	// Guard against unreasonable values
	if math.IsNaN(vol) || math.IsInf(vol, 0) || vol > 10 {
		return 0.02
	}
	
	return vol
}

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
	// We want to compare current ATR against the distribution of ATRs over the lookback window
	startAnalysisIdx := n - historyPeriod
	if startAnalysisIdx < atrPeriod+1 {
		startAnalysisIdx = atrPeriod + 1
	}

	for endIdx := startAnalysisIdx; endIdx < n; endIdx++ {
		// Get the ATR as of that point in time
		// We pass the full history up to endIdx so calculateATR has enough data
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

func maxSlice(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	m := vals[0]
	for _, v := range vals[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// calculateChoppinessIndex measures how "choppy" vs "trending" the market is
// Returns value 0-100: High (>61.8) = choppy/ranging, Low (<38.2) = trending
func calculateChoppinessIndex(prices, highs, lows []float64, period int) float64 {
	n := len(prices)
	if n < period+1 {
		return 50 // Neutral default
	}

	// Calculate sum of True Range over period
	atrSum := 0.0
	for i := n - period; i < n; i++ {
		if i < 1 {
			continue
		}
		tr := math.Max(highs[i]-lows[i],
			math.Max(math.Abs(highs[i]-prices[i-1]),
				math.Abs(lows[i]-prices[i-1])))
		atrSum += tr
	}

	// Find highest high and lowest low over period
	highestHigh := highs[n-period]
	lowestLow := lows[n-period]
	for i := n - period; i < n; i++ {
		if highs[i] > highestHigh {
			highestHigh = highs[i]
		}
		if lows[i] < lowestLow {
			lowestLow = lows[i]
		}
	}

	priceRange := highestHigh - lowestLow
	if priceRange <= 0 || atrSum <= 0 {
		return 50 // Neutral
	}

	// Choppiness Index formula
	ci := 100 * math.Log10(atrSum/priceRange) / math.Log10(float64(period))

	// Clamp to 0-100
	if ci < 0 {
		ci = 0
	} else if ci > 100 {
		ci = 100
	}

	return ci
}

// calculateBollingerBandWidth returns the Bollinger Band Width percentile
// Used to detect volatility compression (potential breakout) vs expansion
func calculateBollingerBandWidth(prices []float64, period int) (width float64, percentile float64) {
	n := len(prices)
	if n < period {
		return 0.02, 0.5
	}

	// Calculate current BB width
	start := n - period
	mean := 0.0
	for i := start; i < n; i++ {
		mean += prices[i]
	}
	mean /= float64(period)

	variance := 0.0
	for i := start; i < n; i++ {
		variance += (prices[i] - mean) * (prices[i] - mean)
	}
	variance /= float64(period)
	stdDev := math.Sqrt(variance)

	if mean <= 0 {
		return 0.02, 0.5
	}

	currentWidth := (2 * 2 * stdDev) / mean // 2 std dev bands, normalized

	// Calculate historical widths for percentile
	if n < period*2 {
		return currentWidth, 0.5
	}

	var widths []float64
	for endIdx := period; endIdx <= n; endIdx++ {
		startIdx := endIdx - period
		m := 0.0
		for i := startIdx; i < endIdx; i++ {
			m += prices[i]
		}
		m /= float64(period)

		v := 0.0
		for i := startIdx; i < endIdx; i++ {
			v += (prices[i] - m) * (prices[i] - m)
		}
		v /= float64(period)
		sd := math.Sqrt(v)

		if m > 0 {
			w := (2 * 2 * sd) / m
			widths = append(widths, w)
		}
	}

	if len(widths) == 0 {
		return currentWidth, 0.5
	}

	// Percentile rank
	lower := 0
	for _, w := range widths {
		if w < currentWidth {
			lower++
		}
	}

	return currentWidth, float64(lower) / float64(len(widths))
}
