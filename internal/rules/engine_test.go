package rules

import (
	"testing"

	"mfp/internal/config"
	"mfp/internal/core"
)

func TestNormalizeRateLimit(t *testing.T) {
	err := Normalize(429, []byte(`{"error":{"code":"rate_limit_exceeded","message":"too many requests"}}`), nil)
	if err.Category != "rate_limit" {
		t.Fatalf("expected rate_limit, got %s", err.Category)
	}
}

func TestDecisionRejectsBadRequest(t *testing.T) {
	engine := New(config.DefaultRules(), core.RuleActionFailover)
	decision := engine.Decide(Normalize(429, []byte(`{"error":{"code":"rate_limit_exceeded","message":"too many requests"}}`), nil))
	if decision.Action != core.RuleActionFailover {
		t.Fatalf("expected failover, got %s", decision.Action)
	}
}

func TestDefaultDecisionUsesFallbackAction(t *testing.T) {
	engine := New(nil, core.RuleActionReject)
	decision := engine.Decide(Normalize(418, []byte(`{"error":{"message":"teapot"}}`), nil))
	if decision.Action != "" {
		t.Fatalf("expected empty decision for unmatched rule, got %s", decision.Action)
	}
	if fallback := engine.DefaultDecision(); fallback.Action != core.RuleActionReject {
		t.Fatalf("expected reject fallback, got %s", fallback.Action)
	}

}
