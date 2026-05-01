// hooks.go — bridge 包暴露给 chat / serve 上层的回调钩子。
//
// 设计动机：bridge 包位于 sdk/kernel 之上、chat 之下，不能 import chat（会循环依赖）。
// 当 bridge 工具内部需要做"UI 友好打印"或"事件回写"时，通过这些钩子让上层注入逻辑。
//
// chat 包在 init / RunChat 时设置这些钩子；serve / run 模式不设置则全部 no-op。

package bridge

// VerbosePrint 是 chat 模式 verbose 输出的钩子。
// 非 nil 时，bridge 工具内部"啰嗦但有用"的进度提示会调它（默认走 stderr）。
// nil 时这些提示完全静默。
var VerbosePrint func(line string)

// WorkflowProgressHook 是 workflow 节点状态变化的钩子。
// 由 chat 包注入，把节点事件通过 ChatEvent 通道喂给当前 Activity 的 Todos 面板。
//
// event:    "init" | "running" | "completed" | "failed" | "skipped"
// nodeID:   workflow node ID（"init"/"engine" 等用户传入的 id）
// nodeName: 节点用户可读名（init 时填 prompt 摘要；其他事件可能为空）
// brain:    节点目标 brain kind（init 时填；其他空）
// detail:   失败时的错误摘要；其他空
//
// nil 时使用 bridge 内部的 fmt.Printf fallback。
var WorkflowProgressHook func(event, nodeID, nodeName, brain, detail string)
