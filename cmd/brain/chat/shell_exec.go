// shell_exec.go — chat 模式下的 ! 命令直接执行（不走 LLM）。
//
// claude-code 风格：用户输入 `!ls` / `!git status` 等命令时，不发给 LLM 思考，
// 直接用 chat host 进程的 cwd 跑 sh -c <cmd>，把输出贴回 chat 流。
//
// 设计取舍：
//   - 不走 sandbox / 不走 file_policy / 不走 approval —— 用户自己输的就是用户授权
//   - 不走 bwrap mount namespace —— 用户期望命令真在自己的项目目录跑
//   - workdir 优先用 chat 启动时的 workdir，没有则用进程 cwd
//   - 超时 60 秒（避免误输入卡死整个 chat）
//   - stdout / stderr 都打到 chat 流，区分颜色
package chat

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ExecuteShellCommand 在指定 workdir 直接跑 sh -c <command>，把输出原样打到屏幕。
// workdir 为空时用进程 cwd。
func ExecuteShellCommand(command, workdir string) {
	command = strings.TrimSpace(command)
	if command == "" {
		fmt.Fprintln(os.Stderr, "  \033[33m· 空命令\033[0m\n")
		return
	}

	// 用户视角友好输出：上下加分隔线 + 显示要跑的命令
	fmt.Printf("\n\033[2m──────── shell ────────\033[0m\n")
	fmt.Printf("\033[2m$ %s\033[0m\n", command)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if workdir != "" {
		cmd.Dir = workdir
	}
	// 直接 inherit stdin（让交互式命令如 read 可用）+ 实时输出 stdout/stderr
	cmd.Stdin = os.Stdin
	cmd.Stdout = io.MultiWriter(os.Stdout)
	cmd.Stderr = io.MultiWriter(os.Stderr)

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	exit := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exit = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			fmt.Fprintf(os.Stderr, "\033[31m· 命令超时（60s）\033[0m\n")
			exit = 124
		} else {
			fmt.Fprintf(os.Stderr, "\033[31m· 启动失败: %v\033[0m\n", err)
			exit = -1
		}
	}

	if exit == 0 {
		fmt.Printf("\033[2m──────── done %s ────────\033[0m\n\n", elapsed.Round(time.Millisecond))
	} else {
		fmt.Printf("\033[31m──────── exit %d (%s) ────────\033[0m\n\n", exit, elapsed.Round(time.Millisecond))
	}
}
