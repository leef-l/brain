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
// 中央大脑调用时根据**文件类型**(后缀)决定直接执行还是建议 delegate。
//
// 设计原则:central 是"动嘴"的角色,但写设计文档 / 笔记 / 进度报告等纯说明性文件
// 是中央协调的合理输出 — 这类文件不该绕远路 delegate code。
// "代码" vs "文档"的边界用文件后缀判断,比字符长度更准:
//   - 设计稿 30K 字符是合理的(放行)
//   - hello.go 100 字符也是真代码(拦截)
//
// 规则:
//   - write_file 写文档类(.md/.txt/.rst/.adoc/.markdown) → 放行,不限大小
//   - write_file 写其他后缀 → 拦截改建议 delegate code
//   - edit_file / delete_file → 永远建议 delegate(修改现有文件属于真工作)
//
// brainKind=="" 视为非中央大脑场景(如直接给 code sidecar 用),不拦截。
func CentralAwareWrite(inner tool.Tool, brainKind string) tool.Tool {
	if brainKind != "central" {
		return inner
	}
	return &centralWriteWrapper{inner: inner}
}

// docFileExts 是 central 可以直接写的文档类后缀(纯说明性,无可执行语义)。
// 任何代码 / 配置 / 标记 / 数据文件 (.go/.js/.html/.json/.yaml/.toml/.sh/.py 等) 都不在此列。
var docFileExts = map[string]bool{
	".md":       true,
	".markdown": true,
	".txt":      true,
	".rst":      true,
	".adoc":     true,
	".org":      true,
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

	// write_file 看文件后缀:文档类放行,代码/配置类拦截改 delegate。
	if strings.HasSuffix(name, ".write_file") {
		var params struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(args, &params); err == nil && params.Path != "" {
			ext := strings.ToLower(extOf(params.Path))
			if !docFileExts[ext] {
				return centralDispatchResult(
					fmt.Sprintf("write_file path=%q ext=%q is not a doc file (.md/.txt/.rst/.adoc/.org); central should not write code/config/data directly", params.Path, ext),
					"call central.delegate with brain_id=\"code\" and prompt describing what to write",
				), nil
			}
			// 文档类 → 放行,无大小限制。
			// 但 > largeDocCharThreshold 时在 result 里附加 hint,提示下次可以拆分,
			// 避免把整本 PRD / 多模块架构稿压成单文件。LLM 看到 hint 自行决定。
			if len(params.Content) > largeDocCharThreshold {
				return executeWithLargeDocHint(ctx, w.inner, args, params.Path, len(params.Content))
			}
		}
	}

	return w.inner.Execute(ctx, args)
}

// largeDocCharThreshold 触发拆分建议的字符阈值。50K 是经验值:
// - Claude/GPT 单次响应 max_tokens 上限附近(~32K tokens ≈ 100K chars)的一半,留余量
// - 超过这个长度的设计文档通常多模块,outline + 子文档结构更清晰、便于增量更新
const largeDocCharThreshold = 50000

// executeWithLargeDocHint 写文件成功后,把"建议拆分"hint 合并进结果 JSON。
// 不阻断写入,只在 LLM 看到的 result 里追加结构化提示,让它下次自行优化。
func executeWithLargeDocHint(ctx context.Context, inner tool.Tool, args json.RawMessage, path string, size int) (*tool.Result, error) {
	res, err := inner.Execute(ctx, args)
	if err != nil || res == nil || res.IsError {
		return res, err
	}

	// 把原 output 解析成 map,补充 hint 字段。失败时退回原结果(不影响主流程)。
	var orig map[string]interface{}
	if jerr := json.Unmarshal(res.Output, &orig); jerr != nil {
		return res, nil
	}
	orig["large_doc_hint"] = fmt.Sprintf(
		"wrote %d chars to %q. For docs this large, consider an outline file + per-section files (e.g. README.md + designs/01-x.md, 02-y.md) — easier to update incrementally and to delegate per-section drafting to code brain if needed.",
		size, path,
	)
	merged, mErr := json.Marshal(orig)
	if mErr != nil {
		return res, nil
	}
	return &tool.Result{Output: merged, IsError: res.IsError}, nil
}

// extOf 提取文件路径的扩展名(含 .)。无扩展名返回 ""。
func extOf(path string) string {
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return ""
	}
	// 防止把目录里的 . 当扩展(如 ".brain/config")
	if strings.ContainsAny(path[idx:], "/\\") {
		return ""
	}
	return path[idx:]
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
