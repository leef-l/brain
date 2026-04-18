package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/sdk/cli"
	"github.com/leef-l/brain/sdk/kernel"
)

// Type aliases — allow existing code to keep using old names.
// These will be removed as files migrate to importing config directly.
type brainConfig = config.Config
type sandboxCfg = config.SandboxCfg
type providerConfig = config.ProviderConfig
type budgetConfig = config.BudgetConfig
type resolvedProvider = config.ResolvedProvider
type filePolicyInput = config.FilePolicyInput
type filePolicy = config.FilePolicy
type toolProfileConfig = config.ToolProfileConfig
type modelConfigInput = config.ModelConfigInput

var (
	parseModelConfigJSON    = config.ParseModelConfigJSON
	wantsMockProvider       = config.WantsMockProvider
	hasModelConfigOverrides = config.HasModelConfigOverrides
)

func resolveProviderConfig(cfg *brainConfig, flagKey, flagURL, flagModel, brainKind string) resolvedProvider {
	return config.ResolveProvider(cfg, flagKey, flagURL, flagModel, brainKind)
}

func configPath() string {
	return config.Path()
}

func loadConfig() (*brainConfig, error) {
	return config.Load()
}

func saveConfig(cfg *brainConfig) error {
	return config.Save(cfg)
}

func loadConfigOrEmpty() (*brainConfig, error) {
	return config.LoadOrEmpty()
}

func configToMap(cfg *brainConfig) map[string]string {
	return config.ToMap(cfg)
}

func configGet(cfg *brainConfig, key string) (string, bool) {
	return config.Get(cfg, key)
}

func configSet(cfg *brainConfig, key, value string) error {
	return config.Set(cfg, key, value,
		func(v string) error { _, err := parseChatMode(v); return err },
		func(v string) error { _, err := parsePermissionMode(v); return err },
		func(v string) (string, error) {
			p, err := parseServeWorkdirPolicy(v)
			return string(p), err
		},
	)
}

func configUnset(cfg *brainConfig, key string) {
	config.Unset(cfg, key)
}

func parseFilePolicyJSON(raw string) (*filePolicyInput, error) {
	return config.ParseFilePolicyJSON(raw)
}

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

// initConfig creates a default config file for first-time setup.
func initConfig() error {
	cfg := &brainConfig{
		Mode:               "solo",
		DefaultBrain:       "central",
		ChatMode:           "accept-edits",
		PermissionMode:     "accept-edits",
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
	if err := saveConfig(cfg); err != nil {
		return err
	}
	kbPath := keybindingsPath()
	if _, err := os.Stat(kbPath); os.IsNotExist(err) {
		kb := defaultKeybindings()
		data, err := json.MarshalIndent(kb, "", "  ")
		if err == nil {
			data = append(data, '\n')
			_ = os.WriteFile(kbPath, data, 0600)
		}
	}
	config.WriteExamples(filepath.Dir(configPath()))
	return nil
}

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
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0700)

	configExists := false
	if _, err := os.Stat(path); err == nil {
		configExists = true
	}

	if !configExists {
		if err := initConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "brain config init: %v\n", err)
			return cli.ExitSoftware
		}
	}

	config.WriteExamples(dir)

	if configExists {
		fmt.Fprintf(os.Stdout, "配置文件已存在: %s（未修改）\n", path)
		fmt.Fprintf(os.Stdout, "已更新示例文件:\n")
	} else {
		fmt.Fprintf(os.Stdout, "已生成配置文件:\n")
		fmt.Fprintf(os.Stdout, "  %s\n", path)
		fmt.Fprintf(os.Stdout, "  %s\n", keybindingsPath())
	}
	fmt.Fprintf(os.Stdout, "  %s\n", filepath.Join(dir, "data-brain.example.yaml"))
	fmt.Fprintf(os.Stdout, "  %s\n", filepath.Join(dir, "quant-brain.example.yaml"))
	fmt.Fprintf(os.Stdout, "  %s\n", filepath.Join(dir, "central-brain.example.yaml"))
	fmt.Fprintf(os.Stdout, "  %s\n", filepath.Join(dir, "config-reference.example.yaml"))
	fmt.Fprintln(os.Stdout, "")

	if !configExists {
		fmt.Fprintln(os.Stdout, "下一步，设置你的 API Key：")
		fmt.Fprintln(os.Stdout, "  brain config set providers.anthropic.api_key <your-key>")
		fmt.Fprintln(os.Stdout, "")
	}
	fmt.Fprintln(os.Stdout, "配置专精大脑（可选）：")
	fmt.Fprintln(os.Stdout, "  cp ~/.brain/quant-brain.example.yaml ~/.brain/quant-brain.yaml")
	fmt.Fprintln(os.Stdout, "  cp ~/.brain/data-brain.example.yaml  ~/.brain/data-brain.yaml")
	fmt.Fprintln(os.Stdout, "  # 编辑 yaml 后，在 config.json 的 brains 字段中指定路径")
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
		keys := config.SortedKeys(m)
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
