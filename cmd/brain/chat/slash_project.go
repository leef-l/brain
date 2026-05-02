// slash_project.go — chat /project 子命令(MACCS Wave 7+ 多项目管理)
//
// 7 个子命令:
//   /project                       等价 /project list
//   /project list                  列当前 workdir 所有项目
//   /project new <name>            新建并切换到该项目
//   /project switch <name|id>      切到本 workdir 内别的项目
//   /project current               显示当前活动项目
//   /project rename <new>          重命名当前项目
//   /project delete <name|id>      删除项目(连同对话历史 + 项目记忆)
//   /project info                  当前项目元信息 + 统计
//   /project save <name>           从无项目模式中途救援:把当前对话保存为新项目

package chat

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/persistence"
)

// HandleProjectCommand 处理 /project 系列命令。
// 返回 (handled, shouldQuit)。handled=true 表示已处理。
func HandleProjectCommand(input string, state *State) (handled bool, shouldQuit bool) {
	cmd := strings.TrimSpace(input)
	if !strings.HasPrefix(cmd, "/project") {
		return false, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "/project"))

	if state.ProjectsStore == nil {
		fmt.Println("  \033[33m项目管理未启用(持久化未配置)。\033[0m")
		fmt.Println()
		return true, false
	}

	ctx := context.Background()
	switch {
	case rest == "" || rest == "list" || rest == "ls":
		projectListCmd(ctx, state)
	case rest == "current":
		projectCurrentCmd(state)
	case rest == "info":
		projectInfoCmd(ctx, state)
	case rest == "help":
		projectHelpCmd()
	case strings.HasPrefix(rest, "new "):
		name := strings.TrimSpace(rest[4:])
		projectNewCmd(ctx, state, name)
	case strings.HasPrefix(rest, "switch "):
		target := strings.TrimSpace(rest[7:])
		projectSwitchCmd(ctx, state, target)
	case strings.HasPrefix(rest, "rename "):
		newName := strings.TrimSpace(rest[7:])
		projectRenameCmd(ctx, state, newName)
	case strings.HasPrefix(rest, "delete "):
		target := strings.TrimSpace(rest[7:])
		projectDeleteCmd(ctx, state, target)
	case strings.HasPrefix(rest, "save "):
		name := strings.TrimSpace(rest[5:])
		projectSaveCmd(ctx, state, name)
	default:
		fmt.Printf("  \033[33m未知命令 %q,使用 /project help 查看帮助\033[0m\n\n", rest)
	}
	return true, false
}

// ── 子命令实现 ──────────────────────────────────────────────────────

func projectHelpCmd() {
	fmt.Println("  /project              列出当前工作目录的所有项目")
	fmt.Println("  /project list         同上")
	fmt.Println("  /project current      显示当前活动项目")
	fmt.Println("  /project info         当前项目元信息 + 统计")
	fmt.Println("  /project new <name>   新建项目并切换")
	fmt.Println("  /project switch <name|id>  切到本工作目录的别的项目")
	fmt.Println("  /project rename <new> 重命名当前项目")
	fmt.Println("  /project delete <name|id>  删除项目(含对话历史 + 项目记忆)")
	fmt.Println("  /project save <name>  把当前对话保存为新项目(无项目模式救援)")
	fmt.Println()
}

func projectListCmd(ctx context.Context, state *State) {
	projects, err := state.ProjectsStore.ListByWorkdir(ctx, state.CurrentWorkdir)
	if err != nil {
		fmt.Printf("  \033[1;31m! 列项目失败: %v\033[0m\n\n", err)
		return
	}
	if len(projects) == 0 {
		fmt.Println("  当前工作目录无项目")
		fmt.Println()
		return
	}
	fmt.Printf("  Workdir: %s\n", state.CurrentWorkdir)
	fmt.Printf("  项目数: %d\n\n", len(projects))
	for i, p := range projects {
		marker := " "
		if state.CurrentProject != nil && state.CurrentProject.ID == p.ID {
			marker = "*"
		}
		fmt.Printf("  %s [%d] %-30s id=%s  上次活动: %s\n",
			marker, i+1, p.Name, p.ID[:8]+"…", humanizeAgo(p.LastActiveAt))
	}
	fmt.Println()
}

func projectCurrentCmd(state *State) {
	if state.CurrentProject == nil {
		if state.IsNoProject {
			fmt.Println("  当前为无项目模式,本次对话不持久化")
			fmt.Println("  用 /project save <name> 把对话保存为新项目")
		} else {
			fmt.Println("  当前未选择项目")
		}
		fmt.Println()
		return
	}
	p := state.CurrentProject
	fmt.Printf("  Project:  %s\n", p.Name)
	fmt.Printf("  ID:       %s\n", p.ID)
	fmt.Printf("  Workdir:  %s\n", p.Workdir)
	fmt.Printf("  Created:  %s\n", p.CreatedAt.Format(time.RFC3339))
	fmt.Printf("  Active:   %s\n\n", p.LastActiveAt.Format(time.RFC3339))
}

func projectInfoCmd(ctx context.Context, state *State) {
	if state.CurrentProject == nil {
		projectCurrentCmd(state)
		return
	}
	p := state.CurrentProject
	projectCurrentCmd(state)
	if state.ProjectStore != nil {
		msgs, _ := state.ProjectStore.LoadMessages(ctx, p.ID, 10000)
		fmt.Printf("  对话条数: %d\n", len(msgs))
	}
	if state.ProjectMemoryStore != nil {
		entries, _ := state.ProjectMemoryStore.QueryEntries(ctx, persistence.MemoryQueryRecord{
			ProjectID: p.ID,
			Limit:     10000,
		})
		fmt.Printf("  记忆条目: %d\n", len(entries))
	}
	fmt.Println()
}

func projectNewCmd(ctx context.Context, state *State, name string) {
	if name == "" {
		fmt.Println("  \033[33m用法: /project new <name>\033[0m")
		fmt.Println()
		return
	}
	p := &persistence.ProjectMeta{
		Workdir: state.CurrentWorkdir,
		Name:    name,
	}
	if err := state.ProjectsStore.Create(ctx, p); err != nil {
		fmt.Printf("  \033[1;31m! 创建失败: %v\033[0m\n\n", err)
		return
	}
	// 统一切换:加载历史(空) + 升级 ContextEngine + 重置 state.Messages
	ApplyProjectChange(state, p, false)
	fmt.Printf("  \033[32m✓ 已创建并切换到项目 %q (id=%s)\033[0m\n\n", p.Name, p.ID)
}

func projectSwitchCmd(ctx context.Context, state *State, target string) {
	if target == "" {
		fmt.Println("  \033[33m用法: /project switch <name|id>\033[0m")
		fmt.Println()
		return
	}
	target = strings.TrimSpace(target)
	target = strings.Trim(target, "\"'")

	// 先按 ID 查
	p, _ := state.ProjectsStore.Get(ctx, target)
	if p == nil {
		// 再按 name 查
		p, _ = state.ProjectsStore.FindByName(ctx, state.CurrentWorkdir, target)
	}
	if p == nil {
		fmt.Printf("  \033[1;31m! 找不到项目 %q\033[0m\n\n", target)
		return
	}
	// 验证项目属于当前 workdir(防止误切到别处)
	if p.Workdir != state.CurrentWorkdir {
		fmt.Printf("  \033[33m! 项目 %q 不属于当前工作目录 (%s)\033[0m\n\n", p.Name, p.Workdir)
		return
	}
	_ = state.ProjectsStore.UpdateLastActive(ctx, p.ID, time.Now())
	// 统一切换:加载新项目历史 + 升级 ContextEngine + 重置 state.Messages
	// 这是修复"切换后 LLM 看不到新项目历史"的关键
	ApplyProjectChange(state, p, true)
	fmt.Printf("  \033[32m✓ 已切换到项目 %q\033[0m\n\n", p.Name)
}

func projectRenameCmd(ctx context.Context, state *State, newName string) {
	if state.CurrentProject == nil {
		fmt.Println("  \033[33m没有当前项目可重命名\033[0m")
		fmt.Println()
		return
	}
	if newName == "" {
		fmt.Println("  \033[33m用法: /project rename <new-name>\033[0m")
		fmt.Println()
		return
	}
	old := state.CurrentProject.Name
	if err := state.ProjectsStore.Rename(ctx, state.CurrentProject.ID, newName); err != nil {
		fmt.Printf("  \033[1;31m! 重命名失败: %v\033[0m\n\n", err)
		return
	}
	state.CurrentProject.Name = newName
	fmt.Printf("  \033[32m✓ %q → %q\033[0m\n\n", old, newName)
}

func projectDeleteCmd(ctx context.Context, state *State, target string) {
	if target == "" {
		fmt.Println("  \033[33m用法: /project delete <name|id>\033[0m")
		fmt.Println()
		return
	}
	target = strings.TrimSpace(target)
	target = strings.Trim(target, "\"'")

	p, _ := state.ProjectsStore.Get(ctx, target)
	if p == nil {
		p, _ = state.ProjectsStore.FindByName(ctx, state.CurrentWorkdir, target)
	}
	if p == nil {
		fmt.Printf("  \033[1;31m! 找不到项目 %q\033[0m\n\n", target)
		return
	}

	// 二次确认
	msgCount := 0
	if state.ProjectStore != nil {
		msgs, _ := state.ProjectStore.LoadMessages(ctx, p.ID, 100000)
		msgCount = len(msgs)
	}
	memCount := 0
	if state.ProjectMemoryStore != nil {
		entries, _ := state.ProjectMemoryStore.QueryEntries(ctx, persistence.MemoryQueryRecord{
			ProjectID: p.ID,
			Limit:     100000,
		})
		memCount = len(entries)
	}
	fmt.Printf("  \033[33m将删除项目 %q (含 %d 条对话, %d 条记忆)。确认? [y/N]: \033[0m",
		p.Name, msgCount, memCount)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		fmt.Println("  已取消")
		fmt.Println()
		return
	}

	// 级联删除:对话历史 + 项目记忆 + 项目元数据
	if state.ProjectStore != nil {
		_ = state.ProjectStore.DeleteMessages(ctx, p.ID)
	}
	if state.ProjectMemoryStore != nil {
		// 没有批量删,逐条删
		entries, _ := state.ProjectMemoryStore.QueryEntries(ctx, persistence.MemoryQueryRecord{
			ProjectID: p.ID,
			Limit:     100000,
		})
		for _, e := range entries {
			_ = state.ProjectMemoryStore.DeleteEntry(ctx, e.ID)
		}
	}
	if err := state.ProjectsStore.Delete(ctx, p.ID); err != nil {
		fmt.Printf("  \033[1;31m! 删除项目元数据失败: %v\033[0m\n\n", err)
		return
	}

	// 如果删的是当前项目,降级为无项目模式 + 清空 messages + 降级 ContextEngine
	if state.CurrentProject != nil && state.CurrentProject.ID == p.ID {
		ApplyProjectChange(state, nil, false)
		fmt.Printf("  \033[32m✓ 项目 %q 已删除,当前进入无项目模式\033[0m\n\n", p.Name)
	} else {
		fmt.Printf("  \033[32m✓ 项目 %q 已删除\033[0m\n\n", p.Name)
	}
}

func projectSaveCmd(ctx context.Context, state *State, name string) {
	if name == "" {
		fmt.Println("  \033[33m用法: /project save <name>\033[0m")
		fmt.Println()
		return
	}
	if state.CurrentProject != nil && !state.IsNoProject {
		fmt.Printf("  \033[33m已经在项目 %q 中,使用 /project rename 改名\033[0m\n\n",
			state.CurrentProject.Name)
		return
	}
	// 创建新项目
	p := &persistence.ProjectMeta{
		Workdir: state.CurrentWorkdir,
		Name:    name,
	}
	if err := state.ProjectsStore.Create(ctx, p); err != nil {
		fmt.Printf("  \033[1;31m! 创建失败: %v\033[0m\n\n", err)
		return
	}

	// 把当前 chat state.Messages 写入项目对话历史
	if state.ProjectStore != nil && len(state.Messages) > 0 {
		// 过滤系统消息(可选——系统消息一般是 prompt 配置,不要进入项目历史)
		var toSave []llm.Message
		for _, m := range state.Messages {
			if m.Role == "system" {
				continue
			}
			toSave = append(toSave, m)
		}
		if err := state.ProjectStore.SaveMessages(ctx, p.ID, toSave); err != nil {
			fmt.Printf("  \033[33m! 保存对话历史失败: %v\033[0m\n", err)
		} else {
			fmt.Printf("  \033[32m✓ 已保存 %d 条消息到项目 %q\033[0m\n", len(toSave), p.Name)
		}
	}

	// save 特殊:保留当前 state.Messages 不动(已经写到 SQLite 里),
	// 但需要升级 ContextEngine 为持久化版,使后续对话也能从这个项目记忆受益。
	ApplyProjectChangeKeepMessages(state, p, false)
	fmt.Printf("  \033[32m✓ 当前模式切换为项目持久化模式 (id=%s)\033[0m\n\n", p.ID)
}
