document.addEventListener('alpine:init', () => {
    Alpine.data('app', () => ({
        // State
        page: 'dashboard',
        menuOpen: false,
        connected: false,
        closing: null,
        closingAll: false,
        selectedPositions: [],
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

        // Account filter (controls overview + positions + equity curve)
        selectedAccount: 'all',
        equityAccount: 'all',
        equityRange: '',
        accountList: [],
        rawAccounts: [],  // raw account ticks from WS

        // Strategy info
        strategyInfoMap: {},

        // Trading state
        paused: false,

        // Config page state
        configData: null,
        configDefaults: null,
        configDirty: false,
        configSaving: false,
        configTab: 'accounts',
        editingUnit: null,
        editingAccount: null,

        // Data
        portfolio: {
            total_equity: 0, total_margin: 0, initial_equity: 0,
            unrealized_pnl: 0, locked_pnl: 0, day_pnl: 0,
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
            this.loadStrategyInfo();
            this.$nextTick(() => this.initEquityChart());
        },

        routeFromHash() {
            const hash = window.location.hash || '#/';
            const routes = {
                '#/': 'dashboard',
                '#/positions': 'positions',
                '#/trades': 'trades',
                '#/chart': 'chart',
                '#/config': 'config',
            };
            this.page = routes[hash] || 'dashboard';
            if (this.page === 'trades') this.loadTrades();
            if (this.page === 'chart') this.$nextTick(() => this.initKlineChart());
            if (this.page === 'config') this.loadConfig();
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
                    this.rawAccounts = d.accounts || [];

                    // Filter by selected account
                    const acctFilter = this.selectedAccount;
                    const allPositions = d.positions || [];
                    const filteredPositions = acctFilter === 'all'
                        ? allPositions
                        : allPositions.filter(p => p.account_id === acctFilter);
                    const filteredAccounts = acctFilter === 'all'
                        ? (d.accounts || [])
                        : (d.accounts || []).filter(a => a.account_id === acctFilter);

                    // Compute filtered totals
                    let equity = 0, margin = 0, initEquity = 0;
                    for (const a of filteredAccounts) {
                        equity += a.equity || 0;
                        margin += a.margin || 0;
                        initEquity += a.initial_equity || 0;
                    }
                    let unrealizedPnl = 0;
                    let lockedPnl = 0;
                    for (const p of filteredPositions) {
                        unrealizedPnl += p.pnl || 0;
                        // Locked PnL: only count guaranteed profit (SL in profit zone).
                        // SL in loss zone → add nothing (no guaranteed profit).
                        const slp = this.slPnl(p);
                        if (slp > 0) {
                            lockedPnl += slp;
                        }
                    }

                    this.paused = d.paused || false;
                    this.portfolio = {
                        total_equity: equity,
                        exchange_equity: d.exchange_equity || 0,
                        total_margin: margin,
                        initial_equity: initEquity,
                        unrealized_pnl: unrealizedPnl,
                        locked_pnl: lockedPnl,
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
                    // Sort by open_time descending (newest first).
                    filteredPositions.sort((a, b) => (b.open_time || 0) - (a.open_time || 0));
                    this.positions = filteredPositions;
                    for (const p of this.positions) {
                        if (p.side === 'long') this.portfolio.long_exposure += p.notional;
                        else this.portfolio.short_exposure += p.notional;
                    }
                    this.portfolio.total_exposure = this.portfolio.long_exposure + this.portfolio.short_exposure;
                    this.lastUpdate = new Date().toLocaleTimeString('zh-CN');

                    if (this.equitySeries && equity > 0) {
                        const now = Math.floor(Date.now() / 1000);
                        this.equitySeries.update({ time: now, value: equity });
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
                    const data = await resp.json();
                    // data is [{symbol, change_pct, price_pct, rank}, ...]
                    this.symbolList = data;
                    if (this.symbolList.length > 0 && !this.klineSymbol) {
                        // Default to BTC if available, otherwise first ranked
                        const btc = this.symbolList.find(s => s.symbol && s.symbol.startsWith('BTC'));
                        this.klineSymbol = btc ? btc.symbol : this.symbolList[0].symbol;
                    }
                }
            } catch (e) {
                console.error('load symbols:', e);
            }
        },

        symbolLabel(s) {
            const name = s.symbol.replace('-USDT-SWAP', '');
            const sign = s.price_pct >= 0 ? '+' : '';
            return `#${s.rank} ${name}  ${sign}${s.price_pct.toFixed(2)}%  (振幅${s.change_pct.toFixed(1)}%)`;
        },

        async loadAccounts() {
            try {
                const resp = await fetch('/api/v1/accounts');
                if (resp.ok) this.accountList = await resp.json();
            } catch (e) {
                console.error('load accounts:', e);
            }
        },

        onAccountChange() {
            // Sync equity curve filter with account selector
            this.equityAccount = this.selectedAccount;
            this.loadEquityCurve(this.equityDays);
        },

        async loadStrategyInfo() {
            try {
                const resp = await fetch('/api/v1/strategy-info');
                if (resp.ok) {
                    const infos = await resp.json();
                    for (const s of infos) {
                        this.strategyInfoMap[s.name] = s;
                    }
                }
            } catch (e) {
                console.error('load strategy info:', e);
            }
        },

        strategyTip(name) {
            const s = this.strategyInfoMap[name];
            if (!s) return '';
            let tip = `${s.name_zh}\n${s.desc}\n\n⏱ 周期: ${s.timeframes.join(', ')}\n`;

            // SL/TP 根据时间级别动态调整
            const sltpByTF = {
                '1m':  { sl: '1.2x', tp: '1.8x', ratio: '1:1.5' },
                '5m':  { sl: '1.0x', tp: '1.5x', ratio: '1:1.5' },
                '15m': { sl: '1.2x', tp: '1.8x', ratio: '1:1.5' },
                '1H':  { sl: '1.5x', tp: '2.5x', ratio: '1:1.7' },
                '4H':  { sl: '2.0x', tp: '3.5x', ratio: '1:1.75' },
            };
            tip += '\n止损止盈 (ATR倍数, 按时间级别):\n';
            for (const tf of s.timeframes) {
                const m = sltpByTF[tf] || sltpByTF['1H'];
                tip += `  ${tf}: SL ${m.sl} / TP ${m.tp} (盈亏比 ${m.ratio})\n`;
            }

            if (s.params && s.params.length > 0) {
                tip += '\n参数:\n';
                for (const p of s.params) {
                    const modified = p.value !== p.default ? ' ⚠已调整' : '';
                    tip += `  ${p.name}: ${p.value}${modified}\n    ${p.desc}\n`;
                }
            }
            return tip;
        },

        async closePosition(accountId, symbol) {
            const name = symbol.replace('-USDT-SWAP', '');
            if (!confirm(`确认平仓 ${name}？\n\n将以市价单立即平仓该持仓。`)) return;
            this.closing = symbol;
            try {
                const resp = await fetch('/api/v1/positions/close', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ account_id: accountId, symbol: symbol, reason: 'manual_close' }),
                });
                const data = await resp.json();
                if (!resp.ok) {
                    alert('平仓失败: ' + (data.error || '未知错误'));
                } else {
                    alert(`${name} 平仓成功\n成交价: ${data.fill_price || '-'}`);
                }
            } catch (e) {
                alert('平仓错误: ' + e.message);
            }
            this.closing = null;
        },

        toggleAllPositions(event) {
            if (event.target.checked) {
                this.selectedPositions = this.positions.map(p => p.account_id + '|' + p.symbol);
            } else {
                this.selectedPositions = [];
            }
        },

        async togglePause() {
            const action = this.paused ? 'resume' : 'pause';
            try {
                const resp = await fetch(`/api/v1/trading/${action}`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                });
                if (resp.ok) {
                    const data = await resp.json();
                    this.paused = data.paused;
                }
            } catch (e) {
                alert(`${action} 失败: ` + e.message);
            }
        },

        async syncPositions() {
            try {
                const resp = await fetch('/api/v1/positions/sync', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                });
                const data = await resp.json();
                if (resp.ok) {
                    const existed = (data.results || []).filter(r => r.action === 'exists').length;
                    alert(`同步完成:\n- OKX 仓位: ${data.total} 个\n- 已有记录: ${existed} 个 (跳过)\n- 新建记录: ${data.created} 个`);
                } else {
                    alert('同步失败: ' + (data.error || '未知错误'));
                }
            } catch (e) {
                alert('同步错误: ' + e.message);
            }
        },

        async forceCloseAll() {
            if (!confirm('OKX 强制平仓所有持仓！\n\n将市价平掉 OKX 账户上的全部仓位（包括孤儿仓位）。\n\n确认执行？')) return;
            this.closingAll = true;
            try {
                const resp = await fetch('/api/v1/positions/close-all', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                });
                const data = await resp.json();
                if (!resp.ok) {
                    alert('强制平仓失败: ' + (data.error || '未知错误'));
                } else {
                    const failed = (data.results || []).filter(r => r.status === 'failed').length;
                    if (failed > 0) {
                        alert(`平仓完成: ${data.closed} 个，其中 ${failed} 个失败`);
                    }
                }
            } catch (e) {
                alert('强制平仓错误: ' + e.message);
            }
            this.closingAll = false;
        },

        async closeSelected() {
            const count = this.selectedPositions.length;
            if (count === 0) return;

            const names = this.selectedPositions.map(k => k.split('|')[1].replace('-USDT-SWAP', '')).join(', ');
            if (!confirm(`确认批量平仓 ${count} 个持仓？\n\n${names}\n\n将以市价单立即平仓所有选中持仓。`)) return;

            this.closingAll = true;
            const tasks = [...this.selectedPositions];
            let failed = 0;

            for (const key of tasks) {
                const [accountId, symbol] = key.split('|');
                try {
                    const resp = await fetch('/api/v1/positions/close', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ account_id: accountId, symbol: symbol, reason: 'batch_close' }),
                    });
                    if (!resp.ok) failed++;
                } catch (e) {
                    failed++;
                }
            }

            this.closingAll = false;
            this.selectedPositions = [];
            if (failed > 0) alert(`批量平仓完成，${failed} 个失败`);
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

                // Show data range hint
                const first = new Date(points[0].time);
                const last = new Date(points[points.length - 1].time);
                const hours = Math.round((last - first) / 3600000);
                if (hours < 24) {
                    this.equityRange = `数据范围: ${hours} 小时 (${points.length} 个采样点，系统运行不足 ${days} 天)`;
                } else {
                    const d = Math.round(hours / 24);
                    this.equityRange = `数据范围: ${d} 天 (${points.length} 个采样点)`;
                }
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

        // 止损触发时的盈亏金额: (sl - entry) * qty，做空取反
        slPnl(p) {
            if (!p.stop_loss || !p.avg_price || !p.quantity) return 0;
            const diff = p.side === 'long'
                ? (p.stop_loss - p.avg_price) * p.quantity
                : (p.avg_price - p.stop_loss) * p.quantity;
            return diff;
        },
        // 止盈触发时的盈亏金额: (tp - entry) * qty，做空取反
        tpPnl(p) {
            if (!p.take_profit || !p.avg_price || !p.quantity) return 0;
            const diff = p.side === 'long'
                ? (p.take_profit - p.avg_price) * p.quantity
                : (p.avg_price - p.take_profit) * p.quantity;
            return diff;
        },

        // ===== Config Management =====
        async loadConfig() {
            try {
                const [cfgResp, defResp] = await Promise.all([
                    fetch('/api/v1/config'),
                    fetch('/api/v1/config/defaults'),
                ]);
                if (cfgResp.ok) this.configData = await cfgResp.json();
                if (defResp.ok) this.configDefaults = await defResp.json();
                this.configDirty = false;
            } catch (e) {
                console.error('load config:', e);
            }
        },

        markDirty() { this.configDirty = true; },

        async saveConfig() {
            if (!this.configData || this.configSaving) return;
            this.configSaving = true;
            try {
                const resp = await fetch('/api/v1/config', {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        accounts: this.configData.accounts,
                        units: this.configData.units,
                        strategy: this.configData.strategy,
                        risk: this.configData.risk,
                        signal_exit: this.configData.signal_exit,
                        trailing_stop: this.configData.trailing_stop,
                    }),
                });
                const data = await resp.json();
                if (data.saved) {
                    this.configDirty = false;
                    alert('配置已保存成功！\n\n注意：账户和交易单元的修改需要重启 sidecar 才能完全生效。\n策略参数和信号退出配置已实时应用。');
                } else {
                    alert('保存提示: ' + (data.message || '未知状态'));
                }
            } catch (e) {
                alert('保存失败: ' + e.message);
            }
            this.configSaving = false;
        },

        // Account CRUD
        addAccount() {
            if (!this.configData) return;
            const id = 'account-' + Date.now().toString(36);
            this.configData.accounts.push({
                id: id,
                exchange: 'paper',
                api_key: '',
                secret_key: '',
                passphrase: '',
                base_url: '',
                simulated: false,
                initial_equity: 10000,
                slippage_bps: 5,
                fee_bps: 4,
                tags: [],
            });
            this.editingAccount = id;
            this.markDirty();
        },

        removeAccount(id) {
            if (!confirm(`确认删除账户 "${id}"？\n\n关联的交易单元也将失去账户引用。`)) return;
            this.configData.accounts = this.configData.accounts.filter(a => a.id !== id);
            if (this.editingAccount === id) this.editingAccount = null;
            this.markDirty();
        },

        // Unit CRUD
        addUnit(accountId) {
            if (!this.configData) return;
            const id = 'unit-' + Date.now().toString(36);
            this.configData.units.push({
                id: id,
                account_id: accountId || (this.configData.accounts[0]?.id || ''),
                symbols: [],
                timeframe: '1H',
                max_leverage: 10,
                enabled: true,
                strategy: null,
                risk: null,
            });
            this.editingUnit = id;
            this.markDirty();
        },

        removeUnit(id) {
            if (!confirm(`确认删除交易单元 "${id}"？`)) return;
            this.configData.units = this.configData.units.filter(u => u.id !== id);
            if (this.editingUnit === id) this.editingUnit = null;
            this.markDirty();
        },

        enableUnitStrategy(unit) {
            if (unit.strategy) return;
            const g = this.configData.strategy;
            unit.strategy = JSON.parse(JSON.stringify(g));
            this.markDirty();
        },

        disableUnitStrategy(unit) {
            unit.strategy = null;
            this.markDirty();
        },

        enableUnitRisk(unit) {
            if (unit.risk) return;
            const g = this.configData.risk;
            unit.risk = JSON.parse(JSON.stringify(g));
            this.markDirty();
        },

        disableUnitRisk(unit) {
            unit.risk = null;
            this.markDirty();
        },

        unitsForAccount(accountId) {
            if (!this.configData) return [];
            return this.configData.units.filter(u => u.account_id === accountId);
        },

        timeframeOptions: [
            { value: '1m', label: '1 分钟' },
            { value: '5m', label: '5 分钟' },
            { value: '15m', label: '15 分钟' },
            { value: '1H', label: '1 小时' },
            { value: '4H', label: '4 小时' },
        ],

        addSymbolToUnit(unit) {
            const sym = prompt('输入交易对名称（例如: BTC-USDT-SWAP）');
            if (sym && sym.trim()) {
                if (!unit.symbols) unit.symbols = [];
                unit.symbols.push(sym.trim().toUpperCase());
                this.markDirty();
            }
        },

        removeSymbolFromUnit(unit, idx) {
            unit.symbols.splice(idx, 1);
            this.markDirty();
        },

        addTag(account) {
            const tag = prompt('输入标签名称');
            if (tag && tag.trim()) {
                if (!account.tags) account.tags = [];
                account.tags.push(tag.trim());
                this.markDirty();
            }
        },

        removeTag(account, idx) {
            account.tags.splice(idx, 1);
            this.markDirty();
        },

        reasonText(reason) {
            const map = {
                'signal_exit': '信号退出', 'stop_loss': '止损', 'take_profit': '止盈',
                'manual_close': '手动平仓', 'batch_close': '批量平仓',
                'timeout': '超时退出', 'max_hold': '超时退出',
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
        tfBadgeClass(tf) {
            const map = { '1m': 'badge-tf-1m', '5m': 'badge-tf-5m', '15m': 'badge-tf-15m', '1H': 'badge-tf-1h', '4H': 'badge-tf-4h' };
            return map[tf] || 'badge-tf-1h';
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
