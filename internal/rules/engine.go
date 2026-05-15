package rules

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"mfp/internal/core"
)

type Decision struct {
	Rule            core.ErrorRule    `json:"rule"`
	Action          core.RuleAction   `json:"action"`
	CooldownSeconds int               `json:"cooldown_seconds"`
	HealthImpact    core.HealthImpact `json:"health_impact"`
}

type Engine struct {
	rules         []core.ErrorRule
	defaultAction core.RuleAction
}

func New(ruleSet []core.ErrorRule, defaultAction core.RuleAction) *Engine {
	copied := append([]core.ErrorRule(nil), ruleSet...)
	sort.SliceStable(copied, func(i, j int) bool {
		return copied[i].Priority < copied[j].Priority
	})
	if defaultAction == "" {
		defaultAction = core.RuleActionFailover
	}
	return &Engine{rules: copied, defaultAction: defaultAction}
}

func (e *Engine) Rules() []core.ErrorRule {
	return append([]core.ErrorRule(nil), e.rules...)
}

func (e *Engine) DefaultAction() core.RuleAction {
	return e.defaultAction
}

func (e *Engine) DefaultDecision() Decision {
	return Decision{Action: e.defaultAction, CooldownSeconds: 0, HealthImpact: core.HealthImpactNone}
}

func Normalize(statusCode int, body []byte, err error) core.NormalizedError {
	if err != nil {
		msg := err.Error()
		category := "network_error"
		if strings.Contains(strings.ToLower(msg), "timeout") || strings.Contains(strings.ToLower(msg), "deadline") {
			category = "timeout"
		}
		return core.NormalizedError{
			StatusCode: 0,
			Body:       msg,
			Category:   category,
			Message:    msg,
			Retryable:  true,
		}
	}

	normalized := core.NormalizedError{
		StatusCode: statusCode,
		Body:       string(body),
		Message:    httpMessage(statusCode),
		Retryable:  statusCode >= 500 || statusCode == 429,
	}

	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &payload)
	}
	if payload.Error.Code != "" {
		normalized.ErrorCode = payload.Error.Code
	}
	if payload.Error.Message != "" {
		normalized.Message = payload.Error.Message
	}

	content := strings.ToLower(normalized.Body + " " + normalized.ErrorCode + " " + normalized.Message)
	switch {
	case statusCode == 401 || statusCode == 403 || strings.Contains(content, "invalid_api_key"):
		normalized.Category = "auth_failed"
	case statusCode == 402 || strings.Contains(content, "insufficient_quota"):
		normalized.Category = "quota_exhausted"
	case statusCode == 429 || strings.Contains(content, "rate_limit"):
		normalized.Category = "rate_limit"
	case statusCode == 404 || strings.Contains(content, "model_not_found"):
		normalized.Category = "model_not_found"
	case statusCode == 400 && strings.Contains(content, "context_length_exceeded"):
		normalized.Category = "context_too_long"
	case statusCode == 400 && strings.Contains(content, "content_policy_violation"):
		normalized.Category = "content_filter"
	case statusCode == 400:
		normalized.Category = "bad_request"
	case statusCode >= 500:
		normalized.Category = "upstream_error"
	default:
		normalized.Category = "unknown_error"
	}
	return normalized
}

func (e *Engine) Decide(n core.NormalizedError) Decision {
	for _, rule := range e.rules {
		if !rule.Enabled {
			continue
		}
		if matches(rule.Match, n) {
			return Decision{
				Rule:            rule,
				Action:          rule.Action,
				CooldownSeconds: rule.CooldownSeconds,
				HealthImpact:    rule.HealthImpact,
			}
		}
	}
	return Decision{}
}

func matches(match core.ErrorMatch, n core.NormalizedError) bool {
	if match.Category != "" && match.Category != n.Category {
		return false
	}
	if match.StatusCode != nil && *match.StatusCode != n.StatusCode {
		return false
	}
	if match.StatusCodeRange != nil {
		if n.StatusCode < match.StatusCodeRange[0] || n.StatusCode > match.StatusCodeRange[1] {
			return false
		}
	}
	if match.ErrorCode != "" && match.ErrorCode != n.ErrorCode {
		return false
	}
	body := n.Body
	if match.BodyContains != "" && !strings.Contains(body, match.BodyContains) {
		return false
	}
	if match.BodyRegex != "" {
		re, err := regexp.Compile(match.BodyRegex)
		if err != nil || !re.MatchString(body) {
			return false
		}
	}
	return true
}

func httpMessage(statusCode int) string {
	if statusCode == 0 {
		return "network error"
	}
	return fmt.Sprintf("upstream returned status %d", statusCode)
}
