package tool

import (
	"context"
	"fmt"
)

// 挂接 ui_patterns_auth.go 定义的 extraSeedProviders:空库首次 Seed 时电商
// 种子自动并入 seedPatterns 产出,不需要调用方再显式 SeedEcommerce。
// 沿用 dev-auth 建立的跨场景包扩展点,不另造接线。
func init() {
	extraSeedProviders = append(extraSeedProviders, EcommerceSeedPatterns)
}

// 电商购物流程场景包(P1.2)。
//
// 本文件只定义 UIPattern 实例并暴露 EcommerceSeedPatterns() / SeedEcommerce()
// 两个入口;底层 UIPattern / MatchCondition / ElementDescriptor / ActionStep /
// PostCondition / AnomalyHandler / PatternLibrary 全部复用 ui_pattern.go 已
// 沉淀的类型,不新增并列接口(遵循 brain-v3 "复用优先" 铁律)。
//
// 覆盖流程(对应任务 #2):
//   1. 商品列表 → 详情页(ecommerce_browse_product_list)
//   2. 详情页图集切换(ecommerce_product_detail_gallery)
//   3. 加入购物车 + toast / mini-cart / cart-redirect 三种反馈
//      (ecommerce_add_to_cart_with_feedback)
//   4. 去结算(ecommerce_proceed_to_checkout)
//   5. 填写收货地址 + 地址自动补全检测(ecommerce_fill_shipping_address)
//   6. 下单停在支付网关前(ecommerce_place_order_to_payment_gateway)
//   7. 售罄时降级到"找相似商品"(ecommerce_find_similar_product,作为 #3
//      的 fallback_pattern 目标)
//
// OnAnomaly 约定:
//   - "sold_out" / "out_of_stock"(error_alert 子类型)→ fallback_pattern
//     切到 ecommerce_find_similar_product
//   - "promotion_modal"(modal_blocking 子类型)→ abort,避免误点促销按
//     钮触发下单/优惠券绑定等副作用
//   - "ui_injection"(M4 UI injection 检测路由到这里)→ abort,不在电商
//     场景内自动点击被注入的 CTA

// EcommerceSeedPatterns 返回 P1.2 沉淀的电商场景种子模式集合。
// 上游(cmd/brain 启动 / 第三方专精大脑种子注入)可以把这份列表与
// seedPatterns() 合并后一次 Upsert,避免单独一套管线。
func EcommerceSeedPatterns() []*UIPattern {
	return []*UIPattern{
		ecommercePatternBrowseProductList(),
		ecommercePatternProductDetailGallery(),
		ecommercePatternAddToCartWithFeedback(),
		ecommercePatternProceedToCheckout(),
		ecommercePatternFillShippingAddress(),
		ecommercePatternPlaceOrderToPaymentGateway(),
		ecommercePatternFindSimilarProduct(),
	}
}

// SeedEcommerce 把 EcommerceSeedPatterns 全部 Upsert 进 lib。
// 幂等:重复调用只会把 body/enabled 覆写,stats 不会被重置(Upsert 的
// SQL 在 ON CONFLICT 分支里也没更新 stats 列,保留已有统计)。
func SeedEcommerce(ctx context.Context, lib *PatternLibrary) error {
	if lib == nil {
		return fmt.Errorf("nil pattern library")
	}
	for _, p := range EcommerceSeedPatterns() {
		if err := lib.Upsert(ctx, p); err != nil {
			return fmt.Errorf("seed ecommerce %s: %w", p.ID, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 1. 商品列表 → 详情页
// ---------------------------------------------------------------------------

func ecommercePatternBrowseProductList() *UIPattern {
	return &UIPattern{
		ID:          "ecommerce_browse_product_list",
		Category:    "commerce",
		Source:      "seed",
		Description: "From a product listing page, click a product card to open its detail page",
		AppliesWhen: MatchCondition{
			// 兼容两种路由形态:
			//   - 路径式 /category/ /catalog/ /products/ /shop/ /collection/
			//   - 查询式 ?route=product/category 或 ?route=product/products(OpenCart 等)
			URLPattern: `(?i)(/(category|categories|catalog|products?|shop|collection)(/|\?|$)|route=product/(category|products?))`,
			Has: []string{
				`[class*="product" i], [itemtype*="Product" i], [data-product-id], .product-card, .product-tile`,
			},
		},
		ElementRoles: map[string]ElementDescriptor{
			"product_card_link": {
				Role: "link",
				CSS:  `a[href*="/product"], a[href*="/p/"], .product-card a, .product-tile a, [data-product-id] a`,
				Fallback: []ElementDescriptor{
					{Tag: "a", Name: "~(?i)(view|details|查看|详情)"},
				},
			},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.click", TargetRole: "product_card_link"},
			{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 8000}},
		},
		PostConditions: []PostCondition{
			{Type: "any_of", Any: []PostCondition{
				{Type: "url_matches", URLPattern: `(?i)/(product|p|item|dp)/`},
				{Type: "dom_contains", Selector: `[itemtype*="Product" i], [class*="product-detail" i], [class*="pdp" i]`},
			}},
		},
		OnAnomaly: map[string]AnomalyHandler{
			"ui_injection": {
				Action: "abort",
				Reason: "UI injection detected on listing (likely promo CTA) — do not click",
			},
		},
	}
}

// ---------------------------------------------------------------------------
// 2. 详情页图集切换
// ---------------------------------------------------------------------------

func ecommercePatternProductDetailGallery() *UIPattern {
	return &UIPattern{
		ID:          "ecommerce_product_detail_gallery",
		Category:    "commerce",
		Source:      "seed",
		Description: "Switch product gallery thumbnail on a detail page (validates gallery widget wiring)",
		AppliesWhen: MatchCondition{
			// 兼容路径式 /product/ 和查询式 ?route=product/product(OpenCart)
			URLPattern: `(?i)(/(products?|p|item|dp|goods)/|route=product/product)`,
			Has: []string{
				`[class*="gallery" i] img, [class*="thumb" i], [data-gallery-thumb], .product-images, .pdp-gallery`,
			},
		},
		ElementRoles: map[string]ElementDescriptor{
			"gallery_thumbnail": {
				CSS: `[class*="gallery" i] [class*="thumb" i], [data-gallery-thumb], .thumbs img, .product-images li img`,
				Fallback: []ElementDescriptor{
					{Role: "button", Name: "~(?i)(image|photo|图片)"},
				},
			},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.click", TargetRole: "gallery_thumbnail"},
		},
		PostConditions: []PostCondition{
			{Type: "dom_contains", Selector: `[class*="gallery" i] img, [class*="zoom" i], [class*="main-image" i]`},
		},
	}
}

// ---------------------------------------------------------------------------
// 3. 加入购物车(三种反馈)
// ---------------------------------------------------------------------------

func ecommercePatternAddToCartWithFeedback() *UIPattern {
	return &UIPattern{
		ID:          "ecommerce_add_to_cart_with_feedback",
		Category:    "commerce",
		Source:      "seed",
		Description: "Add current product to cart; accepts toast / mini-cart / cart-redirect as success feedback",
		AppliesWhen: MatchCondition{
			// 兼容路径式与 OpenCart 查询式商品页 URL。
			URLPattern: `(?i)(/(products?|p|item|dp|goods)/|route=product/product)`,
			Has: []string{
				`[class*="product" i], [itemtype*="Product" i]`,
				`button, [role="button"]`,
			},
			// 注意:MatchCondition.TextContains 是 AND 语义(见 evaluateMatch),
			// 不能把多个候选短语都塞进去。URL + Has 已经足够筛出商品详情页,
			// 所以这里省略 TextContains,让匹配更宽松。英文/中文站都能命中。
		},
		ElementRoles: map[string]ElementDescriptor{
			"add_to_cart_button": {
				Role: "button",
				Name: "~(?i)(add\\s*to\\s*(cart|bag|basket)|加入购物车|立即购买|buy\\s*now)",
				CSS:  `button[name*="cart" i], button[id*="cart" i], button[data-add-to-cart], .add-to-cart`,
				Fallback: []ElementDescriptor{
					{Role: "button", Name: "~(?i)(add|买|购)"},
				},
			},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.click", TargetRole: "add_to_cart_button"},
			{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 6000}},
		},
		PostConditions: []PostCondition{
			// 三种 add-to-cart 反馈任一命中即算成功:
			//   - 显式 toast / 通知("added to cart"、"已加入购物车")
			//   - mini cart / cart drawer 里出现商品项或数量 +1
			//   - 直接跳转到 /cart
			{Type: "any_of", Any: []PostCondition{
				{Type: "dom_contains", Selector: `[role="status"], [role="alert"], .toast, .notification, .added-to-cart-message`},
				{Type: "dom_contains", Selector: `.mini-cart, .cart-drawer, [data-cart-drawer], [class*="cart-count" i], [data-cart-count]`},
				{Type: "url_matches", URLPattern: `(?i)/cart(/|\?|$)`},
			}},
		},
		OnAnomaly: map[string]AnomalyHandler{
			// 售罄:error_alert 的 subtype=sold_out / out_of_stock → 切"找相似商品"
			"sold_out": {
				Action:     "fallback_pattern",
				FallbackID: "ecommerce_find_similar_product",
				Reason:     "Product sold out; search for a similar SKU instead",
			},
			"out_of_stock": {
				Action:     "fallback_pattern",
				FallbackID: "ecommerce_find_similar_product",
				Reason:     "Out of stock; fallback to similar-product search",
			},
			// error_alert 兜底(无明确 subtype):保守 abort,让 Agent 决定
			"error_alert": {
				Action: "abort",
				Reason: "Add-to-cart error alert; require agent decision before retry",
			},
			// 促销 modal(非信息性,可能诱导下错单)→ abort
			"promotion_modal": {
				Action: "abort",
				Reason: "Promotion modal detected — avoid unintended order placement",
			},
			"modal_blocking": {
				Action: "abort",
				Reason: "Blocking modal (likely promo) before add-to-cart; abort to be safe",
			},
			// M4 UI injection:电商页促销 CTA 注入常见,拒绝点击
			"ui_injection": {
				Action: "abort",
				Reason: "UI injection detected (likely injected promo CTA) — refuse click",
			},
		},
	}
}

// ---------------------------------------------------------------------------
// 4. 去结算(cart → checkout)
// ---------------------------------------------------------------------------

func ecommercePatternProceedToCheckout() *UIPattern {
	return &UIPattern{
		ID:          "ecommerce_proceed_to_checkout",
		Category:    "commerce",
		Source:      "seed",
		Description: "From the cart page, proceed to checkout flow",
		AppliesWhen: MatchCondition{
			URLPattern: `(?i)/(cart|basket|shopping-cart)(/|\?|$)`,
			Has: []string{
				`form[action*="checkout" i], a[href*="checkout" i], button[name*="checkout" i]`,
			},
		},
		ElementRoles: map[string]ElementDescriptor{
			"checkout_button": {
				Role: "button",
				Name: "~(?i)(check\\s*out|proceed|place\\s*order|去结算|立即结算|结算)",
				CSS:  `a[href*="checkout" i], button[name*="checkout" i], button[id*="checkout" i], .checkout-button`,
				Fallback: []ElementDescriptor{
					{Tag: "button", Name: "~(?i)(continue|proceed|下一步)"},
				},
			},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.click", TargetRole: "checkout_button"},
			{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 10000}},
		},
		PostConditions: []PostCondition{
			{Type: "any_of", Any: []PostCondition{
				{Type: "url_matches", URLPattern: `(?i)/(checkout|order|payment)(/|\?|$)`},
				{Type: "dom_contains", Selector: `form[action*="checkout" i], [class*="checkout-step" i]`},
			}},
		},
		OnAnomaly: map[string]AnomalyHandler{
			"promotion_modal": {
				Action: "abort",
				Reason: "Promo modal on checkout CTA — abort to avoid unintended coupon/bundle",
			},
			"ui_injection": {
				Action: "abort",
				Reason: "UI injection on cart/checkout button — abort",
			},
			"session_expired": {
				Action:     "retry",
				MaxRetries: 1,
				BackoffMS:  800,
				Reason:     "Session refresh on checkout hop",
			},
		},
	}
}

// ---------------------------------------------------------------------------
// 5. 填写收货地址(复用 browser.fill_form + 地址自动补全检测)
// ---------------------------------------------------------------------------

func ecommercePatternFillShippingAddress() *UIPattern {
	return &UIPattern{
		ID:          "ecommerce_fill_shipping_address",
		Category:    "commerce",
		Source:      "seed",
		Description: "Fill shipping address fields on checkout; detects and prefers address autocomplete dropdown when present",
		AppliesWhen: MatchCondition{
			URLPattern: `(?i)/(checkout|order|shipping|address)`,
			Has: []string{
				`form`,
				`input[name*="address" i], input[id*="address" i], input[autocomplete*="street" i], input[name*="zip" i], input[name*="postcode" i]`,
			},
		},
		ElementRoles: map[string]ElementDescriptor{
			// 地址自动补全输入:优先命中带 autocomplete=street-address / 地图服务
			// 注入的 Places widget。没有 autocomplete 时回落到普通 address1。
			"address_autocomplete": {
				Tag: "input",
				CSS: `input[autocomplete*="street-address" i], input[autocomplete*="address-line1" i], input[data-places-autocomplete], input[aria-autocomplete="list"][name*="address" i]`,
			},
			"address_line1": {
				Tag: "input",
				Name: "~(?i)(address|street|地址|街道)",
				CSS:  `input[name*="address1" i], input[name*="street" i], input[id*="address1" i]`,
			},
			"city_field": {
				Tag: "input",
				CSS: `input[name*="city" i], input[autocomplete*="address-level2" i]`,
			},
			"zip_field": {
				Tag: "input",
				CSS: `input[name*="zip" i], input[name*="postcode" i], input[autocomplete*="postal-code" i]`,
			},
			"submit_button": {
				Role: "button",
				Name: "~(?i)(continue|next|proceed|save|下一步|保存|继续)",
				CSS:  `button[type="submit"], button[name*="continue" i], button[name*="save" i]`,
			},
		},
		ActionSequence: []ActionStep{
			// browser.fill_form 统一处理多字段填写 + 地址自动补全下拉选择。
			// 具体字段由 variables.$shipping 提供;tool 实现里如果发现
			// address_autocomplete 存在,会先用 autocomplete 路径,匹配首条
			// suggestion 后再补齐 city/zip(这些字段会被 widget 自动填)。
			{
				Tool: "browser.fill_form",
				Params: map[string]interface{}{
					"fields": map[string]interface{}{
						"address_autocomplete": "$shipping.full_address",
						"address_line1":        "$shipping.line1",
						"city":                 "$shipping.city",
						"zip":                  "$shipping.zip",
					},
					"prefer_autocomplete": true,
				},
			},
			{Tool: "browser.click", TargetRole: "submit_button"},
			{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 10000}},
		},
		PostConditions: []PostCondition{
			{Type: "any_of", Any: []PostCondition{
				// 进入下一步(review / shipping-method / payment)
				{Type: "url_matches", URLPattern: `(?i)/(review|shipping|payment|order/confirm)`},
				// 或者同页出现下一步 stepper 激活态
				{Type: "dom_contains", Selector: `[class*="active" i][class*="step" i][class*="payment" i], [class*="checkout-step-payment" i]`},
			}},
		},
		OnAnomaly: map[string]AnomalyHandler{
			"validation_error": {
				Action: "abort",
				Reason: "Address validation failed — let agent inspect field errors",
			},
			"promotion_modal": {
				Action: "abort",
				Reason: "Promo modal during address fill — abort to avoid upsell",
			},
			"ui_injection": {
				Action: "abort",
				Reason: "UI injection on address form — refuse auto-submit",
			},
		},
	}
}

// ---------------------------------------------------------------------------
// 6. 下单 → 支付跳转(停在支付网关前)
// ---------------------------------------------------------------------------

func ecommercePatternPlaceOrderToPaymentGateway() *UIPattern {
	return &UIPattern{
		ID:          "ecommerce_place_order_to_payment_gateway",
		Category:    "commerce",
		Source:      "seed",
		Description: "Click the final 'place order / pay' button and stop once URL reaches the payment gateway (avoids real charge)",
		AppliesWhen: MatchCondition{
			URLPattern: `(?i)/(checkout|order/review|confirm|payment-method)`,
			Has: []string{
				`button, [role="button"]`,
			},
			// TextContains 省略:URL(/checkout|/confirm|/review)+ Has 已筛得准;
			// TextContains AND 语义下多候选短语会导致任何一条缺失都不命中。
		},
		ElementRoles: map[string]ElementDescriptor{
			"place_order_button": {
				Role: "button",
				Name: "~(?i)(place\\s*order|pay\\s*now|complete\\s*order|confirm\\s*order|下单|去支付|立即付款|提交订单)",
				CSS:  `button[name*="place" i], button[id*="place-order" i], button[data-place-order], .place-order-button`,
				Fallback: []ElementDescriptor{
					{Role: "button", Name: "~(?i)(submit|confirm)"},
				},
			},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.click", TargetRole: "place_order_button"},
			// 最多等 12 秒网络稳定(PSP 跳转链 1~3 跳常见)
			{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 12000}},
		},
		PostConditions: []PostCondition{
			// 只要 URL 落到已知支付网关或 /payment /psp 路径即视为成功——
			// 不继续填卡号,交由人类 / 沙箱卡完成扣款。
			{Type: "url_matches", URLPattern: `(?i)(stripe|checkout\.stripe|paypal|braintree|adyen|alipay|wx\.tenpay|gateway|psp|3dsecure|/payment(/|\?|$)|/pay(/|\?|$))`},
		},
		OnAnomaly: map[string]AnomalyHandler{
			"promotion_modal": {
				Action: "abort",
				Reason: "Promo modal on place-order — abort (user may be opting into bundle)",
			},
			"modal_blocking": {
				Action: "abort",
				Reason: "Blocking modal before place-order — abort",
			},
			"validation_error": {
				Action: "abort",
				Reason: "Checkout validation failed at place-order step",
			},
			"ui_injection": {
				Action: "abort",
				Reason: "UI injection on place-order CTA — refuse click",
			},
		},
	}
}

// ---------------------------------------------------------------------------
// 7. 售罄降级:找相似商品
// ---------------------------------------------------------------------------

// ecommercePatternFindSimilarProduct 是 add-to-cart 遇 sold_out 时的降级目标。
// 行为:在详情页上寻找"相似商品 / 推荐 / related products"模块的第一个链接
// 并点击,使 Agent 回到商品详情页继续尝试。
func ecommercePatternFindSimilarProduct() *UIPattern {
	return &UIPattern{
		ID:          "ecommerce_find_similar_product",
		Category:    "commerce",
		Source:      "seed",
		Description: "Fallback for sold-out: click the first 'similar / related products' link",
		AppliesWhen: MatchCondition{
			Has: []string{
				`[class*="related" i], [class*="similar" i], [class*="recommend" i], [data-related-products]`,
			},
		},
		ElementRoles: map[string]ElementDescriptor{
			"similar_product_link": {
				Role: "link",
				CSS:  `[class*="related" i] a, [class*="similar" i] a, [class*="recommend" i] a, [data-related-products] a`,
				Fallback: []ElementDescriptor{
					{Tag: "a", Name: "~(?i)(similar|related|推荐|相似)"},
				},
			},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.click", TargetRole: "similar_product_link"},
			{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 8000}},
		},
		PostConditions: []PostCondition{
			{Type: "any_of", Any: []PostCondition{
				{Type: "url_matches", URLPattern: `(?i)/(product|p|item|dp|goods)/`},
				{Type: "dom_contains", Selector: `[itemtype*="Product" i], [class*="product-detail" i]`},
			}},
		},
		OnAnomaly: map[string]AnomalyHandler{
			"ui_injection":    {Action: "abort", Reason: "Injection on related-products area"},
			"promotion_modal": {Action: "abort", Reason: "Promo modal on fallback page"},
		},
	}
}
