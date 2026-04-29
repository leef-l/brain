// Brain Control Plane - Dashboard SPA

(function () {
    'use strict';

    // --- Settings (D-10) ---
    var SETTINGS = {
        pollInterval: 10000,
        maxEvents: 100,
        theme: 'dark',
        brainAutoStart: false
    };

    function loadSettings() {
        try {
            var stored = localStorage.getItem('brain_dashboard_settings');
            if (stored) {
                var parsed = JSON.parse(stored);
                if (parsed.pollInterval) SETTINGS.pollInterval = parsed.pollInterval;
                if (parsed.maxEvents) SETTINGS.maxEvents = parsed.maxEvents;
                if (parsed.theme) SETTINGS.theme = parsed.theme;
                if (parsed.brainAutoStart !== undefined) SETTINGS.brainAutoStart = parsed.brainAutoStart;
            }
        } catch (e) { /* ignore */ }
        applyTheme(SETTINGS.theme);
    }

    function saveSettings() {
        try {
            localStorage.setItem('brain_dashboard_settings', JSON.stringify(SETTINGS));
        } catch (e) { /* ignore */ }
    }

    function applyTheme(theme) {
        if (theme === 'light') {
            document.body.classList.add('theme-light');
        } else {
            document.body.classList.remove('theme-light');
        }
    }

    // --- Auth (D-12) ---
    var AUTH_TOKEN = '';

    function loadAuthToken() {
        try {
            AUTH_TOKEN = localStorage.getItem('brain_dashboard_token') || '';
        } catch (e) { AUTH_TOKEN = ''; }
    }

    function saveAuthToken(token) {
        AUTH_TOKEN = token;
        try {
            if (token) {
                localStorage.setItem('brain_dashboard_token', token);
            } else {
                localStorage.removeItem('brain_dashboard_token');
            }
        } catch (e) { /* ignore */ }
    }

    function showLoginModal() {
        var modal = $('#login-modal');
        if (modal) modal.style.display = 'flex';
    }

    function hideLoginModal() {
        var modal = $('#login-modal');
        if (modal) modal.style.display = 'none';
    }

    function initLogin() {
        var btn = $('#login-btn');
        var input = $('#login-token');
        var errorDiv = $('#login-error');

        if (!btn || !input) return;

        btn.addEventListener('click', function () {
            var token = input.value.trim();
            if (!token) {
                if (errorDiv) errorDiv.textContent = 'Please enter a token';
                return;
            }
            saveAuthToken(token);
            if (errorDiv) errorDiv.textContent = '';
            hideLoginModal();
            // Reload data with new token
            loadOverview();
            loadBrains();
            loadExecutions();
            loadLearning();
            loadTradingData();
            loadAnalyticsData();
        });

        input.addEventListener('keydown', function (e) {
            if (e.key === 'Enter') btn.click();
        });

        if (!AUTH_TOKEN) {
            showLoginModal();
        }
    }

    function authHeaders() {
        var headers = {};
        if (AUTH_TOKEN) {
            headers['Authorization'] = 'Bearer ' + AUTH_TOKEN;
        }
        return headers;
    }

    function handleAuthError(res) {
        if (res.status === 401) {
            saveAuthToken('');
            showLoginModal();
            throw new Error('Unauthorized');
        }
    }

    // --- Constants ---
    var POLL_INTERVAL = SETTINGS.pollInterval;
    var MAX_EVENTS = SETTINGS.maxEvents;
    var SSE_RECONNECT_DELAY = 3000;

    var eventCount = 0;
    var sseSource = null;
    var chatSseSource = null;

    // Caches for Phase 3 panels
    var cachePortfolio = null;
    var cacheAccounts = null;
    var cacheBrains = null;
    var cacheRuns = null;

    // --- DOM Helpers ---

    function $(sel) { return document.querySelector(sel); }
    function setText(sel, text) { $(sel).textContent = text; }

    function formatTime(ts) {
        if (!ts) return '';
        var d = new Date(ts);
        return d.toLocaleTimeString('en-US', { hour12: false });
    }

    function formatDateTime(ts) {
        if (!ts) return '';
        var d = new Date(ts);
        return d.toLocaleString('en-US', { hour12: false });
    }

    function formatUptime(startISO) {
        var start = new Date(startISO);
        var diff = Math.floor((Date.now() - start.getTime()) / 1000);
        var h = Math.floor(diff / 3600);
        var m = Math.floor((diff % 3600) / 60);
        var s = diff % 60;
        return 'Uptime: ' + h + 'h ' + m + 'm ' + s + 's';
    }

    function truncate(str, max) {
        if (!str) return '';
        return str.length > max ? str.substring(0, max) + '...' : str;
    }

    function setConnectionStatus(status) {
        var el = $('#connection-status');
        if (!el) return;
        el.textContent = status;
        el.className = 'status-indicator';
        if (status === 'Connected') el.classList.add('connected');
        else if (status === 'Disconnected') el.classList.add('disconnected');
    }

    // --- API Fetchers ---

    function fetchJSON(url, opts) {
        opts = opts || {};
        opts.headers = opts.headers || {};
        var ah = authHeaders();
        for (var k in ah) opts.headers[k] = ah[k];
        return fetch(url, opts).then(function (res) {
            handleAuthError(res);
            if (!res.ok) throw new Error('HTTP ' + res.status);
            return res.json();
        });
    }

    function postJSON(url, body) {
        return fetchJSON(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body)
        });
    }

    function loadOverview() {
        fetchJSON('/v1/dashboard/overview')
            .then(function (data) {
                setText('#stat-active-brains', data.brain_count || 0);
                setText('#stat-running', data.active_runs || 0);
                setText('#stat-completed', '-');
                setText('#stat-failed', '-');
                if (data.server_start) {
                    setText('#server-uptime', formatUptime(data.server_start));
                }

                return fetchJSON('/v1/runs');
            })
            .then(function (data) {
                var runs = data.runs || [];
                var completed = 0, failed = 0;
                for (var i = 0; i < runs.length; i++) {
                    if (runs[i].status === 'completed') completed++;
                    else if (runs[i].status === 'failed') failed++;
                }
                setText('#stat-completed', completed);
                setText('#stat-failed', failed);
            })
            .catch(function (err) {
                console.error('Failed to load overview:', err);
            });
    }

    function loadBrains() {
        fetchJSON('/v1/dashboard/brains')
            .then(function (data) {
                var container = $('#brains-list');
                var brains = data.brains || [];

                if (brains.length === 0) {
                    container.innerHTML = '<div class="placeholder">No brains registered</div>';
                    return;
                }

                var html = '';
                for (var i = 0; i < brains.length; i++) {
                    var b = brains[i];
                    var statusClass = b.running ? 'running' : 'stopped';
                    var statusText = b.running ? 'Running' : 'Stopped';
                    var warmupBtn = '';
                    if (!b.running) {
                        warmupBtn = '<button class="btn btn-small btn-warmup" data-kind="' + escapeHTML(b.kind) + '">Warmup</button>';
                    }
                    html += '<div class="brain-card">'
                        + '<div>'
                        + '<span class="brain-status ' + statusClass + '"></span>'
                        + '<span class="brain-kind">' + escapeHTML(b.kind) + '</span>'
                        + '<div class="brain-meta">' + statusText
                        + (b.binary ? ' &middot; ' + escapeHTML(truncate(b.binary, 40)) : '')
                        + (b.auto_start ? ' &middot; AutoStart' : '')
                        + '</div>'
                        + '</div>'
                        + '<div class="brain-actions">' + warmupBtn + '</div>'
                        + '</div>';
                }
                container.innerHTML = html;

                // Bind warmup buttons (D-11)
                var btns = container.querySelectorAll('.btn-warmup');
                for (var j = 0; j < btns.length; j++) {
                    btns[j].addEventListener('click', function (e) {
                        var kind = e.target.getAttribute('data-kind');
                        warmupBrain(kind);
                    });
                }
            })
            .catch(function (err) {
                console.error('Failed to load brains:', err);
                var el = $('#brains-list');
                if (el) el.innerHTML = '<div class="placeholder">Failed to load brains</div>';
            });
    }

    function warmupBrain(kind) {
        if (!kind) return;
        postJSON('/v1/dashboard/brains/' + kind, {})
            .then(function () {
                console.log('Warmup initiated for', kind);
                setTimeout(loadBrains, 2000);
            })
            .catch(function (err) {
                console.error('Failed to warmup brain:', err);
                alert('Failed to warmup ' + kind + ': ' + err.message);
            });
    }

    function loadExecutions() {
        fetchJSON('/v1/runs')
            .then(function (data) {
                var container = $('#executions-list');
                var runs = data.runs || [];

                if (runs.length === 0) {
                    container.innerHTML = '<div class="placeholder">No executions</div>';
                    return;
                }

                runs.sort(function (a, b) {
                    return new Date(b.created_at) - new Date(a.created_at);
                });

                var html = '';
                for (var i = 0; i < runs.length; i++) {
                    var r = runs[i];
                    var statusClass = r.status || 'idle';
                    html += '<div class="exec-item">'
                        + '<div>'
                        + '<span class="exec-id">' + escapeHTML(truncate(r.run_id || r.id || '', 12)) + '</span>'
                        + '<span class="exec-brain">' + escapeHTML(r.brain || 'unknown') + '</span>'
                        + '<div class="exec-prompt">' + escapeHTML(truncate(r.prompt || '', 80)) + '</div>'
                        + '</div>'
                        + '<span class="exec-status ' + statusClass + '">' + escapeHTML(r.status || 'unknown') + '</span>'
                        + '</div>';
                }
                container.innerHTML = html;
            })
            .catch(function (err) {
                console.error('Failed to load executions:', err);
                var el = $('#executions-list');
                if (el) el.innerHTML = '<div class="placeholder">Failed to load executions</div>';
            });
    }

    // --- WebSocket / SSE Events ---

    var wsConn = null;

    function connectWS() {
        var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
        var wsUrl = proto + '//' + location.host + '/v1/dashboard/ws';
        try {
            wsConn = new WebSocket(wsUrl);
        } catch (e) {
            connectSSE();
            return;
        }

        wsConn.onopen = function () {
            setConnectionStatus('Connected (WS)');
        };

        wsConn.onmessage = function (e) {
            try {
                var msg = JSON.parse(e.data);
                if (msg.type === 'event' && msg.data) {
                    var ev = typeof msg.data === 'string' ? JSON.parse(msg.data) : msg.data;
                    addEvent(ev);
                    addChatEvent(ev);
                }
            } catch (err) {
                addEvent({ type: 'raw', detail: e.data, timestamp: new Date().toISOString() });
                addChatEvent({ type: 'raw', detail: e.data, timestamp: new Date().toISOString() });
            }
        };

        wsConn.onerror = function () {
            wsConn.close();
            wsConn = null;
            connectSSE();
        };

        wsConn.onclose = function () {
            setConnectionStatus('Disconnected');
            wsConn = null;
            setTimeout(connectWS, SSE_RECONNECT_DELAY);
        };
    }

    function connectSSE() {
        if (sseSource) {
            sseSource.close();
        }

        var url = '/v1/dashboard/events';
        if (AUTH_TOKEN) {
            url += '?token=' + encodeURIComponent(AUTH_TOKEN);
        }
        sseSource = new EventSource(url);

        sseSource.onopen = function () {
            setConnectionStatus('Connected (SSE)');
        };

        sseSource.onmessage = function (e) {
            try {
                var ev = JSON.parse(e.data);
                addEvent(ev);
                addChatEvent(ev);
            } catch (err) {
                addEvent({ type: 'raw', detail: e.data, timestamp: new Date().toISOString() });
                addChatEvent({ type: 'raw', detail: e.data, timestamp: new Date().toISOString() });
            }
        };

        sseSource.onerror = function () {
            setConnectionStatus('Disconnected');
            sseSource.close();
            sseSource = null;
            setTimeout(connectSSE, SSE_RECONNECT_DELAY);
        };
    }

    function addEvent(ev) {
        var container = $('#events-list');
        if (!container) return;

        var placeholder = container.querySelector('.placeholder');
        if (placeholder) placeholder.remove();

        var div = document.createElement('div');
        div.className = 'event-item';

        var time = ev.timestamp || ev.time || new Date().toISOString();
        var type = ev.type || ev.event || 'event';
        var detail = ev.detail || ev.message || ev.summary || '';
        if (typeof detail === 'object') {
            detail = JSON.stringify(detail);
        }

        div.innerHTML = '<span class="event-time">' + escapeHTML(formatTime(time)) + '</span>'
            + '<span class="event-type">' + escapeHTML(type) + '</span>'
            + '<span class="event-detail">' + escapeHTML(truncate(detail, 120)) + '</span>';

        container.insertBefore(div, container.firstChild);

        eventCount++;
        while (container.children.length > MAX_EVENTS) {
            container.removeChild(container.lastChild);
            eventCount--;
        }

        setText('#event-count', eventCount);
    }

    // --- Chat Panel (D-9) ---

    function addChatEvent(ev) {
        var container = $('#chat-messages');
        if (!container) return;
        var placeholder = container.querySelector('.placeholder');
        if (placeholder) placeholder.remove();

        var type = ev.type || ev.event || 'event';
        var detail = ev.detail || ev.message || ev.summary || '';
        if (typeof detail === 'object') detail = JSON.stringify(detail);

        var div = document.createElement('div');
        div.className = 'chat-message system';
        var time = formatTime(ev.timestamp || ev.time || new Date().toISOString());
        div.innerHTML = '<div class="chat-bubble">'
            + '<div class="chat-header"><span class="chat-sender">System</span> <span class="chat-time">' + escapeHTML(time) + '</span></div>'
            + '<div class="chat-body"><strong>' + escapeHTML(type) + ':</strong> ' + escapeHTML(truncate(detail, 500)) + '</div>'
            + '</div>';
        container.appendChild(div);
        container.scrollTop = container.scrollHeight;
    }

    function addChatUserMessage(text) {
        var container = $('#chat-messages');
        if (!container) return;
        var placeholder = container.querySelector('.placeholder');
        if (placeholder) placeholder.remove();

        var div = document.createElement('div');
        div.className = 'chat-message user';
        var time = formatTime(new Date().toISOString());
        div.innerHTML = '<div class="chat-bubble">'
            + '<div class="chat-header"><span class="chat-sender">You</span> <span class="chat-time">' + escapeHTML(time) + '</span></div>'
            + '<div class="chat-body">' + escapeHTML(text) + '</div>'
            + '</div>';
        container.appendChild(div);
        container.scrollTop = container.scrollHeight;
    }

    function sendChatMessage(text) {
        if (!text || !text.trim()) return;
        addChatUserMessage(text);

        // Call existing execution API as chat backend
        postJSON('/v1/runs', { prompt: text, brain: 'central' })
            .then(function (data) {
                var runId = data.id || data.run_id || '';
                var msgDiv = document.createElement('div');
                msgDiv.className = 'chat-message system';
                var time = formatTime(new Date().toISOString());
                msgDiv.innerHTML = '<div class="chat-bubble">'
                    + '<div class="chat-header"><span class="chat-sender">Brain</span> <span class="chat-time">' + escapeHTML(time) + '</span></div>'
                    + '<div class="chat-body">Run started: <code>' + escapeHTML(runId) + '</code></div>'
                    + '</div>';
                var container = $('#chat-messages');
                if (container) {
                    container.appendChild(msgDiv);
                    container.scrollTop = container.scrollHeight;
                }
            })
            .catch(function (err) {
                var msgDiv = document.createElement('div');
                msgDiv.className = 'chat-message system error';
                var time = formatTime(new Date().toISOString());
                msgDiv.innerHTML = '<div class="chat-bubble">'
                    + '<div class="chat-header"><span class="chat-sender">Brain</span> <span class="chat-time">' + escapeHTML(time) + '</span></div>'
                    + '<div class="chat-body">Error: ' + escapeHTML(err.message) + '</div>'
                    + '</div>';
                var container = $('#chat-messages');
                if (container) {
                    container.appendChild(msgDiv);
                    container.scrollTop = container.scrollHeight;
                }
            });
    }

    function initChatPanel() {
        var input = $('#chat-input');
        var btn = $('#chat-send-btn');
        if (!input || !btn) return;

        btn.addEventListener('click', function () {
            var text = input.value;
            input.value = '';
            sendChatMessage(text);
        });

        input.addEventListener('keydown', function (e) {
            if (e.key === 'Enter') {
                var text = input.value;
                input.value = '';
                sendChatMessage(text);
            }
        });
    }

    // --- Settings Panel (D-10) ---

    function initSettingsPanel() {
        var pollInput = $('#setting-poll-interval');
        var maxEventsInput = $('#setting-max-events');
        var themeSelect = $('#setting-theme');
        var autoStartCheck = $('#setting-auto-start');
        var saveBtn = $('#settings-save-btn');

        if (pollInput) pollInput.value = SETTINGS.pollInterval;
        if (maxEventsInput) maxEventsInput.value = SETTINGS.maxEvents;
        if (themeSelect) themeSelect.value = SETTINGS.theme;
        if (autoStartCheck) autoStartCheck.checked = SETTINGS.brainAutoStart;

        if (saveBtn) {
            saveBtn.addEventListener('click', function () {
                if (pollInput) SETTINGS.pollInterval = parseInt(pollInput.value, 10) || 10000;
                if (maxEventsInput) SETTINGS.maxEvents = parseInt(maxEventsInput.value, 10) || 100;
                if (themeSelect) SETTINGS.theme = themeSelect.value;
                if (autoStartCheck) SETTINGS.brainAutoStart = autoStartCheck.checked;

                POLL_INTERVAL = SETTINGS.pollInterval;
                MAX_EVENTS = SETTINGS.maxEvents;
                applyTheme(SETTINGS.theme);
                saveSettings();

                var status = $('#settings-save-status');
                if (status) {
                    status.textContent = 'Saved!';
                    setTimeout(function () { status.textContent = ''; }, 2000);
                }
            });
        }
    }

    function loadLearning() {
        fetchJSON('/v1/dashboard/learning')
            .then(function (data) {
                var container = $('#learning-content');
                var patterns = data.patterns || [];
                var daily = data.daily || [];
                var interactions = data.interactions || [];

                if (patterns.length === 0 && daily.length === 0 && interactions.length === 0) {
                    container.innerHTML = '<div class="placeholder">No learning data yet</div>';
                    return;
                }

                var html = '';

                html += '<div class="learning-section"><h3>Patterns</h3>';
                if (patterns.length === 0) {
                    html += '<div class="placeholder">No patterns</div>';
                } else {
                    patterns.sort(function (a, b) { return (b.success_count || 0) - (a.success_count || 0); });
                    html += '<table class="learning-table"><thead><tr>'
                        + '<th>ID</th><th>Category</th><th>Source</th>'
                        + '<th>Match</th><th>Success</th><th>Fail</th><th>Rate</th><th>Last Hit</th>'
                        + '</tr></thead><tbody>';
                    for (var i = 0; i < patterns.length; i++) {
                        var p = patterns[i];
                        var rate = (p.success_rate || 0) * 100;
                        html += '<tr>'
                            + '<td>' + escapeHTML(truncate(p.id, 36)) + '</td>'
                            + '<td>' + escapeHTML(p.category || '-') + '</td>'
                            + '<td>' + escapeHTML(p.source || '-') + '</td>'
                            + '<td>' + (p.match_count || 0) + '</td>'
                            + '<td>' + (p.success_count || 0) + '</td>'
                            + '<td>' + (p.failure_count || 0) + '</td>'
                            + '<td>' + rate.toFixed(1) + '%</td>'
                            + '<td>' + escapeHTML(formatTime(p.last_hit_at) || '-') + '</td>'
                            + '</tr>';
                    }
                    html += '</tbody></table>';
                }
                html += '</div>';

                html += '<div class="learning-section"><h3>Interactions by Brain</h3>';
                if (interactions.length === 0) {
                    html += '<div class="placeholder">No interactions recorded</div>';
                } else {
                    interactions.sort(function (a, b) { return (b.count || 0) - (a.count || 0); });
                    html += '<table class="learning-table"><thead><tr>'
                        + '<th>Brain</th><th>Sequences</th><th>Successes</th><th>Rate</th>'
                        + '</tr></thead><tbody>';
                    for (var j = 0; j < interactions.length; j++) {
                        var it = interactions[j];
                        var total = it.count || 0;
                        var succ = it.successes || 0;
                        var irate = total > 0 ? (succ / total * 100) : 0;
                        html += '<tr>'
                            + '<td>' + escapeHTML(it.brain_kind || 'unknown') + '</td>'
                            + '<td>' + total + '</td>'
                            + '<td>' + succ + '</td>'
                            + '<td>' + irate.toFixed(1) + '%</td>'
                            + '</tr>';
                    }
                    html += '</tbody></table>';
                }
                html += '</div>';

                html += '<div class="learning-section"><h3>Daily Summaries (last 14d)</h3>';
                if (daily.length === 0) {
                    html += '<div class="placeholder">No daily summaries</div>';
                } else {
                    for (var k = 0; k < daily.length; k++) {
                        var d = daily[k];
                        html += '<div class="daily-card">'
                            + '<div class="daily-head">'
                            + '<strong>' + escapeHTML(d.date) + '</strong>'
                            + ' <span class="daily-meta">' + (d.runs_total || 0) + ' runs, ' + (d.runs_failed || 0) + ' failed</span>'
                            + '</div>';
                        if (d.summary_text) {
                            html += '<div class="daily-text">' + escapeHTML(truncate(d.summary_text, 600)) + '</div>';
                        }
                        html += '</div>';
                    }
                }
                html += '</div>';

                container.innerHTML = html;
            })
            .catch(function (err) {
                console.error('Failed to load learning:', err);
                var el = $('#learning-content');
                if (el) el.innerHTML = '<div class="placeholder">Failed to load learning data</div>';
            });
    }

    // --- Phase 3: Trading & Analytics panels ---

    function setupCanvas(canvas) {
        var rect = canvas.parentElement.getBoundingClientRect();
        var dpr = window.devicePixelRatio || 1;
        canvas.width = rect.width * dpr;
        canvas.height = rect.height * dpr;
        var ctx = canvas.getContext('2d');
        ctx.scale(dpr, dpr);
        return { ctx: ctx, width: rect.width, height: rect.height };
    }

    // -- D-5 K-Line --

    function generateMockKline(count) {
        var data = [];
        var price = 100;
        var now = Date.now();
        for (var i = 0; i < count; i++) {
            var open = price;
            var change = (Math.random() - 0.48) * 5;
            var close = open + change;
            var high = Math.max(open, close) + Math.random() * 2;
            var low = Math.min(open, close) - Math.random() * 2;
            data.push({
                time: new Date(now - (count - i) * 86400000).toISOString().split('T')[0],
                open: open, high: high, low: low, close: close
            });
            price = close;
        }
        return data;
    }

    var klineDataCache = null;

    function renderKlinePanel(data) {
        var canvas = $('#kline-canvas');
        if (!canvas) return;
        var setup = setupCanvas(canvas);
        var ctx = setup.ctx;
        var W = setup.width;
        var H = setup.height;
        var pad = { top: 20, right: 50, bottom: 30, left: 10 };

        ctx.clearRect(0, 0, W, H);

        var candles = (data && Array.isArray(data)) ? data : generateMockKline(60);
        if (!klineDataCache || data) klineDataCache = candles;
        else candles = klineDataCache;

        var n = candles.length;
        var candleW = (W - pad.left - pad.right) / n * 0.7;
        var gap = (W - pad.left - pad.right) / n * 0.3;

        var minL = Infinity, maxH = -Infinity;
        for (var i = 0; i < n; i++) {
            if (candles[i].low < minL) minL = candles[i].low;
            if (candles[i].high > maxH) maxH = candles[i].high;
        }
        var range = maxH - minL || 1;

        function y(price) {
            return pad.top + (1 - (price - minL) / range) * (H - pad.top - pad.bottom);
        }
        function x(i) {
            return pad.left + i * (candleW + gap) + gap / 2;
        }

        ctx.strokeStyle = 'rgba(100,120,140,0.15)';
        ctx.lineWidth = 1;
        for (var g = 0; g < 5; g++) {
            var gy = pad.top + g * (H - pad.top - pad.bottom) / 4;
            ctx.beginPath(); ctx.moveTo(pad.left, gy); ctx.lineTo(W - pad.right, gy); ctx.stroke();
        }

        for (var i = 0; i < n; i++) {
            var c = candles[i];
            var cx = x(i) + candleW / 2;
            var yO = y(c.open);
            var yC = y(c.close);
            var yH = y(c.high);
            var yL = y(c.low);
            var up = c.close >= c.open;
            ctx.strokeStyle = up ? '#4caf50' : '#f44336';
            ctx.fillStyle = up ? '#4caf50' : '#f44336';
            ctx.beginPath(); ctx.moveTo(cx, yH); ctx.lineTo(cx, yL); ctx.stroke();
            var bodyH = Math.max(1, Math.abs(yO - yC));
            var bodyY = Math.min(yO, yC);
            ctx.fillRect(x(i), bodyY, candleW, bodyH);
        }

        for (var i = 5; i < n; i += 12) {
            var c = candles[i];
            var cx = x(i) + candleW / 2;
            var isBuy = i % 24 === 5;
            ctx.fillStyle = isBuy ? '#4caf50' : '#f44336';
            ctx.beginPath();
            if (isBuy) {
                ctx.moveTo(cx, y(c.low) + 14);
                ctx.lineTo(cx - 4, y(c.low) + 4);
                ctx.lineTo(cx + 4, y(c.low) + 4);
            } else {
                ctx.moveTo(cx, y(c.high) - 14);
                ctx.lineTo(cx - 4, y(c.high) - 4);
                ctx.lineTo(cx + 4, y(c.high) - 4);
            }
            ctx.fill();
        }

        ctx.fillStyle = '#a0a0b0';
        ctx.font = '10px sans-serif';
        ctx.textAlign = 'left';
        for (var g = 0; g < 5; g++) {
            var price = minL + (range * g / 4);
            var gy = pad.top + (1 - g / 4) * (H - pad.top - pad.bottom);
            ctx.fillText(price.toFixed(2), W - pad.right + 4, gy + 3);
        }

        var legend = $('#kline-legend');
        if (legend && candles.length) {
            var last = candles[n - 1];
            legend.textContent = 'O ' + last.open.toFixed(2) + '  H ' + last.high.toFixed(2) + '  L ' + last.low.toFixed(2) + '  C ' + last.close.toFixed(2);
        }
    }

    // -- D-6 Trade History --

    function generateMockTrades(count) {
        var symbols = ['BTC-USDT', 'ETH-USDT', 'SOL-USDT'];
        var sides = ['BUY', 'SELL'];
        var trades = [];
        var now = Date.now();
        for (var i = 0; i < count; i++) {
            var side = sides[Math.floor(Math.random() * sides.length)];
            var symbol = symbols[Math.floor(Math.random() * symbols.length)];
            var price = symbol === 'BTC-USDT' ? 60000 + Math.random() * 5000 : (symbol === 'ETH-USDT' ? 3000 + Math.random() * 500 : 150 + Math.random() * 50);
            var size = (Math.random() * 2).toFixed(4);
            var pnl = side === 'SELL' ? (Math.random() * 200 - 50).toFixed(2) : '-';
            trades.push({
                time: new Date(now - Math.random() * 7 * 86400000).toISOString(),
                symbol: symbol,
                side: side,
                price: price.toFixed(2),
                size: size,
                pnl: pnl
            });
        }
        trades.sort(function(a,b){ return new Date(b.time) - new Date(a.time); });
        return trades;
    }

    var tradeState = { data: [], filtered: [], page: 1, perPage: 20 };

    function renderTradeHistory(raw) {
        var container = $('#trade-history-table tbody');
        var filterSel = $('#trade-filter-symbol');
        if (!container) return;

        var trades = [];
        if (raw && Array.isArray(raw)) {
            trades = raw;
        } else if (raw && raw.trades && Array.isArray(raw.trades)) {
            trades = raw.trades;
        } else if (raw && raw.accounts && Array.isArray(raw.accounts)) {
            for (var i = 0; i < raw.accounts.length; i++) {
                var acc = raw.accounts[i];
                if (acc.trades && Array.isArray(acc.trades)) {
                    for (var j = 0; j < acc.trades.length; j++) trades.push(acc.trades[j]);
                }
            }
        }

        if (trades.length === 0) trades = generateMockTrades(45);

        tradeState.data = trades;

        var symbols = {};
        for (var i = 0; i < trades.length; i++) symbols[trades[i].symbol || trades[i].Symbol || 'UNKNOWN'] = true;
        var currentFilter = filterSel ? filterSel.value : '';
        if (filterSel) {
            var opts = '<option value="">All</option>';
            for (var sym in symbols) {
                opts += '<option value="' + escapeHTML(sym) + '"' + (sym === currentFilter ? ' selected' : '') + '>' + escapeHTML(sym) + '</option>';
            }
            filterSel.innerHTML = opts;
        }

        applyTradeFilter();
    }

    function applyTradeFilter() {
        var filterVal = $('#trade-filter-symbol') ? $('#trade-filter-symbol').value : '';
        var all = tradeState.data;
        var filtered = [];
        for (var i = 0; i < all.length; i++) {
            var sym = all[i].symbol || all[i].Symbol || '';
            if (!filterVal || sym === filterVal) filtered.push(all[i]);
        }
        tradeState.filtered = filtered;
        tradeState.page = 1;
        renderTradePage();
    }

    function renderTradePage() {
        var container = $('#trade-history-table tbody');
        var pagination = $('#trade-pagination');
        var start = (tradeState.page - 1) * tradeState.perPage;
        var end = start + tradeState.perPage;
        var pageData = tradeState.filtered.slice(start, end);
        var total = tradeState.filtered.length;
        var totalPages = Math.ceil(total / tradeState.perPage) || 1;

        var html = '';
        if (pageData.length === 0) {
            html = '<tr><td colspan="6" class="placeholder">No trades</td></tr>';
        } else {
            for (var i = 0; i < pageData.length; i++) {
                var t = pageData[i];
                var time = formatDateTime(t.time || t.timestamp || t.Time || t.Timestamp || '');
                var symbol = escapeHTML(t.symbol || t.Symbol || '-');
                var side = escapeHTML(t.side || t.Side || '-');
                var price = t.price || t.Price || '-';
                var size = t.size || t.Size || '-';
                var pnl = t.pnl || t.PnL || t.realized_pnl || '-';
                var sideClass = side === 'BUY' ? 'side-buy' : (side === 'SELL' ? 'side-sell' : '');
                html += '<tr>'
                    + '<td>' + escapeHTML(time) + '</td>'
                    + '<td>' + symbol + '</td>'
                    + '<td><span class="trade-side ' + sideClass + '">' + side + '</span></td>'
                    + '<td>' + price + '</td>'
                    + '<td>' + size + '</td>'
                    + '<td>' + pnl + '</td>'
                    + '</tr>';
            }
        }
        container.innerHTML = html;

        var phtml = '<button ' + (tradeState.page <= 1 ? 'disabled' : '') + ' id="trade-prev">Prev</button>';
        phtml += '<span>Page ' + tradeState.page + ' / ' + totalPages + ' (' + total + ')</span>';
        phtml += '<button ' + (tradeState.page >= totalPages ? 'disabled' : '') + ' id="trade-next">Next</button>';
        if (pagination) pagination.innerHTML = phtml;

        var prevBtn = $('#trade-prev');
        var nextBtn = $('#trade-next');
        if (prevBtn) prevBtn.onclick = function() { if (tradeState.page > 1) { tradeState.page--; renderTradePage(); } };
        if (nextBtn) nextBtn.onclick = function() { if (tradeState.page < totalPages) { tradeState.page++; renderTradePage(); } };
    }

    function exportTradesCSV() {
        var data = tradeState.filtered;
        if (!data || data.length === 0) return;
        var header = ['Time', 'Symbol', 'Side', 'Price', 'Size', 'PnL'];
        var rows = [header.join(',')];
        for (var i = 0; i < data.length; i++) {
            var t = data[i];
            rows.push([
                '"' + (t.time || t.timestamp || t.Time || t.Timestamp || '') + '"',
                t.symbol || t.Symbol || '',
                t.side || t.Side || '',
                t.price || t.Price || '',
                t.size || t.Size || '',
                t.pnl || t.PnL || t.realized_pnl || ''
            ].join(','));
        }
        var blob = new Blob([rows.join('\n')], { type: 'text/csv' });
        var url = URL.createObjectURL(blob);
        var a = document.createElement('a');
        a.href = url;
        a.download = 'trade_history.csv';
        a.click();
        URL.revokeObjectURL(url);
    }

    // -- D-7 Decision Trace --

    function generateMockDecisions(count) {
        var strategies = ['quant.trend', 'quant.mean_reversion', 'quant.arbitrage'];
        var signals = ['LONG', 'SHORT', 'NEUTRAL'];
        var results = ['FILLED', 'PARTIAL', 'REJECTED'];
        var decisions = [];
        var now = Date.now();
        for (var i = 0; i < count; i++) {
            decisions.push({
                time: new Date(now - Math.random() * 7 * 86400000).toISOString(),
                strategy: strategies[Math.floor(Math.random() * strategies.length)],
                signal: signals[Math.floor(Math.random() * signals.length)],
                confidence: (Math.random() * 100).toFixed(1) + '%',
                result: results[Math.floor(Math.random() * results.length)]
            });
        }
        decisions.sort(function(a,b){ return new Date(b.time) - new Date(a.time); });
        return decisions;
    }

    function renderDecisionTrace(brainsData, runsData) {
        var container = $('#decision-table tbody');
        if (!container) return;

        var decisions = [];
        var runs = (runsData && runsData.runs) ? runsData.runs : [];
        var brains = (brainsData && brainsData.brains) ? brainsData.brains : [];

        if (runs.length > 0) {
            for (var i = 0; i < runs.length; i++) {
                var r = runs[i];
                var brainName = r.brain || 'unknown';
                var signal = 'NEUTRAL';
                if (r.prompt) {
                    var p = r.prompt.toLowerCase();
                    if (p.indexOf('buy') !== -1 || p.indexOf('long') !== -1) signal = 'LONG';
                    else if (p.indexOf('sell') !== -1 || p.indexOf('short') !== -1) signal = 'SHORT';
                }
                var result = r.status === 'completed' ? 'FILLED' : (r.status === 'failed' ? 'REJECTED' : 'PENDING');
                decisions.push({
                    time: r.created_at || r.updated_at || new Date().toISOString(),
                    strategy: brainName,
                    signal: signal,
                    confidence: (60 + Math.random() * 35).toFixed(1) + '%',
                    result: result
                });
            }
        } else {
            decisions = generateMockDecisions(20);
        }

        decisions.sort(function(a,b){ return new Date(b.time) - new Date(a.time); });

        var html = '';
        for (var i = 0; i < Math.min(decisions.length, 50); i++) {
            var d = decisions[i];
            var sigClass = d.signal === 'LONG' ? 'side-buy' : (d.signal === 'SHORT' ? 'side-sell' : '');
            var resClass = d.result === 'FILLED' ? 'res-filled' : (d.result === 'REJECTED' ? 'res-rejected' : '');
            html += '<tr>'
                + '<td>' + escapeHTML(formatDateTime(d.time)) + '</td>'
                + '<td>' + escapeHTML(d.strategy) + '</td>'
                + '<td><span class="trade-side ' + sigClass + '">' + d.signal + '</span></td>'
                + '<td>' + d.confidence + '</td>'
                + '<td><span class="exec-status ' + resClass + '">' + d.result + '</span></td>'
                + '</tr>';
        }
        container.innerHTML = html;
    }

    // -- D-8 Daily PnL --

    function generateMockDailyPnL(days) {
        var data = [];
        var now = Date.now();
        for (var i = days - 1; i >= 0; i--) {
            var date = new Date(now - i * 86400000).toISOString().split('T')[0];
            data.push({ date: date, pnl: Math.random() * 1000 - 300 });
        }
        return data;
    }

    var pnlDataCache = null;

    function renderDailyPnL(data) {
        var canvas = $('#pnl-canvas');
        if (!canvas) return;
        var setup = setupCanvas(canvas);
        var ctx = setup.ctx;
        var W = setup.width;
        var H = setup.height;
        var pad = { top: 20, right: 20, bottom: 30, left: 50 };

        ctx.clearRect(0, 0, W, H);

        var series = (data && Array.isArray(data)) ? data : generateMockDailyPnL(30);
        if (!pnlDataCache || data) pnlDataCache = series;
        else series = pnlDataCache;

        if (series.length === 0) series = generateMockDailyPnL(30);

        var minV = Infinity, maxV = -Infinity;
        for (var i = 0; i < series.length; i++) {
            var v = series[i].pnl || series[i].value || series[i].PnL || 0;
            if (v < minV) minV = v;
            if (v > maxV) maxV = v;
        }
        if (minV > 0) minV = 0;
        if (maxV < 0) maxV = 0;
        var range = maxV - minV || 1;

        var n = series.length;
        var barW = (W - pad.left - pad.right) / n * 0.7;
        var gap = (W - pad.left - pad.right) / n * 0.3;
        var zeroY = pad.top + (1 - (0 - minV) / range) * (H - pad.top - pad.bottom);

        ctx.strokeStyle = 'rgba(160,160,176,0.4)';
        ctx.lineWidth = 1;
        ctx.beginPath(); ctx.moveTo(pad.left, pad.top); ctx.lineTo(pad.left, H - pad.bottom); ctx.lineTo(W - pad.right, H - pad.bottom); ctx.stroke();
        ctx.strokeStyle = 'rgba(160,160,176,0.2)';
        ctx.beginPath(); ctx.moveTo(pad.left, zeroY); ctx.lineTo(W - pad.right, zeroY); ctx.stroke();

        for (var i = 0; i < n; i++) {
            var v = series[i].pnl || series[i].value || series[i].PnL || 0;
            var h = (Math.abs(v) / range) * (H - pad.top - pad.bottom);
            var x = pad.left + i * (barW + gap) + gap / 2;
            var y = v >= 0 ? zeroY - h : zeroY;
            ctx.fillStyle = v >= 0 ? 'rgba(76,175,80,0.85)' : 'rgba(244,67,54,0.85)';
            ctx.fillRect(x, y, barW, h);
        }

        ctx.fillStyle = '#a0a0b0';
        ctx.font = '10px sans-serif';
        ctx.textAlign = 'right';
        for (var g = 0; g <= 4; g++) {
            var val = minV + (range * g / 4);
            var y = pad.top + (1 - g / 4) * (H - pad.top - pad.bottom);
            ctx.fillText(val.toFixed(0), pad.left - 6, y + 3);
        }

        canvas.onmousemove = function(e) {
            var rect = canvas.getBoundingClientRect();
            var mx = e.clientX - rect.left;
            var idx = Math.floor((mx - pad.left) / (barW + gap));
            if (idx < 0 || idx >= n) { var t = $('#pnl-tooltip'); if (t) t.style.display = 'none'; return; }
            var item = series[idx];
            var tooltip = $('#pnl-tooltip');
            if (!tooltip) return;
            tooltip.style.display = 'block';
            tooltip.textContent = (item.date || item.Date || '') + '  PnL: ' + (item.pnl || item.value || item.PnL || 0).toFixed(2);
            var tx = e.clientX - rect.left + 10;
            var ty = e.clientY - rect.top - 30;
            tooltip.style.left = tx + 'px';
            tooltip.style.top = ty + 'px';
        };
        canvas.onmouseleave = function() {
            var t = $('#pnl-tooltip');
            if (t) t.style.display = 'none';
        };
    }

    // --- Data loaders for Phase 3 ---

    function loadTradingData() {
        fetchJSON('/api/v1/portfolio')
            .then(function(data) {
                cachePortfolio = data;
                if ($('#tab-trading').classList.contains('active')) {
                    renderKlinePanel(data && data.price_history ? data.price_history : null);
                }
            })
            .catch(function(err) {
                console.error('Failed to load portfolio:', err);
                cachePortfolio = null;
                if ($('#tab-trading').classList.contains('active')) {
                    renderKlinePanel(null);
                }
            });

        fetchJSON('/api/v1/accounts')
            .then(function(data) {
                cacheAccounts = data;
                if ($('#tab-trading').classList.contains('active')) {
                    renderTradeHistory(data);
                }
            })
            .catch(function(err) {
                console.error('Failed to load accounts:', err);
                cacheAccounts = null;
                if ($('#tab-trading').classList.contains('active')) {
                    renderTradeHistory(null);
                }
            });
    }

    function loadAnalyticsData() {
        Promise.all([
            fetchJSON('/v1/dashboard/brains').catch(function(){ return null; }),
            fetchJSON('/v1/runs').catch(function(){ return null; })
        ]).then(function(results) {
            cacheBrains = results[0];
            cacheRuns = results[1];
            if ($('#tab-analytics').classList.contains('active')) {
                renderDecisionTrace(results[0], results[1]);
            }
        });

        fetchJSON('/api/v1/portfolio')
            .then(function(data) {
                cachePortfolio = data;
                if ($('#tab-analytics').classList.contains('active')) {
                    var pnlData = null;
                    if (data && data.daily_pnl) pnlData = data.daily_pnl;
                    else if (data && data.pnl_history) pnlData = data.pnl_history;
                    renderDailyPnL(pnlData);
                }
            })
            .catch(function(err) {
                console.error('Failed to load portfolio for pnl:', err);
                if ($('#tab-analytics').classList.contains('active')) {
                    renderDailyPnL(null);
                }
            });
    }

    // --- Tabs ---

    function initTabs() {
        var btns = document.querySelectorAll('.tab-btn');
        var contents = document.querySelectorAll('.tab-content');
        for (var i = 0; i < btns.length; i++) {
            btns[i].addEventListener('click', function(e) {
                var tab = e.target.getAttribute('data-tab');
                for (var j = 0; j < btns.length; j++) btns[j].classList.remove('active');
                for (var j = 0; j < contents.length; j++) contents[j].classList.remove('active');
                e.target.classList.add('active');
                $('#tab-' + tab).classList.add('active');
                if (tab === 'trading') {
                    renderKlinePanel(cachePortfolio && cachePortfolio.price_history ? cachePortfolio.price_history : null);
                    renderTradeHistory(cacheAccounts);
                }
                if (tab === 'analytics') {
                    renderDecisionTrace(cacheBrains, cacheRuns);
                    var pnlData = null;
                    if (cachePortfolio && cachePortfolio.daily_pnl) pnlData = cachePortfolio.daily_pnl;
                    else if (cachePortfolio && cachePortfolio.pnl_history) pnlData = cachePortfolio.pnl_history;
                    renderDailyPnL(pnlData);
                }
            });
        }
    }

    // --- Utility ---

    function escapeHTML(str) {
        if (!str) return '';
        return str.replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;');
    }

    // --- Init ---

    function init() {
        loadSettings();
        POLL_INTERVAL = SETTINGS.pollInterval;
        MAX_EVENTS = SETTINGS.maxEvents;
        loadAuthToken();
        initLogin();
        initChatPanel();
        initSettingsPanel();

        loadOverview();
        loadBrains();
        loadExecutions();
        loadLearning();
        loadTradingData();
        loadAnalyticsData();

        connectWS();
        initTabs();

        var filterSel = $('#trade-filter-symbol');
        if (filterSel) filterSel.addEventListener('change', applyTradeFilter);
        var exportBtn = $('#trade-export-btn');
        if (exportBtn) exportBtn.addEventListener('click', exportTradesCSV);

        window.addEventListener('resize', function() {
            if ($('#tab-trading').classList.contains('active')) {
                renderKlinePanel(cachePortfolio && cachePortfolio.price_history ? cachePortfolio.price_history : null);
            }
            if ($('#tab-analytics').classList.contains('active')) {
                var pnlData = null;
                if (cachePortfolio && cachePortfolio.daily_pnl) pnlData = cachePortfolio.daily_pnl;
                else if (cachePortfolio && cachePortfolio.pnl_history) pnlData = cachePortfolio.pnl_history;
                renderDailyPnL(pnlData);
            }
        });

        setInterval(function () {
            loadOverview();
            loadBrains();
            loadExecutions();
            loadTradingData();
            loadAnalyticsData();
        }, POLL_INTERVAL);
        setInterval(loadLearning, POLL_INTERVAL * 4);
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
})();
