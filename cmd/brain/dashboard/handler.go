package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/kernel"
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
		Kind    string `json:"kind"`
		Running bool   `json:"running"`
		Binary  string `json:"binary,omitempty"`
	}

	items := make([]brainItem, 0, len(statuses))
	for kind, bs := range statuses {
		items = append(items, brainItem{
			Kind:    string(kind),
			Running: bs.Running,
			Binary:  bs.Binary,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"brains": items, "total": len(items)})
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

// RegisterRoutes registers all /v1/dashboard/* routes.
// Returns the WSHub so callers can start it with hub.Start(ctx).
// learnP 允许为 nil(运行在无学习库的早期配置),对应端点返回空集。
func RegisterRoutes(mux *http.ServeMux, mgr RunManager, pool *kernel.ProcessBrainPool, bus *events.MemEventBus, cfg *config.Config, startTime time.Time, lp LeaseProvider, learnP LearningProvider) *WSHub {
	wsHub := NewWSHub(bus)
	mux.HandleFunc("/v1/dashboard/overview", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleOverview(w, r, mgr, pool, startTime)
	})

	mux.HandleFunc("/v1/dashboard/brains", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleBrains(w, r, pool)
	})

	mux.HandleFunc("/v1/dashboard/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleEvents(w, r, bus)
	})

	mux.HandleFunc("/v1/dashboard/auth", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleAuth(w, r, cfg)
	})

	mux.HandleFunc("/v1/dashboard/leases", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleLeases(w, r, lp)
	})

	mux.HandleFunc("/v1/dashboard/providers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleProviders(w, r, cfg)
	})

	mux.HandleFunc("/v1/dashboard/learning", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleLearning(w, r, learnP)
	})

	mux.HandleFunc("/v1/dashboard/ws", func(w http.ResponseWriter, r *http.Request) {
		wsHub.HandleWS(w, r)
	})

	return wsHub
}
