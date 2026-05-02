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
		{"/project", "列出当前工作目录的项目"},
		{"/project new <name>", "创建新项目"},
		{"/project switch <name>", "切换到指定项目"},
		{"/project current", "显示当前项目"},
		{"/project info", "显示项目详情"},
		{"/project rename <new>", "重命名当前项目"},
		{"/project delete <name>", "删除项目"},
		{"/project save <name>", "另存为新项目"},
		{"/project help", "显示 project 帮助"},
		{"/takeover", "进入手动接管模式"},
		{"/resume", "继续等待中的 agent"},
		{"/abort", "中止等待中的 agent"},
		{"/like", "标记上一轮为有帮助 (L3 学习)"},
		{"/dislike", "标记上一轮为无帮助 (L3 学习)"},
		{"/verbose", "切换/开关 verbose 显示"},
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
