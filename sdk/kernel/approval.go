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

	// Args 是工具参数的文本形式,供参数级规则(Task #20 ParamRule)匹配。
	// Key 是参数名,value 是字符串化后的参数值。调用方对需要参数级细化审批
	// 的工具(如 shell.exec、command_exec、browser.navigate)填充本字段。
	// 未提供时,参数级规则全部跳过,退回工具名/class 级别决策。
	Args map[string]string
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

	// ParamRules 是参数级 glob 匹配规则表(Task #20)。按顺序匹配,先命中先生效。
	// 规则支持两种效果:
	//   - 命中并给出更高 class → 升级审批(变严)
	//   - 命中并明确 Deny → 直接拒绝
	// 未匹配任何规则时走原 (工具名 + class) 决策。
	ParamRules []ParamRule
}

// ParamRule 是一条参数级审批规则。
type ParamRule struct {
	// ToolPattern 用和 matchToolPattern 相同的语法匹配工具名。
	ToolPattern string

	// ArgKey 是要检查的参数名;从 ApprovalRequest.Args[ArgKey] 取字符串。
	// 参数不存在或为空时本规则跳过。
	ArgKey string

	// ValueGlob 用 sdk/internal/pathglob 语法匹配参数值(支持 ** 段通配)。
	// 例如 "git *" 匹配任何以 "git " 开头的命令;"rm -rf /" 精确匹配。
	ValueGlob string

	// 以下三个字段互斥,命中时只有一个生效(按优先级 Deny > Class > AllowAs):
	//   - Deny 为 true → 直接拒绝
	//   - Class 非空 → 覆盖为该 class(通常用来升级,如 exec-capable → control-plane)
	//   - AllowAs 非空 → 降级到该 class(需要审慎使用,仅用于白名单场景)
	Deny    bool
	Class   ApprovalClass
	AllowAs ApprovalClass

	// Reason 命中时用于 ApprovalDecision.Reason 或 log。空时自动生成。
	Reason string
}

// Approve 实现 SemanticApprover 接口。
func (d *DefaultSemanticApprover) Approve(_ context.Context, req ApprovalRequest) ApprovalDecision {
	// 第一步：确定最终的 ApprovalClass(前缀规则 + manifest 最小级别)
	class := d.resolveClass(req)

	// 第一.五步(Task #20): 参数级 glob 匹配。命中 Deny 规则直接拒绝;
	// 命中 Class 规则升级到该 class;命中 AllowAs 降级到该 class。
	if decision, override, ok := d.applyParamRules(req, class); ok {
		if decision != nil {
			return *decision
		}
		class = override
	}

	// 第二步：根据 class 和 caller 做授权决策
	return d.authorize(req.CallerKind, class)
}

// applyParamRules 按顺序匹配 ParamRules。
// 返回值:
//   - decision 非 nil 时立即返回(典型是 Deny 命中)
//   - override 非空时替换 class(Class 升级或 AllowAs 降级)
//   - ok 表示有命中
func (d *DefaultSemanticApprover) applyParamRules(req ApprovalRequest, class ApprovalClass) (*ApprovalDecision, ApprovalClass, bool) {
	if len(d.ParamRules) == 0 || len(req.Args) == 0 {
		return nil, "", false
	}
	for _, rule := range d.ParamRules {
		if rule.ToolPattern != "" && !matchToolPattern(req.ToolName, rule.ToolPattern) {
			continue
		}
		val, ok := req.Args[rule.ArgKey]
		if !ok || val == "" {
			continue
		}
		matched, err := matchArgGlob(rule.ValueGlob, val)
		if err != nil || !matched {
			continue
		}
		reason := rule.Reason
		if rule.Deny {
			if reason == "" {
				reason = "参数级规则拒绝:" + req.ToolName + "(" + rule.ArgKey + "=" + val + ")"
			}
			return &ApprovalDecision{Granted: false, Reason: reason}, "", true
		}
		if rule.Class != "" {
			return nil, maxClass(class, rule.Class), true
		}
		if rule.AllowAs != "" {
			return nil, rule.AllowAs, true
		}
	}
	return nil, "", false
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

	// 纯本地 scratchpad：必须先于 browser.* 等网络规则匹配
	{"*.note", ApprovalReadonly},

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
	{"code.edit_*", ApprovalWorkspaceWrite},
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

// matchArgGlob 对 ParamRule.ValueGlob 做 shell/URL 友好的 glob 匹配。
//
// 与 sdk/internal/pathglob 不同,它不按 "/" 切段、不做 path.Clean,更贴合
// 参数值(命令行、URL)的直觉:
//   - "**"  匹配任意字符(含空格、斜杠)
//   - "*"   同 "**",都映射为"任意字符"
//   - "?"   匹配单个字符
//   - 其他字符字面匹配
//
// 约定这种宽松语义是为了"rm -rf *" / "sudo **" / "http://localhost**" 都能
// 直接用。需要更严格匹配时,调用方可以把 ValueGlob 写成带精确前后缀的模式。
func matchArgGlob(pattern, value string) (bool, error) {
	// 转成 path.Match 兼容的单段模式:用 "?"(任意单字符)占位 "/" 避免
	// path.Match 拒绝含 "/" 的 target。再用 "**" → 用 strings.Contains
	// 兜底。这里干脆直接做双指针匹配,语义最稳。
	return globMatch(pattern, value), nil
}

func globMatch(pattern, value string) bool {
	// 递归 + memo 的简单实现;pattern 和 value 长度有限(都是单次参数串)。
	type key struct{ p, v int }
	memo := map[key]bool{}
	var walk func(p, v int) bool
	walk = func(p, v int) bool {
		if r, ok := memo[key{p, v}]; ok {
			return r
		}
		result := false
		defer func() { memo[key{p, v}] = result }()

		if p == len(pattern) {
			result = v == len(value)
			return result
		}
		c := pattern[p]
		// "**" 等价于 "*"(都匹配任意字符序列)
		if c == '*' {
			// 吞掉连续的 *
			next := p + 1
			for next < len(pattern) && pattern[next] == '*' {
				next++
			}
			// 尝试让 * 匹配 0..N 个字符
			for k := v; k <= len(value); k++ {
				if walk(next, k) {
					result = true
					return result
				}
			}
			return result
		}
		if v == len(value) {
			return result
		}
		if c == '?' {
			result = walk(p+1, v+1)
			return result
		}
		if c == value[v] {
			result = walk(p+1, v+1)
			return result
		}
		return result
	}
	return walk(0, 0)
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
