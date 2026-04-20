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

	ToolProfiles map[string]*ToolProfileConfig `json:"tool_profiles,omitempty"`
	ActiveTools  map[string]string             `json:"active_tools,omitempty"`
}

type DiagnosticsConfig struct {
	Enabled    bool     `json:"enabled,omitempty"`
	Categories []string `json:"categories,omitempty"`
	File       string   `json:"file,omitempty"`
	Stderr     bool     `json:"stderr,omitempty"`
	Level      string   `json:"level,omitempty"`
	Format     string   `json:"format,omitempty"`
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

type ResolvedProvider struct {
	Name     string
	BaseURL  string
	APIKey   string
	Model    string
	Protocol string
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
	return os.WriteFile(path, data, 0600)
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
