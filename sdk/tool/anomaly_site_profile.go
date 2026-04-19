package tool

// anomaly_site_profile.go — P3.1-B 跨站异常模式识别。
//
// 把 anomalyHistory 从"全局一个 buckets map"升级为"按 site_origin 分桶",
// 聚合成 SiteAnomalyProfile 喂 LLM 做跨站诊断,也作为 persistence
// (site_anomaly_profile 表,#16 已加)的写入源。
//
// 与 anomaly_template.go 的关系:
//   - SiteAnomalyProfile 是"观察到的":哪些站哪些异常多发
//   - AnomalyTemplate 是"该怎么做":命中后的 recovery 序列
//   - 两者通过 Signature(type + subtype)对齐,Match 时 Library 可以参考
//     Profile 来做排序(例如高频站点的模板优先级升级)——本期先只做
//     独立功能,对齐是下一次迭代
//
// 不改 anomalyHistory 的既有接口,只扩:
//   - byHost map[host]*hostBucket:细分事件
//   - recordForSite(host, type, subtype, severity, durationMs, recovered)
//   - snapshotProfiles():收走所有 host 的聚合结果

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// HostAnomalyEntry 是 host 桶里单个 (type,subtype) 的聚合条目。
// 对齐 persistence.SiteAnomalyProfile 字段 —— kernel/learning.go 一次
// snapshot 就能直接 Upsert 入库。
type HostAnomalyEntry struct {
	SiteOrigin          string
	AnomalyType         string
	AnomalySubtype      string
	Frequency           int
	TotalDurationMs     int64 // 累积,AvgDurationMs = Total/Frequency
	RecoverAttempts     int
	RecoverSuccesses    int
	LastSeenAt          time.Time
}

// AvgDurationMs 避免除零。
func (e *HostAnomalyEntry) AvgDurationMs() int64 {
	if e.Frequency == 0 {
		return 0
	}
	return e.TotalDurationMs / int64(e.Frequency)
}

// RecoverSuccessRate 统计 RecordSiteAnomaly 中 recovered=true 的比例;
// 样本 <3 返回 -1(冷启动,调用方不可依赖)。
func (e *HostAnomalyEntry) RecoverSuccessRate() float64 {
	if e.RecoverAttempts < 3 {
		return -1
	}
	return float64(e.RecoverSuccesses) / float64(e.RecoverAttempts)
}

// hostBucket 是单个 site_origin 下按 (type,subtype) 索引的 HostAnomalyEntry 集合。
type hostBucket struct {
	entries map[string]*HostAnomalyEntry // key: type|subtype
}

// entryKey 合并 type+subtype,subtype 空时用纯 type。
func entryKey(typ, sub string) string {
	if sub == "" {
		return typ
	}
	return typ + "|" + sub
}

// siteHistory 把 anomalyHistory 的"分 host"视图抽成独立结构,不依赖
// builtin_browser_anomaly_v2.go 内部的 buckets/jsErrors 字段。
//
// 注意:本类型是线程安全的,可以同时被浏览器工具(写入)和 LLM 观察工具
// (读取)访问。
type siteHistory struct {
	mu      sync.RWMutex
	byHost  map[string]*hostBucket
}

// newSiteHistory 创建空实例。anomalyHistory 里会在 newAnomalyHistory 内部
// 构造一个嵌入实例。
func newSiteHistory() *siteHistory {
	return &siteHistory{byHost: map[string]*hostBucket{}}
}

// recordSiteAnomaly 记录一次异常发生,site 可以是 URL 或 origin;会被规范化。
// durationMs 填 0 表示还没测到恢复时长。recovered 填 nil 表示"还没尝试恢复"。
func (h *siteHistory) recordSiteAnomaly(site, anomalyType, subtype string, durationMs int64, recovered *bool) {
	if site == "" || anomalyType == "" {
		return
	}
	origin := normalizeOrigin(site)
	key := entryKey(anomalyType, subtype)

	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.byHost[origin]
	if b == nil {
		b = &hostBucket{entries: map[string]*HostAnomalyEntry{}}
		h.byHost[origin] = b
	}
	e := b.entries[key]
	if e == nil {
		e = &HostAnomalyEntry{
			SiteOrigin: origin, AnomalyType: anomalyType, AnomalySubtype: subtype,
		}
		b.entries[key] = e
	}
	e.Frequency++
	if durationMs > 0 {
		e.TotalDurationMs += durationMs
	}
	if recovered != nil {
		e.RecoverAttempts++
		if *recovered {
			e.RecoverSuccesses++
		}
	}
	e.LastSeenAt = time.Now()
}

// snapshotProfiles 拷出当前所有 host 的聚合条目,供 kernel 批量 Upsert。
// 返回副本,调用方可以安全并发使用。
func (h *siteHistory) snapshotProfiles() []HostAnomalyEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []HostAnomalyEntry
	for _, b := range h.byHost {
		for _, e := range b.entries {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SiteOrigin != out[j].SiteOrigin {
			return out[i].SiteOrigin < out[j].SiteOrigin
		}
		return entryKey(out[i].AnomalyType, out[i].AnomalySubtype) <
			entryKey(out[j].AnomalyType, out[j].AnomalySubtype)
	})
	return out
}

// listForSite 返回某个 host 的聚合条目(只读副本)。site 不存在返回空切片。
// 查询时先做规范化精确匹配;miss 再按 host 后缀 contains 做一次松匹配,
// 以便"x.example" 能查到以 "x.example" 结尾的 origin(调用方可能只知道 host 不知道 scheme)。
func (h *siteHistory) listForSite(site string) []HostAnomalyEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	norm := normalizeOrigin(site)
	b := h.byHost[norm]
	if b == nil && norm != "" {
		// 松匹配:找第一个以 norm 结尾的 origin(不带 scheme 的输入典型场景)。
		for origin, bucket := range h.byHost {
			if strings.HasSuffix(origin, norm) {
				b = bucket
				break
			}
		}
	}
	if b == nil {
		return nil
	}
	out := make([]HostAnomalyEntry, 0, len(b.entries))
	for _, e := range b.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Frequency != out[j].Frequency {
			return out[i].Frequency > out[j].Frequency
		}
		return entryKey(out[i].AnomalyType, out[i].AnomalySubtype) <
			entryKey(out[j].AnomalyType, out[j].AnomalySubtype)
	})
	return out
}

// topFailingHosts 返回按 Frequency 总和降序的前 N 个 host,给 LLM 做
// 跨站对比时用(哪几个站对 Brain 特别难)。topN<=0 返回空。
func (h *siteHistory) topFailingHosts(topN int) []string {
	if topN <= 0 {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	type hostAgg struct {
		host string
		freq int
	}
	aggs := make([]hostAgg, 0, len(h.byHost))
	for host, b := range h.byHost {
		sum := 0
		for _, e := range b.entries {
			sum += e.Frequency
		}
		aggs = append(aggs, hostAgg{host: host, freq: sum})
	}
	sort.Slice(aggs, func(i, j int) bool {
		if aggs[i].freq != aggs[j].freq {
			return aggs[i].freq > aggs[j].freq
		}
		return aggs[i].host < aggs[j].host
	})
	if topN > len(aggs) {
		topN = len(aggs)
	}
	out := make([]string, 0, topN)
	for _, a := range aggs[:topN] {
		out = append(out, a.host)
	}
	return out
}

// ---------------------------------------------------------------------------
// normalizeOrigin 把 URL / origin 字符串统一为 "scheme://host[:port]"。
// 粗糙版(不引 net/url 以减少开销 + 兜底 URL 不合法的情形)。
// 接受:
//   https://shop.example.com/cart   → https://shop.example.com
//   https://a.example:8443/         → https://a.example:8443
//   shop.example.com                → shop.example.com(已经是 origin)
// ---------------------------------------------------------------------------

func normalizeOrigin(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// 找 scheme
	schemeEnd := strings.Index(s, "://")
	if schemeEnd < 0 {
		// 没 scheme,当成 host,去掉可能的 path
		if slash := strings.Index(s, "/"); slash >= 0 {
			return strings.ToLower(s[:slash])
		}
		return strings.ToLower(s)
	}
	// 有 scheme:定位 host 结束位置
	rest := s[schemeEnd+3:]
	slash := strings.Index(rest, "/")
	host := rest
	if slash >= 0 {
		host = rest[:slash]
	}
	return strings.ToLower(s[:schemeEnd+3] + host)
}
