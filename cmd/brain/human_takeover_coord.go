package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/tool"
)

// Task #16 — 人类接管协调器(主机侧实现,tool 包通过 SetHumanTakeoverCoordinator
// 注入)。复用 brain-v3 已有:
//   - TaskExecution.StatePaused + Transition(见 sdk/kernel/execution.go)
//   - events.EventBus(Task #19 已把 task.state.* 挂上去)
//   - runManager(已经跟踪每个 runEntry 的 taskExec)
//
// 只新增 3 个业务事件:
//   task.human.requested / task.human.resumed / task.human.aborted
// 它们和 task.state.* 同一总线,hook-runner / WebUI 都能订阅。

// hostHumanTakeoverCoordinator 实现 tool.HumanTakeoverCoordinator。
type hostHumanTakeoverCoordinator struct {
	mgr *runManager
	bus events.Publisher

	mu      sync.Mutex
	pending map[string]chan tool.HumanTakeoverResponse // run_id → resume/abort channel
}

func newHostHumanTakeoverCoordinator(mgr *runManager, bus events.Publisher) *hostHumanTakeoverCoordinator {
	return &hostHumanTakeoverCoordinator{
		mgr:     mgr,
		bus:     bus,
		pending: map[string]chan tool.HumanTakeoverResponse{},
	}
}

// RequestTakeover:把对应 TaskExecution 切到 Paused,发 task.human.requested,
// 阻塞直到 /v1/executions/{id}/resume|abort 被调用,或 ctx / timeout 到期。
func (c *hostHumanTakeoverCoordinator) RequestTakeover(ctx context.Context, req tool.HumanTakeoverRequest) tool.HumanTakeoverResponse {
	runID := req.RunID
	if runID == "" {
		return tool.HumanTakeoverResponse{Outcome: tool.HumanOutcomeAborted, Note: "no run_id bound; cannot pause execution"}
	}

	entry := c.lookupEntry(runID)
	if entry == nil {
		return tool.HumanTakeoverResponse{Outcome: tool.HumanOutcomeAborted, Note: "run not found"}
	}

	// 转到 Paused(invalid transition 时直接返回 aborted)
	if entry.taskExec != nil {
		if err := entry.taskExec.Transition(kernel.StatePaused); err != nil {
			return tool.HumanTakeoverResponse{Outcome: tool.HumanOutcomeAborted, Note: "cannot pause: " + err.Error()}
		}
	}

	ch := make(chan tool.HumanTakeoverResponse, 1)
	c.mu.Lock()
	c.pending[runID] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, runID)
		c.mu.Unlock()
	}()

	c.publishHumanEvent("task.human.requested", req, "")

	// timeout 处理
	var timer *time.Timer
	var timerCh <-chan time.Time
	if req.TimeoutSec > 0 {
		timer = time.NewTimer(time.Duration(req.TimeoutSec) * time.Second)
		defer timer.Stop()
		timerCh = timer.C
	}

	select {
	case resp := <-ch:
		// 转回 Running 让 Agent 继续(失败时保持 Paused,让上层处理)
		if entry.taskExec != nil && resp.Outcome == tool.HumanOutcomeResumed {
			_ = entry.taskExec.Transition(kernel.StateRunning)
		}
		eventType := "task.human.resumed"
		if resp.Outcome == tool.HumanOutcomeAborted {
			eventType = "task.human.aborted"
		}
		c.publishHumanEvent(eventType, req, resp.Note)
		return resp
	case <-ctx.Done():
		c.publishHumanEvent("task.human.aborted", req, "agent context canceled")
		return tool.HumanTakeoverResponse{Outcome: tool.HumanOutcomeAborted, Note: "agent context canceled"}
	case <-timerCh:
		c.publishHumanEvent("task.human.aborted", req, "timed out waiting for human")
		return tool.HumanTakeoverResponse{Outcome: tool.HumanOutcomeAborted, Note: "timed out"}
	}
}

// Resume 由 HTTP 端点调用。没人在等就返回 false(404)。
func (c *hostHumanTakeoverCoordinator) Resume(runID, note string) bool {
	return c.deliver(runID, tool.HumanTakeoverResponse{Outcome: tool.HumanOutcomeResumed, Note: note})
}

func (c *hostHumanTakeoverCoordinator) Abort(runID, note string) bool {
	return c.deliver(runID, tool.HumanTakeoverResponse{Outcome: tool.HumanOutcomeAborted, Note: note})
}

func (c *hostHumanTakeoverCoordinator) deliver(runID string, resp tool.HumanTakeoverResponse) bool {
	c.mu.Lock()
	ch, ok := c.pending[runID]
	c.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- resp:
		return true
	default:
		return false
	}
}

func (c *hostHumanTakeoverCoordinator) lookupEntry(runID string) *runEntry {
	v, ok := c.mgr.runs.Load(runID)
	if !ok {
		return nil
	}
	entry, _ := v.(*runEntry)
	return entry
}

func (c *hostHumanTakeoverCoordinator) publishHumanEvent(eventType string, req tool.HumanTakeoverRequest, note string) {
	if c.bus == nil {
		return
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"run_id":      req.RunID,
		"brain_id":    req.BrainKind,
		"reason":      req.Reason,
		"guidance":    req.Guidance,
		"url":         req.URL,
		"timeout_sec": req.TimeoutSec,
		"note":        note,
	})
	c.bus.Publish(context.Background(), events.Event{
		ExecutionID: req.RunID,
		Type:        eventType,
		Timestamp:   time.Now().UTC(),
		Data:        payload,
	})
}

// ---------------------------------------------------------------------------
// HTTP 端点:/v1/executions/{id}/resume  /v1/executions/{id}/abort
// ---------------------------------------------------------------------------

func handleResumeExecution(w http.ResponseWriter, r *http.Request, coord *hostHumanTakeoverCoordinator, id string) {
	body, _ := decodeHumanPayload(r)
	if coord == nil || !coord.Resume(id, body.Note) {
		http.Error(w, "no pending takeover for "+id, http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]string{"status": "resumed"})
}

func handleAbortExecution(w http.ResponseWriter, r *http.Request, coord *hostHumanTakeoverCoordinator, id string) {
	body, _ := decodeHumanPayload(r)
	if coord == nil || !coord.Abort(id, body.Note) {
		http.Error(w, "no pending takeover for "+id, http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]string{"status": "aborted"})
}

type humanResumePayload struct {
	Note string `json:"note,omitempty"`
}

func decodeHumanPayload(r *http.Request) (humanResumePayload, error) {
	var p humanResumePayload
	if r.Body == nil {
		return p, nil
	}
	defer r.Body.Close()
	_ = json.NewDecoder(r.Body).Decode(&p)
	return p, nil
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// matchHumanSubpath 从 {id}/resume|abort 提取出子路径。
func matchHumanSubpath(path string) (id, sub string, ok bool) {
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// 防"引入未使用"提示
var _ = fmt.Sprintf
