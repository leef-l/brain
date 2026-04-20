package sidecar

import (
	"context"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/protocol"
)

// ProgressEvent 是 sidecar→host 的细粒度进度通知。
// Kind 典型值:tool_start / tool_end / turn / content。
type ProgressEvent struct {
	Kind     string `json:"kind"`
	BrainKind string `json:"brain_kind,omitempty"`
	ToolName string `json:"tool_name,omitempty"`
	Args     string `json:"args,omitempty"`
	Detail   string `json:"detail,omitempty"`
	OK       bool   `json:"ok,omitempty"`
	Message  string `json:"message,omitempty"`
	SentAt   int64  `json:"sent_at"`
}

var (
	progressMu     sync.RWMutex
	progressCaller KernelCaller
	progressBrain  string
)

// SetProgressContext 在 SetKernelCaller 时调,记录当前 sidecar 的
// caller + brain 名,后续所有 Emit 共享这两个值。
func SetProgressContext(caller KernelCaller, brainKind string) {
	progressMu.Lock()
	defer progressMu.Unlock()
	progressCaller = caller
	progressBrain = brainKind
}

// EmitProgress 发送一条进度事件给 kernel。failure-tolerant:caller 没装
// 或 RPC 失败只返回 false,不打断业务。
func EmitProgress(ctx context.Context, ev ProgressEvent) bool {
	progressMu.RLock()
	caller := progressCaller
	brain := progressBrain
	progressMu.RUnlock()
	if caller == nil {
		return false
	}
	if ev.BrainKind == "" {
		ev.BrainKind = brain
	}
	ev.SentAt = time.Now().UnixMilli()
	// best-effort notify:不等 response,失败静默
	_ = caller.CallKernel(ctx, protocol.MethodBrainProgress, ev, nil)
	return true
}
