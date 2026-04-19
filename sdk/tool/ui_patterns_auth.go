package tool

// P1.1 登录/注册/验证场景包 —— Browser Brain 第二批种子模式。
//
// 设计原则:
//   - 复用 UIPattern / MatchCondition / ElementDescriptor / PostCondition
//     已有结构,不引入任何新类型。
//   - OnAnomaly 词汇表对齐 sdk/tool/builtin_browser_anomaly.go 的常量:
//       session_expired / captcha(subtype: recaptcha/hcaptcha/
//       cloudflare_interstitial/geetest/datadome)/ ui_injection /
//       error_message。拼写一致,ExecutePattern 的 matchAnomalyHandler
//       才能命中。
//   - 每个模式都配了 captcha → human_intervention 与
//     error_message → abort(防止密码错误触发暴力重试锁号)。
//   - OAuth / email-verify / TOTP 场景不把具体第三方 provider 的邮件
//     内容或 TOTP seed 写死,占位符走 $credentials.* / $otp.code,由
//     Agent 侧的运行时变量解析。
//
// 扩展机制:本文件用包级 extraSeedProviders 注册;ui_pattern.go
// 的 seedPatterns() 末尾会调用所有 providers 追加结果。这样 P1.2+
// 其他场景包也能走同样的挂接点,彼此不改对方文件。

// extraSeedProviders 是跨场景包种子扩展点。任何场景包 init() 里往
// 这里注册一个返回 []*UIPattern 的函数即可自动被 seedPatterns 吸收。
// 不暴露 setter:各场景包靠 init() 并发安全地追加(Go 单线程 init)。
var extraSeedProviders []func() []*UIPattern

func init() {
	extraSeedProviders = append(extraSeedProviders, authSeedPatterns)
}

// authSeedPatterns 返回登录/注册/验证场景的 6 个种子模式:
//
//  1. login_step1_email_only       —— 分步登录第一步(email → 下一页)
//  2. login_step2_password         —— 分步登录第二步(password 页)
//  3. register_email_password      —— 注册(email + password + confirm)
//  4. oauth_sign_in_button         —— OAuth(Google/GitHub/Apple)按钮点击
//  5. email_verification_code      —— 邮箱验证码回填
//  6. totp_2fa_code                —— TOTP / 2FA 6 位数字输入
//
// 种子里不包含标准单表单 username+password(已在 seedPatterns 里
// 的 login_username_password 覆盖)。
func authSeedPatterns() []*UIPattern {
	return []*UIPattern{
		loginStep1EmailOnlyPattern(),
		loginStep2PasswordPattern(),
		registerEmailPasswordPattern(),
		oauthSignInButtonPattern(),
		emailVerificationCodePattern(),
		totpTwoFactorPattern(),
		sessionExpiredRelogPattern(),
	}
}

// ---------------------------------------------------------------------------
// 1. 分步登录——第一步(只要 email / username,按下一步跳转)
// ---------------------------------------------------------------------------
// 代表站点:Google / Microsoft / Apple(新版)/ Notion 等 progressive
// auth。AppliesWhen 要求页上有 email/username 输入框但 *没有*
// password 框,避免与 login_username_password 重叠。

func loginStep1EmailOnlyPattern() *UIPattern {
	return &UIPattern{
		ID:          "login_step1_email_only",
		Category:    "auth",
		Source:      "seed",
		Description: "Progressive auth step 1 — enter email/username, advance to password page",
		AppliesWhen: MatchCondition{
			URLPattern: `(?i)/(login|signin|sign-in|auth|identifier|account)\b`,
			Has: []string{
				`input[type="email"], input[name*="email" i], input[name*="user" i], input[name="identifier"]`,
				`button[type="submit"], button[aria-label*="next" i], input[type="submit"]`,
			},
			HasNot: []string{`input[type="password"]`},
		},
		ElementRoles: map[string]ElementDescriptor{
			"email_field": {
				Tag:  "input",
				Name: "~(?i)(email|user|account|identifier|手机|邮箱)",
				CSS:  `input[type="email"], input[name*="email" i], input[name*="user" i], input[name="identifier"], input[type="text"]`,
			},
			"next_button": {
				Role: "button",
				Name: "~(?i)(next|continue|下一步|继续)",
				CSS:  `button[type="submit"], input[type="submit"], button[aria-label*="next" i]`,
			},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.type", TargetRole: "email_field", Params: map[string]interface{}{"text": "$credentials.email", "clear": true}},
			{Tool: "browser.click", TargetRole: "next_button"},
			{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 8000}},
		},
		PostConditions: []PostCondition{
			{Type: "any_of", Any: []PostCondition{
				{Type: "dom_contains", Selector: `input[type="password"]`},
				{Type: "url_matches", URLPattern: `(?i)/(password|challenge|pwd|verify)\b`},
				{Type: "url_changed"},
			}},
		},
		OnAnomaly: map[string]AnomalyHandler{
			// step1 找不到账号通常是 error_message;不要重试,让 Agent 决定换账号。
			"error_message": {Action: "abort", Reason: "Account not found or disabled — do not retry with same identifier"},
			// step1 也可能被 captcha 拦住(Google 经常这么干)。
			"captcha":         {Action: "human_intervention", Reason: "Provider issued a CAPTCHA on identifier step"},
			"recaptcha":       {Action: "human_intervention"},
			"hcaptcha":        {Action: "human_intervention"},
			"cloudflare_interstitial": {Action: "human_intervention"},
			// step1 很少遇到 session_expired,但若遇到就重试一次。
			"session_expired": {Action: "retry", MaxRetries: 1, BackoffMS: 500},
			// UI 注入(钓鱼变体)一律 abort,防止把凭证灌给伪页。
			"ui_injection": {Action: "abort", Reason: "Suspicious UI injection on login step 1 — refuse to submit credentials"},
		},
	}
}

// ---------------------------------------------------------------------------
// 2. 分步登录——第二步(password 页)
// ---------------------------------------------------------------------------
// 接 step1:页面 URL 已切到 password/challenge,或出现 password 框。
// 带 "记住我" 的勾选可选(Optional step)。

func loginStep2PasswordPattern() *UIPattern {
	return &UIPattern{
		ID:          "login_step2_password",
		Category:    "auth",
		Source:      "seed",
		Description: "Progressive auth step 2 — enter password after identifier accepted (with optional remember-me)",
		AppliesWhen: MatchCondition{
			URLPattern: `(?i)/(password|challenge|pwd|verify|signin|login)\b`,
			Has:        []string{`input[type="password"]`},
		},
		ElementRoles: map[string]ElementDescriptor{
			"password_field": {
				Tag:  "input",
				Type: "password",
				CSS:  `input[type="password"]`,
			},
			"remember_me": {
				Tag:  "input",
				Type: "checkbox",
				Name: "~(?i)(remember|keep.*signed|stay|记住)",
				CSS:  `input[type="checkbox"][name*="remember" i], input[type="checkbox"][id*="remember" i]`,
			},
			"submit_button": {
				Role: "button",
				Name: "~(?i)(sign\\s*in|log\\s*in|login|submit|continue|next|登录|登陆|继续)",
				CSS:  `button[type="submit"], input[type="submit"]`,
			},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.type", TargetRole: "password_field", Params: map[string]interface{}{"text": "$credentials.password", "clear": true}},
			// 记住我勾选;页面没这个 checkbox 时 Optional=true 跳过,不算失败。
			{Tool: "browser.click", TargetRole: "remember_me", Optional: true},
			{Tool: "browser.click", TargetRole: "submit_button"},
			{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 10000}},
		},
		PostConditions: []PostCondition{
			{Type: "any_of", Any: []PostCondition{
				{Type: "url_changed"},
				{Type: "dom_contains", Selector: `[data-user-profile], .user-menu, a[href*="logout" i], [aria-label*="account" i]`},
				{Type: "cookie_set", CookieName: "session"},
				{Type: "cookie_set", CookieName: "sessionid"},
			}},
		},
		OnAnomaly: map[string]AnomalyHandler{
			// 密码错的 error_message / wrong_password 一律 abort:再试一次就锁号。
			"error_message":  {Action: "abort", Reason: "Wrong password — aborting to avoid account lockout"},
			"wrong_password": {Action: "abort", Reason: "Wrong password — aborting to avoid account lockout"},
			// captcha 需要人工。
			"captcha":                 {Action: "human_intervention"},
			"recaptcha":               {Action: "human_intervention"},
			"hcaptcha":                {Action: "human_intervention"},
			"cloudflare_interstitial": {Action: "human_intervention"},
			// 点提交时会话已过期 → 回到 login_step1_email_only 重走。
			"session_expired": {Action: "fallback_pattern", FallbackID: "login_step1_email_only", Reason: "Session expired during password step — restart flow"},
			// 页面被 UI 注入 → 绝不提交密码。
			"ui_injection": {Action: "abort", Reason: "Suspicious UI injection on password page — refuse to submit credentials"},
		},
	}
}

// ---------------------------------------------------------------------------
// 3. 注册 —— email + password + confirm
// ---------------------------------------------------------------------------
// 最常见的注册表单:邮箱、密码、确认密码(可选)、同意条款 checkbox
// (可选)。不覆盖三方 OAuth 注册(那等价于 oauth_sign_in_button)。

func registerEmailPasswordPattern() *UIPattern {
	return &UIPattern{
		ID:          "register_email_password",
		Category:    "auth",
		Source:      "seed",
		Description: "Account registration with email + password (+ optional confirm + optional ToS checkbox)",
		AppliesWhen: MatchCondition{
			URLPattern: `(?i)/(register|signup|sign-up|join|create.*account)\b`,
			Has: []string{
				`input[type="email"], input[name*="email" i]`,
				`input[type="password"]`,
			},
		},
		ElementRoles: map[string]ElementDescriptor{
			"email_field": {
				Tag: "input",
				CSS: `input[type="email"], input[name*="email" i]`,
			},
			"password_field": {
				Tag:  "input",
				Type: "password",
				// 两个 password 框时取第一个。
				CSS: `input[type="password"]:not([name*="confirm" i]):not([name*="repeat" i]):not([id*="confirm" i]), input[type="password"]`,
			},
			"confirm_field": {
				Tag:  "input",
				Type: "password",
				CSS:  `input[type="password"][name*="confirm" i], input[type="password"][name*="repeat" i], input[type="password"][id*="confirm" i]`,
			},
			"tos_checkbox": {
				Tag:  "input",
				Type: "checkbox",
				Name: "~(?i)(agree|accept|terms|同意|接受)",
				CSS:  `input[type="checkbox"][name*="terms" i], input[type="checkbox"][name*="agree" i], input[type="checkbox"][id*="terms" i]`,
			},
			"submit_button": {
				Role: "button",
				Name: "~(?i)(sign\\s*up|register|create|join|注册|创建)",
				CSS:  `button[type="submit"], input[type="submit"]`,
			},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.type", TargetRole: "email_field", Params: map[string]interface{}{"text": "$credentials.email", "clear": true}},
			{Tool: "browser.type", TargetRole: "password_field", Params: map[string]interface{}{"text": "$credentials.password", "clear": true}},
			// confirm / tos 不是所有站点都有,缺失时不算失败。
			{Tool: "browser.type", TargetRole: "confirm_field", Params: map[string]interface{}{"text": "$credentials.password", "clear": true}, Optional: true},
			{Tool: "browser.click", TargetRole: "tos_checkbox", Optional: true},
			{Tool: "browser.click", TargetRole: "submit_button"},
			{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 12000}},
		},
		PostConditions: []PostCondition{
			{Type: "any_of", Any: []PostCondition{
				{Type: "url_matches", URLPattern: `(?i)/(welcome|dashboard|verify|onboarding|email.*sent)\b`},
				{Type: "dom_contains", Selector: `.success, [role="alert"][class*="success" i], [data-testid*="verify" i]`},
				{Type: "url_changed"},
			}},
		},
		OnAnomaly: map[string]AnomalyHandler{
			// 注册错误(邮箱已存在、密码太弱)不能重试——语义不变再试只会再报错。
			"error_message": {Action: "abort", Reason: "Registration validation failed — review fields before retrying"},
			// Captcha 人工介入。
			"captcha":                 {Action: "human_intervention"},
			"recaptcha":               {Action: "human_intervention"},
			"hcaptcha":                {Action: "human_intervention"},
			"cloudflare_interstitial": {Action: "human_intervention"},
			// 注册页被注入也 abort。
			"ui_injection": {Action: "abort", Reason: "Suspicious UI injection on registration page"},
		},
	}
}

// ---------------------------------------------------------------------------
// 4. OAuth 登录按钮(Google / GitHub / Apple)
// ---------------------------------------------------------------------------
// 点击后本页不会自己完成登录——它跳到 provider 的授权页。本模式的职责
// 只是"找到并点对 provider 按钮",PostConditions 只要求 URL 离开本
// 站或 popup 打开。后续 provider 登录交给对应 provider 的场景包
// (通常是 login_step1_email_only + login_step2_password 的组合)。

func oauthSignInButtonPattern() *UIPattern {
	return &UIPattern{
		ID:          "oauth_sign_in_button",
		Category:    "auth",
		Source:      "seed",
		Description: "Locate and click OAuth provider button (Google / GitHub / Apple). Follow-up flow is provider-specific.",
		AppliesWhen: MatchCondition{
			URLPattern: `(?i)/(login|signin|sign-in|auth|register|signup)\b`,
			// 至少要有一个明显的 OAuth 按钮。HasNot 不限,登录页常同时有
			// 表单和 OAuth 按钮。
			Has: []string{
				`[aria-label*="google" i], [aria-label*="github" i], [aria-label*="apple" i], button[class*="google" i], button[class*="github" i], button[class*="apple" i], a[href*="/oauth" i], a[href*="accounts.google.com"], a[href*="github.com/login"], a[href*="appleid.apple.com"]`,
			},
		},
		ElementRoles: map[string]ElementDescriptor{
			// $oauth.provider 在 Agent 侧是 "google" / "github" / "apple"。
			// 因为 ElementDescriptor 不支持占位符,这里给三条 Fallback
			// 覆盖 —— ResolveElement 按顺序尝试直到命中其一。
			"oauth_button": {
				Role: "button",
				Name: "~(?i)(continue|sign.*in|log.*in|login|使用).*(google|github|apple)",
				CSS:  `button[aria-label*="google" i], button[class*="google" i], a[href*="accounts.google.com"]`,
				Fallback: []ElementDescriptor{
					{
						Role: "button",
						Name: "~(?i)(continue|sign.*in|log.*in|login|使用).*github",
						CSS:  `button[aria-label*="github" i], button[class*="github" i], a[href*="github.com/login"]`,
					},
					{
						Role: "button",
						Name: "~(?i)(continue|sign.*in|log.*in|login|使用).*apple",
						CSS:  `button[aria-label*="apple" i], button[class*="apple" i], a[href*="appleid.apple.com"]`,
					},
					// 最后兜底:任意 OAuth href。
					{
						CSS: `a[href*="/oauth" i], a[href*="/sso" i]`,
					},
				},
			},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.click", TargetRole: "oauth_button"},
			// OAuth 通常跳 provider,wait 不是 network_idle 而是 URL 变化。
			// 这里用 network_idle 短超时 + PostCondition 兜底。
			{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 6000}, Optional: true},
		},
		PostConditions: []PostCondition{
			{Type: "any_of", Any: []PostCondition{
				// 跳到 Google
				{Type: "url_matches", URLPattern: `accounts\.google\.com|oauth2/auth`},
				// 跳到 GitHub
				{Type: "url_matches", URLPattern: `github\.com/login|github\.com/login/oauth`},
				// 跳到 Apple
				{Type: "url_matches", URLPattern: `appleid\.apple\.com/auth`},
				// 本站已登录(某些站 OAuth 已授权会直接刷新本页)
				{Type: "dom_contains", Selector: `[data-user-profile], .user-menu, a[href*="logout" i]`},
				{Type: "cookie_set", CookieName: "session"},
			}},
		},
		OnAnomaly: map[string]AnomalyHandler{
			// 点 OAuth 按钮通常不会遇密码错;如果 provider 侧返回 error,
			// 交人工(可能是撤权 / scope 拒绝)。
			"error_message":           {Action: "human_intervention", Reason: "OAuth provider returned error — likely scope denial or consent withdrawal"},
			"captcha":                 {Action: "human_intervention"},
			"recaptcha":               {Action: "human_intervention"},
			"hcaptcha":                {Action: "human_intervention"},
			"cloudflare_interstitial": {Action: "human_intervention"},
			"ui_injection":            {Action: "abort", Reason: "Suspicious UI injection — refuse to click OAuth button"},
		},
	}
}

// ---------------------------------------------------------------------------
// 5. 邮箱验证码回填
// ---------------------------------------------------------------------------
// "我们给你邮箱发了一个 6 位验证码,请输入以验证" 这一步。
// 输入框常见形态:单个 input(长度 6) 或 6 个分离 input([data-index])。
// 这里只覆盖"单 input"和"语义 name=otp/code"两种。6-digit-split UI
// 推迟到独立模式或学习层处理。
//
// $otp.code 由 Agent 侧从邮件抓取或人工输入后注入变量。

func emailVerificationCodePattern() *UIPattern {
	return &UIPattern{
		ID:          "email_verification_code",
		Category:    "auth",
		Source:      "seed",
		Description: "Enter email verification code after signup or sensitive action",
		AppliesWhen: MatchCondition{
			URLPattern: `(?i)/(verify|verification|confirm|check.*email|activate)\b`,
			Has: []string{
				`input[type="text"], input[type="number"], input[type="tel"], input[inputmode="numeric"]`,
			},
			// TextContains 是 AND 语义 —— 只放最能判定"这是验证码页"的一个词。
			// 同义词(verify / verification / 验证码)由 URLPattern 承担。
			TextContains: []string{"code"},
		},
		ElementRoles: map[string]ElementDescriptor{
			"code_field": {
				Tag:  "input",
				Name: "~(?i)(code|otp|verify|verification|验证码)",
				CSS:  `input[name*="code" i], input[name*="otp" i], input[id*="code" i], input[autocomplete="one-time-code"], input[inputmode="numeric"][maxlength="6"]`,
				Fallback: []ElementDescriptor{
					// 某些站用 type=tel + maxlength=6。
					{Tag: "input", Type: "tel", CSS: `input[type="tel"][maxlength="6"]`},
					// 最后兜底:任意 6-长度数字输入。
					{Tag: "input", CSS: `input[maxlength="6"][type!="hidden"]`},
				},
			},
			"submit_button": {
				Role: "button",
				Name: "~(?i)(verify|confirm|submit|continue|验证|确认|提交)",
				CSS:  `button[type="submit"], input[type="submit"]`,
			},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.type", TargetRole: "code_field", Params: map[string]interface{}{"text": "$otp.code", "clear": true}},
			{Tool: "browser.click", TargetRole: "submit_button"},
			{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 10000}},
		},
		PostConditions: []PostCondition{
			{Type: "any_of", Any: []PostCondition{
				{Type: "url_matches", URLPattern: `(?i)/(welcome|dashboard|success|home)\b`},
				{Type: "dom_contains", Selector: `.success, [role="alert"][class*="success" i]`},
				{Type: "url_changed"},
				{Type: "cookie_set", CookieName: "session"},
			}},
		},
		OnAnomaly: map[string]AnomalyHandler{
			// 验证码错 / 过期 —— 不要重试同一个 code,交回 Agent 去抓新的。
			"error_message": {Action: "abort", Reason: "Verification code invalid or expired — do not retry with stale code"},
			// 过期可以请求新的 code + 重试一次。
			"session_expired": {Action: "retry", MaxRetries: 1, BackoffMS: 1000},
			// captcha / injection 处理同其他场景。
			"captcha":                 {Action: "human_intervention"},
			"recaptcha":               {Action: "human_intervention"},
			"hcaptcha":                {Action: "human_intervention"},
			"cloudflare_interstitial": {Action: "human_intervention"},
			"ui_injection":            {Action: "abort", Reason: "Suspicious UI injection on verification page"},
		},
	}
}

// ---------------------------------------------------------------------------
// 6. TOTP / 2FA 6 位数字输入
// ---------------------------------------------------------------------------
// 特征:页面已出现 password 登录成功后,跳到"two-factor" / "2FA" /
// "authenticator" 页,一个 6 位数字输入框。
//
// 与 email_verification_code 的区别:
//   - URL/文本包含 "two-factor" / "authenticator" / "2fa" / "totp"
//   - $otp.code 由 TOTP 算法生成(authy/1password)或人工输入
//   - AppliesWhen 的 TextContains 区分开,避免 pattern_match 冲突

func totpTwoFactorPattern() *UIPattern {
	return &UIPattern{
		ID:          "totp_2fa_code",
		Category:    "auth",
		Source:      "seed",
		Description: "Enter 6-digit TOTP/2FA code from authenticator app (Google Authenticator / Authy / 1Password)",
		AppliesWhen: MatchCondition{
			URLPattern: `(?i)/(two.?factor|2fa|mfa|totp|authenticator|challenge)\b`,
			Has: []string{
				`input[type="text"], input[type="number"], input[type="tel"], input[inputmode="numeric"]`,
			},
			// AND 语义:只保留最能区别于 email_verification_code 的一个词。
			// URL 已含 two-factor/totp/2fa,text 只需锁定 authenticator。
			TextContains: []string{"authenticator"},
		},
		ElementRoles: map[string]ElementDescriptor{
			"totp_field": {
				Tag:  "input",
				Name: "~(?i)(code|otp|token|authenticator|验证码|代码)",
				CSS:  `input[autocomplete="one-time-code"], input[inputmode="numeric"][maxlength="6"], input[name*="totp" i], input[name*="otp" i], input[name*="code" i]`,
				Fallback: []ElementDescriptor{
					{Tag: "input", Type: "tel", CSS: `input[type="tel"][maxlength="6"]`},
					{Tag: "input", CSS: `input[maxlength="6"][type!="hidden"]`},
				},
			},
			"trust_device": {
				Tag:  "input",
				Type: "checkbox",
				Name: "~(?i)(trust|remember|don'?t.*ask|记住此设备|信任)",
			},
			"submit_button": {
				Role: "button",
				Name: "~(?i)(verify|confirm|submit|continue|验证|确认)",
				CSS:  `button[type="submit"], input[type="submit"]`,
			},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.type", TargetRole: "totp_field", Params: map[string]interface{}{"text": "$otp.code", "clear": true}},
			{Tool: "browser.click", TargetRole: "trust_device", Optional: true},
			{Tool: "browser.click", TargetRole: "submit_button"},
			{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 10000}},
		},
		PostConditions: []PostCondition{
			{Type: "any_of", Any: []PostCondition{
				{Type: "url_matches", URLPattern: `(?i)/(dashboard|home|welcome|account)\b`},
				{Type: "dom_contains", Selector: `[data-user-profile], .user-menu, a[href*="logout" i]`},
				{Type: "cookie_set", CookieName: "session"},
				{Type: "url_changed"},
			}},
		},
		OnAnomaly: map[string]AnomalyHandler{
			// TOTP 错误:时间漂移?不要重试同一 code,让 Agent 用下一个时间窗的 code。
			"error_message":   {Action: "abort", Reason: "TOTP code rejected — clock skew or stale code; caller should regenerate"},
			"session_expired": {Action: "fallback_pattern", FallbackID: "login_step1_email_only", Reason: "2FA session expired — restart login"},
			// captcha 在 2FA 很少见但可能(异地登录警惕)。
			"captcha":                 {Action: "human_intervention"},
			"recaptcha":               {Action: "human_intervention"},
			"hcaptcha":                {Action: "human_intervention"},
			"cloudflare_interstitial": {Action: "human_intervention"},
			"ui_injection":            {Action: "abort", Reason: "Suspicious UI injection on 2FA page — refuse to submit code"},
		},
	}
}

// ---------------------------------------------------------------------------
// 7. 会话过期重登(配合 M4 UI injection + anomaly v2 session_expired)
// ---------------------------------------------------------------------------
// 场景:Agent 在操作某个已登录页时,anomaly 层报 session_expired
// (URL 被重定向到 /login、密码框重新出现、401 响应…)。本 pattern
// 的 ActionSequence 为空(不点任何东西),仅靠 OnAnomaly 的
// fallback_pattern 指到 login_step1_email_only / login_username_password
// 来引导 ExecutePattern 切链。AppliesWhen 故意配得严,只在页面真的回到
// 登录态时被 MatchPatterns 选中。
//
// PostConditions 检查"已经重登成功":有 user-profile / logout 链接
// 且 password 框消失。

func sessionExpiredRelogPattern() *UIPattern {
	return &UIPattern{
		ID:          "session_expired_relog",
		Category:    "auth",
		Source:      "seed",
		Description: "Detect forced-to-login state (session expired mid-task) and route to a login pattern",
		AppliesWhen: MatchCondition{
			URLPattern:   `(?i)/(login|signin|sign-in|auth|session.*expired)\b`,
			Has:          []string{`input[type="password"], input[type="email"], input[name*="user" i]`},
			// AND 语义:留单个最窄的词"expired"配合 URLPattern 收敛,
			// 避免把普通登录页也吞掉。
			TextContains: []string{"expired"},
		},
		ElementRoles: map[string]ElementDescriptor{},
		// 空 ActionSequence:本 pattern 自己不点任何按钮,只靠 fallback_pattern 切走。
		// 但 ExecutePattern 的 fallback_pattern 必须由 anomaly 触发 —— 所以
		// 真正的使用路径是:Agent 在别处遇 session_expired anomaly 时,该其他
		// pattern 的 OnAnomaly 把 fallback_pattern 指向 "login_step1_email_only"
		// 或 "login_username_password"。本 pattern 的价值是给 MatchPatterns
		// 一个可识别的 "session expired" 名字,让 Agent 明确看到"当前页=被踢登"。
		ActionSequence: []ActionStep{},
		PostConditions: []PostCondition{
			// 没 ActionSequence → PostCondition 在刚进来的页面上评估。
			// 要求还在登录页 —— 即"识别到 session expired"视为匹配成功。
			{Type: "dom_contains", Selector: `input[type="password"], input[type="email"]`},
		},
		OnAnomaly: map[string]AnomalyHandler{
			// 若本 pattern 被选中执行且遇 session_expired anomaly(unlikely,
			// 它自己 ActionSequence 为空),仍路由到登录模式。
			"session_expired":         {Action: "fallback_pattern", FallbackID: "login_step1_email_only"},
			"captcha":                 {Action: "human_intervention"},
			"recaptcha":               {Action: "human_intervention"},
			"hcaptcha":                {Action: "human_intervention"},
			"cloudflare_interstitial": {Action: "human_intervention"},
			"ui_injection":            {Action: "abort", Reason: "Suspicious UI injection on re-login page"},
			// 理论上本模式 ActionSequence 为空不会触发 error_message,
			// 仍给一条 abort 兜底,便于 OnAnomaly 完整性校验与 Agent 诊断。
			"error_message": {Action: "abort", Reason: "Re-login recognizer surfaced an error — escalate to Agent"},
		},
	}
}
