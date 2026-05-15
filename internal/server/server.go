package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"mfp/internal/auth"
	"mfp/internal/config"
	"mfp/internal/core"
	"mfp/internal/orchestrator"
	"mfp/internal/provider"
	"mfp/internal/rules"
	"mfp/internal/state"
)

type ConfigRuntime struct {
	mu  sync.RWMutex
	cfg core.AppConfig
}

func NewConfigRuntime(cfg core.AppConfig) *ConfigRuntime {
	return &ConfigRuntime{cfg: cfg.Clone()}
}

func (r *ConfigRuntime) Snapshot() core.AppConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.Clone()
}

func (r *ConfigRuntime) Replace(cfg core.AppConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg.Clone()
}

type Service struct {
	configPath      string
	configManager   *config.Manager
	configRuntime   *ConfigRuntime
	tokenManager    *auth.TokenManager
	sessionVersions map[string]int
	sessionMu       sync.RWMutex
	state           *state.Hub
	adapter         *provider.OpenAICompatible
	logger          *log.Logger
}

func New(cfgPath string, cfg core.AppConfig, hub *state.Hub, logger *log.Logger) *Service {
	return &Service{
		configPath:      cfgPath,
		configManager:   config.NewManager(cfgPath),
		configRuntime:   NewConfigRuntime(cfg),
		tokenManager:    auth.NewTokenManager(config.ResolveAdminSecret(cfg), time.Duration(cfg.Admin.SessionTTLMinutes)*time.Minute),
		sessionVersions: map[string]int{},
		state:           hub,
		adapter:         provider.NewOpenAICompatible(),
		logger:          logger,
	}
}

func (s *Service) APIServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.handleProxy)
	mux.HandleFunc("/v1/responses", s.handleProxy)
	return &http.Server{
		Addr:              s.configRuntime.Snapshot().APIListenAddr,
		Handler:           loggingMiddleware(s.logger, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
}

func (s *Service) AdminServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleAdminUI)
	mux.Handle("/web/", http.FileServer(http.FS(webFS)))
	mux.HandleFunc("/admin/v1/auth/login", s.handleLogin)
	mux.Handle("/admin/v1/auth/logout", s.requireAdmin(http.HandlerFunc(s.handleLogout)))
	mux.Handle("/admin/v1/virtual-models", s.requireAdmin(http.HandlerFunc(s.handleVirtualModels)))
	mux.Handle("/admin/v1/virtual-models/", s.requireAdmin(http.HandlerFunc(s.handleVirtualModelByID)))
	mux.Handle("/admin/v1/providers", s.requireAdmin(http.HandlerFunc(s.handleProviders)))
	mux.Handle("/admin/v1/providers/discover-models", s.requireAdmin(http.HandlerFunc(s.handleProviderModelDiscovery)))
	mux.Handle("/admin/v1/providers/", s.requireAdmin(http.HandlerFunc(s.handleProviderByID)))
	mux.Handle("/admin/v1/models/health", s.requireAdmin(http.HandlerFunc(s.handleModelsHealth)))
	mux.Handle("/admin/v1/models/", s.requireAdmin(http.HandlerFunc(s.handleModelAction)))
	mux.Handle("/admin/v1/models/catalog", s.requireAdmin(http.HandlerFunc(s.handleModelsCatalog)))
	mux.Handle("/admin/v1/models/sync", s.requireAdmin(http.HandlerFunc(s.handleModelsSync)))
	mux.Handle("/admin/v1/rules", s.requireAdmin(http.HandlerFunc(s.handleRules)))
	mux.Handle("/admin/v1/settings", s.requireAdmin(http.HandlerFunc(s.handleSettings)))
	mux.Handle("/admin/v1/sticky", s.requireAdmin(http.HandlerFunc(s.handleSticky)))
	mux.Handle("/admin/v1/sticky/", s.requireAdmin(http.HandlerFunc(s.handleStickyDelete)))
	mux.Handle("/admin/v1/config/export", s.requireAdmin(http.HandlerFunc(s.handleConfigExport)))
	mux.Handle("/admin/v1/config/reload", s.requireAdmin(http.HandlerFunc(s.handleConfigReload)))
	mux.Handle("/admin/v1/metrics/summary", s.requireAdmin(http.HandlerFunc(s.handleMetricsSummary)))
	mux.Handle("/admin/v1/diagnostics", s.requireAdmin(http.HandlerFunc(s.handleDiagnostics)))
	mux.Handle("/admin/v1/test/virtual-model", s.requireAdmin(http.HandlerFunc(s.handleTestVirtualModel)))
	mux.Handle("/admin/v1/test/provider-model", s.requireAdmin(http.HandlerFunc(s.handleTestProviderModel)))
	mux.Handle("/admin/v1/stats", s.requireAdmin(http.HandlerFunc(s.handleStats)))
	mux.Handle("/admin/v1/stats/live", s.requireAdmin(http.HandlerFunc(s.handleLiveStats)))
	mux.Handle("/admin/v1/audit/logs", s.requireAdmin(http.HandlerFunc(s.handleAuditLogs)))
	return &http.Server{
		Addr:              s.configRuntime.Snapshot().AdminListenAddr,
		Handler:           loggingMiddleware(s.logger, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
}

func (s *Service) handleProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is supported")
		return
	}
	cfg := s.configRuntime.Snapshot()
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "failed to read request body")
		return
	}
	defer r.Body.Close()

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "request body must be valid JSON")
		return
	}
	modelValue, _ := payload["model"].(string)
	virtualModelID := normalizeVirtualModelID(modelValue)
	vm, ok := findVirtualModel(cfg.VirtualModels, virtualModelID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "model_not_found", "virtual model not found")
		return
	}
	if !virtualModelAPIKeyMatches(r, vm) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid virtual model api key")
		return
	}

	route := core.RouteContext{
		VirtualModel: vm.ID,
		AgentID:      r.Header.Get("X-MFP-Agent-Id"),
		SessionID:    r.Header.Get("X-MFP-Session-Id"),
		Debug:        strings.EqualFold(r.Header.Get("X-MFP-Debug"), "true"),
	}
	planner := orchestrator.New(s, s.state)
	plan, err := planner.Build(vm, route)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "no_available_model", err.Error())
		return
	}

	ruleEngine := rules.New(cfg.ErrorRules, cfg.DefaultRuleAction)
	start := time.Now()
	modelsTried := make([]string, 0, len(plan.Candidates))
	attempts := make([]core.AttemptLog, 0, len(plan.Candidates))
	failoverCount := 0
	var lastNormalized core.NormalizedError

	for _, candidate := range plan.Candidates {
		providerCfg, _ := s.ProviderByID(candidate.ProviderID)
		for attempt := 1; attempt <= max(1, candidate.MaxRetry); attempt++ {
			modelsTried = append(modelsTried, candidate.Key())
			s.state.IncrementActive(candidate.Key(), candidate.ProviderID, candidate.ModelID)
			attemptStart := time.Now()

			attemptPayload := cloneMap(payload)
			orchestrator.ReplaceModel(attemptPayload, candidate)
			bodyBytes, _ := json.Marshal(attemptPayload)
			response, err := s.adapter.Do(r.Context(), provider.AttemptRequest{
				Provider:  providerCfg,
				Candidate: candidate,
				Path:      r.URL.Path,
				Body:      bodyBytes,
				Headers:   stripHopHeaders(r.Header, cfg.Proxy.TrustAuthorizationHeader),
			})
			s.state.DecrementActive(candidate.Key())
			attemptLatency := time.Since(attemptStart)
			attemptLog := core.AttemptLog{
				ProviderID: candidate.ProviderID,
				ModelID:    candidate.ModelID,
				ModelKey:   candidate.Key(),
				StatusCode: response.StatusCode,
				LatencyMS:  attemptLatency.Milliseconds(),
			}
			if err != nil {
				attemptLog.ErrorType = rules.Normalize(response.StatusCode, response.Body, err).Category
			}
			attempts = append(attempts, attemptLog)

			if err == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
				defer response.Stream.Close()
				s.state.RecordSuccess(candidate.Key(), candidate.ProviderID, candidate.ModelID, attemptLatency)
				if vm.Sticky {
					s.state.SetSticky(plan.ScopeKey, vm.ID, candidate, time.Duration(vm.StickyTimeoutMinutes)*time.Minute)
				}
				requestStatus := "success"
				if failoverCount > 0 {
					requestStatus = "failover"
				}
				_ = s.state.AddRequestLog(core.RequestLog{
					ID:            newID(),
					Path:          r.URL.Path,
					VirtualModel:  vm.ID,
					ActualModel:   candidate.ModelID,
					ProviderID:    candidate.ProviderID,
					ScopeKey:      plan.ScopeKey,
					Status:        requestStatus,
					RouteReason:   plan.Reason,
					StickyHit:     plan.StickyHit,
					FailoverCount: failoverCount,
					ModelsTried:   modelsTried,
					Attempts:      attempts,
					LatencyMS:     time.Since(start).Milliseconds(),
					CreatedAt:     time.Now().UTC(),
				})
				copyHeaders(w.Header(), response.Header)
				addDebugHeaders(w.Header(), cfg, vm.ID, candidate, failoverCount, plan.StickyHit, plan.Reason)
				w.WriteHeader(response.StatusCode)
				_, _ = io.Copy(w, response.Stream)
				return
			}

			normalized := rules.Normalize(response.StatusCode, response.Body, err)
			attempts[len(attempts)-1].ErrorType = normalized.Category
			lastNormalized = normalized
			decision := ruleEngine.Decide(normalized)
			if decision.Action == "" {
				decision = ruleEngine.DefaultDecision()
			}
			cooldown := time.Duration(decision.CooldownSeconds) * time.Second
			s.state.RecordFailure(candidate.Key(), candidate.ProviderID, candidate.ModelID, normalized.ErrorCode, normalized.Category, cooldown)
			applyCooldownImpact(s.state, providerCfg, candidate, decision, cooldown)
			if decision.Action == core.RuleActionRetry && attempt < max(1, candidate.MaxRetry) {
				continue
			}
			failoverCount++
			if decision.Action == core.RuleActionReject {
				goto done
			}
			break
		}
	}

done:
	_ = s.state.AddRequestLog(core.RequestLog{
		ID:            newID(),
		Path:          r.URL.Path,
		VirtualModel:  vm.ID,
		ProviderID:    "",
		ScopeKey:      plan.ScopeKey,
		Status:        "all_failed",
		RouteReason:   plan.Reason,
		StickyHit:     plan.StickyHit,
		FailoverCount: failoverCount,
		ModelsTried:   modelsTried,
		Attempts:      attempts,
		ErrorType:     lastNormalized.Category,
		LatencyMS:     time.Since(start).Milliseconds(),
		CreatedAt:     time.Now().UTC(),
	})
	writeNormalizedError(w, lastNormalized)
}

func applyCooldownImpact(hub *state.Hub, providerCfg core.ProviderConfig, candidate core.ActualModelRef, decision rules.Decision, cooldown time.Duration) {
	if cooldown <= 0 {
		return
	}
	until := time.Now().UTC().Add(cooldown)
	switch decision.HealthImpact {
	case core.HealthImpactProvider:
		hub.SetProviderCooldown(providerCfg.ID, until)
	case core.HealthImpactCredential:
		hub.SetCredentialCooldown(providerCfg.CredentialRef, until)
		hub.SetProviderCooldown(providerCfg.ID, until)
	}
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is supported")
		return
	}
	var payload struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
		return
	}
	cfg := s.configRuntime.Snapshot()
	for _, account := range cfg.Admin.Accounts {
		if account.Username == payload.Username && accountPasswordMatches(account, payload.Password) {
			sessionVersion := s.sessionVersion(account.Username)
			token, err := s.tokenManager.Issue(account.Username, account.Role, sessionVersion)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, "token_error", err.Error())
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     cfg.Admin.SessionCookieName,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				Expires:  time.Now().UTC().Add(time.Duration(cfg.Admin.SessionTTLMinutes) * time.Minute),
			})
			_ = s.state.AddAudit(core.AuditRecord{
				ID:        newID(),
				Actor:     account.Username,
				Action:    "login",
				Resource:  "admin_session",
				CreatedAt: time.Now().UTC(),
			})
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "role": account.Role, "default_password": payload.Password == "change-me"})
			return
		}
	}
	writeJSONError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is incorrect")
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is supported")
		return
	}
	cfg := s.configRuntime.Snapshot()
	if claims, ok := auth.ClaimsFromContext(r.Context()); ok {
		s.invalidateSession(claims.Username)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.Admin.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Service) handleAdminUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeJSONError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	content, err := fs.ReadFile(webFS, "web/index.html")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "asset_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(content)
}

func (s *Service) handleVirtualModels(w http.ResponseWriter, r *http.Request) {
	cfg := s.configRuntime.Snapshot()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, cfg.VirtualModels)
	case http.MethodPost:
		var vm core.VirtualModel
		if err := json.NewDecoder(r.Body).Decode(&vm); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
			return
		}
		cfg.VirtualModels = append(cfg.VirtualModels, vm)
		if err := s.persistConfig(r.Context(), "create_virtual_model", "virtual_models", cfg); err != nil {
			writeJSONError(w, http.StatusBadRequest, "config_error", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, vm)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
	}
}

func (s *Service) handleVirtualModelByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/admin/v1/virtual-models/")
	cfg := s.configRuntime.Snapshot()
	index := -1
	for i, vm := range cfg.VirtualModels {
		if vm.ID == id {
			index = i
			break
		}
	}
	if index == -1 {
		writeJSONError(w, http.StatusNotFound, "not_found", "virtual model not found")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var vm core.VirtualModel
		if err := json.NewDecoder(r.Body).Decode(&vm); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
			return
		}
		vm.ID = id
		cfg.VirtualModels[index] = vm
		if err := s.persistConfig(r.Context(), "update_virtual_model", id, cfg); err != nil {
			writeJSONError(w, http.StatusBadRequest, "config_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, vm)
	case http.MethodDelete:
		cfg.VirtualModels = append(cfg.VirtualModels[:index], cfg.VirtualModels[index+1:]...)
		if err := s.persistConfig(r.Context(), "delete_virtual_model", id, cfg); err != nil {
			writeJSONError(w, http.StatusBadRequest, "config_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
	}
}

func (s *Service) handleProviders(w http.ResponseWriter, r *http.Request) {
	cfg := s.configRuntime.Snapshot()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, cfg.Providers)
	case http.MethodPost:
		var providerCfg core.ProviderConfig
		if err := json.NewDecoder(r.Body).Decode(&providerCfg); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
			return
		}
		cfg.Providers = append(cfg.Providers, providerCfg)
		if err := s.persistConfig(r.Context(), "create_provider", "providers", cfg); err != nil {
			writeJSONError(w, http.StatusBadRequest, "config_error", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, providerCfg)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
	}
}

func (s *Service) handleProviderByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/admin/v1/providers/")
	if strings.HasSuffix(id, "/sync") {
		s.handleProviderSyncByID(w, r)
		return
	}
	cfg := s.configRuntime.Snapshot()
	index := -1
	for i, providerCfg := range cfg.Providers {
		if providerCfg.ID == id {
			index = i
			break
		}
	}
	if index == -1 {
		writeJSONError(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var providerCfg core.ProviderConfig
		if err := json.NewDecoder(r.Body).Decode(&providerCfg); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
			return
		}
		providerCfg.ID = id
		cfg.Providers[index] = providerCfg
		pruneMissingProviderModelCandidates(&cfg, id, providerCfg.Models)
		if err := s.persistConfig(r.Context(), "update_provider", id, cfg); err != nil {
			writeJSONError(w, http.StatusBadRequest, "config_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, providerCfg)
	case http.MethodDelete:
		cfg.Providers = append(cfg.Providers[:index], cfg.Providers[index+1:]...)
		pruneProviderCandidates(&cfg, id)
		if err := s.persistConfig(r.Context(), "delete_provider", id, cfg); err != nil {
			writeJSONError(w, http.StatusBadRequest, "config_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
	}
}

func pruneProviderCandidates(cfg *core.AppConfig, providerID string) {
	for vmIndex := range cfg.VirtualModels {
		candidates := cfg.VirtualModels[vmIndex].Candidates[:0]
		for _, candidate := range cfg.VirtualModels[vmIndex].Candidates {
			if candidate.ProviderID != providerID {
				candidates = append(candidates, candidate)
			}
		}
		cfg.VirtualModels[vmIndex].Candidates = candidates
	}
}

func pruneMissingProviderModelCandidates(cfg *core.AppConfig, providerID string, models []core.ProviderModel) {
	modelIDs := make(map[string]struct{}, len(models))
	for _, model := range models {
		modelIDs[model.ID] = struct{}{}
	}
	for vmIndex := range cfg.VirtualModels {
		candidates := cfg.VirtualModels[vmIndex].Candidates[:0]
		for _, candidate := range cfg.VirtualModels[vmIndex].Candidates {
			if candidate.ProviderID != providerID {
				candidates = append(candidates, candidate)
				continue
			}
			if _, ok := modelIDs[candidate.ModelID]; ok {
				candidates = append(candidates, candidate)
			}
		}
		cfg.VirtualModels[vmIndex].Candidates = candidates
	}
}

func (s *Service) handleModelsHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.configuredModelHealth())
}

func (s *Service) handleModelAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/recover") {
		writeJSONError(w, http.StatusNotFound, "not_found", "unknown model action")
		return
	}
	modelKey := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/admin/v1/models/"), "/recover")
	modelKey, _ = url.PathUnescape(modelKey)
	s.state.RecoverModel(modelKey)
	_ = s.state.AddAudit(core.AuditRecord{
		ID:        newID(),
		Actor:     actorFromContext(r.Context()),
		Action:    "recover_model",
		Resource:  modelKey,
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"recovered": modelKey})
}

func (s *Service) handleModelsCatalog(w http.ResponseWriter, r *http.Request) {
	cfg := s.configRuntime.Snapshot()
	type item struct {
		ProviderID   string   `json:"provider_id"`
		ModelID      string   `json:"model_id"`
		Capabilities []string `json:"capabilities"`
	}
	out := []item{}
	for _, providerCfg := range cfg.Providers {
		for _, model := range providerCfg.Models {
			out = append(out, item{
				ProviderID:   providerCfg.ID,
				ModelID:      model.ID,
				Capabilities: model.Capabilities,
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) handleModelsSync(w http.ResponseWriter, r *http.Request) {
	cfg := s.configRuntime.Snapshot()
	type syncResult struct {
		ProviderID string   `json:"provider_id"`
		Models     []string `json:"models"`
		Error      string   `json:"error,omitempty"`
	}
	results := []syncResult{}
	for i, providerCfg := range cfg.Providers {
		models, err := s.fetchProviderModels(r.Context(), providerCfg)
		if err != nil {
			results = append(results, syncResult{ProviderID: providerCfg.ID, Error: err.Error()})
			continue
		}
		cfg.Providers[i].Models = models
		ids := make([]string, 0, len(models))
		for _, model := range models {
			ids = append(ids, model.ID)
		}
		results = append(results, syncResult{ProviderID: providerCfg.ID, Models: ids})
	}
	if err := s.persistConfig(r.Context(), "sync_models", "providers", cfg); err != nil {
		writeJSONError(w, http.StatusBadRequest, "config_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Service) handleProviderModelDiscovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
		return
	}
	var providerCfg core.ProviderConfig
	if err := json.NewDecoder(r.Body).Decode(&providerCfg); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
		return
	}
	models, err := s.fetchProviderModels(r.Context(), providerCfg)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "sync_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Service) handleProviderSyncByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/admin/v1/providers/"), "/sync")
	cfg := s.configRuntime.Snapshot()
	index := -1
	for i, providerCfg := range cfg.Providers {
		if providerCfg.ID == id {
			index = i
			break
		}
	}
	if index == -1 {
		writeJSONError(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	models, err := s.fetchProviderModels(r.Context(), cfg.Providers[index])
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "sync_failed", err.Error())
		return
	}
	cfg.Providers[index].Models = models
	if err := s.persistAndRefresh(r.Context(), "sync_provider_models", id, cfg); err != nil {
		writeJSONError(w, http.StatusBadRequest, "config_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg.Providers[index])
}

func (s *Service) fetchProviderModels(ctx context.Context, providerCfg core.ProviderConfig) ([]core.ProviderModel, error) {
	target, err := providerModelsURL(providerCfg.BaseURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if credential := config.ResolveCredential(providerCfg); credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := (&http.Client{Timeout: time.Duration(providerCfg.TimeoutMS) * time.Millisecond}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("provider %s returned %d: %s", providerCfg.ID, resp.StatusCode, string(body))
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	models := make([]core.ProviderModel, 0, len(payload.Data))
	for _, item := range payload.Data {
		models = append(models, core.ProviderModel{ID: item.ID})
	}
	return models, nil
}

func providerModelsURL(base string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", err
	}
	joined := path.Join(parsed.Path, "/models")
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	parsed.Path = joined
	parsed.RawPath = joined
	return parsed.String(), nil
}

func (s *Service) handleRules(w http.ResponseWriter, r *http.Request) {
	cfg := s.configRuntime.Snapshot()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, cfg.ErrorRules)
	case http.MethodPut:
		var payload []core.ErrorRule
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
			return
		}
		cfg.ErrorRules = payload
		if err := s.persistConfig(r.Context(), "update_rules", "error_rules", cfg); err != nil {
			writeJSONError(w, http.StatusBadRequest, "config_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, payload)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
	}
}

func (s *Service) handleSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.configRuntime.Snapshot()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, sanitizeSettings(cfg))
	case http.MethodPut:
		var payload settingsPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
			return
		}
		oldAPIAddr := cfg.APIListenAddr
		oldAdminAddr := cfg.AdminListenAddr
		cfg.APIListenAddr = payload.APIListenAddr
		cfg.AdminListenAddr = payload.AdminListenAddr
		cfg.Proxy = payload.Proxy
		accounts, err := mergeAdminAccounts(cfg.Admin.Accounts, payload.Admin.Accounts)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "password_error", err.Error())
			return
		}
		cfg.Admin.SessionCookieName = payload.Admin.SessionCookieName
		cfg.Admin.SessionTTLMinutes = payload.Admin.SessionTTLMinutes
		cfg.Admin.Accounts = accounts
		cfg.DefaultRuleAction = payload.DefaultRuleAction
		if err := s.persistAndRefresh(r.Context(), "update_settings", "settings", cfg); err != nil {
			writeJSONError(w, http.StatusBadRequest, "config_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"saved":            true,
			"restart_required": oldAPIAddr != cfg.APIListenAddr || oldAdminAddr != cfg.AdminListenAddr,
			"settings":         sanitizeSettings(cfg),
		})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
	}
}

func (s *Service) handleSticky(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.state.StickyRecords())
}

func (s *Service) handleStickyDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
		return
	}
	scopeKey := strings.TrimPrefix(r.URL.Path, "/admin/v1/sticky/")
	scopeKey, _ = url.PathUnescape(scopeKey)
	s.state.DeleteSticky(scopeKey)
	_ = s.state.AddAudit(core.AuditRecord{
		ID:        newID(),
		Actor:     actorFromContext(r.Context()),
		Action:    "delete_sticky",
		Resource:  scopeKey,
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": scopeKey})
}

func (s *Service) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, config.ExportSanitized(s.configRuntime.Snapshot()))
}

func (s *Service) handleConfigReload(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.configManager.Load()
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "config_error", err.Error())
		return
	}
	s.configRuntime.Replace(cfg)
	s.tokenManager = auth.NewTokenManager(config.ResolveAdminSecret(cfg), time.Duration(cfg.Admin.SessionTTLMinutes)*time.Minute)
	_ = s.state.AddAudit(core.AuditRecord{
		ID:        newID(),
		Actor:     actorFromContext(r.Context()),
		Action:    "reload_config",
		Resource:  s.configPath,
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"reloaded": true})
}

func (s *Service) handleMetricsSummary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.state.MetricsSummary())
}

func (s *Service) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	cfg := s.configRuntime.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"summary":         s.state.MetricsSummary(),
		"health":          s.configuredModelHealth(),
		"sticky":          s.state.StickyRecords(),
		"recent_requests": s.state.RequestLogs(),
		"providers":       cfg.Providers,
		"virtual_models":  cfg.VirtualModels,
	})
}

func (s *Service) handleTestVirtualModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
		return
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
		return
	}
	if payload.Model == "" {
		payload.Model = "smart"
	}
	testBody := map[string]any{"model": payload.Model, "messages": []map[string]string{{"role": "user", "content": "MFP health test"}}}
	writeJSON(w, http.StatusOK, s.runProxyTest(r.Context(), "/v1/chat/completions", testBody))
}

func (s *Service) handleTestProviderModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
		return
	}
	var payload struct {
		ProviderID string `json:"provider_id"`
		ModelID    string `json:"model_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
		return
	}
	providerCfg, ok := s.ProviderByID(payload.ProviderID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	body, _ := json.Marshal(map[string]any{"model": payload.ModelID, "messages": []map[string]string{{"role": "user", "content": "MFP provider test"}}})
	started := time.Now()
	resp, err := s.adapter.Do(r.Context(), provider.AttemptRequest{
		Provider:  providerCfg,
		Candidate: core.ActualModelRef{ProviderID: payload.ProviderID, ModelID: payload.ModelID},
		Path:      "/v1/chat/completions",
		Body:      body,
		Headers:   http.Header{},
	})
	result := map[string]any{"ok": err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300, "status_code": resp.StatusCode, "latency_ms": time.Since(started).Milliseconds()}
	if err != nil {
		result["error"] = err.Error()
	} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_ = resp.Stream.Close()
	} else {
		result["error"] = rules.Normalize(resp.StatusCode, resp.Body, nil).Message
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) runProxyTest(ctx context.Context, proxyPath string, payload map[string]any) map[string]any {
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://mfp-test"+proxyPath, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleProxy(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	return map[string]any{
		"ok":             res.StatusCode >= 200 && res.StatusCode < 300,
		"status_code":    res.StatusCode,
		"provider":       res.Header.Get("X-MFP-Provider"),
		"actual_model":   res.Header.Get("X-MFP-Actual-Model"),
		"failover_count": res.Header.Get("X-MFP-Failover-Count"),
	}
}

func (s *Service) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.state.RequestLogs())
}

func (s *Service) handleLiveStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "sse_unsupported", "streaming unsupported")
		return
	}
	events := s.state.Subscribe(r.Context())
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			body, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", body)
			flusher.Flush()
		}
	}
}

func (s *Service) handleAuditLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.state.AuditLogs())
}

func (s *Service) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.configRuntime.Snapshot()
		token := bearerToken(r.Header.Get("Authorization"))
		if token == "" {
			if cookie, err := r.Cookie(cfg.Admin.SessionCookieName); err == nil {
				token = cookie.Value
			}
		}
		if token == "" {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing admin session")
			return
		}
		claims, err := s.tokenManager.Parse(token)
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", err.Error())
			return
		}
		if claims.SessionVersion != s.sessionVersion(claims.Username) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "admin session expired")
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithClaims(r.Context(), claims)))
	})
}

func (s *Service) sessionVersion(username string) int {
	s.sessionMu.RLock()
	defer s.sessionMu.RUnlock()
	return s.sessionVersions[username]
}

func (s *Service) invalidateSession(username string) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	s.sessionVersions[username]++
}

func (s *Service) persistConfig(ctx context.Context, action, resource string, cfg core.AppConfig) error {
	if err := s.configManager.Save(cfg); err != nil {
		return err
	}
	s.configRuntime.Replace(cfg)
	return s.state.AddAudit(core.AuditRecord{
		ID:        newID(),
		Actor:     actorFromContext(ctx),
		Action:    action,
		Resource:  resource,
		CreatedAt: time.Now().UTC(),
	})
}

func (s *Service) persistAndRefresh(ctx context.Context, action, resource string, cfg core.AppConfig) error {
	if err := s.persistConfig(ctx, action, resource, cfg); err != nil {
		return err
	}
	s.tokenManager = auth.NewTokenManager(config.ResolveAdminSecret(cfg), time.Duration(cfg.Admin.SessionTTLMinutes)*time.Minute)
	return nil
}

func (s *Service) ProviderByID(id string) (core.ProviderConfig, bool) {
	cfg := s.configRuntime.Snapshot()
	for _, providerCfg := range cfg.Providers {
		if providerCfg.ID == id {
			return providerCfg, true
		}
	}
	return core.ProviderConfig{}, false
}

func findVirtualModel(models []core.VirtualModel, id string) (core.VirtualModel, bool) {
	for _, model := range models {
		if model.ID == id {
			return model, true
		}
	}
	return core.VirtualModel{}, false
}

func normalizeVirtualModelID(model string) string {
	model = strings.TrimSpace(model)
	if strings.Contains(model, "/") {
		return path.Base(model)
	}
	return model
}

func stripHopHeaders(in http.Header, trustAuthorization bool) http.Header {
	out := http.Header{}
	for key, values := range in {
		lower := strings.ToLower(key)
		switch lower {
		case "host", "content-length", "connection", "cookie":
			continue
		case "authorization":
			if !trustAuthorization {
				continue
			}
		}
		for _, value := range values {
			out.Add(key, value)
		}
	}
	return out
}

func addDebugHeaders(header http.Header, cfg core.AppConfig, virtualModel string, candidate core.ActualModelRef, failoverCount int, stickyHit bool, reason string) {
	if !cfg.Proxy.DebugHeaders {
		return
	}
	header.Set("X-MFP-Virtual-Model", virtualModel)
	header.Set("X-MFP-Actual-Model", candidate.ModelID)
	header.Set("X-MFP-Provider", candidate.ProviderID)
	header.Set("X-MFP-Failover-Count", fmt.Sprintf("%d", failoverCount))
	header.Set("X-MFP-Sticky-Hit", fmt.Sprintf("%t", stickyHit))
	header.Set("X-MFP-Route-Reason", reason)
}

func writeNormalizedError(w http.ResponseWriter, n core.NormalizedError) {
	status := n.StatusCode
	if status == 0 {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    n.ErrorCode,
			"type":    n.Category,
			"message": n.Message,
		},
	})
}

func writeJSONError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"type":    code,
			"message": message,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func loggingMiddleware(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func virtualModelAPIKeyMatches(r *http.Request, vm core.VirtualModel) bool {
	if vm.APIKey == "" {
		return true
	}
	key := bearerToken(r.Header.Get("Authorization"))
	if key == "" {
		key = strings.TrimSpace(r.Header.Get("X-MFP-API-Key"))
	}
	return key == vm.APIKey
}

func accountPasswordMatches(account core.AdminAccountConfig, password string) bool {
	if account.PasswordHash != "" {
		return auth.CheckPasswordHash(account.PasswordHash, password)
	}
	return config.ResolveAccountPassword(account) == password
}

func bearerToken(header string) string {
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

func actorFromContext(ctx context.Context) string {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return "system"
	}
	return claims.Username
}

func cloneMap(in map[string]any) map[string]any {
	body, _ := json.Marshal(in)
	out := map[string]any{}
	_ = json.Unmarshal(body, &out)
	return out
}

type settingsPayload struct {
	APIListenAddr     string           `json:"api_listen_addr"`
	AdminListenAddr   string           `json:"admin_listen_addr"`
	DefaultRuleAction core.RuleAction  `json:"default_rule_action"`
	Proxy             core.ProxyConfig `json:"proxy"`
	Admin             struct {
		SessionCookieName string                    `json:"session_cookie_name"`
		SessionTTLMinutes int                       `json:"session_ttl_minutes"`
		Accounts          []core.AdminAccountConfig `json:"accounts"`
	} `json:"admin"`
}

func mergeAdminAccounts(existing []core.AdminAccountConfig, incoming []core.AdminAccountConfig) ([]core.AdminAccountConfig, error) {
	existingByUsername := make(map[string]core.AdminAccountConfig, len(existing))
	for _, account := range existing {
		existingByUsername[account.Username] = account
	}
	out := make([]core.AdminAccountConfig, 0, len(incoming))
	for _, account := range incoming {
		previous := existingByUsername[account.Username]
		if account.Password != "" {
			hash, err := auth.HashPassword(account.Password)
			if err != nil {
				return nil, err
			}
			account.PasswordHash = hash
		} else {
			account.PasswordHash = previous.PasswordHash
			account.Password = previous.Password
		}
		if account.PasswordHash == "" && account.Password == "" {
			return nil, fmt.Errorf("admin account %s requires a password", account.Username)
		}
		out = append(out, account)
	}
	return out, nil
}

func sanitizeSettings(cfg core.AppConfig) settingsPayload {
	payload := settingsPayload{
		APIListenAddr:     cfg.APIListenAddr,
		AdminListenAddr:   cfg.AdminListenAddr,
		DefaultRuleAction: cfg.DefaultRuleAction,
		Proxy:             cfg.Proxy,
	}
	payload.Admin.SessionCookieName = cfg.Admin.SessionCookieName
	payload.Admin.SessionTTLMinutes = cfg.Admin.SessionTTLMinutes
	payload.Admin.Accounts = append([]core.AdminAccountConfig(nil), cfg.Admin.Accounts...)
	for i := range payload.Admin.Accounts {
		payload.Admin.Accounts[i].Password = ""
		payload.Admin.Accounts[i].PasswordHash = ""
	}
	return payload
}

func (s *Service) configuredModelHealth() []core.ModelHealth {
	cfg := s.configRuntime.Snapshot()
	current := s.state.SnapshotHealth()
	byKey := make(map[string]core.ModelHealth, len(current))
	for _, item := range current {
		byKey[item.ModelKey] = item
	}
	out := make([]core.ModelHealth, 0)
	for _, providerCfg := range cfg.Providers {
		for _, model := range providerCfg.Models {
			key := providerCfg.ID + "/" + model.ID
			if item, ok := byKey[key]; ok {
				out = append(out, item)
				continue
			}
			out = append(out, core.ModelHealth{
				ModelKey:   key,
				ProviderID: providerCfg.ID,
				ModelID:    model.ID,
				Status:     core.HealthHealthy,
			})
		}
	}
	return out
}

func newID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}
