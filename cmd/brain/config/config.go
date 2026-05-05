package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

// Config is the on-disk configuration structure.
// JSON format, file: ~/.brain/config.json
type Config struct {
	Mode               string             `json:"mode,omitempty"`
	Endpoint           string             `json:"endpoint,omitempty"`
	DefaultBrain       string             `json:"default_brain,omitempty"`
	DefaultModel       string             `json:"default_model,omitempty"`
	Output             string             `json:"output,omitempty"`
	LogLevel           string             `json:"log_level,omitempty"`
	Diagnostics        *DiagnosticsConfig `json:"diagnostics,omitempty"`
	NoColor            bool               `json:"no_color,omitempty"`
	Timeout            string             `json:"timeout,omitempty"`
	Budget             *BudgetConfig      `json:"default_budget,omitempty"`
	ChatMode           string             `json:"chat_mode,omitempty"`
	PermissionMode     string             `json:"permission_mode,omitempty"`
	ServeWorkdirPolicy string             `json:"serve_workdir_policy,omitempty"`
	APIKey             string             `json:"api_key,omitempty"`
	BaseURL            string             `json:"base_url,omitempty"`
	Model              string             `json:"model,omitempty"`

	Providers      map[string]*ProviderConfig `json:"providers,omitempty"`
	ActiveProvider string                     `json:"active_provider,omitempty"`

	Sandbox    *SandboxCfg      `json:"sandbox,omitempty"`
	FilePolicy *FilePolicyInput `json:"file_policy,omitempty"`

	Brains       []kernel.BrainRegistration `json:"brains,omitempty"`
	RemoteBrains []RemoteBrainEntry         `json:"remote_brains,omitempty"`
	MCPServers   []MCPServerEntry           `json:"mcp_servers,omitempty"`

	ToolProfiles map[string]*ToolProfileConfig `json:"tool_profiles,omitempty"`
	ActiveTools  map[string]string             `json:"active_tools,omitempty"`

	// MACCS 多大脑协同系统配置（v2.2 新增）
	// 所有字段都有合理默认值，整个 maccs 块可以省略；
	// 仅当需要调优 / 关闭某项时显式配置。
	MACCS *MACCSConfig `json:"maccs,omitempty"`
}

// MACCSConfig 是 MACCS 协同系统的运行参数。
// nil 等价于全部使用默认值（与 examples 注释保持一致）。
type MACCSConfig struct {
	// 6.1 HealthManager —— 组件健康监控（GET /v1/health）
	// 默认 enabled=true，注册 BrainPool + LeaseManager 两个 checker。
	Health *MACCSHealthConfig `json:"health,omitempty"`

	// 6.3 PerfCollector —— 性能采样（GET /v1/metrics/perf）
	// 默认 enabled=true。开销极小（map 写入），生产推荐保持开启。
	Perf *MACCSPerfConfig `json:"perf,omitempty"`

	// 6.4 ObservabilityHub —— 调用链 Span（GET /v1/observability）
	// 默认 enabled=true，挂载内存 provider；外部 OTLP/Prometheus 通过代码扩展。
	Observability *MACCSObservConfig `json:"observability,omitempty"`

	// 6.5 SecurityAuditor —— POST /v1/projects 入参注入风险审计
	// 默认 enabled=true，命中 critical/high 拒绝；medium/low 仅日志放行。
	Security *MACCSSecurityConfig `json:"security,omitempty"`

	// 6.6 MultiProjectManager —— 项目级并发槽位 + 配额
	// 默认 max_concurrent=3, queue_size=16；超额返回 429。
	MultiProject *MACCSMultiProjectConfig `json:"multi_project,omitempty"`

	// 5.5 AdaptivePromptManager —— A/B 变体注入 LLMProxy.PromptManager
	// 默认 enabled=true，但需用户通过 RegisterVariant 注入变体后才有实际效果。
	AdaptivePrompt *MACCSAdaptivePromptConfig `json:"adaptive_prompt,omitempty"`

	// 4.2/4.5 ConflictDetector + SmartScheduler —— 冲突感知重排
	// 默认 enabled=true, dry_run=true（仅日志，不实际改变 layer）。
	// 生产观察一周确认无误报后切换 dry_run=false 启用强制重排。
	Conflict *MACCSConflictConfig `json:"conflict,omitempty"`

	// 5.4 PatternExtractor —— ExecuteProject 后异步提取共性模式
	// 默认 enabled=true。需要 ExperienceStore 配合（自动启用 MemExperienceStore）。
	PatternExtractor *MACCSPatternConfig `json:"pattern_extractor,omitempty"`

	// 4.3/4.4 DeadlockDetector + Arbiter —— wait-for graph 死锁检测与仲裁（Wave 7）
	// 默认 enabled=true, dry_run=true（仅 diaglog 警告，不实际中止 victim）。
	// 生产观察一周确认 ConflictDetector 不误报后切换 dry_run=false 启用强制中止。
	Deadlock *MACCSDeadlockConfig `json:"deadlock,omitempty"`
}

type MACCSHealthConfig struct {
	Enabled bool `json:"enabled"`
}

type MACCSPerfConfig struct {
	Enabled bool `json:"enabled"`
}

type MACCSObservConfig struct {
	Enabled bool `json:"enabled"`
}

type MACCSSecurityConfig struct {
	Enabled bool `json:"enabled"`
	// RejectSeverity 决定多严重的发现才拒绝请求；
	// 默认 "high" → critical/high 拒绝, medium/low 仅日志。
	// 可选: "critical" / "high" / "medium" / "low"
	RejectSeverity string `json:"reject_severity,omitempty"`
}

type MACCSMultiProjectConfig struct {
	Enabled       bool `json:"enabled"`
	MaxConcurrent int  `json:"max_concurrent,omitempty"` // 默认 3
	QueueSize     int  `json:"queue_size,omitempty"`     // 默认 16
}

type MACCSAdaptivePromptConfig struct {
	Enabled bool `json:"enabled"`
}

type MACCSConflictConfig struct {
	Enabled bool `json:"enabled"`
	// DryRun=true 时仅日志记录冲突重排建议，不实际改变 layer 顺序；
	// 生产环境观察一周后切换为 false 启用强制重排（路线图风险对策）。
	DryRun bool `json:"dry_run"`
}

type MACCSPatternConfig struct {
	Enabled bool `json:"enabled"`
}

type MACCSDeadlockConfig struct {
	Enabled bool `json:"enabled"`
	// DryRun=true 时仅日志记录死锁仲裁结果，不真正中止 victim 任务；
	// 生产环境观察一周后切换为 false 启用强制中止（路线图 Wave 7 风险对策）。
	DryRun bool `json:"dry_run"`
}

type DiagnosticsConfig struct {
	Enabled    bool     `json:"enabled,omitempty"`
	Categories []string `json:"categories,omitempty"`
	File       string   `json:"file,omitempty"`
	Stderr     bool     `json:"stderr,omitempty"`
	Level      string   `json:"level,omitempty"`
	Format     string   `json:"format,omitempty"`

	// Debug 子开关 — 默认全 false,生产保持关闭。出问题时按需启用,
	// 通过 stderr 打印详细诊断,帮助定位"工具没调用 / 卡住 / 输出截断"
	// 等典型问题。
	Debug *DebugConfig `json:"debug,omitempty"`
}

// DebugConfig 是细粒度调试开关。任一开启 → stderr 打印对应 [debug] 日志。
// 设计原则:每条日志一行 + 关键字段都打,便于 grep。
type DebugConfig struct {
	// Runner=true 时打印每轮 LLM 响应的 stop_reason / tool_use_count / tools /
	// content_blocks / text_chars。用于定位"嘴上承诺但工具调用没发出"类问题。
	Runner bool `json:"runner,omitempty"`

	// LLMRequest=true 时打印每次 ChatRequest 的 model / messages 数 / max_tokens /
	// tools 数。用于排查"请求送出去但响应不对"。
	LLMRequest bool `json:"llm_request,omitempty"`

	// LLMResponse=true 时打印每次 ChatResponse 的 stop_reason / 各 ContentBlock 类型
	// 与长度。用于排查 provider 映射 / 截断问题。
	LLMResponse bool `json:"llm_response,omitempty"`

	// ToolDispatch=true 时打印每个工具调用的 name / args 摘要 / 耗时 / IsError。
	ToolDispatch bool `json:"tool_dispatch,omitempty"`

	// ContextEngine=true 时打印 Assemble 流程:input msgs / 项目记忆字符 / 历史
	// 加载条数 / Compress 阶段。
	ContextEngine bool `json:"context_engine,omitempty"`
}

type SandboxCfg struct {
	Enabled           bool     `json:"enabled"`
	AllowWrite        []string `json:"allow_write,omitempty"`
	DenyRead          []string `json:"deny_read,omitempty"`
	AllowNet          []string `json:"allow_net,omitempty"`
	FailIfUnavailable bool     `json:"fail_if_unavailable,omitempty"`
}

type ProviderConfig struct {
	BaseURL  string            `json:"base_url"`
	APIKey   string            `json:"api_key"`
	Model    string            `json:"model,omitempty"`
	Models   map[string]string `json:"models,omitempty"`
	Protocol string            `json:"protocol,omitempty"`

	// Capabilities 声明此 provider 的能力覆盖(可选)。
	//
	// 已声明的字段覆盖 builtin 表 / 启发式的同名字段,未声明的字段沿用
	// 内置数据。仅在用户明确知道 builtin 数据不准、或接入了 builtin 表
	// 不认识的新 model 时才需要填。
	//
	// 之所以是 json.RawMessage 而非 *llm.CapabilitiesOverride:config
	// 包不依赖 sdk/llm,避免循环 / 反向依赖。Provider 包(cmd/brain/provider)
	// 拿到 ResolvedProvider 后用 llm.CapabilitiesOverride.UnmarshalJSON
	// 反序列化此字段。
	//
	// 示例(deepseek 普通版,显式声明 tool_choice 和 max_parallel_tools):
	//
	//   "capabilities": {
	//     "tool_choice":         "none",
	//     "max_parallel_tools":  4
	//   }
	//
	// 字段说明:
	//   - family:                  自定义 family 名(影响日志/dashboard 显示)
	//   - native_tool_call:        是否原生支持 tool_use 块(几乎全部 = true)
	//   - tool_choice:             "none" / "auto" / "required" / "specific"
	//   - reasoner:                是否思考类模型(影响 grace turn 与 nudge 短消息)
	//   - emits_reasoning_content: 响应是否含 reasoning_content 字段(deepseek-r 等)
	//   - prefers_structured_output: 是否倾向结构化输出(降低 IntentChain 触发率)
	//   - max_parallel_tools:      单轮最大并行工具数(影响 BatchPlanner)
	//
	// 详细文档见 docs/配置参考-capability.md。
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

// RemoteBrainEntry 配置一个远程 brain 连接。
type RemoteBrainEntry struct {
	Kind      string `json:"kind"`
	Endpoint  string `json:"endpoint"`
	APIKey    string `json:"api_key,omitempty"`
	Timeout   string `json:"timeout,omitempty"`
	AutoStart bool   `json:"auto_start,omitempty"`
}

type BudgetConfig struct {
	MaxTurns   int     `json:"max_turns,omitempty"`
	MaxCostUSD float64 `json:"max_cost_usd,omitempty"`
}

// MCPServerEntry configures a single MCP server for use as a brain.
type MCPServerEntry struct {
	Kind       string   `json:"kind"`
	BinPath    string   `json:"bin_path"`
	Args       []string `json:"args,omitempty"`
	Env        []string `json:"env,omitempty"`
	ToolPrefix string   `json:"tool_prefix"`
	AutoStart  bool     `json:"auto_start,omitempty"`
}

type ResolvedProvider struct {
	Name     string
	BaseURL  string
	APIKey   string
	Model    string
	Protocol string

	// Capabilities 是用户在 config.json 里 active_provider.capabilities 块
	// 填的原始 JSON,延迟到 provider 装配层反序列化为 *llm.CapabilitiesOverride。
	// 空表示用户未声明,装配层应只用 builtin 表 + 启发式构造 capability。
	Capabilities json.RawMessage
}

type FilePolicyInput = executionpolicy.FilePolicySpec
type FilePolicy = executionpolicy.FilePolicy
type ToolProfileConfig = toolpolicy.Profile

// ResolveProvider resolves provider settings with priority:
// 1. CLI flags (highest) 2. active_provider 3. top-level fields 4. env var 5. defaults
func ResolveProvider(cfg *Config, flagKey, flagURL, flagModel, brainKind string) ResolvedProvider {
	var r ResolvedProvider

	r.BaseURL = cfg.BaseURL
	r.APIKey = cfg.APIKey
	r.Model = cfg.Model

	if cfg.ActiveProvider != "" && cfg.Providers != nil {
		if p, ok := cfg.Providers[cfg.ActiveProvider]; ok {
			if p.BaseURL != "" {
				r.BaseURL = p.BaseURL
			}
			if p.APIKey != "" {
				r.APIKey = p.APIKey
			}
			if p.Model != "" {
				r.Model = p.Model
			}
			if brainKind != "" && p.Models != nil {
				if m, ok := p.Models[brainKind]; ok {
					r.Model = m
				}
			}
		}
	}

	if r.APIKey == "" {
		r.APIKey = os.Getenv("ANTHROPIC_API_KEY")
	}

	if flagKey != "" {
		r.APIKey = flagKey
	}
	if flagURL != "" {
		r.BaseURL = flagURL
	}
	if flagModel != "" {
		r.Model = flagModel
	}

	return r
}

func Path() string {
	return toolpolicy.ConfigPath()
}

// Load reads the config file. Returns nil if file doesn't exist.
func Load() (*Config, error) {
	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %v", path, err)
	}
	if cfg.ServeWorkdirPolicy != "" {
		if _, err := ParseServeWorkdirPolicy(cfg.ServeWorkdirPolicy); err != nil {
			return nil, fmt.Errorf("validate %s: %v", path, err)
		}
	}
	if _, err := executionpolicy.NewFilePolicy(".", cfg.FilePolicy); err != nil {
		return nil, fmt.Errorf("validate %s: %v", path, err)
	}
	if err := toolpolicy.ValidateConfig(PolicyConfig(cfg)); err != nil {
		return nil, fmt.Errorf("validate %s: %v", path, err)
	}
	return cfg, nil
}

func Save(cfg *Config) error {
	path := Path()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// 原子写:tmp + rename 防止中途 SIGKILL/断电留下半截 JSON,
	// 否则下次 Load 解析失败导致 brain 启动崩溃。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// LoadOrEmpty loads config, returns empty config if file doesn't exist.
func LoadOrEmpty() (*Config, error) {
	cfg, err := Load()
	if cfg == nil && err == nil {
		return &Config{}, nil
	}
	return cfg, err
}

// PolicyConfig converts Config to toolpolicy.Config.
func PolicyConfig(cfg *Config) *toolpolicy.Config {
	if cfg == nil {
		return nil
	}
	return &toolpolicy.Config{
		ToolProfiles: cfg.ToolProfiles,
		ActiveTools:  cfg.ActiveTools,
	}
}

// ToMap converts config struct to flat key-value map for display.
func ToMap(cfg *Config) map[string]string {
	m := make(map[string]string)
	if cfg.Mode != "" {
		m["mode"] = cfg.Mode
	}
	if cfg.Endpoint != "" {
		m["endpoint"] = cfg.Endpoint
	}
	if cfg.DefaultBrain != "" {
		m["default_brain"] = cfg.DefaultBrain
	}
	if cfg.DefaultModel != "" {
		m["default_model"] = cfg.DefaultModel
	}
	if cfg.Output != "" {
		m["output"] = cfg.Output
	}
	if cfg.LogLevel != "" {
		m["log_level"] = cfg.LogLevel
	}
	if cfg.ChatMode != "" {
		m["chat_mode"] = cfg.ChatMode
	}
	if cfg.PermissionMode != "" {
		m["permission_mode"] = cfg.PermissionMode
	}
	if cfg.ServeWorkdirPolicy != "" {
		m["serve_workdir_policy"] = cfg.ServeWorkdirPolicy
	}
	if cfg.NoColor {
		m["no_color"] = "true"
	}
	if cfg.Timeout != "" {
		m["timeout"] = cfg.Timeout
	}
	if cfg.FilePolicy != nil {
		if raw, err := json.Marshal(cfg.FilePolicy); err == nil {
			m["file_policy"] = string(raw)
		}
	}
	if cfg.APIKey != "" {
		m["api_key"] = cfg.APIKey
	}
	if cfg.BaseURL != "" {
		m["base_url"] = cfg.BaseURL
	}
	if cfg.Model != "" {
		m["model"] = cfg.Model
	}
	if cfg.ActiveProvider != "" {
		m["active_provider"] = cfg.ActiveProvider
	}
	if cfg.Providers != nil {
		for name, p := range cfg.Providers {
			prefix := "providers." + name + "."
			if p.BaseURL != "" {
				m[prefix+"base_url"] = p.BaseURL
			}
			if p.APIKey != "" {
				m[prefix+"api_key"] = p.APIKey
			}
			if p.Model != "" {
				m[prefix+"model"] = p.Model
			}
			for brain, model := range p.Models {
				m[prefix+"models."+brain] = model
			}
		}
	}
	if cfg.Budget != nil {
		if cfg.Budget.MaxTurns > 0 {
			m["default_budget.max_turns"] = strconv.Itoa(cfg.Budget.MaxTurns)
		}
		if cfg.Budget.MaxCostUSD > 0 {
			m["default_budget.max_cost_usd"] = strconv.FormatFloat(cfg.Budget.MaxCostUSD, 'f', -1, 64)
		}
	}
	if cfg.ToolProfiles != nil {
		for name, profile := range cfg.ToolProfiles {
			if profile == nil {
				continue
			}
			prefix := "tool_profiles." + name + "."
			if len(profile.Include) > 0 {
				m[prefix+"include"] = strings.Join(profile.Include, ",")
			}
			if len(profile.Exclude) > 0 {
				m[prefix+"exclude"] = strings.Join(profile.Exclude, ",")
			}
		}
	}
	if cfg.ActiveTools != nil {
		for scope, profileNames := range cfg.ActiveTools {
			if strings.TrimSpace(profileNames) == "" {
				continue
			}
			m["active_tools."+scope] = profileNames
		}
	}
	return m
}

func Get(cfg *Config, key string) (string, bool) {
	m := ToMap(cfg)
	v, ok := m[key]
	return v, ok
}

// Set sets a single key. parseChatMode and parsePermissionMode are injected to avoid circular deps.
func Set(cfg *Config, key, value string, parseChatMode, parsePermissionMode func(string) error, parseWorkdirPolicy func(string) (string, error)) error {
	switch key {
	case "mode":
		if value != "solo" && value != "cluster" {
			return fmt.Errorf("invalid mode %q (must be solo or cluster)", value)
		}
		cfg.Mode = value
	case "endpoint":
		cfg.Endpoint = value
	case "default_brain":
		cfg.DefaultBrain = value
	case "default_model":
		cfg.DefaultModel = value
	case "chat_mode":
		if parseChatMode != nil {
			if err := parseChatMode(value); err != nil {
				return err
			}
		}
		cfg.ChatMode = value
	case "permission_mode":
		if parsePermissionMode != nil {
			if err := parsePermissionMode(value); err != nil {
				return err
			}
		}
		cfg.PermissionMode = value
	case "serve_workdir_policy":
		if parseWorkdirPolicy != nil {
			policy, err := parseWorkdirPolicy(value)
			if err != nil {
				return err
			}
			cfg.ServeWorkdirPolicy = policy
		} else {
			cfg.ServeWorkdirPolicy = value
		}
	case "output":
		if value != "human" && value != "json" {
			return fmt.Errorf("invalid output %q (must be human or json)", value)
		}
		cfg.Output = value
	case "log_level":
		valid := map[string]bool{"trace": true, "debug": true, "info": true, "warn": true, "error": true}
		if !valid[value] {
			return fmt.Errorf("invalid log_level %q (must be trace/debug/info/warn/error)", value)
		}
		cfg.LogLevel = value
	case "no_color":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid no_color %q (must be true or false)", value)
		}
		cfg.NoColor = b
	case "timeout":
		cfg.Timeout = value
	case "file_policy":
		policy, err := ParseFilePolicyJSON(value)
		if err != nil {
			return err
		}
		cfg.FilePolicy = policy
	case "api_key":
		cfg.APIKey = value
	case "base_url":
		cfg.BaseURL = value
	case "model":
		cfg.Model = value
	case "active_provider":
		cfg.ActiveProvider = value
	case "default_budget.max_turns":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid max_turns %q (must be positive integer)", value)
		}
		if cfg.Budget == nil {
			cfg.Budget = &BudgetConfig{}
		}
		cfg.Budget.MaxTurns = n
	case "default_budget.max_cost_usd":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || f < 0 {
			return fmt.Errorf("invalid max_cost_usd %q (must be non-negative number)", value)
		}
		if cfg.Budget == nil {
			cfg.Budget = &BudgetConfig{}
		}
		cfg.Budget.MaxCostUSD = f
	default:
		if strings.HasPrefix(key, "providers.") {
			return setProviderKey(cfg, key, value)
		}
		if strings.HasPrefix(key, "tool_profiles.") {
			return setToolProfileKey(cfg, key, value)
		}
		if strings.HasPrefix(key, "active_tools.") {
			return setActiveToolsKey(cfg, key, value)
		}
		return fmt.Errorf("unknown config key %q", key)
	}
	return nil
}

func setProviderKey(cfg *Config, key, value string) error {
	parts := strings.SplitN(key, ".", 4)
	if len(parts) < 3 {
		return fmt.Errorf("invalid provider key %q (use providers.<name>.<field>)", key)
	}
	name := parts[1]
	field := parts[2]

	if cfg.Providers == nil {
		cfg.Providers = make(map[string]*ProviderConfig)
	}
	p, ok := cfg.Providers[name]
	if !ok {
		p = &ProviderConfig{}
		cfg.Providers[name] = p
	}

	switch field {
	case "base_url":
		p.BaseURL = value
	case "api_key":
		p.APIKey = value
	case "model":
		p.Model = value
	case "protocol":
		p.Protocol = value
	case "models":
		if len(parts) < 4 {
			return fmt.Errorf("invalid key %q (use providers.%s.models.<brain>)", key, name)
		}
		brain := parts[3]
		if p.Models == nil {
			p.Models = make(map[string]string)
		}
		p.Models[brain] = value
	default:
		return fmt.Errorf("unknown provider field %q (use base_url, api_key, model, protocol, or models.<brain>)", field)
	}
	return nil
}

func setToolProfileKey(cfg *Config, key, value string) error {
	parts := strings.SplitN(key, ".", 3)
	if len(parts) != 3 {
		return fmt.Errorf("invalid tool profile key %q (use tool_profiles.<name>.<include|exclude>)", key)
	}

	name := strings.TrimSpace(parts[1])
	field := parts[2]
	if name == "" {
		return fmt.Errorf("invalid tool profile key %q (profile name is required)", key)
	}

	patterns := toolpolicy.SplitCSV(value)
	if err := toolpolicy.ValidateProfileName(name); err != nil {
		return err
	}
	if len(patterns) == 0 {
		return fmt.Errorf("invalid %s %q (must be a comma-separated list of tool patterns)", field, value)
	}
	if err := toolpolicy.ValidatePatterns(patterns); err != nil {
		return err
	}

	if cfg.ToolProfiles == nil {
		cfg.ToolProfiles = make(map[string]*ToolProfileConfig)
	}
	profile, ok := cfg.ToolProfiles[name]
	if !ok || profile == nil {
		profile = &ToolProfileConfig{}
		cfg.ToolProfiles[name] = profile
	}

	switch field {
	case "include":
		profile.Include = patterns
	case "exclude":
		profile.Exclude = patterns
	default:
		return fmt.Errorf("unknown tool profile field %q (use include or exclude)", field)
	}
	return nil
}

func setActiveToolsKey(cfg *Config, key, value string) error {
	scope := strings.TrimSpace(strings.TrimPrefix(key, "active_tools."))
	if scope == "" || scope == key {
		return fmt.Errorf("invalid active_tools key %q (use active_tools.<scope>)", key)
	}

	profiles := toolpolicy.SplitCSV(value)
	raw := strings.Join(profiles, ",")
	if err := toolpolicy.ValidateActiveToolsValue(PolicyConfig(cfg), scope, raw); err != nil {
		return err
	}

	if cfg.ActiveTools == nil {
		cfg.ActiveTools = make(map[string]string)
	}
	cfg.ActiveTools[scope] = raw
	return nil
}

func Unset(cfg *Config, key string) {
	switch key {
	case "mode":
		cfg.Mode = ""
	case "endpoint":
		cfg.Endpoint = ""
	case "default_brain":
		cfg.DefaultBrain = ""
	case "default_model":
		cfg.DefaultModel = ""
	case "chat_mode":
		cfg.ChatMode = ""
	case "permission_mode":
		cfg.PermissionMode = ""
	case "serve_workdir_policy":
		cfg.ServeWorkdirPolicy = ""
	case "output":
		cfg.Output = ""
	case "log_level":
		cfg.LogLevel = ""
	case "no_color":
		cfg.NoColor = false
	case "timeout":
		cfg.Timeout = ""
	case "file_policy":
		cfg.FilePolicy = nil
	case "api_key":
		cfg.APIKey = ""
	case "base_url":
		cfg.BaseURL = ""
	case "model":
		cfg.Model = ""
	case "active_provider":
		cfg.ActiveProvider = ""
	case "default_budget.max_turns":
		if cfg.Budget != nil {
			cfg.Budget.MaxTurns = 0
		}
	case "default_budget.max_cost_usd":
		if cfg.Budget != nil {
			cfg.Budget.MaxCostUSD = 0
		}
	default:
		if strings.HasPrefix(key, "providers.") {
			unsetProviderKey(cfg, key)
		}
		if strings.HasPrefix(key, "tool_profiles.") {
			unsetToolProfileKey(cfg, key)
		}
		if strings.HasPrefix(key, "active_tools.") {
			unsetActiveToolsKey(cfg, key)
		}
	}
}

func unsetProviderKey(cfg *Config, key string) {
	if cfg.Providers == nil {
		return
	}
	parts := strings.SplitN(key, ".", 4)
	if len(parts) < 2 {
		return
	}
	name := parts[1]
	p, ok := cfg.Providers[name]
	if !ok {
		return
	}
	if len(parts) == 2 {
		delete(cfg.Providers, name)
		if len(cfg.Providers) == 0 {
			cfg.Providers = nil
		}
		return
	}
	field := parts[2]
	switch field {
	case "base_url":
		p.BaseURL = ""
	case "api_key":
		p.APIKey = ""
	case "model":
		p.Model = ""
	case "models":
		if len(parts) == 4 {
			delete(p.Models, parts[3])
			if len(p.Models) == 0 {
				p.Models = nil
			}
		} else {
			p.Models = nil
		}
	}
}

func unsetToolProfileKey(cfg *Config, key string) {
	if cfg.ToolProfiles == nil {
		return
	}

	parts := strings.SplitN(key, ".", 3)
	if len(parts) < 2 {
		return
	}
	name := parts[1]
	profile, ok := cfg.ToolProfiles[name]
	if !ok {
		return
	}

	if len(parts) == 2 {
		delete(cfg.ToolProfiles, name)
		if len(cfg.ToolProfiles) == 0 {
			cfg.ToolProfiles = nil
		}
		toolpolicy.PruneMissingProfiles(PolicyConfig(cfg))
		return
	}

	switch parts[2] {
	case "include":
		profile.Include = nil
	case "exclude":
		profile.Exclude = nil
	}

	if len(profile.Include) == 0 && len(profile.Exclude) == 0 {
		delete(cfg.ToolProfiles, name)
		if len(cfg.ToolProfiles) == 0 {
			cfg.ToolProfiles = nil
		}
	}
	toolpolicy.PruneMissingProfiles(PolicyConfig(cfg))
}

func unsetActiveToolsKey(cfg *Config, key string) {
	if cfg.ActiveTools == nil {
		return
	}
	scope := strings.TrimSpace(strings.TrimPrefix(key, "active_tools."))
	if scope == "" || scope == key {
		return
	}
	delete(cfg.ActiveTools, scope)
	if len(cfg.ActiveTools) == 0 {
		cfg.ActiveTools = nil
	}
}

func ParseFilePolicyJSON(raw string) (*FilePolicyInput, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	cfg := &FilePolicyInput{}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("parse file_policy_json: %w", err)
	}
	return cfg, nil
}

// SortedKeys returns sorted keys from a config map.
func SortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ─── Debug 配置访问器(nil-safe) ────────────────────────────────────
//
// 默认全部 false。任一开启 → 对应 [debug] 行打到 stderr,生产关闭。

// DebugRunnerEnabled 默认 false。开启后 sdk/loop/runner.go 每轮打 stop_reason 等。
func (c *Config) DebugRunnerEnabled() bool {
	if c == nil || c.Diagnostics == nil || c.Diagnostics.Debug == nil {
		return false
	}
	return c.Diagnostics.Debug.Runner
}

// DebugLLMRequestEnabled 默认 false。开启后 LLM 调用前打 ChatRequest 摘要。
func (c *Config) DebugLLMRequestEnabled() bool {
	if c == nil || c.Diagnostics == nil || c.Diagnostics.Debug == nil {
		return false
	}
	return c.Diagnostics.Debug.LLMRequest
}

// DebugLLMResponseEnabled 默认 false。开启后 LLM 响应到来时打 ChatResponse 摘要。
func (c *Config) DebugLLMResponseEnabled() bool {
	if c == nil || c.Diagnostics == nil || c.Diagnostics.Debug == nil {
		return false
	}
	return c.Diagnostics.Debug.LLMResponse
}

// DebugToolDispatchEnabled 默认 false。开启后每个工具调用打 name/args/耗时。
func (c *Config) DebugToolDispatchEnabled() bool {
	if c == nil || c.Diagnostics == nil || c.Diagnostics.Debug == nil {
		return false
	}
	return c.Diagnostics.Debug.ToolDispatch
}

// DebugContextEngineEnabled 默认 false。开启后 Assemble 流程打项目记忆/历史/Compress。
func (c *Config) DebugContextEngineEnabled() bool {
	if c == nil || c.Diagnostics == nil || c.Diagnostics.Debug == nil {
		return false
	}
	return c.Diagnostics.Debug.ContextEngine
}

// ─── MACCS 配置默认值访问器（nil-safe）────────────────────────────────
//
// 调用方应该用这些方法读 MACCS 配置，而不是直接 cfg.MACCS.X.Y —— 它们处理
// nil 指针并填充默认值，调用方零判空成本。

// MACCSHealthEnabled 返回 HealthManager 是否启用，默认 true。
func (c *Config) MACCSHealthEnabled() bool {
	if c == nil || c.MACCS == nil || c.MACCS.Health == nil {
		return true
	}
	return c.MACCS.Health.Enabled
}

// MACCSPerfEnabled 返回 PerfCollector 是否启用，默认 true。
func (c *Config) MACCSPerfEnabled() bool {
	if c == nil || c.MACCS == nil || c.MACCS.Perf == nil {
		return true
	}
	return c.MACCS.Perf.Enabled
}

// MACCSObservabilityEnabled 返回 ObservabilityHub 是否启用，默认 true。
func (c *Config) MACCSObservabilityEnabled() bool {
	if c == nil || c.MACCS == nil || c.MACCS.Observability == nil {
		return true
	}
	return c.MACCS.Observability.Enabled
}

// MACCSSecurityEnabled 返回 SecurityAuditor 是否启用，默认 true。
func (c *Config) MACCSSecurityEnabled() bool {
	if c == nil || c.MACCS == nil || c.MACCS.Security == nil {
		return true
	}
	return c.MACCS.Security.Enabled
}

// MACCSSecurityRejectSeverity 返回拒绝阈值，默认 "high"。
func (c *Config) MACCSSecurityRejectSeverity() string {
	if c == nil || c.MACCS == nil || c.MACCS.Security == nil || c.MACCS.Security.RejectSeverity == "" {
		return "high"
	}
	return c.MACCS.Security.RejectSeverity
}

// MACCSMultiProjectEnabled 返回 MultiProjectManager 是否启用，默认 true。
func (c *Config) MACCSMultiProjectEnabled() bool {
	if c == nil || c.MACCS == nil || c.MACCS.MultiProject == nil {
		return true
	}
	return c.MACCS.MultiProject.Enabled
}

// MACCSMultiProjectMaxConcurrent 默认 3。
func (c *Config) MACCSMultiProjectMaxConcurrent() int {
	if c == nil || c.MACCS == nil || c.MACCS.MultiProject == nil || c.MACCS.MultiProject.MaxConcurrent <= 0 {
		return 3
	}
	return c.MACCS.MultiProject.MaxConcurrent
}

// MACCSMultiProjectQueueSize 默认 16。
func (c *Config) MACCSMultiProjectQueueSize() int {
	if c == nil || c.MACCS == nil || c.MACCS.MultiProject == nil || c.MACCS.MultiProject.QueueSize <= 0 {
		return 16
	}
	return c.MACCS.MultiProject.QueueSize
}

// MACCSAdaptivePromptEnabled 默认 true。
func (c *Config) MACCSAdaptivePromptEnabled() bool {
	if c == nil || c.MACCS == nil || c.MACCS.AdaptivePrompt == nil {
		return true
	}
	return c.MACCS.AdaptivePrompt.Enabled
}

// MACCSConflictEnabled 默认 true。
func (c *Config) MACCSConflictEnabled() bool {
	if c == nil || c.MACCS == nil || c.MACCS.Conflict == nil {
		return true
	}
	return c.MACCS.Conflict.Enabled
}

// MACCSConflictDryRun 默认 true（生产首周观察期）。
func (c *Config) MACCSConflictDryRun() bool {
	if c == nil || c.MACCS == nil || c.MACCS.Conflict == nil {
		return true
	}
	return c.MACCS.Conflict.DryRun
}

// MACCSPatternExtractorEnabled 默认 true。
func (c *Config) MACCSPatternExtractorEnabled() bool {
	if c == nil || c.MACCS == nil || c.MACCS.PatternExtractor == nil {
		return true
	}
	return c.MACCS.PatternExtractor.Enabled
}

// MACCSDeadlockEnabled 默认 true（Wave 7 启用 wait-for graph 死锁检测）。
func (c *Config) MACCSDeadlockEnabled() bool {
	if c == nil || c.MACCS == nil || c.MACCS.Deadlock == nil {
		return true
	}
	return c.MACCS.Deadlock.Enabled
}

// MACCSDeadlockDryRun 默认 true（首周观察期：仅日志，不真中止 victim）。
func (c *Config) MACCSDeadlockDryRun() bool {
	if c == nil || c.MACCS == nil || c.MACCS.Deadlock == nil {
		return true
	}
	return c.MACCS.Deadlock.DryRun
}
