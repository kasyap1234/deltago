package selector

import (
	"context"

	"github.com/kiwhtas/deltago/internal/portfolio"
	"github.com/kiwhtas/deltago/internal/regime"
	"github.com/kiwhtas/deltago/internal/strategies"
)

// StrategyIntent represents intent to run a strategy
type StrategyIntent struct {
	StrategyID   string
	Strategy     strategies.Strategy
	Weight       float64 // fraction of risk budget
	Reason       string
}

// StrategyPlan is the output of strategy selection
type StrategyPlan struct {
	Regime      *regime.Regime
	Intents     []StrategyIntent
	TotalWeight float64
}

// Selector chooses which strategies to run based on regime
type Selector interface {
	BuildPlan(ctx context.Context, r *regime.Regime, pf *portfolio.State) (*StrategyPlan, error)
}

// RuleBasedSelector implements regime-to-strategy mapping
type RuleBasedSelector struct {
	strategies map[string]strategies.Strategy
	
	// Configuration
	MaxStrategiesActive int
	MinRegimeConfidence float64
}

// NewRuleBasedSelector creates a new selector
func NewRuleBasedSelector(strats []strategies.Strategy) *RuleBasedSelector {
	stratMap := make(map[string]strategies.Strategy)
	for _, s := range strats {
		stratMap[s.ID()] = s
	}
	
	return &RuleBasedSelector{
		strategies:          stratMap,
		MaxStrategiesActive: 3,
		MinRegimeConfidence: 0.5,
	}
}

// BuildPlan selects strategies for the current regime
func (s *RuleBasedSelector) BuildPlan(ctx context.Context, r *regime.Regime, pf *portfolio.State) (*StrategyPlan, error) {
	plan := &StrategyPlan{
		Regime:  r,
		Intents: make([]StrategyIntent, 0),
	}
	
	// Skip if regime confidence is too low
	if r.Score < s.MinRegimeConfidence {
		return plan, nil
	}
	
	// Priority 1: Crash protection
	if r.Stress == regime.StressCrash {
		if strat, ok := s.strategies["protective_put"]; ok {
			plan.Intents = append(plan.Intents, StrategyIntent{
				StrategyID: "protective_put",
				Strategy:   strat,
				Weight:     0.5,
				Reason:     "crash detected",
			})
		}
		// Also consider bear put spread
		if strat, ok := s.strategies["bear_put_spread"]; ok {
			plan.Intents = append(plan.Intents, StrategyIntent{
				StrategyID: "bear_put_spread",
				Strategy:   strat,
				Weight:     0.3,
				Reason:     "crash protection",
			})
		}
		plan.TotalWeight = 0.8
		return plan, nil
	}
	
	// Priority 2: Trend following
	switch r.Trend {
	case regime.TrendUp:
		if strat, ok := s.strategies["bull_call_spread"]; ok {
			plan.Intents = append(plan.Intents, StrategyIntent{
				StrategyID: "bull_call_spread",
				Strategy:   strat,
				Weight:     0.4,
				Reason:     "uptrend confirmed",
			})
		}
		
	case regime.TrendDown:
		if strat, ok := s.strategies["bear_put_spread"]; ok {
			plan.Intents = append(plan.Intents, StrategyIntent{
				StrategyID: "bear_put_spread",
				Strategy:   strat,
				Weight:     0.4,
				Reason:     "downtrend confirmed",
			})
		}
		// Add protective puts in strong downtrends
		if r.Vol == regime.VolHigh {
			if strat, ok := s.strategies["protective_put"]; ok {
				plan.Intents = append(plan.Intents, StrategyIntent{
					StrategyID: "protective_put",
					Strategy:   strat,
					Weight:     0.2,
					Reason:     "high vol downtrend protection",
				})
			}
		}
		
	case regime.TrendSideways:
		// Volatility determines strategy
		switch r.Vol {
		case regime.VolHigh:
			// Sell premium when IV is high
			if strat, ok := s.strategies["iron_condor"]; ok {
				plan.Intents = append(plan.Intents, StrategyIntent{
					StrategyID: "iron_condor",
					Strategy:   strat,
					Weight:     0.4,
					Reason:     "sideways + high IV - sell premium",
				})
			}
			
		case regime.VolLow:
			// Buy premium when IV is low (expecting expansion)
			if strat, ok := s.strategies["long_straddle"]; ok {
				plan.Intents = append(plan.Intents, StrategyIntent{
					StrategyID: "long_straddle",
					Strategy:   strat,
					Weight:     0.3,
					Reason:     "sideways + low IV - vol expansion bet",
				})
			}
			
		default:
			// Normal vol sideways - iron condor with smaller size
			if strat, ok := s.strategies["iron_condor"]; ok {
				plan.Intents = append(plan.Intents, StrategyIntent{
					StrategyID: "iron_condor",
					Strategy:   strat,
					Weight:     0.3,
					Reason:     "sideways market",
				})
			}
		}
	}
	
	// Calculate total weight
	for _, intent := range plan.Intents {
		plan.TotalWeight += intent.Weight
	}
	
	// Limit number of strategies
	if len(plan.Intents) > s.MaxStrategiesActive {
		plan.Intents = plan.Intents[:s.MaxStrategiesActive]
	}
	
	return plan, nil
}

// GetActiveStrategies returns strategies that currently have positions
func (s *RuleBasedSelector) GetActiveStrategies() []strategies.Strategy {
	var active []strategies.Strategy
	for _, strat := range s.strategies {
		if strat.HasPosition() {
			active = append(active, strat)
		}
	}
	return active
}

// GetAllStrategies returns all registered strategies
func (s *RuleBasedSelector) GetAllStrategies() []strategies.Strategy {
	var all []strategies.Strategy
	for _, strat := range s.strategies {
		all = append(all, strat)
	}
	return all
}
