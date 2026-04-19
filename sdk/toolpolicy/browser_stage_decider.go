package toolpolicy

// P3.5 —— BrowserStage 自动切换决策器。
//
// 历史上 EvalRequest.BrowserStage 由 LLM 自己在 prompt 里声明,对每一轮
// 都要让 LLM 做一次"我现在应该切到哪个 stage"的判断,占 token 又不稳定。
// 本决策器把规则固化成纯函数,调用方(sidecar loop / cliruntime)在每 turn
// 开始前塞进 EvalRequest 再交给 AdaptiveToolPolicy.Evaluate。
//
// 规则(对齐 sdk/docs/44 §4.5):
//   1. 即将执行的动作若触及 destructive 审批级别 → BrowserStageDestructive,
//      不管匹配度和历史 turn 如何(硬约束优先,保护用户)。
//   2. 最近 3 turn 里 ≥ 2 次 "error" → BrowserStageFallback(卡住了,
//      开放 visual_inspect + eval 让 LLM 自救)。
//   3. pattern_match top score ≥ HighScoreThreshold(默认 0.8)
//      → BrowserStageKnownFlow。
//   4. pattern_match top score < LowScoreThreshold(默认 0.3)
//      或根本没 pattern_match 数据 → BrowserStageNewPage(退回探索)。
//   5. 其他情况 → 保持 Fallback 不激进地切,返回空字符串让调用方沿用
//      上一轮 stage(避免抖动)。
//
// 设计原则:
//   - 纯函数,易测;所有输入通过 DecisionInput 显式打包。
//   - 阈值用 DecisionThresholds 外部化,允许集成方调优而不改代码。
//   - ApprovalClass 这里用 string 形式传入,不 import sdk/kernel,避免
//     循环依赖和把 kernel 的政策耦合进 toolpolicy 层。

// DecisionInput 是 DecideBrowserStage 的完整输入。调用方自己从
// recorder / AdaptivePolicy / 当前 action 里填好,不要在决策器内偷偷
// 读 recorder——那会让测试无法独立验证规则。
type DecisionInput struct {
	// RecentPatternScores 最近若干次 pattern_match 的 top score。
	// 空切片 → 视为"还没 pattern_match 过",规则 4 触发 new_page。
	RecentPatternScores []float64

	// RecentTurnOutcomes 最近若干次 turn 的 outcome("ok"/"error" 或自定义)。
	// 决策器只识别 "error"(任何非 ok 的失败标记建议统一成 "error")。
	RecentTurnOutcomes []string

	// PendingApprovalClass 即将执行动作的 ApprovalClass 字符串(如
	// "readonly" / "workspace-write" / "exec-capable" / "control-plane" /
	// "external-network")。未知或空则当作 readonly 处理,不会触发规则 1。
	PendingApprovalClass string
}

// DecisionThresholds 可调阈值。零值即使用默认。
type DecisionThresholds struct {
	// HighScoreThreshold 超过此值认为"高置信模式匹配"。
	// 默认 0.8,来自文档 44 §4.5。
	HighScoreThreshold float64
	// LowScoreThreshold 低于此值认为"无模式可用"。默认 0.3。
	LowScoreThreshold float64
	// NoProgressErrorCount 最近窗口里达到此错误数视为"卡住"。默认 2。
	NoProgressErrorCount int
}

// DefaultDecisionThresholds 是 DecideBrowserStage 的默认阈值。
func DefaultDecisionThresholds() DecisionThresholds {
	return DecisionThresholds{
		HighScoreThreshold:   0.8,
		LowScoreThreshold:    0.3,
		NoProgressErrorCount: 2,
	}
}

// destructiveApprovalClasses 是被视为 "destructive" 的 ApprovalClass 集合。
// 对齐 sdk/kernel/approval.go 的五级(readonly / workspace-write /
// exec-capable / control-plane / external-network),其中 exec-capable 及
// 以上(control-plane / external-network)归 destructive。
//
// 这里用字面值而不 import 常量,是为了让 toolpolicy 包不反向依赖 kernel;
// 契约由 sdk/docs/26-审批.md 固化,枚举值极少变动。
var destructiveApprovalClasses = map[string]struct{}{
	"exec-capable":     {},
	"control-plane":    {},
	"external-network": {},
}

// IsDestructiveApproval 对外暴露给调用方判断 ApprovalClass 是否触发
// destructive stage,也用于单测断言。
func IsDestructiveApproval(class string) bool {
	_, ok := destructiveApprovalClasses[class]
	return ok
}

// DecideBrowserStage 按规则返回本 turn 应该使用的 BrowserStage。返回空
// 字符串代表"决策器没强意见,沿用上一轮"——调用方应自行保留上次值。
//
// thresh 传零值代表默认阈值(见 DefaultDecisionThresholds)。
func DecideBrowserStage(in DecisionInput, thresh DecisionThresholds) string {
	t := thresh
	if t.HighScoreThreshold <= 0 {
		t.HighScoreThreshold = 0.8
	}
	if t.LowScoreThreshold <= 0 {
		t.LowScoreThreshold = 0.3
	}
	if t.NoProgressErrorCount <= 0 {
		t.NoProgressErrorCount = 2
	}

	// 规则 1:destructive 动作硬约束优先。
	if IsDestructiveApproval(in.PendingApprovalClass) {
		return BrowserStageDestructive
	}

	// 规则 2:最近窗口里 error 次数达阈值 → fallback。
	errs := 0
	for _, o := range in.RecentTurnOutcomes {
		if o == "error" {
			errs++
		}
	}
	if errs >= t.NoProgressErrorCount {
		return BrowserStageFallback
	}

	// 规则 3 / 4:看最近 pattern_match 的最高 score。
	// 取最近若干次里的 **max**——一次高分命中足以进 known_flow,不必等
	// 平均;一个低分记录也不会把高分历史淹没掉。
	hasScore := false
	maxScore := 0.0
	for _, s := range in.RecentPatternScores {
		hasScore = true
		if s > maxScore {
			maxScore = s
		}
	}
	if !hasScore {
		return BrowserStageNewPage
	}
	if maxScore >= t.HighScoreThreshold {
		return BrowserStageKnownFlow
	}
	if maxScore < t.LowScoreThreshold {
		return BrowserStageNewPage
	}

	// 介于高低阈值之间:不切,让调用方沿用上轮值,减少抖动。
	return ""
}
