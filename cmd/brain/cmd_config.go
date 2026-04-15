package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/leef-l/brain/sdk/cli"
	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

// brainConfig is the on-disk configuration structure.
// We use JSON (not YAML) to stay zero-dependency.
// File: ~/.brain/config.json
type brainConfig struct {
	Mode               string        `json:"mode,omitempty"`
	Endpoint           string        `json:"endpoint,omitempty"`
	DefaultBrain       string        `json:"default_brain,omitempty"`
	DefaultModel       string        `json:"default_model,omitempty"`
	Output             string        `json:"output,omitempty"`
	LogLevel           string        `json:"log_level,omitempty"`
	NoColor            bool          `json:"no_color,omitempty"`
	Timeout            string        `json:"timeout,omitempty"`
	Budget             *budgetConfig `json:"default_budget,omitempty"`
	ChatMode           string        `json:"chat_mode,omitempty"`
	PermissionMode     string        `json:"permission_mode,omitempty"`
	ServeWorkdirPolicy string        `json:"serve_workdir_policy,omitempty"`
	APIKey             string        `json:"api_key,omitempty"`
	BaseURL            string        `json:"base_url,omitempty"`
	Model              string        `json:"model,omitempty"`

	// Multi-provider support.
	// "providers" maps a name to its configuration.
	// "active_provider" selects which provider to use by default.
	Providers      map[string]*providerConfig `json:"providers,omitempty"`
	ActiveProvider string                     `json:"active_provider,omitempty"`

	// Sandbox configuration for OS-level command isolation.
	Sandbox    *sandboxCfg      `json:"sandbox,omitempty"`
	FilePolicy *filePolicyInput `json:"file_policy,omitempty"`

	// Brains registers specialist brains that the Orchestrator can delegate to.
	// When non-empty, only configured brains are available — the built-in
	// kind list is bypassed. Each entry specifies kind, optional binary path,
	// and optional LLM model override.
	Brains []kernel.BrainRegistration `json:"brains,omitempty"`

	// ToolProfiles contains named include/exclude profiles that can be
	// activated per runtime scope via ActiveTools.
	ToolProfiles map[string]*toolProfileConfig `json:"tool_profiles,omitempty"`

	// ActiveTools maps a runtime scope ("chat", "chat.central.default",
	// "run.code", ...) to one or more profile names (comma-separated).
	ActiveTools map[string]string `json:"active_tools,omitempty"`
}

// sandboxCfg mirrors tool.SandboxConfig but lives in the config package
// to avoid circular imports.
type sandboxCfg struct {
	Enabled           bool     `json:"enabled"`
	AllowWrite        []string `json:"allow_write,omitempty"`
	DenyRead          []string `json:"deny_read,omitempty"`
	AllowNet          []string `json:"allow_net,omitempty"`
	FailIfUnavailable bool     `json:"fail_if_unavailable,omitempty"`
}

// providerConfig holds the configuration for a single LLM provider endpoint.
type providerConfig struct {
	BaseURL string            `json:"base_url"`
	APIKey  string            `json:"api_key"`
	Model   string            `json:"model,omitempty"`
	Models  map[string]string `json:"models,omitempty"` // brain kind → model
}

type budgetConfig struct {
	MaxTurns   int     `json:"max_turns,omitempty"`
	MaxCostUSD float64 `json:"max_cost_usd,omitempty"`
}

// resolvedProvider holds the resolved provider settings after merging all sources.
type resolvedProvider struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
}

// resolveProviderConfig resolves provider settings with the following priority:
//
//  1. CLI flags (apiKey, baseURL, model) — highest
//  2. active_provider → providers[name] → models[brainKind] → model
//  3. top-level api_key / base_url / model (backward compat)
//  4. ANTHROPIC_API_KEY env var
//  5. defaults
func resolveProviderConfig(cfg *brainConfig, flagKey, flagURL, flagModel, brainKind string) resolvedProvider {
	var r resolvedProvider

	// Layer 3: top-level fields (backward compat)
	r.BaseURL = cfg.BaseURL
	r.APIKey = cfg.APIKey
	r.Model = cfg.Model

	// Layer 2: active_provider → providers[name]
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
			// models[brainKind] overrides model
			if brainKind != "" && p.Models != nil {
				if m, ok := p.Models[brainKind]; ok {
					r.Model = m
				}
			}
		}
	}

	// Layer 4: env var (only if still empty)
	if r.APIKey == "" {
		r.APIKey = os.Getenv("ANTHROPIC_API_KEY")
	}

	// Layer 1: CLI flags (highest priority)
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

// configPath returns the default config file path.
func configPath() string {
	return toolpolicy.ConfigPath()
}

// loadConfig reads the config file. Returns nil if file doesn't exist.
func loadConfig() (*brainConfig, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	cfg := &brainConfig{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %v", path, err)
	}
	if _, err := parseServeWorkdirPolicy(cfg.ServeWorkdirPolicy); err != nil {
		return nil, fmt.Errorf("validate %s: %v", path, err)
	}
	if _, err := executionpolicy.NewFilePolicy(".", cfg.FilePolicy); err != nil {
		return nil, fmt.Errorf("validate %s: %v", path, err)
	}
	if err := toolpolicy.ValidateConfig(toolPolicyConfig(cfg)); err != nil {
		return nil, fmt.Errorf("validate %s: %v", path, err)
	}
	return cfg, nil
}

// initConfig creates a default config file for first-time setup.
func initConfig() error {
	cfg := &brainConfig{
		Mode:               "solo",
		DefaultBrain:       "central",
		ChatMode:           "accept-edits",
		PermissionMode:     "restricted",
		ServeWorkdirPolicy: string(serveWorkdirPolicyConfined),
		Timeout:            "30m",
		LogLevel:           "info",
		ActiveProvider:     "anthropic",
		Providers: map[string]*providerConfig{
			"anthropic": {
				BaseURL: "https://api.anthropic.com",
				APIKey:  "",
				Model:   "claude-sonnet-4-20250514",
				Models: map[string]string{
					"central":  "claude-sonnet-4-20250514",
					"code":     "claude-sonnet-4-20250514",
					"verifier": "claude-haiku-4-5-20251001",
				},
			},
		},
		Brains: []kernel.BrainRegistration{
			{Kind: "code", Model: "claude-sonnet-4-20250514"},
			{Kind: "verifier", Model: "claude-haiku-4-5-20251001"},
			{Kind: "data"},
			{Kind: "quant"},
		},
		Budget: &budgetConfig{
			MaxTurns:   20,
			MaxCostUSD: 5.0,
		},
		FilePolicy: &filePolicyInput{
			AllowRead:   []string{"**"},
			AllowCreate: []string{"**"},
			AllowEdit:   []string{"**"},
			AllowDelete: []string{},
			Deny:        []string{".git/**", "bin/**", "**/.env", "**/secrets/**"},
		},
	}
	return saveConfig(cfg)
}

// printConfigSetupGuide prints instructions for first-time configuration.
func printConfigSetupGuide() {
	path := configPath()
	fmt.Fprintln(os.Stderr, "\033[1;33m! 未找到配置文件\033[0m")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "请先完成配置，运行以下命令：")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "  \033[1mbrain config init\033[0m              # 生成默认配置文件 (%s)\n", path)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "然后设置 API Key 和模型：")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  \033[1mbrain config set active_provider anthropic\033[0m")
	fmt.Fprintln(os.Stderr, "  \033[1mbrain config set providers.anthropic.api_key sk-ant-xxxxx\033[0m")
	fmt.Fprintln(os.Stderr, "  \033[1mbrain config set providers.anthropic.model claude-sonnet-4-20250514\033[0m")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "可选配置：")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  brain config set permission_mode <mode> # run/serve 默认权限模式")
	fmt.Fprintln(os.Stderr, "  brain config set chat_mode <mode>     # plan, default, accept-edits, auto, restricted, bypass-permissions")
	fmt.Fprintln(os.Stderr, "  brain config set permission_mode restricted")
	fmt.Fprintln(os.Stderr, "  brain config set serve_workdir_policy confined")
	fmt.Fprintln(os.Stderr, "  brain config set timeout 30m")
	fmt.Fprintln(os.Stderr, "  brain config set providers.<name>.models.central <model>")
	fmt.Fprintln(os.Stderr, "  brain config set providers.<name>.models.code <model>")
	fmt.Fprintln(os.Stderr, "  brain config set default_budget.max_turns 20")
	fmt.Fprintln(os.Stderr, "  # 或直接在 config.json 里设置 file_policy")
	fmt.Fprintln(os.Stderr, "  brain config set tool_profiles.safe.include code.read_file,code.search")
	fmt.Fprintln(os.Stderr, "  brain config set active_tools.chat.central.default safe")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "或直接编辑配置文件: \033[2m%s\033[0m\n", path)
	fmt.Fprintln(os.Stderr, "")
}

// saveConfig writes the config to disk, creating the directory if needed.
func saveConfig(cfg *brainConfig) error {
	path := configPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0600)
}

// configToMap converts config struct to flat key-value map for display.
func configToMap(cfg *brainConfig) map[string]string {
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

// configGet reads a single key from the config.
func configGet(cfg *brainConfig, key string) (string, bool) {
	m := configToMap(cfg)
	v, ok := m[key]
	return v, ok
}

// configSet sets a single key. Returns error for invalid values.
func configSet(cfg *brainConfig, key, value string) error {
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
		if _, err := parseChatMode(value); err != nil {
			return err
		}
		cfg.ChatMode = value
	case "permission_mode":
		if _, err := parsePermissionMode(value); err != nil {
			return err
		}
		cfg.PermissionMode = value
	case "serve_workdir_policy":
		policy, err := parseServeWorkdirPolicy(value)
		if err != nil {
			return err
		}
		cfg.ServeWorkdirPolicy = string(policy)
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
		policy, err := parseFilePolicyJSON(value)
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
			cfg.Budget = &budgetConfig{}
		}
		cfg.Budget.MaxTurns = n
	case "default_budget.max_cost_usd":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || f < 0 {
			return fmt.Errorf("invalid max_cost_usd %q (must be non-negative number)", value)
		}
		if cfg.Budget == nil {
			cfg.Budget = &budgetConfig{}
		}
		cfg.Budget.MaxCostUSD = f
	default:
		// Handle providers.<name>.<field> and providers.<name>.models.<brain>
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

// setProviderKey handles "providers.<name>.base_url|api_key|model|models.<brain>"
func setProviderKey(cfg *brainConfig, key, value string) error {
	parts := strings.SplitN(key, ".", 4) // providers, name, field[, subfield]
	if len(parts) < 3 {
		return fmt.Errorf("invalid provider key %q (use providers.<name>.<field>)", key)
	}
	name := parts[1]
	field := parts[2]

	if cfg.Providers == nil {
		cfg.Providers = make(map[string]*providerConfig)
	}
	p, ok := cfg.Providers[name]
	if !ok {
		p = &providerConfig{}
		cfg.Providers[name] = p
	}

	switch field {
	case "base_url":
		p.BaseURL = value
	case "api_key":
		p.APIKey = value
	case "model":
		p.Model = value
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
		return fmt.Errorf("unknown provider field %q (use base_url, api_key, model, or models.<brain>)", field)
	}
	return nil
}

// setToolProfileKey handles "tool_profiles.<name>.include|exclude".
func setToolProfileKey(cfg *brainConfig, key, value string) error {
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
		cfg.ToolProfiles = make(map[string]*toolProfileConfig)
	}
	profile, ok := cfg.ToolProfiles[name]
	if !ok || profile == nil {
		profile = &toolProfileConfig{}
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

// setActiveToolsKey handles "active_tools.<scope>".
func setActiveToolsKey(cfg *brainConfig, key, value string) error {
	scope := strings.TrimSpace(strings.TrimPrefix(key, "active_tools."))
	if scope == "" || scope == key {
		return fmt.Errorf("invalid active_tools key %q (use active_tools.<scope>)", key)
	}

	profiles := toolpolicy.SplitCSV(value)
	raw := strings.Join(profiles, ",")
	if err := toolpolicy.ValidateActiveToolsValue(toolPolicyConfig(cfg), scope, raw); err != nil {
		return err
	}

	if cfg.ActiveTools == nil {
		cfg.ActiveTools = make(map[string]string)
	}
	cfg.ActiveTools[scope] = raw
	return nil
}

// configUnset removes a key from the config.
func configUnset(cfg *brainConfig, key string) {
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

// unsetProviderKey removes a provider key or an entire provider.
func unsetProviderKey(cfg *brainConfig, key string) {
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
	// "providers.<name>" — remove entire provider
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

func unsetToolProfileKey(cfg *brainConfig, key string) {
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
		toolpolicy.PruneMissingProfiles(toolPolicyConfig(cfg))
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
	toolpolicy.PruneMissingProfiles(toolPolicyConfig(cfg))
}

func unsetActiveToolsKey(cfg *brainConfig, key string) {
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

// runConfig implements `brain config` with subcommands.
// See 27-CLI命令契约.md §14.
func runConfig(args []string) int {
	if len(args) == 0 {
		printConfigUsage()
		return cli.ExitUsage
	}

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "init":
		return runConfigInit(rest)
	case "list":
		return runConfigList(rest)
	case "get":
		return runConfigGet(rest)
	case "set":
		return runConfigSet(rest)
	case "unset":
		return runConfigUnset(rest)
	case "path":
		return runConfigPath(rest)
	case "-h", "--help", "help":
		printConfigUsage()
		return cli.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "brain config: unknown subcommand %q\n", sub)
		printConfigUsage()
		return cli.ExitUsage
	}
}

func runConfigInit(_ []string) int {
	path := configPath()
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(os.Stderr, "配置文件已存在: %s\n", path)
		fmt.Fprintln(os.Stderr, "如需重置，请先删除该文件。")
		return cli.ExitFailed
	}
	if err := initConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "brain config init: %v\n", err)
		return cli.ExitSoftware
	}
	fmt.Fprintf(os.Stdout, "已生成默认配置文件: %s\n", path)
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "下一步，设置你的 API Key：")
	fmt.Fprintln(os.Stdout, "  brain config set providers.anthropic.api_key <your-key>")
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "或直接编辑配置文件修改 provider 和模型。")
	return cli.ExitOK
}

func printConfigUsage() {
	fmt.Fprintln(os.Stderr, "Usage: brain config <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  init     Generate default config file")
	fmt.Fprintln(os.Stderr, "  list     List all configuration values")
	fmt.Fprintln(os.Stderr, "  get      Get a configuration value")
	fmt.Fprintln(os.Stderr, "  set      Set a configuration value")
	fmt.Fprintln(os.Stderr, "  unset    Remove a configuration value")
	fmt.Fprintln(os.Stderr, "  path     Print the config file path")
}

// loadConfigOrEmpty loads config, returns empty config if file doesn't exist.
func loadConfigOrEmpty() (*brainConfig, error) {
	cfg, err := loadConfig()
	if cfg == nil && err == nil {
		return &brainConfig{}, nil
	}
	return cfg, err
}

func runConfigList(args []string) int {
	fs := flag.NewFlagSet("config list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain config: %v\n", err)
		return cli.ExitSoftware
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(cfg)
	} else {
		m := configToMap(cfg)
		if len(m) == 0 {
			fmt.Fprintln(os.Stdout, "(no configuration set)")
			fmt.Fprintf(os.Stdout, "Config file: %s\n", configPath())
			return cli.ExitOK
		}
		// Sort keys for stable output
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		maxLen := 0
		for _, k := range keys {
			if len(k) > maxLen {
				maxLen = len(k)
			}
		}
		for _, k := range keys {
			fmt.Fprintf(os.Stdout, "%-*s  %s\n", maxLen, k, m[k])
		}
	}
	return cli.ExitOK
}

func runConfigGet(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain config get <key>")
		return cli.ExitUsage
	}

	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain config: %v\n", err)
		return cli.ExitSoftware
	}

	val, ok := configGet(cfg, args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "brain config get: key %q not set\n", args[0])
		return cli.ExitNotFound
	}
	fmt.Fprintln(os.Stdout, val)
	return cli.ExitOK
}

func runConfigSet(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: brain config set <key> <value>")
		return cli.ExitUsage
	}

	key := args[0]
	value := strings.Join(args[1:], " ")

	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain config: %v\n", err)
		return cli.ExitSoftware
	}

	if err := configSet(cfg, key, value); err != nil {
		fmt.Fprintf(os.Stderr, "brain config set: %v\n", err)
		return cli.ExitDataErr
	}

	if err := saveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "brain config set: write: %v\n", err)
		if os.IsPermission(err) {
			return cli.ExitNoPerm
		}
		return cli.ExitSoftware
	}
	fmt.Fprintf(os.Stdout, "Updated: %s = %s\n", key, value)
	return cli.ExitOK
}

func runConfigUnset(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain config unset <key>")
		return cli.ExitUsage
	}

	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain config: %v\n", err)
		return cli.ExitSoftware
	}

	configUnset(cfg, args[0])

	if err := saveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "brain config unset: write: %v\n", err)
		return cli.ExitSoftware
	}
	fmt.Fprintf(os.Stdout, "Removed: %s\n", args[0])
	return cli.ExitOK
}

func runConfigPath(_ []string) int {
	fmt.Fprintln(os.Stdout, configPath())
	return cli.ExitOK
}
