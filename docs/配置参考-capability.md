# Capability 配置参考

## 这是什么

`active_provider.capabilities` 是 `~/.brain/config.json` 里 active_provider 块下的可选字段,用于**显式声明 LLM provider 的能力**(是否支持 tool_choice、是否是 reasoner 思考类模型、是否吐 reasoning_content 等)。

**99% 的场景下不需要填它** —— brain 内置了国内外主流 model 的精确 capability 数据(见 `sdk/llm/builtin_capabilities.go` 的 `builtinTable`),只要你用的 model 在内置表里,系统会零配置自动适配。

只有以下场景需要手动声明:
- 内置表不认识你接的 model(全新模型 / 小众模型 / 内部 fine-tune)
- 内置表数据不准(provider 升级了行为、你用的是定制代理)
- 想强制覆盖内置行为做实验

## 优先级链

Capability 的最终值由 4 个来源按优先级链合成:

```
default → InferCapabilities 启发式 → builtin 表 → user override
                                                    ↑ 最高优先级
```

每一层都做**字段级合并**(只覆盖该层有意见的字段),不是整体替换。意味着:你只声明 `tool_choice` 一个字段时,其他字段沿用 builtin / 启发式 / 默认值,不会被清零。

| 层 | 来源 | 触发条件 | 数据来源 |
|---|---|---|---|
| L1 | user override | config.json 写了 `capabilities` 块 | 你 |
| L2 | builtin 表 | 内置 lookup 命中 | brain 仓库 `builtin_capabilities.go` |
| L3 | 启发式 | 上面没命中,model 名/baseURL 含通用关键词 | brain 仓库 `capabilities.go` `InferCapabilities` |
| L4 | default | 都没命中 | brain 仓库 `capabilities.go` `DefaultCapabilities` |

## 字段说明

```json
{
  "active_provider": {
    "protocol": "openai",
    "base_url": "https://api.example.com/v1",
    "api_key": "...",
    "model": "my-custom-model-2030",

    "capabilities": {
      "family":                    "my-custom-family",
      "native_tool_call":          true,
      "tool_choice":               "none",
      "reasoner":                  true,
      "emits_reasoning_content":   true,
      "prefers_structured_output": false,
      "max_parallel_tools":        1
    }
  }
}
```

### 各字段含义

| 字段 | 类型 | 默认 | 含义 |
|---|---|---|---|
| `family` | string | (内置或启发式给) | 自定义 family 名,影响 dashboard 显示和日志聚合,**不影响 runner 行为**。一般无需填。 |
| `native_tool_call` | bool | true | LLM 是否原生输出 tool_use 块。**几乎所有现代 model 都是 true**,只有纯 completion 接口才设 false。 |
| `tool_choice` | string | (按 family 推断) | 取值: `"none"` / `"auto"` / `"required"` / `"specific"`。表示 provider 对 `tool_choice` 字段的支持级别。设错的后果见下表。 |
| `reasoner` | bool | false | 是否思考类模型(deepseek-r / mimo / qwq / o1 / o3 等)。设 true 时 runner 给第 1 轮 thinking-only 一次免费 grace turn,Clarifier 用更短的提示节省 thinking-token。 |
| `emits_reasoning_content` | bool | false | 响应是否包含 `reasoning_content` 字段(deepseek-reasoner 等)。设 true 时 provider 必须将其 round-trip 到下一轮请求。 |
| `prefers_structured_output` | bool | false | 模型是否倾向输出结构化(tool_use / JSON)而非自然语言。仅启发式提示,目前不影响 runner。 |
| `max_parallel_tools` | int | 1 | 单轮响应中模型可可靠输出的最大 tool_use 块数。影响 BatchPlanner 是否启用并行调度。 |

### tool_choice 设错的后果

| 真实情况 | 你设的值 | 后果 |
|---|---|---|
| 不支持 | `"required"` | HTTP 400,LLM 调用直接失败,需要 retry |
| 不支持 | `"auto"` | 字段被 silently ignore,行为等同于不设;**安全** |
| 不支持 | `"none"` | 字段被 silently ignore,行为等同于不设;**安全** |
| 支持 | `"none"` | 失去 tool_choice 强制能力,LLM 容易 announce-without-act,需要 IntentChain + Clarifier 兜底,**变慢** |
| 支持 | `"required"` | 行为正确;sub agent 第 1 turn 强制调工具 |

**保守原则**:不确定就设 `"none"`,顶多慢一点,不会崩。

### reasoner 设错的后果

| 真实情况 | 你设的值 | 后果 |
|---|---|---|
| 是 reasoner | false | 第 1 轮 thinking-only 触发 nudge,浪费一次往返 |
| 不是 reasoner | true | 第 1 轮 thinking-only 不 nudge,但因为不是真 reasoner 也不会有 thinking,实际无影响 |

**保守原则**:不确定就设 true,无副作用,只是多一次 grace turn。

## 完整示例:接入一个全新的小众 model

假设你接了一个内置表不认识的 `wizard-coder-15b-instruct`,你知道它:
- OpenAI 兼容接口
- 不支持 tool_choice(silently ignore)
- 不是 reasoner
- 一次响应最多并行 2 个 tool_use

```json
{
  "active_provider": "openai",
  "providers": {
    "openai": {
      "protocol":  "openai",
      "base_url":  "https://my-internal-api.corp/v1",
      "api_key":   "sk-...",
      "model":     "wizard-coder-15b-instruct",

      "capabilities": {
        "tool_choice":        "none",
        "reasoner":           false,
        "max_parallel_tools": 2,
        "family":             "wizard-coder"
      }
    }
  }
}
```

## 完整示例:覆盖内置表的某个字段

假设你用的 deepseek 内部代理特殊改了行为,真的支持 `tool_choice=required`(罕见)。内置表标的是 `"none"`,你要覆盖:

```json
{
  "providers": {
    "deepseek": {
      "protocol":  "openai",
      "base_url":  "https://my-deepseek-proxy.corp/v1",
      "api_key":   "...",
      "model":     "deepseek-v4-pro",

      "capabilities": {
        "tool_choice": "required"
      }
    }
  }
}
```

注意:**只声明了一个字段**,其余 `family`/`reasoner`/`max_parallel_tools` 沿用 builtin 表里 deepseek 的值。

## 内置覆盖了哪些 model

完整清单见代码:[`sdk/llm/builtin_capabilities.go`](../sdk/llm/builtin_capabilities.go) 的 `builtinTable`。

**国外**:Claude / GPT-4/5 / Gemini / Mistral / Cohere / Llama / OpenAI o-series

**国内**:DeepSeek (chat/coder/reasoner) / Qwen (含 QwQ/R1) / Mimo / GLM / Doubao / Moonshot Kimi / Yi / Step / Hunyuan / ERNIE / Spark

**平台/部署**:OpenRouter / Together / Fireworks / Groq / SiliconFlow(通过 model 名继承)/ 本地部署(localhost / ollama / llama.cpp)

新 model 出来要加到内置表,提 PR 修改 `builtinTable`(参考已有条目格式),包含 `Confidence` 和 `VerifiedAt` 字段。

## 排查:我设了 capability 但不生效

1. **JSON 解析错误** — `tool_choice` 拼错(如写成 `"REQUIRED"` 而不是 `"required"`)会导致 `OpenConfigured` 返回 `active_provider.capabilities parse: ...` 错误,brain 启动直接失败。修正 typo 即可。
2. **写在了错地方** — `capabilities` 必须在 `providers.<name>` 块下,不是顶级 active_provider。看本文档示例对齐。
3. **被 active_provider 切换** — 如果你用 `--provider <name>` flag 或 `active_provider` 字段切到了别的 provider,只有那个 provider 的 capabilities 生效。
4. **被 brain_models 二级模型覆盖了 model 但 capabilities 没改** — `providers.<name>.models.<brain_kind>` 把 model 换成了别的(比如 sub agent 用 mimo,central 用 deepseek),capability 是按**最终生效的 model**走 builtin 表 lookup,不是按 capabilities 块。如果你想给特定 brain_kind 单独覆盖 capability,需要在该 sub provider 单独配。

## 设计原则(为什么这样设计)

这一节给将来读源码的开发者:

- **配置驱动 + 数据表分离** — 用户不应该改 Go 代码就能接入新 model。`builtin_capabilities.go` 是声明式数据,新增一个 family 加一行不改逻辑。
- **字段级 merge** — 用户大概率只想覆盖 1-2 个字段,整体替换会逼用户填全部字段,门槛太高。
- **String JSON for tool_choice** — 内部用 `ToolChoiceMode int`(类型安全 + iota 自增),用户面 JSON 用 `"none"/"auto"/...`(可读 + 可发现)。`UnmarshalJSON` 在边界翻译。
- **Pointer 字段做 override** — `*bool` 让 `null/缺失` 与 `false` 区分得开,避免"用户想说 reasoner=false"被当成"用户没说"。
- **typo 报错而非降级** — `"REQUIRED"` 大写会报错而非默认 `None`,因为静默降级会让用户找不到为什么 mimo 突然慢了。
- **Resolver 不可扩展** — 当前只有 4 层,没有 Resolver 接口抽象。如果将来需要加 manifest / probe 等来源,用接口重构;现在不预留。
