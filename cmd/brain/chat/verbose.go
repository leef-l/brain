// verbose.go — chat 模式的 verbose 显示开关。
//
// 默认 false：用户只看见 spinner / LLM 文本 / todo 框 / 最终结果摘要。
// 开启 true（/verbose 命令切换）：额外显示 Plan / Run / Done / Fail 三连、
// "delegating to X" 提示、workflow 节点逐步事件、结束元数据等"plumbing"。
//
// 任何"啰嗦"打印必须先 `if VerboseEnabled() {}` 守卫。
package chat

import (
	"fmt"
	"sync/atomic"
)

// verboseFlag 是 chat 模式 verbose 的全局开关。
// 用 atomic.Bool 避免读写竞争（progress ticker 可能并发读）。
var verboseFlag atomic.Bool

// verboseHooks 是 verbose 切换时的回调列表（同步给依赖 verbose 的其他包，
// 如 cliruntime 的 shell stream 开关）。chat_aliases.go 在 init 时注册。
var verboseHooks []func(on bool)

// RegisterVerboseHook 注册一个 verbose 状态变化时的回调。
// chat_aliases.go init 时调用，保证 SetVerbose / ToggleVerbose 改变状态后，
// 其他包（cliruntime / bridge）能同步切换自己的开关。
func RegisterVerboseHook(fn func(on bool)) {
	verboseHooks = append(verboseHooks, fn)
}

// estimatorInjector 由 chat_aliases.go init 时设置，签名是
// func(learner *kernel.LearningEngine)。chat 包不直接 import bridge，
// 通过这个 hook 让外层在 RunChat 启动时把 learner 注入 bridge.delegate 的
// ComplexityEstimator。
//
// 用 interface{} 是因为 verbose.go 不应 import kernel（避免循环），由
// chat_aliases.go 那边做类型断言。
var estimatorInjector func(learner interface{})

// RegisterEstimatorInjector 由外层（chat_aliases.go）在包加载时注册。
func RegisterEstimatorInjector(fn func(learner interface{})) {
	estimatorInjector = fn
}

// InjectEstimatorWithLearner 在 RunChat 启动时调用，把当前 chat session 的
// 持久化 LearningEngine 注入到 bridge.delegate 的 estimator。
// 没注册 injector（serve / run 模式）时静默 no-op。
func InjectEstimatorWithLearner(learner interface{}) {
	if estimatorInjector != nil {
		estimatorInjector(learner)
	}
}

// VerboseEnabled 返回当前是否启用 verbose 显示。
func VerboseEnabled() bool {
	return verboseFlag.Load()
}

// SetVerbose 切换 verbose 状态，返回切换后的值。
// /verbose 命令调用此函数。
func SetVerbose(on bool) bool {
	verboseFlag.Store(on)
	for _, fn := range verboseHooks {
		fn(on)
	}
	return on
}

// ToggleVerbose 翻转当前 verbose，返回切换后的值。
func ToggleVerbose() bool {
	for {
		cur := verboseFlag.Load()
		if verboseFlag.CompareAndSwap(cur, !cur) {
			next := !cur
			for _, fn := range verboseHooks {
				fn(next)
			}
			return next
		}
	}
}

// printVerboseStatus 打印一行 verbose 切换提示给用户看。
// 由 /verbose 命令调用，不应在其他场合调。
func printVerboseStatus(on bool) {
	if on {
		fmt.Println("  \033[33m· Verbose ON\033[0m  会显示工具 Plan/Run/Done、元数据、bridge 进度提示")
	} else {
		fmt.Println("  \033[2m· Verbose OFF\033[0m  仅显示思考行 + LLM 文本 + todo + 结果摘要")
	}
	fmt.Println()
}
