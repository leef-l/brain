---
name: 编译和配置铁律
description: 编译必须输出到 GOPATH/bin，配置文件统一在 ~/.brain，sidecar 入口必须用正确的 cmd 目录
type: feedback
originSessionId: c013a763-4fb3-437a-9e83-608bcdcc6659
---
## 铁律：编译输出到 GOPATH

所有 go build 必须输出到 `$GOPATH/bin`（即 `/root/go/bin/`），**绝对禁止**编译到 `/usr/local/bin` 或其他目录。

示例：
```bash
go build -o /root/go/bin/brain ./cmd/brain/
go build -o /root/go/bin/brain-quant-sidecar ./brains/quant/cmd/brain-quant-sidecar/
go build -o /root/go/bin/brain-data-sidecar ./brains/data/cmd/brain-data-sidecar/
go build -o /root/go/bin/brain-central ./central/cmd/
```

**Why:** 用户环境 PATH 包含 GOPATH/bin，kernel 的 BinResolver 会从 PATH 和同目录搜索 sidecar 二进制。编译到其他目录会导致找不到或版本混乱。

**How to apply:** 每次编译时用 `go build -o $GOPATH/bin/<name>`，不要用 `go install`（会按包目录名命名，容易出错）。

## 铁律：配置文件在 ~/.brain

所有 brain 配置文件统一放在 `~/.brain/` 目录下：
- `~/.brain/data-brain.yaml` — 数据大脑配置
- `~/.brain/quant-brain.yaml` — 量化大脑配置
- `~/.brain/central-brain.yaml` — 中央大脑配置
- `~/.brain/config.json` — 主配置

**Why:** kernel 的 `injectSidecarConfigEnv()` 自动从 `~/.brain/<kind>-brain.yaml` 注入环境变量（DATA_CONFIG、QUANT_CONFIG、CENTRAL_CONFIG），配置放其他位置不会被自动发现。

**How to apply:** 修改配置时直接编辑 `~/.brain/` 下的文件，不要在项目目录创建配置副本。

## 注意：sidecar 二进制入口

各 sidecar 的正确编译源码路径（实现 stdio JSON-RPC 协议的入口）：
- quant: `./brains/quant/cmd/brain-quant-sidecar/`（不是 `./brains/quant/cmd/`，后者是独立运行模式）
- data: `./brains/data/cmd/brain-data-sidecar/`
- central: `./central/cmd/`

**Why:** 曾因编译了错误的入口（独立运行的 main.go 需要 -paper 参数），导致 sidecar 握手失败。
