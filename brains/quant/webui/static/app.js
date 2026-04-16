document.addEventListener('alpine:init', () => {
    Alpine.data('app', () => ({
        // State
        page: 'dashboard',
        connected: false,
        closing: null,
        lastUpdate: '-',
        ws: null,
        equityChart: null,
        equitySeries: null,
        equityDays: 1,

        // K-line state
        klineChart: null,
        klineSeries: null,
        volumeChart: null,
        volumeSeries: null,
        klineSymbol: '',
        klineBar: '1m',
        klineData: [],
        symbolList: [],
        klineBars: [
            { value: '1m', label: '1分钟' },
            { value: '5m', label: '5分钟' },
            { value: '15m', label: '15分钟' },
            { value: '1H', label: '1小时' },
            { value: '4H', label: '4小时' },
        ],

        // Equity curve state
        equityAccount: 'all',
        accountList: [],

        // Data
        portfolio: {
            total_equity: 0, unrealized_pnl: 0, day_pnl: 0,
            total_trades: 0, wins: 0, losses: 0, win_rate: 0,
            total_exposure: 0, long_exposure: 0, short_exposure: 0,
            accountCount: 0,
        },
        positions: [],
        trades: [],

        init() {
            this.routeFromHash();
            window.addEventListener('hashchange', () => this.routeFromHash());
            this.connectWS();
            this.loadTrades();
            this.loadSymbols();
            this.loadAccounts();
            this.$nextTick(() => this.initEquityChart());
        },

        routeFromHash() {
            const hash = window.location.hash || '#/';
            const routes = {
                '#/': 'dashboard',
                '#/positions': 'positions',
                '#/trades': 'trades',
                '#/chart': 'chart',
            };
            this.page = routes[hash] || 'dashboard';
            if (this.page === 'trades') this.loadTrades();
            if (this.page === 'chart') this.$nextTick(() => this.initKlineChart());
        },

        // Navigate to chart page for a specific symbol
        goToChart(symbol) {
            this.klineSymbol = symbol;
            this.page = 'chart';
            window.location.hash = '#/chart';
            this.$nextTick(() => {
                this.initKlineChart();
                this.loadKline();
            });
        },

        // WebSocket
        connectWS() {
            const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
            const url = `${proto}//${location.host}/ws`;
            this.ws = new WebSocket(url);

            this.ws.onopen = () => { this.connected = true; };
            this.ws.onclose = () => {
                this.connected = false;
                setTimeout(() => this.connectWS(), 2000);
            };
            this.ws.onerror = () => { this.connected = false; };
            this.ws.onmessage = (event) => {
                try {
                    this.handleMessage(JSON.parse(event.data));
                } catch (e) {
                    console.error('ws parse error:', e);
                }
            };
        },

        handleMessage(msg) {
            switch (msg.type) {
                case 'portfolio_tick':
                    const d = msg.data;
                    this.portfolio = {
                        total_equity: d.total_equity || 0,
                        unrealized_pnl: d.unrealized_pnl || 0,
                        day_pnl: d.day_pnl || 0,
                        total_trades: d.total_trades || 0,
                        wins: d.wins || 0,
                        losses: d.losses || 0,
                        win_rate: d.win_rate || 0,
                        total_exposure: 0,
                        long_exposure: 0,
                        short_exposure: 0,
                        accountCount: (d.accounts || []).length,
                    };
                    this.positions = d.positions || [];
                    for (const p of this.positions) {
                        if (p.side === 'long') this.portfolio.long_exposure += p.notional;
                        else this.portfolio.short_exposure += p.notional;
                    }
                    this.portfolio.total_exposure = this.portfolio.long_exposure + this.portfolio.short_exposure;
                    this.lastUpdate = new Date().toLocaleTimeString('zh-CN');

                    if (this.equitySeries && d.total_equity > 0) {
                        const now = Math.floor(Date.now() / 1000);
                        this.equitySeries.update({ time: now, value: d.total_equity });
                    }
                    break;

                case 'trade_event':
                    this.loadTrades();
                    break;
            }
        },

        // API
        async loadTrades() {
            try {
                const resp = await fetch('/api/v1/trades?limit=100');
                if (resp.ok) this.trades = await resp.json();
            } catch (e) {
                console.error('load trades:', e);
            }
        },

        async loadSymbols() {
            try {
                const resp = await fetch('/api/v1/symbols');
                if (resp.ok) {
                    this.symbolList = await resp.json();
                    if (this.symbolList.length > 0 && !this.klineSymbol) {
                        // Default to BTC if available, otherwise first
                        const btc = this.symbolList.find(s => s.startsWith('BTC'));
                        this.klineSymbol = btc || this.symbolList[0];
                    }
                }
            } catch (e) {
                console.error('load symbols:', e);
            }
        },

        async loadAccounts() {
            try {
                const resp = await fetch('/api/v1/accounts');
                if (resp.ok) this.accountList = await resp.json();
            } catch (e) {
                console.error('load accounts:', e);
            }
        },

        async closePosition(accountId, symbol) {
            const name = symbol.replace('-USDT-SWAP', '');
            if (!confirm(`确认平仓 ${name}？\n\n将以市价单立即平仓该持仓。`)) return;
            this.closing = symbol;
            try {
                const resp = await fetch('/api/v1/positions/close', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ account_id: accountId, symbol: symbol }),
                });
                const data = await resp.json();
                if (!resp.ok) alert('平仓失败: ' + (data.error || '未知错误'));
            } catch (e) {
                alert('平仓错误: ' + e.message);
            }
            this.closing = null;
        },

        // ===== Equity Chart =====
        initEquityChart() {
            const container = document.getElementById('equity-chart');
            if (!container || typeof LightweightCharts === 'undefined') return;
            if (this.equityChart) return; // already initialized

            this.equityChart = LightweightCharts.createChart(container, {
                layout: {
                    background: { type: 'solid', color: '#14142a' },
                    textColor: '#9999bb',
                    fontFamily: "-apple-system, 'PingFang SC', sans-serif",
                    fontSize: 11,
                },
                grid: { vertLines: { color: '#1e1e38' }, horzLines: { color: '#1e1e38' } },
                timeScale: { timeVisible: true, secondsVisible: false },
                rightPriceScale: { borderColor: '#1e1e38' },
                crosshair: { vertLine: { color: '#4a4a6a', style: 2 }, horzLine: { color: '#4a4a6a', style: 2 } },
                width: container.clientWidth,
                height: 220,
            });

            this.equitySeries = this.equityChart.addAreaSeries({
                lineColor: '#00c853',
                topColor: 'rgba(0, 200, 83, 0.25)',
                bottomColor: 'rgba(0, 200, 83, 0.0)',
                lineWidth: 2,
            });

            new ResizeObserver(() => {
                if (this.equityChart) this.equityChart.applyOptions({ width: container.clientWidth });
            }).observe(container);

            this.loadEquityCurve(1);
        },

        async loadEquityCurve(days) {
            this.equityDays = days;
            try {
                let url = `/api/v1/equity-curve?days=${days}`;
                if (this.equityAccount && this.equityAccount !== 'all') {
                    url += `&account=${encodeURIComponent(this.equityAccount)}`;
                }
                const resp = await fetch(url);
                if (!resp.ok) return;
                const points = await resp.json();
                if (!points || points.length === 0) return;
                const data = points.map(p => ({
                    time: Math.floor(new Date(p.time).getTime() / 1000),
                    value: p.equity,
                }));
                this.equitySeries.setData(data);
                this.equityChart.timeScale().fitContent();
            } catch (e) {
                console.error('load equity curve:', e);
            }
        },

        // ===== K-line Chart =====
        initKlineChart() {
            const container = document.getElementById('kline-chart');
            const volContainer = document.getElementById('volume-chart');
            if (!container || typeof LightweightCharts === 'undefined') return;

            // Destroy old charts
            if (this.klineChart) { this.klineChart.remove(); this.klineChart = null; }
            if (this.volumeChart) { this.volumeChart.remove(); this.volumeChart = null; }

            const chartOpts = {
                layout: {
                    background: { type: 'solid', color: '#14142a' },
                    textColor: '#9999bb',
                    fontFamily: "-apple-system, 'PingFang SC', sans-serif",
                    fontSize: 11,
                },
                grid: { vertLines: { color: '#1e1e38' }, horzLines: { color: '#1e1e38' } },
                timeScale: { timeVisible: true, secondsVisible: true },
                rightPriceScale: { borderColor: '#1e1e38' },
                crosshair: {
                    mode: LightweightCharts.CrosshairMode.Normal,
                    vertLine: { color: '#4a4a6a', style: 2 },
                    horzLine: { color: '#4a4a6a', style: 2 },
                },
                width: container.clientWidth,
                height: 450,
            };

            this.klineChart = LightweightCharts.createChart(container, chartOpts);
            this.klineSeries = this.klineChart.addCandlestickSeries({
                upColor: '#00c853',
                downColor: '#ff5252',
                borderUpColor: '#00c853',
                borderDownColor: '#ff5252',
                wickUpColor: '#00c853',
                wickDownColor: '#ff5252',
            });

            // Volume chart
            if (volContainer) {
                this.volumeChart = LightweightCharts.createChart(volContainer, {
                    ...chartOpts,
                    height: 120,
                    rightPriceScale: { borderColor: '#1e1e38', scaleMargins: { top: 0.1, bottom: 0 } },
                });
                this.volumeSeries = this.volumeChart.addHistogramSeries({
                    priceFormat: { type: 'volume' },
                    priceScaleId: '',
                });

                // Sync time scales
                this.klineChart.timeScale().subscribeVisibleLogicalRangeChange((range) => {
                    if (range && this.volumeChart) {
                        this.volumeChart.timeScale().setVisibleLogicalRange(range);
                    }
                });
                this.volumeChart.timeScale().subscribeVisibleLogicalRangeChange((range) => {
                    if (range && this.klineChart) {
                        this.klineChart.timeScale().setVisibleLogicalRange(range);
                    }
                });
            }

            // Responsive
            new ResizeObserver(() => {
                if (this.klineChart) this.klineChart.applyOptions({ width: container.clientWidth });
                if (this.volumeChart && volContainer) this.volumeChart.applyOptions({ width: volContainer.clientWidth });
            }).observe(container);

            this.loadKline();
        },

        async loadKline() {
            if (!this.klineSymbol || !this.klineSeries) return;

            // More candles for smaller timeframes
            const limitMap = { '1m': 500, '5m': 500, '15m': 400, '1H': 300, '4H': 200 };
            const limit = limitMap[this.klineBar] || 300;

            try {
                const resp = await fetch(`/api/v1/candles?symbol=${this.klineSymbol}&bar=${this.klineBar}&limit=${limit}`);
                if (!resp.ok) return;
                const candles = await resp.json();
                if (!candles || candles.length === 0) {
                    this.klineData = [];
                    return;
                }

                this.klineData = candles;

                const ohlc = candles.map(c => ({
                    time: c.time,
                    open: c.open,
                    high: c.high,
                    low: c.low,
                    close: c.close,
                }));
                this.klineSeries.setData(ohlc);
                this.klineChart.timeScale().fitContent();

                // Volume
                if (this.volumeSeries) {
                    const vol = candles.map(c => ({
                        time: c.time,
                        value: c.volume,
                        color: c.close >= c.open ? 'rgba(0, 200, 83, 0.4)' : 'rgba(255, 82, 82, 0.4)',
                    }));
                    this.volumeSeries.setData(vol);
                    this.volumeChart.timeScale().fitContent();
                }
            } catch (e) {
                console.error('load kline:', e);
            }
        },

        // Helpers
        longExposure() {
            return this.positions.filter(p => p.side === 'long').reduce((s, p) => s + p.notional, 0);
        },
        shortExposure() {
            return this.positions.filter(p => p.side === 'short').reduce((s, p) => s + p.notional, 0);
        },

        healthClass(h) {
            if (h > 0.6) return 'health-good';
            if (h > 0.3) return 'health-warn';
            return 'health-danger';
        },
        healthText(h) { return (h * 100).toFixed(0) + '%'; },

        reasonText(reason) {
            const map = {
                'signal_exit': '信号退出', 'stop_loss': '止损', 'take_profit': '止盈',
                'manual_close': '手动平仓', 'timeout': '超时退出', 'max_hold': '超时退出',
                'review_exit': '复审退出',
            };
            return map[reason] || reason || '-';
        },
        strategyText(s) {
            const map = {
                'BreakoutMomentum': '突破动量', 'MeanReversion': '均值回归',
                'OrderFlow': '订单流', 'TrendFollower': '趋势跟踪',
            };
            return map[s] || s || '-';
        },

        // Formatters
        formatUSD(v) {
            if (v == null || isNaN(v)) return '$0.00';
            return '$' + v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
        },
        formatPnL(v) {
            if (v == null || isNaN(v)) return '$0.00';
            const sign = v >= 0 ? '+' : '-';
            return sign + '$' + Math.abs(v).toFixed(4);
        },
        formatPnLPct(v) {
            if (v == null || isNaN(v)) return '0%';
            const sign = v >= 0 ? '+' : '';
            return sign + v.toFixed(2) + '%';
        },
        formatPct(exposure, equity) {
            if (!equity) return '0%';
            return (exposure / equity * 100).toFixed(1) + '%';
        },
        formatPrice(v) {
            if (v == null || v === 0) return '-';
            if (v < 0.001) return v.toExponential(4);
            if (v < 1) return v.toFixed(6);
            if (v < 100) return v.toFixed(4);
            return v.toFixed(2);
        },
        formatTime(t) {
            if (!t) return '-';
            const d = new Date(t);
            const month = String(d.getMonth() + 1).padStart(2, '0');
            const day = String(d.getDate()).padStart(2, '0');
            const h = String(d.getHours()).padStart(2, '0');
            const m = String(d.getMinutes()).padStart(2, '0');
            const s = String(d.getSeconds()).padStart(2, '0');
            return `${month}-${day} ${h}:${m}:${s}`;
        },
    }));
});
