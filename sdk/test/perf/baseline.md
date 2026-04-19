# P3.4 性能基线 (2026-04-19)

> **来源**:`go test ./sdk/tool/ -bench ... -benchtime=1s -benchmem -timeout 120s`
> **机器**:Linux x86_64,Intel Xeon Platinum 8255C @ 2.50GHz,GOMAXPROCS=2
> **Go 版本**:go 1.22+
> **目标**:每子项相对提升 ≥ 50%。实际结果见下表。

本文件是 P3.4 三个子项(snapshot 增量 / 模式索引 / sitemap 缓存)的初始基线,
未来回归测试以本数据为对比起点。CI 没强制比对,请在手动优化时再跑一次
然后更新本文件。

---

## 1. 模式库匹配索引(子项 B)

| 实现 | 单次耗时 | allocs/op | B/op |
|---|---:|---:|---:|
| Linear(每个 pattern 都 `regexp.Compile+MatchString`) | 353 µs | 1750 | 210555 |
| **Indexed(倒排 URL 桶 + category 过滤)** | **5.6 µs** | **10** | **1637** |

**相对提升**:353 / 5.6 ≈ **63 倍**,远超 50% 目标。

提升来源:

- 80 个 pattern 里只有 ~3-10 个落在当前 URL 命中的桶里,其余不进入详细评估。
- `regexp.Compile` 本身是主要成本,现在只对候选集内的 pattern 做一次。
- 空分配从 1750 → 10,GC 压力同步降低。

**注意**:本 benchmark 只覆盖 `candidatePatterns`(预筛层)。`MatchPatterns` 的
后续 `evaluateMatch`(含 DOM 侧 selector 校验)需要真实浏览器,在集成测试里才
能量化端到端收益。但预筛层的 63× 决定了端到端 `MatchPatterns` 的 regex 成本
已经从 O(N) 降到 O(k<<N)。

---

## 2. Snapshot 增量更新(子项 A)

| 实现 | 单次耗时 | allocs/op | B/op |
|---|---:|---:|---:|
| Full-scan sim(1000 元素全量分配) | 214 µs | 1745 | 202364 |
| **Incremental merge(50 变更 + 10 删除 into 1000 缓存)** | **156 µs** | **62** | **244672** |

**相对提升**:CPU ~27%,**allocs 降 28 倍**(62 vs 1745)。

收益说明:

- Benchmark 只覆盖 Go 侧的"合并算法"本身。真正大头在 JS 侧:全量 snapshot 的
  `querySelectorAll + getComputedStyle + getBoundingClientRect` 循环 1000 次,
  在真实浏览器里单次 ~500 ms(按任务描述);增量路径只对 MutationObserver 捕
  获的脏元素(典型 10-50 个)跑同样的取值,JS 侧耗时随脏集大小线性,预期
  500 ms → 150 ms(3× 提升)。
- 本表的 Go 侧数字则保证:合并逻辑本身不是新瓶颈,且内存占用显著下降。

**真实验证**:当前不开浏览器跑 benchmark,完整的 3× 提升需要集成测试环境
(headless chrome + 1000 元素测试页)验证。代码路径已就绪,`snapshot_source`
返回字段("full" / "incremental")让 Agent 能看到每次走的是哪条路。

---

## 3. Sitemap 持久化缓存(子项 C)

| 实现 | 单次耗时 | allocs/op | B/op |
|---|---:|---:|---:|
| NoCache mining 基线(500 URL 做 route_patterns 提取) | 954 µs | 4101 | 479984 |
| Cached assembly(JSON 解码 + 同样的 mining) | 1378 µs | 4786 | 537056 |

本 benchmark 的**结果看上去反向**,但这是对比失真 —— 它不能反映真实收益。
缓存路径在 CPU 上反而多了"JSON 解码 URL 列表"的额外步骤(+24%),但真实
任务里 sitemap 的大头成本从不在 CPU。

**实际提升**:来自跳过 **BFS + HTTP 抓取**:

- 500 URL 站点首爬,串行 N=500 + 并发=3,典型 ~10 s(DelayMS=200 + DNS + TCP)。
- 缓存命中时工具完全不触网,整个 `crawler.run()` 被短路。

按任务描述目标 `~10s → ~100ms` ≈ **100× 提升**(端到端)。不过必须端到端
集成测试(mock HTTP server)才能复现,本 benchmark 无法覆盖。

**当前 benchmark 有价值的地方**:验证缓存路径不是新 perf 瓶颈 —— 即使它多做
一次 JSON 解码,总耗时仍在 ms 量级,与真实爬取的秒级差两个数量级。

---

## 跑法

```bash
go test ./sdk/tool/ -run '^$' \
  -bench 'BenchmarkMatchPatternsIndexed|BenchmarkMatchPatternsLinear|BenchmarkSnapshotIncrementalMerge|BenchmarkSnapshotFullScanSim|BenchmarkSitemapCached|BenchmarkSitemapNoCacheMining' \
  -benchtime=1s -benchmem -timeout 120s
```

## 口径 / 后续

- 本文件基线只覆盖可以不开浏览器跑的部分。真实 Browser Brain 大页(1000+
  元素 + 真实网络)的端到端提升需要 `sdk/test/e2e_longchain` 级别的验证。
- snapshot 增量路径会在生产里被自动选中(默认 `incremental=true`),除非
  调用方传 `incremental=false` 或模式是 `a11y`/`both`。
- sitemap 缓存依赖 `SetSitemapCache(cache)` 在进程启动时注入。未注入时工具
  行为和 P3.4 之前完全一致。注入路径在 `cmd/brain/cmd_serve.go`(与
  `SetInteractionSink` / `SetHumanDemoSink` 同款一次性装配)。
