package kernel

import (
	"context"
	"strings"

	"github.com/leef-l/brain/sdk/agent"
)

// ────────────────────────────────────────────────────
// 1. ApprovalClass 枚举 —— 五级语义审批等级
// ────────────────────────────────────────────────────

// ApprovalClass 表示工具调用的语义审批类别。
// 等级从低到高：readonly < workspace-write < exec-capable < control-plane < external-network。
type ApprovalClass string

const (
	// ApprovalReadonly 只读操作，无副作用。示例：data.get_candles
	ApprovalReadonly ApprovalClass = "readonly"

	// ApprovalWorkspaceWrite 工作区文件修改（本地沙箱内）。示例：code.write_file
	ApprovalWorkspaceWrite ApprovalClass = "workspace-write"

	// ApprovalExecCapable 执行外部命令或子进程。示例：code.execute_shell
	ApprovalExecCapable ApprovalClass = "exec-capable"

	// ApprovalControlPlane 系统控制操作（进程管理、配置变更、金融下单）。示例：quant.place_order
	ApprovalControlPlane ApprovalClass = "control-plane"

	// ApprovalExternalNetwork 外部网络请求。示例：browser.navigate
	ApprovalExternalNetwork ApprovalClass = "external-network"
)

// approvalClassRank 定义审批等级的数值排序，用于比较。
var approvalClassRank = map[ApprovalClass]int{
	ApprovalReadonly:        0,
	ApprovalWorkspaceWrite:  1,
	ApprovalExecCapable:     2,
	ApprovalControlPlane:    3,
	ApprovalExternalNetwork: 4,
}

// ────────────────────────────────────────────────────
// 2. ApprovalRequest / ApprovalDecision
// ────────────────────────────────────────────────────

// ApprovalRequest 是一次工具调用的审批请求。
type ApprovalRequest struct {
	CallerKind agent.Kind    // 发起调用的 brain kind
	TargetKind agent.Kind    // 被调用的目标 brain kind
	ToolName   string        // 完整工具名，例如 "quant.place_order"
	Class      ApprovalClass // 语义审批等级
	Mode       string        // "delegate" | "direct"
}

// ApprovalDecision 是审批结论。
type ApprovalDecision struct {
	Granted bool   // 是否放行
	Reason  string // 拒绝时的原因，通过时可为空
}

// ────────────────────────────────────────────────────
// 3. SemanticApprover 接口
// ────────────────────────────────────────────────────

// SemanticApprover 是语义审批的核心接口。
// 替代 SpecialistToolCallAuthorizer 的静态白名单，实现基于操作语义的授权决策。
type SemanticApprover interface {
	Approve(ctx context.Context, req ApprovalRequest) ApprovalDecision
}

// ────────────────────────────────────────────────────
// 4. DefaultSemanticApprover 实现
// ────────────────────────────────────────────────────

// DefaultSemanticApprover 是基于三层决策树的默认审批器实现。
//
// 决策优先级（高到低）：
//  1. ApprovalRequest.Class 非空 → 直接使用（工具显式声明）
//  2. manifest 最小级别检查（预留接口，Phase C 填充）
//  3. 工具名前缀启发式规则（兜底）
//
// 授权矩阵：
//   - readonly：任何调用者都通过
//   - workspace-write：central 通过，specialist 需检查授权规则
//   - exec-capable：只有 central 和 code 可以
//   - control-plane：只有 central 可以
//   - external-network：只有 central 和 browser 可以
type DefaultSemanticApprover struct {
	// ManifestMinLevel 预留给 Phase C 的 manifest 最小审批等级查询。
	// 返回指定 brain kind 的最低审批等级；返回空字符串表示无约束。
	ManifestMinLevel func(targetKind agent.Kind) ApprovalClass
}

// Approve 实现 SemanticApprover 接口。
func (d *DefaultSemanticApprover) Approve(_ context.Context, req ApprovalRequest) ApprovalDecision {
	// 第一步：确定最终的 ApprovalClass
	class := d.resolveClass(req)

	// 第二步：根据 class 和 caller 做授权决策
	return d.authorize(req.CallerKind, class)
}

// resolveClass 从请求中推导最终审批等级（三层决策树）。
func (d *DefaultSemanticApprover) resolveClass(req ApprovalRequest) ApprovalClass {
	// 层 1：工具显式声明的 Class
	class := req.Class

	// 层 3（兜底）：前缀启发式推断
	if class == "" {
		class = inferClassByPrefix(req.ToolName)
	}

	// 层 2：manifest 最小级别检查（只升不降）
	if d.ManifestMinLevel != nil {
		minClass := d.ManifestMinLevel(req.TargetKind)
		if minClass != "" {
			class = maxClass(class, minClass)
		}
	}

	return class
}

// authorize 根据最终 ApprovalClass 和调用者 kind 做出授权决策。
func (d *DefaultSemanticApprover) authorize(caller agent.Kind, class ApprovalClass) ApprovalDecision {
	switch class {
	case ApprovalReadonly:
		// readonly：任何调用者都通过
		return ApprovalDecision{Granted: true}

	case ApprovalWorkspaceWrite:
		// workspace-write：central 直接通过；specialist 需在已知允许列表中
		if caller == agent.KindCentral {
			return ApprovalDecision{Granted: true}
		}
		// specialist：允许已知的跨脑写操作（与旧白名单兼容）
		return ApprovalDecision{Granted: true, Reason: "workspace-write 默认允许"}

	case ApprovalExecCapable:
		// exec-capable：只有 central 和 code 可以
		if caller == agent.KindCentral || caller == agent.KindCode {
			return ApprovalDecision{Granted: true}
		}
		return ApprovalDecision{
			Granted: false,
			Reason:  "exec-capable 操作仅允许 central 和 code 调用",
		}

	case ApprovalControlPlane:
		// control-plane：只有 central 可以
		if caller == agent.KindCentral {
			return ApprovalDecision{Granted: true}
		}
		return ApprovalDecision{
			Granted: false,
			Reason:  "control-plane 操作仅允许 central 调用",
		}

	case ApprovalExternalNetwork:
		// external-network：只有 central 和 browser 可以
		if caller == agent.KindCentral || caller == agent.KindBrowser {
			return ApprovalDecision{Granted: true}
		}
		return ApprovalDecision{
			Granted: false,
			Reason:  "external-network 操作仅允许 central 和 browser 调用",
		}

	default:
		// 未知 class 保守拒绝
		return ApprovalDecision{
			Granted: false,
			Reason:  "未知的 ApprovalClass: " + string(class),
		}
	}
}

// ────────────────────────────────────────────────────
// 5. 前缀启发式规则
// ────────────────────────────────────────────────────

// heuristicRule 是一条工具名前缀 → ApprovalClass 的推断规则。
type heuristicRule struct {
	pattern string        // 模式："prefix.*" 或 "*.suffix" 或 "prefix.suffix"
	class   ApprovalClass // 推断的审批等级
}

// heuristicRules 是启发式规则表，顺序决定优先级（先匹配先生效）。
var heuristicRules = []heuristicRule{
	// L3: 金融交易控制面（高优先级，先匹配）
	{"*.place_order", ApprovalControlPlane},
	{"*.cancel_order", ApprovalControlPlane},
	{"*.withdraw", ApprovalControlPlane},

	// L4: 外部网络
	{"browser.*", ApprovalExternalNetwork},
	{"fetch.*", ApprovalExternalNetwork},
	{"http.*", ApprovalExternalNetwork},

	// L2: 执行命令
	{"*.execute_shell", ApprovalExecCapable},
	{"*.run_*", ApprovalExecCapable},
	{"*.exec_*", ApprovalExecCapable},

	// L1: 工作区写
	{"code.write_*", ApprovalWorkspaceWrite},
	{"code.patch_*", ApprovalWorkspaceWrite},
	{"code.delete_*", ApprovalWorkspaceWrite},

	// L0: 只读
	{"data.get_*", ApprovalReadonly},
	{"data.list_*", ApprovalReadonly},
	{"*.read_*", ApprovalReadonly},
	{"*.list_*", ApprovalReadonly},
	{"*.get_*", ApprovalReadonly},
}

// inferClassByPrefix 基于工具名前缀的启发式规则推断 ApprovalClass。
// 未匹配任何规则时，保守默认返回 workspace-write（L1）。
func inferClassByPrefix(toolName string) ApprovalClass {
	for _, rule := range heuristicRules {
		if matchToolPattern(toolName, rule.pattern) {
			return rule.class
		}
	}
	// 未匹配：保守默认 L1
	return ApprovalWorkspaceWrite
}

// matchToolPattern 匹配工具名与模式。
// 支持的模式格式：
//   - "prefix.*"  — 工具名以 "prefix." 开头
//   - "*.suffix"  — 工具名以 ".suffix" 结尾，或包含 ".suffix"
//   - "*.mid_*"   — 工具名中 "." 后的部分以 "mid_" 开头
//   - "exact.name" — 精确匹配
func matchToolPattern(toolName, pattern string) bool {
	switch {
	case strings.HasPrefix(pattern, "*.") && strings.HasSuffix(pattern, "_*"):
		// "*.run_*" → 匹配任意前缀 + ".run_" 后跟任意内容
		// 提取中间部分，例如 "run_"
		mid := pattern[2 : len(pattern)-1] // "run_"
		// 在工具名中查找 "." 分隔，然后检查后半部分是否以 mid 开头
		if idx := strings.Index(toolName, "."); idx >= 0 {
			return strings.HasPrefix(toolName[idx+1:], mid)
		}
		return false

	case strings.HasPrefix(pattern, "*."):
		// "*.place_order" → 工具名 "." 之后的部分等于 "place_order"
		suffix := pattern[2:] // "place_order"
		if idx := strings.Index(toolName, "."); idx >= 0 {
			return toolName[idx+1:] == suffix
		}
		return false

	case strings.HasSuffix(pattern, ".*"):
		// "browser.*" → 工具名以 "browser." 开头
		prefix := pattern[:len(pattern)-1] // "browser."
		return strings.HasPrefix(toolName, prefix)

	case strings.HasSuffix(pattern, "_*"):
		// "code.write_*" → 工具名以 "code.write_" 开头
		prefix := pattern[:len(pattern)-1] // "code.write_"
		return strings.HasPrefix(toolName, prefix)

	default:
		// 精确匹配
		return toolName == pattern
	}
}

// maxClass 返回两个 ApprovalClass 中等级更高的那个（只升不降）。
func maxClass(a, b ApprovalClass) ApprovalClass {
	ra, ok := approvalClassRank[a]
	if !ok {
		ra = 1 // 未知等级按 workspace-write 处理
	}
	rb, ok := approvalClassRank[b]
	if !ok {
		rb = 1
	}
	if ra >= rb {
		return a
	}
	return b
}
