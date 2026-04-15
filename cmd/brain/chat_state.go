package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/tool"
)

// chatState holds the mutable state shared between the main loop and
// background goroutines during an interactive chat session.
type chatState struct {
	mode         chatMode
	messages     []llm.Message
	turnCount    int
	registry     tool.Registry
	opts         loop.RunOptions
	cfg          *brainConfig
	brainID      string
	env          *executionEnvironment
	kb           *keybindings
	sandbox      *tool.Sandbox
	sandboxCfg   *tool.SandboxConfig
	orchestrator *kernel.Orchestrator // nil = solo mode (no specialist brains)

	// cancelRun cancels the current AI execution (if any).
	cancelRun context.CancelFunc
	cancelGen uint64 // generation counter for safe cancel tracking
	cancelMu  sync.Mutex

	// inputQueue holds user messages typed during AI execution.
	inputQueue []string
	inputMu    sync.Mutex

	approvalCh chan approvalRequest
	runTimeout time.Duration
}

// setCancelRun stores a new cancel function and returns a generation token.
func (s *chatState) setCancelRun(cancel context.CancelFunc) uint64 {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	s.cancelGen++
	s.cancelRun = cancel
	return s.cancelGen
}

// clearCancelRun clears the cancel function only if the generation matches.
func (s *chatState) clearCancelRun(gen uint64) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if s.cancelGen == gen {
		s.cancelRun = nil
	}
}

// cancelCurrentRun cancels the running AI execution and clears the queue.
func (s *chatState) cancelCurrentRun() bool {
	s.cancelMu.Lock()
	cancel := s.cancelRun
	s.cancelRun = nil
	s.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.clearQueue()
	return cancel != nil
}

func (s *chatState) requestApproval(ctx context.Context, req approvalRequest) bool {
	if s.approvalCh == nil {
		return false
	}
	req.answerCh = make(chan bool, 1)

	select {
	case s.approvalCh <- req:
	case <-ctx.Done():
		return false
	}

	select {
	case answer := <-req.answerCh:
		return answer
	case <-ctx.Done():
		return false
	}
}

// enqueue adds a user message to the input queue.
func (s *chatState) enqueue(msg string) {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	s.inputQueue = append(s.inputQueue, msg)
}

// dequeue returns the next queued message, or "" if empty.
func (s *chatState) dequeue() string {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	if len(s.inputQueue) == 0 {
		return ""
	}
	msg := s.inputQueue[0]
	s.inputQueue = s.inputQueue[1:]
	return msg
}

// clearQueue discards all queued messages.
func (s *chatState) clearQueue() {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	s.inputQueue = nil
}

// queueLen returns the number of pending messages.
func (s *chatState) queueLen() int {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return len(s.inputQueue)
}

func (s *chatState) queueSnapshot() []string {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	out := make([]string, len(s.inputQueue))
	copy(out, s.inputQueue)
	return out
}

func (s *chatState) queueDisplayLines() []string {
	queue := s.queueSnapshot()
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

// switchMode rebuilds tools and system prompt for a new mode.
func (s *chatState) switchMode(m chatMode) {
	s.mode = m
	s.registry = tool.NewMemRegistry()
	registerToolsForMode(s.registry, m, s.brainID, s.env, s.requestApproval)

	registerDelegateToolForEnvironment(s.registry, s.orchestrator, s.env)
	registerSpecialistBridgeTools(s.registry, s.orchestrator)

	// Brain management tool — lets the LLM list/start/stop specialist brains.
	if s.orchestrator != nil {
		s.registry.Register(newBrainManageTool(s.orchestrator))
	}

	s.registry = filterRegistryWithConfig(s.registry, s.cfg, toolScopesForChat(s.brainID, m)...)
	s.opts.Tools = buildToolSchemas(s.registry)

	prompt := buildSystemPrompt(m, s.sandbox)
	if s.orchestrator != nil {
		prompt += buildOrchestratorPrompt(s.orchestrator, s.registry)
	}
	s.opts.System = []llm.SystemBlock{
		{Text: prompt, Cache: true},
	}
}
