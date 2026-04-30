package chat

import "strings"

type SlashCommand struct {
	Cmd  string
	Desc string
}

func AllSlashCommands() []SlashCommand {
	return []SlashCommand{
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
		{"/plan", "创建智能项目计划"},
		{"/plan list", "列出已创建的计划"},
		{"/plan status", "查看计划进度"},
		{"/workflow", "提交 DAG workflow 执行"},
		{"/keys", "查看快捷键配置"},
		{"/exit", "退出 chat"},
	}
}

func SlashCompletionLines(input string) []string {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return nil
	}

	prefix := strings.ToLower(input)
	var matches []SlashCommand
	for _, sc := range AllSlashCommands() {
		if strings.HasPrefix(sc.Cmd, prefix) {
			matches = append(matches, sc)
		}
	}

	if len(matches) == 0 {
		return nil
	}

	lines := make([]string, 0, len(matches))
	for _, m := range matches {
		lines = append(lines, "\033[2m"+m.Cmd+"  "+m.Desc+"\033[0m")
	}
	return lines
}
