package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/cmd/brain/agentpipe"
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/cmd/brain/term"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/persistence"
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

	// PlanRunner 是 IntentProject 触发的项目级执行器(七阶段闭环)。
	// 跟 chat session 同生命周期,首次需要时懒构造,跨 turn 复用 Memory/Patterns/Lessons。
	// 每个 chat session 独立,避免多 session 串状态。
	//
	// 并发安全:chat REPL 队列允许并发 launchRun(repl.go follow-up 路径),
	// 多个 IntentProject goroutine 同时进入 runChatPlanFlow 时
	// "if PlanRunner==nil { PlanRunner=... }" 是非原子读-改-写 → race。
	// 用 PlanRunnerOnce 做单次构造,确保第一个 caller 才真正分配。
	PlanRunner     *agentpipe.PlanRunner
	PlanRunnerOnce sync.Once

	// 跨 turn 共享的 loop 状态检测器。chat 模式所有 turn 复用同一组实例,
	// 否则 LoopDetector 每 turn 重建 → 永远空状态 → 检测失效。
	//
	// 在 NewState 时一次性构造,runChatTurn 把它们透传给 agentpipe.Invocation。
	// run/serve 单 prompt 模式不需要,Invocation hooks 留空走默认 Mem* 即可。
	Sanitizer    loop.ToolResultSanitizer
	LoopDetector loop.LoopDetector
	CacheBuilder loop.CacheBuilder

	// ActiveRuns 管理所有正在执行的 run。
	// key 是 runID（如 "run-0", "run-1"）。
	ActiveRuns map[string]*RunHandle
	RunsMu     sync.Mutex
	NextRunID  int

	InputQueue []string
	InputMu    sync.Mutex

	ApprovalCh chan env.ApprovalRequest
	RunTimeout time.Duration

	PlanResumeAfterRun bool

	SessionApprovedTools   map[string]bool
	ApprovedMu             sync.Mutex
	SessionApprovedSandbox map[string]bool

	// Human takeover coordinator for this chat session(browser/其他 brain
	// 遇到需要人工的场景时,工具会阻塞在这里等 /resume 或 /abort)。
	HumanCoord *ChatHumanCoordinator

	// ─── 项目级持久化(MACCS Wave 7+ 多项目管理) ───────────────────────
	//
	// chat 启动时由 project_picker 决定:
	//   - 选了已有项目 → CurrentProject 非 nil,CurrentWorkdir = project.Workdir
	//   - 新建项目     → 同上,新创建并 last_active_at = now
	//   - 跳过("无项目"模式) → CurrentProject = nil,IsNoProject = true
	//
	// 每次 turn 后(executor.go::persistChatTurn 处)调:
	//   - ProjectStore.SaveMessages(CurrentProject.ID, [user, assistant])
	//   - ProjectsStore.UpdateLastActive(CurrentProject.ID, now)
	//
	// ContextEngine 的 AssembleRequest.ProjectID = CurrentProject.ID,
	// 自动加载历史 + 注入项目记忆摘要。
	ProjectsStore      persistence.ProjectsStore
	ProjectStore       persistence.ProjectStore  // 对话历史
	ProjectMemoryStore persistence.ProjectMemoryStore  // 项目记忆 entries
	CurrentProject     *persistence.ProjectMeta
	CurrentWorkdir     string  // 通常 = CurrentProject.Workdir,无项目模式时 = os.Getwd()
	IsNoProject        bool    // true: 无项目模式,本次对话不持久化
}

// RunHandle 代表一个正在执行的 run。
type RunHandle struct {
	ID        string
	Cancel    context.CancelFunc
	Input     string
	StartedAt time.Time
}

// StartRun 注册一个新的 run，返回分配的 runID。
func (s *State) StartRun(input string, cancel context.CancelFunc) string {
	s.RunsMu.Lock()
	defer s.RunsMu.Unlock()
	id := fmt.Sprintf("run-%d", s.NextRunID)
	s.NextRunID++
	if s.ActiveRuns == nil {
		s.ActiveRuns = make(map[string]*RunHandle)
	}
	s.ActiveRuns[id] = &RunHandle{
		ID:        id,
		Cancel:    cancel,
		Input:     input,
		StartedAt: time.Now(),
	}
	return id
}

// CancelRun 取消指定 runID 的任务。
func (s *State) CancelRun(id string) bool {
	s.RunsMu.Lock()
	h, ok := s.ActiveRuns[id]
	s.RunsMu.Unlock()
	if ok && h.Cancel != nil {
		h.Cancel()
		return true
	}
	return false
}

// CancelAllRuns 取消所有正在执行的 run。
func (s *State) CancelAllRuns() bool {
	s.RunsMu.Lock()
	handles := make([]*RunHandle, 0, len(s.ActiveRuns))
	for _, h := range s.ActiveRuns {
		handles = append(handles, h)
	}
	s.RunsMu.Unlock()

	canceledAny := false
	for _, h := range handles {
		if h.Cancel != nil {
			h.Cancel()
			canceledAny = true
		}
	}
	return canceledAny
}

// RemoveRun 从 ActiveRuns 中移除已完成的 run。
func (s *State) RemoveRun(id string) {
	s.RunsMu.Lock()
	delete(s.ActiveRuns, id)
	s.RunsMu.Unlock()
}

// AnyRunning 返回是否有正在执行的 run。
func (s *State) AnyRunning() bool {
	s.RunsMu.Lock()
	defer s.RunsMu.Unlock()
	return len(s.ActiveRuns) > 0
}

// RunningCount 返回正在执行的 run 数量。
func (s *State) RunningCount() int {
	s.RunsMu.Lock()
	defer s.RunsMu.Unlock()
	return len(s.ActiveRuns)
}

// LatestRunID 返回最新（编号最大）的 runID，如果没有则返回空字符串。
func (s *State) LatestRunID() string {
	s.RunsMu.Lock()
	defer s.RunsMu.Unlock()
	if len(s.ActiveRuns) == 0 {
		return ""
	}
	// 由于 runID 是 run-N 格式，找最大的 N
	latest := ""
	maxN := -1
	for id := range s.ActiveRuns {
		var n int
		fmt.Sscanf(id, "run-%d", &n)
		if n > maxN {
			maxN = n
			latest = id
		}
	}
	return latest
}

// ActiveRunIDs 返回所有正在执行的 runID 列表（按编号排序）。
func (s *State) ActiveRunIDs() []string {
	s.RunsMu.Lock()
	defer s.RunsMu.Unlock()
	ids := make([]string, 0, len(s.ActiveRuns))
	for id := range s.ActiveRuns {
		ids = append(ids, id)
	}
	return ids
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

// Close 释放 State 持有的会话级资源，应在 chat REPL 退出时 defer 调用。
//
// 关闭顺序:
//   - PlanRunner.Close() 取消 bgCtx → consumeFeedbackRequests goroutine 退出,
//     避免 chat 退出后该 goroutine 持续订阅 EventBus 造成永久泄漏。
//   - RemovePlanRegistry 清 chat /plan slash 命令的注册表。
func (s *State) Close() {
	if s.PlanRunner != nil {
		s.PlanRunner.Close()
	}
	RemovePlanRegistry(s)
}

func (s *State) SwitchMode(m env.PermissionMode) {
	s.Mode = m
	s.Registry = tool.NewMemRegistry()
	RegisterToolsForMode(s.Registry, m, s.BrainID, s.Env, s.RequestApproval)

	deps.RegisterDelegateTool(s.Registry, s.Orchestrator, s.Env)
	deps.RegisterBridgeTools(s.Registry, s.Orchestrator)
	if deps.RegisterWorkflowTool != nil && s.Orchestrator != nil {
		deps.RegisterWorkflowTool(s.Registry, s.Orchestrator)
	}

	if s.Orchestrator != nil {
		s.Registry.Register(tool.WrapWithFailureLog(NewBrainManageTool(s.Orchestrator)))
		s.Registry.Register(tool.WrapWithFailureLog(NewStartHumanDemoTool(s.Orchestrator, s.Env, s.HumanCoord)))
		// MACCS 智能编排：暴露元认知查询给中央大脑（complexity / memory / pattern /
		// brain_status / budget），让 LLM 自己决定如何拆分任务，而不是硬编码 prompt 规则。
		s.Registry.Register(tool.WrapWithFailureLog(NewMetacognitionTool(s.Orchestrator)))
	}

	// brain.memory_recall 让 LLM 查询 ~/.brain/brain.db 里的 ui_patterns /
	// human_demo_sequences / learning_profiles,用户问"读取上下文记忆 / 你
	// 学过什么 / 有没有这个站的 pattern"时直接调这个而不是去 workdir 找
	// memory/*.md 文件。store 可能是 nil(mock provider / 无持久化场景),
	// lib 也可能 nil,工具内部对 nil 安全,返回空列表不报错。
	if rt, _ := deps.NewDefaultCLIRuntime(s.BrainID); rt != nil && rt.Stores != nil {
		s.Registry.Register(tool.WrapWithFailureLog(tool.NewMemoryRecallTool(rt.Stores.LearningStore, tool.SharedPatternLibrary())))
	} else {
		s.Registry.Register(tool.WrapWithFailureLog(tool.NewMemoryRecallTool(nil, tool.SharedPatternLibrary())))
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
