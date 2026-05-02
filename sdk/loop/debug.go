// debug.go — sdk/loop 包的调试开关。
//
// sdk 包不能 import cmd/brain/config(分层禁止),所以用全局变量
// 让外层(cmd/brain)启动时根据 config.diagnostics.debug.runner 设置。
//
// 这些开关同时支持环境变量兜底(BRAIN_RUNNER_DEBUG=1 等),
// 方便单次排障无需改 config 重启。

package loop

// DebugRunner 控制 runner.go 每轮打印 stop_reason / tool_use_count 等诊断信息。
// 由 cmd/brain 启动期从 config.diagnostics.debug.runner 设。
// 也可以用环境变量 BRAIN_RUNNER_DEBUG=1 临时启用(runner.go 里 OR 检查)。
var DebugRunner bool
