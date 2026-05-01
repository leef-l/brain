// verbose.go — cliruntime 包的 verbose 钩子。
//
// chat 包（导入 cliruntime）通过 SetVerboseShellStream 注入"是否实时流式 shell 输出"的判断。
// 默认 false：shell stdout 不会打到终端（避免污染 chat UI）；
// /verbose on 时 chat 包会切到 true，让用户看到 long-running 命令的实时输出。
package cliruntime

import "sync/atomic"

var verboseShellStream atomic.Bool

// VerboseShellStream 当前是否启用 shell 实时流式输出。
// chat 包构建工具时调此函数判断要不要把 StreamTo 设成 os.Stderr。
func VerboseShellStream() bool {
	return verboseShellStream.Load()
}

// SetVerboseShellStream 由 chat 的 /verbose 命令调用，切换 shell 实时输出。
// 注意：已经创建的工具的 StreamTo 是绑死的，切换后只影响**新创建**的工具。
// 实际中由于每次 RunChat 启动会重建 registry，下次问答就会生效。
func SetVerboseShellStream(on bool) {
	verboseShellStream.Store(on)
}
