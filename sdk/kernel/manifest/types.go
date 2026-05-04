package manifest

// RuntimeType 运行时类型枚举
type RuntimeType string

const (
	RuntimeNative    RuntimeType = "native"     // 原生 Go sidecar
	RuntimeMCPBacked RuntimeType = "mcp-backed" // MCP server 驱动
	RuntimeHybrid    RuntimeType = "hybrid"     // native + MCP-backed 混合模��
	RuntimeWasm      RuntimeType = "wasm"       // WASM 模块
	RuntimeDocker    RuntimeType = "docker"     // 容��化
	RuntimeRemote    RuntimeType = "remote"     // 远程 HTTP JSON-RPC
)

// validRuntimeTypes 用于校验
var validRuntimeTypes = map[RuntimeType]bool{
	RuntimeNative:    true,
	RuntimeMCPBacked: true,
	RuntimeHybrid:    true,
	RuntimeWasm:      true,
	RuntimeDocker:    true,
	RuntimeRemote:    true,
}

// Manifest 是 brain.json / brain.yaml 的完整 Go 结构体
type Manifest struct {
	SchemaVersion int            `json:"schema_version" yaml:"schema_version"`
	Kind          string         `json:"kind"           yaml:"kind"`
	Name          string         `json:"name"           yaml:"name"`
	BrainVersion  string         `json:"brain_version"  yaml:"brain_version"`
	Description   string         `json:"description,omitempty"  yaml:"description,omitempty"`
	Capabilities  []string       `json:"capabilities"           yaml:"capabilities"`
	TaskPatterns  []string       `json:"task_patterns,omitempty" yaml:"task_patterns,omitempty"`
	Runtime       RuntimeSpec    `json:"runtime"        yaml:"runtime"`
	Policy        PolicySpec     `json:"policy"         yaml:"policy"`
	Compatibility *CompatSpec    `json:"compatibility,omitempty" yaml:"compatibility,omitempty"`
	Health        *HealthSpec    `json:"health,omitempty"        yaml:"health,omitempty"`
	License       *LicenseSpec   `json:"license,omitempty"       yaml:"license,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"      yaml:"metadata,omitempty"`
	SourcePath    string         `json:"-" yaml:"-"` // 解析时注入，不序列化
}

// LicenseSpec 运行授权声明(非开源许可证文本)。
// 规格:33-Brain-Manifest规格.md §9 line 359-388。
//
// 这只是 brain 自己声明"我需要什么授权才能跑",真正的授权校验由 runtime
// 读取 license.json 执行。Manifest 不做强制。
type LicenseSpec struct {
	// Required 是否需要运行授权。规格 §9.1。
	Required bool `json:"required" yaml:"required"`
	// Edition free / pro / enterprise 等。规格 §9.1。
	Edition string `json:"edition,omitempty" yaml:"edition,omitempty"`
	// Features 该 brain 可能用到的 feature gate 列表。规格 §9.1。
	Features []string `json:"features,omitempty" yaml:"features,omitempty"`
}

// RuntimeSpec 运行时配置
type RuntimeSpec struct {
	Type       RuntimeType        `json:"type"                    yaml:"type"`
	Entrypoint string             `json:"entrypoint,omitempty"    yaml:"entrypoint,omitempty"`
	Args       []string           `json:"args,omitempty"          yaml:"args,omitempty"`
	Env        map[string]string  `json:"env,omitempty"           yaml:"env,omitempty"`
	MCPServers []MCPServerBinding `json:"mcp_servers,omitempty" yaml:"mcp_servers,omitempty"`

	// Endpoint 是远程 brain 的 HTTP/gRPC 地址（仅 remote 类型使用）。
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`

	// Auth 指定远程认证方式（如 "bearer"），仅 remote 类型使用。
	Auth string `json:"auth,omitempty" yaml:"auth,omitempty"`
}

// MCPServerBinding MCP 服务器绑定
type MCPServerBinding struct {
	Name       string            `json:"name"                 yaml:"name"`
	Command    string            `json:"command"              yaml:"command"`
	Args       []string          `json:"args,omitempty"       yaml:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"        yaml:"env,omitempty"`
	ToolPrefix string            `json:"tool_prefix"          yaml:"tool_prefix"`
}

// PolicySpec 策略配置
type PolicySpec struct {
	MaxConcurrency       int    `json:"max_concurrency,omitempty"            yaml:"max_concurrency,omitempty"`
	TimeoutSeconds       int    `json:"timeout_seconds,omitempty"            yaml:"timeout_seconds,omitempty"`
	MinApprovalLevel     string `json:"min_approval_level,omitempty"         yaml:"min_approval_level,omitempty"`
	RequireHumanApproval string `json:"require_human_approval_above,omitempty" yaml:"require_human_approval_above,omitempty"`
	ApprovalClass        string `json:"approval_class,omitempty"             yaml:"approval_class,omitempty"`
	ApprovalMode         string `json:"approval_mode,omitempty"              yaml:"approval_mode,omitempty"`
	SandboxProfile       string `json:"sandbox_profile,omitempty"            yaml:"sandbox_profile,omitempty"`
	ToolScope            string `json:"tool_scope,omitempty"                 yaml:"tool_scope,omitempty"`
	// ActiveToolsProfile 推荐的工具集 profile(如 "safe"/"default"/"none"),由 kernel
	// 决定是否对接 active_tools.<scope>.<profile>。Manifest 只做声明,不做强制。
	// 规格:33-Brain-Manifest规格.md §7.1 line 305-316。
	ActiveToolsProfile string `json:"active_tools_profile,omitempty"       yaml:"active_tools_profile,omitempty"`
}

// CompatSpec 兼容性约束。
// 规格:33-Brain-Manifest规格.md §8 line 329-356。
//
// JSON tag 说明:
//   - 规格里 §8 用了 min_kernel/max_kernel,但代码历史上一直用 min_kernel_version/
//     max_kernel_version,因为 Marketplace 已发布的 brain.json 都用后者。修改 tag
//     会破坏向后兼容,因此保持代码现状,文档 §8 加勘误即可。
//   - Protocol/TestedKernel 是规格 v1 至少要求的字段(§8.1),这里补全。
type CompatSpec struct {
	// Protocol BrainKernel 协议版本(SHOULD 为明确值,如 "1.0")。规格 §8.1。
	Protocol string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	// TestedKernel 该 brain 测试通过的 kernel 版本(可通配,如 "1.0.x")。规格 §8.1。
	TestedKernel     string `json:"tested_kernel,omitempty" yaml:"tested_kernel,omitempty"`
	MinKernelVersion string `json:"min_kernel_version,omitempty" yaml:"min_kernel_version,omitempty"`
	MaxKernelVersion string `json:"max_kernel_version,omitempty" yaml:"max_kernel_version,omitempty"`
}

// HealthSpec 健康检查配置
type HealthSpec struct {
	PingIntervalSeconds int      `json:"ping_interval_seconds,omitempty"   yaml:"ping_interval_seconds,omitempty"`
	PingTimeoutSeconds  int      `json:"ping_timeout_seconds,omitempty"    yaml:"ping_timeout_seconds,omitempty"`
	MaxMissedPings      int      `json:"max_missed_pings,omitempty"        yaml:"max_missed_pings,omitempty"`
	StartupTimeoutMs    int      `json:"startup_timeout_ms,omitempty"      yaml:"startup_timeout_ms,omitempty"`
	HeartbeatTimeoutMs  int      `json:"heartbeat_timeout_ms,omitempty"    yaml:"heartbeat_timeout_ms,omitempty"`
	ExpectedMethods     []string `json:"expected_methods,omitempty"        yaml:"expected_methods,omitempty"`
}
