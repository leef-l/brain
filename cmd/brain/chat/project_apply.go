// project_apply.go — 项目切换的统一 helper(MACCS Wave 7+ 持久化闭环)
//
// 任何"切换当前项目"的操作(启动 picker / /project new / switch / delete / save)
// 都必须调 ApplyProjectChange,集中处理 5 件事:
//   1. state.CurrentProject = newProject
//   2. state.IsNoProject = (newProject == nil)
//   3. state.Messages 重新加载该项目历史(或清空)
//   4. state.TurnCount 重新计
//   5. ContextEngine 升级/降级:有项目 → 装 PersistentProjectMemory 包装;无项目 → 退回 base
//
// 没有这个 helper 之前,各处分散写,容易漏一处导致"切了项目但 LLM 看不到新历史"。

package chat

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/persistence"
)

// ApplyProjectChange 把项目切换的 5 件事一次性做完。
//
// newProject 为 nil 时进入"无项目模式":清空消息 + 降级 ContextEngine。
// printPrefix 是日志前缀(如 "  Loaded N messages from project ...")。
// 调用方需保证 state 非 nil。
func ApplyProjectChange(state *State, newProject *persistence.ProjectMeta, verbose bool) {
	applyProjectChangeImpl(state, newProject, true /* reloadMessages */, verbose)
}

// ApplyProjectChangeKeepMessages 升级 ContextEngine + 切 CurrentProject,但保留
// 当前 state.Messages 不动。专给 /project save 救援命令用 — 那时 state.Messages
// 已经被写入 SQLite,不应该再 LoadMessages 覆盖。
func ApplyProjectChangeKeepMessages(state *State, newProject *persistence.ProjectMeta, verbose bool) {
	applyProjectChangeImpl(state, newProject, false /* reloadMessages */, verbose)
}

func applyProjectChangeImpl(state *State, newProject *persistence.ProjectMeta, reloadMessages bool, verbose bool) {
	if state == nil {
		return
	}

	// 1+2. 切项目元数据
	state.CurrentProject = newProject
	state.IsNoProject = (newProject == nil)

	if reloadMessages {
		// 3+4. 重置消息列表 + TurnCount
		// 切换/删除 都应该让 state.Messages 反映新项目的历史(或清空)
		state.Messages = nil
		state.TurnCount = 0
	}

	// 5. ContextEngine 升级/降级
	// 注意:必须用 base DefaultContextEngine 包装,而不是反复嵌套。
	// 我们把 orch 当前的 ContextEngine 解包到 *DefaultContextEngine 再重新包。
	updateContextEngine(state, newProject)

	if newProject == nil {
		if verbose {
			fmt.Println("  \033[2m已切换到无项目模式,本次起对话不持久化\033[0m")
		}
		return
	}

	if !reloadMessages {
		if verbose {
			fmt.Printf("  \033[2m已切换到项目 %q (保留当前对话)\033[0m\n", newProject.Name)
		}
		return
	}

	// 加载新项目的历史
	if state.ProjectStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		history, err := state.ProjectStore.LoadMessages(ctx, newProject.ID, 50)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  \033[33m! 加载项目 %q 历史失败: %v\033[0m\n", newProject.Name, err)
			return
		}
		if len(history) > 0 {
			state.Messages = append(state.Messages, history...)
			userCount := 0
			for _, m := range history {
				if m.Role == "user" {
					userCount++
				}
			}
			state.TurnCount = userCount
			if verbose {
				fmt.Printf("  \033[2mLoaded %d messages from project %q (%d user turns)\033[0m\n",
					len(history), newProject.Name, userCount)
			}
		} else if verbose {
			fmt.Printf("  \033[2m项目 %q 暂无历史对话\033[0m\n", newProject.Name)
		}
	}
}

// updateContextEngine 根据新项目状态升级/降级 Orchestrator.ContextEngine。
//
// 升级路径:有项目 + 有 ProjectMemoryStore → 包装为 ContextEngineWithMemory(persistent)
// 降级路径:无项目 → 解包回 *DefaultContextEngine(去掉 ProjectMemory)
//
// 包装/解包需要拿到 base DefaultContextEngine。如果当前是 ContextEngineWithMemory,
// 我们用其 Engine() 方法拿底层 base;如果当前是 DefaultContextEngine,直接用。
func updateContextEngine(state *State, newProject *persistence.ProjectMeta) {
	if state == nil || state.Orchestrator == nil {
		return
	}
	current := state.Orchestrator.ContextEngine()
	if current == nil {
		return
	}

	// 拿到 base *DefaultContextEngine
	var base *kernel.DefaultContextEngine
	switch ce := current.(type) {
	case *kernel.DefaultContextEngine:
		base = ce
	case *kernel.ContextEngineWithMemory:
		base = ce.Engine()
	default:
		// 未知 ContextEngine 类型 — 不动它
		return
	}
	if base == nil {
		return
	}

	if newProject == nil {
		// 降级:无项目模式直接用 base
		state.Orchestrator.SetContextEngine(base)
		return
	}

	// 升级:用持久化 ProjectMemory 包装
	if state.ProjectMemoryStore == nil {
		// 没有持久化 store(file/mem driver 仍能用内存版),退回 base
		// 也可以考虑 fallback NewMemProjectMemory,但默认 base 行为最简单
		state.Orchestrator.SetContextEngine(base)
		return
	}
	persistentMem := kernel.NewPersistentProjectMemory(state.ProjectMemoryStore)
	wrapped := kernel.NewContextEngineWithMemory(base, persistentMem)
	state.Orchestrator.SetContextEngine(wrapped)
}
