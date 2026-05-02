// project_picker.go — chat 启动期项目选择器(MACCS Wave 7+)
//
// 当 chat 启动时,根据 workdir 列出已有项目让用户选择,或新建,或跳过持久化。
// 强制二选一(选已有 / 新建 / 跳过)— 不允许沉默继续。
//
// 流程:
//   workdir 0 项目 → 提示 [n 新建] / [s 跳过]
//   workdir N 项目 → 列表 + [n 新建] / [s 跳过],默认回车=最近活动项目
//
// 命令行 flag 跳过交互:
//   brain chat --project NAME       直接进 (workdir, NAME),不存在则新建
//   brain chat --new-project NAME   直接新建 (workdir, NAME)
//   brain chat --no-project         直接进无项目模式

package chat

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/persistence"
)

// ProjectPickerOptions 描述 picker 的输入。
type ProjectPickerOptions struct {
	Store       persistence.ProjectsStore
	Workdir     string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	// CLI flag 直通(任一非空就跳过交互)
	ExplicitProject    string  // --project (找/建)
	ExplicitNewProject string  // --new-project (强制新建)
	NoProject          bool    // --no-project
}

// ProjectPickerResult 描述 picker 的决策结果。
type ProjectPickerResult struct {
	Project     *persistence.ProjectMeta // nil 表示无项目模式
	IsNoProject bool                      // true 表示用户主动跳过
}

// PickProject 在 chat 启动时让用户选择/创建项目。
// 流程:
//  1. 优先处理 explicit flag(--project/--new-project/--no-project)
//  2. 列出 workdir 下所有项目
//  3. 0 项目:提示 [n] 新建 / [s] 跳过
//  4. N 项目:列表 + [n]/[s],默认回车=最近活动
//  5. 用户选 [n] → 让输入项目名,创建
//  6. 用户选 [s] → IsNoProject=true
//  7. 用户选数字 → 取该项目
func PickProject(ctx context.Context, opts ProjectPickerOptions) (*ProjectPickerResult, error) {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.Store == nil {
		// 没存储 → 直接无项目模式
		return &ProjectPickerResult{IsNoProject: true}, nil
	}
	if strings.TrimSpace(opts.Workdir) == "" {
		return &ProjectPickerResult{IsNoProject: true}, nil
	}

	// ── 1. CLI flag 直通 ─────────────────────────────────────────────
	if opts.NoProject {
		return &ProjectPickerResult{IsNoProject: true}, nil
	}
	if opts.ExplicitProject != "" {
		p, err := opts.Store.FindByName(ctx, opts.Workdir, opts.ExplicitProject)
		if err != nil {
			return nil, fmt.Errorf("查找项目 %q 失败: %w", opts.ExplicitProject, err)
		}
		if p != nil {
			_ = opts.Store.UpdateLastActive(ctx, p.ID, time.Now())
			return &ProjectPickerResult{Project: p}, nil
		}
		// 不存在 → 新建
		return createProject(ctx, opts.Store, opts.Workdir, opts.ExplicitProject)
	}
	if opts.ExplicitNewProject != "" {
		return createProject(ctx, opts.Store, opts.Workdir, opts.ExplicitNewProject)
	}

	// ── 2. 列出 workdir 下所有项目 ────────────────────────────────────
	projects, err := opts.Store.ListByWorkdir(ctx, opts.Workdir)
	if err != nil {
		return nil, fmt.Errorf("列出工作目录项目失败: %w", err)
	}

	reader := bufio.NewReader(opts.Stdin)
	out := opts.Stdout

	// ── 3. 0 项目 ─────────────────────────────────────────────────────
	if len(projects) == 0 {
		fmt.Fprintf(out, "\n  当前工作目录下尚无项目。\n\n")
		fmt.Fprintf(out, "    [n] 新建项目(持久化对话与项目记忆,推荐)\n")
		fmt.Fprintf(out, "    [s] 不使用项目(单次对话,本次结束就丢)\n\n")
		fmt.Fprintf(out, "  请选择 [n / s]: ")
		choice, err := readLine(reader)
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "s":
			fmt.Fprintf(out, "  ⚠ 已进入「无项目模式」,本次对话不持久化\n\n")
			return &ProjectPickerResult{IsNoProject: true}, nil
		case "n", "":
			return promptCreateProject(ctx, opts.Store, opts.Workdir, reader, out)
		default:
			fmt.Fprintf(out, "  无效选项,默认进入新建流程\n")
			return promptCreateProject(ctx, opts.Store, opts.Workdir, reader, out)
		}
	}

	// ── 4. N 项目:列表 ───────────────────────────────────────────────
	fmt.Fprintf(out, "\n  当前工作目录下有 %d 个项目:\n\n", len(projects))
	for i, p := range projects {
		fmt.Fprintf(out, "    [%d] %-30s (上次活动: %s)\n",
			i+1, p.Name, humanizeAgo(p.LastActiveAt))
	}
	fmt.Fprintf(out, "\n    [n] 新建项目\n    [s] 不使用项目(单次对话,不持久化)\n\n")
	fmt.Fprintf(out, "  请选择 [回车=1 最近活动 / 1..%d / n / s]: ", len(projects))

	choice, err := readLine(reader)
	if err != nil {
		return nil, err
	}
	choice = strings.ToLower(strings.TrimSpace(choice))

	switch choice {
	case "":
		// 默认回车 → 选第一个(已按 last_active_at DESC 排)
		_ = opts.Store.UpdateLastActive(ctx, projects[0].ID, time.Now())
		fmt.Fprintf(out, "  ✓ 已恢复项目 %q\n\n", projects[0].Name)
		return &ProjectPickerResult{Project: projects[0]}, nil
	case "n":
		return promptCreateProject(ctx, opts.Store, opts.Workdir, reader, out)
	case "s":
		fmt.Fprintf(out, "  ⚠ 已进入「无项目模式」,本次对话不持久化\n\n")
		return &ProjectPickerResult{IsNoProject: true}, nil
	default:
		// 数字
		idx, err := strconv.Atoi(choice)
		if err != nil || idx < 1 || idx > len(projects) {
			fmt.Fprintf(out, "  无效选项 %q,默认选第 1 个\n", choice)
			idx = 1
		}
		p := projects[idx-1]
		_ = opts.Store.UpdateLastActive(ctx, p.ID, time.Now())
		fmt.Fprintf(out, "  ✓ 已恢复项目 %q\n\n", p.Name)
		return &ProjectPickerResult{Project: p}, nil
	}
}

// promptCreateProject 提示用户输入项目名并创建。
func promptCreateProject(ctx context.Context, store persistence.ProjectsStore,
	workdir string, reader *bufio.Reader, out io.Writer) (*ProjectPickerResult, error) {
	for {
		fmt.Fprintf(out, "  请输入项目名: ")
		name, err := readLine(reader)
		if err != nil {
			return nil, err
		}
		name = strings.TrimSpace(name)
		if name == "" {
			fmt.Fprintf(out, "  项目名不能为空,请重新输入\n")
			continue
		}
		return createProject(ctx, store, workdir, name)
	}
}

// createProject 创建项目并打印反馈。
func createProject(ctx context.Context, store persistence.ProjectsStore,
	workdir, name string) (*ProjectPickerResult, error) {
	p := &persistence.ProjectMeta{
		Workdir: workdir,
		Name:    name,
	}
	if err := store.Create(ctx, p); err != nil {
		return nil, fmt.Errorf("创建项目失败: %w", err)
	}
	return &ProjectPickerResult{Project: p}, nil
}

// readLine 读一行,去尾换行。
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// humanizeAgo 格式化"多久之前"。
func humanizeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}
