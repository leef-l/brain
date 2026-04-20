package chat

import (
	"fmt"
	"strings"
	"time"
)

// streamStartTime 记录当前 run 的起始时间,用于给每条事件加相对秒数。
// 每次 run 开始重置(由 ResetStreamClock 调)。
var streamStartTime time.Time

// ResetStreamClock 由 chat REPL 在 StartChatRun 之前调,把相对时间归零。
func ResetStreamClock() {
	streamStartTime = time.Now()
}

func elapsedTag() string {
	if streamStartTime.IsZero() {
		return ""
	}
	sec := int64(time.Since(streamStartTime) / time.Second)
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	return fmt.Sprintf("%dm%02ds", sec/60, sec%60)
}

// StreamProgressEvent 把一条 ProgressEvent 以精简风格实时打到终端。
// running 期间 prompt frame 被 Detach,事件直接 Println,用户立刻
// 看到 LLM 正在做什么(文字流 / 调工具 / 工具结果)。
//
// 设计取舍:
//   - content 事件:累积每次 delta,按行 flush,像聊天气泡那样渐进输出。
//   - tool_start:一行摘要 "Run: <tool>(<short args>)"。
//   - tool_end  :一行摘要 "Done: <tool>"(成功)/"Fail: <tool>: <err>"(失败)。
//   - thinking  :不打印(噪声大),仅记录到 Activity 内存态。
//   - finished  :不打印,让 HandleChatRunResult 渲染最终 assistant 回复。
//
// content flush 用一个全局 buffer,同一个 event 里的完整 content
// 一次性 Println;中间换行保持换行。
var contentBuf strings.Builder

func StreamProgressEvent(ev ProgressEvent) {
	switch ev.Kind {
	case ProgressContent:
		if ev.Text != "" {
			// 直接累积并刷到 stdout(不 Println,由文本自己控制换行)。
			fmt.Print(ev.Text)
			contentBuf.WriteString(ev.Text)
			// 换行后 reset buffer,让每段连续文本看起来独立。
			if strings.HasSuffix(ev.Text, "\n") {
				contentBuf.Reset()
			}
		}
	case ProgressToolPlan:
		if contentBuf.Len() > 0 {
			fmt.Println()
			contentBuf.Reset()
		}
		fmt.Printf("\033[2m  [%s] Plan: %s %s\033[0m\n", elapsedTag(), ev.ToolName, truncate(ev.Args, 120))
	case ProgressToolStart:
		if contentBuf.Len() > 0 {
			fmt.Println()
			contentBuf.Reset()
		}
		fmt.Printf("\033[2m  [%s] Run:  %s %s\033[0m\n", elapsedTag(), ev.ToolName, truncate(ev.Args, 120))
	case ProgressToolEnd:
		if contentBuf.Len() > 0 {
			fmt.Println()
			contentBuf.Reset()
		}
		if ev.OK {
			detail := truncate(ev.Detail, 120)
			if detail != "" {
				fmt.Printf("\033[2m  [%s] Done: %s — %s\033[0m\n", elapsedTag(), ev.ToolName, detail)
			} else {
				fmt.Printf("\033[2m  [%s] Done: %s\033[0m\n", elapsedTag(), ev.ToolName)
			}
		} else {
			fmt.Printf("\033[31m  [%s] Fail: %s — %s\033[0m\n", elapsedTag(), ev.ToolName, truncate(ev.Detail, 200))
		}
	case ProgressThinking:
		// 不打印:thinking token 对用户是噪声,仅记内存态。
	case ProgressFinished:
		// finished 让 HandleChatRunResult 接管渲染,本函数不介入。
		if contentBuf.Len() > 0 {
			fmt.Println()
			contentBuf.Reset()
		}
	}
}

// trimForDisplay 去掉首尾空白并截断到 n 字符(供 ProgressHandler 用)。
func trimForDisplay(s string, n int) string {
	return truncate(s, n)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
