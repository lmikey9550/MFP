package state

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mfp/internal/core"
)

type Event struct {
	Type string          `json:"type"`
	Time time.Time       `json:"time"`
	Log  core.RequestLog `json:"log"`
}

type Hub struct {
	mu                 sync.RWMutex
	health             map[string]*healthEntry
	sticky             map[string]core.StickyRecord
	providerCooldown   map[string]time.Time
	credentialCooldown map[string]time.Time
	events             []eventPoint
	logs               []core.RequestLog
	audits             []core.AuditRecord
	subscribers        map[chan Event]struct{}
	dataDir            string
}

type healthEntry struct {
	ProviderID           string     `json:"provider_id"`
	ModelID              string     `json:"model_id"`
	ConsecutiveFailures  int        `json:"consecutive_failures"`
	ConsecutiveSuccesses int        `json:"consecutive_successes"`
	LastFailureAt        *time.Time `json:"last_failure_at,omitempty"`
	LastFailureReason    string     `json:"last_failure_reason,omitempty"`
	LastSuccessAt        *time.Time `json:"last_success_at,omitempty"`
	MarkedUnhealthyAt    *time.Time `json:"marked_unhealthy_at,omitempty"`
	CooldownUntil        *time.Time `json:"cooldown_until,omitempty"`
	ActiveRequests       int        `json:"active_requests"`
	Successes            int        `json:"successes"`
	Failures             int        `json:"failures"`
	LatenciesMS          []int64    `json:"latencies_ms,omitempty"`
}

type eventPoint struct {
	Time      time.Time `json:"time"`
	ModelKey  string    `json:"model_key"`
	Success   bool      `json:"success"`
	LatencyMS int64     `json:"latency_ms"`
}

func NewHub(dataDir string) (*Hub, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	hub := &Hub{
		health:             map[string]*healthEntry{},
		sticky:             map[string]core.StickyRecord{},
		providerCooldown:   map[string]time.Time{},
		credentialCooldown: map[string]time.Time{},
		subscribers:        map[chan Event]struct{}{},
		dataDir:            dataDir,
	}
	_ = hub.loadLogs()
	_ = hub.loadAudits()
	return hub, nil
}

func (h *Hub) loadLogs() error {
	path := filepath.Join(h.dataDir, "requests.jsonl")
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var logEntry core.RequestLog
		if err := json.Unmarshal(scanner.Bytes(), &logEntry); err == nil {
			h.logs = append(h.logs, logEntry)
		}
	}
	return scanner.Err()
}

func (h *Hub) loadAudits() error {
	path := filepath.Join(h.dataDir, "audit.jsonl")
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record core.AuditRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err == nil {
			h.audits = append(h.audits, record)
		}
	}
	return scanner.Err()
}

func (h *Hub) IncrementActive(modelKey string, providerID string, modelID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.ensureHealthLocked(modelKey, providerID, modelID)
	entry.ActiveRequests++
}

func (h *Hub) DecrementActive(modelKey string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if entry, ok := h.health[modelKey]; ok && entry.ActiveRequests > 0 {
		entry.ActiveRequests--
	}
}

func (h *Hub) ActiveRequests(modelKey string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if entry, ok := h.health[modelKey]; ok {
		return entry.ActiveRequests
	}
	return 0
}

func (h *Hub) RecordSuccess(modelKey, providerID, modelID string, latency time.Duration) {
	now := time.Now().UTC()
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.ensureHealthLocked(modelKey, providerID, modelID)
	entry.ConsecutiveFailures = 0
	entry.ConsecutiveSuccesses++
	entry.LastSuccessAt = &now
	entry.LastFailureReason = ""
	entry.MarkedUnhealthyAt = nil
	entry.CooldownUntil = nil
	entry.Successes++
	entry.LatenciesMS = appendTrim(entry.LatenciesMS, latency.Milliseconds(), 256)
	h.events = appendTrimEvent(h.events, eventPoint{Time: now, ModelKey: modelKey, Success: true, LatencyMS: latency.Milliseconds()}, 2048)
}

func (h *Hub) RecordFailure(modelKey, providerID, modelID, errorCode, category string, cooldown time.Duration) {
	now := time.Now().UTC()
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.ensureHealthLocked(modelKey, providerID, modelID)
	entry.ConsecutiveFailures++
	entry.ConsecutiveSuccesses = 0
	entry.LastFailureAt = &now
	entry.LastFailureReason = errorCode
	if entry.LastFailureReason == "" {
		entry.LastFailureReason = category
	}
	entry.Failures++
	if cooldown > 0 {
		until := now.Add(cooldown)
		entry.CooldownUntil = &until
		entry.MarkedUnhealthyAt = &now
	} else {
		entry.CooldownUntil = nil
		entry.MarkedUnhealthyAt = nil
	}
	h.events = appendTrimEvent(h.events, eventPoint{Time: now, ModelKey: modelKey, Success: false, LatencyMS: 0}, 2048)
}

func (h *Hub) SetProviderCooldown(providerID string, until time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.providerCooldown[providerID] = until
}

func (h *Hub) SetCredentialCooldown(credentialRef string, until time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.credentialCooldown[credentialRef] = until
}

func (h *Hub) InCooldown(providerID, credentialRef, modelKey string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	now := time.Now().UTC()
	if until, ok := h.providerCooldown[providerID]; ok && now.Before(until) {
		return true
	}
	if credentialRef != "" {
		if until, ok := h.credentialCooldown[credentialRef]; ok && now.Before(until) {
			return true
		}
	}
	if entry, ok := h.health[modelKey]; ok && entry.CooldownUntil != nil {
		return now.Before(*entry.CooldownUntil)
	}
	return false
}

func (h *Hub) SnapshotHealth() []core.ModelHealth {
	h.mu.RLock()
	defer h.mu.RUnlock()
	now := time.Now().UTC()
	out := make([]core.ModelHealth, 0, len(h.health))
	for modelKey, entry := range h.health {
		status := core.HealthHealthy
		if entry.CooldownUntil != nil && now.Before(*entry.CooldownUntil) {
			status = core.HealthUnhealthy
		}
		successRate, avgLatency, p95Latency := calcMetrics(entry)
		out = append(out, core.ModelHealth{
			ModelKey:             modelKey,
			ProviderID:           entry.ProviderID,
			ModelID:              entry.ModelID,
			Status:               status,
			ConsecutiveFailures:  entry.ConsecutiveFailures,
			ConsecutiveSuccesses: entry.ConsecutiveSuccesses,
			LastFailureAt:        entry.LastFailureAt,
			LastFailureReason:    entry.LastFailureReason,
			LastSuccessAt:        entry.LastSuccessAt,
			SuccessRate24h:       successRate,
			AvgLatencyMS:         avgLatency,
			P95LatencyMS:         p95Latency,
			ActiveRequests:       entry.ActiveRequests,
			MarkedUnhealthyAt:    entry.MarkedUnhealthyAt,
			CooldownUntil:        entry.CooldownUntil,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ModelKey < out[j].ModelKey
	})
	return out
}

func (h *Hub) HealthByKey(modelKey string) core.ModelHealth {
	for _, item := range h.SnapshotHealth() {
		if item.ModelKey == modelKey {
			return item
		}
	}
	return core.ModelHealth{ModelKey: modelKey, Status: core.HealthUnknown}
}

func calcMetrics(entry *healthEntry) (float64, float64, float64) {
	total := entry.Successes + entry.Failures
	successRate := 0.0
	if total > 0 {
		successRate = float64(entry.Successes) / float64(total) * 100
	}
	if len(entry.LatenciesMS) == 0 {
		return successRate, 0, 0
	}
	var sum int64
	copied := append([]int64(nil), entry.LatenciesMS...)
	for _, latency := range copied {
		sum += latency
	}
	sort.Slice(copied, func(i, j int) bool { return copied[i] < copied[j] })
	p95Index := int(math.Ceil(float64(len(copied))*0.95)) - 1
	if p95Index < 0 {
		p95Index = 0
	}
	return successRate, float64(sum) / float64(len(copied)), float64(copied[p95Index])
}

func (h *Hub) SetSticky(scopeKey, virtualModel string, candidate core.ActualModelRef, ttl time.Duration) {
	if scopeKey == "" || ttl <= 0 {
		return
	}
	now := time.Now().UTC()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sticky[scopeKey] = core.StickyRecord{
		VirtualModel: virtualModel,
		ScopeKey:     scopeKey,
		ActualModel:  candidate.Key(),
		LastUsedAt:   now,
		ExpiresAt:    now.Add(ttl),
	}
}

func (h *Hub) GetSticky(scopeKey, virtualModel string) (core.StickyRecord, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	record, ok := h.sticky[scopeKey]
	if !ok || record.VirtualModel != virtualModel || time.Now().UTC().After(record.ExpiresAt) {
		return core.StickyRecord{}, false
	}
	return record, true
}

func (h *Hub) DeleteSticky(scopeKey string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sticky, scopeKey)
}

func (h *Hub) RecoverModel(modelKey string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if entry, ok := h.health[modelKey]; ok {
		entry.ConsecutiveFailures = 0
		entry.ConsecutiveSuccesses = 0
		entry.LastFailureReason = ""
		entry.MarkedUnhealthyAt = nil
		entry.CooldownUntil = nil
	}
}

func (h *Hub) StickyRecords() []core.StickyRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()
	now := time.Now().UTC()
	out := make([]core.StickyRecord, 0, len(h.sticky))
	for key, record := range h.sticky {
		if now.After(record.ExpiresAt) {
			continue
		}
		record.ScopeKey = key
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ScopeKey < out[j].ScopeKey })
	return out
}

func (h *Hub) AddRequestLog(logEntry core.RequestLog) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = append(h.logs, logEntry)
	if len(h.logs) > 10000 {
		h.logs = append([]core.RequestLog(nil), h.logs[len(h.logs)-10000:]...)
	}
	if err := appendJSONLine(filepath.Join(h.dataDir, "requests.jsonl"), logEntry); err != nil {
		return err
	}
	event := Event{Type: "request", Time: time.Now().UTC(), Log: logEntry}
	for ch := range h.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
	return nil
}

func (h *Hub) RequestLogs() []core.RequestLog {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := append([]core.RequestLog(nil), h.logs...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (h *Hub) AddAudit(record core.AuditRecord) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.audits = append(h.audits, record)
	if len(h.audits) > 5000 {
		h.audits = append([]core.AuditRecord(nil), h.audits[len(h.audits)-5000:]...)
	}
	return appendJSONLine(filepath.Join(h.dataDir, "audit.jsonl"), record)
}

func (h *Hub) AuditLogs() []core.AuditRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := append([]core.AuditRecord(nil), h.audits...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (h *Hub) MetricsSummary() map[string]any {
	logs := h.RequestLogs()
	health := h.SnapshotHealth()
	success := 0
	failover := 0
	allFailed := 0
	for _, logEntry := range logs {
		switch logEntry.Status {
		case "success":
			success++
		case "failover":
			failover++
		case "all_failed":
			allFailed++
		}
	}
	return map[string]any{
		"requests_total":   len(logs),
		"success_total":    success,
		"failover_total":   failover,
		"all_failed_total": allFailed,
		"healthy_models":   countStatus(health, core.HealthHealthy),
		"unhealthy_models": countStatus(health, core.HealthUnhealthy),
		"timestamp":        time.Now().UTC(),
	}
}

func countStatus(health []core.ModelHealth, status core.HealthStatus) int {
	count := 0
	for _, item := range health {
		if item.Status == status {
			count++
		}
	}
	return count
}

func (h *Hub) Subscribe(ctx context.Context) <-chan Event {
	ch := make(chan Event, 16)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		h.mu.Lock()
		delete(h.subscribers, ch)
		h.mu.Unlock()
		close(ch)
	}()
	return ch
}

func appendJSONLine(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		return err
	}
	return nil
}

func (h *Hub) ensureHealthLocked(modelKey, providerID, modelID string) *healthEntry {
	entry, ok := h.health[modelKey]
	if !ok {
		entry = &healthEntry{
			ProviderID: providerID,
			ModelID:    modelID,
		}
		h.health[modelKey] = entry
	}
	return entry
}

func appendTrim(in []int64, value int64, max int) []int64 {
	in = append(in, value)
	if len(in) <= max {
		return in
	}
	return append([]int64(nil), in[len(in)-max:]...)
}

func appendTrimEvent(in []eventPoint, value eventPoint, max int) []eventPoint {
	in = append(in, value)
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	filtered := in[:0]
	for _, item := range in {
		if item.Time.After(cutoff) {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) <= max {
		return append([]eventPoint(nil), filtered...)
	}
	return append([]eventPoint(nil), filtered[len(filtered)-max:]...)
}

func SanitizeAuthorization(in string) string {
	if in == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(in), "bearer ") {
		return "***"
	}
	token := strings.TrimSpace(in[7:])
	if len(token) <= 8 {
		return "Bearer ****"
	}
	return "Bearer " + token[:4] + "..." + token[len(token)-4:]
}
