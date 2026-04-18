# 第三方 Brain Sidecar 开发模板

本目录包含第三方专精大脑的项目骨架模板。

## 快速开始

使用 `brain brain init` 命令在当前目录生成新项目：

```bash
brain brain init <kind>
```

例如创建一个 `image` 类型的专精大脑：

```bash
mkdir brain-image && cd brain-image
brain brain init image
go mod init github.com/yourname/brain-image
go build -o brain-image-sidecar .
```

## 生成的文件

| 文件 | 说明 |
|------|------|
| `brain.json` | Brain Manifest，声明 kind、版本、运行时等契约信息 |
| `main.go` | sidecar 入口，调用 `sidecar.Run(handler)` 启动 |
| `handler.go` | `BrainHandler` 接口实现，包含业务逻辑 |

## 开发要点

1. **实现 `HandleMethod`**：至少支持 `brain/execute` 和 `tools/call`
2. **注册工具**：在 `Tools()` 中返回实际有效的工具名列表
3. **二进制命名**：编译产物必须命名为 `brain-<kind>-sidecar`，放在主程序同目录
4. **需要 LLM 推理**：实现 `sidecar.RichBrainHandler` 接口获取 `KernelCaller`

## 完整文档

详细开发指南请参阅 `sdk/docs/29-第三方专精大脑开发.md`。
