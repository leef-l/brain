---
name: 正确的编译目标
description: 三个编译目标各有用途，绝对不能搞混，否则会覆盖 kernel 导致 502
type: feedback
originSessionId: 3c4046a8-c43f-405d-b9a9-d92ab199a696
---
## 三个编译目标（铁律）

| 二进制 | 编译命令 | 用途 |
|--------|---------|------|
| `brain` (kernel) | `go build -o $GOPATH/bin/brain ./cmd/brain/` | Kernel 主进程，`brain serve` 命令入口 |
| `brain-quant-sidecar` | `go build -o $GOPATH/bin/brain-quant-sidecar ./brains/quant/cmd/brain-quant-sidecar/` | Quant sidecar（含 WebUI），由 kernel 自动 fork |
| `brain-data-sidecar` | `go build -o $GOPATH/bin/brain-data-sidecar ./brains/data/cmd/brain-data-sidecar/` | Data sidecar，由 kernel 自动 fork |

## 绝对禁止

```
# 这条命令会把 quant-brain 独立入口覆盖到 brain，导致 brain serve 失败 → 502
go build -o $GOPATH/bin/brain ./brains/quant/cmd/main.go   ← 禁止！
go build -o $GOPATH/bin/brain ./brains/quant/cmd/           ← 禁止！
```

**Why:** 2026-04-16 事故——上述命令把 kernel binary 覆盖成了 quant-brain 独立入口，`brain serve` 变成 `usage: quant-brain -paper`，supervisor 进程虽然 RUNNING 但端口不监听，WebUI 502。排查花了大量时间。

**How to apply:**
- 改了 `brains/quant/` 下的代码 → 编译 `brain-quant-sidecar`
- 改了 `brains/data/` 下的代码 → 编译 `brain-data-sidecar`  
- 改了 `sdk/kernel/`、`cmd/brain/` 下的代码 → 编译 `brain`（kernel）
- 改了多处 → 分别编译对应目标，**每个目标用各自的命令**
- 编译后重启：`/www/server/panel/pyenv/bin/supervisorctl restart brain-quant`
