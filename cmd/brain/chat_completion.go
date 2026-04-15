package main

import (
	"strings"
)

// slashCommand describes a slash command for completion hints.
type slashCommand struct {
	cmd  string // e.g. "/help"
	desc string // short description
}

// allSlashCommands returns the full list of available slash commands.
func allSlashCommands() []slashCommand {
	return []slashCommand{
		{"/help", "显示帮助"},
		{"/clear", "清除对话历史"},
		{"/history", "显示对话轮次"},
		{"/mode", "查看/切换模式"},
		{"/tools", "列出可用工具"},
		{"/sandbox", "查看/授权目录"},
		{"/brain", "查看大脑状态"},
		{"/brain start <kind>", "启动指定大脑"},
		{"/brain start all", "启动所有大脑"},
		{"/brain stop <kind>", "停止指定大脑"},
		{"/brain stop all", "停止所有大脑"},
		{"/keys", "查看快捷键配置"},
		{"/exit", "退出 chat"},
	}
}

// slashCompletionLines returns hint lines for the current input prefix.
// Returns nil if the input doesn't start with "/".
func slashCompletionLines(input string) []string {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return nil
	}

	prefix := strings.ToLower(input)
	var matches []slashCommand
	for _, sc := range allSlashCommands() {
		if strings.HasPrefix(sc.cmd, prefix) {
			matches = append(matches, sc)
		}
	}

	if len(matches) == 0 {
		return nil
	}

	lines := make([]string, 0, len(matches))
	for _, m := range matches {
		lines = append(lines, "\033[2m"+m.cmd+"  "+m.desc+"\033[0m")
	}
	return lines
}
