package toolpolicy

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/leef-l/brain/sdk/tool"
)

// Profile defines a named tool filter profile. Include is an allow-list
// (empty means "start from every registered tool"), and Exclude is a deny-list
// applied afterward. Patterns use path.Match semantics.
type Profile struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// Config is the tool policy subset of ~/.brain/config.json.
type Config struct {
	ToolProfiles map[string]*Profile `json:"tool_profiles,omitempty"`
	ActiveTools  map[string]string   `json:"active_tools,omitempty"`
}

var (
	profileNameRE = regexp.MustCompile(`^[a-z0-9_-]+$`)
	scopeNameRE   = regexp.MustCompile(`^[a-z0-9_-]+(\.[a-z0-9_-]+)*$`)
)

// ConfigPath returns the active config path. BRAIN_CONFIG overrides the
// default ~/.brain/config.json location.
func ConfigPath() string {
	if override := strings.TrimSpace(os.Getenv("BRAIN_CONFIG")); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".brain", "config.json")
}

// Load reads tool policy configuration from path. Empty path resolves to
// ConfigPath(). A missing file returns (nil, nil).
func Load(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		path = ConfigPath()
	}

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
	if err := ValidateConfig(cfg); err != nil {
		return nil, fmt.Errorf("validate %s: %v", path, err)
	}
	return cfg, nil
}

// ValidateConfig validates the tool_profiles / active_tools sections.
func ValidateConfig(cfg *Config) error {
	if cfg == nil {
		return nil
	}

	for name, profile := range cfg.ToolProfiles {
		if err := ValidateProfileName(name); err != nil {
			return err
		}
		if profile == nil {
			return fmt.Errorf("tool_profiles.%s must not be null", name)
		}
		if err := ValidatePatterns(profile.Include); err != nil {
			return fmt.Errorf("tool_profiles.%s.include: %w", name, err)
		}
		if err := ValidatePatterns(profile.Exclude); err != nil {
			return fmt.Errorf("tool_profiles.%s.exclude: %w", name, err)
		}
	}

	for scope, rawProfiles := range cfg.ActiveTools {
		if err := ValidateActiveToolsValue(cfg, scope, rawProfiles); err != nil {
			return err
		}
	}
	return nil
}

// ValidateProfileName validates a profile identifier.
func ValidateProfileName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	if !profileNameRE.MatchString(name) {
		return fmt.Errorf("invalid profile name %q", name)
	}
	return nil
}

// ValidateScope validates an active_tools scope.
func ValidateScope(scope string) error {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return fmt.Errorf("scope is required")
	}
	if !scopeNameRE.MatchString(scope) {
		return fmt.Errorf("invalid scope %q", scope)
	}
	return nil
}

// ValidatePatterns validates a list of tool glob patterns.
func ValidatePatterns(patterns []string) error {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			return fmt.Errorf("pattern must not be empty")
		}
		if _, err := path.Match(pattern, "probe"); err != nil {
			return fmt.Errorf("invalid pattern %q: %v", pattern, err)
		}
	}
	return nil
}

// ValidateActiveToolsValue validates one active_tools entry.
func ValidateActiveToolsValue(cfg *Config, scope string, rawProfiles string) error {
	if err := ValidateScope(scope); err != nil {
		return err
	}
	profiles := SplitCSV(rawProfiles)
	if len(profiles) == 0 {
		return fmt.Errorf("active_tools.%s must name at least one profile", scope)
	}

	for _, profile := range profiles {
		if err := ValidateProfileName(profile); err != nil {
			return fmt.Errorf("active_tools.%s: %w", scope, err)
		}
		if cfg == nil || cfg.ToolProfiles == nil || cfg.ToolProfiles[profile] == nil {
			return fmt.Errorf("active_tools.%s references unknown profile %q", scope, profile)
		}
	}
	return nil
}

// PruneMissingProfiles removes missing-profile references from ActiveTools.
func PruneMissingProfiles(cfg *Config) {
	if cfg == nil || len(cfg.ActiveTools) == 0 {
		return
	}

	for scope, rawProfiles := range cfg.ActiveTools {
		var kept []string
		for _, profile := range SplitCSV(rawProfiles) {
			if cfg.ToolProfiles != nil && cfg.ToolProfiles[profile] != nil {
				kept = append(kept, profile)
			}
		}
		if len(kept) == 0 {
			delete(cfg.ActiveTools, scope)
			continue
		}
		cfg.ActiveTools[scope] = strings.Join(kept, ",")
	}
	if len(cfg.ActiveTools) == 0 {
		cfg.ActiveTools = nil
	}
}

// FilterRegistry applies any active tool profile configured for the given
// scopes. Scopes are evaluated from least to most specific so callers can
// layer a broad profile ("chat") with narrower overrides
// ("chat.central.default").
func FilterRegistry(reg tool.Registry, cfg *Config, scopes ...string) tool.Registry {
	if reg == nil || cfg == nil || len(cfg.ToolProfiles) == 0 || len(cfg.ActiveTools) == 0 {
		return reg
	}

	profileNames := ActiveProfiles(cfg, scopes...)
	if len(profileNames) == 0 {
		return reg
	}

	include, exclude := MergePatterns(cfg, profileNames)
	filtered := tool.NewMemRegistry()
	for _, t := range reg.List() {
		if ToolAllowed(t.Name(), include, exclude) {
			_ = filtered.Register(t)
		}
	}
	return filtered
}

// ActiveProfiles resolves the active profile names for the provided scopes.
func ActiveProfiles(cfg *Config, scopes ...string) []string {
	if cfg == nil || len(cfg.ActiveTools) == 0 {
		return nil
	}

	var profiles []string
	seen := make(map[string]bool)
	for _, scope := range scopes {
		raw := strings.TrimSpace(cfg.ActiveTools[scope])
		if raw == "" {
			continue
		}
		for _, name := range SplitCSV(raw) {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			profiles = append(profiles, name)
		}
	}
	return profiles
}

// MergePatterns combines the include/exclude patterns from multiple profiles.
func MergePatterns(cfg *Config, profileNames []string) ([]string, []string) {
	var include []string
	var exclude []string

	for _, name := range profileNames {
		profile := cfg.ToolProfiles[name]
		if profile == nil {
			continue
		}
		include = append(include, profile.Include...)
		exclude = append(exclude, profile.Exclude...)
	}
	return include, exclude
}

// ToolAllowed reports whether name survives the merged profile rules.
func ToolAllowed(name string, include, exclude []string) bool {
	if len(include) > 0 && !MatchesAny(name, include) {
		return false
	}
	if MatchesAny(name, exclude) {
		return false
	}
	return true
}

// MatchesAny reports whether name matches any pattern.
func MatchesAny(name string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if pattern == "*" || pattern == name {
			return true
		}
		ok, err := path.Match(pattern, name)
		if err == nil && ok {
			return true
		}
	}
	return false
}

// SplitCSV splits a comma-separated profile list.
func SplitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// ToolScopesForChat returns ordered scopes for a chat session.
func ToolScopesForChat(brainKind, mode string) []string {
	scopes := []string{"chat"}
	if brainKind != "" {
		scopes = append(scopes, "chat."+brainKind)
	}
	if mode != "" {
		scopes = append(scopes, "chat."+mode)
	}
	if brainKind != "" && mode != "" {
		scopes = append(scopes, "chat."+brainKind+"."+mode)
	}
	return scopes
}

// ToolScopesForRun returns ordered scopes for a non-interactive run.
func ToolScopesForRun(brainKind string) []string {
	scopes := []string{"run"}
	if brainKind != "" {
		scopes = append(scopes, "run."+brainKind)
	}
	return scopes
}

// ToolScopesForDelegate returns ordered scopes for delegated specialist brains.
func ToolScopesForDelegate(brainKind string) []string {
	scopes := []string{"delegate"}
	if brainKind != "" {
		scopes = append(scopes, "delegate."+brainKind)
	}
	return scopes
}

// BrowserStage* 是 Browser Brain 语义理解阶段标识,对应 40 号文档 §3 的阶段 1-3。
const (
	BrowserStageNewPage     = "new_page"
	BrowserStageKnownFlow   = "known_flow"
	BrowserStageDestructive = "destructive"
	BrowserStageFallback    = "fallback"
)

// ToolScopesForBrowserStage 返回某个 Browser Brain 语义阶段额外开放的 scope。
// 这些 scope 叠加在 run.browser 之后,profile 匹配时晚定义的覆盖早定义的,
// 方便运营在 ~/.brain/config.json 里为不同阶段配不同工具 allow-list。
//
// 未知 stage 返回空,退回通用 run.browser 行为。
func ToolScopesForBrowserStage(stage string) []string {
	stage = strings.TrimSpace(stage)
	switch stage {
	case BrowserStageNewPage,
		BrowserStageKnownFlow,
		BrowserStageDestructive,
		BrowserStageFallback:
		return []string{"run.browser." + stage}
	}
	return nil
}

// DefaultBrowserStageProfiles 返回四个内建 profile + 对应 active_tools 绑定,
// 调用方可以合并到已有 Config,也可全量覆盖。profile 命名以 "browser_" 开头,
// 避免和用户自定义 profile 冲突。
//
// 设计对应文档 40 §5.1:
//   - new_page   优先 snapshot + understand + sitemap,禁 screenshot
//   - known_flow 优先 pattern_match/list/exec,限制昂贵工具
//   - destructive 在 known_flow 之上加 visual_inspect + snapshot 做视觉复核
//   - fallback   全量开放,包括 visual_inspect 和 eval
func DefaultBrowserStageProfiles() (map[string]*Profile, map[string]string) {
	profiles := map[string]*Profile{
		"browser_new_page": {
			Include: []string{
				"browser.snapshot", "browser.understand", "browser.sitemap",
				"browser.check_anomaly", "browser.wait*", "browser.network",
				"browser.navigate", "browser.open",
				"browser.click", "browser.type", "browser.press_key",
				"browser.scroll", "browser.hover",
			},
		},
		"browser_known_flow": {
			Include: []string{
				"browser.pattern_*",
				"browser.snapshot", "browser.check_anomaly",
				"browser.click", "browser.type", "browser.press_key",
				"browser.scroll", "browser.navigate",
				"browser.fill_form", "browser.storage",
				"browser.wait*", "browser.network",
			},
		},
		"browser_destructive": {
			Include: []string{
				"browser.pattern_*",
				"browser.snapshot", "browser.understand",
				"browser.visual_inspect", "browser.screenshot",
				"browser.check_anomaly",
				"browser.click", "browser.type", "browser.press_key",
			},
		},
		"browser_fallback": {
			// 兜底阶段允许全量工具,但 exclude 掉 eval 的原始危险变体
			// (如未来引入 browser.eval.unrestricted)。目前全开。
			Include: []string{"browser.*"},
		},
	}
	active := map[string]string{
		"run.browser." + BrowserStageNewPage:     "browser_new_page",
		"run.browser." + BrowserStageKnownFlow:   "browser_known_flow",
		"run.browser." + BrowserStageDestructive: "browser_destructive",
		"run.browser." + BrowserStageFallback:    "browser_fallback",
	}
	return profiles, active
}

// MergeBrowserStageProfiles 把默认 browser 阶段 profiles/active_tools 合并到
// cfg,调用方可在启动时无感引入。已存在的同名 profile 不被覆盖,让用户可自定义。
func MergeBrowserStageProfiles(cfg *Config) {
	if cfg == nil {
		return
	}
	profiles, active := DefaultBrowserStageProfiles()
	if cfg.ToolProfiles == nil {
		cfg.ToolProfiles = map[string]*Profile{}
	}
	for k, v := range profiles {
		if _, exists := cfg.ToolProfiles[k]; !exists {
			cfg.ToolProfiles[k] = v
		}
	}
	if cfg.ActiveTools == nil {
		cfg.ActiveTools = map[string]string{}
	}
	for k, v := range active {
		if _, exists := cfg.ActiveTools[k]; !exists {
			cfg.ActiveTools[k] = v
		}
	}
}
