package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/kernel/mcpadapter"
)

// RunManager is the interface dashboard needs from the serve subsystem.
type RunManager interface {
	RunningCount() int
	TotalCount() int
}

// LeaseSnapshot mirrors kernel.LeaseSnapshot to avoid circular imports.
type LeaseSnapshot struct {
	ID          string `json:"id"`
	Capability  string `json:"capability"`
	ResourceKey string `json:"resource_key"`
	AccessMode  string `json:"access_mode"`
}

// LeaseProvider provides active lease information for dashboard display.
type LeaseProvider interface {
	ActiveLeases() []LeaseSnapshot
}

// PatternStat 是模式库中单个模式的 dashboard 视图。
type PatternStat struct {
	ID           string  `json:"id"`
	Category     string  `json:"category"`
	Source       string  `json:"source"`
	MatchCount   int     `json:"match_count"`
	SuccessCount int     `json:"success_count"`
	FailureCount int     `json:"failure_count"`
	SuccessRate  float64 `json:"success_rate"`
	LastHitAt    string  `json:"last_hit_at,omitempty"`
}

// DailySummaryStat 是一天的总结简化视图。
type DailySummaryStat struct {
	Date        string `json:"date"`
	RunsTotal   int    `json:"runs_total"`
	RunsFailed  int    `json:"runs_failed"`
	BrainCounts string `json:"brain_counts"`
	SummaryText string `json:"summary_text,omitempty"`
}

// InteractionStat 是按 brain 维度的交互序列计数。
type InteractionStat struct {
	BrainKind string `json:"brain_kind"`
	Count     int    `json:"count"`
	Successes int    `json:"successes"`
}

// LearningOverview 是 /v1/dashboard/learning 的完整响应。
type LearningOverview struct {
	Patterns     []PatternStat      `json:"patterns"`
	Daily        []DailySummaryStat `json:"daily"`
	Interactions []InteractionStat  `json:"interactions"`
}

// LearningProvider 是学习成果数据源。cmd/brain 侧实现,组合
// PatternLibrary + LearningStore。
type LearningProvider interface {
	LearningOverview() LearningOverview
}

type Overview struct {
	Brains      int       `json:"brain_count"`
	ActiveRuns  int       `json:"active_runs"`
	TotalRuns   int       `json:"total_runs"`
	ServerStart time.Time `json:"server_start"`
}

func handleOverview(w http.ResponseWriter, _ *http.Request, mgr RunManager, pool *kernel.ProcessBrainPool, startTime time.Time) {
	brainCount := 0
	if pool != nil {
		brainCount = len(pool.AvailableKinds())
	}

	overview := Overview{
		Brains:      brainCount,
		ActiveRuns:  mgr.RunningCount(),
		TotalRuns:   mgr.TotalCount(),
		ServerStart: startTime,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(overview)
}

func handleBrains(w http.ResponseWriter, _ *http.Request, pool *kernel.ProcessBrainPool) {
	if pool == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"brains": []interface{}{}, "total": 0})
		return
	}

	statuses := pool.Status()
	type brainItem struct {
		Kind       string `json:"kind"`
		Running    bool   `json:"running"`
		Binary     string `json:"binary,omitempty"`
		AutoStart  bool   `json:"auto_start,omitempty"`
	}

	items := make([]brainItem, 0, len(statuses))
	for kind, bs := range statuses {
		items = append(items, brainItem{
			Kind:      string(kind),
			Running:   bs.Running,
			Binary:    bs.Binary,
			AutoStart: bs.AutoStart,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"brains": items, "total": len(items)})
}

func handleMCP(w http.ResponseWriter, _ *http.Request, mcpPool *mcpadapter.MCPBrainPool) {
	if mcpPool == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"adapters": []interface{}{}, "total": 0})
		return
	}

	statuses := mcpPool.Status()
	type adapterItem struct {
		Kind    string `json:"kind"`
		Running bool   `json:"running"`
		Binary  string `json:"binary,omitempty"`
	}

	items := make([]adapterItem, 0, len(statuses))
	for kind, bs := range statuses {
		items = append(items, adapterItem{
			Kind:    string(kind),
			Running: bs.Running,
			Binary:  bs.Binary,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"adapters": items, "total": len(items)})
}

func handleBrainDetail(w http.ResponseWriter, r *http.Request, pool *kernel.ProcessBrainPool) {
	if pool == nil {
		http.Error(w, `{"error":"brain pool not available"}`, http.StatusServiceUnavailable)
		return
	}

	kind := strings.TrimPrefix(r.URL.Path, "/v1/dashboard/brains/")
	kind = strings.TrimSpace(kind)
	if kind == "" {
		http.Error(w, `{"error":"kind is required"}`, http.StatusBadRequest)
		return
	}

	status, ok := pool.BrainDetail(agent.Kind(kind))
	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":"brain %q not found"}`, kind), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func handleBrainRestart(w http.ResponseWriter, r *http.Request, pool *kernel.ProcessBrainPool) {
	if pool == nil {
		http.Error(w, `{"error":"brain pool not available"}`, http.StatusServiceUnavailable)
		return
	}

	kind := strings.TrimPrefix(r.URL.Path, "/v1/dashboard/brains/")
	kind = strings.TrimSpace(kind)
	if kind == "" {
		http.Error(w, `{"error":"kind is required"}`, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := pool.RestartBrain(ctx, agent.Kind(kind)); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"restart failed: %v"}`, err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "restarted", "kind": kind})
}

func handleDeleteLease(w http.ResponseWriter, r *http.Request, lp LeaseProvider) {
	capability := r.URL.Query().Get("capability")
	resourceKey := r.URL.Query().Get("resource_key")
	if capability == "" || resourceKey == "" {
		http.Error(w, `{"error":"capability and resource_key are required"}`, http.StatusBadRequest)
		return
	}

	if mlm, ok := lp.(interface{ ForceRevoke(string, string) int }); ok {
		n := mlm.ForceRevoke(capability, resourceKey)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":       "revoked",
			"count":        n,
			"capability":   capability,
			"resource_key": resourceKey,
		})
		return
	}

	http.Error(w, `{"error":"lease provider does not support force revoke"}`, http.StatusNotImplemented)
}

func handleEvents(w http.ResponseWriter, r *http.Request, bus *events.MemEventBus) {
	if bus == nil {
		http.Error(w, `{"error":"event bus not available"}`, http.StatusServiceUnavailable)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := bus.Subscribe(r.Context(), "")
	defer cancel()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func handleAuth(w http.ResponseWriter, _ *http.Request, cfg *config.Config) {
	type providerStatus struct {
		Name      string `json:"name"`
		HasAPIKey bool   `json:"has_api_key"`
		BaseURL   string `json:"base_url,omitempty"`
		Model     string `json:"model,omitempty"`
	}

	var providers []providerStatus
	if cfg != nil {
		for name, p := range cfg.Providers {
			providers = append(providers, providerStatus{
				Name:      name,
				HasAPIKey: p.APIKey != "",
				BaseURL:   p.BaseURL,
				Model:     p.Model,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"providers": providers,
		"total":     len(providers),
	})
}

func handleLeases(w http.ResponseWriter, _ *http.Request, lp LeaseProvider) {
	var leases []LeaseSnapshot
	if lp != nil {
		leases = lp.ActiveLeases()
	}
	if leases == nil {
		leases = []LeaseSnapshot{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"leases": leases,
		"total":  len(leases),
	})
}

func handleLearning(w http.ResponseWriter, _ *http.Request, lp LearningProvider) {
	overview := LearningOverview{
		Patterns:     []PatternStat{},
		Daily:        []DailySummaryStat{},
		Interactions: []InteractionStat{},
	}
	if lp != nil {
		overview = lp.LearningOverview()
		if overview.Patterns == nil {
			overview.Patterns = []PatternStat{}
		}
		if overview.Daily == nil {
			overview.Daily = []DailySummaryStat{}
		}
		if overview.Interactions == nil {
			overview.Interactions = []InteractionStat{}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(overview)
}

func handleProviders(w http.ResponseWriter, _ *http.Request, cfg *config.Config) {
	type providerDetail struct {
		Name      string `json:"name"`
		HasAPIKey bool   `json:"has_api_key"`
		BaseURL   string `json:"base_url,omitempty"`
		Model     string `json:"model,omitempty"`
	}

	var providers []providerDetail
	if cfg != nil {
		for name, p := range cfg.Providers {
			providers = append(providers, providerDetail{
				Name:      name,
				HasAPIKey: p.APIKey != "",
				BaseURL:   p.BaseURL,
				Model:     p.Model,
			})
		}
	}
	if providers == nil {
		providers = []providerDetail{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"providers": providers,
		"total":     len(providers),
	})
}

// AuthMiddleware returns an http.Handler that enforces Bearer token auth.
// If token is empty, it passes through without checks.
func extractBearerToken(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") && strings.TrimPrefix(auth, "Bearer ") == token {
		return true
	}
	// SSE/EventSource does not support custom headers, fallback to query param
	if r.URL.Query().Get("token") == token {
		return true
	}
	return false
}

// AuthMiddleware returns an http.Handler that enforces Bearer token auth.
// If token is empty, it passes through without checks.
func AuthMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !extractBearerToken(r, token) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func authWrap(token string, h http.HandlerFunc) http.HandlerFunc {
	if token == "" {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !extractBearerToken(r, token) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		h(w, r)
	}
}

// RegisterRoutes registers all /v1/dashboard/* routes.
// Returns the WSHub so callers can start it with hub.Start(ctx).
// learnP 允许为 nil(运行在无学习库的早期配置),对应端点返回空集。
// dashboardToken 从环境变量 BRAIN_DASHBOARD_TOKEN 读取;若为空则跳过认证。
func RegisterRoutes(mux *http.ServeMux, mgr RunManager, pool *kernel.ProcessBrainPool, mcpPool *mcpadapter.MCPBrainPool, bus *events.MemEventBus, cfg *config.Config, startTime time.Time, lp LeaseProvider, learnP LearningProvider, quantCaller QuantToolCaller) *WSHub {
	token := os.Getenv("BRAIN_DASHBOARD_TOKEN")
	wsHub := NewWSHub(bus)

	// 交易 API 路由 (映射到 quant sidecar tools)
	mux.HandleFunc("/api/v1/portfolio", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handlePortfolio(w, r, quantCaller)
	})
	mux.HandleFunc("/api/v1/accounts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleAccounts(w, r, quantCaller)
	})
	mux.HandleFunc("/api/v1/risk", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleRisk(w, r, quantCaller)
	})
	mux.HandleFunc("/api/v1/trading/pause", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleTradingPause(w, r, quantCaller)
	})
	mux.HandleFunc("/api/v1/trading/resume", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleTradingResume(w, r, quantCaller)
	})
	mux.HandleFunc("/api/v1/accounts/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/accounts/")
		if path == "" {
			http.Error(w, `{"error":"missing account id"}`, http.StatusBadRequest)
			return
		}
		switch {
		case strings.HasSuffix(path, "/pause") && r.Method == http.MethodPost:
			handleAccountPause(w, r, quantCaller)
		case strings.HasSuffix(path, "/resume") && r.Method == http.MethodPost:
			handleAccountResume(w, r, quantCaller)
		case strings.HasSuffix(path, "/close-all") && r.Method == http.MethodPost:
			handleAccountCloseAll(w, r, quantCaller)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/positions/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/positions/")
		if path == "" {
			http.Error(w, `{"error":"missing position id"}`, http.StatusBadRequest)
			return
		}
		if strings.HasSuffix(path, "/close") && r.Method == http.MethodPost {
			handlePositionClose(w, r, quantCaller)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	mux.HandleFunc("/v1/dashboard/overview", authWrap(token, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleOverview(w, r, mgr, pool, startTime)
	}))

	mux.HandleFunc("/v1/dashboard/brains", authWrap(token, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/dashboard/brains/") && len(r.URL.Path) > len("/v1/dashboard/brains/") {
			if r.Method == http.MethodPost {
				handleBrainRestart(w, r, pool)
				return
			}
			handleBrainDetail(w, r, pool)
			return
		}
		handleBrains(w, r, pool)
	}))

	mux.HandleFunc("/v1/dashboard/events", authWrap(token, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleEvents(w, r, bus)
	}))

	mux.HandleFunc("/v1/dashboard/auth", authWrap(token, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleAuth(w, r, cfg)
	}))

	mux.HandleFunc("/v1/dashboard/leases", authWrap(token, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleLeases(w, r, lp)
		case http.MethodDelete:
			handleDeleteLease(w, r, lp)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))

	mux.HandleFunc("/v1/dashboard/providers", authWrap(token, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleProviders(w, r, cfg)
	}))

	mux.HandleFunc("/v1/dashboard/mcp", authWrap(token, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleMCP(w, r, mcpPool)
	}))

	mux.HandleFunc("/v1/dashboard/learning", authWrap(token, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleLearning(w, r, learnP)
	}))

	// B-5: Browser pattern stats endpoint.
	mux.HandleFunc("/v1/dashboard/browser/pattern-stats", authWrap(token, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleBrowserPatternStats(w, r)
	}))

	mux.HandleFunc("/v1/dashboard/executions", authWrap(token, func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/v1/executions"
		http.Redirect(w, r, "/v1/executions", http.StatusTemporaryRedirect)
	}))

	mux.HandleFunc("/v1/dashboard/ws", authWrap(token, func(w http.ResponseWriter, r *http.Request) {
		wsHub.HandleWS(w, r)
	}))

	return wsHub
}
