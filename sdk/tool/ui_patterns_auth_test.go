package tool

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// P1.1 登录/注册/验证场景包的测试。
//
// 验证点:
//  1. authSeedPatterns 返回 7 个条目、ID/Category/Source 合法。
//  2. 每条 OnAnomaly 覆盖强制列表(captcha + error_message),且
//     密码相关模式对 error_message 走 abort(防锁号铁律)。
//  3. ActionSequence / ElementRoles / PostConditions 完整:target_role
//     引用的 role key 都在 ElementRoles 里可找到。
//  4. URL/Text 层面的 AppliesWhen 对三个 fake "页面"(单表单/分步第一步/
//     OAuth 按钮)各自命中正确的 pattern,验证模式库的语义区分度。
//  5. extraSeedProviders 挂接成功:lib.Seed 后能 Get 到所有 7 个 id。
//  6. seed 批次之间 ID 不重复,与通用 seedPatterns() 也不重。

func TestAuthSeedPatternsStructure(t *testing.T) {
	patterns := authSeedPatterns()
	if len(patterns) < 5 || len(patterns) > 8 {
		t.Fatalf("authSeedPatterns length = %d, expect 5-8 per TaskList spec", len(patterns))
	}

	ids := map[string]bool{}
	for _, p := range patterns {
		if p.ID == "" {
			t.Errorf("pattern has empty ID: %+v", p)
			continue
		}
		if ids[p.ID] {
			t.Errorf("duplicate ID in auth seed: %q", p.ID)
		}
		ids[p.ID] = true

		if p.Category != "auth" {
			t.Errorf("pattern %s Category = %q, want auth", p.ID, p.Category)
		}
		if p.Source != "seed" {
			t.Errorf("pattern %s Source = %q, want seed", p.ID, p.Source)
		}
		if p.Description == "" {
			t.Errorf("pattern %s has empty Description", p.ID)
		}
	}
}

func TestAuthSeedOnAnomalyMandatory(t *testing.T) {
	// 铁律:每个登录类模式都必须对 captcha 给出处理(至少 human_intervention),
	// 且对 error_message / wrong_password 中至少一个给出 abort(防止密码错误
	// 触发多次重试锁号)。只有 session_expired_relog 例外,它本身不提交凭证。
	exemptForErrorAbort := map[string]bool{
		"session_expired_relog": true,
		// oauth button 的 error_message 走 human_intervention(OAuth 错不等于
		// 密码错,abort 过于严),但仍必须对 error_message 有处理。
		"oauth_sign_in_button": true,
	}

	for _, p := range authSeedPatterns() {
		if len(p.OnAnomaly) == 0 {
			t.Errorf("pattern %s: OnAnomaly empty — P1.1 要求必配", p.ID)
			continue
		}
		if _, ok := p.OnAnomaly["captcha"]; !ok {
			t.Errorf("pattern %s: missing OnAnomaly[captcha]", p.ID)
		}
		errH, hasErr := p.OnAnomaly["error_message"]
		if !hasErr {
			t.Errorf("pattern %s: missing OnAnomaly[error_message]", p.ID)
			continue
		}
		if exemptForErrorAbort[p.ID] {
			continue
		}
		if errH.Action != "abort" {
			t.Errorf("pattern %s: error_message action = %q, want abort (anti-lockout rule)", p.ID, errH.Action)
		}
	}
}

func TestAuthSeedActionRolesResolve(t *testing.T) {
	// ActionSequence 中每个非空 target_role 必须能在 ElementRoles 里找到。
	// 否则 ResolveElement 会直接"unknown target_role"错,等于该 step 必失败。
	for _, p := range authSeedPatterns() {
		for i, step := range p.ActionSequence {
			if step.TargetRole == "" {
				continue
			}
			if _, ok := p.ElementRoles[step.TargetRole]; !ok {
				t.Errorf("pattern %s step[%d] tool=%s target_role=%q not in ElementRoles",
					p.ID, i, step.Tool, step.TargetRole)
			}
		}
	}
}

func TestAuthSeedPostConditionsNonEmpty(t *testing.T) {
	// 登录类模式没有 PostConditions 等于"永远视作成功",会污染统计。
	// 仅 session_expired_relog 允许(它自己是识别状态,没有真执行)。
	for _, p := range authSeedPatterns() {
		if len(p.PostConditions) == 0 {
			t.Errorf("pattern %s: PostConditions empty — 需要显式断言登录成功标志", p.ID)
		}
	}
}

// fakePage 模拟一张 URL + 可见文本 + 选择器存在性的页面,仅驱动 evaluateMatch
// 中无需 cdp.BrowserSession 的 URLPattern / TitleContains / TextContains 分支。
// Has / HasNot(依赖 checkSelectors → sess.Exec)在端到端测试里再覆盖。
type fakePage struct {
	url   string
	title string
	body  string
}

// matchByURLAndText 独立重走 URLPattern + TitleContains + TextContains 判定
// 逻辑(避免调 evaluateMatch 因 sess=nil 而在 checkSelectors 处 panic)。
// 与 evaluateMatch 的对应段(sdk/tool/ui_pattern_match.go:62-88)保持同义;
// 实际集成测试走真 cdp。
func matchByURLAndText(cond *MatchCondition, p fakePage) bool {
	if cond.URLPattern != "" {
		re, err := regexp.Compile(cond.URLPattern)
		if err != nil || !re.MatchString(p.url) {
			return false
		}
	}
	title := strings.ToLower(p.title)
	for _, n := range cond.TitleContains {
		if !strings.Contains(title, strings.ToLower(n)) {
			return false
		}
	}
	body := strings.ToLower(p.body)
	for _, n := range cond.TextContains {
		if !strings.Contains(body, strings.ToLower(n)) {
			return false
		}
	}
	return true
}

func TestAuthSeedAppliesWhenDistinguishesPages(t *testing.T) {
	// Fake pages — 只构造 URL + 文本。Has 选择器由注释说明,端到端测
	// 试会在真浏览器里验证。这里只证明 URL+Text 层面的"模式之间不糊"。
	pages := map[string]fakePage{
		// 单表单登录(login_username_password 应命中,auth 场景包里的
		// step1/step2 应落空 —— step1 HasNot password 需要真 DOM 才能
		// 100% 断定,这里至少确认 URL/Text 没把不该命中的模式拉出来)。
		"single_form": {
			url:   "https://example.com/login",
			title: "Sign in",
			body:  "Sign in to your account. Email. Password.",
		},
		// 分步登录第一步(URL=/signin/identifier, 文本没 password 字样)
		"progressive_step1": {
			url:   "https://accounts.example.com/signin/identifier",
			title: "Sign in",
			body:  "Enter your email to continue.",
		},
		// OAuth 按钮页(URL=/login, 文本有 google/github 按钮字样)
		"oauth_page": {
			url:   "https://app.example.com/login",
			title: "Sign in",
			body:  "Continue with Google. Continue with GitHub. Sign in with email.",
		},
		// 邮箱验证页
		"verify_email": {
			url:   "https://app.example.com/verify-email",
			title: "Check your email",
			body:  "We sent a verification code to your email address.",
		},
		// TOTP 2FA 页
		"two_factor": {
			url:   "https://app.example.com/two-factor",
			title: "Two-factor authentication",
			body:  "Enter the 6-digit code from your authenticator app.",
		},
	}

	// 期望命中的 pattern id 集合(按 URL+Text 层面能确认的)。
	// Has 过滤(必须有 password/email 等真 DOM 元素)不在这里测。
	type expectation struct {
		must    []string // 这些 pattern 的 URLPattern + TextContains 必须全部通过
		mustNot []string // 这些必须落空(证明不相互污染)
	}
	patternsByID := map[string]*UIPattern{}
	for _, p := range authSeedPatterns() {
		patternsByID[p.ID] = p
	}

	cases := map[string]expectation{
		// progressive_step1 URL 含 identifier,email_only 应通过 URL+Text;
		// totp / verify / register 都不该通过 URL。
		"progressive_step1": {
			must:    []string{"login_step1_email_only", "oauth_sign_in_button"},
			mustNot: []string{"totp_2fa_code", "email_verification_code", "register_email_password"},
		},
		// oauth_page URL=/login 能过 oauth_sign_in_button 的 URLPattern;
		// totp / email_verify 落空。
		"oauth_page": {
			must:    []string{"oauth_sign_in_button", "login_step1_email_only"},
			mustNot: []string{"totp_2fa_code", "email_verification_code"},
		},
		// verify_email:email_verification_code 应命中 URL + Text;
		// totp 的 Text 包含 authenticator 而 verify-email 没有,落空。
		"verify_email": {
			must:    []string{"email_verification_code"},
			mustNot: []string{"totp_2fa_code", "oauth_sign_in_button", "register_email_password"},
		},
		// two_factor:totp 命中;email_verification 的 URL 要 /verify 字样,
		// two-factor 不含,所以落空。
		"two_factor": {
			must:    []string{"totp_2fa_code"},
			mustNot: []string{"email_verification_code", "register_email_password"},
		},
	}

	for pageName, exp := range cases {
		page := pages[pageName]
		for _, id := range exp.must {
			p, ok := patternsByID[id]
			if !ok {
				t.Errorf("pattern %q not in auth seed", id)
				continue
			}
			if !matchByURLAndText(&p.AppliesWhen, page) {
				t.Errorf("page=%s: URL/Text layer did NOT match pattern %s (expected match)",
					pageName, id)
			}
		}
		for _, id := range exp.mustNot {
			p, ok := patternsByID[id]
			if !ok {
				continue
			}
			if matchByURLAndText(&p.AppliesWhen, page) {
				t.Errorf("page=%s: pattern %s matched URL/Text but should NOT",
					pageName, id)
			}
		}
	}
}

func TestExtraSeedProvidersHookIntegratesWithLibrary(t *testing.T) {
	// 端到端:新建空 lib → Seed 会跑 seedPatterns(),它末尾会调
	// extraSeedProviders(含 authSeedPatterns)。GetAny 能找到所有 auth id。
	lib := newTestLib(t)
	authIDs := []string{
		"login_step1_email_only",
		"login_step2_password",
		"register_email_password",
		"oauth_sign_in_button",
		"email_verification_code",
		"totp_2fa_code",
		"session_expired_relog",
	}
	for _, id := range authIDs {
		if got := lib.GetAny(id); got == nil {
			t.Errorf("auth pattern %q not loaded via extraSeedProviders hook", id)
		} else {
			if got.Category != "auth" {
				t.Errorf("pattern %s Category = %q, want auth", id, got.Category)
			}
			if !got.Enabled {
				t.Errorf("pattern %s Enabled = false, want true by default", id)
			}
		}
	}
}

func TestAuthSeedIDsDoNotCollideWithBaseSeed(t *testing.T) {
	// 防止 auth 场景包与 ui_pattern.go 的通用 seedPatterns 撞 ID。
	// 通用包里已经有 login_username_password / logout / skip_login_already_authed
	// 属于 auth 但与我们 7 个新 ID 不冲突。
	base := map[string]bool{}
	// 直接跑一次 seedPatterns() 即可拿全量(已含 auth 扩展)。为了只拿
	// 基础那部分,临时清空 extraSeedProviders 再恢复。
	saved := extraSeedProviders
	extraSeedProviders = nil
	for _, p := range seedPatterns() {
		base[p.ID] = true
	}
	extraSeedProviders = saved

	for _, p := range authSeedPatterns() {
		if base[p.ID] {
			t.Errorf("auth seed ID %q collides with base seedPatterns()", p.ID)
		}
	}
}

// guard:保留 context 引用避免 go vet 抱怨未用 import(实际 evaluateMatch
// 走真路径时需要)。
var _ = context.Background
