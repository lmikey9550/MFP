package orchestrator

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"mfp/internal/core"
	"mfp/internal/state"
)

type ProviderResolver interface {
	ProviderByID(id string) (core.ProviderConfig, bool)
}

type Planner struct {
	resolver ProviderResolver
	state    *state.Hub
}

func New(resolver ProviderResolver, hub *state.Hub) *Planner {
	return &Planner{
		resolver: resolver,
		state:    hub,
	}
}

func (p *Planner) Build(vm core.VirtualModel, route core.RouteContext) (core.RoutePlan, error) {
	scopeKey := ScopeKey(vm, route)
	candidates := append([]core.ActualModelRef(nil), vm.Candidates...)
	candidates = filterEnabled(candidates)
	if len(candidates) == 0 {
		return core.RoutePlan{}, errors.New("no enabled candidates")
	}
	candidates = orderByStrategy(vm.FailoverStrategy, candidates)

	plan := core.RoutePlan{
		ScopeKey:   scopeKey,
		Reason:     "strategy",
		Candidates: candidates,
	}
	if vm.Sticky && scopeKey != "" {
		if sticky, ok := p.state.GetSticky(scopeKey, vm.ID); ok {
			if reordered, hit := moveStickyFirst(candidates, sticky.ActualModel); hit {
				plan.Candidates = reordered
				plan.StickyHit = true
				plan.Reason = "sticky"
			}
		}
	}
	plan.Candidates = p.applyAvailability(plan.Candidates)
	if len(plan.Candidates) == 0 {
		return core.RoutePlan{}, errors.New("no available candidates after filtering")
	}
	if vm.Congestion.Enabled {
		plan.Candidates = p.applyCongestion(vm, plan.Candidates)
	}
	if vm.MaxAttempts > 0 && len(plan.Candidates) > vm.MaxAttempts {
		plan.Candidates = plan.Candidates[:vm.MaxAttempts]
	}
	return plan, nil
}

func ScopeKey(vm core.VirtualModel, route core.RouteContext) string {
	switch vm.StickyScope {
	case core.StickyScopeSession:
		if route.SessionID != "" {
			return "session:" + route.SessionID + ":" + vm.ID
		}
		if route.AgentID != "" {
			return "agent:" + route.AgentID + ":" + vm.ID
		}
		return "global:" + vm.ID
	case core.StickyScopeAgent:
		if route.AgentID != "" {
			return "agent:" + route.AgentID + ":" + vm.ID
		}
		return "global:" + vm.ID
	case core.StickyScopeGlobal:
		return "global:" + vm.ID
	default:
		return ""
	}
}

func filterEnabled(in []core.ActualModelRef) []core.ActualModelRef {
	out := make([]core.ActualModelRef, 0, len(in))
	for _, item := range in {
		if item.Enabled {
			out = append(out, item)
		}
	}
	return out
}

func moveStickyFirst(in []core.ActualModelRef, key string) ([]core.ActualModelRef, bool) {
	out := append([]core.ActualModelRef(nil), in...)
	for i, item := range out {
		if item.Key() == key {
			copy(out[1:i+1], out[0:i])
			out[0] = item
			return out, true
		}
	}
	return out, false
}

func orderByStrategy(strategy core.FailoverStrategy, in []core.ActualModelRef) []core.ActualModelRef {
	out := append([]core.ActualModelRef(nil), in...)
	switch strategy {
	case core.FailoverRandom:
		rand.New(rand.NewSource(time.Now().UnixNano())).Shuffle(len(out), func(i, j int) {
			out[i], out[j] = out[j], out[i]
		})
	case core.FailoverLeastLoaded:
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Weight == out[j].Weight {
				return out[i].Priority < out[j].Priority
			}
			return out[i].Weight > out[j].Weight
		})
	default:
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Priority < out[j].Priority
		})
	}
	return out
}

func (p *Planner) applyAvailability(in []core.ActualModelRef) []core.ActualModelRef {
	out := make([]core.ActualModelRef, 0, len(in))
	for _, candidate := range in {
		provider, ok := p.resolver.ProviderByID(candidate.ProviderID)
		if !ok || !provider.Enabled {
			continue
		}
		if p.state.InCooldown(provider.ID, provider.CredentialRef, candidate.Key()) {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func (p *Planner) applyCongestion(vm core.VirtualModel, in []core.ActualModelRef) []core.ActualModelRef {
	if len(in) < 2 {
		return in
	}
	first := in[0]
	if p.state.ActiveRequests(first.Key()) <= vm.Congestion.Threshold {
		return in
	}
	out := append([]core.ActualModelRef(nil), in...)
	switch vm.Congestion.Strategy {
	case core.CongestionRandom:
		rand.New(rand.NewSource(time.Now().UnixNano())).Shuffle(len(out), func(i, j int) {
			out[i], out[j] = out[j], out[i]
		})
	case core.CongestionRoundRobin:
		out = append(out[1:], out[0])
	default:
		sort.SliceStable(out, func(i, j int) bool {
			return p.state.ActiveRequests(out[i].Key()) < p.state.ActiveRequests(out[j].Key())
		})
	}
	return out
}

func ReplaceModel(body map[string]any, candidate core.ActualModelRef) {
	body["model"] = candidate.ModelID
}

func DescribeCandidates(candidates []core.ActualModelRef) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, fmt.Sprintf("%s/%s", candidate.ProviderID, candidate.ModelID))
	}
	return out
}
