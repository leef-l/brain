// failure_log.go — 工具调用失败的结构化日志。
//
// 设计动机：用户遇到工具失败（如 shell_exec "cd: No such file or directory" 或
// write_file 路径越权）时，UI 默认折叠详细信息只展示一行红色 ✗。但失败信息对后续
// 排查 LLM 误调用 / 提示词缺陷 / 路径预检不足都至关重要，必须落到磁盘等待人工分析。
//
// 输出位置：~/.brain/logs/tool-failures.log（JSON Lines，append-only）
// 输出格式：每行一个 JSON 对象，包含
//   - ts:        ISO8601 时间戳
//   - tool:      工具名（如 "central.shell_exec"）
//   - brain:     调用方 brain kind（central / code / browser ...）
//   - args:      工具入参完整 JSON（用于复现）
//   - detail:    工具返回的 output（截断到 4KB，避免 stderr 爆炸）
//   - is_error:  true（只记录失败）
//
// 设计权衡：与现有 diaglog（默认 stderr，受 config.diagnostics 控制）分离，
// 因为本日志是"无条件 always-on"的诊断输出，不应被 diagnostics off 关掉。

package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	failureLogFilename = "tool-failures.log"
	failureLogMaxBytes = 4096 // 单条记录的 detail 字段截断阈值
)

var (
	failureLogMu     sync.Mutex
	failureLogFile   *os.File
	failureLogPath   string
	failureLogOpened bool
)

// LogToolFailure 把一次失败的工具调用追加到 ~/.brain/logs/tool-failures.log。
// 调用方传入 brainKind（来自 schema.Brain，用于区分中央 / 专精）+ 完整 args + detail。
//
// 写入失败时静默忽略（日志不应阻塞主流程）。
// detail 超过 failureLogMaxBytes 时截断，避免 sidecar 全量 stderr 灌爆磁盘。
func LogToolFailure(toolName, brainKind string, args, detail string) {
	if !ensureFailureLogOpen() {
		return
	}

	if len(detail) > failureLogMaxBytes {
		detail = detail[:failureLogMaxBytes] + "...(truncated)"
	}

	record := map[string]interface{}{
		"ts":       time.Now().UTC().Format(time.RFC3339Nano),
		"tool":     toolName,
		"brain":    brainKind,
		"args":     args,
		"detail":   detail,
		"is_error": true,
	}
	line, err := json.Marshal(record)
	if err != nil {
		return
	}

	failureLogMu.Lock()
	defer failureLogMu.Unlock()
	if failureLogFile == nil {
		return
	}
	_, _ = failureLogFile.Write(append(line, '\n'))
}

// FailureLogPath 返回当前激活的日志文件路径（便于 UI / doctor 引用）。
// 未打开时返回空字符串。
func FailureLogPath() string {
	failureLogMu.Lock()
	defer failureLogMu.Unlock()
	return failureLogPath
}

// ensureFailureLogOpen 惰性打开日志文件（首次调用时打开，后续复用）。
// 返回 true 表示文件可写，false 表示无法定位 ~/.brain/logs（fallback 静默丢弃）。
func ensureFailureLogOpen() bool {
	failureLogMu.Lock()
	defer failureLogMu.Unlock()

	if failureLogOpened {
		return failureLogFile != nil
	}
	failureLogOpened = true

	dir := resolveBrainLogsDir()
	if dir == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	path := filepath.Join(dir, failureLogFilename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return false
	}
	failureLogFile = f
	failureLogPath = path
	return true
}

// resolveBrainLogsDir 解析 ~/.brain/logs 路径。
// 优先用 BRAIN_HOME，其次 $HOME/.brain；都没有时返回空。
func resolveBrainLogsDir() string {
	if base := strings.TrimSpace(os.Getenv("BRAIN_HOME")); base != "" {
		return filepath.Join(base, "logs")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".brain", "logs")
}

// CloseFailureLog 关闭日志文件（程序退出时调用）。
// 测试或多次启动时也用此函数重置。
func CloseFailureLog() {
	failureLogMu.Lock()
	defer failureLogMu.Unlock()
	if failureLogFile != nil {
		_ = failureLogFile.Close()
		failureLogFile = nil
	}
	failureLogPath = ""
	failureLogOpened = false
}

// formatArgsForLog 把 json.RawMessage 转为字符串用于日志记录。
// 入参为 nil / 无效 JSON 时返回 "{}"，避免日志里出现裸 binary。
func formatArgsForLog(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	if !json.Valid(raw) {
		return fmt.Sprintf("%q", string(raw))
	}
	return string(raw)
}
