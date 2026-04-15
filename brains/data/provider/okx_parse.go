package provider

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// okxWSMessage is the top-level envelope for OKX WebSocket pushes.
type okxWSMessage struct {
	Arg  okxArg            `json:"arg"`
	Data json.RawMessage   `json:"data"`
}

type okxArg struct {
	Channel string `json:"channel"`
	InstID  string `json:"instId"`
}

// channelToBar maps OKX candle channel names to bar strings.
var channelToBar = map[string]string{
	"candle1m":  "1m",
	"candle5m":  "5m",
	"candle15m": "15m",
	"candle1H":  "1H",
	"candle4H":  "4H",
}

// ParseCandlePayload parses the data array from a candle push.
// Each element: [ts, o, h, l, c, vol, volCcy, ...]
func ParseCandlePayload(data json.RawMessage, instID, channel string) ([]Candle, error) {
	var rows [][]string
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("unmarshal candle rows: %w", err)
	}
	bar := channelToBar[channel]
	if bar == "" {
		bar = strings.TrimPrefix(channel, "candle")
	}
	out := make([]Candle, 0, len(rows))
	for i, r := range rows {
		if len(r) < 7 {
			return nil, fmt.Errorf("candle row %d: want >=7 fields, got %d", i, len(r))
		}
		ts, err := strconv.ParseInt(r[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("candle row %d ts: %w", i, err)
		}
		o, err := strconv.ParseFloat(r[1], 64)
		if err != nil {
			return nil, fmt.Errorf("candle row %d open: %w", i, err)
		}
		h, err := strconv.ParseFloat(r[2], 64)
		if err != nil {
			return nil, fmt.Errorf("candle row %d high: %w", i, err)
		}
		l, err := strconv.ParseFloat(r[3], 64)
		if err != nil {
			return nil, fmt.Errorf("candle row %d low: %w", i, err)
		}
		c, err := strconv.ParseFloat(r[4], 64)
		if err != nil {
			return nil, fmt.Errorf("candle row %d close: %w", i, err)
		}
		vol, err := strconv.ParseFloat(r[5], 64)
		if err != nil {
			return nil, fmt.Errorf("candle row %d vol: %w", i, err)
		}
		volCcy, err := strconv.ParseFloat(r[6], 64)
		if err != nil {
			return nil, fmt.Errorf("candle row %d volCcy: %w", i, err)
		}
		out = append(out, Candle{
			InstID:    instID,
			Bar:       bar,
			Timestamp: ts,
			Open:      o,
			High:      h,
			Low:       l,
			Close:     c,
			Volume:    vol,
			VolumeCcy: volCcy,
		})
	}
	return out, nil
}

// okxTrade is the JSON shape of one trade object pushed by OKX.
type okxTrade struct {
	InstID  string `json:"instId"`
	TradeID string `json:"tradeId"`
	Px      string `json:"px"`
	Sz      string `json:"sz"`
	Side    string `json:"side"`
	Ts      string `json:"ts"`
}

// ParseTradePayload parses the data array from a trades push.
func ParseTradePayload(data json.RawMessage) ([]Trade, error) {
	var rows []okxTrade
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("unmarshal trades: %w", err)
	}
	out := make([]Trade, 0, len(rows))
	for i, r := range rows {
		px, err := strconv.ParseFloat(r.Px, 64)
		if err != nil {
			return nil, fmt.Errorf("trade %d px: %w", i, err)
		}
		sz, err := strconv.ParseFloat(r.Sz, 64)
		if err != nil {
			return nil, fmt.Errorf("trade %d sz: %w", i, err)
		}
		ts, err := strconv.ParseInt(r.Ts, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("trade %d ts: %w", i, err)
		}
		out = append(out, Trade{
			InstID:    r.InstID,
			TradeID:   r.TradeID,
			Price:     px,
			Size:      sz,
			Side:      r.Side,
			Timestamp: ts,
		})
	}
	return out, nil
}

// okxBookLevel is one level in a books5 snapshot: [price, size, _, numOrders].
// We only need price and size.

// okxBookData is the JSON shape of one books5 snapshot.
type okxBookData struct {
	Bids [][]string `json:"bids"`
	Asks [][]string `json:"asks"`
	Ts   string     `json:"ts"`
}

// ParseOrderBookPayload parses the data array from a books5 push.
func ParseOrderBookPayload(data json.RawMessage, instID string) (*OrderBook, error) {
	var rows []okxBookData
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("unmarshal book: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("empty book data")
	}
	r := rows[0]
	ts, err := strconv.ParseInt(r.Ts, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("book ts: %w", err)
	}
	ob := &OrderBook{
		InstID:    instID,
		Timestamp: ts,
	}
	for i := 0; i < 5 && i < len(r.Bids); i++ {
		if len(r.Bids[i]) < 2 {
			continue
		}
		p, _ := strconv.ParseFloat(r.Bids[i][0], 64)
		s, _ := strconv.ParseFloat(r.Bids[i][1], 64)
		ob.Bids[i] = PriceLevel{Price: p, Size: s}
	}
	for i := 0; i < 5 && i < len(r.Asks); i++ {
		if len(r.Asks[i]) < 2 {
			continue
		}
		p, _ := strconv.ParseFloat(r.Asks[i][0], 64)
		s, _ := strconv.ParseFloat(r.Asks[i][1], 64)
		ob.Asks[i] = PriceLevel{Price: p, Size: s}
	}
	return ob, nil
}

// okxFunding is the JSON shape of one funding-rate push.
type okxFunding struct {
	InstID      string `json:"instId"`
	FundingRate string `json:"fundingRate"`
	FundingTime string `json:"fundingTime"`
}

// ParseFundingPayload parses the data array from a funding-rate push.
func ParseFundingPayload(data json.RawMessage) ([]FundingRate, error) {
	var rows []okxFunding
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("unmarshal funding: %w", err)
	}
	out := make([]FundingRate, 0, len(rows))
	for i, r := range rows {
		rate, err := strconv.ParseFloat(r.FundingRate, 64)
		if err != nil {
			return nil, fmt.Errorf("funding %d rate: %w", i, err)
		}
		ft, err := strconv.ParseInt(r.FundingTime, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("funding %d time: %w", i, err)
		}
		out = append(out, FundingRate{
			InstID:      r.InstID,
			Rate:        rate,
			NextFunding: ft,
			Timestamp:   time.Now().UnixMilli(),
		})
	}
	return out, nil
}

// ParseWSMessage parses a raw OKX WebSocket push and returns DataEvents.
// It returns nil (no events) for subscription confirmations, pongs, or
// unrecognised channels.
func ParseWSMessage(raw []byte, providerName string) []DataEvent {
	var msg okxWSMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}
	// Skip subscription acks and error responses.
	if msg.Data == nil {
		return nil
	}

	now := time.Now().UnixMilli()
	ch := msg.Arg.Channel
	instID := msg.Arg.InstID

	switch {
	case strings.HasPrefix(ch, "candle"):
		candles, err := ParseCandlePayload(msg.Data, instID, ch)
		if err != nil {
			return nil
		}
		events := make([]DataEvent, 0, len(candles))
		for _, c := range candles {
			events = append(events, DataEvent{
				Provider:  providerName,
				Symbol:    instID,
				Topic:     ch,
				Timestamp: c.Timestamp,
				LocalTS:   now,
				Priority:  PriorityNearRT,
				Payload:   c,
			})
		}
		return events

	case ch == "trades":
		trades, err := ParseTradePayload(msg.Data)
		if err != nil {
			return nil
		}
		events := make([]DataEvent, 0, len(trades))
		for _, t := range trades {
			events = append(events, DataEvent{
				Provider:  providerName,
				Symbol:    instID,
				Topic:     "trades",
				Timestamp: t.Timestamp,
				LocalTS:   now,
				Priority:  PriorityRealtime,
				Payload:   t,
			})
		}
		return events

	case ch == "books5":
		ob, err := ParseOrderBookPayload(msg.Data, instID)
		if err != nil {
			return nil
		}
		return []DataEvent{{
			Provider:  providerName,
			Symbol:    instID,
			Topic:     "books5",
			Timestamp: ob.Timestamp,
			LocalTS:   now,
			Priority:  PriorityRealtime,
			Payload:   *ob,
		}}

	case ch == "funding-rate":
		rates, err := ParseFundingPayload(msg.Data)
		if err != nil {
			return nil
		}
		events := make([]DataEvent, 0, len(rates))
		for _, fr := range rates {
			events = append(events, DataEvent{
				Provider:  providerName,
				Symbol:    instID,
				Topic:     "funding-rate",
				Timestamp: fr.Timestamp,
				LocalTS:   now,
				Priority:  PriorityNearRT,
				Payload:   fr,
			})
		}
		return events
	}

	return nil
}
