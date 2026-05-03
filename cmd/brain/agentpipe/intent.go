// intent.go — 用户输入的意图分类。
//
// 三模式 (chat/run/serve) 都通过 IntentClassifier 判断用户输入是简单问答
// 还是项目级需求,自动选择执行路径:
//
//   IntentSimple   → Invocation.Execute() 直接走 Runner
//                    适合: "天气如何"/"解释 X"/"做个 hello world"等单步任务
//   IntentProject  → 走 PlanOrchestrator.ExecuteProject 七阶段闭环
//                    适合: "做一个完整的博客系统"/"实现贪吃蛇游戏带前后端"等多模块任务
//
// 设计原则:
//   - 默认 IntentSimple,IntentProject 必须有强信号才触发 (避免简单问答被误判跑全流程)
//   - 关键词 + 长度 + 多模块词组三组信号联合判断
//   - 用户显式指令 ("用 plan 做" / "/plan") 优先级最高,直接 IntentProject

package agentpipe

import (
	"strings"
	"unicode/utf8"
)

// Intent 表示用户输入的执行意图分类。
type Intent int

const (
	// IntentSimple 简单问答或单步任务,直接走 Runner.Execute。
	IntentSimple Intent = iota
	// IntentProject 项目级需求,走 PlanOrchestrator 七阶段闭环。
	IntentProject
)

func (i Intent) String() string {
	switch i {
	case IntentSimple:
		return "simple"
	case IntentProject:
		return "project"
	default:
		return "unknown"
	}
}

// projectKeywords 是表达"完整项目/系统/产品"意图的强信号词。
// 命中任一即视为 IntentProject 候选,但还要叠加"动作动词"+"非疑问句"才能确定。
var projectKeywords = []string{
	// 中文
	"完整项目", "完整的项目", "完整系统", "完整的系统",
	"前后端分离", "全栈", "前端 + 后端",
	"多模块", "微服务",
	"项目级", "工程级",
	"用 plan", "/plan",
	// 英文
	"full-stack", "fullstack", "full stack",
	"complete project", "entire system",
	"microservices", "multi-module",
	"frontend and backend", "frontend + backend",
	"use plan", "use /plan",
}

// strongTriggers 是显式且无歧义的项目级触发词,命中直接 IntentProject,
// 不再叠加动作动词/疑问句检查(用户明确要走 plan 流程)。
var strongTriggers = []string{
	"用 plan", "/plan", "use plan", "use /plan",
}

// actionVerbs 是表达"做/实现/搭建"意图的动作动词。
// projectKeywords 命中后必须再叠加动作动词才确认 IntentProject,
// 避免"什么是完整项目"/"解释微服务架构"等疑问句被误判。
var actionVerbs = []string{
	// 中文动作动词
	"做一个", "做个", "做一套",
	"实现", "搭建", "搭一个", "搭个",
	"开发", "构建", "创建", "建一个",
	"写一个", "写个", "撸一个", "造一个",
	"帮我做", "帮我建", "帮我搭", "帮我写", "帮我实现",
	"我要做", "我要建", "我要搭", "我要写", "我要实现",
	// 英文动作动词
	"build", "create", "implement", "develop",
	"make a", "make me", "set up", "scaffold",
	"help me build", "help me create", "i want to build", "i need to build",
}

// questionMarkers 是疑问句标记,命中任一直接 IntentSimple。
// 即使叠加了项目关键词 + 长度,疑问句永远是 simple(用户在问问题,不是要做)。
var questionMarkers = []string{
	"什么是", "啥是", "什么叫", "怎么理解",
	"为什么", "为啥", "怎么会", "为何",
	"是什么", "是啥", "区别", "差别", "不同点", "差异",
	"解释一下", "解释下", "解释 ", "解释:",
	"介绍一下", "介绍下", "讲解", "说说", "聊聊",
	"how does", "how do", "what is", "what are", "what's",
	"why is", "why does", "why do",
	"explain", "describe", "compare",
}

// projectStructureWords 是表达"项目结构"的弱信号(配合长度 / 关键词增强判断)。
var projectStructureWords = []string{
	// 中文 — 描述项目组件 / 角色 / 流程
	"模块", "组件", "页面", "服务", "数据库",
	"前端", "后端", "API", "接口",
	"用户", "登录", "注册", "权限",
	"鉴权", "认证",
	"部署", "上线", "构建",
	// 英文
	"module", "component", "page", "service", "database",
	"frontend", "backend", "api", "endpoint",
	"user", "login", "register", "auth",
	"deploy", "build",
}

// IntentClassifier 用启发式 + 关键词判定输入意图。
// 不依赖 LLM 调用 (零额外开销),误判时倾向 IntentSimple (保守)。
type IntentClassifier struct {
	// MinChineseChars 触发 IntentProject 的最少中文字符数,默认 30。
	// 太短的"做项目"通常是闲聊,不上 PlanOrchestrator。
	MinChineseChars int
	// MinEnglishWords 触发 IntentProject 的最少英文单词数,默认 15。
	MinEnglishWords int
	// MinStructureHits 项目结构词最少命中数,默认 3。
	MinStructureHits int
}

// NewDefaultIntentClassifier 返回默认配置的分类器。
// 阈值偏保守 — 只有强信号才走 IntentProject,避免误把"算一下 X 的值"判成项目级。
func NewDefaultIntentClassifier() *IntentClassifier {
	return &IntentClassifier{
		MinChineseChars:  30,
		MinEnglishWords:  15,
		MinStructureHits: 5, // 提升阈值,避免长技术问答(常含 5+ 结构词)误判
	}
}

// Classify 根据用户输入返回意图分类。
//
// 决策顺序(短路):
//  1. 空 → IntentSimple
//  2. 疑问句标记("什么是"/"为什么"/"how does"/"?")→ IntentSimple
//     (无论后面是否含 strongTrigger,"什么是用 plan 命令"应该是问问题
//     不是要走 plan;疑问句永远优先)
//  3. 显式触发词("/plan"等) → IntentProject(用户明确要走 plan)
//  4. 项目关键词命中 + 动作动词命中 → IntentProject
//  5. 长输入(>= 阈值) + 多个结构词命中 + 动作动词命中 → IntentProject
//  6. 默认 IntentSimple
func (c *IntentClassifier) Classify(input string) Intent {
	if c == nil {
		return IntentSimple
	}
	text := strings.TrimSpace(input)
	if text == "" {
		return IntentSimple
	}
	low := strings.ToLower(text)

	// 1. 疑问句直接 IntentSimple — 用户在问问题不是要做事。
	// 必须放在 strongTriggers 之前,否则"什么是 /plan"会被误判 IntentProject。
	if hasAnyKeyword(low, questionMarkers) {
		return IntentSimple
	}
	// 中文?也是疑问句(rune-level 判断,strings.Contains 直接命中也可以)
	if strings.Contains(text, "?") || strings.Contains(text, "?") {
		return IntentSimple
	}

	// 2. 显式触发词 — 非疑问句下用户明确要走 plan,无需动作动词叠加
	for _, kw := range strongTriggers {
		if strings.Contains(low, strings.ToLower(kw)) {
			return IntentProject
		}
	}

	hasAction := hasAnyKeyword(low, actionVerbs)

	// 3. 项目关键词 + 动作动词 → IntentProject
	if hasAnyKeyword(low, projectKeywords) && hasAction {
		return IntentProject
	}

	// 4. 长度判断:太短的输入即使含动作动词也 simple
	chineseChars := countChineseChars(text)
	englishWords := countEnglishWords(text)
	if chineseChars < c.MinChineseChars && englishWords < c.MinEnglishWords {
		return IntentSimple
	}

	// 5. 长输入 + 结构词多命中 + 动作动词 → IntentProject
	if !hasAction {
		return IntentSimple
	}
	hits := 0
	for _, w := range projectStructureWords {
		if strings.Contains(low, strings.ToLower(w)) {
			hits++
			if hits >= c.MinStructureHits {
				return IntentProject
			}
		}
	}

	return IntentSimple
}

// hasAnyKeyword 任意命中即返回 true。
func hasAnyKeyword(low string, kws []string) bool {
	for _, kw := range kws {
		if strings.Contains(low, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// countChineseChars 统计字符串里 CJK 字符数(rune 在 0x4E00-0x9FFF 范围)。
func countChineseChars(s string) int {
	count := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x4E00 && r <= 0x9FFF {
			count++
		}
		i += size
	}
	return count
}

// countEnglishWords 统计英文单词数(空白分隔的 token,长度 >= 2 视为词)。
func countEnglishWords(s string) int {
	fields := strings.Fields(s)
	count := 0
	for _, f := range fields {
		// 过滤纯标点 / 单字
		alphaOnly := true
		for _, r := range f {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
				alphaOnly = false
				break
			}
		}
		if alphaOnly && len(f) >= 2 {
			count++
		}
	}
	return count
}
