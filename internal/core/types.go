package core

import "time"

type ProviderType string

const (
	ProviderTypeOpenAICompatible ProviderType = "openai_compatible"
)

type StickyScope string

const (
	StickyScopeGlobal  StickyScope = "global"
	StickyScopeAgent   StickyScope = "agent"
	StickyScopeSession StickyScope = "session"
	StickyScopeOff     StickyScope = "off"
)

type FailoverStrategy string

const (
	FailoverSequential  FailoverStrategy = "sequential"
	FailoverRandom      FailoverStrategy = "random"
	FailoverLeastLoaded FailoverStrategy = "least_loaded"
)

type CongestionStrategy string

const (
	CongestionRoundRobin  CongestionStrategy = "round_robin"
	CongestionRandom      CongestionStrategy = "random"
	CongestionLeastLoaded CongestionStrategy = "least_loaded"
)

type HealthStatus string

const (
	HealthHealthy     HealthStatus = "healthy"
	HealthDegraded    HealthStatus = "degraded"
	HealthCongested   HealthStatus = "congested"
	HealthUnhealthy   HealthStatus = "unhealthy"
	HealthCoolingDown HealthStatus = "cooling_down"
	HealthUnknown     HealthStatus = "unknown"
)

type RuleAction string

const (
	RuleActionFailover RuleAction = "failover"
	RuleActionReject   RuleAction = "reject"
	RuleActionRetry    RuleAction = "retry"
)

type HealthImpact string

const (
	HealthImpactNone       HealthImpact = "none"
	HealthImpactModel      HealthImpact = "model"
	HealthImpactProvider   HealthImpact = "provider"
	HealthImpactCredential HealthImpact = "credential"
)

type AppConfig struct {
	APIListenAddr     string           `json:"api_listen_addr"`
	AdminListenAddr   string           `json:"admin_listen_addr"`
	DataDir           string           `json:"data_dir"`
	Proxy             ProxyConfig      `json:"proxy"`
	Admin             AdminConfig      `json:"admin"`
	Providers         []ProviderConfig `json:"providers"`
	VirtualModels     []VirtualModel   `json:"virtual_models"`
	ErrorRules        []ErrorRule      `json:"error_rules"`
	DefaultRuleAction RuleAction       `json:"default_rule_action"`
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (c AppConfig) Clone() AppConfig {
	clone := c
	clone.Providers = append([]ProviderConfig(nil), c.Providers...)
	for i := range clone.Providers {
		clone.Providers[i].Models = append([]ProviderModel(nil), clone.Providers[i].Models...)
		if clone.Providers[i].HeadersTemplate != nil {
			clone.Providers[i].HeadersTemplate = cloneStringMap(clone.Providers[i].HeadersTemplate)
		}
	}
	clone.VirtualModels = append([]VirtualModel(nil), c.VirtualModels...)
	for i := range clone.VirtualModels {
		clone.VirtualModels[i].Candidates = append([]ActualModelRef(nil), clone.VirtualModels[i].Candidates...)
		for j := range clone.VirtualModels[i].Candidates {
			clone.VirtualModels[i].Candidates[j].Capabilities = append([]string(nil), clone.VirtualModels[i].Candidates[j].Capabilities...)
		}
	}
	clone.ErrorRules = append([]ErrorRule(nil), c.ErrorRules...)
	for i := range clone.ErrorRules {
		if clone.ErrorRules[i].Match.StatusCodeRange != nil {
			rangeValue := *clone.ErrorRules[i].Match.StatusCodeRange
			clone.ErrorRules[i].Match.StatusCodeRange = &rangeValue
		}
		if clone.ErrorRules[i].Match.StatusCode != nil {
			statusCode := *clone.ErrorRules[i].Match.StatusCode
			clone.ErrorRules[i].Match.StatusCode = &statusCode
		}
	}
	if c.Admin.Accounts != nil {
		clone.Admin.Accounts = append([]AdminAccountConfig(nil), c.Admin.Accounts...)
	}
	return clone
}

type ProxyConfig struct {
	RequestTimeoutMS         int  `json:"request_timeout_ms"`
	TrustAuthorizationHeader bool `json:"trust_authorization_header"`
}

type AdminConfig struct {
	SessionCookieName string               `json:"session_cookie_name"`
	SessionTTLMinutes int                  `json:"session_ttl_minutes"`
	Accounts          []AdminAccountConfig `json:"accounts"`
}

type AdminAccountConfig struct {
	Username     string `json:"username"`
	Role         string `json:"role"`
	PasswordHash string `json:"password_hash,omitempty"`
	Password     string `json:"password,omitempty"`
}

type ProviderConfig struct {
	ID              string            `json:"id"`
	Type            ProviderType      `json:"type"`
	BaseURL         string            `json:"base_url"`
	CredentialRef   string            `json:"credential_ref"`
	CredentialEnv   string            `json:"credential_env"`
	APIKey          string            `json:"api_key,omitempty"`
	HeadersTemplate map[string]string `json:"headers_template"`
	TimeoutMS       int               `json:"timeout_ms"`
	Enabled         bool              `json:"enabled"`
	Models          []ProviderModel   `json:"models"`
}

type ProviderModel struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Capabilities []string `json:"capabilities"`
}

type VirtualModel struct {
	ID                   string           `json:"id"`
	DisplayName          string           `json:"display_name"`
	Description          string           `json:"description"`
	APIKey               string           `json:"api_key,omitempty"`
	Candidates           []ActualModelRef `json:"candidates"`
	Sticky               bool             `json:"sticky"`
	StickyScope          StickyScope      `json:"sticky_scope"`
	StickyTimeoutMinutes int              `json:"sticky_timeout_minutes"`
	FailoverStrategy     FailoverStrategy `json:"failover_strategy"`
	MaxAttempts          int              `json:"max_attempts"`
	Congestion           CongestionConfig `json:"congestion"`
	CreatedAt            string           `json:"created_at"`
	UpdatedAt            string           `json:"updated_at"`
}

type ActualModelRef struct {
	ProviderID   string   `json:"provider_id"`
	ModelID      string   `json:"model_id"`
	Label        string   `json:"label"`
	Priority     int      `json:"priority"`
	Weight       int      `json:"weight"`
	MaxRetry     int      `json:"max_retry"`
	CostHint     string   `json:"cost_hint"`
	Capabilities []string `json:"capabilities"`
	Enabled      bool     `json:"enabled"`
}

func (a ActualModelRef) Key() string {
	return a.ProviderID + "/" + a.ModelID
}

type CongestionConfig struct {
	Enabled         bool               `json:"enabled"`
	WindowSeconds   int                `json:"window_seconds"`
	Threshold       int                `json:"threshold"`
	Strategy        CongestionStrategy `json:"strategy"`
	CooldownSeconds int                `json:"cooldown_seconds"`
}

type ErrorRule struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Match           ErrorMatch   `json:"match"`
	Action          RuleAction   `json:"action"`
	CooldownSeconds int          `json:"cooldown_seconds"`
	IsBuiltin       bool         `json:"is_builtin"`
	Enabled         bool         `json:"enabled"`
	Priority        int          `json:"priority"`
	HealthImpact    HealthImpact `json:"health_impact"`
}

type ErrorMatch struct {
	StatusCode      *int    `json:"status_code,omitempty"`
	StatusCodeRange *[2]int `json:"status_code_range,omitempty"`
	BodyContains    string  `json:"body_contains,omitempty"`
	BodyRegex       string  `json:"body_regex,omitempty"`
	ErrorCode       string  `json:"error_code,omitempty"`
	Category        string  `json:"category,omitempty"`
}

type StickyRecord struct {
	VirtualModel string    `json:"virtual_model"`
	ScopeKey     string    `json:"scope_key"`
	ActualModel  string    `json:"actual_model"`
	LastUsedAt   time.Time `json:"last_used_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type AttemptLog struct {
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
	ModelKey   string `json:"model_key"`
	StatusCode int    `json:"status_code"`
	ErrorType  string `json:"error_type,omitempty"`
	LatencyMS  int64  `json:"latency_ms"`
}

type RequestLog struct {
	ID            string       `json:"id"`
	Path          string       `json:"path"`
	VirtualModel  string       `json:"virtual_model"`
	ActualModel   string       `json:"actual_model"`
	ProviderID    string       `json:"provider_id"`
	ScopeKey      string       `json:"scope_key"`
	Status        string       `json:"status"`
	RouteReason   string       `json:"route_reason"`
	StickyHit     bool         `json:"sticky_hit"`
	FailoverCount int          `json:"failover_count"`
	ModelsTried   []string     `json:"models_tried"`
	Attempts      []AttemptLog `json:"attempts,omitempty"`
	ErrorType     string       `json:"error_type,omitempty"`
	LatencyMS     int64        `json:"latency_ms"`
	CreatedAt     time.Time    `json:"created_at"`
}

type AuditRecord struct {
	ID        string         `json:"id"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	Resource  string         `json:"resource"`
	CreatedAt time.Time      `json:"created_at"`
	Detail    map[string]any `json:"detail,omitempty"`
}

type RouteContext struct {
	VirtualModel string
	AgentID      string
	SessionID    string
}

type RoutePlan struct {
	ScopeKey   string
	StickyHit  bool
	Reason     string
	Candidates []ActualModelRef
}

type ModelHealth struct {
	ModelKey             string       `json:"model_key"`
	ProviderID           string       `json:"provider_id"`
	ModelID              string       `json:"model_id"`
	Status               HealthStatus `json:"status"`
	ConsecutiveFailures  int          `json:"consecutive_failures"`
	ConsecutiveSuccesses int          `json:"consecutive_successes"`
	LastFailureAt        *time.Time   `json:"last_failure_at,omitempty"`
	LastFailureReason    string       `json:"last_failure_reason,omitempty"`
	LastSuccessAt        *time.Time   `json:"last_success_at,omitempty"`
	SuccessRate24h       float64      `json:"success_rate_24h"`
	AvgLatencyMS         float64      `json:"avg_latency_ms"`
	P95LatencyMS         float64      `json:"p95_latency_ms"`
	ActiveRequests       int          `json:"active_requests"`
	MarkedUnhealthyAt    *time.Time   `json:"marked_unhealthy_at,omitempty"`
	CooldownUntil        *time.Time   `json:"cooldown_until,omitempty"`
}

type NormalizedError struct {
	StatusCode int    `json:"status_code"`
	ErrorCode  string `json:"error_code,omitempty"`
	Body       string `json:"body,omitempty"`
	Category   string `json:"category"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
}
