package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/cmd/brain/term"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

type State struct {
	Mode         env.PermissionMode
	Messages     []llm.Message
	TurnCount    int
	Registry     tool.Registry
	Opts         loop.RunOptions
	Cfg          *config.Config
	BrainID      string
	Env          *env.Environment
	KB           *term.Keybindings
	Sandbox      *tool.Sandbox
	SandboxCfg   *tool.SandboxConfig
	Orchestrator *kernel.Orchestrator

	CancelRun context.CancelFunc
	CancelGen uint64
	CancelMu  sync.Mutex

	InputQueue []string
	InputMu    sync.Mutex

	ApprovalCh chan env.ApprovalRequest
	RunTimeout time.Duration

	PlanResumeAfterRun bool

	SessionApprovedTools   map[string]bool
	ApprovedMu             sync.Mutex
	SessionApprovedSandbox map[string]bool
}

func (s *State) SetCancelRun(cancel context.CancelFunc) uint64 {
	s.CancelMu.Lock()
	defer s.CancelMu.Unlock()
	s.CancelGen++
	s.CancelRun = cancel
	return s.CancelGen
}

func (s *State) ClearCancelRun(gen uint64) {
	s.CancelMu.Lock()
	defer s.CancelMu.Unlock()
	if s.CancelGen == gen {
		s.CancelRun = nil
	}
}

func (s *State) CancelCurrentRun() bool {
	s.CancelMu.Lock()
	cancel := s.CancelRun
	s.CancelRun = nil
	s.CancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.ClearQueue()
	return cancel != nil
}

func (s *State) RequestApproval(ctx context.Context, req env.ApprovalRequest) bool {
	if s.ApprovalCh == nil {
		return false
	}
	req.AnswerCh = make(chan bool, 1)

	select {
	case s.ApprovalCh <- req:
	case <-ctx.Done():
		return false
	}

	select {
	case answer := <-req.AnswerCh:
		return answer
	case <-ctx.Done():
		return false
	}
}

func (s *State) Enqueue(msg string) {
	s.InputMu.Lock()
	defer s.InputMu.Unlock()
	s.InputQueue = append(s.InputQueue, msg)
}

func (s *State) Dequeue() string {
	s.InputMu.Lock()
	defer s.InputMu.Unlock()
	if len(s.InputQueue) == 0 {
		return ""
	}
	msg := s.InputQueue[0]
	s.InputQueue = s.InputQueue[1:]
	return msg
}

func (s *State) ClearQueue() {
	s.InputMu.Lock()
	defer s.InputMu.Unlock()
	s.InputQueue = nil
}

func (s *State) QueueLen() int {
	s.InputMu.Lock()
	defer s.InputMu.Unlock()
	return len(s.InputQueue)
}

func (s *State) QueueSnapshot() []string {
	s.InputMu.Lock()
	defer s.InputMu.Unlock()
	out := make([]string, len(s.InputQueue))
	copy(out, s.InputQueue)
	return out
}

func (s *State) QueueDisplayLines() []string {
	queue := s.QueueSnapshot()
	if len(queue) == 0 {
		return nil
	}

	limit := len(queue)
	if limit > 3 {
		limit = 3
	}

	lines := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		line := queuePreview(queue[i])
		if i == 0 {
			line = "Queued follow-up messages: " + line
		}
		if i == limit-1 && len(queue) > limit {
			line += fmt.Sprintf(" (+%d more)", len(queue)-limit)
		}
		lines = append(lines, line)
	}
	return lines
}

func queuePreview(msg string) string {
	return strings.Join(strings.Fields(msg), " ")
}

func (s *State) IsToolSessionApproved(toolName string) bool {
	s.ApprovedMu.Lock()
	defer s.ApprovedMu.Unlock()
	return s.SessionApprovedTools[toolName]
}

func (s *State) ApproveToolForSession(toolName string) {
	s.ApprovedMu.Lock()
	defer s.ApprovedMu.Unlock()
	if s.SessionApprovedTools == nil {
		s.SessionApprovedTools = make(map[string]bool)
	}
	s.SessionApprovedTools[toolName] = true
}

func (s *State) IsSandboxEscapeApproved(dir string) bool {
	s.ApprovedMu.Lock()
	defer s.ApprovedMu.Unlock()
	return s.SessionApprovedSandbox[dir]
}

func (s *State) ApproveSandboxEscapeForSession(dir string) {
	s.ApprovedMu.Lock()
	defer s.ApprovedMu.Unlock()
	if s.SessionApprovedSandbox == nil {
		s.SessionApprovedSandbox = make(map[string]bool)
	}
	s.SessionApprovedSandbox[dir] = true
}

func (s *State) SwitchMode(m env.PermissionMode) {
	s.Mode = m
	s.Registry = tool.NewMemRegistry()
	RegisterToolsForMode(s.Registry, m, s.BrainID, s.Env, s.RequestApproval)

	deps.RegisterDelegateTool(s.Registry, s.Orchestrator, s.Env)
	deps.RegisterBridgeTools(s.Registry, s.Orchestrator)

	if s.Orchestrator != nil {
		s.Registry.Register(NewBrainManageTool(s.Orchestrator))
	}

	s.Registry = filterRegistryWithConfig(s.Registry, s.Cfg, toolpolicy.ToolScopesForChat(s.BrainID, string(m))...)
	s.Opts.Tools = BuildToolSchemas(s.Registry)

	prompt := BuildSystemPrompt(m, s.Sandbox)
	if s.Orchestrator != nil {
		prompt += BuildOrchestratorPrompt(s.Orchestrator, s.Registry)
	}
	s.Opts.System = []llm.SystemBlock{
		{Text: prompt, Cache: true},
	}
}
