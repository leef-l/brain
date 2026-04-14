package provider

import (
	"encoding/json"
	"math"
	"testing"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestParseCandlePayload(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		instID  string
		channel string
		wantLen int
		check   func(t *testing.T, cs []Candle)
	}{
		{
			name:    "single 1m candle",
			json:    `[["1697040000000","27500.1","27550.2","27480.3","27520.5","1234.56","34000000"]]`,
			instID:  "BTC-USDT-SWAP",
			channel: "candle1m",
			wantLen: 1,
			check: func(t *testing.T, cs []Candle) {
				c := cs[0]
				if c.InstID != "BTC-USDT-SWAP" {
					t.Errorf("InstID = %q", c.InstID)
				}
				if c.Bar != "1m" {
					t.Errorf("Bar = %q, want 1m", c.Bar)
				}
				if c.Timestamp != 1697040000000 {
					t.Errorf("Timestamp = %d", c.Timestamp)
				}
				if !almostEqual(c.Open, 27500.1) {
					t.Errorf("Open = %f", c.Open)
				}
				if !almostEqual(c.Volume, 1234.56) {
					t.Errorf("Volume = %f", c.Volume)
				}
			},
		},
		{
			name:    "two 5m candles",
			json:    `[["1697040000000","100","110","90","105","50","5000"],["1697040300000","105","115","100","112","60","6000"]]`,
			instID:  "ETH-USDT-SWAP",
			channel: "candle5m",
			wantLen: 2,
			check: func(t *testing.T, cs []Candle) {
				if cs[0].Bar != "5m" || cs[1].Bar != "5m" {
					t.Errorf("bars: %q, %q", cs[0].Bar, cs[1].Bar)
				}
				if cs[1].Timestamp != 1697040300000 {
					t.Errorf("second ts = %d", cs[1].Timestamp)
				}
				if !almostEqual(cs[1].Close, 112) {
					t.Errorf("second close = %f", cs[1].Close)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs, err := ParseCandlePayload(json.RawMessage(tt.json), tt.instID, tt.channel)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cs) != tt.wantLen {
				t.Fatalf("got %d candles, want %d", len(cs), tt.wantLen)
			}
			tt.check(t, cs)
		})
	}
}

func TestParseTradePayload(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantLen int
		check   func(t *testing.T, ts []Trade)
	}{
		{
			name:    "single trade",
			json:    `[{"instId":"BTC-USDT-SWAP","tradeId":"123456","px":"27520.5","sz":"0.5","side":"buy","ts":"1697040000000"}]`,
			wantLen: 1,
			check: func(t *testing.T, ts []Trade) {
				tr := ts[0]
				if tr.InstID != "BTC-USDT-SWAP" {
					t.Errorf("InstID = %q", tr.InstID)
				}
				if tr.Side != "buy" {
					t.Errorf("Side = %q", tr.Side)
				}
				if !almostEqual(tr.Price, 27520.5) {
					t.Errorf("Price = %f", tr.Price)
				}
			},
		},
		{
			name:    "two trades",
			json:    `[{"instId":"ETH-USDT-SWAP","tradeId":"1","px":"1800.0","sz":"10","side":"sell","ts":"1697040000000"},{"instId":"ETH-USDT-SWAP","tradeId":"2","px":"1801.5","sz":"5","side":"buy","ts":"1697040001000"}]`,
			wantLen: 2,
			check: func(t *testing.T, ts []Trade) {
				if ts[1].TradeID != "2" {
					t.Errorf("second tradeId = %q", ts[1].TradeID)
				}
				if !almostEqual(ts[1].Price, 1801.5) {
					t.Errorf("second price = %f", ts[1].Price)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trades, err := ParseTradePayload(json.RawMessage(tt.json))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(trades) != tt.wantLen {
				t.Fatalf("got %d trades, want %d", len(trades), tt.wantLen)
			}
			tt.check(t, trades)
		})
	}
}

func TestParseOrderBookPayload(t *testing.T) {
	tests := []struct {
		name   string
		json   string
		instID string
		check  func(t *testing.T, ob *OrderBook)
	}{
		{
			name:   "two levels each side",
			json:    `[{"bids":[["27520.0","1.5","","1"],["27519.0","2.0","","3"]],"asks":[["27521.0","1.2","","2"],["27522.0","3.0","","1"]],"ts":"1697040000000"}]`,
			instID: "BTC-USDT-SWAP",
			check: func(t *testing.T, ob *OrderBook) {
				if ob.Timestamp != 1697040000000 {
					t.Errorf("ts = %d", ob.Timestamp)
				}
				if !almostEqual(ob.Bids[0].Price, 27520.0) {
					t.Errorf("bid0 price = %f", ob.Bids[0].Price)
				}
				if !almostEqual(ob.Asks[1].Size, 3.0) {
					t.Errorf("ask1 size = %f", ob.Asks[1].Size)
				}
			},
		},
		{
			name:   "five full levels",
			json:    `[{"bids":[["100","1","","1"],["99","2","","1"],["98","3","","1"],["97","4","","1"],["96","5","","1"]],"asks":[["101","1","","1"],["102","2","","1"],["103","3","","1"],["104","4","","1"],["105","5","","1"]],"ts":"1000"}]`,
			instID: "ETH-USDT-SWAP",
			check: func(t *testing.T, ob *OrderBook) {
				if !almostEqual(ob.Bids[4].Price, 96) {
					t.Errorf("bid4 = %f", ob.Bids[4].Price)
				}
				if !almostEqual(ob.Asks[4].Size, 5) {
					t.Errorf("ask4 size = %f", ob.Asks[4].Size)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ob, err := ParseOrderBookPayload(json.RawMessage(tt.json), tt.instID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.check(t, ob)
		})
	}
}

func TestParseFundingPayload(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantLen int
		check   func(t *testing.T, frs []FundingRate)
	}{
		{
			name:    "single funding rate",
			json:    `[{"instId":"BTC-USDT-SWAP","fundingRate":"0.0001","fundingTime":"1697040000000"}]`,
			wantLen: 1,
			check: func(t *testing.T, frs []FundingRate) {
				if frs[0].InstID != "BTC-USDT-SWAP" {
					t.Errorf("InstID = %q", frs[0].InstID)
				}
				if !almostEqual(frs[0].Rate, 0.0001) {
					t.Errorf("Rate = %f", frs[0].Rate)
				}
			},
		},
		{
			name:    "two funding rates",
			json:    `[{"instId":"BTC-USDT-SWAP","fundingRate":"0.0001","fundingTime":"1697040000000"},{"instId":"ETH-USDT-SWAP","fundingRate":"-0.0002","fundingTime":"1697040000000"}]`,
			wantLen: 2,
			check: func(t *testing.T, frs []FundingRate) {
				if !almostEqual(frs[1].Rate, -0.0002) {
					t.Errorf("second rate = %f", frs[1].Rate)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frs, err := ParseFundingPayload(json.RawMessage(tt.json))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(frs) != tt.wantLen {
				t.Fatalf("got %d, want %d", len(frs), tt.wantLen)
			}
			tt.check(t, frs)
		})
	}
}

func TestParseWSMessage(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantTopic string
		wantLen   int
	}{
		{
			name:      "candle message",
			raw:       `{"arg":{"channel":"candle1m","instId":"BTC-USDT-SWAP"},"data":[["1697040000000","27500.1","27550.2","27480.3","27520.5","1234.56","34000000"]]}`,
			wantTopic: "candle1m",
			wantLen:   1,
		},
		{
			name:      "trade message",
			raw:       `{"arg":{"channel":"trades","instId":"BTC-USDT-SWAP"},"data":[{"instId":"BTC-USDT-SWAP","tradeId":"123","px":"100","sz":"1","side":"buy","ts":"1000"}]}`,
			wantTopic: "trades",
			wantLen:   1,
		},
		{
			name:      "books5 message",
			raw:       `{"arg":{"channel":"books5","instId":"BTC-USDT-SWAP"},"data":[{"bids":[["100","1","","1"]],"asks":[["101","1","","1"]],"ts":"1000"}]}`,
			wantTopic: "books5",
			wantLen:   1,
		},
		{
			name:      "funding-rate message",
			raw:       `{"arg":{"channel":"funding-rate","instId":"BTC-USDT-SWAP"},"data":[{"instId":"BTC-USDT-SWAP","fundingRate":"0.0001","fundingTime":"1000"}]}`,
			wantTopic: "funding-rate",
			wantLen:   1,
		},
		{
			name:    "subscription ack (no data)",
			raw:     `{"event":"subscribe","arg":{"channel":"trades","instId":"BTC-USDT-SWAP"}}`,
			wantLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := ParseWSMessage([]byte(tt.raw), "test")
			if len(events) != tt.wantLen {
				t.Fatalf("got %d events, want %d", len(events), tt.wantLen)
			}
			if tt.wantLen > 0 && events[0].Topic != tt.wantTopic {
				t.Errorf("topic = %q, want %q", events[0].Topic, tt.wantTopic)
			}
		})
	}
}
