# 35. Flow Edge 存储层与注册/发现机制设计

> **状态**：v1 · 2026-04-17
> **归属**：§7.10 Flow Edge（32-v3-Brain架构.md）的详细规格
> **Phase 映射**：Phase C-1 Workflow Engine 核心子系统
> **依赖文档**：[32-v3-Brain架构.md](./32-v3-Brain架构.md) §7.10 / §7.7.1

---

## 0. 设计目标与范围

§7.10 定义了 Flow Edge 的顶层模型：

- **materialized edge**：任务完成后写入 CAS，通过 ref 传递
- **streaming edge**：任务运行中通过 pipe/channel 实时传递，有三种 backend（pipe / ringbuf / queue）

本文件是 §7.10 的下一层设计，覆盖：

1. CAS 存储层接口 + 后端实现
2. materialized edge 完整数据流
3. Streaming PipeRegistry 接口与生命周期
4. 三种 streaming backend 统一抽象
5. Edge 注册与 runtime 发现
6. 背压与流控
7. 现有 mmap ring buffer 迁移方案
8. 错误处理

**不在范围内**：Workflow DAG 引擎本身（调度、拓扑排序）、Capability Lease、Dispatch Policy——这些在 §7.7/7.8 定义。

#### 与跨脑通信协议（35-14）的职责分界

| 层次 | 职责方 | 说明 |
|------|--------|------|
| **存储抽象** | Flow Edge（本文档） | CAS 内容寻址 + StreamBackend 接口定义 |
| **传输协议** | [跨脑通信协议](./35-跨脑通信协议设计.md) | Ring Buffer 帧格式、JSON-RPC 方法、背压策略 |
| **授权控制** | [语义审批](./35-语义审批分级设计.md) | 数据访问权限检查 |

> **StreamBackend 实现归属**：`StreamBackend` 接口定义在 Flow Edge 中（`sdk/flow/stream.go`），
> 三种后端中 `RingBufBackend` 的底层通信由跨脑通信协议（35-14 §2）提供。
> Flow Edge 只关心"写入/读取"语义，不关心传输细节。

> **背压降级路径**：当 `RingBufBackend` 触发 `SpillToSlow` 时，由跨脑通信层（35-14 §3.2.6）
> 负责通过 `brain.spill_frame` RPC 发送溢出帧，Flow Edge 层不感知降级过程。

---

## 1. CAS 存储层设计

### 1.1 设计原理

Content-Addressable Storage（内容寻址存储）的核心特性：

- **写入幂等**：同一份内容永远产生同一个 ref，重复写入安全
- **天然去重**：相同内容不占用额外存储
- **不可变**：通过 ref 读取的内容永远不会变化，安全的并发读
- **引用即校验**：拿到 ref 就知道内容的完整性，无需额外校验协议

这恰好是 materialized edge 需要的语义：Task A 写入，Task B 拿着 ref 去读，中间无论等多久内容都在、都一致。

### 1.2 核心接口

```go
// cas/store.go

package cas

import (
    "context"
    "io"
    "time"
)

// Ref 是内容的唯一标识符
// 格式：<algorithm>:<hex-digest>
// 示例："blake3:a1b2c3d4..."
type Ref string

// StoreInfo CAS 存储层的元信息
type StoreInfo struct {
    Algorithm   HashAlgorithm
    Backend     BackendKind
    TotalSize   int64          // 字节数
    ObjectCount int64
}

// ObjectMeta 对象元信息，不含内容本体
type ObjectMeta struct {
    Ref         Ref
    Size        int64
    CreatedAt   time.Time
    ContentType string         // MIME type，可选
    Tags        map[string]string
}

// CASStore 是 CAS 的核心接口
// 所有 materialized edge 的 producer/consumer 都通过这个接口交互
type CASStore interface {
    // Write 将 content 写入 CAS，返回 ref
    // 实现必须：先缓冲内容 → 计算 hash → 写入 → 返回 ref
    // 如果内容已存在，直接返回已有 ref（写入幂等）
    Write(ctx context.Context, content io.Reader, meta ObjectMeta) (Ref, error)

    // WriteBytes 便捷方法，直接写入 []byte
    WriteBytes(ctx context.Context, data []byte, meta ObjectMeta) (Ref, error)

    // Read 根据 ref 读取内容
    // 如果 ref 不存在，返回 ErrRefNotFound
    Read(ctx context.Context, ref Ref) (io.ReadCloser, error)

    // ReadBytes 便捷方法，读取全部内容到 []byte
    ReadBytes(ctx context.Context, ref Ref) ([]byte, error)

    // Stat 获取对象元信息，不读取内容本体
    Stat(ctx context.Context, ref Ref) (*ObjectMeta, error)

    // Exists 检查 ref 是否存在
    Exists(ctx context.Context, ref Ref) (bool, error)

    // Delete 删除对象（GC 调用，正常 edge 流程不应调用）
    Delete(ctx context.Context, ref Ref) error

    // Pin 对 ref 加 pin，GC 时不会回收（用于重要数据）
    Pin(ctx context.Context, ref Ref, reason string) error

    // Unpin 取消 pin
    Unpin(ctx context.Context, ref Ref, reason string) error

    // GC 执行垃圾回收
    GC(ctx context.Context, policy GCPolicy) (GCResult, error)

    // Info 返回存储层元信息
    Info(ctx context.Context) (StoreInfo, error)
}
```

### 1.3 Hash 算法选择

```go
// cas/hash.go

type HashAlgorithm string

const (
    // AlgoBlake3 推荐算法：比 SHA-256 快 4-8 倍，抗碰撞强度等同
    // 适合大量小对象（task 输出通常 < 10MB）
    AlgoBlake3 HashAlgorithm = "blake3"

    // AlgoSHA256 兼容算法：与 S3 / IPFS 生态互通
    // 适合需要外部对接的场景
    AlgoSHA256 HashAlgorithm = "sha256"
)

// HashAlgorithmDefault 默认使用 blake3
const HashAlgorithmDefault = AlgoBlake3

// ComputeRef 计算内容的 ref
func ComputeRef(algo HashAlgorithm, content []byte) (Ref, error) {
    switch algo {
    case AlgoBlake3:
        h := blake3.New()
        h.Write(content)
        digest := hex.EncodeToString(h.Sum(nil))
        return Ref("blake3:" + digest), nil
    case AlgoSHA256:
        h := sha256.New()
        h.Write(content)
        digest := hex.EncodeToString(h.Sum(nil))
        return Ref("sha256:" + digest), nil
    default:
        return "", ErrUnsupportedHashAlgorithm
    }
}
```

**为什么选 Blake3 而不是 SHA-256**：

| 指标 | SHA-256 | Blake3 |
|------|---------|--------|
| 速度（1MB 数据） | ~300MB/s | ~2GB/s |
| 安全强度 | 128 bit | 128 bit |
| 流式计算 | 支持 | 支持 |
| 并行化 | 不支持 | 内置并行 |
| Go 生态 | 标准库 | `lukechampine.com/blake3` |

Brain 场景下 task 输出频繁（Data Brain 每秒数百次），Blake3 的速度优势直接转化为吞吐量。

### 1.4 存储后端

```go
// cas/backend.go

type BackendKind string

const (
    BackendLocal  BackendKind = "local"   // 本地文件系统（默认）
    BackendSQLite BackendKind = "sqlite"  // SQLite BLOB 存储（嵌入式）
    BackendS3     BackendKind = "s3"      // AWS S3 / 兼容存储（跨机）
)

// BackendConfig 统一配置
type BackendConfig struct {
    Kind BackendKind

    // Local 配置
    LocalDir string // 默认 ~/.brain/cas/

    // SQLite 配置
    SQLitePath string // 默认 ~/.brain/cas/store.db

    // S3 配置
    S3Bucket    string
    S3Region    string
    S3Endpoint  string // 留空则用 AWS 官方端点
    S3KeyPrefix string // 对象前缀，如 "brain-cas/"
}
```

**三种后端对比**：

| 后端 | 适用场景 | 最大对象 | 并发 | 跨机 |
|------|----------|----------|------|------|
| **Local** | 本地单机（v3.0 默认） | 无限制 | 文件锁 | 否 |
| **SQLite** | 嵌入式，事务一致性要求高 | 建议 < 100MB | WAL 模式 | 否 |
| **S3** | 跨主机、大文件 | 无限制 | 原生并发 | 是 |

**本地文件系统后端的目录布局**：

```text
~/.brain/cas/
├── objects/
│   ├── a1/                  # ref 前2字节作为目录（分散 inode）
│   │   └── b2c3d4e5...      # ref 剩余部分作为文件名
│   └── ff/
│       └── 0011223344...
├── pins/
│   └── a1b2c3d4...          # 每个 pin 一个文件，内容是 pin reason JSON
├── tmp/                     # 写入进行中的临时文件（GC 候选）
└── meta.json                # 存储层元信息
```

```go
// cas/local_backend.go

type localBackend struct {
    dir  string
    algo HashAlgorithm
}

func (b *localBackend) Write(ctx context.Context, content io.Reader, meta ObjectMeta) (Ref, error) {
    // 1. 写入 tmp 目录
    tmpFile, err := os.CreateTemp(filepath.Join(b.dir, "tmp"), "cas-*")
    if err != nil {
        return "", err
    }
    defer os.Remove(tmpFile.Name()) // 成功后会 rename 走，失败时清理

    // 2. Tee：同时计算 hash 和写文件
    h := newHasher(b.algo)
    size, err := io.Copy(io.MultiWriter(tmpFile, h), content)
    if err != nil {
        tmpFile.Close()
        return "", err
    }
    tmpFile.Close()

    // 3. 生成 ref
    ref := Ref(string(b.algo) + ":" + hex.EncodeToString(h.Sum(nil)))

    // 4. 原子性 rename 到目标路径（幂等：目标已存在则忽略）
    objPath := b.objectPath(ref)
    if err := os.MkdirAll(filepath.Dir(objPath), 0755); err != nil {
        return "", err
    }
    if err := os.Rename(tmpFile.Name(), objPath); err != nil {
        // 目标已存在（并发写入相同内容），忽略错误
        if errors.Is(err, os.ErrExist) {
            return ref, nil
        }
        return "", err
    }

    _ = size // 可记录到 meta
    return ref, nil
}

func (b *localBackend) objectPath(ref Ref) string {
    // "blake3:a1b2c3..." → objects/a1/b2c3...
    parts := strings.SplitN(string(ref), ":", 2)
    if len(parts) != 2 {
        return filepath.Join(b.dir, "objects", "invalid", string(ref))
    }
    digest := parts[1]
    if len(digest) < 2 {
        return filepath.Join(b.dir, "objects", digest)
    }
    return filepath.Join(b.dir, "objects", digest[:2], digest[2:])
}
```

### 1.5 GC 策略

```go
// cas/gc.go

// GCPolicy 垃圾回收策略
type GCPolicy struct {
    // Mode 控制哪些对象会被回收
    Mode GCMode

    // MaxAge 超过此时间且未被 pin 的对象会被回收（仅 AgeBasedGC 生效）
    MaxAge time.Duration

    // MaxTotalSize 超过此大小时触发回收（仅 SizeBasedGC 生效）
    MaxTotalSize int64

    // DryRun 只统计，不真正删除
    DryRun bool
}

type GCMode string

const (
    // GCMarkAndSweep 扫描所有已知的 edge ref 作为 root，
    // 未被任何 TaskExecution.Inputs/Outputs 引用的对象为垃圾
    // 最精确，需要遍历全部 TaskExecution 记录
    GCMarkAndSweep GCMode = "mark-and-sweep"

    // GCAgeBasedGC 超时回收：超过 MaxAge 且未 pin 的对象直接删除
    // 最简单，适合本地开发环境
    GCAgeBasedGC GCMode = "age-based"

    // GCSizeBased 当存储超出 MaxTotalSize 时，
    // 按 LRU 顺序删除未 pin 的对象，直到低于水位
    GCSizeBased GCMode = "size-based"
)

type GCResult struct {
    ScannedCount  int64
    DeletedCount  int64
    FreedBytes    int64
    Duration      time.Duration
    Errors        []error        // 非致命错误（单文件删除失败不终止 GC）
}
```

**GC 触发机制**：

```go
// GC 由三个触发源驱动，不需要独立 GC 进程

type GCTrigger struct {
    // 1. 定时触发（默认每 6 小时）
    Schedule time.Duration

    // 2. 容量触发（写入后检查，超过水位触发）
    HighWatermarkBytes int64
    LowWatermarkBytes  int64

    // 3. TaskExecution 完成时触发（mark-and-sweep 的最佳时机）
    OnTaskCompletion bool
}
```

**Pin 机制**：防止 GC 删除重要数据。

```go
// 在 TaskExecution 运行期间，其 Inputs 和 Outputs 的 ref 自动 pin
// TaskExecution 完成后，Outputs 保持 pin，Inputs 解 pin
// 用户显式 pin 的对象永远不会被 GC 回收

type PinRecord struct {
    Ref       Ref
    Reason    string       // "task:<id>" / "user" / "workflow:<id>"
    CreatedAt time.Time
    PinnedBy  string       // TaskExecution ID 或 "user"
}
```

---

## 2. CAS 写入/读取完整流程

### 2.1 数据流图

```text
Producer Task (Task A)               Consumer Task (Task B)
─────────────────────               ─────────────────────
TaskExecution.run()
  │
  ├─ 执行工具，产出 result
  │
  ├─ EdgeWriter.Write(result)
  │     │
  │     ├─ io.Pipe 缓冲（大对象流式）
  │     ├─ 计算 Blake3 hash（流式）
  │     ├─ 写入 local/SQLite/S3
  │     └─ 返回 Ref
  │
  ├─ TaskExecution.Outputs = []EdgeRef{
  │       {EdgeMode: Materialized, Ref: "blake3:a1b2..."}
  │   }
  │
  └─ 更新 TaskExecution.Status = Done
       │
       │  EdgeRouter 监听 Status 变更
       ▼
  EdgeRouter.OnTaskDone(taskID)
       │
       ├─ 找到以 taskID 为 producer 的 DAG 边
       ├─ 提取 Outputs[i].Ref
       └─ 设置 Consumer Task B 的 Inputs[j].Ref
            └─ 触发 Task B 就绪检查
                 └─ Task B 所有 Inputs 就绪 → 加入调度队列

                                    Consumer Task (Task B)
                                      │
                                      ├─ 从 TaskExecution.Inputs 读取 EdgeRef
                                      │
                                      ├─ EdgeReader.Read(ref)
                                      │     │
                                      │     ├─ 检查本地 local/SQLite
                                      │     ├─ （可选）从 S3 拉取
                                      │     └─ 返回 io.ReadCloser
                                      │
                                      └─ 消费数据，继续执行
```

### 2.2 EdgeWriter / EdgeReader 接口

```go
// edge/materialized.go

// EdgeWriter 是 producer 侧的写入抽象
// Task 的执行代码只与 EdgeWriter 交互，不直接接触 CASStore
type EdgeWriter interface {
    // Write 写入单个 artifact，返回 EdgeRef
    Write(ctx context.Context, content io.Reader, opts WriteOptions) (EdgeRef, error)

    // WriteJSON 便捷方法：将 Go 值序列化为 JSON 写入
    WriteJSON(ctx context.Context, v any, opts WriteOptions) (EdgeRef, error)

    // WriteBytes 便捷方法
    WriteBytes(ctx context.Context, data []byte, opts WriteOptions) (EdgeRef, error)
}

// EdgeReader 是 consumer 侧的读取抽象
type EdgeReader interface {
    // Read 根据 EdgeRef 读取内容
    // 如果 EdgeRef.EdgeMode != Materialized，返回 ErrWrongEdgeMode
    Read(ctx context.Context, ref EdgeRef) (io.ReadCloser, error)

    // ReadJSON 便捷方法：读取并反序列化 JSON
    ReadJSON(ctx context.Context, ref EdgeRef, v any) error

    // ReadBytes 便捷方法
    ReadBytes(ctx context.Context, ref EdgeRef) ([]byte, error)

    // Wait 等待 ref 就绪（用于 consumer 提前知道 ref 名但还未写入的场景）
    // 配合 pre-declared DAG 使用
    Wait(ctx context.Context, ref EdgeRef, timeout time.Duration) error
}

type WriteOptions struct {
    ContentType string            // MIME type，如 "application/json"
    Tags        map[string]string // 任意 key-value 标签
    Pin         bool              // 是否立即 pin（防 GC）
}
```

### 2.3 实现伪代码

```go
// edge/materialized_impl.go

type materializedWriter struct {
    store  cas.CASStore
    taskID string
}

func (w *materializedWriter) Write(ctx context.Context, content io.Reader, opts WriteOptions) (EdgeRef, error) {
    // 1. 调用 CASStore.Write（内部流式 hash + 写文件 + 原子 rename）
    ref, err := w.store.Write(ctx, content, cas.ObjectMeta{
        ContentType: opts.ContentType,
        Tags:        mergeTags(opts.Tags, map[string]string{"producer_task": w.taskID}),
    })
    if err != nil {
        return EdgeRef{}, fmt.Errorf("cas write: %w", err)
    }

    // 2. 如果需要 pin，调用 store.Pin
    if opts.Pin {
        if err := w.store.Pin(ctx, ref, "task:"+w.taskID); err != nil {
            // pin 失败不是致命错误，记录警告继续
            log.Warn("failed to pin ref", "ref", ref, "task", w.taskID, "err", err)
        }
    }

    return EdgeRef{
        EdgeMode: EdgeMaterialized,
        Ref:      string(ref),
    }, nil
}

type materializedReader struct {
    store cas.CASStore
}

func (r *materializedReader) ReadBytes(ctx context.Context, ref EdgeRef) ([]byte, error) {
    if ref.EdgeMode != EdgeMaterialized {
        return nil, ErrWrongEdgeMode
    }
    return r.store.ReadBytes(ctx, cas.Ref(ref.Ref))
}

// Wait 实现：轮询 + 指数退避
// 正常情况下 consumer 在 producer 完成后才会被调度，不需要 Wait
// Wait 主要用于跨进程边界的异步确认
func (r *materializedReader) Wait(ctx context.Context, ref EdgeRef, timeout time.Duration) error {
    deadline := time.Now().Add(timeout)
    backoff := 10 * time.Millisecond
    for time.Now().Before(deadline) {
        exists, err := r.store.Exists(ctx, cas.Ref(ref.Ref))
        if err != nil {
            return err
        }
        if exists {
            return nil
        }
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-time.After(backoff):
            backoff = min(backoff*2, 2*time.Second)
        }
    }
    return ErrWaitTimeout
}
```

---

## 3. Streaming Pipe 注册表

### 3.1 PipeRegistry 接口

```go
// edge/pipe_registry.go

// PipeID 是 streaming edge 的唯一标识
// 格式：<workflow-id>/<edge-name>
// 示例："wf-abc123/data-to-quant"
type PipeID string

// PipeDescriptor 描述一个 streaming pipe 的元信息
type PipeDescriptor struct {
    ID          PipeID
    Backend     StreamBackend     // pipe / ringbuf / queue
    BackendConf BackendConf       // backend 特定配置
    ContentType string
    CreatedAt   time.Time
    ProducerID  string            // TaskExecution ID
    ConsumerIDs []string          // 允许消费的 TaskExecution ID（空 = 所有人）
    State       PipeState
}

type PipeState string

const (
    PipeStateCreating PipeState = "creating"   // 创建中
    PipeStateActive   PipeState = "active"     // 活跃，可读写
    PipeStateDraining PipeState = "draining"   // producer 关闭，consumer 可继续读完
    PipeStateClosed   PipeState = "closed"     // 完全关闭
    PipeStateError    PipeState = "error"      // 出错
)

// PipeRegistry 管理所有 streaming edge 的注册与发现
type PipeRegistry interface {
    // Create 创建一个新的 pipe（由 producer task 调用）
    // 如果 ID 已存在，返回 ErrPipeAlreadyExists
    Create(ctx context.Context, desc PipeDescriptor) error

    // Get 根据 ID 查找 pipe 描述（用于 consumer 发现）
    Get(ctx context.Context, id PipeID) (*PipeDescriptor, error)

    // List 列出所有活跃 pipe（可按 workflow/producer/state 过滤）
    List(ctx context.Context, filter PipeFilter) ([]*PipeDescriptor, error)

    // UpdateState 更新 pipe 状态（producer 关闭时调用 Draining，完全关闭时 Closed）
    UpdateState(ctx context.Context, id PipeID, state PipeState) error

    // Delete 删除 pipe 记录（GC 时调用）
    Delete(ctx context.Context, id PipeID) error

    // Watch 监听 pipe 状态变更（consumer 等待 pipe 就绪时使用）
    Watch(ctx context.Context, id PipeID) (<-chan PipeDescriptor, error)
}

type PipeFilter struct {
    WorkflowID string
    ProducerID string
    State      PipeState
    Backend    StreamBackend
}
```

### 3.2 Registry 的存储实现

```go
// edge/pipe_registry_local.go

// localPipeRegistry 是内存 + 可选持久化的本地实现
// Phase C-1 的 v3.2 阶段使用此实现
// Phase D 再扩展为分布式 etcd/Redis 实现

type localPipeRegistry struct {
    mu      sync.RWMutex
    pipes   map[PipeID]*PipeDescriptor
    watchers map[PipeID][]chan PipeDescriptor

    // 可选持久化路径（进程重启恢复）
    persistPath string
}

func (r *localPipeRegistry) Create(ctx context.Context, desc PipeDescriptor) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    if _, exists := r.pipes[desc.ID]; exists {
        return ErrPipeAlreadyExists
    }
    r.pipes[desc.ID] = &desc
    r.persistAsync()
    return nil
}

func (r *localPipeRegistry) UpdateState(ctx context.Context, id PipeID, state PipeState) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    desc, ok := r.pipes[id]
    if !ok {
        return ErrPipeNotFound
    }
    desc.State = state
    // 通知所有 watcher
    for _, ch := range r.watchers[id] {
        select {
        case ch <- *desc:
        default: // 非阻塞，watcher 跟不上就丢
        }
    }
    r.persistAsync()
    return nil
}
```

### 3.3 Pipe 生命周期

```text
Producer 调用 PipeRegistry.Create()
    │
    ▼
PipeState: creating
    │
    ├─ Backend 初始化（channel / mmap / MQ 连接）成功
    ▼
PipeState: active  ←───────── Consumer 可以开始订阅读取
    │
    ├─ Producer 正常完成（调用 PipeHandle.Close()）
    ▼
PipeState: draining          Consumer 可以继续读完 buffer 中的数据
    │
    ├─ Consumer 读完所有数据（EOF）
    ▼
PipeState: closed            GC 可以回收资源

    （异常路径）
    │
    ├─ Producer 崩溃 / Context 取消
    ▼
PipeState: error             Consumer 收到 ErrPipeError，决定是否重试
```

---

## 4. 三种 Streaming Backend 的统一抽象

### 4.1 统一接口

```go
// edge/stream.go

// StreamBackend 标识 streaming backend 类型
type StreamBackend string

const (
    BackendPipe    StreamBackend = "pipe"    // 进程内 channel
    BackendRingbuf StreamBackend = "ringbuf" // /dev/shm mmap ring buffer
    BackendQueue   StreamBackend = "queue"   // 外部 MQ（NATS / Redis Streams）
)

// StreamWriter 是 streaming edge 的 producer 接口
// producer 通过此接口向 edge 写入数据单元
type StreamWriter interface {
    // Write 写入一个数据帧（原子单元，consumer 每次 Read 一帧）
    Write(ctx context.Context, frame []byte) error

    // WriteTyped 写入带类型标记的帧（consumer 可按类型过滤）
    WriteTyped(ctx context.Context, frameType string, frame []byte) error

    // Flush 强制刷新 buffer（ringbuf 场景下确保 consumer 可见）
    Flush() error

    // Close 关闭写入端（等价于 EOF，触发 Draining 状态）
    Close() error
}

// StreamReader 是 streaming edge 的 consumer 接口
type StreamReader interface {
    // Read 阻塞读取下一帧，EOF 时返回 io.EOF
    Read(ctx context.Context) ([]byte, error)

    // ReadTyped 读取帧和类型标记
    ReadTyped(ctx context.Context) (frameType string, frame []byte, err error)

    // TryRead 非阻塞尝试读取，没有数据时返回 ErrNoData
    TryRead() ([]byte, error)

    // Close 关闭读取端（consumer 不再消费）
    Close() error
}

// StreamBackendFactory 根据配置创建 StreamWriter/StreamReader
type StreamBackendFactory interface {
    NewWriter(ctx context.Context, id PipeID, conf BackendConf) (StreamWriter, error)
    NewReader(ctx context.Context, id PipeID, conf BackendConf) (StreamReader, error)
}

// BackendConf 是各 backend 的配置联合体
type BackendConf struct {
    // Pipe 配置
    Pipe *PipeConf

    // Ringbuf 配置
    Ringbuf *RingbufConf

    // Queue 配置
    Queue *QueueConf
}
```

### 4.2 Pipe Backend（进程内 channel）

```go
// edge/backend_pipe.go

type PipeConf struct {
    BufferSize int // channel 缓冲大小，默认 256
}

type pipeBackend struct {
    channels sync.Map // PipeID → chan frameEntry
}

type frameEntry struct {
    frameType string
    data      []byte
}

func (b *pipeBackend) NewWriter(ctx context.Context, id PipeID, conf BackendConf) (StreamWriter, error) {
    c := conf.Pipe
    if c == nil {
        c = &PipeConf{BufferSize: 256}
    }
    ch := make(chan frameEntry, c.BufferSize)
    b.channels.Store(id, ch)
    return &pipeWriter{ch: ch, id: id, backend: b}, nil
}

func (b *pipeBackend) NewReader(ctx context.Context, id PipeID, conf BackendConf) (StreamReader, error) {
    // 等待 writer 先创建 channel（writer 通常先于 reader 初始化）
    deadline := time.Now().Add(5 * time.Second)
    for time.Now().Before(deadline) {
        if v, ok := b.channels.Load(id); ok {
            return &pipeReader{ch: v.(chan frameEntry)}, nil
        }
        time.Sleep(10 * time.Millisecond)
    }
    return nil, ErrPipeNotFound
}

type pipeWriter struct {
    ch      chan frameEntry
    id      PipeID
    backend *pipeBackend
    closed  atomic.Bool
}

func (w *pipeWriter) Write(ctx context.Context, frame []byte) error {
    if w.closed.Load() {
        return ErrPipeClosed
    }
    select {
    case w.ch <- frameEntry{data: frame}:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
    // channel 满时 Write 会阻塞（背压，见 §6）
}

func (w *pipeWriter) Close() error {
    if w.closed.CompareAndSwap(false, true) {
        close(w.ch)
        w.backend.channels.Delete(w.id)
    }
    return nil
}

type pipeReader struct {
    ch chan frameEntry
}

func (r *pipeReader) Read(ctx context.Context) ([]byte, error) {
    select {
    case entry, ok := <-r.ch:
        if !ok {
            return nil, io.EOF
        }
        return entry.data, nil
    case <-ctx.Done():
        return nil, ctx.Err()
    }
}

func (r *pipeReader) TryRead() ([]byte, error) {
    select {
    case entry, ok := <-r.ch:
        if !ok {
            return nil, io.EOF
        }
        return entry.data, nil
    default:
        return nil, ErrNoData
    }
}
```

### 4.3 Ringbuf Backend（mmap ring buffer 适配器）

```go
// edge/backend_ringbuf.go

// RingbufConf ring buffer 配置
type RingbufConf struct {
    ShmPath    string // /dev/shm/ 下的路径，如 "/dev/shm/brain-pipe-<id>"
    SlotCount  int    // 槽位数，默认 1024
    SlotSizeHint int  // 单帧大小提示（字节），影响预分配
}

// ringbufWriter 将现有 data/ringbuf.RingBuffer 包装为 StreamWriter
// 注意：ringbuf 是一写多读模型，Write 永远成功（覆写旧数据）
// 背压通过流控策略处理（见 §6）
type ringbufWriter struct {
    manager *ringbuf.BufferManager
    instID  string  // 复用 ringbuf 的 instID 机制，用 PipeID 作为 instID
}

func (w *ringbufWriter) Write(ctx context.Context, frame []byte) error {
    // 将 frame 封装为 MarketSnapshot 的泛化版本
    // Phase C 引入泛型 frame，替换硬编码的 MarketSnapshot
    snap := ringbuf.MarketSnapshot{} // 待替换为泛型 Frame
    // TODO：Phase C 需要将 ringbuf 泛化，脱离 MarketSnapshot 硬编码
    w.manager.Write(w.instID, snap)
    return nil // ringbuf 写入永远成功，背压在上层处理
}

func (w *ringbufWriter) Close() error {
    // ring buffer 不关闭，只标记 draining 状态
    // 实际清理由 PipeRegistry 的 GC 触发
    return nil
}

// ringbufReader 将 ringbuf.MultiReader 包装为 StreamReader
type ringbufReader struct {
    reader *ringbuf.MultiReader
    instID string
    lastSeq uint64
}

func (r *ringbufReader) Read(ctx context.Context) ([]byte, error) {
    // 轮询 + 背压（参见 §6）
    for {
        reader := r.reader.getReader(r.instID)
        snaps, ok := reader.ReadSince()
        if ok && len(snaps) > 0 {
            // 序列化返回第一帧（批量场景调用方可以调用多次 Read）
            data, err := json.Marshal(snaps[0])
            return data, err
        }
        select {
        case <-ctx.Done():
            return nil, ctx.Err()
        case <-time.After(1 * time.Millisecond): // 1ms 轮询间隔
        }
    }
}
```

**Phase C 的 Ringbuf 泛化任务**：现有 `ringbuf.RingBuffer` 强依赖 `MarketSnapshot` 类型，需要泛化为 `RingBuffer[T any]` 或基于 `[]byte` 的通用 frame 存储。详见 §7 迁移方案。

### 4.4 Queue Backend（外部 MQ）

```go
// edge/backend_queue.go

type QueueConf struct {
    Driver  string // "nats" / "redis-streams" / "kafka"（Phase D 实现）
    URL     string
    Subject string // NATS subject 或 Redis stream key
    Group   string // 消费者组 ID
}

// queueWriter 适配外部 MQ 的 producer
type queueWriter struct {
    driver  QueueDriver // 抽象接口，具体实现按 Driver 字段选择
    subject string
}

// QueueDriver 外部 MQ 驱动接口（留扩展点）
type QueueDriver interface {
    Publish(ctx context.Context, subject string, data []byte) error
    Subscribe(ctx context.Context, subject, group string) (<-chan []byte, error)
    Close() error
}

func (w *queueWriter) Write(ctx context.Context, frame []byte) error {
    return w.driver.Publish(ctx, w.subject, frame)
}

// Phase D 实现 NATS / Redis Streams driver
// Phase C 只声明接口，不实现，避免引入外部依赖
```

### 4.5 统一工厂

```go
// edge/stream_factory.go

// globalFactory 全局 backend 工厂，按 BackendConf 路由到对应实现
var globalPipeBackend = &pipeBackend{}

func NewStreamWriter(ctx context.Context, id PipeID, conf BackendConf) (StreamWriter, error) {
    switch {
    case conf.Pipe != nil:
        return globalPipeBackend.NewWriter(ctx, id, conf)
    case conf.Ringbuf != nil:
        return newRingbufWriter(ctx, id, conf)
    case conf.Queue != nil:
        return newQueueWriter(ctx, id, conf)
    default:
        return nil, ErrNoBackendConf
    }
}

func NewStreamReader(ctx context.Context, id PipeID, conf BackendConf) (StreamReader, error) {
    switch {
    case conf.Pipe != nil:
        return globalPipeBackend.NewReader(ctx, id, conf)
    case conf.Ringbuf != nil:
        return newRingbufReader(ctx, id, conf)
    case conf.Queue != nil:
        return newQueueReader(ctx, id, conf)
    default:
        return nil, ErrNoBackendConf
    }
}
```

---

## 5. Edge 注册与发现

### 5.1 Workflow DAG 中声明 Edge

Workflow DAG 由 WorkflowDef 描述，边在定义时静态声明：

```go
// workflow/workflow.go

// WorkflowDef 工作流定义（静态 DAG 结构）
type WorkflowDef struct {
    ID    string
    Name  string
    Nodes []NodeDef
    Edges []EdgeDef
}

// NodeDef 工作流节点，对应一个 TaskExecution
type NodeDef struct {
    ID       string
    BrainKind string            // 委托给哪个 brain
    Prompt   string             // 任务描述（或模板）
    Policy   TaskExecutionPolicy
}

// EdgeDef 工作流边，声明两个节点之间的数据传递
type EdgeDef struct {
    ID       string            // 边的唯一名称，在 workflow 内唯一
    From     string            // producer 节点 ID
    To       string            // consumer 节点 ID
    Mode     EdgeMode          // materialized / streaming

    // materialized 专用：consumer 等待 producer 完全完成后才启动
    // streaming 专用：consumer 在 producer 启动后即可启动
    StreamConf *StreamEdgeConf // Mode = streaming 时填写
}

type StreamEdgeConf struct {
    Backend    StreamBackend
    BackendConf BackendConf
    // 背压策略（见 §6）
    Backpressure BackpressurePolicy
}
```

**声明示例**：

```go
// Data Brain → Quant Brain 实时数据流的 WorkflowDef 声明
WorkflowDef{
    ID: "data-quant-pipeline",
    Nodes: []NodeDef{
        {ID: "data-collector", BrainKind: "data", Policy: TaskExecutionPolicy{
            Mode: ModeBackground, Lifecycle: Daemon, Restart: Always,
        }},
        {ID: "quant-strategy", BrainKind: "quant", Policy: TaskExecutionPolicy{
            Mode: ModeBackground, Lifecycle: Daemon, Restart: OnFailure,
        }},
    },
    Edges: []EdgeDef{
        {
            ID:   "market-feed",
            From: "data-collector",
            To:   "quant-strategy",
            Mode: EdgeStreaming,
            StreamConf: &StreamEdgeConf{
                Backend: BackendRingbuf,
                BackendConf: BackendConf{
                    Ringbuf: &RingbufConf{
                        ShmPath:   "/dev/shm/brain-market-feed",
                        SlotCount: 1024,
                    },
                },
                Backpressure: BackpressurePolicy{
                    Strategy: StrategyOverwrite, // ringbuf 写满时覆写最旧
                },
            },
        },
    },
}
```

### 5.2 Runtime 中 EdgeRef 解析为读写 Handle

```go
// edge/resolver.go

// EdgeResolver 在 runtime 中将 EdgeRef 解析为实际的读/写 handle
// TaskExecution 运行时调用此接口，无需知道底层细节
type EdgeResolver interface {
    // ResolveWriter 为 producer task 解析输出边的写入 handle
    // task 的 Outputs[i] 必须先由 WorkflowEngine 根据 EdgeDef 初始化
    ResolveWriter(ctx context.Context, taskID string, outputIndex int) (EdgeHandle, error)

    // ResolveReader 为 consumer task 解析输入边的读取 handle
    ResolveReader(ctx context.Context, taskID string, inputIndex int) (EdgeHandle, error)
}

// EdgeHandle 统一读写句柄（屏蔽 materialized 和 streaming 的差异）
type EdgeHandle interface {
    // Mode 返回 materialized 还是 streaming
    Mode() EdgeMode

    // AsMaterializedWriter 只有 Mode() == Materialized 且是 producer 端时有效
    AsMaterializedWriter() (EdgeWriter, bool)

    // AsMaterializedReader 只有 Mode() == Materialized 且是 consumer 端时有效
    AsMaterializedReader() (EdgeReader, bool)

    // AsStreamWriter 只有 Mode() == Streaming 且是 producer 端时有效
    AsStreamWriter() (StreamWriter, bool)

    // AsStreamReader 只有 Mode() == Streaming 且是 consumer 端时有效
    AsStreamReader() (StreamReader, bool)

    // Close 使用完毕后释放资源
    Close() error
}
```

**EdgeResolver 实现流程**：

```go
// edge/resolver_impl.go

type workflowEdgeResolver struct {
    workflowDef  *WorkflowDef
    pipeRegistry PipeRegistry
    casStore     cas.CASStore
    executions   TaskExecutionStore // 查询 TaskExecution 的 Inputs/Outputs
}

func (r *workflowEdgeResolver) ResolveWriter(ctx context.Context, taskID string, outputIndex int) (EdgeHandle, error) {
    // 1. 找到当前 task 对应的 NodeDef
    nodeDef, err := r.findNode(taskID)
    if err != nil {
        return nil, err
    }

    // 2. 找到以此 node 为 From 的 EdgeDef（按 outputIndex 对应）
    edgeDef, err := r.findEdgeByFrom(nodeDef.ID, outputIndex)
    if err != nil {
        return nil, err
    }

    switch edgeDef.Mode {
    case EdgeMaterialized:
        // 3a. materialized：返回 CAS writer
        writer := &materializedWriter{store: r.casStore, taskID: taskID}
        return &materializedWriterHandle{writer: writer}, nil

    case EdgeStreaming:
        // 3b. streaming：根据 BackendConf 创建 StreamWriter
        pipeID := PipeID(fmt.Sprintf("%s/%s", r.workflowDef.ID, edgeDef.ID))
        sw, err := NewStreamWriter(ctx, pipeID, edgeDef.StreamConf.BackendConf)
        if err != nil {
            return nil, err
        }
        // 注册到 PipeRegistry
        if err := r.pipeRegistry.Create(ctx, PipeDescriptor{
            ID:         pipeID,
            Backend:    edgeDef.StreamConf.Backend,
            BackendConf: edgeDef.StreamConf.BackendConf,
            ProducerID: taskID,
        }); err != nil && !errors.Is(err, ErrPipeAlreadyExists) {
            return nil, err
        }
        if err := r.pipeRegistry.UpdateState(ctx, pipeID, PipeStateActive); err != nil {
            return nil, err
        }
        return &streamWriterHandle{writer: sw, registry: r.pipeRegistry, pipeID: pipeID}, nil
    }
    return nil, ErrUnknownEdgeMode
}

func (r *workflowEdgeResolver) ResolveReader(ctx context.Context, taskID string, inputIndex int) (EdgeHandle, error) {
    // 1. 找到当前 task 的 TaskExecution，读取 Inputs[inputIndex]
    exec, err := r.executions.Get(ctx, taskID)
    if err != nil {
        return nil, err
    }
    if inputIndex >= len(exec.Inputs) {
        return nil, ErrInputIndexOutOfRange
    }
    ref := exec.Inputs[inputIndex]

    switch ref.EdgeMode {
    case EdgeMaterialized:
        // 2a. materialized：直接返回 CAS reader
        reader := &materializedReader{store: r.casStore}
        return &materializedReaderHandle{reader: reader, ref: ref}, nil

    case EdgeStreaming:
        // 2b. streaming：根据 PipeRegistry 找到 pipe，创建 StreamReader
        pipeID := PipeID(ref.Ref)
        desc, err := r.pipeRegistry.Get(ctx, pipeID)
        if err != nil {
            return nil, err
        }
        sr, err := NewStreamReader(ctx, pipeID, desc.BackendConf)
        if err != nil {
            return nil, err
        }
        return &streamReaderHandle{reader: sr}, nil
    }
    return nil, ErrUnknownEdgeMode
}
```

### 5.3 TaskExecution 中的 Inputs/Outputs 填充时序

```text
WorkflowEngine 初始化阶段：
  ┌─ 解析 WorkflowDef.Edges
  ├─ 对每条 streaming edge：
  │    ├─ 生成 PipeID = "wf-<id>/<edge-id>"
  │    └─ 预填充 producer task 的 Outputs[i].Ref = string(pipeID)
  │    └─ 预填充 consumer task 的 Inputs[j].Ref = string(pipeID)
  └─ 对每条 materialized edge：
       ├─ Ref 暂为空（"pending:<edge-id>"）
       └─ producer 完成后由 EdgeRouter 填充

执行阶段（materialized）：
  producer 完成 → EdgeWriter.Write() → 拿到 CAS ref
  → EdgeRouter 更新 consumer task 的 Inputs[j].Ref = string(casRef)
  → consumer task 就绪，加入调度队列

执行阶段（streaming）：
  producer 和 consumer 可以同时启动
  producer 调用 ResolveWriter() → 得到 StreamWriter，开始写入
  consumer 调用 ResolveReader() → 得到 StreamReader，开始消费
```

---

## 6. 背压和流控

### 6.1 设计原则

不同 backend 对"写满"的承受能力不同，需要统一的背压策略接口：

```go
// edge/backpressure.go

type BackpressureStrategy string

const (
    // StrategyBlock 写满时阻塞 producer，直到 consumer 消费腾出空间
    // 适用于：pipe backend、需要严格不丢数据的场景
    StrategyBlock BackpressureStrategy = "block"

    // StrategyDrop 写满时丢弃新数据（最新数据优先，保留旧数据）
    // 适用于：ringbuf 场景的低优先级数据
    StrategyDrop BackpressureStrategy = "drop"

    // StrategyOverwrite 写满时覆写最旧数据（新数据优先）
    // 适用于：ringbuf 场景的实时行情（旧数据天然过时）
    StrategyOverwrite BackpressureStrategy = "overwrite"

    // StrategySpillToCAS 写满时将溢出帧写入 CAS，consumer 从 CAS 补读
    // 适用于：不能丢数据但也不能无限阻塞的场景（批处理任务输出）
    StrategySpillToCAS BackpressureStrategy = "spill-to-cas"
)

type BackpressurePolicy struct {
    Strategy BackpressureStrategy

    // Block 策略参数
    BlockTimeout time.Duration // 超时后转为 ErrBackpressureTimeout

    // SpillToCAS 策略参数
    SpillCASStore cas.CASStore
    SpillMaxBytes int64          // 溢出写入 CAS 的最大字节数上限
}
```

### 6.2 各 Backend 的背压实现

**Pipe Backend（channel）**：

```go
// pipeWriter.Write() 在 channel 满时的行为

func (w *pipeWriter) Write(ctx context.Context, frame []byte) error {
    if w.closed.Load() {
        return ErrPipeClosed
    }
    switch w.bp.Strategy {
    case StrategyBlock:
        // 阻塞写，ctx 超时则返回错误
        select {
        case w.ch <- frameEntry{data: frame}:
            return nil
        case <-ctx.Done():
            return ctx.Err()
        }

    case StrategyDrop:
        select {
        case w.ch <- frameEntry{data: frame}:
        default:
            // 丢弃，记录 metric
            metrics.StreamDropped.Inc()
        }
        return nil

    case StrategySpillToCAS:
        select {
        case w.ch <- frameEntry{data: frame}:
        default:
            // 溢出写入 CAS
            ref, err := w.bp.SpillCASStore.WriteBytes(ctx, frame, cas.ObjectMeta{
                Tags: map[string]string{"spill_pipe": string(w.id)},
            })
            if err != nil {
                return fmt.Errorf("spill to cas: %w", err)
            }
            // 将 CAS ref 作为特殊帧写入 channel（consumer 侧补读）
            select {
            case w.ch <- frameEntry{frameType: "spill-ref", data: []byte(ref)}:
            case <-ctx.Done():
                return ctx.Err()
            }
        }
        return nil
    }
    return ErrUnknownBackpressureStrategy
}
```

**Ringbuf Backend**：

```go
// ringbuf 的背压行为由 StrategyOverwrite 天然实现：
// RingBuffer.Write() 永远成功，覆写最旧槽位
// 这是实时行情场景的正确语义（旧数据没有保留价值）

// 但对于不能丢数据的场景，需要在 ringbufWriter 上层包一层检查：

func (w *ringbufWriter) Write(ctx context.Context, frame []byte) error {
    if w.bp.Strategy == StrategyBlock {
        // 检查 consumer 的 lag（读序号与写序号的差）
        // 如果 lag > SlotCount * 0.8，说明 consumer 跟不上，阻塞等待
        for {
            lag := w.manager.GetOrCreate(w.instID).WriteSeq() - w.consumerLastSeq()
            if lag < uint64(float64(w.slotCount)*0.8) {
                break
            }
            select {
            case <-ctx.Done():
                return ctx.Err()
            case <-time.After(500 * time.Microsecond):
            }
        }
    }
    w.manager.Write(w.instID, marshalToSnapshot(frame))
    return nil
}
```

### 6.3 流控指标（可观测性）

```go
// 所有 streaming edge 暴露以下 Prometheus 指标

var (
    // Producer 写入速率
    streamWrittenFrames = prometheus.NewCounterVec(
        prometheus.CounterOpts{Name: "brain_stream_written_frames_total"},
        []string{"pipe_id", "backend"},
    )

    // Consumer 读取速率
    streamReadFrames = prometheus.NewCounterVec(
        prometheus.CounterOpts{Name: "brain_stream_read_frames_total"},
        []string{"pipe_id", "backend"},
    )

    // 丢弃/覆写的帧数
    streamDroppedFrames = prometheus.NewCounterVec(
        prometheus.CounterOpts{Name: "brain_stream_dropped_frames_total"},
        []string{"pipe_id", "strategy"},
    )

    // Consumer lag（写序号 - 读序号）
    streamConsumerLag = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{Name: "brain_stream_consumer_lag"},
        []string{"pipe_id"},
    )

    // SpillToCAS 的帧数和字节数
    streamSpilledFrames = prometheus.NewCounterVec(
        prometheus.CounterOpts{Name: "brain_stream_spilled_frames_total"},
        []string{"pipe_id"},
    )
)
```

---

## 7. 现有 mmap Ring Buffer 迁移方案

### 7.1 现状分析

当前 Data→Quant 的 ring buffer 有以下硬编码耦合：

| 耦合点 | 位置 | 问题 |
|--------|------|------|
| `MarketSnapshot` 固定类型 | `brains/data/ringbuf/snapshot.go` | 无法承载非市场数据的 frame |
| `BufferManager` 直接实例化 | `brains/data/brain.go` | 无法通过 Edge 框架管理生命周期 |
| `ringbuf.Reader` 直接被 Quant 导入 | `brains/quant/` | 依赖倒置，quant 知道 data 的内部实现 |
| 没有 PipeID / 注册表概念 | — | 无法被 WorkflowEngine 发现和管理 |

### 7.2 迁移策略：渐进式，不破坏现有功能

**原则**：三步走，每步都可独立交付，不 break 现有 Data/Quant brain 的功能。

---

**Step 1（Phase C-1 前置）：泛化 RingBuffer 类型**

目标：使 `ringbuf.RingBuffer` 不再绑定 `MarketSnapshot`，支持任意 `[]byte` frame。

```go
// brains/data/ringbuf/frame.go （新增文件）

// Frame 是泛化的 ring buffer 数据单元
// 替代 MarketSnapshot 成为传输层的通用单元
type Frame struct {
    SeqNum    uint64
    Timestamp int64
    Type      string  // 帧类型标记，如 "market.snapshot" / "feature.vector"
    Payload   []byte  // JSON / protobuf / raw bytes
}

// 保留 MarketSnapshot，但让它变成 Frame 的一个具体实现
// MarketSnapshot 不再是 ring buffer 的内部类型，而是应用层类型

// MarketSnapshot → Frame 的编解码
func EncodeSnapshot(snap MarketSnapshot) (Frame, error) {
    data, err := json.Marshal(snap)
    if err != nil {
        return Frame{}, err
    }
    return Frame{
        Timestamp: snap.Timestamp,
        Type:      "market.snapshot",
        Payload:   data,
    }, nil
}

func DecodeSnapshot(f Frame) (MarketSnapshot, error) {
    var snap MarketSnapshot
    return snap, json.Unmarshal(f.Payload, &snap)
}
```

```go
// brains/data/ringbuf/ringbuf.go 修改
// 将 []MarketSnapshot 改为 []Frame，对外接口从 MarketSnapshot 改为 Frame
// 提供向后兼容的 WriteSnapshot / LatestSnapshot 方法（内部调用 EncodeSnapshot）

type RingBuffer struct {
    slots    []Frame    // 原来是 []MarketSnapshot
    size     int
    writeSeq uint64
    mu       sync.RWMutex
}

// 向后兼容方法，保持 Data Brain 代码不变
func (rb *RingBuffer) WriteSnapshot(snap MarketSnapshot) error {
    frame, err := EncodeSnapshot(snap)
    if err != nil {
        return err
    }
    rb.Write(frame)
    return nil
}

func (rb *RingBuffer) LatestSnapshot() (MarketSnapshot, bool) {
    frame, ok := rb.Latest()
    if !ok {
        return MarketSnapshot{}, false
    }
    snap, err := DecodeSnapshot(frame)
    if err != nil {
        return MarketSnapshot{}, false
    }
    return snap, true
}
```

---

**Step 2（Phase C-1）：将 ringbuf 接入 Flow Edge 框架**

目标：Data Brain 的 `BufferManager` 通过 `PipeRegistry` 注册，Quant Brain 通过 `EdgeResolver` 发现。

```go
// brains/data/brain.go 修改

type DataBrain struct {
    // 现有字段
    bufManager *ringbuf.BufferManager
    // 新增
    pipeRegistry  edge.PipeRegistry
    edgeResolver  edge.EdgeResolver
}

func (b *DataBrain) Start(ctx context.Context) error {
    // 原有逻辑：启动行情订阅、Feature Engine 等

    // 新增：将 bufManager 注册为 Flow Edge 的 streaming pipe
    pipeID := edge.PipeID("system/data-quant-market-feed")
    if err := b.pipeRegistry.Create(ctx, edge.PipeDescriptor{
        ID:      pipeID,
        Backend: edge.BackendRingbuf,
        BackendConf: edge.BackendConf{
            Ringbuf: &edge.RingbufConf{
                ShmPath:   "/dev/shm/brain-market-feed",
                SlotCount: 1024,
            },
        },
        ProducerID: "data-brain-daemon",
        State:      edge.PipeStateActive,
    }); err != nil && !errors.Is(err, edge.ErrPipeAlreadyExists) {
        return fmt.Errorf("register market feed pipe: %w", err)
    }

    return b.runMarketLoop(ctx) // 原有逻辑不变
}

// 写入数据时，同时写入旧路径（向后兼容）和新路径（Flow Edge）
func (b *DataBrain) publishSnapshot(snap ringbuf.MarketSnapshot) {
    // 旧路径（保持不变，Quant Brain 渐进迁移）
    b.bufManager.Write(snap.InstID, snap)

    // 新路径（Frame 格式，供 Flow Edge 框架使用）
    // ringbufWriter 内部仍然用同一个 bufManager，不重复写
}
```

```go
// brains/quant/brain.go 修改（渐进迁移）

// 阶段一：保持直接导入 ringbuf.MultiReader（不改变）
// 阶段二（Phase C 稳定后）：改为通过 EdgeResolver 获取 StreamReader

// 阶段二的写法：
func (b *QuantBrain) subscribeMarketFeed(ctx context.Context) (edge.StreamReader, error) {
    // 通过 EdgeResolver 获取 reader，不再直接导入 data/ringbuf
    handle, err := b.edgeResolver.ResolveReader(ctx, "quant-brain-daemon", 0)
    if err != nil {
        return nil, err
    }
    sr, ok := handle.AsStreamReader()
    if !ok {
        return nil, errors.New("expected streaming reader")
    }
    return sr, nil
}
```

---

**Step 3（Phase C 收尾）：清理硬编码**

- 删除 `brains/quant/` 对 `brains/data/ringbuf` 的直接 import
- `MarketSnapshot` 在 quant 侧通过 `DecodeSnapshot(frame)` 获取
- Data Brain 的 `BufferManager` 被封装在 `ringbufBackend` 内部，外部不可见
- 旧的 `/dev/shm` 路径通过 `RingbufConf.ShmPath` 配置化

迁移完成后，Data→Quant 的行情通道从"硬编码特例"变为"一个用了 ringbuf backend 的 streaming edge"，与其他 streaming edge 地位平等，可以被 WorkflowEngine 统一管理。

---

## 8. 错误处理

### 8.1 错误类型定义

```go
// edge/errors.go

var (
    // CAS 错误
    ErrRefNotFound              = errors.New("cas ref not found")
    ErrHashMismatch             = errors.New("content hash mismatch")
    ErrUnsupportedHashAlgorithm = errors.New("unsupported hash algorithm")
    ErrCASWriteFailed           = errors.New("cas write failed")
    ErrCASReadFailed            = errors.New("cas read failed")

    // Pipe 错误
    ErrPipeNotFound             = errors.New("pipe not found")
    ErrPipeAlreadyExists        = errors.New("pipe already exists")
    ErrPipeClosed               = errors.New("pipe is closed")
    ErrPipeError                = errors.New("pipe error")

    // 流控错误
    ErrNoData                   = errors.New("no data available")
    ErrBackpressureTimeout      = errors.New("backpressure timeout")
    ErrSpillLimitExceeded       = errors.New("spill-to-cas limit exceeded")
    ErrUnknownBackpressureStrategy = errors.New("unknown backpressure strategy")

    // 边框架错误
    ErrWrongEdgeMode            = errors.New("wrong edge mode")
    ErrUnknownEdgeMode          = errors.New("unknown edge mode")
    ErrInputIndexOutOfRange     = errors.New("input index out of range")
    ErrWaitTimeout              = errors.New("wait for ref timeout")
    ErrNoBackendConf            = errors.New("no backend configuration provided")
)
```

### 8.2 Materialized Edge 错误处理

```go
// 错误场景及处理策略

// 场景一：producer 写入 CAS 失败
// 处理：边进入 error 状态，consumer 永不会被调度（无 ref）
// 通知：TaskExecution.Status = Failed，EventBus 发布 EdgeErrorEvent
// 重试：看 TaskExecution.Restart 策略（OnFailure → 重启 producer）

// 场景二：consumer 读取 CAS 失败（ref 存在但读取出错）
// 可能原因：文件损坏、S3 临时错误
// 处理：指数退避重试，最多 3 次

func (r *materializedReader) ReadBytes(ctx context.Context, ref EdgeRef) ([]byte, error) {
    var lastErr error
    backoff := 100 * time.Millisecond
    for attempt := 0; attempt < 3; attempt++ {
        data, err := r.store.ReadBytes(ctx, cas.Ref(ref.Ref))
        if err == nil {
            return data, nil
        }
        lastErr = err
        if errors.Is(err, ErrRefNotFound) {
            // ref 不存在是致命错误，不重试
            return nil, fmt.Errorf("edge ref not found: %w", err)
        }
        // 其他错误（IO 错误等）退避重试
        select {
        case <-ctx.Done():
            return nil, ctx.Err()
        case <-time.After(backoff):
            backoff *= 2
        }
    }
    return nil, fmt.Errorf("cas read failed after retries: %w", lastErr)
}

// 场景三：ref 完整性校验失败（数据损坏）
// CASStore.Read() 内部校验 hash，不匹配则返回 ErrHashMismatch
// consumer 层不重试，直接上报错误（数据已损坏，重试无意义）
```

### 8.3 Streaming Edge 错误处理

```go
// 场景一：producer 崩溃（pipe backend channel 关闭）
// consumer Read() 会返回 io.EOF（channel 关闭信号）
// consumer 层的处理：

func (b *QuantBrain) consumeMarketFeed(ctx context.Context, reader edge.StreamReader) error {
    for {
        frame, err := reader.Read(ctx)
        if err != nil {
            if errors.Is(err, io.EOF) {
                // producer 正常关闭，consumer 也优雅退出
                log.Info("market feed producer closed, consumer exiting")
                return nil
            }
            if errors.Is(err, ErrPipeError) {
                // producer 异常崩溃
                log.Error("market feed pipe error", "err", err)
                // 根据 RestartPolicy 决定是否重试
                return fmt.Errorf("stream pipe error: %w", err)
            }
            return err
        }
        // 正常处理 frame...
        b.processFrame(frame)
    }
}

// 场景二：ringbuf consumer 严重落后（lag > SlotCount）
// ringbuf 会覆写旧数据，consumer 检测到 gap 时：

func (r *ringbufReader) detectAndHandleGap(lastSeq, currentSeq uint64) {
    if currentSeq-lastSeq > uint64(r.slotCount) {
        gap := currentSeq - lastSeq - uint64(r.slotCount)
        log.Warn("ringbuf consumer lag too high, frames lost",
            "lost_frames", gap,
            "consumer_seq", lastSeq,
            "producer_seq", currentSeq,
        )
        // 发布 metric
        metrics.StreamDroppedFrames.WithLabelValues(string(r.pipeID), "overwrite").Add(float64(gap))
        // 直接跳到最新位置，继续消费
    }
}

// 场景三：queue backend 连接断开
// 利用 QueueDriver 的重连机制（NATS/Redis 的 client 库自带 reconnect）
// StreamReader.Read() 在 reconnect 期间阻塞，超时后返回错误

// 场景四：CAS spill 空间不足（StrategySpillToCAS）
// 返回 ErrSpillLimitExceeded
// 上层降级为 StrategyDrop，并通过 EventBus 发出告警
```

### 8.4 统一错误通知

```go
// edge/events.go

// 所有 edge 层面的错误都通过 EventBus 通知，不只是 log

type EdgeErrorEvent struct {
    EdgeID     string
    PipeID     PipeID  // streaming edge 才有
    CASRef     cas.Ref // materialized edge 才有
    ErrorCode  string
    Message    string
    ProducerID string
    ConsumerID string
    Timestamp  time.Time
}

// EdgeMonitor 监听并处理 edge 错误事件
type EdgeMonitor struct {
    eventBus EventBus
    registry PipeRegistry
}

func (m *EdgeMonitor) OnEdgeError(ev EdgeErrorEvent) {
    // 1. 更新 PipeRegistry 状态（streaming edge）
    if ev.PipeID != "" {
        _ = m.registry.UpdateState(context.Background(), ev.PipeID, PipeStateError)
    }

    // 2. 发布到 EventBus（Dashboard 实时显示、告警）
    m.eventBus.Publish(ev)

    // 3. 决策：是否需要触发 Task 重启
    // 这个决策委托给 TaskExecution 的 RestartPolicy 处理，EdgeMonitor 不做
}
```

---

## 9. 包结构

```text
sdk/
└── edge/                            # Flow Edge 框架（新增）
    ├── edge.go                      # EdgeRef / EdgeMode 核心类型定义
    ├── handle.go                    # EdgeHandle 统一句柄接口
    ├── materialized.go              # EdgeWriter / EdgeReader 接口
    ├── materialized_impl.go         # 实现
    ├── stream.go                    # StreamWriter / StreamReader / StreamBackend 接口
    ├── backend_pipe.go              # pipe backend
    ├── backend_ringbuf.go           # ringbuf backend（适配 brains/data/ringbuf）
    ├── backend_queue.go             # queue backend（接口，Phase D 实现）
    ├── stream_factory.go            # 工厂方法
    ├── backpressure.go              # 背压策略
    ├── pipe_registry.go             # PipeRegistry 接口
    ├── pipe_registry_local.go       # 本地内存实现
    ├── resolver.go                  # EdgeResolver 接口
    ├── resolver_impl.go             # 基于 WorkflowDef 的实现
    ├── errors.go                    # 错误类型
    └── events.go                    # EdgeErrorEvent + EdgeMonitor

sdk/
└── cas/                             # CAS 存储层（新增）
    ├── store.go                     # CASStore 接口 + Ref 类型
    ├── hash.go                      # HashAlgorithm + ComputeRef
    ├── gc.go                        # GCPolicy / GCResult / GCTrigger
    ├── local_backend.go             # 本地文件系统实现
    ├── sqlite_backend.go            # SQLite 实现（Phase C 可选）
    ├── s3_backend.go                # S3 实现（Phase D）
    └── errors.go

workflow/                            # Workflow Engine（新增，Phase C-1）
    ├── workflow.go                  # WorkflowDef / NodeDef / EdgeDef
    ├── engine.go                    # WorkflowEngine 调度核心
    └── edge_router.go               # EdgeRouter：监听 TaskExecution 完成，更新 Inputs ref

brains/data/ringbuf/                 # 迁移改造
    ├── frame.go                     # 新增：Frame 泛型单元
    ├── snapshot.go                  # 改造：MarketSnapshot ↔ Frame 编解码
    ├── ringbuf.go                   # 改造：slots []MarketSnapshot → []Frame
    └── reader.go                    # 改造：ReadSince() 返回 []Frame（兼容方法保留）
```

---

## 10. 实现优先级

| 优先级 | 内容 | Phase | 说明 |
|--------|------|-------|------|
| P0 | `cas/store.go` + `cas/local_backend.go` | C-1 | materialized edge 的基础，无它无法运行 |
| P0 | `edge/edge.go` + `edge/handle.go` | C-1 | 核心类型，所有其他模块依赖 |
| P0 | `edge/materialized.go` + `edge/materialized_impl.go` | C-1 | materialized edge 读写 |
| P0 | `edge/backend_pipe.go` + `edge/pipe_registry_local.go` | C-1 | streaming pipe（最简实现） |
| P1 | `edge/backend_ringbuf.go` | C-1 | 接入现有 ringbuf，迁移 Data→Quant |
| P1 | `edge/resolver.go` + `workflow/edge_router.go` | C-1 | Edge 发现与 DAG 集成 |
| P1 | `edge/backpressure.go` | C-1 | 背压策略（至少 block + overwrite） |
| P2 | `cas/gc.go` 完整实现 | C-2 | GC 策略，短期内手动清理可接受 |
| P2 | `ringbuf/frame.go` 泛化 | C-2 | 迁移 Step 1，不影响现有功能 |
| P3 | `cas/sqlite_backend.go` | C-3 | 嵌入式场景，本地文件已满足大部分需求 |
| P3 | `edge/backend_queue.go` 实现 | D | 跨主机场景，Phase D 需要 |
| P3 | `cas/s3_backend.go` | D | 云存储，Phase D 需要 |
