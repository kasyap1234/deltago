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
	StrategyID string
	Strategy   strategies.Strategy
	Weight     float64 // fraction of risk budget
	Reason     string
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
		if score.Score > 0.15 { // Lowered from 0.3 to allow Trend-only matches at 0.67 confidence
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

	// Vol preference match - only exact matches get full bonus, VolNormal is a weak match
	prefVol := strat.PreferredVol()
	if prefVol == r.Vol {
		score.Score += 0.25
		score.Reasons = append(score.Reasons, "exact vol preference match")
	} else if prefVol == regime.VolNormal {
		// VolNormal preference = strategy works in any vol, give small bonus
		score.Score += 0.1
		score.Reasons = append(score.Reasons, "vol-neutral strategy")
	}
	// No bonus if vol preference doesn't match (mismatch is penalty by omission)

	// Regime confidence boost
	score.Score *= r.Score

	// Penalize if we already have many positions
	positionCount := len(pf.GetPositionsByStrategy(strat.ID()))
	if positionCount > 0 {
		score.Score *= 0.5 // Reduce score for strategies with existing positions
		score.Reasons = append(score.Reasons, fmt.Sprintf("existing positions: %d", positionCount))
	}

	// Penalize near event risk - affects ALL short premium strategies
	if r.EventRisk != regime.EventRiskNone {
		stratID := strat.ID()
		// Long premium strategies benefit from events
		if stratID == "long_straddle" || stratID == "protective_put" {
			score.Score *= 1.3
			score.Reasons = append(score.Reasons, "event risk - vol expansion opportunity")
		} else if isShortPremiumStrategy(stratID) {
			// ALL short premium strategies are dangerous near events
			score.Score *= 0.2
			score.Reasons = append(score.Reasons, "event risk - avoid short premium")
		}
	}

	// Special handling for crash - be more aggressive about protection
	if r.Stress == regime.StressCrash {
		if strat.ID() == "protective_put" || strat.ID() == "bear_put_spread" {
			score.Score = 1.0 // Force high score for protective strategies
			score.Reasons = append(score.Reasons, "CRASH PROTECTION ACTIVATED")
		} else {
			score.Score *= 0.05 // Almost zero other strategies
			score.Reasons = append(score.Reasons, "crash - avoiding non-protective strategy")
		}
	}

	return score
}

// isShortPremiumStrategy returns true if the strategy sells premium (short gamma/vega)
func isShortPremiumStrategy(stratID string) bool {
	shortPremiumStrategies := map[string]bool{
		"iron_condor":        true,
		"iron_butterfly":     true,
		"put_credit_spread":  true,
		"call_credit_spread": true,
		"short_strangle":     true,
		"short_straddle":     true,
	}
	return shortPremiumStrategies[stratID]
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
