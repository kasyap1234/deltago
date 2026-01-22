package selector

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

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

// StrategyScore holds the score for a strategy
type StrategyScore struct {
	Strategy strategies.Strategy
	Score    float64
	Reasons  []string
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
	mu         sync.RWMutex
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
	s.mu.RLock()
	defer s.mu.RUnlock()

	plan := &StrategyPlan{
		Regime:  r,
		Intents: make([]StrategyIntent, 0),
	}
	
	// Skip if regime confidence is too low
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
	for i := 0; i < len(scores); i++ {
		if i >= s.MaxStrategiesActive {
			break
		}
		
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
			score.Reasons = append(score.Reasons, "event risk - vol expansion opportunity")
		} else if strat.ID() == "iron_condor" {
			// Dangerous to sell premium near events
			score.Score *= 0.3
			score.Reasons = append(score.Reasons, "event risk - avoid short premium")
		}
	}
	
	// Special handling for crash
	if r.Stress == regime.StressCrash {
		if strat.ID() == "protective_put" || strat.ID() == "bear_put_spread" {
			score.Score *= 2.0
			score.Reasons = append(score.Reasons, "CRASH PROTECTION")
		} else {
			score.Score *= 0.1 // Avoid other strategies
		}
	}
	
	return score
}

// GetActiveStrategies returns strategies that currently have positions
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

// GetAllStrategies returns all registered strategies
func (s *RuleBasedSelector) GetAllStrategies() []strategies.Strategy {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var all []strategies.Strategy
	for _, strat := range s.strategies {
		all = append(all, strat)
	}
	return all
}
