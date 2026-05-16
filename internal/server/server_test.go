package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mfp/internal/auth"
	"mfp/internal/core"
	"mfp/internal/state"
)

func TestProxyFailoverAcrossProviders(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded","message":"limit"}}`))
	}))
	defer first.Close()

	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[]}`))
	}))
	defer second.Close()

	cfg := core.AppConfig{
		APIListenAddr:   ":0",
		AdminListenAddr: ":0",
		DataDir:         t.TempDir(),
		Admin: core.AdminConfig{
			SessionCookieName: "mfp",
			SessionTTLMinutes: 10,
			Accounts:          []core.AdminAccountConfig{{Username: "admin", Role: "admin"}},
		},
		Providers: []core.ProviderConfig{
			{ID: "p1", Type: core.ProviderTypeOpenAICompatible, BaseURL: first.URL, Enabled: true, Models: []core.ProviderModel{{ID: "m1"}}},
			{ID: "p2", Type: core.ProviderTypeOpenAICompatible, BaseURL: second.URL, Enabled: true, Models: []core.ProviderModel{{ID: "m2"}}},
		},
		VirtualModels: []core.VirtualModel{
			{
				ID:          "smart",
				Sticky:      true,
				StickyScope: core.StickyScopeAgent,
				MaxAttempts: 2,
				Candidates: []core.ActualModelRef{
					{ProviderID: "p1", ModelID: "m1", Enabled: true, Priority: 1, MaxRetry: 1},
					{ProviderID: "p2", ModelID: "m2", Enabled: true, Priority: 2, MaxRetry: 1},
				},
			},
		},
		ErrorRules: []core.ErrorRule{
			{
				ID:              "rate-limit",
				Enabled:         true,
				Priority:        1,
				Action:          core.RuleActionFailover,
				CooldownSeconds: 1,
				HealthImpact:    core.HealthImpactCredential,
				Match:           core.ErrorMatch{Category: "rate_limit"},
			},
		},
	}
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"mfp/smart","messages":[]}`))
	req.Header.Set("X-MFP-Agent-Id", "ops")
	rec := httptest.NewRecorder()

	service.handleProxy(rec, req)
	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	logs := hub.RequestLogs()
	if len(logs) != 1 || logs[0].Status != "failover" {
		t.Fatalf("expected one failover log, got %#v", logs)
	}
	if logs[0].ProviderID != "p2" {
		t.Fatalf("expected failover to p2, got %s", logs[0].ProviderID)
	}
}

func TestClientModelsListsVirtualModels(t *testing.T) {
	cfg := proxyTestConfig(t, "http://127.0.0.1:1", []core.ActualModelRef{{ProviderID: "p1", ModelID: "m1", Enabled: true, MaxRetry: 1}})
	cfg.VirtualModels = append(cfg.VirtualModels, core.VirtualModel{ID: "fast"})
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))

	for _, path := range []string{"/v1/models", "/models"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			service.APIServer().Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			res := rec.Result()
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d", res.StatusCode)
			}
			var payload struct {
				Object string `json:"object"`
				Data   []struct {
					ID      string `json:"id"`
					Object  string `json:"object"`
					OwnedBy string `json:"owned_by"`
				} `json:"data"`
			}
			if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Object != "list" || len(payload.Data) != 2 || payload.Data[0].ID != "smart" || payload.Data[1].ID != "fast" {
				t.Fatalf("unexpected models payload: %#v", payload)
			}
		})
	}
}

func TestClientModelsRetrievesVirtualModel(t *testing.T) {
	cfg := proxyTestConfig(t, "http://127.0.0.1:1", []core.ActualModelRef{{ProviderID: "p1", ModelID: "m1", Enabled: true, MaxRetry: 1}})
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))
	rec := httptest.NewRecorder()
	service.APIServer().Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/mfp/smart", nil))
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	var payload struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID != "smart" || payload.Object != "model" || payload.OwnedBy != "mfp" {
		t.Fatalf("unexpected model payload: %#v", payload)
	}
}

func TestProxyRequiresVirtualModelAPIKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[]}`))
	}))
	defer upstream.Close()
	cfg := core.AppConfig{
		APIListenAddr:   ":0",
		AdminListenAddr: ":0",
		DataDir:         t.TempDir(),
		Admin:           core.AdminConfig{SessionCookieName: "mfp", SessionTTLMinutes: 10},
		Providers: []core.ProviderConfig{
			{ID: "p1", Type: core.ProviderTypeOpenAICompatible, BaseURL: upstream.URL, Enabled: true, Models: []core.ProviderModel{{ID: "m1"}}},
		},
		VirtualModels: []core.VirtualModel{
			{ID: "smart", APIKey: "vm-key", Candidates: []core.ActualModelRef{{ProviderID: "p1", ModelID: "m1", Enabled: true, MaxRetry: 1}}},
		},
	}
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))

	missingReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"smart","messages":[]}`))
	missingRec := httptest.NewRecorder()
	service.handleProxy(missingRec, missingReq)
	if missingRec.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", missingRec.Result().StatusCode)
	}

	validReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"smart","messages":[]}`))
	validReq.Header.Set("X-MFP-API-Key", "vm-key")
	validRec := httptest.NewRecorder()
	service.handleProxy(validRec, validReq)
	if validRec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with key, got %d", validRec.Result().StatusCode)
	}
}

func TestProxyRejectsRequestBodyOverConfiguredLimit(t *testing.T) {
	cfg := proxyTestConfig(t, "http://127.0.0.1:1", []core.ActualModelRef{{ProviderID: "p1", ModelID: "m1", Enabled: true, MaxRetry: 1}})
	cfg.Proxy.MaxBodyBytes = 20
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))
	rec := httptest.NewRecorder()
	service.APIServer().Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"smart","messages":[],"padding":"too-large"}`)))
	if rec.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 over body limit, got %d", rec.Result().StatusCode)
	}
}

func TestProxyAuthorizationForwardingAndProviderCredential(t *testing.T) {
	for _, tt := range []struct {
		name        string
		trustClient bool
		providerKey string
		wantAuth    string
	}{
		{name: "drops client authorization by default", wantAuth: ""},
		{name: "forwards client authorization when trusted", trustClient: true, wantAuth: "Bearer client-key"},
		{name: "provider credential overrides client authorization", trustClient: true, providerKey: "provider-key", wantAuth: "Bearer provider-key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuth string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer upstream.Close()

			cfg := proxyTestConfig(t, upstream.URL, []core.ActualModelRef{{ProviderID: "p1", ModelID: "m1", Enabled: true, MaxRetry: 1}})
			cfg.Proxy.TrustAuthorizationHeader = tt.trustClient
			cfg.Providers[0].APIKey = tt.providerKey
			hub, err := state.NewHub(cfg.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"smart","messages":[]}`))
			req.Header.Set("Authorization", "Bearer client-key")
			rec := httptest.NewRecorder()
			service.APIServer().Handler.ServeHTTP(rec, req)
			if rec.Result().StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Result().StatusCode)
			}
			if gotAuth != tt.wantAuth {
				t.Fatalf("expected Authorization %q, got %q", tt.wantAuth, gotAuth)
			}
		})
	}
}

func TestProxyPreservesResponseContentTypeAndStripsHopHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Connection", "close")
		w.Header().Set("Transfer-Encoding", "chunked")
		_, _ = w.Write([]byte("audio"))
	}))
	defer upstream.Close()

	cfg := proxyTestConfig(t, upstream.URL, []core.ActualModelRef{{ProviderID: "p1", ModelID: "speech-model", Enabled: true, MaxRetry: 1}})
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))
	rec := httptest.NewRecorder()
	service.APIServer().Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(`{"model":"smart","input":"hello"}`)))
	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); got != "audio/mpeg" {
		t.Fatalf("expected audio/mpeg content type, got %q", got)
	}
	if got := res.Header.Get("Connection"); got != "" {
		t.Fatalf("expected Connection header stripped, got %q", got)
	}
	if got := res.Header.Get("Transfer-Encoding"); got != "" {
		t.Fatalf("expected Transfer-Encoding header stripped, got %q", got)
	}
}

func TestProxyForwardsCommonEndpointsWithBackendModel(t *testing.T) {
	for path, body := range map[string]string{
		"/v1/chat/completions":      `{"model":"smart","messages":[]}`,
		"/v1/responses":             `{"model":"smart","input":"hello"}`,
		"/v1/completions":           `{"model":"smart","prompt":"hello"}`,
		"/v1/embeddings":            `{"model":"smart","input":"hello"}`,
		"/v1/images/generations":    `{"model":"smart","prompt":"hello"}`,
		"/v1/images/edits":          `{"model":"smart","prompt":"hello"}`,
		"/v1/audio/transcriptions":  `{"model":"smart"}`,
		"/v1/audio/speech":          `{"model":"smart","input":"hello","voice":"alloy"}`,
		"/v1/moderations":           `{"model":"smart","input":"hello"}`,
		"/v1/rerank":                `{"model":"smart","query":"q","documents":["d"]}`,
		"/v1/reranks":               `{"model":"smart","query":"q","documents":["d"]}`,
		"/v1/messages":              `{"model":"smart","messages":[]}`,
		"/v1/messages/count_tokens": `{"model":"smart","messages":[]}`,
	} {
		t.Run(path, func(t *testing.T) {
			var gotPath string
			var gotModel string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				gotModel, _ = payload["model"].(string)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer upstream.Close()

			cfg := proxyTestConfig(t, upstream.URL, []core.ActualModelRef{{ProviderID: "p1", ModelID: "m1", Enabled: true, MaxRetry: 1}})
			hub, err := state.NewHub(cfg.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))
			rec := httptest.NewRecorder()
			service.APIServer().Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
			if rec.Result().StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Result().StatusCode)
			}
			if gotPath != path {
				t.Fatalf("expected upstream path %s, got %s", path, gotPath)
			}
			if gotModel != "m1" {
				t.Fatalf("expected backend model m1, got %s", gotModel)
			}
		})
	}
}

func TestProxyForwardsMultipartEndpointWithBackendModel(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Fatalf("expected multipart content type, got %q", r.Header.Get("Content-Type"))
		}
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Fatal(err)
		}
		gotModel = r.FormValue("model")
		if got := r.FormValue("language"); got != "en" {
			t.Fatalf("expected language field preserved, got %q", got)
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "audio" {
			t.Fatalf("expected file content preserved, got %q", string(content))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer upstream.Close()

	cfg := proxyTestConfig(t, upstream.URL, []core.ActualModelRef{{ProviderID: "p1", ModelID: "transcribe-model", Enabled: true, MaxRetry: 1}})
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "smart"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("language", "en"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "audio.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("audio")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	service.APIServer().Handler.ServeHTTP(rec, req)
	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Result().StatusCode)
	}
	if gotModel != "transcribe-model" {
		t.Fatalf("expected transcribe-model, got %s", gotModel)
	}
}

func TestProxyIgnoresCapabilitiesAndForwardsByRouteOrder(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	cfg := proxyTestConfig(t, upstream.URL, []core.ActualModelRef{
		{ProviderID: "p1", ModelID: "first-model", Enabled: true, Priority: 1, MaxRetry: 1, Capabilities: []string{"chat_only"}},
		{ProviderID: "p1", ModelID: "second-model", Enabled: true, Priority: 2, MaxRetry: 1, Capabilities: []string{"embeddings"}},
	})
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))
	rec := httptest.NewRecorder()
	service.APIServer().Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"smart","input":"hello"}`)))
	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Result().StatusCode)
	}
	if gotModel != "first-model" {
		t.Fatalf("expected first route model, got %s", gotModel)
	}
}

func TestProxyForwardsUnknownV1PostEndpoint(t *testing.T) {
	var gotPath string
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	cfg := proxyTestConfig(t, upstream.URL, []core.ActualModelRef{{ProviderID: "p1", ModelID: "backend-model", Enabled: true, MaxRetry: 1}})
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))
	rec := httptest.NewRecorder()
	service.APIServer().Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/future/endpoint", strings.NewReader(`{"model":"smart","input":"hello"}`)))
	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Result().StatusCode)
	}
	if gotPath != "/v1/future/endpoint" {
		t.Fatalf("expected future endpoint path, got %s", gotPath)
	}
	if gotModel != "backend-model" {
		t.Fatalf("expected backend-model, got %s", gotModel)
	}
}

func proxyTestConfig(t *testing.T, upstreamURL string, candidates []core.ActualModelRef) core.AppConfig {
	t.Helper()
	models := make([]core.ProviderModel, 0, len(candidates))
	for _, candidate := range candidates {
		models = append(models, core.ProviderModel{ID: candidate.ModelID, Capabilities: candidate.Capabilities})
	}
	return core.AppConfig{
		APIListenAddr:   ":0",
		AdminListenAddr: ":0",
		DataDir:         t.TempDir(),
		Admin:           core.AdminConfig{SessionCookieName: "mfp", SessionTTLMinutes: 10},
		Providers:       []core.ProviderConfig{{ID: "p1", Type: core.ProviderTypeOpenAICompatible, BaseURL: upstreamURL, Enabled: true, Models: models}},
		VirtualModels:   []core.VirtualModel{{ID: "smart", Candidates: candidates}},
	}
}

func TestAdminLoginAndExport(t *testing.T) {
	cfg := core.AppConfig{
		APIListenAddr:   ":0",
		AdminListenAddr: ":0",
		DataDir:         t.TempDir(),
		Admin: core.AdminConfig{
			SessionCookieName: "mfp",
			SessionTTLMinutes: 10,
			Accounts:          []core.AdminAccountConfig{{Username: "admin", Role: "admin", Password: "secret"}},
		},
		Providers: []core.ProviderConfig{
			{ID: "p1", Type: core.ProviderTypeOpenAICompatible, BaseURL: "http://localhost", CredentialRef: "secret-ref", Enabled: true, Models: []core.ProviderModel{{ID: "m1"}}},
		},
		VirtualModels: []core.VirtualModel{
			{ID: "smart", Sticky: true, StickyScope: core.StickyScopeAgent, Candidates: []core.ActualModelRef{{ProviderID: "p1", ModelID: "m1", Enabled: true}}},
		},
	}
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))

	loginReq := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"admin","password":"secret"}`))
	loginRec := httptest.NewRecorder()
	service.handleLogin(loginRec, loginReq)
	loginRes := loginRec.Result()
	if loginRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", loginRes.StatusCode)
	}
	cookies := loginRes.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected session cookie")
	}

	exportReq := httptest.NewRequest(http.MethodGet, "/admin/v1/config/export", nil)
	exportReq.AddCookie(cookies[0])
	exportRec := httptest.NewRecorder()
	service.requireAdmin(http.HandlerFunc(service.handleConfigExport)).ServeHTTP(exportRec, exportReq)
	if exportRec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", exportRec.Result().StatusCode)
	}
	var exported core.AppConfig
	if err := json.NewDecoder(exportRec.Result().Body).Decode(&exported); err != nil {
		t.Fatal(err)
	}
	if exported.Providers[0].CredentialRef == "secret-ref" {
		t.Fatal("expected credential ref to be sanitized")
	}
	if exported.Admin.Accounts[0].Password != "" {
		t.Fatal("expected password to be removed")
	}
	if exported.Admin.Accounts[0].PasswordHash != "" {
		t.Fatal("expected password hash to be removed")
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/logout", nil)
	logoutReq.AddCookie(cookies[0])
	logoutRec := httptest.NewRecorder()
	service.requireAdmin(http.HandlerFunc(service.handleLogout)).ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected logout 200, got %d", logoutRec.Result().StatusCode)
	}

	expiredReq := httptest.NewRequest(http.MethodGet, "/admin/v1/config/export", nil)
	expiredReq.AddCookie(cookies[0])
	expiredRec := httptest.NewRecorder()
	service.requireAdmin(http.HandlerFunc(service.handleConfigExport)).ServeHTTP(expiredRec, expiredReq)
	if expiredRec.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected old session 401 after logout, got %d", expiredRec.Result().StatusCode)
	}
}

func TestAdminLoginWithPasswordHash(t *testing.T) {
	hash, err := auth.HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	cfg := core.AppConfig{
		APIListenAddr:   ":0",
		AdminListenAddr: ":0",
		DataDir:         t.TempDir(),
		Admin: core.AdminConfig{
			SessionCookieName: "mfp",
			SessionTTLMinutes: 10,
			Accounts:          []core.AdminAccountConfig{{Username: "admin", Role: "admin", PasswordHash: hash}},
		},
		Providers: []core.ProviderConfig{
			{ID: "p1", Type: core.ProviderTypeOpenAICompatible, BaseURL: "http://localhost", Enabled: true, Models: []core.ProviderModel{{ID: "m1"}}},
		},
		VirtualModels: []core.VirtualModel{
			{ID: "smart", Candidates: []core.ActualModelRef{{ProviderID: "p1", ModelID: "m1", Enabled: true}}},
		},
	}
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))

	loginReq := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"admin","password":"secret"}`))
	loginRec := httptest.NewRecorder()
	service.handleLogin(loginRec, loginReq)
	if loginRec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", loginRec.Result().StatusCode)
	}

	badReq := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	badRec := httptest.NewRecorder()
	service.handleLogin(badRec, badReq)
	if badRec.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", badRec.Result().StatusCode)
	}
}

func TestConfigRuntimeSnapshotCloneIsolation(t *testing.T) {
	cfg := core.AppConfig{
		Providers:     []core.ProviderConfig{{ID: "p1", HeadersTemplate: map[string]string{"X-Test": "one"}, Models: []core.ProviderModel{{ID: "m1"}}}},
		VirtualModels: []core.VirtualModel{{ID: "smart", Candidates: []core.ActualModelRef{{ProviderID: "p1", ModelID: "m1", Capabilities: []string{"chat"}}}}},
		ErrorRules:    []core.ErrorRule{{ID: "r1", Match: core.ErrorMatch{StatusCode: func() *int { v := 429; return &v }()}}},
	}
	runtime := NewConfigRuntime(cfg)
	snapshot := runtime.Snapshot()
	snapshot.Providers[0].HeadersTemplate["X-Test"] = "two"
	snapshot.VirtualModels[0].Candidates[0].Capabilities[0] = "responses"
	snapshot.ErrorRules[0].Match.StatusCode = nil
	fresh := runtime.Snapshot()
	if fresh.Providers[0].HeadersTemplate["X-Test"] != "one" {
		t.Fatal("expected provider map to be isolated from snapshot mutations")
	}
	if fresh.VirtualModels[0].Candidates[0].Capabilities[0] != "chat" {
		t.Fatal("expected candidate slice to be isolated from snapshot mutations")
	}
	if fresh.ErrorRules[0].Match.StatusCode == nil {
		t.Fatal("expected error rule pointers to be isolated from snapshot mutations")
	}
}

func TestDeleteProviderPrunesVirtualModelCandidates(t *testing.T) {
	cfg := core.AppConfig{
		APIListenAddr:   ":0",
		AdminListenAddr: ":0",
		DataDir:         t.TempDir(),
		Admin:           core.AdminConfig{SessionCookieName: "mfp", SessionTTLMinutes: 10},
		Providers: []core.ProviderConfig{
			{ID: "p1", Type: core.ProviderTypeOpenAICompatible, BaseURL: "http://one", Enabled: true, Models: []core.ProviderModel{{ID: "m1"}}},
			{ID: "p2", Type: core.ProviderTypeOpenAICompatible, BaseURL: "http://two", Enabled: true, Models: []core.ProviderModel{{ID: "m2"}}},
		},
		VirtualModels: []core.VirtualModel{{ID: "smart", Candidates: []core.ActualModelRef{{ProviderID: "p1", ModelID: "m1", Enabled: true}, {ProviderID: "p2", ModelID: "m2", Enabled: true}}}},
	}
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))

	req := httptest.NewRequest(http.MethodDelete, "/admin/v1/providers/p1", nil)
	rec := httptest.NewRecorder()
	service.handleProviderByID(rec, req)
	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Result().StatusCode)
	}
	got := service.configRuntime.Snapshot()
	if len(got.Providers) != 1 || got.Providers[0].ID != "p2" {
		t.Fatalf("expected remaining provider p2, got %#v", got.Providers)
	}
	if len(got.VirtualModels[0].Candidates) != 1 || got.VirtualModels[0].Candidates[0].ProviderID != "p2" {
		t.Fatalf("expected p1 candidates pruned, got %#v", got.VirtualModels[0].Candidates)
	}
}

func TestUpdateProviderPrunesRemovedModelsFromCandidates(t *testing.T) {
	cfg := core.AppConfig{
		APIListenAddr:   ":0",
		AdminListenAddr: ":0",
		DataDir:         t.TempDir(),
		Admin:           core.AdminConfig{SessionCookieName: "mfp", SessionTTLMinutes: 10},
		Providers:       []core.ProviderConfig{{ID: "p1", Type: core.ProviderTypeOpenAICompatible, BaseURL: "http://one", Enabled: true, Models: []core.ProviderModel{{ID: "m1"}, {ID: "m2"}}}},
		VirtualModels:   []core.VirtualModel{{ID: "smart", Candidates: []core.ActualModelRef{{ProviderID: "p1", ModelID: "m1", Enabled: true}, {ProviderID: "p1", ModelID: "m2", Enabled: true}}}},
	}
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir()+"/test.json", cfg, hub, log.New(io.Discard, "", 0))

	body := strings.NewReader(`{"type":"openai_compatible","base_url":"http://one","enabled":true,"models":[{"id":"m2","label":"M2"}]}`)
	req := httptest.NewRequest(http.MethodPut, "/admin/v1/providers/p1", body)
	rec := httptest.NewRecorder()
	service.handleProviderByID(rec, req)
	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Result().StatusCode)
	}
	got := service.configRuntime.Snapshot()
	if len(got.Providers[0].Models) != 1 || got.Providers[0].Models[0].ID != "m2" {
		t.Fatalf("expected updated provider models, got %#v", got.Providers[0].Models)
	}
	if len(got.VirtualModels[0].Candidates) != 1 || got.VirtualModels[0].Candidates[0].ModelID != "m2" {
		t.Fatalf("expected removed model candidates pruned, got %#v", got.VirtualModels[0].Candidates)
	}
}
