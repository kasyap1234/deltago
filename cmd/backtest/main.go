package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kiwhtas/deltago/internal/backtest"
)

func main() {
	underlying := flag.String("underlying", "BTC", "Underlying asset (BTC or ETH)")
	strategy := flag.String("strategy", "straddle", "Strategy to backtest (straddle, condor, or both)")
	days := flag.Int("days", 90, "Number of days to backtest")
	size := flag.Float64("size", 1.0, "Position size multiplier")
	capital := flag.Float64("capital", 10000, "Initial capital for metrics")
	stopLoss := flag.Float64("stoploss", 1.5, "Stop-loss multiplier for straddle")
	shortDelta := flag.Float64("delta", 0.25, "Short delta for iron condor")
	wingWidth := flag.Float64("wing", 500, "Wing width for iron condor")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `DeltaGo Backtest Engine

Usage: backtest [options]

Options:
`)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  backtest -underlying BTC -strategy straddle -days 90
  backtest -underlying ETH -strategy condor -days 180 -delta 0.20
  backtest -underlying BTC -strategy both -days 365 -size 2.0

`)
	}

	flag.Parse()

	underlying_upper := strings.ToUpper(*underlying)
	if underlying_upper != "BTC" && underlying_upper != "ETH" {
		fmt.Fprintf(os.Stderr, "Error: underlying must be BTC or ETH\n")
		os.Exit(1)
	}

	var strategyType backtest.StrategyType
	switch strings.ToLower(*strategy) {
	case "straddle":
		strategyType = backtest.StrategyStraddle
	case "condor":
		strategyType = backtest.StrategyCondor
	case "both":
		strategyType = backtest.StrategyBoth
	default:
		fmt.Fprintf(os.Stderr, "Error: strategy must be straddle, condor, or both\n")
		os.Exit(1)
	}

	cfg := backtest.EngineConfig{
		Underlying:         underlying_upper,
		Days:               *days,
		PositionSize:       *size,
		InitialCapital:     *capital,
		StopLossMultiplier: *stopLoss,
		ShortDelta:         *shortDelta,
		WingWidth:          *wingWidth,
		StrikeStep:         100,
	}

	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println("                    DeltaGo Backtest Engine                     ")
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Printf("  Underlying:    %s\n", cfg.Underlying)
	fmt.Printf("  Strategy:      %s\n", *strategy)
	fmt.Printf("  Period:        %d days\n", cfg.Days)
	fmt.Printf("  Position Size: %.2f\n", cfg.PositionSize)
	fmt.Println("───────────────────────────────────────────────────────────────")
	fmt.Println("  Fetching historical data...")

	engine := backtest.NewEngine(cfg)
	result, err := engine.Run(strategyType)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("  Date Range:    %s to %s\n",
		result.StartDate.Format("2006-01-02"),
		result.EndDate.Format("2006-01-02"))
	fmt.Println("═══════════════════════════════════════════════════════════════")

	if result.Straddle != nil {
		printStrategyResult("STRADDLE", result.Straddle, cfg.Days)
	}

	if result.Condor != nil {
		printStrategyResult("IRON CONDOR", result.Condor, cfg.Days)
	}

	if result.Straddle != nil && result.Condor != nil {
		printComparison(result.Straddle, result.Condor)
	}
}

func printStrategyResult(name string, r *backtest.BacktestResult, days int) {
	fmt.Printf("\n┌─ %s RESULTS ─────────────────────────────────────────────┐\n", name)
	fmt.Println("│")
	fmt.Printf("│  Total Trades:     %d\n", r.TotalTrades)
	fmt.Printf("│  Winning Trades:   %d (%.1f%%)\n", r.WinningTrades, r.WinRate)
	fmt.Printf("│  Losing Trades:    %d\n", r.LosingTrades)
	fmt.Println("│")
	fmt.Println("├─ PERFORMANCE ───────────────────────────────────────────────")
	fmt.Println("│")

	pnlSign := "+"
	if r.TotalReturn < 0 {
		pnlSign = ""
	}
	fmt.Printf("│  Total P&L:        %s$%.2f (%s%.2f%%)\n", pnlSign, r.TotalReturn, pnlSign, r.TotalReturnPct)
	fmt.Printf("│  Final Capital:    $%.2f\n", r.FinalCapital)
	fmt.Println("│")
	fmt.Printf("│  Sharpe Ratio:     %.2f\n", r.SharpeRatio)
	fmt.Printf("│  Max Drawdown:     $%.2f (%.2f%%)\n", r.MaxDrawdown, r.MaxDrawdownPct)
	fmt.Printf("│  Profit Factor:    %.2f\n", r.ProfitFactor)
	fmt.Println("│")
	fmt.Printf("│  Average Win:      $%.2f\n", r.AverageWin)
	fmt.Printf("│  Average Loss:     $%.2f\n", r.AverageLoss)

	if days > 30 {
		monthly := r.GetMonthlyBreakdown()
		if len(monthly) > 0 {
			fmt.Println("│")
			fmt.Println("├─ MONTHLY BREAKDOWN ─────────────────────────────────────────")
			fmt.Println("│")
			fmt.Println("│    Month      Trades    P&L          Win Rate")
			fmt.Println("│  ─────────────────────────────────────────────")
			for _, m := range monthly {
				pnlStr := fmt.Sprintf("%+.2f", m.PnL)
				fmt.Printf("│    %s     %3d      %-12s  %.1f%%\n",
					m.Month, m.Trades, pnlStr, m.WinRate)
			}
		}
	}

	fmt.Println("│")
	fmt.Println("└──────────────────────────────────────────────────────────────")
}

func printComparison(straddle, condor *backtest.BacktestResult) {
	fmt.Println("\n┌─ STRATEGY COMPARISON ───────────────────────────────────────┐")
	fmt.Println("│")
	fmt.Println("│                      Straddle       Iron Condor")
	fmt.Println("│  ─────────────────────────────────────────────────")
	fmt.Printf("│  Total P&L:        %+10.2f      %+10.2f\n", straddle.TotalReturn, condor.TotalReturn)
	fmt.Printf("│  Win Rate:         %9.1f%%      %9.1f%%\n", straddle.WinRate, condor.WinRate)
	fmt.Printf("│  Sharpe Ratio:     %10.2f      %10.2f\n", straddle.SharpeRatio, condor.SharpeRatio)
	fmt.Printf("│  Max Drawdown:     %9.2f%%      %9.2f%%\n", straddle.MaxDrawdownPct, condor.MaxDrawdownPct)
	fmt.Printf("│  Profit Factor:    %10.2f      %10.2f\n", straddle.ProfitFactor, condor.ProfitFactor)
	fmt.Println("│")

	winner := "Straddle"
	if condor.SharpeRatio > straddle.SharpeRatio {
		winner = "Iron Condor"
	}
	fmt.Printf("│  Best Risk-Adjusted: %s\n", winner)
	fmt.Println("│")
	fmt.Println("└──────────────────────────────────────────────────────────────")
}
