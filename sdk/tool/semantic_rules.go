package tool

import (
	"regexp"
	"strings"
)

// Static semantic rules — the 40% of browser.understand that runs in pure Go
// without touching an LLM. See sdk/docs/40-Browser-Brain语义理解架构.md §3.2.
//
// These rules handle the easy and high-confidence cases so the LLM batch
// only has to annotate the remaining ambiguous elements.

// applyStaticRules attempts to produce a SemanticEntry for the given element
// from DOM-only signals. Returns nil if no rule matched.
func applyStaticRules(el brainElement, pageURL string) *SemanticEntry {
	// Try rules in order of specificity.
	for _, rule := range staticRules {
		if entry := rule(el, pageURL); entry != nil {
			entry.Source = "rules"
			entry.Quality = "full"
			entry.Confidence = 0.95
			return entry
		}
	}
	return nil
}

type staticRule func(brainElement, string) *SemanticEntry

var staticRules = []staticRule{
	rulePasswordField,
	ruleLoginSubmitButton,
	ruleSearchInput,
	ruleAddToCartButton,
	ruleSubmitButton,
	ruleDeleteButton,
	ruleCancelButton,
	ruleLink,
	rulePaginationLink,
	ruleForgotPassword,
	ruleGenericInput,
	ruleGenericCheckbox,
	ruleGenericSelect,
	ruleLogout,
}

// Regexes for common label patterns.
var (
	rxLoginSubmit  = regexp.MustCompile(`(?i)\b(sign[\s-]*in|log[\s-]*in|login|登录|登陆|sign[\s-]*on)\b`)
	rxRegister     = regexp.MustCompile(`(?i)\b(register|sign[\s-]*up|create[\s_]*account|注册)\b`)
	rxSearch       = regexp.MustCompile(`(?i)\b(search|find|查询|搜索)\b`)
	rxAddCart      = regexp.MustCompile(`(?i)\b(add[\s-]*to[\s-]*(cart|bag|basket)|buy[\s-]*now|加入购物车|立即购买|购买)\b`)
	rxSubmit       = regexp.MustCompile(`(?i)\b(submit|send|confirm|proceed|continue|next|提交|确认|继续)\b`)
	rxDelete       = regexp.MustCompile(`(?i)\b(delete|remove|destroy|trash|erase|删除|移除)\b`)
	rxCancel       = regexp.MustCompile(`(?i)\b(cancel|close|dismiss|取消|关闭)\b`)
	rxForgot       = regexp.MustCompile(`(?i)\b(forgot[\s_]*password|reset[\s_]*password|忘记密码)\b`)
	rxLogout       = regexp.MustCompile(`(?i)\b(log[\s-]*out|sign[\s-]*out|注销|退出登录)\b`)
	rxPagination   = regexp.MustCompile(`(?i)^\s*(next|prev(?:ious)?|page|\d+|>|<|下一页|上一页)\s*$`)
)

func rulePasswordField(el brainElement, _ string) *SemanticEntry {
	if el.Tag == "input" && strings.EqualFold(el.Type, "password") {
		return &SemanticEntry{
			ActionIntent:  "Enter account password for authentication",
			Reversibility: "reversible",
			RiskLevel:     "safe",
			FlowRole:      "primary",
		}
	}
	return nil
}

func ruleLoginSubmitButton(el brainElement, _ string) *SemanticEntry {
	if el.Role != "button" && el.Tag != "button" && !(el.Tag == "input" && (el.Type == "submit" || el.Type == "button")) {
		return nil
	}
	if rxLoginSubmit.MatchString(el.Name) {
		return &SemanticEntry{
			ActionIntent:  "Submit login credentials to authenticate",
			Reversibility: "reversible",
			RiskLevel:     "safe_caution",
			FlowRole:      "primary",
		}
	}
	if rxRegister.MatchString(el.Name) {
		return &SemanticEntry{
			ActionIntent:  "Begin account registration",
			Reversibility: "reversible",
			RiskLevel:     "safe_caution",
			FlowRole:      "primary",
		}
	}
	return nil
}

func ruleSearchInput(el brainElement, _ string) *SemanticEntry {
	if el.Tag != "input" {
		return nil
	}
	if strings.EqualFold(el.Type, "search") || rxSearch.MatchString(el.Name) {
		return &SemanticEntry{
			ActionIntent:  "Enter a search query",
			Reversibility: "reversible",
			RiskLevel:     "safe",
			FlowRole:      "utility",
		}
	}
	return nil
}

func ruleAddToCartButton(el brainElement, _ string) *SemanticEntry {
	if el.Role != "button" && el.Tag != "button" {
		return nil
	}
	if rxAddCart.MatchString(el.Name) {
		return &SemanticEntry{
			ActionIntent:  "Add product to cart or initiate purchase",
			Reversibility: "semi_reversible",
			RiskLevel:     "safe_caution",
			FlowRole:      "primary",
		}
	}
	return nil
}

func ruleSubmitButton(el brainElement, _ string) *SemanticEntry {
	if el.Role != "button" && el.Tag != "button" && !(el.Tag == "input" && el.Type == "submit") {
		return nil
	}
	if rxSubmit.MatchString(el.Name) {
		return &SemanticEntry{
			ActionIntent:  "Submit the current form",
			Reversibility: "semi_reversible",
			RiskLevel:     "safe_caution",
			FlowRole:      "primary",
		}
	}
	return nil
}

func ruleDeleteButton(el brainElement, _ string) *SemanticEntry {
	if el.Role != "button" && el.Tag != "button" {
		return nil
	}
	if rxDelete.MatchString(el.Name) {
		return &SemanticEntry{
			ActionIntent:  "Delete an item — irreversible",
			Reversibility: "irreversible",
			RiskLevel:     "destructive",
			FlowRole:      "primary",
		}
	}
	return nil
}

func ruleCancelButton(el brainElement, _ string) *SemanticEntry {
	if el.Role != "button" && el.Tag != "button" && el.Tag != "a" {
		return nil
	}
	if rxCancel.MatchString(el.Name) {
		return &SemanticEntry{
			ActionIntent:  "Cancel the current action or close a dialog",
			Reversibility: "reversible",
			RiskLevel:     "safe",
			FlowRole:      "escape",
		}
	}
	return nil
}

func ruleLink(el brainElement, pageURL string) *SemanticEntry {
	if el.Tag != "a" || el.Href == "" {
		return nil
	}
	// Fragment-only or javascript: — unclear intent, skip (let LLM decide).
	if strings.HasPrefix(el.Href, "#") || strings.HasPrefix(el.Href, "javascript:") {
		return nil
	}
	// Different origin → cross_page_nav with external_effect suspicion.
	if isExternalLink(el.Href, pageURL) {
		return &SemanticEntry{
			ActionIntent:  "Navigate to external site: " + clipURL(el.Href),
			Reversibility: "reversible",
			RiskLevel:     "safe",
			FlowRole:      "cross_page_nav",
		}
	}
	// Same origin — navigation. LLM can refine if needed.
	return &SemanticEntry{
		ActionIntent:  "Navigate to: " + clipURL(el.Href),
		Reversibility: "reversible",
		RiskLevel:     "safe",
		FlowRole:      "cross_page_nav",
	}
}

func rulePaginationLink(el brainElement, _ string) *SemanticEntry {
	if el.Tag != "a" && el.Tag != "button" {
		return nil
	}
	if rxPagination.MatchString(el.Name) {
		return &SemanticEntry{
			ActionIntent:  "Navigate to another page of results",
			Reversibility: "reversible",
			RiskLevel:     "safe",
			FlowRole:      "navigation",
		}
	}
	return nil
}

func ruleForgotPassword(el brainElement, _ string) *SemanticEntry {
	if rxForgot.MatchString(el.Name) {
		return &SemanticEntry{
			ActionIntent:  "Initiate password reset flow — sends recovery email",
			Reversibility: "reversible",
			RiskLevel:     "external_effect",
			FlowRole:      "escape",
		}
	}
	return nil
}

func ruleGenericInput(el brainElement, _ string) *SemanticEntry {
	if el.Tag != "input" {
		return nil
	}
	// Handled by more specific rules above; this is fallback for generic text fields.
	switch strings.ToLower(el.Type) {
	case "", "text", "email", "tel", "number", "url":
		return &SemanticEntry{
			ActionIntent:  "Enter " + inferInputPurpose(el) + " value",
			Reversibility: "reversible",
			RiskLevel:     "safe",
			FlowRole:      "primary",
		}
	}
	return nil
}

func ruleGenericCheckbox(el brainElement, _ string) *SemanticEntry {
	if el.Tag == "input" && (el.Type == "checkbox" || el.Type == "radio") {
		return &SemanticEntry{
			ActionIntent:  "Toggle " + el.Name + " option",
			Reversibility: "reversible",
			RiskLevel:     "safe",
			FlowRole:      "secondary",
		}
	}
	if el.Role == "checkbox" || el.Role == "radio" || el.Role == "switch" {
		return &SemanticEntry{
			ActionIntent:  "Toggle " + el.Name + " option",
			Reversibility: "reversible",
			RiskLevel:     "safe",
			FlowRole:      "secondary",
		}
	}
	return nil
}

func ruleGenericSelect(el brainElement, _ string) *SemanticEntry {
	if el.Tag == "select" || el.Role == "combobox" || el.Role == "listbox" {
		return &SemanticEntry{
			ActionIntent:  "Choose an option from " + firstNonEmpty(el.Name, "dropdown"),
			Reversibility: "reversible",
			RiskLevel:     "safe",
			FlowRole:      "secondary",
		}
	}
	return nil
}

func ruleLogout(el brainElement, _ string) *SemanticEntry {
	if el.Tag != "button" && el.Role != "button" && el.Tag != "a" {
		return nil
	}
	if rxLogout.MatchString(el.Name) {
		return &SemanticEntry{
			ActionIntent:  "Log out of current session",
			Reversibility: "reversible",
			RiskLevel:     "safe_caution",
			FlowRole:      "escape",
		}
	}
	return nil
}

// inferInputPurpose returns a short descriptor from name/placeholder.
func inferInputPurpose(el brainElement) string {
	name := strings.ToLower(el.Name)
	switch {
	case strings.Contains(name, "email"), strings.Contains(name, "邮箱"):
		return "email address"
	case strings.Contains(name, "phone"), strings.Contains(name, "tel"), strings.Contains(name, "电话"):
		return "phone number"
	case strings.Contains(name, "name"), strings.Contains(name, "姓名"):
		return "name"
	case strings.Contains(name, "address"), strings.Contains(name, "地址"):
		return "address"
	case strings.Contains(name, "city"), strings.Contains(name, "城市"):
		return "city"
	case strings.Contains(name, "zip"), strings.Contains(name, "postal"):
		return "postal code"
	case strings.Contains(name, "card"), strings.Contains(name, "卡"):
		return "card"
	case strings.Contains(name, "code"), strings.Contains(name, "验证码"):
		return "verification code"
	case strings.Contains(name, "comment"), strings.Contains(name, "message"):
		return "message"
	}
	if el.Name != "" {
		return strings.ToLower(el.Name)
	}
	return "text"
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func clipURL(u string) string {
	if len(u) > 80 {
		return u[:77] + "..."
	}
	return u
}

func isExternalLink(href, pageURL string) bool {
	if !strings.HasPrefix(href, "http") {
		return false
	}
	pageOrigin := extractOrigin(pageURL)
	linkOrigin := extractOrigin(href)
	if pageOrigin == "" || linkOrigin == "" {
		return false
	}
	return pageOrigin != linkOrigin
}

func extractOrigin(u string) string {
	schemeEnd := indexString(u, "://")
	if schemeEnd < 0 {
		return ""
	}
	pathStart := indexByteFrom(u, '/', schemeEnd+3)
	if pathStart < 0 {
		return u
	}
	return u[:pathStart]
}
