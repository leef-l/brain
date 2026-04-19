# 39. Browser Brain 感知与嗅探增强设计(基础设施层)

> ⚠️ **重要说明(2026-04-20 修订)**
>
> 本文档**仅定义基础设施层**——工具级能力的 snapshot / network / sitemap / iframe 等。
>
> **语义理解层**(让 Agent 达到"人类那种理解按钮意思"的能力)**不在本文档范围内**,见:
> - [`40-Browser-Brain语义理解架构`](./40-Browser-Brain语义理解架构.md) — 核心架构
> - [`41-语义理解阶段0实验设计`](./41-语义理解阶段0实验设计.md) — Go/No-Go 实验方案
>
> 架构关系:
>
> ```
>   ┌─────────────────────────────────────┐
>   │   语义理解层 (40 号)                │  ← L4-L8 真正的"理解"
>   │   understand / patterns / anomaly   │
>   └─────────────────────────────────────┘
>                    ▲ 数据流
>   ┌─────────────────────────────────────┐
>   │   基础设施层 (本文档 39 号)         │  ← L1-L3 结构感知
>   │   snapshot / network / sitemap      │
>   └─────────────────────────────────────┘
>                    ▲ CDP
>   ┌─────────────────────────────────────┐
>   │   CDP 客户端 (sdk/tool/cdp/)        │
>   └─────────────────────────────────────┘
> ```
>
> **承认的局限**:本文档原版在 §9 声称"成功率 40%→80%+"**过于乐观**。实际上 snapshot/network/sitemap 做完后,简单任务(单页填表)能到 80%,复杂任务(跨页流程)只能到 50-60%。真正的跃升需要靠 40 号文档的语义理解层。
>
> **sitemap 定位调整**:原在 P0,现**降为 P1**。99% 的日常 Agent 任务不需要整站嗅探,它只是"给语义层喂数据的管道",不是主路径。

---

## 1. 现状诊断

### 1.1 一句话总结

当前 Browser Brain 的感知层停留在 **2022 年水平**：LLM 看网页只有两种方式——写 `document.querySelector` 字符串(脆弱)或看截图猜坐标(token 贵、歧义大)。

### 1.2 已注册工具(15 个)

`browser.open / navigate / click / double_click / right_click / type / press_key / scroll / hover / drag / select / upload_file / screenshot / eval / wait` + `browser.note`(scratchpad)

### 1.3 关键缺口(已审计,逐项对标 Playwright/Claude CUA/Browser-Use)

| 项 | 现状 | 提升幅度 |
|---|---|---|
| **A. Accessibility Tree** | 完全缺失 | **关键** |
| **B. 带 ID 的 DOM Snapshot** | 完全缺失 | **关键** |
| **C. fill_form 表单批量填充** | 完全缺失 | 中 |
| **D. Network/Fetch 感知** | `Network.enable` 了但无订阅,Fetch 无 | **高** |
| **E. 真 networkIdle / waitForResponse / MutationObserver** | idle 假实现,其余缺失 | **高** |
| **F. storage_state 持久化** | 完全缺失 | 中 |
| **G. 多 BrowserContext** | 单 session 勉强切 tab,无 context 隔离 | 中 |
| **H. anti-bot / stealth** | 基础启动参数,无主动伪装 | 中(生产必备) |
| **I. 下载支持** | 完全缺失(上传 OK) | 中 |
| **J. PDF 导出** | 缺失 | 低-中 |
| **K. iframe 穿透** | 完全缺失 | **高** |
| **L. 整站嗅探 + 路由规则提取** | **全新需求,完全缺失** | **关键** |

---

## 2. 核心设计原则

1. **感知为先**:LLM 优先靠"语义结构"(Accessibility + DOM Snapshot),截图作为补充而非主路径。
2. **元素 ID 稳定化**:引入 `data-brain-id`,LLM 不再写 CSS selector。
3. **网络可见**:把请求/响应当一等公民,LLM 能直接读 API 结果而不是猜页面提示。
4. **向后兼容**:不动现有 15 个工具的 schema,只新增工具 + 给 click/type/hover 增加可选 `id: number` 参数。

---

## 3. P0 能力清单(必做,决定 LLM 感知下限)

### 3.1 `browser.snapshot` —— 语义 + 交互元素融合快照【A + B】

**定位**:Browser Brain 最重要的新工具。代替 95% 的 `browser.eval + querySelectorAll` 场景。

**输入**:
```json
{
  "mode": "a11y" | "interactive" | "both"  // 默认 both
  "max_elements": 200,
  "viewport_only": false,
  "frame": "<url pattern>"  // 可选,指定 iframe
}
```

**实现**:
1. **Accessibility 层**:`Accessibility.enable` → `Accessibility.getFullAXTree {depth: -1}`,扁平化为:
   ```
   [A1]  button        "Submit"
   [A2]  textbox       "Email" value="x@y" focused
   [A3]  link          "Forgot password" href=/reset
   ```
2. **交互元素层**:JS 注入(通过 `Page.addScriptToEvaluateOnNewDocument` 全局持久化):
   ```js
   (function(){
     const SEL = 'a,button,input,select,textarea,[role=button],[role=link],[role=tab],[role=menuitem],[onclick],[tabindex]:not([tabindex="-1"])';
     function isVisible(el){
       const r = el.getBoundingClientRect();
       const s = getComputedStyle(el);
       return r.width>0 && r.height>0 && s.visibility!=='hidden' && s.display!=='none' && +s.opacity>0;
     }
     window.__brainSnapshot = function(){
       let n = 0;
       const out = [];
       document.querySelectorAll(SEL).forEach(el=>{
         if(!isVisible(el)) return;
         const id = ++n;
         el.setAttribute('data-brain-id', id);
         const r = el.getBoundingClientRect();
         out.push({
           id, tag: el.tagName.toLowerCase(),
           role: el.getAttribute('role') || el.tagName.toLowerCase(),
           type: el.type || null,
           name: (el.getAttribute('aria-label') || el.innerText || el.placeholder || el.name || '').trim().slice(0,80),
           value: el.value || null,
           href: el.href || null,
           x: r.x+r.width/2, y: r.y+r.height/2, w: r.width, h: r.height,
           inViewport: r.top >= 0 && r.bottom <= innerHeight
         });
       });
       return out;
     };
   })();
   ```
3. **融合**:把两个视角合并,返回扁平语义 + 交互清单。

**配套改动**:`click / type / hover / double_click / right_click` 增加可选 `id: number` 参数,内部拼 `[data-brain-id="N"]` 定位,不需要 LLM 写 selector。

**相对截图的收益**:token 成本 ~1/10,无坐标歧义,selector 再脆弱也不怕。

**难度**:中等。~300-400 行新代码。

### 3.2 `browser.network` —— 网络请求只读查询【D 只读部分】

**输入**:
```json
{
  "action": "list" | "get" | "clear" | "wait_for"
  "url_pattern": "<regex>",
  "status": 401,
  "method": "POST",
  "since_ts": 1700000000,
  "limit": 50
}
```

**实现**:
1. `attachToTarget` 时已启用 Network,**补上事件订阅**:
   ```go
   client.On("Network.requestWillBeSent", func(p){ buf.Add(...) })
   client.On("Network.responseReceived",  func(p){ buf.Update(...) })
   client.On("Network.loadingFinished",   func(p){
     // 如果被标记为 "capture body",再发 Network.getResponseBody
   })
   client.On("Network.loadingFailed", func(p){ buf.MarkFailed(...) })
   ```
2. `BrowserSession` 里维护 `NetBuf`(环形缓冲,默认 200 条),按 requestId 聚合。
3. `action=list`:按 filter 过滤返回。
4. `action=get`:指定 id 拉完整 body(按需调 `Network.getResponseBody`)。
5. `action=wait_for`:阻塞等待匹配的 response,返回 body(用于"等 API 调用完成")。

**收益**:LLM 可直接判断"登录 API 返回 401 → 密码错了",而不是看截图上的错误提示。

**难度**:中等。~350 行。

### 3.3 修复 `wait.idle` 为真 networkIdle【E 关键部分】

**现状**:`wait.idle` 注释号称"network idle",实际只 `sleep 500ms + 轮询 readyState`,名不副实,SPA 场景几乎必踩坑。

**修复**:复用 3.2 的 Network 订阅,`BrowserSession` 维护 `inflightRequests` 计数:
- `requestWillBeSent` → ++
- `loadingFinished / loadingFailed` → --
- `wait.idle` 轮询 `inflightRequests == 0 连续 500ms` 才返回。

**同时新增**:`wait.response`(action=wait_for 的语法糖):
```json
{ "condition": "response", "url_pattern": "/api/login", "timeout_ms": 5000 }
```

**难度**:简单(复用 3.2 基础设施)。~80 行。

### 3.4 `browser.sitemap` —— **整站嗅探 + 路由规则提取**【L 新需求】

> **📌 优先级修订(2026-04-20)**:原列入 P0,现**调整为 P1**。
>
> 理由:99% 的日常 Agent 任务不需要整站嗅探——它们只关心"当前这一页怎么操作"。sitemap 的真正价值是**给 40 号文档的语义理解层(阶段 1 批量预处理 / 阶段 2 模式库)提供数据管道**,而不是直接喂给 Agent 看。
>
> 应用场景明确:
> - 安全扫描(扫敏感路径)
> - SEO / 死链分析
> - 数据抓取前的"地图探路"
> - **语义理解层的数据源**(阶段 1 嗅探一次整站,批量预处理语义,缓存到 SQLite)
>
> 日常"帮我在这个页面下单"这类任务,不应默认触发 sitemap。

**目标**:给定一个入口 URL,LLM 能拿到整站的页面路径列表和路由规则,用于后续批量抓取、测试覆盖、SEO 分析、安全扫描。

**输入**:
```json
{
  "start_url": "https://example.com",
  "max_pages": 100,
  "max_depth": 3,
  "same_origin_only": true,
  "include_external": false,
  "concurrency": 3,
  "delay_ms": 200,
  "obey_robots_txt": true,
  "capture_api_routes": true,
  "detect_router_type": true,
  "include_sitemap_xml": true,
  "ignore_patterns": ["*.pdf", "*.zip", "/admin/*"]
}
```

**输出**:
```json
{
  "start_url": "...",
  "pages_visited": 87,
  "pages": [
    {
      "url": "https://example.com/users/42",
      "canonical": "/users/:id",        // 路由参数化后的 pattern
      "title": "User 42",
      "depth": 2,
      "discovered_from": "/users",
      "status": 200,
      "content_type": "text/html",
      "internal_links": 12,
      "external_links": 3,
      "has_forms": true,
      "form_actions": ["/users/42/update"],
      "api_calls": ["/api/v1/users/42", "/api/v1/users/42/orders"]
    }
  ],
  "route_patterns": [
    { "pattern": "/users/:id",        "matches": 23, "example": "/users/42" },
    { "pattern": "/posts/:slug",      "matches": 15, "example": "/posts/hello-world" },
    { "pattern": "/api/v1/users/:id", "matches": 18, "type": "api" }
  ],
  "router_type": "spa_history",   // "spa_history" | "spa_hash" | "ssr" | "static"
  "sitemap_xml_urls": [ /* 来自 /sitemap.xml */ ],
  "robots_txt_disallow": ["/admin/", "/api/private/"],
  "unreachable": [
    { "url": "/broken", "status": 404, "discovered_from": "/" }
  ]
}
```

**实现要点**:

#### 3.4.1 BFS 爬取策略

```go
type sitemapCrawler struct {
    session     *BrowserSession
    visited     map[string]bool  // normalized URL
    queue       []crawlTarget
    maxPages    int
    maxDepth    int
    semaphore   chan struct{}    // 限流
    routeMiner  *routePatternMiner
    robotsRules *robotsTxt
}
```

主循环:
1. 从 `start_url` 出发 BFS
2. 每个 URL:`browser.open` → 等 idle → 提取所有 `<a href>` + `<form action>` + JS fetch/XHR → 入队
3. `same_origin_only=true` 时过滤跨域
4. `obey_robots_txt=true` 时先拉 `/robots.txt` 解析 Disallow
5. `max_pages / max_depth / ignore_patterns` 三重限流
6. `concurrency=3` 时开 3 个 tab 并行(配合 P1 的多 Context)

#### 3.4.2 路由参数化(关键难点)

把 `/users/42, /users/137, /users/9` 归并为 `/users/:id`:

```go
// 算法:相同 path 层级结构 + 段落差异度检测
// 1. 按 / 切分路径段
// 2. 统计每段的值分布:如果同一位置出现 N > threshold 个不同值,且符合某种正则(数字/uuid/slug),标记为参数
// 3. 正则识别器:
//    - 纯数字     → :id
//    - UUID       → :uuid
//    - slug(字母+数字+-) → :slug
//    - 日期(YYYY-MM-DD) → :date
//    - hash(40hex) → :hash
//
// 示例:
//   /users/42         →  [/users, 42]
//   /users/137        →  [/users, 137]
//   /users/9/orders   →  [/users, 9, orders]
//
// 参数化后:
//   /users/:id
//   /users/:id/orders
```

实现参考现成思路:[route-parser](https://github.com/expressjs/express/blob/master/lib/router/route.js) 的逆向。

#### 3.4.3 Router 类型识别

- **SPA History**:初始 HTML 几乎相同,路由变化不触发页面重载,URL 无 `#`,存在 `history.pushState` 调用
- **SPA Hash**:URL 含 `#/`,hashchange 事件驱动
- **SSR**:每个 URL 都产生完整 HTML 响应,`Content-Type: text/html; charset=...`,响应体差异大
- **静态站**:所有 URL 映射到 `.html` 文件

检测逻辑:抽样前 5 个页面,观察:
1. 是否有 `<base href>` / `<script src>` 含 `react|vue|angular` 关键字
2. URL 是否含 `#`
3. Network 记录是否有 `/api/*.json` 频繁请求(SPA 特征)
4. `Document.readyState` 初次加载时间 vs 路由切换时间对比

#### 3.4.4 API 路由发现(`capture_api_routes`)

复用 3.2 的 Network 订阅,过滤:
- `Content-Type: application/json`
- `XMLHttpRequest` / `fetch`
- URL 匹配 `/api/*` / `/v1/*` / `/graphql` 等常见模式

归并 API 路由参数(同 3.4.2),输出到 `route_patterns[].type="api"`。

#### 3.4.5 sitemap.xml / robots.txt 融合

启动时先 fetch:
- `<origin>/robots.txt` → 解析 `Disallow` 排除路径 + 读 `Sitemap:` 指令
- `<origin>/sitemap.xml` → 解析 `<loc>` 标签,拿到站长声明的入口

这两个是"官方宣告的站点地图",和 BFS 发现的合并去重,可发现 BFS 够不到的孤立页面。

#### 3.4.6 输出裁剪

`max_pages=100` 时,超出按 BFS 顺序截断;`route_patterns` 永远完整返回(它是汇总视图);`pages[]` 按 `url` 字典序排序便于 diff。

**输出大小估算**:100 页 × 每条 300 字节 ≈ 30KB,LLM 可直接消化;500+ 页时自动开启 `--summary-only` 只返 `route_patterns` + 统计。

**难度**:复杂。~800-1000 行新代码,是本轮改造最大的一项。分两阶段交付:
- **Stage 1**:基础 BFS + 同源爬取 + 路由参数化 + robots.txt
- **Stage 2**:API 路由发现 + Router 类型检测 + 多 Context 并发

**应用场景**:
- 网站测试覆盖率分析
- SEO / 死链扫描
- 安全扫描(扫 `/admin` 等敏感路径)
- 数据抓取前的"地图探路"
- API 契约反推(从前端调用发现后端路由)

---

## 4. P1 能力清单(覆盖真实复杂页面)

### 4.1 `browser.iframe` 与 iframe 穿透【K】

订阅 `Page.frameAttached / Runtime.executionContextCreated`,维护 `frameId → contextId` 映射;所有工具增加可选 `frame` 参数(按 URL 或父文档 selector 定位 iframe)。

支付页(Stripe/PayPal)、广告、富文本编辑器、captcha 全涉及 iframe,当前能看到不能操作。

### 4.2 `browser.downloads` 下载支持【I】

`Browser.setDownloadBehavior` + `Page.downloadWillBegin/downloadProgress` 事件 + 新工具 `browser.downloads {action: list/wait/get_path}`。

### 4.3 `browser.storage` 持久化【F】

```json
{ "action": "export|import|clear", "path": "/tmp/state.json" }
```

导出 cookies + localStorage + sessionStorage;导入时先导航到 origin 再 Runtime.evaluate 注入。解决"每次都要重新登录"痛点。

### 4.4 `browser.fill_form` 批量填充【C】

按 `name / id / aria-label / placeholder / label[for]` 优先级匹配,批量填 10 字段表单从 10 次 tool call 降到 1 次。

### 4.5 `browser.changes` DOM diff【E 剩余】

注入 `MutationObserver` 缓存变化,LLM 调用时消费缓冲区,返回"自上次以来 DOM 变化摘要",SPA 异步内容加载场景必需。

---

## 5. P2 能力清单(对齐企业级,放 Pro 版)

按 [`31-browser-brain-免费版与Pro版规划`](./31-browser-brain-免费版与Pro版规划.md) §3 商业化方案,以下进 Pro 版:

### 5.1 多 BrowserContext / 并发 target【G】

`Target.createBrowserContext` + 重构 BrowserSession 支持 context map + 所有工具接收可选 `target_id`。

### 5.2 `browser.intercept` 请求拦截【D 剩余】

基于 `Fetch.enable + Fetch.requestPaused`,支持 mock 响应、改请求头、block 资源(关广告/字体加速)。测试场景刚需。

### 5.3 Stealth + Emulation【H】

- 注入 `navigator.webdriver = undefined` 等 stealth 脚本
- `Emulation.setUserAgentOverride / setTimezoneOverride / setGeolocationOverride` 等
- Cloudflare / reCAPTCHA 挑战检测 → 上报给 LLM 决策

### 5.4 `browser.pdf`【J】

`Page.printToPDF` 一行调用,报表生成场景。

### 5.5 证据/断言/会话包

`browser_pro.trace_start/stop/export`、`browser_pro.assert_*`、`browser_pro.session_save/load` 等(见 31 号文档 §7)。

---

## 6. 实施路线图

### Phase 1(2-3 周,免费版 P0)

| 周 | 交付 | 代码量 |
|---|---|---|
| W1 | `browser.snapshot`(A+B)+ click/type/hover 加 `id` 参数 | ~400 行 |
| W2 | `browser.network` + 真 networkIdle(3.2 + 3.3) | ~450 行 |
| W3 | `browser.sitemap` Stage 1(BFS + 参数化 + robots.txt) | ~500 行 |

### Phase 2(3-4 周,免费版 P1)

| 周 | 交付 | 代码量 |
|---|---|---|
| W4 | `browser.iframe` 穿透 | ~300 行 |
| W5 | `browser.downloads` + `browser.storage` | ~300 行 |
| W6 | `browser.fill_form` + `browser.changes`(DOM diff) | ~200 行 |
| W7 | `browser.sitemap` Stage 2(API 路由 + Router 类型检测 + 多 tab 并发) | ~400 行 |

### Phase 3(持续,Pro 版)

按 31 号文档商业化路线图,不计入本增强计划。

---

## 7. 向后兼容

**现有 15 个工具 schema 保持不变**。增量改动:
1. `click / type / hover / double_click / right_click / select` 新增可选参数 `id: number`(替代 selector/x,y)
2. `screenshot` 新增可选 `with_boxes: bool`(附带所有 `data-brain-id` 元素的 bbox,视觉+逻辑对齐)
3. **`wait.idle` 语义修订(2026-04-20)**:不悄悄改旧工具行为。采用新工具策略:
   - `wait.idle` 保持原有"500ms + readyState"语义,但标记 `@deprecated`,后续小版本移除
   - 新增独立工具 `wait.network_idle`,实现真正的 networkIdle 语义
   - 避免使用 `mode` 参数隐式切换,那是埋雷

**新增工具**(共 8 个,免费版 P0+P1):
- `browser.snapshot`
- `browser.network`
- `browser.sitemap`
- `browser.iframe`
- `browser.downloads`
- `browser.storage`
- `browser.fill_form`
- `browser.changes`

注册后 Browser Brain 工具数:15 → 23(含 `browser.note` 是 24)。

---

## 8. 风险与缓解

| 风险 | 缓解 |
|---|---|
| `browser.sitemap` 滥用导致被目标站点封 IP | 默认 `obey_robots_txt=true` + `concurrency=3` + `delay_ms=200`,文档强调"仅用于授权站点" |
| `browser.snapshot` 返回过大 | `max_elements` 限制 + `viewport_only` 选项 + 自动降级(超过 500 个元素时只返回 bbox 不返回 innerText) |
| `data-brain-id` 污染原站 DOM | 仅在 JS 内存中赋值 `el.setAttribute`,不影响渲染;用完后可提供 `browser.snapshot.clear` 清除 |
| 真 networkIdle 在长轮询页面永远不 idle | 超时保护:默认 30s 强制返回,返回值标注 `forced: true` |
| iframe 穿透 + cross-origin 限制 | 跨域 iframe 只能用 CDP 的 OOPIF target 路径,Phase 2 单独处理 |
| 路由参数化误判 | `pattern` 字段附带 `confidence: 0.85`,低置信度时不合并,保留具体 URL |

---

## 9. 为什么感知比"加工具"更重要

审计时问过一个哲学问题:**"为什么 Browser Brain 有 15 个工具,复杂任务表现仍不好?"**

答案是:**15 个工具都是"动作"类(点击、输入、滚动),感知类只有 screenshot 和 eval 两个**。LLM 接到任务"在这个页面填表单"时,它能做的事只有:
1. 截图,用视觉推理猜表单字段在哪 → token 贵、歧义大
2. 写 `document.querySelectorAll('input')` + 自己遍历 → 脆弱,一次 getter 改了就坏
3. 盲试点击坐标 → 高频失败

加再多"动作"类工具(更多的 click/scroll 变体)都不解决根本问题——**感知带宽太窄**。

P0 三件套(snapshot / network / sitemap)解决的就是感知带宽:
- **snapshot** 让 LLM 看到结构(语义 + 坐标融合)
- **network** 让 LLM 看到状态(API 请求/响应)
- **sitemap** 让 LLM 看到地形(整站路由图)

这三个到位后,LLM 完成"登录 → 进入后台 → 找到订单列表 → 导出 CSV"这类任务的:
- **简单任务成功率**:40% → 70-80%
- **复杂任务成功率**:25% → 50-60%(**单靠感知带宽扩展到此为止**)

**再要往上提升,必须靠 40 号文档的语义理解层**——纯结构感知的天花板就在这里。

---

## 10. 与其他文档的关系

| 文档 | 关系 |
|---|---|
| [`40-Browser-Brain语义理解架构`](./40-Browser-Brain语义理解架构.md) | **上层架构**。本文是其基础设施层,snapshot/network/sitemap 的数据喂给语义理解层 |
| [`41-语义理解阶段0实验设计`](./41-语义理解阶段0实验设计.md) | **前置条件**。40 号架构的 Go/No-Go 实验 |
| [`31-browser-brain-免费版与Pro版规划`](./31-browser-brain-免费版与Pro版规划.md) | 商业化边界定义。本文 P0/P1 进免费版,P2 进 Pro 版 |
| [`38-v3后续增强计划`](./38-v3后续增强计划.md) | 全项目增强计划。本文是其中 Browser 专章的细化 |
| [`32-v3-Brain架构`](./32-v3-Brain架构.md) | 四种 runtime 类型定义。Browser Brain 保持 `native` runtime |
| [`23-安全模型`](./23-安全模型.md) | `browser.sitemap` 涉及爬虫行为,需遵守其中的沙盒和外部网络策略 |

---

**结论**:这份文档定义的 P0 三件套是 Browser Brain 从"能用"到"好用"的决定性一跃。`browser.sitemap` 更是能打开全新场景(站点探测、API 发现、测试覆盖),是本轮最有产品辨识度的能力。
