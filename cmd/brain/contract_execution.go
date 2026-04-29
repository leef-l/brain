package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/protocol"
)

type contractExecuteRequest struct {
	BrainKind    string          `json:"brain_kind"`
	ContractKind string          `json:"contract_kind"`
	ContextJSON  json.RawMessage `json:"context_json"`
	Instruction  string          `json:"instruction,omitempty"`
	MaxTurns     int             `json:"max_turns,omitempty"`
	Timeout      string          `json:"timeout,omitempty"`
	Stream       bool            `json:"stream,omitempty"`
}

type contractExecuteResponse struct {
	Status  string          `json:"status"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
	Turns   int             `json:"turns,omitempty"`
	Summary string          `json:"summary,omitempty"`
}

func handleContractExecute(w http.ResponseWriter, r *http.Request, mgr *runManager, cfg *brainConfig) {
	start := time.Now()
	var req contractExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	if req.BrainKind == "" {
		req.BrainKind = "easymvp"
	}
	if req.ContractKind == "" {
		http.Error(w, `{"error":"contract_kind is required"}`, http.StatusBadRequest)
		return
	}
	if req.MaxTurns <= 0 {
		req.MaxTurns = 6
	}

	stream := req.Stream || r.URL.Query().Get("stream") == "true"

	timeout := 30 * time.Second
	if req.Timeout != "" {
		if d, err := time.ParseDuration(req.Timeout); err == nil {
			timeout = d
		}
	}

	if mgr.pool == nil {
		http.Error(w, `{"error":"brain pool not available"}`, http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	fmt.Fprintf(os.Stderr, "contract_execute: [%s] kind=%s contract=%s stream=%v timeout=%v start\n",
		req.Instruction[:min(len(req.Instruction), 40)], req.BrainKind, req.ContractKind, stream, timeout)

	ag, err := mgr.pool.GetBrain(ctx, agent.Kind(req.BrainKind))
	if err != nil {
		fmt.Fprintf(os.Stderr, "contract_execute: getBrain failed kind=%s err=%v (elapsed=%v)\n", req.BrainKind, err, time.Since(start))
		http.Error(w, fmt.Sprintf(`{"error":"get brain %s: %s"}`, req.BrainKind, err), http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintf(os.Stderr, "contract_execute: getBrain ok kind=%s (elapsed=%v)\n", req.BrainKind, time.Since(start))

	rpcAgent, ok := ag.(agent.RPCAgent)
	if !ok {
		http.Error(w, `{"error":"agent does not support RPC"}`, http.StatusInternalServerError)
		return
	}

	rpc, ok := rpcAgent.RPC().(protocol.BidirRPC)
	if !ok {
		http.Error(w, `{"error":"agent RPC is not BidirRPC"}`, http.StatusInternalServerError)
		return
	}

	executionID := fmt.Sprintf("exec-%d", time.Now().UnixNano())

	// Register reverse-RPC handlers so sidecar can call llm.complete / llm.stream.
	llmProxy := &kernel.LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider {
			session, err := openConfiguredProvider(cfg, string(kind), nil, "", "", "", "")
			if err != nil {
				fmt.Fprintf(os.Stderr, "contract_execute: openConfiguredProvider(%s) failed: %v\n", kind, err)
				return nil
			}
			return session.Provider
		},
		EventBus:    mgr.eventBus,
		ExecutionID: executionID,
	}
	llmProxy.RegisterHandlers(rpc, agent.Kind(req.BrainKind))

	// Register brain/progress handler if not already present so sidecar
	// progress events (tool_start / tool_end / content) are forwarded
	// to the unified event bus.
	if !rpc.HandlerExists(protocol.MethodBrainProgress) {
		rpc.Handle(protocol.MethodBrainProgress, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			if mgr.eventBus == nil {
				return map[string]string{"ok": "1"}, nil
			}
			var ev struct {
				Kind        string `json:"kind"`
				ExecutionID string `json:"execution_id,omitempty"`
				ToolName    string `json:"tool_name,omitempty"`
				Message     string `json:"message,omitempty"`
				Detail      string `json:"detail,omitempty"`
				OK          bool   `json:"ok,omitempty"`
			}
			if err := json.Unmarshal(params, &ev); err != nil || ev.ExecutionID == "" {
				return map[string]string{"ok": "1"}, nil
			}
			evType := "agent.progress"
			switch ev.Kind {
			case "tool_start":
				evType = "agent.tool_start"
			case "tool_end":
				evType = "agent.tool_end"
			case "turn":
				evType = "agent.turn"
			case "content", "llm_delta":
				evType = "llm.content_delta"
			case "llm_start":
				evType = "llm.message_start"
			case "llm_end":
				evType = "llm.message_end"
			case "tool_call_delta":
				evType = "llm.tool_call_delta"
			}
			data, _ := json.Marshal(map[string]interface{}{
				"tool_name": ev.ToolName,
				"message":   ev.Message,
				"detail":    ev.Detail,
				"ok":        ev.OK,
			})
			mgr.eventBus.Publish(ctx, events.Event{
				ExecutionID: ev.ExecutionID,
				Type:        evType,
				Data:        data,
			})
			return map[string]string{"ok": "1"}, nil
		})
	}

	payload := map[string]interface{}{
		"instruction":  buildContractInstruction(req.ContractKind, req.Instruction),
		"context":      req.ContextJSON,
		"execution_id": executionID,
	}
	if req.MaxTurns > 0 {
		payload["budget"] = map[string]interface{}{"max_turns": req.MaxTurns}
	}

	if !stream {
		var execResult json.RawMessage
		if err := rpc.Call(ctx, "brain/execute", payload, &execResult); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"brain/execute failed: %s"}`, err), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(os.Stderr, "contract_execute: sidecar raw response: %s\n", string(execResult))

		var resp contractExecuteResponse
		if err := json.Unmarshal(execResult, &resp); err != nil {
			resp = contractExecuteResponse{
				Status:  "ok",
				Result:  execResult,
				Summary: string(execResult),
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// ---- SSE streaming mode ----
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, subCancel := mgr.eventBus.Subscribe(r.Context(), executionID)
	defer subCancel()

	if mgr.eventBus != nil {
		mgr.eventBus.Publish(r.Context(), events.Event{
			ExecutionID: executionID,
			Type:        "execution.started",
			Data:        json.RawMessage(fmt.Sprintf(`{"execution_id":"%s"}`, executionID)),
		})
	}

	fmt.Fprintf(os.Stderr, "contract_execute: [%s] SSE loop starting id=%s (elapsed=%v)\n",
		req.Instruction[:min(len(req.Instruction), 40)], executionID, time.Since(start))

	done := make(chan struct{})
	var execResult json.RawMessage
	var execErr error
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "contract_execute: goroutine panic: %v\n", r)
				execErr = fmt.Errorf("internal panic: %v", r)
				if mgr.eventBus != nil {
					mgr.eventBus.Publish(ctx, events.Event{
						ExecutionID: executionID,
						Type:        "execution.error",
						Data:        json.RawMessage(fmt.Sprintf(`{"error":"internal panic: %v"}`, r)),
					})
				}
			}
			close(done)
		}()
		fmt.Fprintf(os.Stderr, "contract_execute: calling brain/execute id=%s\n", executionID)
		execErr = rpc.Call(ctx, "brain/execute", payload, &execResult)
		fmt.Fprintf(os.Stderr, "contract_execute: brain/execute returned id=%s err=%v len=%d (elapsed=%v)\n",
			executionID, execErr, len(execResult), time.Since(start))
		if mgr.eventBus == nil {
			return
		}
		if execErr != nil {
			status := "execution.error"
			if ctx.Err() == context.Canceled {
				status = "execution.cancelled"
			}
			mgr.eventBus.Publish(ctx, events.Event{
				ExecutionID: executionID,
				Type:        status,
				Data:        json.RawMessage(fmt.Sprintf(`{"error":%q}`, execErr.Error())),
			})
		} else {
			fmt.Fprintf(os.Stderr, "contract_execute: sidecar raw response: %s\n", string(execResult))
			mgr.eventBus.Publish(ctx, events.Event{
				ExecutionID: executionID,
				Type:        "execution.done",
				Data:        execResult,
			})
		}
	}()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				fmt.Fprintf(os.Stderr, "contract_execute: [%s] SSE channel closed id=%s (elapsed=%v)\n",
					req.Instruction[:min(len(req.Instruction), 40)], executionID, time.Since(start))
				return
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-done:
			fmt.Fprintf(os.Stderr, "contract_execute: [%s] SSE done signal id=%s (elapsed=%v)\n",
				req.Instruction[:min(len(req.Instruction), 40)], executionID, time.Since(start))
			// Drain all remaining buffered events before closing the stream.
			// Non-blocking fast drain first, then wait for final events.
			for {
				select {
				case ev, ok := <-ch:
					if !ok {
						fmt.Fprintf(os.Stderr, "contract_execute: [%s] SSE drained+closed id=%s (elapsed=%v)\n",
							req.Instruction[:min(len(req.Instruction), 40)], executionID, time.Since(start))
						return
					}
					data, _ := json.Marshal(ev)
					fmt.Fprintf(w, "data: %s\n\n", data)
					flusher.Flush()
				default:
					// Buffer empty — wait briefly for any in-flight final events.
					select {
					case ev, ok := <-ch:
						if !ok {
							fmt.Fprintf(os.Stderr, "contract_execute: [%s] SSE drained+closed id=%s (elapsed=%v)\n",
								req.Instruction[:min(len(req.Instruction), 40)], executionID, time.Since(start))
							return
						}
						data, _ := json.Marshal(ev)
						fmt.Fprintf(w, "data: %s\n\n", data)
						flusher.Flush()
					case <-time.After(500 * time.Millisecond):
						fmt.Fprintf(os.Stderr, "contract_execute: [%s] SSE grace period ended id=%s (elapsed=%v)\n",
							req.Instruction[:min(len(req.Instruction), 40)], executionID, time.Since(start))
						return
					}
				}
			}
		case <-r.Context().Done():
			fmt.Fprintf(os.Stderr, "contract_execute: [%s] SSE client disconnected id=%s (elapsed=%v)\n",
				req.Instruction[:min(len(req.Instruction), 40)], executionID, time.Since(start))
			return
		}
	}
}

func buildContractInstruction(contractKind, instruction string) string {
	if instruction != "" {
		return fmt.Sprintf("[contract:%s] %s", contractKind, instruction)
	}
	return fmt.Sprintf("[contract:%s] Execute the requested domain contract and return only the final contract envelope JSON.", contractKind)
}
