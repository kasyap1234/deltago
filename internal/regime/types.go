package regime

import "time"

type TrendState string

const (
	TrendUp       TrendState = "UP"
	TrendDown     TrendState = "DOWN"
	TrendSideways TrendState = "SIDEWAYS"
)

type VolState string

const (
	VolHigh   VolState = "HIGH"
	VolLow    VolState = "LOW"
	VolNormal VolState = "NORMAL"
)

type StressState string

const (
	StressNormal StressState = "NORMAL"
	StressCrash  StressState = "CRASH"
)

// Regime represents the current market regime
type Regime struct {
	Trend    TrendState
	Vol      VolState
	Stress   StressState
	Score    float64            // confidence 0-1
	Features map[string]float64 // debug/telemetry
	AsOf     time.Time
}

// FeatureSet contains computed market features
type FeatureSet struct {
	// Price features
	Price       float64
	EMAFast     float64 // 8-period
	EMASlow     float64 // 21-period
	EMASlope    float64 // normalized slope
	
	// Trend features
	ADX         float64 // Average Directional Index
	TrendScore  float64 // -1 to 1
	
	// Volatility features
	RealizedVol float64 // rolling std of returns
	ATR         float64 // Average True Range
	ATRPct      float64 // ATR as % of price
	IVRank      float64 // IV percentile 0-1
	
	// Crash detection
	RecentReturn float64 // last N candles return
	VolSpike     float64 // current vol / avg vol
	DrawdownPct  float64 // from recent high
	
	AsOf time.Time
}

// IsValidForTrading returns true if regime is clear enough
func (r *Regime) IsValidForTrading() bool {
	return r.Score >= 0.5
}

// IsCrash returns true if in crash mode
func (r *Regime) IsCrash() bool {
	return r.Stress == StressCrash
}

// SuggestedStrategies returns strategy types suited for this regime
func (r *Regime) SuggestedStrategies() []string {
	if r.Stress == StressCrash {
		return []string{"protective_put", "put_spread"}
	}

	switch r.Trend {
	case TrendUp:
		if r.Vol == VolHigh {
			return []string{"bull_call_spread"}
		}
		return []string{"bull_call_spread", "put_credit_spread"}
	case TrendDown:
		if r.Vol == VolHigh {
			return []string{"bear_put_spread"}
		}
		return []string{"bear_put_spread", "call_credit_spread"}
	case TrendSideways:
		if r.Vol == VolHigh {
			return []string{"iron_condor", "iron_butterfly"}
		}
		if r.Vol == VolLow {
			return []string{"long_straddle", "long_strangle"} // vol expansion bet
		}
		return []string{"iron_condor"}
	}
	return []string{"iron_condor"} // default
}
