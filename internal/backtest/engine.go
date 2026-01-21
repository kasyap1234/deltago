package backtest

import (
	"fmt"
	"time"
)

type StrategyType string

const (
	StrategyStraddle StrategyType = "straddle"
	StrategyCondor   StrategyType = "condor"
	StrategyBoth     StrategyType = "both"
)

type EngineConfig struct {
	Underlying         string
	Days               int
	PositionSize       float64
	InitialCapital     float64
	StopLossMultiplier float64
	ShortDelta         float64
	WingWidth          float64
	StrikeStep         float64
}

func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		Underlying:         "BTC",
		Days:               90,
		PositionSize:       1.0,
		InitialCapital:     10000,
		StopLossMultiplier: 1.5,
		ShortDelta:         0.25,
		WingWidth:          500,
		StrikeStep:         100,
	}
}

type BacktestEngine struct {
	config      EngineConfig
	dataFetcher *DataFetcher
}

func NewEngine(cfg EngineConfig) *BacktestEngine {
	return &BacktestEngine{
		config:      cfg,
		dataFetcher: NewDataFetcher(""),
	}
}

func (e *BacktestEngine) Run(strategy StrategyType) (*CombinedResult, error) {
	data, err := e.dataFetcher.FetchOHLCV(e.config.Underlying, e.config.Days)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch data: %w", err)
	}

	if len(data) < RollingWindow+2 {
		return nil, fmt.Errorf("insufficient data: got %d candles, need at least %d", len(data), RollingWindow+2)
	}

	prices := make([]float64, len(data))
	highs := make([]float64, len(data))
	lows := make([]float64, len(data))
	for i, candle := range data {
		prices[i] = candle.Close
		highs[i] = candle.High
		lows[i] = candle.Low
	}

	result := &CombinedResult{
		Underlying: e.config.Underlying,
		StartDate:  data[RollingWindow].Timestamp,
		EndDate:    data[len(data)-1].Timestamp,
		Days:       e.config.Days,
	}

	switch strategy {
	case StrategyStraddle:
		straddleResults := BatchSimulateStraddles(prices, highs, lows, e.config.StopLossMultiplier)
		result.Straddle = e.buildStraddleResult(straddleResults, data)
	case StrategyCondor:
		condorResults := BatchSimulateCondors(prices, e.config.ShortDelta, e.config.WingWidth, e.config.StrikeStep)
		result.Condor = e.buildCondorResult(condorResults, data)
	case StrategyBoth:
		straddleResults := BatchSimulateStraddles(prices, highs, lows, e.config.StopLossMultiplier)
		result.Straddle = e.buildStraddleResult(straddleResults, data)
		condorResults := BatchSimulateCondors(prices, e.config.ShortDelta, e.config.WingWidth, e.config.StrikeStep)
		result.Condor = e.buildCondorResult(condorResults, data)
	default:
		return nil, fmt.Errorf("unknown strategy: %s", strategy)
	}

	return result, nil
}

func (e *BacktestEngine) buildStraddleResult(simResults []StraddleResult, data []OHLCV) *BacktestResult {
	result := &BacktestResult{
		Config: BacktestConfig{
			StartDate:      data[RollingWindow].Timestamp,
			EndDate:        data[len(data)-1].Timestamp,
			Underlying:     e.config.Underlying,
			InitialCapital: e.config.InitialCapital,
			PositionSize:   e.config.PositionSize,
			StrategyParams: map[string]string{
				"strategy":        "straddle",
				"stop_loss_mult":  fmt.Sprintf("%.2f", e.config.StopLossMultiplier),
			},
		},
		StartDate: data[RollingWindow].Timestamp,
		EndDate:   data[len(data)-1].Timestamp,
	}

	for i, sim := range simResults {
		dataIdx := RollingWindow + i
		// Scale P&L by position size, but calculate return on margin required
		scaledPnL := sim.PnL * e.config.PositionSize
		scaledMargin := sim.MarginRequired * e.config.PositionSize
		
		trade := TradeRecord{
			ID:         i + 1,
			EntryTime:  data[dataIdx].Timestamp,
			ExitTime:   data[dataIdx+1].Timestamp,
			Side:       TradeSideShort,
			EntryPrice: sim.TotalPremium * e.config.PositionSize,
			ExitPrice:  sim.TotalIntrinsic * e.config.PositionSize,
			Quantity:   e.config.PositionSize,
			PnL:        scaledPnL,
			PnLPct:     (scaledPnL / scaledMargin) * 100, // Return on margin
		}
		result.Trades = append(result.Trades, trade)
	}

	result.CalculateMetrics()
	return result
}

func (e *BacktestEngine) buildCondorResult(simResults []IronCondorResult, data []OHLCV) *BacktestResult {
	result := &BacktestResult{
		Config: BacktestConfig{
			StartDate:      data[RollingWindow].Timestamp,
			EndDate:        data[len(data)-1].Timestamp,
			Underlying:     e.config.Underlying,
			InitialCapital: e.config.InitialCapital,
			PositionSize:   e.config.PositionSize,
			StrategyParams: map[string]string{
				"strategy":    "condor",
				"short_delta": fmt.Sprintf("%.2f", e.config.ShortDelta),
				"wing_width":  fmt.Sprintf("%.0f", e.config.WingWidth),
			},
		},
		StartDate: data[RollingWindow].Timestamp,
		EndDate:   data[len(data)-1].Timestamp,
	}

	for i, sim := range simResults {
		dataIdx := RollingWindow + i
		scaledPnL := sim.PnL * e.config.PositionSize
		scaledMargin := sim.MarginRequired * e.config.PositionSize
		
		pnlPct := 0.0
		if scaledMargin > 0 {
			pnlPct = (scaledPnL / scaledMargin) * 100 // Return on margin
		}
		trade := TradeRecord{
			ID:         i + 1,
			EntryTime:  data[dataIdx].Timestamp,
			ExitTime:   data[dataIdx+1].Timestamp,
			Side:       TradeSideShort,
			EntryPrice: sim.NetCredit * e.config.PositionSize,
			ExitPrice:  0,
			Quantity:   e.config.PositionSize,
			PnL:        scaledPnL,
			PnLPct:     pnlPct,
		}
		result.Trades = append(result.Trades, trade)
	}

	result.CalculateMetrics()
	return result
}

type CombinedResult struct {
	Underlying string
	StartDate  time.Time
	EndDate    time.Time
	Days       int
	Straddle   *BacktestResult
	Condor     *BacktestResult
}

type MonthlyBreakdown struct {
	Month      string
	Trades     int
	PnL        float64
	WinRate    float64
	Winners    int
	Losers     int
}

func (r *BacktestResult) GetMonthlyBreakdown() []MonthlyBreakdown {
	if len(r.Trades) == 0 {
		return nil
	}

	monthlyData := make(map[string]*MonthlyBreakdown)

	for _, trade := range r.Trades {
		monthKey := trade.EntryTime.Format("2006-01")
		if _, exists := monthlyData[monthKey]; !exists {
			monthlyData[monthKey] = &MonthlyBreakdown{Month: monthKey}
		}
		mb := monthlyData[monthKey]
		mb.Trades++
		mb.PnL += trade.PnL
		if trade.PnL > 0 {
			mb.Winners++
		} else {
			mb.Losers++
		}
	}

	var months []string
	for m := range monthlyData {
		months = append(months, m)
	}

	for i := 0; i < len(months)-1; i++ {
		for j := i + 1; j < len(months); j++ {
			if months[i] > months[j] {
				months[i], months[j] = months[j], months[i]
			}
		}
	}

	var result []MonthlyBreakdown
	for _, m := range months {
		mb := monthlyData[m]
		if mb.Trades > 0 {
			mb.WinRate = float64(mb.Winners) / float64(mb.Trades) * 100
		}
		result = append(result, *mb)
	}

	return result
}
