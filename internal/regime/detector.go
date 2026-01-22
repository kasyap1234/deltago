package regime

import (
	"math"
	"time"
)

// OHLCV represents a candlestick
type OHLCV struct {
	Timestamp time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
}

// Detector detects market regime from price data
type Detector struct {
	// Configuration
	EMAFastPeriod  int
	EMASlowPeriod  int
	ADXPeriod      int
	VolPeriod      int
	CrashThreshold float64 // % drop to trigger crash
	VolSpikeThresh float64 // vol spike multiplier

	// State
	priceHistory []float64
	highHistory  []float64
	lowHistory   []float64
	volHistory   []float64
	lastRegime   *Regime
}

// NewDetector creates a new regime detector
func NewDetector() *Detector {
	return &Detector{
		EMAFastPeriod:  8,
		EMASlowPeriod:  21,
		ADXPeriod:      14,
		VolPeriod:      20,
		CrashThreshold: 0.05, // 5% drop
		VolSpikeThresh: 2.0,  // 2x normal vol
		priceHistory:   make([]float64, 0, 100),
		highHistory:    make([]float64, 0, 100),
		lowHistory:     make([]float64, 0, 100),
		volHistory:     make([]float64, 0, 100),
	}
}

// Update adds new candle data and returns updated regime
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

	// Update volatility history
	if len(d.priceHistory) >= d.VolPeriod+1 {
		currentVol := d.computeRealizedVol()
		d.volHistory = append(d.volHistory, currentVol)

		// Keep last 100 vol readings
		if len(d.volHistory) > 100 {
			d.volHistory = d.volHistory[1:]
		}
	}

	features := d.computeFeatures()
	regime := d.classifyRegime(features)
	d.lastRegime = regime

	return regime, nil
}

// UpdateBatch processes multiple candles
func (d *Detector) UpdateBatch(candles []OHLCV) (*Regime, error) {
	var regime *Regime
	for _, c := range candles {
		r, err := d.Update(c)
		if err != nil {
			return nil, err
		}
		regime = r
	}
	return regime, nil
}

// GetCurrentRegime returns the last computed regime
func (d *Detector) GetCurrentRegime() *Regime {
	return d.lastRegime
}

func (d *Detector) computeFeatures() *FeatureSet {
	if len(d.priceHistory) < d.EMASlowPeriod+5 {
		return &FeatureSet{AsOf: time.Now()}
	}

	prices := d.priceHistory
	n := len(prices)

	fs := &FeatureSet{
		Price:   prices[n-1],
		EMAFast: ema(prices, d.EMAFastPeriod),
		EMASlow: ema(prices, d.EMASlowPeriod),
		AsOf:    time.Now(),
	}

	// EMA slope (normalized)
	if fs.EMASlow > 0 {
		fs.EMASlope = (fs.EMAFast - fs.EMASlow) / fs.EMASlow
	}

	// ADX approximation (simplified)
	fs.ADX = d.computeADX()

	// Trend score: -1 to 1
	fs.TrendScore = d.computeTrendScore(fs)

	// Realized volatility
	fs.RealizedVol = d.computeRealizedVol()

	// ATR
	fs.ATR = d.computeATR()
	if fs.Price > 0 {
		fs.ATRPct = fs.ATR / fs.Price
	}

	// Recent return (for crash detection)
	if n >= 5 {
		fs.RecentReturn = (prices[n-1] - prices[n-5]) / prices[n-5]
	}

	// Vol spike
	avgVol := d.computeAvgVol()
	if avgVol > 0 {
		fs.VolSpike = fs.RealizedVol / avgVol
	}

	// Drawdown from recent high
	startIdx := n - 20
	if startIdx < 0 {
		startIdx = 0
	}
	recentHigh := maxFloat(d.highHistory[startIdx:]...)
	if recentHigh > 0 {
		fs.DrawdownPct = (recentHigh - fs.Price) / recentHigh
	}

	// IV Rank (approximated from price volatility)
	fs.IVRank = d.computeIVRank()

	return fs
}

func (d *Detector) classifyRegime(fs *FeatureSet) *Regime {
	r := &Regime{
		Trend:    TrendSideways,
		Vol:      VolNormal,
		Stress:   StressNormal,
		Features: make(map[string]float64),
		AsOf:     time.Now(),
	}

	// Store features for debugging
	r.Features["ema_slope"] = fs.EMASlope
	r.Features["adx"] = fs.ADX
	r.Features["trend_score"] = fs.TrendScore
	r.Features["realized_vol"] = fs.RealizedVol
	r.Features["atr_pct"] = fs.ATRPct
	r.Features["recent_return"] = fs.RecentReturn
	r.Features["vol_spike"] = fs.VolSpike
	r.Features["drawdown"] = fs.DrawdownPct
	r.Features["iv_rank"] = fs.IVRank

	// 1. Crash detection (highest priority)
	if fs.RecentReturn < -d.CrashThreshold ||
		(fs.DrawdownPct > 0.08 && fs.VolSpike > d.VolSpikeThresh) {
		r.Stress = StressCrash
		r.Score = 0.9
	}

	// 2. Trend classification
	if fs.ADX > 25 {
		// Strong trend
		if fs.TrendScore > 0.3 {
			r.Trend = TrendUp
			r.Score = math.Min(1.0, 0.5+fs.ADX/100)
		} else if fs.TrendScore < -0.3 {
			r.Trend = TrendDown
			r.Score = math.Min(1.0, 0.5+fs.ADX/100)
		}
	} else if fs.ADX < 20 {
		// Weak trend = sideways
		r.Trend = TrendSideways
		r.Score = math.Max(0.5, 1.0-fs.ADX/40)
	} else {
		// Transition zone
		if fs.TrendScore > 0.2 {
			r.Trend = TrendUp
		} else if fs.TrendScore < -0.2 {
			r.Trend = TrendDown
		}
		r.Score = 0.6
	}

	// 3. Volatility classification
	if fs.IVRank > 0.7 || fs.ATRPct > 0.04 {
		r.Vol = VolHigh
	} else if fs.IVRank < 0.3 && fs.ATRPct < 0.015 {
		r.Vol = VolLow
	}

	return r
}

// Helper functions

func ema(prices []float64, period int) float64 {
	if len(prices) < period {
		return 0
	}

	multiplier := 2.0 / float64(period+1)
	emaVal := prices[0]

	for i := 1; i < len(prices); i++ {
		emaVal = (prices[i]-emaVal)*multiplier + emaVal
	}
	return emaVal
}

func (d *Detector) computeADX() float64 {
	if len(d.priceHistory) < d.ADXPeriod+1 {
		return 20 // neutral default
	}

	n := len(d.priceHistory)
	var sumDX float64

	for i := n - d.ADXPeriod; i < n; i++ {
		if i < 1 {
			continue
		}
		upMove := d.highHistory[i] - d.highHistory[i-1]
		downMove := d.lowHistory[i-1] - d.lowHistory[i]

		var plusDM, minusDM float64
		if upMove > downMove && upMove > 0 {
			plusDM = upMove
		}
		if downMove > upMove && downMove > 0 {
			minusDM = downMove
		}

		tr := math.Max(d.highHistory[i]-d.lowHistory[i],
			math.Max(math.Abs(d.highHistory[i]-d.priceHistory[i-1]),
				math.Abs(d.lowHistory[i]-d.priceHistory[i-1])))

		if tr > 0 {
			dx := math.Abs(plusDM-minusDM) / (plusDM + minusDM + 0.0001) * 100
			sumDX += dx
		}
	}

	return sumDX / float64(d.ADXPeriod)
}

func (d *Detector) computeTrendScore(fs *FeatureSet) float64 {
	if len(d.priceHistory) < 20 {
		return 0
	}

	// Combine multiple trend signals
	score := 0.0

	// 1. EMA slope contribution
	score += fs.EMASlope * 10 // scaled

	// 2. Price vs EMAs
	if fs.Price > fs.EMAFast && fs.EMAFast > fs.EMASlow {
		score += 0.3
	} else if fs.Price < fs.EMAFast && fs.EMAFast < fs.EMASlow {
		score -= 0.3
	}

	// 3. Recent momentum
	n := len(d.priceHistory)
	if n >= 10 {
		ret10 := (d.priceHistory[n-1] - d.priceHistory[n-10]) / d.priceHistory[n-10]
		score += ret10 * 5
	}

	// Clamp to [-1, 1]
	return math.Max(-1, math.Min(1, score))
}

func (d *Detector) computeRealizedVol() float64 {
	if len(d.priceHistory) < d.VolPeriod+1 {
		return 0.02 // default
	}

	n := len(d.priceHistory)
	returns := make([]float64, d.VolPeriod)

	for i := 0; i < d.VolPeriod; i++ {
		idx := n - d.VolPeriod + i
		if idx > 0 {
			returns[i] = math.Log(d.priceHistory[idx] / d.priceHistory[idx-1])
		}
	}

	// Standard deviation
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

	return math.Sqrt(variance) * math.Sqrt(252) // Annualized
}

func (d *Detector) computeATR() float64 {
	if len(d.priceHistory) < d.ADXPeriod+1 {
		return 0
	}

	n := len(d.priceHistory)
	sumTR := 0.0

	for i := n - d.ADXPeriod; i < n; i++ {
		if i < 1 {
			continue
		}
		tr := math.Max(d.highHistory[i]-d.lowHistory[i],
			math.Max(math.Abs(d.highHistory[i]-d.priceHistory[i-1]),
				math.Abs(d.lowHistory[i]-d.priceHistory[i-1])))
		sumTR += tr
	}

	return sumTR / float64(d.ADXPeriod)
}

func (d *Detector) computeAvgVol() float64 {
	if len(d.volHistory) < 10 {
		return d.computeRealizedVol()
	}

	sum := 0.0
	for _, v := range d.volHistory {
		sum += v
	}
	return sum / float64(len(d.volHistory))
}

func (d *Detector) computeIVRank() float64 {
	// Simplified: use ATR percentile as IV proxy
	if len(d.priceHistory) < 50 {
		return 0.5
	}

	currentATR := d.computeATR()

	// Compute ATRs for historical windows
	var atrHistory []float64
	for i := 30; i < len(d.priceHistory); i++ {
		windowHigh := d.highHistory[i-14 : i]
		windowLow := d.lowHistory[i-14 : i]
		windowPrice := d.priceHistory[i-14 : i]

		sumTR := 0.0
		for j := 1; j < len(windowPrice); j++ {
			tr := math.Max(windowHigh[j]-windowLow[j],
				math.Max(math.Abs(windowHigh[j]-windowPrice[j-1]),
					math.Abs(windowLow[j]-windowPrice[j-1])))
			sumTR += tr
		}
		atrHistory = append(atrHistory, sumTR/float64(len(windowPrice)-1))
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

func maxFloat(vals ...float64) float64 {
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
