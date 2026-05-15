package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"mfp/internal/core"
)

type Manager struct {
	path string
}

func NewManager(path string) *Manager {
	return &Manager{path: path}
}

func (m *Manager) Path() string {
	return m.path
}

func (m *Manager) Load() (core.AppConfig, error) {
	raw, err := os.ReadFile(m.path)
	if err != nil {
		return core.AppConfig{}, err
	}

	var cfg core.AppConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return core.AppConfig{}, fmt.Errorf("parse config: %w", err)
	}
	applyDefaults(&cfg)
	if err := Validate(cfg); err != nil {
		return core.AppConfig{}, err
	}
	return cfg, nil
}

func (m *Manager) Save(cfg core.AppConfig) error {
	applyDefaults(&cfg)
	if err := Validate(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.path); err != nil {
		_ = os.Remove(tmp)
		return os.WriteFile(m.path, body, 0o644)
	}
	return nil
}

func Validate(cfg core.AppConfig) error {
	if cfg.APIListenAddr == "" || cfg.AdminListenAddr == "" {
		return errors.New("api_listen_addr and admin_listen_addr are required")
	}
	accounts := map[string]struct{}{}
	for _, account := range cfg.Admin.Accounts {
		if account.Username == "" {
			return errors.New("admin account username is required")
		}
		if _, exists := accounts[account.Username]; exists {
			return fmt.Errorf("duplicate admin account %s", account.Username)
		}
		accounts[account.Username] = struct{}{}
	}
	providers := map[string]core.ProviderConfig{}
	for _, provider := range cfg.Providers {
		if provider.ID == "" {
			return errors.New("provider.id is required")
		}
		if provider.BaseURL == "" {
			return fmt.Errorf("provider %s base_url is required", provider.ID)
		}
		if provider.Type == "" {
			return fmt.Errorf("provider %s type is required", provider.ID)
		}
		if _, exists := providers[provider.ID]; exists {
			return fmt.Errorf("duplicate provider %s", provider.ID)
		}
		providers[provider.ID] = provider
	}
	virtualModels := map[string]struct{}{}
	for _, vm := range cfg.VirtualModels {
		if vm.ID == "" {
			return errors.New("virtual_model.id is required")
		}
		if _, exists := virtualModels[vm.ID]; exists {
			return fmt.Errorf("duplicate virtual model %s", vm.ID)
		}
		virtualModels[vm.ID] = struct{}{}
		candidates := map[string]struct{}{}
		for _, candidate := range vm.Candidates {
			provider, ok := providers[candidate.ProviderID]
			if !ok {
				return fmt.Errorf("virtual model %s references unknown provider %s", vm.ID, candidate.ProviderID)
			}
			if !hasProviderModel(provider, candidate.ModelID) {
				return fmt.Errorf("virtual model %s references unknown model %s on provider %s", vm.ID, candidate.ModelID, candidate.ProviderID)
			}
			key := candidate.ProviderID + "/" + candidate.ModelID
			if _, exists := candidates[key]; exists {
				return fmt.Errorf("virtual model %s has duplicate candidate %s", vm.ID, key)
			}
			candidates[key] = struct{}{}
		}
	}
	return nil
}

func hasProviderModel(provider core.ProviderConfig, modelID string) bool {
	for _, model := range provider.Models {
		if model.ID == modelID {
			return true
		}
	}
	return false
}

func ResolveAdminSecret(cfg core.AppConfig) []byte {
	return []byte("mfp-local-admin-session")
}

func ResolveAccountPassword(account core.AdminAccountConfig) string {
	return account.Password
}

func ResolveCredential(provider core.ProviderConfig) string {
	if provider.APIKey != "" {
		return provider.APIKey
	}
	if provider.CredentialEnv == "" {
		return ""
	}
	return os.Getenv(provider.CredentialEnv)
}

func ExportSanitized(cfg core.AppConfig) core.AppConfig {
	exported := cfg
	for i := range exported.Providers {
		exported.Providers[i].CredentialEnv = ""
		exported.Providers[i].APIKey = ""
		exported.Providers[i].CredentialRef = redactCredentialRef(exported.Providers[i].CredentialRef)
	}
	for i := range exported.Admin.Accounts {
		exported.Admin.Accounts[i].PasswordHash = ""
		exported.Admin.Accounts[i].Password = ""
	}
	return exported
}

func redactCredentialRef(in string) string {
	if in == "" {
		return ""
	}
	if len(in) <= 4 {
		return "****"
	}
	return in[:2] + strings.Repeat("*", len(in)-4) + in[len(in)-2:]
}

func applyDefaults(cfg *core.AppConfig) {
	if cfg.APIListenAddr == "" {
		cfg.APIListenAddr = ":18320"
	}
	if cfg.AdminListenAddr == "" {
		cfg.AdminListenAddr = ":18321"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if cfg.Proxy.RequestTimeoutMS == 0 {
		cfg.Proxy.RequestTimeoutMS = 120000
	}
	if cfg.Admin.SessionCookieName == "" {
		cfg.Admin.SessionCookieName = "mfp_session"
	}
	if cfg.Admin.SessionTTLMinutes == 0 {
		cfg.Admin.SessionTTLMinutes = 120
	}
	for i := range cfg.Providers {
		if cfg.Providers[i].TimeoutMS == 0 {
			cfg.Providers[i].TimeoutMS = cfg.Proxy.RequestTimeoutMS
		}
		if cfg.Providers[i].HeadersTemplate == nil {
			cfg.Providers[i].HeadersTemplate = map[string]string{}
		}
	}
	for i := range cfg.VirtualModels {
		vm := &cfg.VirtualModels[i]
		if vm.StickyScope == "" {
			vm.StickyScope = core.StickyScopeAgent
		}
		if vm.StickyTimeoutMinutes == 0 {
			vm.StickyTimeoutMinutes = 30
		}
		if vm.FailoverStrategy == "" {
			vm.FailoverStrategy = core.FailoverSequential
		}
		if vm.MaxAttempts == 0 {
			vm.MaxAttempts = len(vm.Candidates)
		}
		if vm.Congestion.WindowSeconds == 0 {
			vm.Congestion.WindowSeconds = 10
		}
		if vm.Congestion.Threshold == 0 {
			vm.Congestion.Threshold = 5
		}
		if vm.Congestion.Strategy == "" {
			vm.Congestion.Strategy = core.CongestionLeastLoaded
		}
		if vm.Congestion.CooldownSeconds == 0 {
			vm.Congestion.CooldownSeconds = 30
		}
		if vm.CreatedAt == "" {
			vm.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		}
		vm.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		for idx := range vm.Candidates {
			candidate := &vm.Candidates[idx]
			if candidate.Priority == 0 {
				candidate.Priority = idx + 1
			}
			if candidate.Weight == 0 {
				candidate.Weight = 100 - idx
			}
			if candidate.MaxRetry == 0 {
				candidate.MaxRetry = 1
			}
		}
		slices.SortFunc(vm.Candidates, func(a, b core.ActualModelRef) int {
			return a.Priority - b.Priority
		})
	}
	if len(cfg.ErrorRules) == 0 {
		cfg.ErrorRules = DefaultRules()
	}
	if cfg.DefaultRuleAction == "" {
		cfg.DefaultRuleAction = core.RuleActionFailover
	}
}

func DefaultRules() []core.ErrorRule {
	status := func(v int) *int { return &v }
	return []core.ErrorRule{
		{
			ID:              "builtin-rate-limit",
			Name:            "Rate limit",
			Enabled:         true,
			IsBuiltin:       true,
			Priority:        10,
			Action:          core.RuleActionFailover,
			CooldownSeconds: 60,
			HealthImpact:    core.HealthImpactCredential,
			Match:           core.ErrorMatch{StatusCode: status(429)},
		},
		{
			ID:              "builtin-upstream",
			Name:            "Upstream error",
			Enabled:         true,
			IsBuiltin:       true,
			Priority:        20,
			Action:          core.RuleActionFailover,
			CooldownSeconds: 45,
			HealthImpact:    core.HealthImpactProvider,
			Match:           core.ErrorMatch{StatusCodeRange: &[2]int{500, 599}},
		},
		{
			ID:              "builtin-auth-failed",
			Name:            "Auth failed",
			Enabled:         true,
			IsBuiltin:       true,
			Priority:        30,
			Action:          core.RuleActionFailover,
			CooldownSeconds: 300,
			HealthImpact:    core.HealthImpactCredential,
			Match:           core.ErrorMatch{StatusCode: status(401)},
		},
		{
			ID:              "builtin-forbidden",
			Name:            "Forbidden",
			Enabled:         true,
			IsBuiltin:       true,
			Priority:        40,
			Action:          core.RuleActionFailover,
			CooldownSeconds: 300,
			HealthImpact:    core.HealthImpactCredential,
			Match:           core.ErrorMatch{StatusCode: status(403)},
		},
		{
			ID:              "builtin-quota",
			Name:            "Quota exhausted",
			Enabled:         true,
			IsBuiltin:       true,
			Priority:        50,
			Action:          core.RuleActionFailover,
			CooldownSeconds: 300,
			HealthImpact:    core.HealthImpactCredential,
			Match:           core.ErrorMatch{StatusCode: status(402)},
		},
		{
			ID:              "builtin-timeout",
			Name:            "Timeout",
			Enabled:         true,
			IsBuiltin:       true,
			Priority:        60,
			Action:          core.RuleActionFailover,
			CooldownSeconds: 45,
			HealthImpact:    core.HealthImpactModel,
			Match:           core.ErrorMatch{StatusCode: status(504)},
		},
		{
			ID:              "builtin-context",
			Name:            "Context too long",
			Enabled:         true,
			IsBuiltin:       true,
			Priority:        70,
			Action:          core.RuleActionReject,
			CooldownSeconds: 0,
			HealthImpact:    core.HealthImpactNone,
			Match:           core.ErrorMatch{StatusCode: status(400)},
		},
		{
			ID:              "builtin-model-not-found",
			Name:            "Model not found",
			Enabled:         true,
			IsBuiltin:       true,
			Priority:        80,
			Action:          core.RuleActionFailover,
			CooldownSeconds: 300,
			HealthImpact:    core.HealthImpactModel,
			Match:           core.ErrorMatch{StatusCode: status(404)},
		},
	}
}
