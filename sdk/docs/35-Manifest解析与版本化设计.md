# 35. Manifest 解析与版本化设计

> **状态**：v1 · 2026-04-16
> **上位规格**：[33-Brain-Manifest规格.md](./33-Brain-Manifest规格.md)
> **关联文档**：[35-BrainPool实现设计.md](./35-BrainPool实现设计.md) / [32-v3-Brain架构.md](./32-v3-Brain架构.md) §7.5
> **实现目标**：`sdk/kernel/manifest/` 包（新增）

---

## 0. 文档定位

[33-Brain-Manifest规格.md](./33-Brain-Manifest规格.md) 是 Manifest 的**格式契约**，定义了字段语义和取值规则。

本文档是**实现级细化**，覆盖：

- Go 解析器的完整流程（发现 → 读取 → 校验 → 注册）
- JSON Schema 校验规则与枚举约束
- `schema_version` 升级策略与兼容性矩阵
- 可选扩展字段的分阶段引入计划
- 从硬编码注册迁移到 manifest-driven 自动发现
- 四种 `runtime.type` 的 manifest 声明差异
- Manifest → `BrainPoolRegistration` 转换逻辑
- brain 升级时的热更新处理流程

---

## 1. Manifest 解析器完整设计

### 1.1 包结构

```
sdk/kernel/manifest/
├── loader.go          # 发现 + 读取 + 反序列化
├── validator.go       # Schema 校验（必填字段 / 类型 / 枚举 / 兼容性）
├── registry.go        # 已加载 manifest 的内存注册表
├── watcher.go         # 文件系统热更新监听
├── converter.go       # Manifest → BrainPoolRegistration 转换
├── schema/
│   └── v1.json        # JSON Schema（供外部工具验证）
└── manifest_test.go
```

### 1.2 核心数据结构

```go
// sdk/kernel/manifest/types.go

// Manifest 是 brain.json 的完整 Go 结构体表示。
// 字段顺序与 JSON Schema v1 保持一致。
type Manifest struct {
    // ---- 必填核心字段（7 个）----

    SchemaVersion int    `json:"schema_version"`
    Kind          string `json:"kind"`
    Name          string `json:"name"`
    BrainVersion  string `json:"brain_version"`
    Capabilities  []string `json:"capabilities"`
    Runtime       RuntimeSpec `json:"runtime"`
    Policy        PolicySpec  `json:"policy"`

    // ---- 可选扩展字段 ----

    Description  string          `json:"description,omitempty"`
    TaskPatterns []string        `json:"task_patterns,omitempty"`
    Compatibility *CompatSpec    `json:"compatibility,omitempty"`
    License      *LicenseSpec   `json:"license,omitempty"`
    Health       *HealthSpec    `json:"health,omitempty"`
    Metadata     map[string]any `json:"metadata,omitempty"`

    // ---- 解析时注入（不在 JSON 文件中）----

    // SourcePath 是这个 manifest 文件的绝对路径。
    // 用于计算相对路径（entrypoint、mcp_bindings 等）。
    SourcePath string `json:"-"`
}

// RuntimeSpec 描述 brain 的运行时装配方式。
type RuntimeSpec struct {
    Type        RuntimeType `json:"type"`
    Entrypoint  string      `json:"entrypoint,omitempty"`
    Args        []string    `json:"args,omitempty"`
    Env         map[string]string `json:"env,omitempty"`
    MCPBindings []string    `json:"mcp_bindings,omitempty"`
    Endpoint    string      `json:"endpoint,omitempty"`
    AuthRef     string      `json:"auth_ref,omitempty"`
}

// RuntimeType 是 runtime.type 的枚举
type RuntimeType string

const (
    RuntimeNative    RuntimeType = "native"
    RuntimeMCPBacked RuntimeType = "mcp-backed"
    RuntimeHybrid    RuntimeType = "hybrid"
    RuntimeRemote    RuntimeType = "remote"
)

// PolicySpec 是 brain 的执行门禁声明。
type PolicySpec struct {
    ApprovalClass      string `json:"approval_class,omitempty"`
    ToolScope          string `json:"tool_scope,omitempty"`
    ApprovalMode       string `json:"approval_mode,omitempty"`
    SandboxProfile     string `json:"sandbox_profile,omitempty"`
    ActiveToolsProfile string `json:"active_tools_profile,omitempty"`
}

// CompatSpec 是兼容性声明。
type CompatSpec struct {
    Protocol     string `json:"protocol"`
    TestedKernel string `json:"tested_kernel"`
    MinKernel    string `json:"min_kernel,omitempty"`
    MaxKernel    string `json:"max_kernel,omitempty"`
}

// LicenseSpec 是运行授权声明。
type LicenseSpec struct {
    Required bool     `json:"required"`
    Edition  string   `json:"edition,omitempty"`
    Features []string `json:"features,omitempty"`
}

// HealthSpec 是存活门槛声明。
type HealthSpec struct {
    StartupTimeoutMs   int      `json:"startup_timeout_ms,omitempty"`
    HeartbeatTimeoutMs int      `json:"heartbeat_timeout_ms,omitempty"`
    ExpectedMethods    []string `json:"expected_methods,omitempty"`
}
```

### 1.3 解析流程：发现 → 读取 → 校验 → 注册

```
┌──────────────────────────────────────────────────────────────────┐
│                        ManifestLoader                            │
│                                                                  │
│  1. Discover   2. Read     3. Validate   4. Register             │
│  ──────────    ──────────  ──────────    ──────────              │
│  扫描搜索路径  读取 JSON    Schema 校验   写入注册表              │
│  找到所有      反序列化到  + 枚举校验    触发 BrainPool           │
│  brain.json    Manifest{}  + 版本兼容    Register 调用           │
└──────────────────────────────────────────────────────────────────┘
```

```go
// sdk/kernel/manifest/loader.go

// ManifestLoader 是 Manifest 发现和解析的入口。
type ManifestLoader struct {
    // SearchPaths 是要扫描的目录列表，按优先级从高到低排列。
    // 典型值：["~/.brain/brains/", "/usr/local/brain/brains/"]
    SearchPaths []string

    // KernelVersion 用于兼容性校验（min_kernel/max_kernel）。
    KernelVersion string

    // ProtocolVersion 用于 compatibility.protocol 校验。
    ProtocolVersion string

    validator *Validator
    registry  *Registry
}

// LoadAll 执行完整的发现 → 读取 → 校验 → 注册流程。
// 返回成功注册的 manifest 列表和所有遇到的非致命错误。
// 严重错误（磁盘不可读）返回 error。
func (l *ManifestLoader) LoadAll(ctx context.Context) ([]*Manifest, []LoadWarning, error) {
    // Step 1：发现所有 brain.json 文件
    paths, err := l.discover(ctx)
    if err != nil {
        return nil, nil, fmt.Errorf("manifest discovery failed: %w", err)
    }

    var loaded []*Manifest
    var warnings []LoadWarning

    for _, path := range paths {
        // Step 2：读取并反序列化
        m, err := l.readFile(path)
        if err != nil {
            warnings = append(warnings, LoadWarning{Path: path, Err: err, Phase: "read"})
            continue
        }

        // Step 3：Schema 校验
        if errs := l.validator.Validate(m); len(errs) > 0 {
            warnings = append(warnings, LoadWarning{
                Path:   path,
                Err:    fmt.Errorf("validation errors: %v", errs),
                Phase:  "validate",
            })
            continue
        }

        // Step 4：注册到内存注册表
        if err := l.registry.Register(m); err != nil {
            warnings = append(warnings, LoadWarning{Path: path, Err: err, Phase: "register"})
            continue
        }

        loaded = append(loaded, m)
    }

    return loaded, warnings, nil
}

// readFile 读取单个 brain.json 文件并反序列化。
func (l *ManifestLoader) readFile(path string) (*Manifest, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("read file: %w", err)
    }

    var m Manifest
    if err := json.Unmarshal(data, &m); err != nil {
        return nil, fmt.Errorf("json unmarshal: %w", err)
    }

    m.SourcePath = path
    return &m, nil
}

// LoadWarning 是单个 manifest 处理的非致命错误。
type LoadWarning struct {
    Path  string
    Phase string // "read" | "validate" | "register"
    Err   error
}
```

---

## 2. Schema 校验规则

### 2.1 JSON Schema v1

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://brain.leef-l.com/schema/manifest/v1.json",
  "title": "Brain Manifest v1",
  "type": "object",
  "required": [
    "schema_version",
    "kind",
    "name",
    "brain_version",
    "capabilities",
    "runtime",
    "policy"
  ],
  "additionalProperties": false,
  "properties": {
    "schema_version": {
      "type": "integer",
      "const": 1,
      "description": "Manifest schema 版本，v1 固定为 1"
    },
    "kind": {
      "type": "string",
      "pattern": "^[a-z][a-z0-9-]*$",
      "minLength": 1,
      "maxLength": 64,
      "description": "brain 的稳定标识符，小写字母、数字、连字符"
    },
    "name": {
      "type": "string",
      "minLength": 1,
      "maxLength": 128,
      "description": "展示名，不参与协议匹配"
    },
    "brain_version": {
      "type": "string",
      "pattern": "^[0-9]+\\.[0-9]+\\.[0-9]+(-[a-zA-Z0-9.]+)?(\\+[a-zA-Z0-9.]+)?$",
      "description": "brain 自身的 semver 版本号"
    },
    "capabilities": {
      "type": "array",
      "minItems": 1,
      "items": {
        "type": "string",
        "pattern": "^[a-z][a-z0-9_]*\\.[a-z][a-z0-9_]*$",
        "description": "capability 标签，格式为 domain.verb"
      },
      "uniqueItems": true
    },
    "runtime": {
      "type": "object",
      "required": ["type"],
      "properties": {
        "type": {
          "type": "string",
          "enum": ["native", "mcp-backed", "hybrid", "remote"],
          "description": "运行时类型，决定 brain 怎么跑"
        },
        "entrypoint": {
          "type": "string",
          "description": "sidecar 二进制的相对路径（相对于 manifest 所在目录）"
        },
        "args": {
          "type": "array",
          "items": { "type": "string" }
        },
        "env": {
          "type": "object",
          "additionalProperties": { "type": "string" }
        },
        "mcp_bindings": {
          "type": "array",
          "items": {
            "type": "string",
            "description": "MCP binding 配置文件路径（相对路径）"
          }
        },
        "endpoint": {
          "type": "string",
          "format": "uri",
          "description": "remote runtime 的 HTTPS 端点"
        },
        "auth_ref": {
          "type": "string",
          "description": "vault 引用，例如 vault://brain/browser-prod"
        }
      },
      "if": {
        "properties": { "type": { "const": "native" } }
      },
      "then": {
        "required": ["entrypoint"]
      },
      "else": {
        "if": {
          "properties": { "type": { "enum": ["mcp-backed", "hybrid"] } }
        },
        "then": {
          "required": ["entrypoint", "mcp_bindings"]
        },
        "else": {
          "if": {
            "properties": { "type": { "const": "remote" } }
          },
          "then": {
            "required": ["endpoint"]
          }
        }
      }
    },
    "policy": {
      "type": "object",
      "properties": {
        "approval_class": {
          "type": "string",
          "enum": [
            "readonly",
            "workspace-read",
            "workspace-write",
            "network",
            "privileged"
          ],
          "description": "审批等级，从低到高：readonly < workspace-read < workspace-write < network < privileged"
        },
        "tool_scope": {
          "type": "string",
          "pattern": "^delegate\\.[a-z][a-z0-9-]*$"
        },
        "approval_mode": {
          "type": "string",
          "enum": ["plan", "default", "accept-edits", "auto"]
        },
        "sandbox_profile": {
          "type": "string"
        },
        "active_tools_profile": {
          "type": "string"
        }
      },
      "additionalProperties": false
    },
    "description": {
      "type": "string",
      "maxLength": 512
    },
    "task_patterns": {
      "type": "array",
      "items": { "type": "string" }
    },
    "compatibility": {
      "type": "object",
      "required": ["protocol", "tested_kernel"],
      "properties": {
        "protocol": {
          "type": "string",
          "pattern": "^[0-9]+\\.[0-9]+$"
        },
        "tested_kernel": {
          "type": "string"
        },
        "min_kernel": {
          "type": "string"
        },
        "max_kernel": {
          "type": "string"
        }
      }
    },
    "license": {
      "type": "object",
      "required": ["required"],
      "properties": {
        "required": { "type": "boolean" },
        "edition": {
          "type": "string",
          "enum": ["free", "pro", "enterprise"]
        },
        "features": {
          "type": "array",
          "items": { "type": "string" }
        }
      }
    },
    "health": {
      "type": "object",
      "properties": {
        "startup_timeout_ms": {
          "type": "integer",
          "minimum": 1000,
          "maximum": 120000
        },
        "heartbeat_timeout_ms": {
          "type": "integer",
          "minimum": 1000,
          "maximum": 300000
        },
        "expected_methods": {
          "type": "array",
          "items": { "type": "string" }
        }
      }
    },
    "metadata": {
      "type": "object",
      "description": "发布者自定义展示/索引元数据，不承载运行时关键字段"
    }
  }
}
```

### 2.2 Go 校验器实现

```go
// sdk/kernel/manifest/validator.go

// ValidationError 是单条校验错误。
type ValidationError struct {
    Field   string // JSON 路径，例如 "runtime.type"
    Message string
    Value   any
}

func (e ValidationError) Error() string {
    return fmt.Sprintf("field %q: %s (got %v)", e.Field, e.Message, e.Value)
}

// Validator 执行完整的 Manifest 校验。
type Validator struct {
    KernelVersion   string
    ProtocolVersion string
    SupportedSchemaVersions []int
}

// Validate 执行所有校验层，返回所有错误（不短路）。
func (v *Validator) Validate(m *Manifest) []ValidationError {
    var errs []ValidationError

    errs = append(errs, v.validateRequired(m)...)
    errs = append(errs, v.validateTypes(m)...)
    errs = append(errs, v.validateEnums(m)...)
    errs = append(errs, v.validateRuntime(m)...)
    errs = append(errs, v.validateCompatibility(m)...)

    return errs
}

// validateRequired 校验必填字段非空/非零。
func (v *Validator) validateRequired(m *Manifest) []ValidationError {
    var errs []ValidationError

    checks := []struct {
        field string
        empty bool
    }{
        {"schema_version", m.SchemaVersion == 0},
        {"kind", m.Kind == ""},
        {"name", m.Name == ""},
        {"brain_version", m.BrainVersion == ""},
        {"capabilities", len(m.Capabilities) == 0},
        {"runtime", m.Runtime.Type == ""},
        // policy 是必填字段，但允许空结构体（所有子字段可选）
        // 所以只确认 policy key 存在即可（JSON 反序列化已保证）
    }

    for _, c := range checks {
        if c.empty {
            errs = append(errs, ValidationError{
                Field:   c.field,
                Message: "required field is missing or empty",
            })
        }
    }

    return errs
}

// validateEnums 校验枚举字段的取值合法性。
func (v *Validator) validateEnums(m *Manifest) []ValidationError {
    var errs []ValidationError

    // schema_version
    supported := false
    for _, sv := range v.SupportedSchemaVersions {
        if m.SchemaVersion == sv {
            supported = true
            break
        }
    }
    if !supported {
        errs = append(errs, ValidationError{
            Field:   "schema_version",
            Message: fmt.Sprintf("unsupported schema version, supported: %v", v.SupportedSchemaVersions),
            Value:   m.SchemaVersion,
        })
    }

    // runtime.type
    validRuntimeTypes := map[RuntimeType]bool{
        RuntimeNative: true, RuntimeMCPBacked: true,
        RuntimeHybrid: true, RuntimeRemote: true,
    }
    if !validRuntimeTypes[m.Runtime.Type] {
        errs = append(errs, ValidationError{
            Field:   "runtime.type",
            Message: "must be one of: native, mcp-backed, hybrid, remote",
            Value:   m.Runtime.Type,
        })
    }

    // kind 格式
    if matched, _ := regexp.MatchString(`^[a-z][a-z0-9-]*$`, m.Kind); !matched {
        errs = append(errs, ValidationError{
            Field:   "kind",
            Message: "must match pattern ^[a-z][a-z0-9-]*$",
            Value:   m.Kind,
        })
    }

    // capabilities 格式
    capPattern := regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)
    seen := map[string]bool{}
    for i, cap := range m.Capabilities {
        if !capPattern.MatchString(cap) {
            errs = append(errs, ValidationError{
                Field:   fmt.Sprintf("capabilities[%d]", i),
                Message: "must match pattern domain.verb (e.g. web.browse)",
                Value:   cap,
            })
        }
        if seen[cap] {
            errs = append(errs, ValidationError{
                Field:   fmt.Sprintf("capabilities[%d]", i),
                Message: "duplicate capability",
                Value:   cap,
            })
        }
        seen[cap] = true
    }

    // policy.approval_class 枚举（若存在）
    if m.Policy.ApprovalClass != "" {
        validClasses := map[string]bool{
            "readonly": true, "workspace-read": true,
            "workspace-write": true, "network": true, "privileged": true,
        }
        if !validClasses[m.Policy.ApprovalClass] {
            errs = append(errs, ValidationError{
                Field:   "policy.approval_class",
                Message: "must be one of: readonly, workspace-read, workspace-write, network, privileged",
                Value:   m.Policy.ApprovalClass,
            })
        }
    }

    return errs
}

// validateRuntime 校验 runtime 字段的条件约束。
func (v *Validator) validateRuntime(m *Manifest) []ValidationError {
    var errs []ValidationError
    r := m.Runtime

    switch r.Type {
    case RuntimeNative:
        if r.Entrypoint == "" {
            errs = append(errs, ValidationError{
                Field:   "runtime.entrypoint",
                Message: "required for runtime.type=native",
            })
        }
    case RuntimeMCPBacked:
        if r.Entrypoint == "" {
            errs = append(errs, ValidationError{
                Field:   "runtime.entrypoint",
                Message: "required for runtime.type=mcp-backed",
            })
        }
        if len(r.MCPBindings) == 0 {
            errs = append(errs, ValidationError{
                Field:   "runtime.mcp_bindings",
                Message: "required for runtime.type=mcp-backed (at least one binding)",
            })
        }
    case RuntimeHybrid:
        if r.Entrypoint == "" {
            errs = append(errs, ValidationError{
                Field:   "runtime.entrypoint",
                Message: "required for runtime.type=hybrid",
            })
        }
        if len(r.MCPBindings) == 0 {
            errs = append(errs, ValidationError{
                Field:   "runtime.mcp_bindings",
                Message: "required for runtime.type=hybrid (at least one binding)",
            })
        }
    case RuntimeRemote:
        if r.Endpoint == "" {
            errs = append(errs, ValidationError{
                Field:   "runtime.endpoint",
                Message: "required for runtime.type=remote",
            })
        }
    }

    // 验证 entrypoint 实际存在（仅对本地 runtime）
    if r.Entrypoint != "" && r.Type != RuntimeRemote {
        absPath := resolveRelativePath(m.SourcePath, r.Entrypoint)
        if _, err := os.Stat(absPath); os.IsNotExist(err) {
            errs = append(errs, ValidationError{
                Field:   "runtime.entrypoint",
                Message: fmt.Sprintf("file not found at resolved path: %s", absPath),
                Value:   r.Entrypoint,
            })
        }
    }

    return errs
}

// validateCompatibility 校验兼容性约束（仅在有 compatibility 字段时执行）。
func (v *Validator) validateCompatibility(m *Manifest) []ValidationError {
    if m.Compatibility == nil {
        return nil // compatibility 是可选字段
    }

    var errs []ValidationError
    c := m.Compatibility

    // min_kernel 版本门禁
    if c.MinKernel != "" {
        if semverLess(v.KernelVersion, c.MinKernel) {
            errs = append(errs, ValidationError{
                Field:   "compatibility.min_kernel",
                Message: fmt.Sprintf("current kernel %s is below required minimum %s", v.KernelVersion, c.MinKernel),
            })
        }
    }

    // max_kernel 版本门禁
    if c.MaxKernel != "" {
        if semverGreater(v.KernelVersion, c.MaxKernel) {
            errs = append(errs, ValidationError{
                Field:   "compatibility.max_kernel",
                Message: fmt.Sprintf("current kernel %s exceeds declared maximum %s", v.KernelVersion, c.MaxKernel),
            })
        }
    }

    return errs
}

// resolveRelativePath 将 manifest 内的相对路径解析为绝对路径。
// manifestPath 是 brain.json 的绝对路径。
func resolveRelativePath(manifestPath, rel string) string {
    dir := filepath.Dir(manifestPath)
    return filepath.Join(dir, rel)
}
```

---

## 3. 版本化策略

### 3.1 设计原则

`schema_version` 是 Manifest 的**格式版本**，不是 brain 的版本，也不是 kernel 的版本。

它遵循一个简单规则：

> **只有 breaking change 才升级 schema_version。新增可选字段不触发版本升级。**

### 3.2 Breaking Change 定义

以下情况构成 breaking change，必须升 `schema_version`：

| 变更类型 | 示例 | 是否 Breaking |
|---------|------|---------------|
| 删除现有字段 | 删除 `capabilities` | 是 |
| 必填字段语义变更 | `kind` 从字符串改为对象 | 是 |
| 枚举值缩减 | 移除 `runtime.type=native` | 是 |
| 必填字段新增 | 新增 `owner` 为必填 | 是 |
| 可选字段新增 | 新增 `task_patterns` | **否** |
| 枚举值扩充 | 新增 `runtime.type=wasm` | **否** |
| 字段语义澄清 | 明确 `name` 只用于展示 | **否** |

### 3.3 向前/向后兼容矩阵

```
kernel 支持的 schema_version：[1, 2]
brain.json 声明的 schema_version：?

┌────────────────┬────────────────┬──────────────────────────────────┐
│ brain.json v=  │ kernel 支持范围 │ 行为                              │
├────────────────┼────────────────┼──────────────────────────────────┤
│ 1              │ [1, 2]         │ 正常加载（向后兼容，最常见情况）    │
│ 2              │ [1, 2]         │ 正常加载（当前最新版）              │
│ 3              │ [1, 2]         │ 拒绝加载，警告用户升级 kernel       │
│ 0              │ [1, 2]         │ 拒绝加载，无效 schema_version       │
│ 1              │ [2]            │ 降级加载（向前兼容，触发迁移警告）  │
└────────────────┴────────────────┴──────────────────────────────────┘
```

**向前兼容**（旧 brain，新 kernel）：

- kernel 识别旧 schema_version
- 以兼容模式解析（忽略新必填字段的缺失，填充默认值）
- 发出 deprecation 警告，建议 brain 开发者升级 manifest

**向后兼容**（新 brain，旧 kernel）：

- 旧 kernel 遇到未知 schema_version → 拒绝加载
- 错误信息应明确告知当前 kernel 支持的版本范围
- 不允许"降级解析"：旧 kernel 不应猜测新 schema 的含义

### 3.4 schema_version 升级流程

```
1. RFC 阶段（提案）
   - 在 sdk/docs/ 写明 breaking change 内容
   - 与所有 brain 开发者同步（Marketplace 公告）

2. 新版本发布
   - 更新 sdk/kernel/manifest/schema/v{N}.json
   - 更新 Validator.SupportedSchemaVersions，加入新版本号
   - 旧版本保留在支持列表中至少 6 个月

3. 过渡期（6 个月）
   - 同时支持旧版和新版 schema_version
   - 旧版 brain.json 加载时打印 deprecation 警告

4. 废弃
   - 从 SupportedSchemaVersions 移除旧版本
   - 加载旧版 brain.json 返回 hard error
```

---

## 4. 可选扩展字段的引入时机

### 4.1 分阶段引入计划

以下是 6 个可选扩展字段的引入时机，按 Phase 划分：

| 字段 | 引入 Phase | 默认值 | 引入理由 |
|------|-----------|--------|---------|
| `description` | Phase A（当前） | `""` | Marketplace/UI 展示，立即可用 |
| `task_patterns` | Phase A（当前） | `[]` | Central 委托路由参考，越早越好 |
| `compatibility` | Phase A（当前） | `nil`（不校验） | 安装期门禁，Phase A 建议加 |
| `health` | Phase B | 见下表 | 需要 Pool 健康检查基础设施就绪 |
| `license` | Phase B | `{required: false}` | 需要 LicenseGate 运行时就绪 |
| `metadata` | Phase C | `nil` | Marketplace 上线时才有实际价值 |

### 4.2 各字段缺省行为

```go
// 当可选字段缺失时，解析器填充的默认值。
// 在 loader.go 的 readFile 函数末尾调用。
func applyDefaults(m *Manifest) {
    // description：空字符串，不填充
    // task_patterns：nil slice，路由时跳过

    if m.Compatibility == nil {
        // Phase A：兼容性字段缺失时不做版本门禁
        // Phase B 后可以改为要求至少声明 protocol
    }

    if m.Health == nil {
        m.Health = &HealthSpec{
            StartupTimeoutMs:   10000, // 10s
            HeartbeatTimeoutMs: 30000, // 30s
        }
    } else {
        if m.Health.StartupTimeoutMs == 0 {
            m.Health.StartupTimeoutMs = 10000
        }
        if m.Health.HeartbeatTimeoutMs == 0 {
            m.Health.HeartbeatTimeoutMs = 30000
        }
    }

    if m.License == nil {
        m.License = &LicenseSpec{Required: false}
    }
}
```

---

## 5. Manifest 发现机制

### 5.1 发现策略演进

```
Phase A（当前）         Phase B                Phase C
──────────────────────  ─────────────────────  ──────────────────────
硬编码注册               半自动发现              完全 manifest-driven
                        
Orchestrator 直接写死    扫描约定目录             环境变量 + 多路径扫描
Brains 列表             找到 brain.json          + 子目录递归发现
                        自动调用 Register        + 远端 Manifest 拉取
```

### 5.2 目录约定

```
~/.brain/
├── brains/                    # 用户安装的 brain
│   ├── browser/
│   │   ├── brain.json         # ← Manifest（约定文件名）
│   │   ├── bin/
│   │   │   └── brain-browser  # ← entrypoint（相对路径 bin/brain-browser）
│   │   └── bindings/
│   │       └── mcp/
│   │           └── fetch.json
│   ├── data/
│   │   ├── brain.json
│   │   └── bin/
│   │       └── brain-data
│   └── quant/
│       ├── brain.json
│       └── bin/
│           └── brain-quant
└── config.yaml
```

**文件命名约定**：

- Manifest 文件名固定为 `brain.json`（不支持其他名称）
- 目录名 SHOULD 与 `kind` 字段一致（便于 human 定位，不强制）
- 子目录结构由 brain 开发者自定义，只要 entrypoint 路径正确即可

### 5.3 扫描逻辑

```go
// sdk/kernel/manifest/loader.go

// discover 扫描 SearchPaths，返回所有 brain.json 的绝对路径。
// 同一个 kind 只取第一个发现的（SearchPaths 优先级从高到低）。
func (l *ManifestLoader) discover(ctx context.Context) ([]string, error) {
    seenKinds := map[string]string{} // kind → path，用于去重
    var result []string

    for _, searchPath := range l.SearchPaths {
        // 展开 ~
        expanded, err := expandHome(searchPath)
        if err != nil {
            return nil, err
        }

        // 列出 searchPath 的直接子目录
        entries, err := os.ReadDir(expanded)
        if err != nil {
            if os.IsNotExist(err) {
                continue // 目录不存在，跳过
            }
            return nil, fmt.Errorf("readdir %s: %w", expanded, err)
        }

        for _, entry := range entries {
            if !entry.IsDir() {
                continue
            }

            manifestPath := filepath.Join(expanded, entry.Name(), "brain.json")
            if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
                continue // 没有 brain.json，跳过
            }

            // 快速预读 kind 字段，用于去重
            kind, err := peekKind(manifestPath)
            if err != nil {
                // 无效 JSON，后续完整解析会报错
                result = append(result, manifestPath)
                continue
            }

            if existingPath, ok := seenKinds[kind]; ok {
                // 同一 kind 已在更高优先级路径找到，跳过
                log.Printf("manifest: skipping %s (kind=%q already registered from %s)",
                    manifestPath, kind, existingPath)
                continue
            }

            seenKinds[kind] = manifestPath
            result = append(result, manifestPath)
        }
    }

    return result, nil
}

// peekKind 只读取 brain.json 的 kind 字段，避免完整解析开销。
func peekKind(path string) (string, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return "", err
    }
    var partial struct {
        Kind string `json:"kind"`
    }
    if err := json.Unmarshal(data, &partial); err != nil {
        return "", err
    }
    return partial.Kind, nil
}
```

### 5.4 默认搜索路径

```go
// DefaultSearchPaths 返回平台默认的 Manifest 搜索路径。
// 路径按优先级从高到低排列。
func DefaultSearchPaths() []string {
    home, _ := os.UserHomeDir()
    paths := []string{
        filepath.Join(home, ".brain", "brains"),         // 用户安装
        "/usr/local/brain/brains",                        // 系统级安装
        "/opt/brain/brains",                              // 容器/包管理安装
    }

    // 环境变量覆盖（多个路径用冒号分隔）
    if env := os.Getenv("BRAIN_MANIFEST_PATH"); env != "" {
        extra := filepath.SplitList(env)
        paths = append(extra, paths...) // 环境变量优先级最高
    }

    return paths
}
```

---

## 6. runtime 字段细化

四种 runtime 类型在 manifest 声明、进程管理、连接方式上有明显差异：

### 6.1 native

```json
{
  "runtime": {
    "type": "native",
    "entrypoint": "bin/brain-browser",
    "args": ["--log-level=info"],
    "env": {
      "BRAIN_DATA_DIR": "~/.brain/data/browser"
    }
  }
}
```

| 特性 | 说明 |
|------|------|
| 进程来源 | 本地 sidecar 二进制，由 Pool 直接 exec |
| 协议实现 | sidecar 自己实现 `brain/execute`、`tools/list` 等方法 |
| entrypoint | 必填，相对路径（相对于 brain.json 所在目录） |
| mcp_bindings | 不允许（不要混入 MCP 概念） |
| endpoint | 不允许 |
| Pool 策略 | 通常 `shared-service` 或 `exclusive-session` |
| Phase A 支持 | 完整支持 |

### 6.2 mcp-backed

```json
{
  "runtime": {
    "type": "mcp-backed",
    "entrypoint": "bin/brain-data-mcp",
    "mcp_bindings": [
      "bindings/mcp/postgres.json",
      "bindings/mcp/fetch.json"
    ]
  }
}
```

| 特性 | 说明 |
|------|------|
| 进程来源 | 本地 sidecar 二进制（封装层），由 Pool exec |
| 工具来源 | 通过 MCP binding 文件描述的外部 MCP server |
| entrypoint | 必填（封装层二进制） |
| mcp_bindings | 必填，至少 1 个 binding 文件 |
| binding 文件格式 | 指向 MCP binding JSON，描述 MCP server 的连接参数和工具映射 |
| Pool 策略 | 通常 `shared-service` |
| Phase A 支持 | 支持（mcp_bindings 路径解析，MCP server 连接由 sidecar 内部处理） |

**重要约束**：MCP server 本身不是 brain。`mcp-backed` 是 brain 的内部实现方式，对 Kernel 完全透明。

### 6.3 hybrid

```json
{
  "runtime": {
    "type": "hybrid",
    "entrypoint": "bin/brain-browser-pro",
    "mcp_bindings": [
      "bindings/mcp/network.json"
    ],
    "env": {
      "PRO_LICENSE_PATH": "~/.brain/licenses/browser-pro.json"
    }
  }
}
```

| 特性 | 说明 |
|------|------|
| 进程来源 | 本地 sidecar 二进制（同时有本地工具和 MCP 能力） |
| 工具来源 | 本地工具注册表 + MCP binding |
| entrypoint | 必填 |
| mcp_bindings | 必填，至少 1 个 |
| 典型用途 | 付费 Pro brain，本地工具提供核心能力，MCP 补充第三方能力 |
| Pool 策略 | 通常 `shared-service` |
| Phase A 支持 | 支持 |

### 6.4 remote

```json
{
  "runtime": {
    "type": "remote",
    "endpoint": "https://brain.example.com/v1/browser",
    "auth_ref": "vault://brain/browser-prod"
  }
}
```

| 特性 | 说明 |
|------|------|
| 进程来源 | 无本地进程，通过 HTTPS 连接远端 |
| 协议 | brain wire protocol over HTTP/2 或 WebSocket |
| entrypoint | 不允许（远端不需要本地二进制） |
| mcp_bindings | 不允许 |
| endpoint | 必填，HTTPS URI |
| auth_ref | 可选，vault 引用格式：`vault://secret-path` |
| Pool 策略 | 特殊：Pool 管理连接而非进程，`shared-service` 语义 |
| Phase A 支持 | 允许声明，但 Pool 实现延至 Phase B（返回 ErrNotImplemented） |

### 6.5 runtime 字段约束汇总

| 字段 | native | mcp-backed | hybrid | remote |
|------|--------|------------|--------|--------|
| `entrypoint` | 必填 | 必填 | 必填 | 禁止 |
| `args` | 可选 | 可选 | 可选 | 禁止 |
| `env` | 可选 | 可选 | 可选 | 禁止 |
| `mcp_bindings` | 禁止 | 必填 | 必填 | 禁止 |
| `endpoint` | 禁止 | 禁止 | 禁止 | 必填 |
| `auth_ref` | 禁止 | 禁止 | 禁止 | 可选 |

---

## 7. 与 BrainPool Registration 的映射

### 7.1 转换逻辑概述

```
Manifest（brain.json）
      │
      │  converter.go
      ▼
BrainPoolRegistration（pool.go）
      │
      │  pool.Register()
      ▼
kindPool（内部进程管理）
```

### 7.2 转换规则

```go
// sdk/kernel/manifest/converter.go

// ToPoolRegistration 将 Manifest 转换为 BrainPoolRegistration。
// 调用方需要传入 kernelBinDir 用于解析 entrypoint 的绝对路径。
func ToPoolRegistration(m *Manifest) (BrainPoolRegistration, error) {
    binary, err := resolveEntrypoint(m)
    if err != nil {
        return BrainPoolRegistration{}, fmt.Errorf("resolve entrypoint: %w", err)
    }

    strategy, stratCfg := deriveStrategy(m)

    return BrainPoolRegistration{
        Kind:     agent.Kind(m.Kind),
        Binary:   binary,
        Strategy: strategy,
        Config:   stratCfg,
        // Manifest 原始引用，供 Pool 在热更新时使用
        Manifest: m,
    }, nil
}

// resolveEntrypoint 将 manifest 中的相对 entrypoint 路径解析为绝对路径。
// remote runtime 返回空字符串。
func resolveEntrypoint(m *Manifest) (string, error) {
    if m.Runtime.Type == RuntimeRemote {
        return "", nil // remote 无本地二进制
    }

    if m.Runtime.Entrypoint == "" {
        return "", fmt.Errorf("entrypoint is empty for runtime.type=%s", m.Runtime.Type)
    }

    // 相对路径：相对于 brain.json 所在目录
    absPath := resolveRelativePath(m.SourcePath, m.Runtime.Entrypoint)

    // 验证文件存在且可执行
    info, err := os.Stat(absPath)
    if err != nil {
        return "", fmt.Errorf("entrypoint not found: %s", absPath)
    }
    if info.Mode()&0111 == 0 {
        return "", fmt.Errorf("entrypoint is not executable: %s", absPath)
    }

    return absPath, nil
}

// deriveStrategy 根据 Manifest 推导 Pool 进程策略。
//
// 推导规则：
//   - 如果 policy.approval_class 包含写操作（workspace-write, network, privileged）
//     → exclusive-session（避免多任务并发写冲突）
//   - 如果 runtime.type == remote
//     → shared-service（连接复用）
//   - 默认
//     → shared-service（无状态 brain 最常见情况）
//
// Phase B 可以在 Manifest 中新增 runtime.pool_strategy 显式声明，优先于推导。
func deriveStrategy(m *Manifest) (ProcessStrategy, StrategyConfig) {
    cfg := StrategyConfig{}

    // 从 health 字段读取超时配置
    if m.Health != nil {
        if ms := m.Health.StartupTimeoutMs; ms > 0 {
            cfg.StartTimeout = time.Duration(ms) * time.Millisecond
        }
        if ms := m.Health.HeartbeatTimeoutMs; ms > 0 {
            cfg.HealthCheckTimeout = time.Duration(ms) * time.Millisecond
        }
    }

    // 填充 defaults
    if cfg.StartTimeout == 0 {
        cfg.StartTimeout = 10 * time.Second
    }
    if cfg.HealthCheckTimeout == 0 {
        cfg.HealthCheckTimeout = 30 * time.Second
    }

    // 推导策略
    switch m.Policy.ApprovalClass {
    case "workspace-write", "network", "privileged":
        // 高风险操作 → 独占 session，避免并发写
        cfg.MaxInstances = 1
        cfg.WaitTimeout = 30 * time.Second
        return StrategyExclusiveSession, cfg
    }

    if m.Runtime.Type == RuntimeRemote {
        cfg.HealthCheckInterval = 30 * time.Second
        cfg.MaxConsecFailures = 3
        return StrategySharedService, cfg
    }

    // 默认：共享服务
    cfg.HealthCheckInterval = 30 * time.Second
    cfg.MaxConsecFailures = 3
    return StrategySharedService, cfg
}
```

### 7.3 BrainPoolRegistration 扩展

```go
// sdk/kernel/pool.go（在现有 BrainPoolRegistration 基础上新增 Manifest 字段）

// BrainPoolRegistration 注册一个 brain 到 Pool。
// Manifest 字段是可选的，由 manifest-driven 注册路径填充。
// 硬编码注册路径（Phase A）可以不填。
type BrainPoolRegistration struct {
    Kind     agent.Kind
    Binary   string
    Strategy ProcessStrategy
    Config   StrategyConfig

    // Manifest 是原始 manifest 对象，用于热更新对比和 Dashboard 展示。
    // 可选，硬编码注册时为 nil。
    Manifest *manifest.Manifest
}
```

### 7.4 硬编码注册 → Manifest 注册的迁移路径

```go
// Phase A（当前）：硬编码注册
// 位置：sdk/kernel/orchestrator.go 或 cmd/brain/main.go

pool.Register(BrainPoolRegistration{
    Kind:     agent.Kind("browser"),
    Binary:   "/usr/local/bin/brain-browser",
    Strategy: StrategySharedService,
    Config:   StrategyConfig{...},
})

// Phase B（目标）：manifest-driven 注册
// 位置：sdk/kernel/manifest/loader.go + pool.go

loader := manifest.NewManifestLoader(manifest.DefaultSearchPaths(), kernelVersion)
manifests, warnings, err := loader.LoadAll(ctx)
for _, w := range warnings {
    log.Printf("manifest warning: %v", w)
}
for _, m := range manifests {
    reg, err := manifest.ToPoolRegistration(m)
    if err != nil {
        log.Printf("skipping brain %q: %v", m.Kind, err)
        continue
    }
    if err := pool.Register(reg); err != nil {
        log.Printf("register brain %q failed: %v", m.Kind, err)
    }
}
```

---

## 8. 热更新

### 8.1 热更新的目标

brain 升级时（例如 `brain install browser@1.1.0`），做到：

- **不停服**：kernel 不重启，正在进行的 task 不中断
- **无缝切换**：新版本 brain 在旧版本处理完当前 task 后接管
- **回滚安全**：如果新版本 manifest 校验失败，继续使用旧版本

### 8.2 热更新触发条件

```
触发来源：
  1. brain install 命令写入新的 brain.json 后通知 kernel
  2. FileWatcher 检测到 brain.json 的 mtime 或内容变化
  3. /v1/brains/{kind}/reload HTTP API（手动触发）
```

### 8.3 热更新流程

```
┌─────────────────────────────────────────────────────────────────┐
│                       热更新流程                                  │
│                                                                   │
│  1. 检测变化          2. 校验新 Manifest      3. 决策             │
│  ─────────────        ──────────────────      ────────           │
│  FileWatcher 触发     重新加载并完整校验       校验通过 → 继续    │
│  或 API 触发          比较 kind 是否一致       校验失败 → 中止    │
│                       检查 breaking change                        │
│                                                                   │
│  4. 优雅切换          5. 替换注册             6. 日志与告警       │
│  ──────────           ──────────────          ────────────────── │
│  Pool.Drain(kind)     pool.Register(new)      记录版本变更        │
│  等待在途任务完成     启动新版 sidecar         Dashboard 更新     │
│  关闭旧版 sidecar     新任务走新实例                              │
└─────────────────────────────────────────────────────────────────┘
```

### 8.4 FileWatcher 实现

```go
// sdk/kernel/manifest/watcher.go

// Watcher 监听 brain.json 文件变化，触发热更新。
type Watcher struct {
    loader   *ManifestLoader
    pool     BrainPool
    debounce time.Duration // 防止写入中的瞬间触发，默认 500ms
}

// Watch 启动后台监听。ctx 取消时停止。
func (w *Watcher) Watch(ctx context.Context) error {
    fsWatcher, err := fsnotify.NewWatcher()
    if err != nil {
        return fmt.Errorf("create watcher: %w", err)
    }
    defer fsWatcher.Close()

    // 监听所有搜索路径下的 brains 目录
    for _, path := range w.loader.SearchPaths {
        expanded, _ := expandHome(path)
        if err := fsWatcher.Add(expanded); err != nil {
            // 目录不存在是正常情况，跳过
            continue
        }
    }

    var debounceTimers = map[string]*time.Timer{}

    for {
        select {
        case <-ctx.Done():
            return nil

        case event, ok := <-fsWatcher.Events:
            if !ok {
                return nil
            }

            // 只关注 brain.json 文件的变化
            if filepath.Base(event.Name) != "brain.json" {
                continue
            }
            if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
                continue
            }

            // 防抖：同一文件 500ms 内只触发一次
            path := event.Name
            if t, ok := debounceTimers[path]; ok {
                t.Reset(w.debounce)
            } else {
                debounceTimers[path] = time.AfterFunc(w.debounce, func() {
                    w.handleChange(ctx, path)
                    delete(debounceTimers, path)
                })
            }

        case err := <-fsWatcher.Errors:
            log.Printf("manifest watcher error: %v", err)
        }
    }
}

// handleChange 处理单个 brain.json 变化事件。
func (w *Watcher) handleChange(ctx context.Context, manifestPath string) {
    log.Printf("manifest: detected change in %s, starting hot-reload", manifestPath)

    // Step 1：读取并校验新 Manifest
    newManifest, err := w.loader.readFile(manifestPath)
    if err != nil {
        log.Printf("manifest: hot-reload aborted — read error: %v", err)
        return
    }
    if errs := w.loader.validator.Validate(newManifest); len(errs) > 0 {
        log.Printf("manifest: hot-reload aborted — validation failed: %v", errs)
        return
    }

    kind := agent.Kind(newManifest.Kind)

    // Step 2：检查 breaking change（kind 不允许在热更新中改变）
    existing := w.pool.Status()[kind]
    if !existing.HealthOK && existing.TotalInstances > 0 {
        // 如果旧版本 kind 不匹配，这是一个危险操作，需要人工确认
        log.Printf("manifest: hot-reload warning — kind mismatch, manual intervention required")
        return
    }

    // Step 3：Drain 旧版本（等待在途任务完成）
    drainCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
    defer cancel()

    log.Printf("manifest: draining old brain %q before hot-reload", kind)
    if err := w.pool.Drain(drainCtx, kind); err != nil {
        log.Printf("manifest: drain failed: %v, forcing reload anyway", err)
    }

    // Step 4：注册新版本
    reg, err := manifest.ToPoolRegistration(newManifest)
    if err != nil {
        log.Printf("manifest: hot-reload aborted — cannot build registration: %v", err)
        // 旧版本已 Drain，此时 brain 不可用。尝试用旧 manifest 重新注册。
        w.attemptRollback(ctx, kind)
        return
    }

    if err := w.pool.Register(reg); err != nil {
        log.Printf("manifest: hot-reload register failed: %v", err)
        w.attemptRollback(ctx, kind)
        return
    }

    // Step 5：预热新版本
    if err := w.pool.WarmUp(ctx, kind, 0); err != nil {
        log.Printf("manifest: hot-reload warmup warning: %v", err)
        // warmup 失败不阻止注册成功，下次 GetBrain 时会冷启动
    }

    // Step 6：更新注册表
    w.loader.registry.Register(newManifest)

    log.Printf("manifest: hot-reload complete — brain %q upgraded to %s",
        kind, newManifest.BrainVersion)
}

// attemptRollback 在热更新失败后尝试用旧 manifest 恢复。
func (w *Watcher) attemptRollback(ctx context.Context, kind agent.Kind) {
    old := w.loader.registry.Get(kind)
    if old == nil {
        log.Printf("manifest: rollback failed — no previous manifest for kind=%q", kind)
        return
    }

    reg, err := manifest.ToPoolRegistration(old)
    if err != nil {
        log.Printf("manifest: rollback failed — cannot rebuild old registration: %v", err)
        return
    }

    if err := w.pool.Register(reg); err != nil {
        log.Printf("manifest: rollback register failed: %v", err)
        return
    }

    log.Printf("manifest: rollback complete — brain %q restored to %s", kind, old.BrainVersion)
}
```

### 8.5 热更新中的版本对比

```go
// HotReloadDiff 分析新旧 Manifest 的差异，用于决定是否允许热更新。
type HotReloadDiff struct {
    Kind            bool // kind 是否相同（false = 拒绝热更新）
    VersionUpgrade  bool // brain_version 是否升级
    RuntimeChanged  bool // runtime.type 是否变化（需要完整 Drain）
    PolicyChanged   bool // policy 是否变化（需要重新鉴权）
    CapabAdded      []string // 新增的 capabilities
    CapabRemoved    []string // 删除的 capabilities（警告：可能破坏委托路由）
}

// Diff 计算两个 Manifest 的差异。
func Diff(old, new *Manifest) HotReloadDiff {
    d := HotReloadDiff{
        Kind:           old.Kind == new.Kind,
        VersionUpgrade: semverGreater(new.BrainVersion, old.BrainVersion),
        RuntimeChanged: old.Runtime.Type != new.Runtime.Type,
        PolicyChanged:  old.Policy != new.Policy,
    }

    oldCaps := toSet(old.Capabilities)
    newCaps := toSet(new.Capabilities)

    for cap := range newCaps {
        if !oldCaps[cap] {
            d.CapabAdded = append(d.CapabAdded, cap)
        }
    }
    for cap := range oldCaps {
        if !newCaps[cap] {
            d.CapabRemoved = append(d.CapabRemoved, cap)
        }
    }

    return d
}
```

### 8.6 热更新约束与边界

| 场景 | 处理方式 |
|------|---------|
| `kind` 在热更新中改变 | 拒绝热更新，要求手动停服更新 |
| `runtime.type` 变化（如 native → mcp-backed） | 允许，但需要完整 Drain 后重启 |
| `capabilities` 删除 | 允许，但打印警告（可能影响 Central 委托路由） |
| `policy.approval_class` 升级 | 允许，新会话使用新 policy |
| `schema_version` 升级 | 按版本兼容矩阵处理 |
| 新 Manifest 校验失败 | 中止热更新，旧版本继续运行 |
| Drain 超时（60s）| 强制继续，旧进程 kill -SIGTERM |

---

## 9. Manifest 注册表

### 9.1 Registry 设计

```go
// sdk/kernel/manifest/registry.go

// Registry 是已加载 Manifest 的内存注册表。
// 线程安全。
type Registry struct {
    mu        sync.RWMutex
    manifests map[string]*Manifest // kind → Manifest
}

func NewRegistry() *Registry {
    return &Registry{manifests: make(map[string]*Manifest)}
}

// Register 注册或更新一个 Manifest。
// 如果 kind 已存在，替换为新版本。
func (r *Registry) Register(m *Manifest) error {
    r.mu.Lock()
    defer r.mu.Unlock()

    if existing, ok := r.manifests[m.Kind]; ok {
        // 已存在：记录版本变更日志
        if existing.BrainVersion != m.BrainVersion {
            log.Printf("manifest registry: updating kind=%q version %s → %s",
                m.Kind, existing.BrainVersion, m.BrainVersion)
        }
    }

    r.manifests[m.Kind] = m
    return nil
}

// Get 获取指定 kind 的 Manifest。
func (r *Registry) Get(kind agent.Kind) *Manifest {
    r.mu.RLock()
    defer r.mu.RUnlock()
    return r.manifests[string(kind)]
}

// List 返回所有已注册 Manifest 的快照。
func (r *Registry) List() []*Manifest {
    r.mu.RLock()
    defer r.mu.RUnlock()
    result := make([]*Manifest, 0, len(r.manifests))
    for _, m := range r.manifests {
        result = append(result, m)
    }
    return result
}
```

---

## 10. 错误码设计

```go
// sdk/kernel/manifest/errors.go

var (
    // ErrManifestNotFound manifest 文件不存在
    ErrManifestNotFound = errors.New("manifest: brain.json not found")

    // ErrSchemaVersionUnsupported 不支持的 schema_version
    ErrSchemaVersionUnsupported = errors.New("manifest: unsupported schema_version")

    // ErrKindConflict 同一 kind 在多个路径冲突
    ErrKindConflict = errors.New("manifest: kind already registered from higher-priority path")

    // ErrEntrypointMissing entrypoint 文件不存在
    ErrEntrypointMissing = errors.New("manifest: entrypoint file not found")

    // ErrEntrypointNotExecutable entrypoint 不可执行
    ErrEntrypointNotExecutable = errors.New("manifest: entrypoint is not executable")

    // ErrKernelTooOld 当前 kernel 版本低于 min_kernel
    ErrKernelTooOld = errors.New("manifest: current kernel version is below min_kernel")

    // ErrKernelTooNew 当前 kernel 版本高于 max_kernel
    ErrKernelTooNew = errors.New("manifest: current kernel version exceeds max_kernel")

    // ErrHotReloadKindMismatch 热更新时 kind 发生变化
    ErrHotReloadKindMismatch = errors.New("manifest: kind cannot change during hot-reload")
)
```

---

## 11. 与现有代码的集成点

### 11.1 Phase A 集成：最小改动

Phase A 目标是**在不破坏现有硬编码注册的前提下**，引入 manifest 解析基础设施：

```go
// cmd/brain/main.go 或 sdk/kernel/orchestrator.go 启动时

// 尝试 manifest-driven 加载（不强制，失败则 fallback 到硬编码）
loader := manifest.NewManifestLoader(
    manifest.DefaultSearchPaths(),
    kernelVersion,
    protocolVersion,
)
loaded, warnings, err := loader.LoadAll(ctx)
if err != nil {
    log.Printf("manifest loader error: %v, falling back to hardcoded registration", err)
} else {
    for _, w := range warnings {
        log.Printf("manifest warning [%s] %s: %v", w.Phase, w.Path, w.Err)
    }
    for _, m := range loaded {
        reg, err := manifest.ToPoolRegistration(m)
        if err != nil {
            log.Printf("skip brain %q: %v", m.Kind, err)
            continue
        }
        // manifest 注册会覆盖同 kind 的硬编码注册
        pool.Register(reg)
    }
}

// 启动 FileWatcher（Phase A 可选）
if watchEnabled {
    watcher := manifest.NewWatcher(loader, pool, 500*time.Millisecond)
    go watcher.Watch(ctx)
}
```

### 11.2 Dashboard API 集成

```go
// /v1/brains API 在 Pool.Status() 基础上附加 Manifest 信息

type BrainStatusResponse struct {
    // 来自 BrainPoolStatus
    Kind           string `json:"kind"`
    Strategy       string `json:"strategy"`
    TotalInstances int    `json:"total_instances"`
    IdleInstances  int    `json:"idle_instances"`
    InUseInstances int    `json:"in_use_instances"`
    HealthOK       bool   `json:"health_ok"`

    // 来自 Manifest Registry
    BrainVersion string   `json:"brain_version,omitempty"`
    DisplayName  string   `json:"display_name,omitempty"`
    Capabilities []string `json:"capabilities,omitempty"`
    RuntimeType  string   `json:"runtime_type,omitempty"`
    Description  string   `json:"description,omitempty"`
}
```

---

## 12. 实现优先级

| 优先级 | 组件 | Phase |
|--------|------|-------|
| P0 | `types.go` — 数据结构定义 | Phase A |
| P0 | `validator.go` — 必填字段 + runtime 条件校验 | Phase A |
| P0 | `loader.go` — readFile + applyDefaults | Phase A |
| P1 | `registry.go` — 内存注册表 | Phase A |
| P1 | `converter.go` — ToPoolRegistration | Phase A |
| P1 | `loader.go#discover` — 目录扫描 | Phase A |
| P2 | `watcher.go` — FileWatcher 热更新 | Phase B |
| P2 | `validator.go#validateCompatibility` — 版本门禁 | Phase B |
| P3 | `schema/v1.json` — 正式 JSON Schema | Phase B |
| P3 | `/v1/brains` API Manifest 字段扩展 | Phase B |

---

## 13. 一句话结论

`Brain Manifest` 的解析器是 v3 从"硬编码配置"走向"manifest-driven 生态"的关键枢纽。

它的职责是：**把 brain.json 文件变成 Kernel 可以信任的 BrainPoolRegistration**——严格校验、安全转换、支持热更新、不停服升级。

Manifest 解析器不做策略决策，不管工具权限，只管**把一个磁盘上的 JSON 文件，变成内核里一个有名字、有能力、有进程的活着的 Brain**。
