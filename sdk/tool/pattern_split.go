package tool

// pattern_split.go — P3.2 模式自分裂。
//
// 当某个 learned 模式在某站点反复命中同一种 anomaly 失败 →
// 把父模式复制一份,AppliesWhen 加"仅该 site"约束,OnAnomaly 插一条专治
// 该 anomaly 的 handler,source 保持 "learned",Pending=true 进入试用期。
// 试用期变种被 browser.pattern_match 和 pattern_exec 正常看见,统计独立;
// SuccessCount >= 3 时 RecordExecution 自动清 Pending 毕业成正式模式;
// FailureCount >= 5 且 SuccessRate < 0.3 时 M3 已有逻辑会把它 Disable。
//
// 触发时机:这里不起 goroutine 也不订阅 EventBus——设计上由调用方
// (browser brain 每 N 次 pattern_exec / ops 管理端点 / HookRunner on
// task.state.completed)调 MaybeScanForSplit。这样决策器纯被动、可测。
//
// 保护铁律(对齐任务描述 §3):
//   - 只分裂 source="learned" 的模式(seed 手工设计的不动,避免变种污染)。
//   - 不分裂已 Disabled 的模式(避免从一个坏模式再派生坏变种)。
//   - 不为已存在的变种再分裂(幂等,ID 命名里已含 site+anomaly)。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/leef-l/brain/sdk/persistence"
)

// PatternFailureStore 是 pattern_split 读写失败样本的最小依赖接口。
// 生产实现由 kernel.LearningEngine 提供(走 sqliteLearningStore)。
// 测试可注入内存 mock,不必打开 SQLite。nil 实现表示"未配置样本存储",
// 整条自分裂链路静默退化为 no-op。
type PatternFailureStore interface {
	SavePatternFailureSample(ctx context.Context, sample *persistence.PatternFailureSample) error
	ListPatternFailureSamples(ctx context.Context, patternID string) ([]*persistence.PatternFailureSample, error)
}

var (
	failureStoreMu sync.RWMutex
	failureStore   PatternFailureStore
)

// SetPatternFailureStore 在进程启动时被 kernel/cmd 注入。传 nil 即关闭。
// 和 SetInteractionSink / SetSitemapCache 同款风格。
func SetPatternFailureStore(s PatternFailureStore) {
	failureStoreMu.Lock()
	defer failureStoreMu.Unlock()
	failureStore = s
}

func currentFailureStore() PatternFailureStore {
	failureStoreMu.RLock()
	defer failureStoreMu.RUnlock()
	return failureStore
}

// splitMinSampleCount 是触发一次分裂候选所需的最小同组合样本数。
// 任务描述 §3 "同 pattern_id + 同 site_origin + 同 anomaly_subtype 累计 ≥ 5 次"。
const splitMinSampleCount = 5

// RecordPatternFailure 在 pattern_exec 失败时被调。把失败上下文(站点 +
// 异常子类型 + 失败 step + 页面指纹)写进 pattern_failure_samples 表,
// 给 ScanForSplit 提供聚类原料。
//
// 所有参数都可以为空——空值也如实记录,只是 anomalySubtype="" 的样本不会
// 参与 ScanForSplit 的分裂决策(分裂必须有明确 anomaly 驱动)。
//
// 这个函数故意不做任何阈值判断:调用方只需要无脑记录,分裂决策留给
// 异步 / 定期调 ScanForSplit 的逻辑。
func RecordPatternFailure(ctx context.Context, patternID, siteOrigin, anomalySubtype string, failureStep int, url string) error {
	if patternID == "" {
		return nil
	}
	store := currentFailureStore()
	if store == nil {
		return nil
	}
	fp := buildFailureFingerprint(url, failureStep)
	return store.SavePatternFailureSample(ctx, &persistence.PatternFailureSample{
		PatternID:       patternID,
		SiteOrigin:      siteOrigin,
		AnomalySubtype:  anomalySubtype,
		FailureStep:     failureStep,
		PageFingerprint: fp,
	})
}

// buildFailureFingerprint 生成一份最小 page fingerprint。这里只塞 url + step,
// 不做 DOM hash(DOM 采集成本高,且当前任务不需要跨站去重——same site 已经
// 在聚类 key 里)。未来需要更细粒度的变种判定时,这是扩展点:加 dom_hash、
// main_region_selectors 等。
func buildFailureFingerprint(pageURL string, step int) json.RawMessage {
	obj := map[string]interface{}{
		"url":  pageURL,
		"step": step,
	}
	if buf, err := json.Marshal(obj); err == nil {
		return buf
	}
	return json.RawMessage(`{}`)
}

// ScanForSplit 扫 store 里的失败样本,按 (pattern_id, site_origin, anomaly_subtype)
// 聚类;任何一组样本数 >= splitMinSampleCount 且父模式满足保护铁律的,
// 生成一份变种入库。返回新生成的变种 ID 列表。lib / store 任一 nil 直接返回空。
//
// 目的是让 ops / 定时任务有一个"不带副作用"的入口可调,返回值便于观测。
func ScanForSplit(ctx context.Context, lib *PatternLibrary, store PatternFailureStore) ([]string, error) {
	if lib == nil {
		return nil, nil
	}
	if store == nil {
		store = currentFailureStore()
	}
	if store == nil {
		return nil, nil
	}

	// 以 patternID 为外层 key,site_origin + anomaly_subtype 为内层 key 聚类。
	// 只看当前 library 里存在、source=learned、Enabled=true 的模式。
	candidates := map[string]*UIPattern{}
	for _, p := range lib.ListAll("") {
		if p.Source != "learned" {
			continue
		}
		if !p.Enabled {
			continue
		}
		candidates[p.ID] = p
	}

	var spawned []string
	for pid, parent := range candidates {
		samples, err := store.ListPatternFailureSamples(ctx, pid)
		if err != nil {
			// 单个 pattern 读失败不影响其它——继续跑,错误靠 ops 日志覆盖。
			continue
		}
		if len(samples) < splitMinSampleCount {
			continue
		}
		clusters := clusterFailureSamples(samples)
		for key, group := range clusters {
			if len(group) < splitMinSampleCount {
				continue
			}
			if key.site == "" || key.subtype == "" {
				// 无法定位的聚类不生成变种(会和父模式 AppliesWhen 一样,无意义)。
				continue
			}
			variantID := variantPatternID(parent.ID, key.site, key.subtype)
			if lib.GetAny(variantID) != nil {
				// 已存在同 ID 变种 → 本轮跳过(幂等)。
				continue
			}
			variant := SpawnVariant(parent, key.site, key.subtype)
			if variant == nil {
				continue
			}
			if err := lib.Upsert(ctx, variant); err != nil {
				continue
			}
			spawned = append(spawned, variant.ID)
		}
	}
	return spawned, nil
}

// failureClusterKey 是聚类时的内层 key,不 export 给外部——调用方只关心结果。
type failureClusterKey struct {
	site    string
	subtype string
}

// clusterFailureSamples 按 (site_origin, anomaly_subtype) 分组。空值样本
// 按原样入组,让上层决定怎么处理。
func clusterFailureSamples(samples []*persistence.PatternFailureSample) map[failureClusterKey][]*persistence.PatternFailureSample {
	out := map[failureClusterKey][]*persistence.PatternFailureSample{}
	for _, s := range samples {
		k := failureClusterKey{site: s.SiteOrigin, subtype: s.AnomalySubtype}
		out[k] = append(out[k], s)
	}
	return out
}

// variantPatternID 按任务描述 §3 的命名约定拼 ID:
//   <parent_id>__<site>__<anomaly_subtype>
// site 里的 . / : 等特殊字符会被 normalizeForID 转成下划线,避免和
// IO 层的文件名 / 查询参数冲突。示例:
//   login_username_password__demo_gitea_com__captcha
func variantPatternID(parentID, siteOrigin, subtype string) string {
	return fmt.Sprintf("%s__%s__%s",
		parentID,
		normalizeForID(siteOrigin),
		normalizeForID(subtype))
}

// normalizeForID 把任意字符串打成 [a-z0-9_-] 集合。保留长度不截断,
// 让 parent + site + subtype 的追溯性完整。
func normalizeForID(s string) string {
	s = strings.ToLower(s)
	// 去掉 scheme 前缀,让 https://demo.gitea.com → demo.gitea.com → demo_gitea_com。
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// SpawnVariant 从父模式 + site + anomaly subtype 生成一个变种。
// 变种 AppliesWhen 基础上叠加 URL 必须命中该 site、text_contains 暗示 anomaly;
// OnAnomaly 插一条针对该 subtype 的 retry 策略(MaxRetries=1,BackoffMS=500),
// 如果父模式已对该 subtype 定义过 handler 则保留父的(允许专门化优先级高
// 的 action,例如 captcha 的 human_intervention 不被降级成 retry)。
//
// 不修改父模式,返回一个新结构体,调用方负责 Upsert。空 parent / site / subtype
// 返回 nil。
func SpawnVariant(parent *UIPattern, siteOrigin, anomalySubtype string) *UIPattern {
	if parent == nil || siteOrigin == "" || anomalySubtype == "" {
		return nil
	}

	// 深拷贝 AppliesWhen + ElementRoles + ActionSequence + OnAnomaly 字段,
	// 避免父子共享 slice/map。
	variant := &UIPattern{
		ID:             variantPatternID(parent.ID, siteOrigin, anomalySubtype),
		Category:       parent.Category,
		Description:    fmt.Sprintf("%s (variant for site=%s anomaly=%s)", parent.Description, siteOrigin, anomalySubtype),
		AppliesWhen:    specializeAppliesWhen(parent.AppliesWhen, siteOrigin, anomalySubtype),
		ElementRoles:   cloneElementRoles(parent.ElementRoles),
		ActionSequence: cloneActionSteps(parent.ActionSequence),
		PostConditions: clonePostConditions(parent.PostConditions),
		OnAnomaly:      cloneOnAnomaly(parent.OnAnomaly),
		// 变种仍是"学到的",不是种子;这样 ScanForSplit 以后还能继续看到它。
		Source:  "learned",
		Enabled: true,
		Pending: true,
	}
	// 父模式对同一 subtype 有 handler → 保留不动;否则插一个默认 retry handler。
	if _, already := variant.OnAnomaly[anomalySubtype]; !already {
		if variant.OnAnomaly == nil {
			variant.OnAnomaly = map[string]AnomalyHandler{}
		}
		variant.OnAnomaly[anomalySubtype] = AnomalyHandler{
			Action:     "retry",
			MaxRetries: 1,
			BackoffMS:  500,
			Reason:     fmt.Sprintf("auto-split: recurring %s on %s", anomalySubtype, siteOrigin),
		}
	}
	return variant
}

// specializeAppliesWhen 在父 MatchCondition 基础上追加 site 和 anomaly 的判别条件。
//   - URLPattern:若父已有 pattern,合并成"父 AND host 要命中 site 的 host";
//                若父无 pattern,直接写一个只匹配 host 的正则。
//   - TextContains:追加 anomaly subtype 字符串(松散线索,命中率不高也不要紧,
//                   真正的筛选靠 URLPattern)。
//
// 合并成 AND 条件的做法是加一个额外字段会更优雅,但会影响 pattern_match 主路径;
// 这里用"在现有 URLPattern 后面拼 site"这一最小改动路径。
func specializeAppliesWhen(parent MatchCondition, siteOrigin, anomalySubtype string) MatchCondition {
	out := MatchCondition{
		URLPattern:    parent.URLPattern,
		Has:           append([]string(nil), parent.Has...),
		HasNot:        append([]string(nil), parent.HasNot...),
		TitleContains: append([]string(nil), parent.TitleContains...),
		TextContains:  append([]string(nil), parent.TextContains...),
	}

	host := extractHost(siteOrigin)
	if host != "" {
		// 正则表达里要转义 host 的点,避免 a.b 误命中 axb。
		hostRE := regexp.QuoteMeta(host)
		if out.URLPattern == "" {
			out.URLPattern = `(?i)https?://` + hostRE
		} else {
			// 用 lookahead 合并:要求同时满足父 pattern 和 host。lookahead 在
			// Go regexp 里没支持(RE2),所以退化成"(?i)host.*父pattern | 父pattern.*host"
			// 的松散组合——match_patterns 只用 regexp.MatchString,不需要严格 AND。
			out.URLPattern = `(?i)(?:` + hostRE + `).*|.*(?:` + out.URLPattern + `)`
		}
	}
	// 不无脑叠 anomalySubtype 到 TextContains ——纯 subtype 字符串(如 "captcha")
	// 几乎不会出现在正文;做这一步只会引入假阴性匹配。保留为未来扩展。
	_ = anomalySubtype
	return out
}

// extractHost 从 site_origin 提主机名。接受 "https://a.b"、"a.b"、"a.b/path" 等
// 输入,解析失败回退成原字符串去掉 scheme 前缀。
func extractHost(origin string) string {
	if origin == "" {
		return ""
	}
	if u, err := url.Parse(origin); err == nil && u.Host != "" {
		return u.Host
	}
	if i := strings.Index(origin, "://"); i >= 0 {
		origin = origin[i+3:]
	}
	if i := strings.IndexAny(origin, "/?#"); i >= 0 {
		origin = origin[:i]
	}
	return origin
}

// cloneElementRoles 深拷贝 ElementDescriptor map,避免父子共享引用。
// Fallback slice 也复制,保证 ResolveElement 在变种上修改 Fallback 不影响父。
func cloneElementRoles(src map[string]ElementDescriptor) map[string]ElementDescriptor {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]ElementDescriptor, len(src))
	for k, v := range src {
		cp := v
		if len(v.Fallback) > 0 {
			cp.Fallback = append([]ElementDescriptor(nil), v.Fallback...)
		}
		out[k] = cp
	}
	return out
}

func cloneActionSteps(src []ActionStep) []ActionStep {
	if len(src) == 0 {
		return nil
	}
	out := make([]ActionStep, len(src))
	for i, s := range src {
		cp := s
		if len(s.Params) > 0 {
			cp.Params = make(map[string]interface{}, len(s.Params))
			for k, v := range s.Params {
				cp.Params[k] = v
			}
		}
		out[i] = cp
	}
	return out
}

func clonePostConditions(src []PostCondition) []PostCondition {
	if len(src) == 0 {
		return nil
	}
	out := make([]PostCondition, len(src))
	for i, p := range src {
		cp := p
		if len(p.Any) > 0 {
			cp.Any = append([]PostCondition(nil), p.Any...)
		}
		out[i] = cp
	}
	return out
}

func cloneOnAnomaly(src map[string]AnomalyHandler) map[string]AnomalyHandler {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]AnomalyHandler, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
