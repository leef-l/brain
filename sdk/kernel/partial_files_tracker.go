// partial_files_tracker.go — 通过 brain/progress 事件流收集 sub agent 的 fs_write 路径
//
// 设计动机:
//   Replan 触发时需要知道每个 sub agent 已写过哪些文件,才能 BackupPartialFiles 把
//   半成品文件备份+清理,让新 sub 从干净环境开始。
//
//   sidecar 已通过 brain/progress 事件流上报 tool_start(含 tool_name + args JSON)
//   实时回到 host。本组件订阅这些事件,匹配 fs_write / fs_create / fs_edit / shell.cd 等
//   写文件类工具,从 args 提取 path,按 task_id (来自 ExecutionID) 累积。
//
//   PlanOrchestrator.snapshotState 在收集 InterruptedTasks 时调
//   PartialFilesTracker.Get(taskID) 拿到这个 task 写过的所有文件。
//
// 设计文档:Replan-基于现有持久化与EventBus的渐进路线.md §3.5

package kernel

import (
	"encoding/json"
	"strings"
	"sync"
)

// fsWriteTools 是会产生新文件 / 修改已有文件的工具白名单。
// 命中这些 tool_name 时,从 args.path / args.file_path 提取路径累积。
//
// 第三方 brain 自定义工具如有写文件副作用,应遵循同一命名约定(包含 path / file_path 字段)。
var fsWriteTools = map[string]bool{
	"fs_write":       true,
	"fs_create":      true,
	"fs_edit":        true,
	"fs_replace":     true,
	"fs_append":      true,
	"file_write":     true,
	"write_file":     true,
	"create_file":    true,
	"edit_file":      true,
	"replace_in_file": true,
	"central.write_file":   true,
	"central.create_file":  true,
	"central.edit_file":    true,
}

// PartialFilesTracker 按 task_id(=ExecutionID)累积 sub agent 写过的文件路径。
//
// 线程安全:用 RWMutex 保护 map。Record 是高频写,RecordSnapshot/Clear 是 replan 时低频读。
type PartialFilesTracker struct {
	mu    sync.RWMutex
	files map[string][]string // taskID → []paths(去重)
}

// NewPartialFilesTracker 构造空 tracker。
func NewPartialFilesTracker() *PartialFilesTracker {
	return &PartialFilesTracker{files: make(map[string][]string)}
}

// Record 记录 task 的一次工具调用涉及的文件路径。
// toolName / argsJSON 来自 brain/progress tool_start 事件。
// 路径不识别 / 工具不在白名单 / argsJSON 解析失败时 silent 跳过。
func (t *PartialFilesTracker) Record(taskID, toolName string, argsJSON []byte) {
	if t == nil || taskID == "" || toolName == "" || len(argsJSON) == 0 {
		return
	}
	if !isFSWriteTool(toolName) {
		return
	}
	path := extractPathFromArgs(argsJSON)
	if path == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	existing := t.files[taskID]
	for _, p := range existing {
		if p == path {
			return // 去重
		}
	}
	t.files[taskID] = append(existing, path)
}

// Get 返回指定 task 已写文件路径列表的副本。
// 不存在的 taskID 返回 nil,nil 切片可安全 range / len(返回 0)。
func (t *PartialFilesTracker) Get(taskID string) []string {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	src := t.files[taskID]
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// Clear 清除指定 task 的累积记录。
// 在 SubTask 完成(无论 Completed / Failed)+ snapshotState 已读取后调用,避免内存累积。
func (t *PartialFilesTracker) Clear(taskID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.files, taskID)
}

// ClearAll 清除所有累积记录。chat session 退出 / 项目切换时调。
func (t *PartialFilesTracker) ClearAll() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.files = make(map[string][]string)
}

// isFSWriteTool 判断工具名是否会产生写文件副作用。
// 兼容前缀模式:central.* / code.* / browser.* 等 brain 命名空间。
func isFSWriteTool(toolName string) bool {
	if fsWriteTools[toolName] {
		return true
	}
	// 同时检测后缀部分(剥 namespace 前缀)
	if idx := strings.LastIndex(toolName, "."); idx >= 0 && idx < len(toolName)-1 {
		short := toolName[idx+1:]
		if fsWriteTools[short] {
			return true
		}
	}
	return false
}

// extractPathFromArgs 从工具参数 JSON 中提取文件路径。
// 兼容多种字段名:path / file_path / filename / target / file。
// 字段不存在或不是 string 时返回空串。
func extractPathFromArgs(argsJSON []byte) string {
	var args map[string]interface{}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return ""
	}
	for _, key := range []string{"path", "file_path", "filename", "filepath", "target", "file"} {
		if v, ok := args[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
