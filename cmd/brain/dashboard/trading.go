package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// QuantToolCaller calls a quant sidecar tool and returns its raw JSON output.
type QuantToolCaller func(ctx context.Context, toolName string, args map[string]interface{}) (json.RawMessage, error)

func callQuantTool(ctx context.Context, caller QuantToolCaller, toolName string, args map[string]interface{}) (map[string]interface{}, error) {
	if caller == nil {
		return nil, fmt.Errorf("quant tool caller not available")
	}
	out, err := caller(ctx, toolName, args)
	if err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		// If it's not a JSON object, wrap it
		return map[string]interface{}{"raw": string(out)}, nil
	}
	return result, nil
}

func handlePortfolio(w http.ResponseWriter, r *http.Request, caller QuantToolCaller) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, err := callQuantTool(ctx, caller, "quant.global_portfolio", nil)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleAccounts(w http.ResponseWriter, r *http.Request, caller QuantToolCaller) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, err := callQuantTool(ctx, caller, "quant.account_status", nil)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleRisk(w http.ResponseWriter, r *http.Request, caller QuantToolCaller) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, err := callQuantTool(ctx, caller, "quant.global_risk_status", nil)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleTradingPause(w http.ResponseWriter, r *http.Request, caller QuantToolCaller) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, err := callQuantTool(ctx, caller, "quant.pause_trading", nil)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleTradingResume(w http.ResponseWriter, r *http.Request, caller QuantToolCaller) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, err := callQuantTool(ctx, caller, "quant.resume_trading", nil)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleAccountPause(w http.ResponseWriter, r *http.Request, caller QuantToolCaller) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/accounts/")
	id = strings.TrimSuffix(id, "/pause")
	if id == "" {
		http.Error(w, `{"error":"account id is required"}`, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, err := callQuantTool(ctx, caller, "quant.account_pause", map[string]interface{}{"account_id": id})
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleAccountResume(w http.ResponseWriter, r *http.Request, caller QuantToolCaller) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/accounts/")
	id = strings.TrimSuffix(id, "/resume")
	if id == "" {
		http.Error(w, `{"error":"account id is required"}`, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, err := callQuantTool(ctx, caller, "quant.account_resume", map[string]interface{}{"account_id": id})
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleAccountCloseAll(w http.ResponseWriter, r *http.Request, caller QuantToolCaller) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/accounts/")
	id = strings.TrimSuffix(id, "/close-all")
	if id == "" {
		http.Error(w, `{"error":"account id is required"}`, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := callQuantTool(ctx, caller, "quant.account_close_all", map[string]interface{}{"account_id": id})
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handlePositionClose(w http.ResponseWriter, r *http.Request, caller QuantToolCaller) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/positions/")
	id = strings.TrimSuffix(id, "/close")
	if id == "" {
		http.Error(w, `{"error":"position id is required"}`, http.StatusBadRequest)
		return
	}
	// Parse account_id and symbol from id: "account_id:symbol"
	parts := strings.SplitN(id, ":", 2)
	if len(parts) != 2 {
		http.Error(w, `{"error":"invalid position id format, expected account_id:symbol"}`, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := callQuantTool(ctx, caller, "quant.force_close", map[string]interface{}{
		"account_id": parts[0],
		"symbol":     parts[1],
	})
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
