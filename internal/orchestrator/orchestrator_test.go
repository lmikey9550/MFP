package orchestrator

import (
	"testing"
	"time"

	"mfp/internal/core"
	"mfp/internal/state"
)

type testResolver map[string]core.ProviderConfig

func (r testResolver) ProviderByID(id string) (core.ProviderConfig, bool) {
	value, ok := r[id]
	return value, ok
}

func TestScopeKeyFallsBackFromSessionToAgent(t *testing.T) {
	vm := core.VirtualModel{
		ID:          "smart",
		StickyScope: core.StickyScopeSession,
	}
	scope := ScopeKey(vm, core.RouteContext{AgentID: "ops"})
	if scope != "agent:ops:smart" {
		t.Fatalf("unexpected scope: %s", scope)
	}
}

func TestStickyCandidateMovesToFront(t *testing.T) {
	hub, err := state.NewHub(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vm := core.VirtualModel{
		ID:          "smart",
		Sticky:      true,
		StickyScope: core.StickyScopeAgent,
		Candidates: []core.ActualModelRef{
			{ProviderID: "p1", ModelID: "m1", Enabled: true, Priority: 1},
			{ProviderID: "p2", ModelID: "m2", Enabled: true, Priority: 2},
		},
	}
	hub.SetSticky("agent:ops:smart", "smart", core.ActualModelRef{ProviderID: "p2", ModelID: "m2"}, 30*time.Second)
	planner := New(testResolver{
		"p1": {ID: "p1", Enabled: true},
		"p2": {ID: "p2", Enabled: true},
	}, hub)

	plan, err := planner.Build(vm, core.RouteContext{AgentID: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Candidates[0].Key() != "p2/m2" {
		t.Fatalf("expected sticky candidate first, got %s", plan.Candidates[0].Key())
	}
}
