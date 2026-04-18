package manifest

// RuntimeType 运行时类型枚举
type RuntimeType string

const (
	RuntimeNative    RuntimeType = "native"     // 原生 Go sidecar
	RuntimeMCPBacked RuntimeType = "mcp-backed" // MCP server 驱动
	RuntimeWasm      RuntimeType = "wasm"       // WASM 模块
	RuntimeDocker    RuntimeType = "docker"     // 容器化
	RuntimeRemote    RuntimeType = "remote"     // 远程 HTTP JSON-RPC
)

// validRuntimeTypes 用于校验
var validRuntimeTypes = map[RuntimeType]bool{
	RuntimeNative:    true,
	RuntimeMCPBacked: true,
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
	Metadata      map[string]any `json:"metadata,omitempty"      yaml:"metadata,omitempty"`
	SourcePath    string         `json:"-" yaml:"-"` // 解析时注入，不序列化
}

// RuntimeSpec 运行时配置
type RuntimeSpec struct {
	Type       RuntimeType      `json:"type"                    yaml:"type"`
	Entrypoint string           `json:"entrypoint,omitempty"    yaml:"entrypoint,omitempty"`
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
}

// CompatSpec 兼容性约束
type CompatSpec struct {
	MinKernelVersion string `json:"min_kernel_version,omitempty" yaml:"min_kernel_version,omitempty"`
	MaxKernelVersion string `json:"max_kernel_version,omitempty" yaml:"max_kernel_version,omitempty"`
}

// HealthSpec 健康检查配置
type HealthSpec struct {
	PingIntervalSeconds int `json:"ping_interval_seconds,omitempty" yaml:"ping_interval_seconds,omitempty"`
	PingTimeoutSeconds  int `json:"ping_timeout_seconds,omitempty"  yaml:"ping_timeout_seconds,omitempty"`
	MaxMissedPings      int `json:"max_missed_pings,omitempty"      yaml:"max_missed_pings,omitempty"`
}
