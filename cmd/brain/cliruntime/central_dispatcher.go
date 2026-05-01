// central_dispatcher.go — 中央大脑工具的"智能分级"包装。
//
// 目的：让中央大脑保留完整工具集（写文件 / 跑命令等）以便处理简单任务，
// 但当工具调用属于"真正的工作"（多文件改动 / 编译构建 / 危险命令）时，
// 自动拦截并提示 LLM 改用 central.delegate。
//
// 设计原则（对应 central/docs/38-中央大脑核心职责.md v3）：
//   - Tier 1（只读理解 / 单文件小写入）→ 直接执行（fast path）
//   - Tier 3（多文件改动 / 构建 / 测试）→ 拦截 + 返回 delegate 建议
//
// 拦截不是"拒绝"，而是返回一个特殊 result，告诉 LLM 这个操作应该走 delegate。
// LLM 看到 result 后会自然改调 central.delegate（result 比 prompt 更可靠）。
//
// 不影响 code / verifier / browser 等专精大脑：它们的工具不被这个包装。

package cliruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leef-l/brain/sdk/tool"
)

// 只读 shell 命令白名单：这些命令对中央大脑是"看一眼"性质，直接放行。
// 任何不在白名单的命令都被视为"动手"，触发委派建议。
var readOnlyShellCommands = map[string]bool{
	"ls": true, "ll": true, "la": true,
	"pwd":     true,
	"cat":     true, "head": true, "tail": true, "less": true, "more": true,
	"grep":    true, "egrep": true, "fgrep": true, "rg": true, "ack": true,
	"find":    true, "locate": true, "which": true, "whereis": true,
	"stat":    true, "file": true, "wc": true,
	"echo":    true, "printf": true,
	"date":    true, "uname": true, "whoami": true, "id": true, "hostname": true,
	"env":     true, "printenv": true,
	"diff":    true, "cmp": true,
	"tree":    true,
	"git":     true, // git status/log/show/diff 等读操作占多数；写操作（commit/push）由用户自己决定
	"go":      true, // go list/version/env/doc 是只读；go build/test 由 LLM 决定要不要 delegate
}

// centralDispatchResult 是拦截后返回给 LLM 的标准化"建议改用 delegate"消息。
// 设计要点：output 是结构化 JSON 而非自由文本，让 LLM 容易识别这是"系统建议"而不是"工具结果"。
func centralDispatchResult(reason, suggestion string) *tool.Result {
	body := map[string]interface{}{
		"executed":   false,
		"reason":     reason,
		"suggestion": suggestion,
		"hint":       "use central.delegate or central.submit_workflow instead",
	}
	raw, _ := json.Marshal(body)
	return &tool.Result{Output: raw, IsError: false}
}

// CentralAwareWrite 包装一个文件写入类工具（write_file / edit_file / delete_file）。
// 中央大脑调用时根据参数大小 / 类型决定直接执行还是建议 delegate。
//
// 当前规则（保守估计，可调）：
//   - write_file content > 5000 字符 → 建议 delegate（多半是真代码）
//   - edit_file / delete_file → 永远建议 delegate（修改/删除现有文件属于"真工作"）
//
// brainKind=="" 视为非中央大脑场景（如直接给 code sidecar 用），不拦截。
func CentralAwareWrite(inner tool.Tool, brainKind string) tool.Tool {
	if brainKind != "central" {
		return inner
	}
	return &centralWriteWrapper{inner: inner}
}

type centralWriteWrapper struct {
	inner tool.Tool
}

func (w *centralWriteWrapper) Name() string   { return w.inner.Name() }
func (w *centralWriteWrapper) Schema() tool.Schema { return w.inner.Schema() }
func (w *centralWriteWrapper) Risk() tool.Risk  { return w.inner.Risk() }

func (w *centralWriteWrapper) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	name := w.inner.Name()

	// edit / delete 永远建议 delegate
	if strings.HasSuffix(name, ".edit_file") || strings.HasSuffix(name, ".delete_file") {
		return centralDispatchResult(
			fmt.Sprintf("%s modifies existing project files; central brain should orchestrate, not edit directly", name),
			"call central.delegate with brain_id=\"code\" and pass the modification request",
		), nil
	}

	// write_file 看 content 大小
	if strings.HasSuffix(name, ".write_file") {
		var params struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(args, &params); err == nil {
			if len(params.Content) > 5000 {
				return centralDispatchResult(
					fmt.Sprintf("write_file content is %d chars (>5000); this is real code, central should delegate", len(params.Content)),
					"call central.delegate with brain_id=\"code\" and prompt describing what to write",
				), nil
			}
		}
		// 小文件直接放行
	}

	return w.inner.Execute(ctx, args)
}

// CentralAwareShell 包装 shell_exec 工具。
// 中央大脑调用时按命令首词判断是只读还是写入。
//
// 只读命令白名单（cat/ls/grep/find 等）→ 直接执行
// 任何其他命令（rm/mv/cp/构建工具/包管理器 等）→ 建议 delegate
func CentralAwareShell(inner tool.Tool, brainKind string) tool.Tool {
	if brainKind != "central" {
		return inner
	}
	return &centralShellWrapper{inner: inner}
}

type centralShellWrapper struct {
	inner tool.Tool
}

func (w *centralShellWrapper) Name() string   { return w.inner.Name() }
func (w *centralShellWrapper) Schema() tool.Schema { return w.inner.Schema() }
func (w *centralShellWrapper) Risk() tool.Risk  { return w.inner.Risk() }

func (w *centralShellWrapper) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var params struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &params); err == nil && params.Command != "" {
		first := firstShellWord(params.Command)
		if !readOnlyShellCommands[first] {
			return centralDispatchResult(
				fmt.Sprintf("command %q is not in the central read-only allowlist; this likely modifies state or runs build/test", first),
				"call central.delegate with brain_id=\"code\" (build/test/file ops) or brain_id=\"verifier\" (test verification)",
			), nil
		}

		// 即使命令首词在白名单（如 git），也要检查是不是写操作
		if isWriteSubcommand(first, params.Command) {
			return centralDispatchResult(
				fmt.Sprintf("command %q has write subcommand; central should delegate", trimForDisplay(params.Command, 80)),
				"call central.delegate with brain_id=\"code\" instead",
			), nil
		}
	}
	return w.inner.Execute(ctx, args)
}

// firstShellWord 提取命令的第一个词（命令名）。
// 处理常见前缀（sudo、env VAR=val 等）。
func firstShellWord(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	for {
		// 跳过 env-style 前缀（VAR=val cmd）
		first := strings.SplitN(cmd, " ", 2)[0]
		if strings.Contains(first, "=") && !strings.HasPrefix(first, "=") {
			parts := strings.SplitN(cmd, " ", 2)
			if len(parts) < 2 {
				return ""
			}
			cmd = strings.TrimSpace(parts[1])
			continue
		}
		// 跳过 sudo
		if first == "sudo" {
			parts := strings.SplitN(cmd, " ", 2)
			if len(parts) < 2 {
				return "sudo"
			}
			cmd = strings.TrimSpace(parts[1])
			continue
		}
		return first
	}
}

// isWriteSubcommand 检查白名单命令的子命令是否是写操作。
// 例：git commit / git push / git reset → 是；git status / git log → 否
func isWriteSubcommand(first, full string) bool {
	parts := strings.Fields(full)
	if len(parts) < 2 {
		return false
	}
	sub := parts[1]
	switch first {
	case "git":
		writeSubs := map[string]bool{
			"commit": true, "push": true, "pull": true, "merge": true, "rebase": true,
			"reset": true, "checkout": true, "switch": true, "restore": true,
			"add": true, "rm": true, "mv": true, "stash": true, "tag": true,
			"clone": true, "fetch": true, "init": true, "apply": true, "am": true,
			"cherry-pick": true, "revert": true, "branch": true, // branch -d 也是写
		}
		return writeSubs[sub]
	case "go":
		writeSubs := map[string]bool{
			"build": true, "install": true, "test": true, "run": true, "generate": true,
			"get": true, "mod": true, "work": true, "clean": true, "vet": true, "fix": true,
		}
		return writeSubs[sub]
	}
	return false
}

func trimForDisplay(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
