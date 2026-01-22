package selector

import (
	"context"
	"testing"

	"github.com/kiwhtas/deltago/internal/execution"
	"github.com/kiwhtas/deltago/internal/portfolio"
	"github.com/kiwhtas/deltago/internal/regime"
	"github.com/kiwhtas/deltago/internal/strategies"
)

// MockStrategy mocks strategies.Strategy
type MockStrategy struct {
	id       string
	suitable []regime.TrendState
	prefVol  regime.VolState
	hasPos   bool
}

func (m *MockStrategy) ID() string   { return m.id }
func (m *MockStrategy) Name() string { return m.id }
func (m *MockStrategy) SuitableRegimes() []regime.TrendState { return m.suitable }
func (m *MockStrategy) PreferredVol() regime.VolState { return m.prefVol }
func (m *MockStrategy) ShouldEnter(ctx context.Context, in strategies.Input) (bool, string, error) { return true, "", nil }
func (m *MockStrategy) BuildEntryOrders(ctx context.Context, in strategies.Input) (*execution.MultiLegOrder, error) { return nil, nil }
func (m *MockStrategy) ConfirmEntry(ctx context.Context, res *execution.MultiLegResult, meta *strategies.StrategyPositionMetadata) error { return nil }
func (m *MockStrategy) Manage(ctx context.Context, in strategies.Input) ([]execution.OrderRequest, error) { return nil, nil }
func (m *MockStrategy) BuildCloseOrders(ctx context.Context, in strategies.Input) (*execution.MultiLegOrder, error) { return nil, nil }
func (m *MockStrategy) OnFill(ctx context.Context, fill execution.Fill) error { return nil }
func (m *MockStrategy) GetPosition() *strategies.StrategyPosition { return nil }
func (m *MockStrategy) HasPosition() bool { return m.hasPos }

func TestStrategyScoring(t *testing.T) {
	s1 := &MockStrategy{id: "bull", suitable: []regime.TrendState{regime.TrendUp}, prefVol: regime.VolNormal}
	s2 := &MockStrategy{id: "condor", suitable: []regime.TrendState{regime.TrendSideways}, prefVol: regime.VolHigh}
	
	sel := NewRuleBasedSelector([]strategies.Strategy{s1, s2})
	pf := portfolio.NewState(10000, 1000)
	
	// Case 1: Uptrend
	rUp := &regime.Regime{Trend: regime.TrendUp, Vol: regime.VolNormal, Score: 0.8}
	plan, _ := sel.BuildPlan(context.Background(), rUp, pf)
	
	if len(plan.Intents) == 0 || plan.Intents[0].StrategyID != "bull" {
		t.Errorf("Expected 'bull' strategy for uptrend, got intents: %v", plan.Intents)
	}
	
	// Case 2: Sideways + High Vol
	rSide := &regime.Regime{Trend: regime.TrendSideways, Vol: regime.VolHigh, Score: 0.9}
	plan2, _ := sel.BuildPlan(context.Background(), rSide, pf)
	
	if len(plan2.Intents) == 0 || plan2.Intents[0].StrategyID != "condor" {
		t.Errorf("Expected 'condor' strategy for sideways high vol, got intents: %v", plan2.Intents)
	}
}

func TestStrategyScoring_EventRisk(t *testing.T) {
	// Long straddle should be boosted by event risk, iron condor penalized
	s1 := &MockStrategy{id: "long_straddle", suitable: []regime.TrendState{regime.TrendSideways}, prefVol: regime.VolLow}
	s2 := &MockStrategy{id: "iron_condor", suitable: []regime.TrendState{regime.TrendSideways}, prefVol: regime.VolHigh}
	
	sel := NewRuleBasedSelector([]strategies.Strategy{s1, s2})
	pf := portfolio.NewState(10000, 1000)
	
	r := &regime.Regime{
		Trend:     regime.TrendSideways,
		Vol:       regime.VolNormal,
		EventRisk: regime.EventRiskExpiry,
		Score:     1.0,
	}
	
	plan, _ := sel.BuildPlan(context.Background(), r, pf)
	
	// 'long_straddle' should be the top pick due to event risk boost
	if len(plan.Intents) == 0 || plan.Intents[0].StrategyID != "long_straddle" {
		t.Errorf("Expected 'long_straddle' to be top pick during event risk, got %v", plan.Intents)
	}
}
