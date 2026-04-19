package tool

// anomaly_template_route.go — A 库与 M5 on_anomaly 路由的适配桥。
//
// 背景:P3.1 必做项要求"pattern_exec 命中 anomaly 时先查模板库,有模板按
// 模板执行,没有才走原 OnAnomaly 静态配置"。真正的侵入点在
// ui_pattern_match.go / builtin_browser_pattern.go,按文件冲突规避这归 pattern
// 线(dev-pattern 的 P3.2)。本文件给 pattern 线一个随取随用的中间件:
// 把 AnomalyTemplate 的 Recovery 翻译成现有的 AnomalyHandler,让主路由逻辑
// 不必新增分支 —— 复用 matchAnomalyHandler → switch 的 action 分发。
//
// P3.2 的 dev-pattern 只需要在 runActionSequence 的 matchAnomalyHandler 前
// 加一行:handler, match := ResolveAnomalyHandler(lib, pattern, aType, aSubtype, site, severity)。

import (
	"strings"
	"time"
)

// AnomalyTemplateSource 是 ResolveAnomalyHandler 的最小依赖,方便 pattern_exec
// 侧注入 library 而不直接依赖 kernel 入口。
type AnomalyTemplateSource interface {
	Match(anomalyType, subtype, siteOrigin, severity string) *AnomalyTemplate
}

// ResolveAnomalyHandler 先查模板库;命中则用模板首步 Recovery 合成 AnomalyHandler
// 并返回 (handler, true, templateID);miss 则回退到原 matchAnomalyHandler(p, ...)
// 的结果,templateID=0 表示未走模板路径。
//
// 返回的 handler 指针安全:合成的 handler 是新对象,不共享模板内存;调用方
// 使用后可直接丢弃。
func ResolveAnomalyHandler(
	lib AnomalyTemplateSource, p *UIPattern,
	anomalyType, subtype, siteOrigin, severity string,
) (*AnomalyHandler, bool, int64) {
	if lib != nil {
		if tpl := lib.Match(anomalyType, subtype, siteOrigin, severity); tpl != nil && len(tpl.Recovery) > 0 {
			h := recoveryToHandler(tpl.Recovery[0])
			if h != nil {
				return h, true, tpl.ID
			}
		}
	}
	h, ok := matchAnomalyHandler(p, anomalyType, subtype)
	return h, ok, 0
}

// recoveryToHandler 把一个 AnomalyTemplateRecoveryAction 翻译成
// AnomalyHandler。若 Kind 无法映射(例如 custom_steps 目前不支持直接降维),
// 返回 nil 让调用方回退。
//
// custom_steps 在本轮不生成 AnomalyHandler —— AnomalyHandler 没有通用的
// "一串子步骤"槽位,硬塞会破坏现有行为。dev-pattern 集成 P3.2 时会单独加
// 一条 case 分发 custom_steps(对应"子 ActionSequence 插队执行"),所以这里
// 故意保持 nil 让调用方看得见"没命中可翻译的模板"。
func recoveryToHandler(a AnomalyTemplateRecoveryAction) *AnomalyHandler {
	switch strings.ToLower(a.Kind) {
	case "retry":
		max := a.MaxRetries
		if max <= 0 {
			max = 1
		}
		return &AnomalyHandler{
			Action: "retry", MaxRetries: max, BackoffMS: a.BackoffMS,
			Reason: a.Reason,
		}
	case "fallback_pattern":
		if a.FallbackID == "" {
			return nil
		}
		return &AnomalyHandler{
			Action: "fallback_pattern", FallbackID: a.FallbackID, Reason: a.Reason,
		}
	case "human_intervention":
		return &AnomalyHandler{Action: "human_intervention", Reason: a.Reason}
	}
	// custom_steps 或未知 kind 返回 nil —— 让调用方回退
	return nil
}

// TemplateHitMarker 是给 recorder/LearningEngine 的分界事件。调用方拿到
// templateID > 0 就代表本步是模板命中。用一个 struct 而不是裸 int 是为了
// 将来可以加 "elapsed" 等字段不破坏兼容。
type TemplateHitMarker struct {
	TemplateID int64
	HitAt      time.Time
}

// MarkTemplateHit 构造 TemplateHitMarker,把当前时间补上。零 id 返回 nil。
func MarkTemplateHit(id int64) *TemplateHitMarker {
	if id <= 0 {
		return nil
	}
	return &TemplateHitMarker{TemplateID: id, HitAt: time.Now()}
}
