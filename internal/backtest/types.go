package backtest

import (
	"time"
)

type OHLCV struct {
	Timestamp time.Time `json:"timestamp"`
	Open      float64   `json:"open"`
	High      float64   `json:"high"`
	Low       float64   `json:"low"`
	Close     float64   `json:"close"`
	Volume    float64   `json:"volume"`
}

type BacktestConfig struct {
	StartDate      time.Time         `json:"start_date"`
	EndDate        time.Time         `json:"end_date"`
	Underlying     string            `json:"underlying"`
	InitialCapital float64           `json:"initial_capital"`
	PositionSize   float64           `json:"position_size"`
	StrategyParams map[string]string `json:"strategy_params"`
}

type BacktestResult struct {
	Config          BacktestConfig `json:"config"`
	Trades          []TradeRecord  `json:"trades"`
	TotalReturn     float64        `json:"total_return"`
	TotalReturnPct  float64        `json:"total_return_pct"`
	SharpeRatio     float64        `json:"sharpe_ratio"`
	MaxDrawdown     float64        `json:"max_drawdown"`
	MaxDrawdownPct  float64        `json:"max_drawdown_pct"`
	WinRate         float64        `json:"win_rate"`
	ProfitFactor    float64        `json:"profit_factor"`
	TotalTrades     int            `json:"total_trades"`
	WinningTrades   int            `json:"winning_trades"`
	LosingTrades    int            `json:"losing_trades"`
	AverageWin      float64        `json:"average_win"`
	AverageLoss     float64        `json:"average_loss"`
	FinalCapital    float64        `json:"final_capital"`
	StartDate       time.Time      `json:"start_date"`
	EndDate         time.Time      `json:"end_date"`
}

type TradeSide string

const (
	TradeSideLong  TradeSide = "long"
	TradeSideShort TradeSide = "short"
)

type TradeRecord struct {
	ID         int       `json:"id"`
	EntryTime  time.Time `json:"entry_time"`
	ExitTime   time.Time `json:"exit_time"`
	Side       TradeSide `json:"side"`
	EntryPrice float64   `json:"entry_price"`
	ExitPrice  float64   `json:"exit_price"`
	Quantity   float64   `json:"quantity"`
	PnL        float64   `json:"pnl"`
	PnLPct     float64   `json:"pnl_pct"`
	Commission float64   `json:"commission"`
}

func (r *BacktestResult) CalculateMetrics() {
	if len(r.Trades) == 0 {
		return
	}

	var totalPnL, grossProfit, grossLoss float64
	var winCount, lossCount int
	var returns []float64

	for _, trade := range r.Trades {
		totalPnL += trade.PnL
		if trade.PnL > 0 {
			grossProfit += trade.PnL
			winCount++
		} else {
			grossLoss += -trade.PnL
			lossCount++
		}
		returns = append(returns, trade.PnLPct)
	}

	r.TotalTrades = len(r.Trades)
	r.WinningTrades = winCount
	r.LosingTrades = lossCount
	r.TotalReturn = totalPnL
	r.TotalReturnPct = (totalPnL / r.Config.InitialCapital) * 100
	r.FinalCapital = r.Config.InitialCapital + totalPnL

	if r.TotalTrades > 0 {
		r.WinRate = float64(winCount) / float64(r.TotalTrades) * 100
	}
	if winCount > 0 {
		r.AverageWin = grossProfit / float64(winCount)
	}
	if lossCount > 0 {
		r.AverageLoss = grossLoss / float64(lossCount)
	}
	if grossLoss > 0 {
		r.ProfitFactor = grossProfit / grossLoss
	}

	r.SharpeRatio = calculateSharpeRatio(returns)
	r.MaxDrawdown, r.MaxDrawdownPct = calculateMaxDrawdown(r.Trades, r.Config.InitialCapital)
}

func calculateSharpeRatio(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}

	var sum float64
	for _, r := range returns {
		sum += r
	}
	mean := sum / float64(len(returns))

	var variance float64
	for _, r := range returns {
		diff := r - mean
		variance += diff * diff
	}
	variance /= float64(len(returns) - 1)

	if variance == 0 {
		return 0
	}

	stdDev := sqrt(variance)
	annualizationFactor := sqrt(252)
	return (mean / stdDev) * annualizationFactor
}

func calculateMaxDrawdown(trades []TradeRecord, initialCapital float64) (maxDD, maxDDPct float64) {
	if len(trades) == 0 {
		return 0, 0
	}

	equity := initialCapital
	peak := equity

	for _, trade := range trades {
		equity += trade.PnL
		if equity > peak {
			peak = equity
		}
		drawdown := peak - equity
		if drawdown > maxDD {
			maxDD = drawdown
			maxDDPct = (drawdown / peak) * 100
		}
	}

	return maxDD, maxDDPct
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x / 2
	for i := 0; i < 100; i++ {
		zNew := z - (z*z-x)/(2*z)
		if abs(zNew-z) < 1e-10 {
			return zNew
		}
		z = zNew
	}
	return z
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
