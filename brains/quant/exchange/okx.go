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
	"math"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
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

	// Contract info cache: instID → contractInfo
	ctCache sync.Map // map[string]*contractInfo
}

type contractInfo struct {
	CtVal  float64 // contract face value (e.g. 0.01 BTC, 10 USDT)
	LotSz  float64 // minimum lot size (e.g. 1)
	CtMult float64 // contract multiplier
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
func (e *OKXExchange) CredentialKey() string      { return e.config.APIKey }
func (e *OKXExchange) Capabilities() Capabilities { return e.caps }
func (e *OKXExchange) IsOpen() bool               { return true } // 24/7

// Init sets the OKX account to the correct trading mode.
// 1. Single-currency margin mode (required for SWAP contracts)
// 2. Long/short position mode (hedge mode)
// Must be called before placing orders. Safe to call multiple times.
func (e *OKXExchange) Init(ctx context.Context) error {
	// Step 1: Get current account config to check mode
	body, err := e.signedGet(ctx, "/api/v5/account/config", "")
	if err != nil {
		return fmt.Errorf("get account config: %w", err)
	}
	var cfgResp struct {
		Code string `json:"code"`
		Data []struct {
			AcctLv  string `json:"acctLv"`  // 1=simple, 2=single-ccy, 3=multi-ccy, 4=portfolio
			PosMode string `json:"posMode"` // "long_short_mode" or "net_mode"
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &cfgResp); err != nil {
		return fmt.Errorf("parse account config: %w", err)
	}

	if cfgResp.Code == "0" && len(cfgResp.Data) > 0 {
		d := cfgResp.Data[0]

		// Step 2: Upgrade account level to single-currency margin if still in simple mode.
		// acctLv: 1=simple, 2=single-ccy margin, 3=multi-ccy, 4=portfolio
		if d.AcctLv == "1" {
			_, err := e.signedPost(ctx, "/api/v5/account/set-account-level", map[string]string{
				"acctLv": "2", // single-currency margin mode
			})
			if err != nil {
				return fmt.Errorf("set account level to single-ccy margin: %w (please set manually in OKX web/app: Trade → Settings → Account Mode → Single-currency margin)", err)
			}
		}

		// Step 3: Set position mode to hedge (long/short) if not already
		if d.PosMode != "long_short_mode" {
			if _, err := e.signedPost(ctx, "/api/v5/account/set-position-mode", map[string]string{
				"posMode": "long_short_mode",
			}); err != nil {
				return fmt.Errorf("set position mode: %w", err)
			}
		}
	}
	return nil
}

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
		lots, _ := strconv.ParseFloat(p.Pos, 64)
		if lots == 0 {
			continue
		}
		avgPx, _ := strconv.ParseFloat(p.AvgPx, 64)
		markPx, _ := strconv.ParseFloat(p.MarkPx, 64)
		upl, _ := strconv.ParseFloat(p.Upl, 64)
		lever, _ := strconv.Atoi(p.Lever)
		ut, _ := strconv.ParseInt(p.UTime, 10, 64)

		// OKX pos field is in contract lots (张数). Convert to coin
		// quantity so the rest of the system uses a uniform unit.
		// qty_coin = lots × ctVal (e.g. 34 lots × 1 ACT/lot = 34 ACT).
		qty := lots
		ci, err := e.getContractInfo(ctx, p.InstID)
		if err == nil && ci.CtVal > 0 {
			qty = lots * ci.CtVal
		}

		notional := qty * markPx
		margin := 0.0
		if lever > 0 {
			margin = notional / float64(lever)
		}
		positions = append(positions, PositionInfo{
			Symbol:       p.InstID,
			Side:         p.PosSide,
			Quantity:     qty,
			AvgPrice:     avgPx,
			MarkPrice:    markPx,
			Notional:     notional,
			Margin:       margin,
			UnrealizedPL: upl,
			Leverage:     lever,
			UpdatedAt:    time.UnixMilli(ut),
		})
	}
	return positions, nil
}

// ── Place Order ─────────────────────────────────────────────────

type okxOrderReq struct {
	InstID      string            `json:"instId"`
	TdMode      string            `json:"tdMode"`
	Side        string            `json:"side"`
	PosSide     string            `json:"posSide,omitempty"`
	OrdType     string            `json:"ordType"`
	Sz          string            `json:"sz"`
	Px          string            `json:"px,omitempty"`
	ReduceOnly  bool              `json:"reduceOnly,omitempty"`
	ClOrdID     string            `json:"clOrdId,omitempty"`
	AttachAlgoOrds []okxAlgoOrd   `json:"attachAlgoOrds,omitempty"` // SL/TP via algo orders
}

// okxAlgoOrd is an attached algo order for SL/TP on a new position.
type okxAlgoOrd struct {
	AttachAlgoClOrdId string `json:"attachAlgoClOrdId,omitempty"`
	TpTriggerPx       string `json:"tpTriggerPx,omitempty"`
	TpOrdPx           string `json:"tpOrdPx,omitempty"`
	SlTriggerPx       string `json:"slTriggerPx,omitempty"`
	SlOrdPx           string `json:"slOrdPx,omitempty"`
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

	// OKX clOrdId: snowflake ID, always starts with 'q', max 20 chars.
	clOrdID := nextClOrdID()

	// Convert coin quantity to OKX contract lots (张数).
	// OKX SWAP orders use lot count, not coin amount.
	ci, err := e.getContractInfo(ctx, params.Symbol)
	if err != nil {
		return OrderResult{Error: err.Error()}, fmt.Errorf("get contract info: %w", err)
	}
	lots := qtyToLots(params.Quantity, ci)
	sz := strconv.FormatInt(lots, 10)

	reqBody := okxOrderReq{
		InstID:     params.Symbol,
		TdMode:     "cross",
		Side:       params.Side,
		PosSide:    params.PosSide,
		OrdType:    ordType,
		Sz:         sz,
		ReduceOnly: params.ReduceOnly,
		ClOrdID:    clOrdID,
	}
	if params.Price > 0 && ordType == "limit" {
		reqBody.Px = strconv.FormatFloat(params.Price, 'f', -1, 64)
	}
	// Attach SL/TP as algo orders (OKX v5 requires attachAlgoOrds array).
	if params.StopLoss > 0 || params.TakeProfit > 0 {
		algo := okxAlgoOrd{
			AttachAlgoClOrdId: nextClOrdID(),
		}
		if params.StopLoss > 0 {
			algo.SlTriggerPx = strconv.FormatFloat(params.StopLoss, 'f', -1, 64)
			algo.SlOrdPx = "-1" // market price on trigger
		}
		if params.TakeProfit > 0 {
			algo.TpTriggerPx = strconv.FormatFloat(params.TakeProfit, 'f', -1, 64)
			algo.TpOrdPx = "-1"
		}
		reqBody.AttachAlgoOrds = []okxAlgoOrd{algo}
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

	ordID := resp.Data[0].OrdID

	// Market orders fill immediately on OKX. Query fill details
	// so we can return accurate fill price and quantity.
	if ordType == "market" {
		if filled, err := e.queryOrderFill(ctx, params.Symbol, ordID); err == nil {
			return filled, nil
		}
		// Fall through to accepted if query fails — order is still placed.
	}

	return OrderResult{
		OrderID:   ordID,
		Status:    "accepted",
		Timestamp: time.Now(),
	}, nil
}

// queryOrderFill queries a filled order's details (fill price, qty, fee).
func (e *OKXExchange) queryOrderFill(ctx context.Context, symbol, ordID string) (OrderResult, error) {
	// Brief delay for OKX to process the fill.
	time.Sleep(200 * time.Millisecond)

	query := fmt.Sprintf("instId=%s&ordId=%s", symbol, ordID)
	body, err := e.signedGet(ctx, "/api/v5/trade/order", query)
	if err != nil {
		return OrderResult{}, err
	}

	var resp struct {
		Code string `json:"code"`
		Data []struct {
			OrdID   string `json:"ordId"`
			ClOrdID string `json:"clOrdId"`
			State   string `json:"state"`    // live, partially_filled, filled, canceled
			AvgPx   string `json:"avgPx"`    // average fill price
			AccFillSz string `json:"accFillSz"` // accumulated fill quantity
			Fee     string `json:"fee"`      // negative = fee charged
			FillTime string `json:"fillTime"` // fill timestamp ms
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Code != "0" || len(resp.Data) == 0 {
		return OrderResult{}, fmt.Errorf("query order failed")
	}

	d := resp.Data[0]
	avgPx, _ := strconv.ParseFloat(d.AvgPx, 64)
	fillSz, _ := strconv.ParseFloat(d.AccFillSz, 64)
	fee, _ := strconv.ParseFloat(d.Fee, 64)
	fillTime, _ := strconv.ParseInt(d.FillTime, 10, 64)

	status := d.State
	if status == "filled" {
		// Map OKX "filled" to our standard status.
	} else if status == "partially_filled" || status == "live" {
		status = "open"
	}

	ts := time.Now()
	if fillTime > 0 {
		ts = time.UnixMilli(fillTime)
	}

	return OrderResult{
		OrderID:   d.OrdID,
		Status:    status,
		FillPrice: avgPx,
		FillQty:   fillSz,
		Fee:       fee,
		Timestamp: ts,
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

// CancelOpenOrders cancels all open orders (regular + algo) for a symbol.
// This covers:
//  1. Regular pending limit orders via /api/v5/trade/cancel-batch-orders
//  2. All algo orders (conditional, oco, trigger) via /api/v5/trade/cancel-algos
//
// OKX attached SL/TP (slTriggerPx/tpTriggerPx on a regular order) become
// independent algo orders (ordType=oco) once the parent order fills. Querying
// only ordType=conditional would miss them entirely.
func (e *OKXExchange) CancelOpenOrders(ctx context.Context, symbol string) int {
	cancelled := 0

	// ── Step 1: Cancel regular pending orders ──
	cancelled += e.cancelRegularOrders(ctx, symbol)

	// ── Step 2: Cancel algo orders (all types) ──
	// OKX algo ordTypes: conditional, oco, trigger, move_order_stop, iceberg, twap
	// We query each relevant type because the API requires ordType filter.
	for _, ordType := range []string{"conditional", "oco", "trigger", "move_order_stop"} {
		cancelled += e.cancelAlgoOrdersByType(ctx, symbol, ordType)
	}

	return cancelled
}

// cancelRegularOrders cancels all pending regular orders for a symbol.
func (e *OKXExchange) cancelRegularOrders(ctx context.Context, symbol string) int {
	query := fmt.Sprintf("instId=%s&instType=SWAP", symbol)
	body, err := e.signedGet(ctx, "/api/v5/trade/orders-pending", query)
	if err != nil {
		return 0
	}
	var listResp struct {
		Code string `json:"code"`
		Data []struct {
			OrdID  string `json:"ordId"`
			InstID string `json:"instId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil || listResp.Code != "0" || len(listResp.Data) == 0 {
		return 0
	}

	// OKX batch cancel supports up to 20 orders per call.
	cancelled := 0
	for i := 0; i < len(listResp.Data); i += 20 {
		end := i + 20
		if end > len(listResp.Data) {
			end = len(listResp.Data)
		}
		batch := make([]map[string]string, 0, end-i)
		for _, d := range listResp.Data[i:end] {
			batch = append(batch, map[string]string{
				"instId": d.InstID,
				"ordId":  d.OrdID,
			})
		}
		cancelBody, err := e.signedPost(ctx, "/api/v5/trade/cancel-batch-orders", batch)
		if err != nil {
			continue
		}
		var cancelResp struct {
			Code string `json:"code"`
			Data []struct {
				SCode string `json:"sCode"`
			} `json:"data"`
		}
		if err := json.Unmarshal(cancelBody, &cancelResp); err != nil {
			continue
		}
		for _, d := range cancelResp.Data {
			if d.SCode == "0" {
				cancelled++
			}
		}
	}
	return cancelled
}

// cancelAlgoOrdersByType cancels all pending algo orders of a specific type for a symbol.
func (e *OKXExchange) cancelAlgoOrdersByType(ctx context.Context, symbol, ordType string) int {
	query := fmt.Sprintf("instId=%s&ordType=%s", symbol, ordType)
	body, err := e.signedGet(ctx, "/api/v5/trade/orders-algo-pending", query)
	if err != nil {
		return 0
	}
	var listResp struct {
		Code string `json:"code"`
		Data []struct {
			AlgoID string `json:"algoId"`
			InstID string `json:"instId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil || listResp.Code != "0" || len(listResp.Data) == 0 {
		return 0
	}

	cancelItems := make([]map[string]string, 0, len(listResp.Data))
	for _, d := range listResp.Data {
		cancelItems = append(cancelItems, map[string]string{
			"algoId": d.AlgoID,
			"instId": d.InstID,
		})
	}
	cancelBody, err := e.signedPost(ctx, "/api/v5/trade/cancel-algos", cancelItems)
	if err != nil {
		return 0
	}
	var cancelResp struct {
		Code string `json:"code"`
		Data []struct {
			SCode string `json:"sCode"`
		} `json:"data"`
	}
	if err := json.Unmarshal(cancelBody, &cancelResp); err != nil {
		return 0
	}
	cancelled := 0
	for _, d := range cancelResp.Data {
		if d.SCode == "0" {
			cancelled++
		}
	}
	return cancelled
}

// ── Update SL/TP (Trailing Stop Support) ───────────────────────
// OKX attached SL/TP become independent algo orders (ordType=oco)
// after the parent fill. To move SL/TP we:
//  1. Find the pending algo order for this symbol+posSide
//  2. Amend it via /api/v5/trade/amend-algos

// UpdateStopLoss modifies the stop-loss trigger price on an existing
// algo order. Implements the StopLossUpdater interface.
func (e *OKXExchange) UpdateStopLoss(ctx context.Context, symbol, posSide string, newSL float64) error {
	algoID, err := e.findAlgoOrder(ctx, symbol, posSide, "sl")
	if err != nil {
		return fmt.Errorf("find SL algo order: %w", err)
	}
	if algoID == "" {
		return fmt.Errorf("no SL algo order found for %s/%s", symbol, posSide)
	}

	reqBody := map[string]string{
		"instId":      symbol,
		"algoId":      algoID,
		"newSlTriggerPx": strconv.FormatFloat(newSL, 'f', -1, 64),
		"newSlOrdPx":  "-1", // market price on trigger
	}
	body, err := e.signedPost(ctx, "/api/v5/trade/amend-algos", reqBody)
	if err != nil {
		return fmt.Errorf("amend SL: %w", err)
	}

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			SCode string `json:"sCode"`
			SMsg  string `json:"sMsg"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse amend SL response: %w", err)
	}
	if resp.Code != "0" {
		errMsg := resp.Msg
		if len(resp.Data) > 0 && resp.Data[0].SMsg != "" {
			errMsg = resp.Data[0].SMsg
		}
		return fmt.Errorf("OKX amend SL error: %s", errMsg)
	}
	return nil
}

// UpdateTakeProfit modifies the take-profit trigger price on an existing
// algo order.
func (e *OKXExchange) UpdateTakeProfit(ctx context.Context, symbol, posSide string, newTP float64) error {
	algoID, err := e.findAlgoOrder(ctx, symbol, posSide, "tp")
	if err != nil {
		return fmt.Errorf("find TP algo order: %w", err)
	}
	if algoID == "" {
		return fmt.Errorf("no TP algo order found for %s/%s", symbol, posSide)
	}

	reqBody := map[string]string{
		"instId":      symbol,
		"algoId":      algoID,
		"newTpTriggerPx": strconv.FormatFloat(newTP, 'f', -1, 64),
		"newTpOrdPx":  "-1",
	}
	body, err := e.signedPost(ctx, "/api/v5/trade/amend-algos", reqBody)
	if err != nil {
		return fmt.Errorf("amend TP: %w", err)
	}

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			SCode string `json:"sCode"`
			SMsg  string `json:"sMsg"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse amend TP response: %w", err)
	}
	if resp.Code != "0" {
		errMsg := resp.Msg
		if len(resp.Data) > 0 && resp.Data[0].SMsg != "" {
			errMsg = resp.Data[0].SMsg
		}
		return fmt.Errorf("OKX amend TP error: %s", errMsg)
	}
	return nil
}

// findAlgoOrder locates the pending algo order (SL or TP) for a given
// symbol and position side. OKX uses ordType "oco" for attached SL/TP.
// slOrTp: "sl" to find stop-loss, "tp" to find take-profit.
func (e *OKXExchange) findAlgoOrder(ctx context.Context, symbol, posSide, slOrTp string) (string, error) {
	// OKX attached SL/TP are "oco" type algo orders after parent fills.
	// Also check "conditional" in case they were placed separately.
	for _, ordType := range []string{"oco", "conditional"} {
		query := fmt.Sprintf("instId=%s&ordType=%s", symbol, ordType)
		body, err := e.signedGet(ctx, "/api/v5/trade/orders-algo-pending", query)
		if err != nil {
			continue
		}
		var resp struct {
			Code string `json:"code"`
			Data []struct {
				AlgoID      string `json:"algoId"`
				InstID      string `json:"instId"`
				PosSide     string `json:"posSide"`
				SlTriggerPx string `json:"slTriggerPx"`
				TpTriggerPx string `json:"tpTriggerPx"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || resp.Code != "0" {
			continue
		}
		for _, d := range resp.Data {
			if d.InstID != symbol {
				continue
			}
			// Match position side (if specified).
			if posSide != "" && d.PosSide != "" && d.PosSide != posSide {
				continue
			}
			switch slOrTp {
			case "sl":
				if d.SlTriggerPx != "" && d.SlTriggerPx != "0" {
					return d.AlgoID, nil
				}
			case "tp":
				if d.TpTriggerPx != "" && d.TpTriggerPx != "0" {
					return d.AlgoID, nil
				}
			}
		}
	}
	return "", nil
}

// ── Contract Info (for qty→lots conversion) ─────────────────────

// getContractInfo fetches and caches contract specifications for an instrument.
func (e *OKXExchange) getContractInfo(ctx context.Context, instID string) (*contractInfo, error) {
	if v, ok := e.ctCache.Load(instID); ok {
		return v.(*contractInfo), nil
	}

	// GET /api/v5/public/instruments?instType=SWAP&instId=BTC-USDT-SWAP
	body, err := e.publicGet(ctx, "/api/v5/public/instruments", "instType=SWAP&instId="+instID)
	if err != nil {
		return nil, fmt.Errorf("get instrument info: %w", err)
	}

	var resp struct {
		Code string `json:"code"`
		Data []struct {
			CtVal  string `json:"ctVal"`
			LotSz  string `json:"lotSz"`
			CtMult string `json:"ctMult"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return nil, fmt.Errorf("instrument %s not found", instID)
	}

	d := resp.Data[0]
	ci := &contractInfo{}
	ci.CtVal, _ = strconv.ParseFloat(d.CtVal, 64)
	ci.LotSz, _ = strconv.ParseFloat(d.LotSz, 64)
	ci.CtMult, _ = strconv.ParseFloat(d.CtMult, 64)
	if ci.CtVal <= 0 {
		ci.CtVal = 1
	}
	if ci.LotSz <= 0 {
		ci.LotSz = 1
	}
	if ci.CtMult <= 0 {
		ci.CtMult = 1
	}

	e.ctCache.Store(instID, ci)
	return ci, nil
}

// qtyToLots converts a coin quantity to OKX contract lots (张数).
// E.g. BTC-USDT-SWAP: ctVal=0.01, qty=0.05 BTC → 5 lots.
func qtyToLots(qty float64, ci *contractInfo) int64 {
	lots := qty / ci.CtVal
	// Round to nearest lot size
	if ci.LotSz > 0 {
		lots = math.Floor(lots/ci.LotSz) * ci.LotSz
	}
	if lots < ci.LotSz {
		lots = ci.LotSz // minimum 1 lot
	}
	return int64(lots)
}

// publicGet makes an unsigned GET request (public endpoints don't need auth).
func (e *OKXExchange) publicGet(ctx context.Context, path, query string) ([]byte, error) {
	url := e.config.BaseURL + path
	if query != "" {
		url += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if e.config.Simulated {
		req.Header.Set("x-simulated-trading", "1")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
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

// okxSnowflake generates globally unique clOrdId for OKX orders.
// Twitter snowflake layout: 41-bit timestamp | 10-bit machine | 12-bit sequence.
var okxSnowflake = struct {
	seq    atomic.Int64
	lastMs atomic.Int64
}{}

const okxSnowflakeEpoch = 1704067200000 // 2024-01-01 UTC

// nextClOrdID generates an OKX-compliant clOrdId using snowflake algorithm.
// Format: "q" + snowflake int64 (max 19 digits) = max 20 chars, well under 32.
// Always starts with letter 'q', only alphanumeric.
func nextClOrdID() string {
	now := time.Now().UnixMilli() - okxSnowflakeEpoch
	last := okxSnowflake.lastMs.Load()
	if now == last {
		seq := okxSnowflake.seq.Add(1) & 0xFFF
		if seq == 0 {
			for now <= last {
				now = time.Now().UnixMilli() - okxSnowflakeEpoch
			}
		}
		okxSnowflake.lastMs.Store(now)
		return "q" + strconv.FormatInt((now<<22)|(0<<12)|seq, 10)
	}
	okxSnowflake.lastMs.Store(now)
	okxSnowflake.seq.Store(0)
	return "q" + strconv.FormatInt((now<<22)|(0<<12)|0, 10)
}
