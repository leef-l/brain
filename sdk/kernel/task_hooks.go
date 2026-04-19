package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/events"
)

// Task #19 — TaskExecution hook 系统:外部进程钩子。
//
// 设计对标 Claude Code hooks:
//   - TaskExecution.Transition 已经把状态转换发到 EventBus(见 execution.go)
//   - 本文件实现一个 HookRunner,订阅 bus,按 ~/.brain/hooks.json 配置
//     执行外部 shell 命令
//
// 配置示例 (~/.brain/hooks.json):
//   {
//     "hooks": [
//       {
//         "on": "task.state.completed",
//         "command": "logger -t brain 'task $EXECUTION_ID done'"
//       },
//       {
//         "on": "task.state.failed",
//         "brain": "browser",
//         "command": "/usr/local/bin/notify-fail.sh $EXECUTION_ID"
//       }
//     ]
//   }
//
// 钩子只传递环境变量(EXECUTION_ID / BRAIN_ID / FROM / TO / EVENT_TYPE),
// 不把完整 payload 塞 stdin——防止 prompt injection 链路从外部程序回灌。

// HookSpec 是单条 hook 的定义。
type HookSpec struct {
	// On 是事件类型过滤。精确匹配,如 "task.state.completed"。
	// "*" 代表所有 task.state.* 事件。
	On string `json:"on"`

	// Brain 可选,只匹配特定 brain_id 产生的事件。空字符串匹配所有。
	Brain string `json:"brain,omitempty"`

	// Command 是要执行的 shell 命令。通过 /bin/sh -c 启动,环境变量注入
	// EXECUTION_ID / BRAIN_ID / FROM / TO / EVENT_TYPE。
	Command string `json:"command"`

	// TimeoutMS 可选,执行超时,默认 10000(10s)。超时强制 kill。
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// HookConfig 是 hooks.json 的顶层结构。
type HookConfig struct {
	Hooks []HookSpec `json:"hooks,omitempty"`
}

// DefaultHookConfigPath 返回 ~/.brain/hooks.json。
func DefaultHookConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "hooks.json"
	}
	return filepath.Join(home, ".brain", "hooks.json")
}

// LoadHookConfig 读取并解析配置。文件不存在返回空配置,不是错误。
func LoadHookConfig(path string) (*HookConfig, error) {
	if path == "" {
		path = DefaultHookConfigPath()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &HookConfig{}, nil
		}
		return nil, err
	}
	var cfg HookConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i, h := range cfg.Hooks {
		if h.On == "" {
			return nil, fmt.Errorf("hooks[%d]: on is required", i)
		}
		if strings.TrimSpace(h.Command) == "" {
			return nil, fmt.Errorf("hooks[%d]: command is required", i)
		}
	}
	return &cfg, nil
}

// HookRunner 订阅 EventBus,按配置执行 shell hooks。
type HookRunner struct {
	bus    events.Subscriber
	cfg    *HookConfig
	logger func(string, ...interface{})

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewHookRunner 创建一个 runner。logger 为 nil 时用 stderr。
func NewHookRunner(bus events.Subscriber, cfg *HookConfig, logger func(string, ...interface{})) *HookRunner {
	if logger == nil {
		logger = func(f string, args ...interface{}) {
			fmt.Fprintf(os.Stderr, "[hook-runner] "+f+"\n", args...)
		}
	}
	if cfg == nil {
		cfg = &HookConfig{}
	}
	return &HookRunner{bus: bus, cfg: cfg, logger: logger}
}

// Start 启动订阅。重复 Start 是 no-op。
func (r *HookRunner) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return nil
	}
	if r.bus == nil || len(r.cfg.Hooks) == 0 {
		r.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	ch, unsub := r.bus.Subscribe(runCtx, "")
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer unsub()
		for {
			select {
			case <-runCtx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				r.dispatch(runCtx, ev)
			}
		}
	}()
	return nil
}

// Stop 停止订阅并等待 in-flight hooks 退出。
func (r *HookRunner) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.wg.Wait()
}

// dispatch 匹配事件到 hooks,顺序执行。外部命令失败不阻塞后续 hook。
func (r *HookRunner) dispatch(ctx context.Context, ev events.Event) {
	for _, h := range r.cfg.Hooks {
		if !hookMatches(h, ev) {
			continue
		}
		if err := r.runHook(ctx, h, ev); err != nil {
			r.logger("hook %q on %s failed: %v", h.Command, ev.Type, err)
		}
	}
}

func hookMatches(h HookSpec, ev events.Event) bool {
	if h.On != "*" && h.On != ev.Type {
		return false
	}
	if h.Brain == "" {
		return true
	}
	// 从 Data 里读 brain_id
	var data struct {
		BrainID string `json:"brain_id"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return false
	}
	return data.BrainID == h.Brain
}

// runHook 在隔离的 /bin/sh -c 子进程里执行 Command,注入事件相关环境变量。
func (r *HookRunner) runHook(parent context.Context, h HookSpec, ev events.Event) error {
	timeout := time.Duration(h.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var data struct {
		ExecutionID string `json:"execution_id"`
		BrainID     string `json:"brain_id"`
		From        string `json:"from"`
		To          string `json:"to"`
	}
	_ = json.Unmarshal(ev.Data, &data)

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", h.Command)
	cmd.Env = append(os.Environ(),
		"EVENT_TYPE="+ev.Type,
		"EXECUTION_ID="+data.ExecutionID,
		"BRAIN_ID="+data.BrainID,
		"FROM="+data.From,
		"TO="+data.To,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		r.logger("hook exit: %s → %v\n%s", h.Command, err, strings.TrimSpace(string(out)))
		return err
	}
	return nil
}
