package exchange

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// OKXConfig holds credentials and settings for the OKX exchange.
type OKXConfig struct {
	APIKey     string
	SecretKey  string
	Passphrase string
	BaseURL    string // default: "https://www.okx.com"
	Simulated  bool   // true = OKX demo trading mode
}

// OKXExchange implements Exchange for OKX perpetual swaps.
type OKXExchange struct {
	config OKXConfig
	client *http.Client
	caps   Capabilities
}

// NewOKXExchange creates a real OKX exchange connection.
func NewOKXExchange(cfg OKXConfig) *OKXExchange {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://www.okx.com"
	}

	return &OKXExchange{
		config: cfg,
		client: &http.Client{Timeout: 10 * time.Second},
		caps: Capabilities{
			CanShort:         true,
			MaxLeverage:      125,
			HasFundingRate:   true,
			HasOrderBook:     true,
			MinOrderSize:     0.001,
			TickSize:         0.01,
			SettlementDays:   0,
			BaseCurrency:     "USDT",
			CrossAssetAnchor: "BTC",
		},
	}
}

func (e *OKXExchange) Name() string              { return "okx" }
func (e *OKXExchange) Capabilities() Capabilities { return e.caps }
func (e *OKXExchange) IsOpen() bool               { return true } // 24/7

// ── Balance ─────────────────────────────────────────────────────

type okxBalanceResp struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		TotalEq   string `json:"totalEq"`
		AdjEq     string `json:"adjEq"`
		IsoEq     string `json:"isoEq"`
		OrdFroz   string `json:"ordFroz"`
		Imr       string `json:"imr"`
		Mmr       string `json:"mmr"`
		NotionalUsd string `json:"notionalUsd"`
		Details   []struct {
			Ccy       string `json:"ccy"`
			Eq        string `json:"eq"`
			AvailBal  string `json:"availBal"`
			FrozenBal string `json:"frozenBal"`
			Upl       string `json:"upl"`
		} `json:"details"`
	} `json:"data"`
}

func (e *OKXExchange) QueryBalance(ctx context.Context) (BalanceInfo, error) {
	body, err := e.signedGet(ctx, "/api/v5/account/balance", "")
	if err != nil {
		return BalanceInfo{}, fmt.Errorf("query balance: %w", err)
	}

	var resp okxBalanceResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return BalanceInfo{}, fmt.Errorf("parse balance: %w", err)
	}
	if resp.Code != "0" {
		return BalanceInfo{}, fmt.Errorf("OKX error: %s %s", resp.Code, resp.Msg)
	}
	if len(resp.Data) == 0 {
		return BalanceInfo{}, fmt.Errorf("empty balance data")
	}

	d := resp.Data[0]
	totalEq, _ := strconv.ParseFloat(d.TotalEq, 64)
	imr, _ := strconv.ParseFloat(d.Imr, 64)

	// Find USDT detail
	avail := 0.0
	upl := 0.0
	for _, det := range d.Details {
		if det.Ccy == "USDT" {
			avail, _ = strconv.ParseFloat(det.AvailBal, 64)
			upl, _ = strconv.ParseFloat(det.Upl, 64)
			break
		}
	}

	return BalanceInfo{
		Equity:       totalEq,
		Available:    avail,
		Margin:       imr,
		UnrealizedPL: upl,
		Currency:     "USDT",
	}, nil
}

// ── Positions ───────────────────────────────────────────────────

type okxPositionResp struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		InstID  string `json:"instId"`
		PosSide string `json:"posSide"`
		Pos     string `json:"pos"`
		AvgPx   string `json:"avgPx"`
		MarkPx  string `json:"markPx"`
		Upl     string `json:"upl"`
		Lever   string `json:"lever"`
		UTime   string `json:"uTime"`
	} `json:"data"`
}

func (e *OKXExchange) QueryPositions(ctx context.Context) ([]PositionInfo, error) {
	body, err := e.signedGet(ctx, "/api/v5/account/positions", "instType=SWAP")
	if err != nil {
		return nil, fmt.Errorf("query positions: %w", err)
	}

	var resp okxPositionResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse positions: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("OKX error: %s %s", resp.Code, resp.Msg)
	}

	positions := make([]PositionInfo, 0, len(resp.Data))
	for _, p := range resp.Data {
		qty, _ := strconv.ParseFloat(p.Pos, 64)
		if qty == 0 {
			continue
		}
		avgPx, _ := strconv.ParseFloat(p.AvgPx, 64)
		markPx, _ := strconv.ParseFloat(p.MarkPx, 64)
		upl, _ := strconv.ParseFloat(p.Upl, 64)
		lever, _ := strconv.Atoi(p.Lever)
		ut, _ := strconv.ParseInt(p.UTime, 10, 64)

		positions = append(positions, PositionInfo{
			Symbol:       p.InstID,
			Side:         p.PosSide,
			Quantity:     qty,
			AvgPrice:     avgPx,
			MarkPrice:    markPx,
			UnrealizedPL: upl,
			Leverage:     lever,
			UpdatedAt:    time.UnixMilli(ut),
		})
	}
	return positions, nil
}

// ── Place Order ─────────────────────────────────────────────────

type okxOrderReq struct {
	InstID  string `json:"instId"`
	TdMode  string `json:"tdMode"`
	Side    string `json:"side"`
	PosSide string `json:"posSide,omitempty"`
	OrdType string `json:"ordType"`
	Sz      string `json:"sz"`
	Px      string `json:"px,omitempty"`
	SlTriggerPx string `json:"slTriggerPx,omitempty"`
	SlOrdPx     string `json:"slOrdPx,omitempty"`
	TpTriggerPx string `json:"tpTriggerPx,omitempty"`
	TpOrdPx     string `json:"tpOrdPx,omitempty"`
	ClOrdID     string `json:"clOrdId,omitempty"`
}

type okxOrderResp struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		OrdID   string `json:"ordId"`
		ClOrdID string `json:"clOrdId"`
		SCode   string `json:"sCode"`
		SMsg    string `json:"sMsg"`
	} `json:"data"`
}

func (e *OKXExchange) PlaceOrder(ctx context.Context, params PlaceOrderParams) (OrderResult, error) {
	// Set leverage first — failure here means the order might execute at wrong leverage.
	if params.Leverage > 0 {
		if err := e.setLeverage(ctx, params.Symbol, params.PosSide, params.Leverage); err != nil {
			return OrderResult{Error: err.Error()}, fmt.Errorf("set leverage to %d: %w", params.Leverage, err)
		}
	}

	ordType := "market"
	if params.Type == "limit" {
		ordType = "limit"
	}

	reqBody := okxOrderReq{
		InstID:  params.Symbol,
		TdMode:  "cross",
		Side:    params.Side,
		PosSide: params.PosSide,
		OrdType: ordType,
		Sz:      strconv.FormatFloat(params.Quantity, 'f', -1, 64),
		ClOrdID: params.ClientID,
	}
	if params.Price > 0 && ordType == "limit" {
		reqBody.Px = strconv.FormatFloat(params.Price, 'f', -1, 64)
	}
	if params.StopLoss > 0 {
		reqBody.SlTriggerPx = strconv.FormatFloat(params.StopLoss, 'f', -1, 64)
		reqBody.SlOrdPx = "-1" // market price on trigger
	}
	if params.TakeProfit > 0 {
		reqBody.TpTriggerPx = strconv.FormatFloat(params.TakeProfit, 'f', -1, 64)
		reqBody.TpOrdPx = "-1"
	}

	body, err := e.signedPost(ctx, "/api/v5/trade/order", reqBody)
	if err != nil {
		return OrderResult{Error: err.Error()}, err
	}

	var resp okxOrderResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return OrderResult{Error: err.Error()}, fmt.Errorf("parse order response: %w", err)
	}
	if resp.Code != "0" {
		errMsg := resp.Msg
		if len(resp.Data) > 0 && resp.Data[0].SMsg != "" {
			errMsg = resp.Data[0].SMsg
		}
		return OrderResult{Error: errMsg, Status: "rejected"}, fmt.Errorf("OKX order error: %s", errMsg)
	}
	if len(resp.Data) == 0 {
		return OrderResult{Error: "empty response"}, fmt.Errorf("empty order response")
	}

	return OrderResult{
		OrderID:   resp.Data[0].OrdID,
		Status:    "accepted",
		Timestamp: time.Now(),
	}, nil
}

// ── Cancel Order ────────────────────────────────────────────────

func (e *OKXExchange) CancelOrder(ctx context.Context, symbol, orderID string) error {
	reqBody := map[string]string{
		"instId": symbol,
		"ordId":  orderID,
	}
	body, err := e.signedPost(ctx, "/api/v5/trade/cancel-order", reqBody)
	if err != nil {
		return fmt.Errorf("cancel order: %w", err)
	}

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse cancel response: %w", err)
	}
	if resp.Code != "0" {
		return fmt.Errorf("OKX cancel error: %s %s", resp.Code, resp.Msg)
	}
	return nil
}

// ── Set Leverage ────────────────────────────────────────────────

func (e *OKXExchange) setLeverage(ctx context.Context, instID, posSide string, lever int) error {
	reqBody := map[string]string{
		"instId":  instID,
		"lever":   strconv.Itoa(lever),
		"mgnMode": "cross",
	}
	if posSide != "" {
		reqBody["posSide"] = posSide
	}
	_, err := e.signedPost(ctx, "/api/v5/account/set-leverage", reqBody)
	return err
}

// ── HTTP helpers ────────────────────────────────────────────────

func (e *OKXExchange) signedGet(ctx context.Context, path, query string) ([]byte, error) {
	url := e.config.BaseURL + path
	if query != "" {
		url += "?" + query
	}

	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	preSign := ts + "GET" + path
	if query != "" {
		preSign += "?" + query
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	e.setHeaders(req, ts, preSign)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (e *OKXExchange) signedPost(ctx context.Context, path string, payload any) ([]byte, error) {
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := e.config.BaseURL + path
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	preSign := ts + "POST" + path + string(jsonBody)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	e.setHeaders(req, ts, preSign)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (e *OKXExchange) setHeaders(req *http.Request, ts, preSign string) {
	sign := e.sign(preSign)
	req.Header.Set("OK-ACCESS-KEY", e.config.APIKey)
	req.Header.Set("OK-ACCESS-SIGN", sign)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", e.config.Passphrase)
	if e.config.Simulated {
		req.Header.Set("x-simulated-trading", "1")
	}
}

func (e *OKXExchange) sign(preSign string) string {
	h := hmac.New(sha256.New, []byte(e.config.SecretKey))
	h.Write([]byte(preSign))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
