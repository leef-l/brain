// Brain Control Plane - Dashboard SPA

(function () {
    'use strict';

    const MAX_EVENTS = 100;
    const POLL_INTERVAL = 10000; // 10s
    const SSE_RECONNECT_DELAY = 3000;

    let eventCount = 0;
    let sseSource = null;

    // --- DOM Helpers ---

    function $(sel) { return document.querySelector(sel); }
    function setText(sel, text) { $(sel).textContent = text; }

    function formatTime(ts) {
        if (!ts) return '';
        const d = new Date(ts);
        return d.toLocaleTimeString('en-US', { hour12: false });
    }

    function formatUptime(startISO) {
        const start = new Date(startISO);
        const diff = Math.floor((Date.now() - start.getTime()) / 1000);
        const h = Math.floor(diff / 3600);
        const m = Math.floor((diff % 3600) / 60);
        const s = diff % 60;
        return 'Uptime: ' + h + 'h ' + m + 'm ' + s + 's';
    }

    function truncate(str, max) {
        if (!str) return '';
        return str.length > max ? str.substring(0, max) + '...' : str;
    }

    function setConnectionStatus(status) {
        const el = $('#connection-status');
        el.textContent = status;
        el.className = 'status-indicator';
        if (status === 'Connected') el.classList.add('connected');
        else if (status === 'Disconnected') el.classList.add('disconnected');
    }

    // --- API Fetchers ---

    function fetchJSON(url) {
        return fetch(url).then(function (res) {
            if (!res.ok) throw new Error('HTTP ' + res.status);
            return res.json();
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

                // Also fetch runs to compute completed/failed counts
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
                    html += '<div class="brain-card">'
                        + '<div>'
                        + '<span class="brain-status ' + statusClass + '"></span>'
                        + '<span class="brain-kind">' + escapeHTML(b.kind) + '</span>'
                        + '<div class="brain-meta">' + statusText
                        + (b.binary ? ' &middot; ' + escapeHTML(truncate(b.binary, 40)) : '')
                        + '</div>'
                        + '</div>'
                        + '</div>';
                }
                container.innerHTML = html;
            })
            .catch(function (err) {
                console.error('Failed to load brains:', err);
                $('#brains-list').innerHTML = '<div class="placeholder">Failed to load brains</div>';
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

                // Sort by created_at descending
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
                $('#executions-list').innerHTML = '<div class="placeholder">Failed to load executions</div>';
            });
    }

    // --- SSE Events ---

    function connectSSE() {
        if (sseSource) {
            sseSource.close();
        }

        sseSource = new EventSource('/v1/dashboard/events');

        sseSource.onopen = function () {
            setConnectionStatus('Connected');
        };

        sseSource.onmessage = function (e) {
            try {
                var ev = JSON.parse(e.data);
                addEvent(ev);
            } catch (err) {
                addEvent({ type: 'raw', detail: e.data, timestamp: new Date().toISOString() });
            }
        };

        sseSource.onerror = function () {
            setConnectionStatus('Disconnected');
            sseSource.close();
            sseSource = null;
            // Auto reconnect
            setTimeout(connectSSE, SSE_RECONNECT_DELAY);
        };
    }

    function addEvent(ev) {
        var container = $('#events-list');

        // Remove placeholder if present
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

        // Insert at top
        container.insertBefore(div, container.firstChild);

        // Limit event count
        eventCount++;
        while (container.children.length > MAX_EVENTS) {
            container.removeChild(container.lastChild);
            eventCount--;
        }

        setText('#event-count', eventCount);
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
        // Initial load
        loadOverview();
        loadBrains();
        loadExecutions();

        // SSE connection
        connectSSE();

        // Periodic polling
        setInterval(function () {
            loadOverview();
            loadBrains();
            loadExecutions();
        }, POLL_INTERVAL);
    }

    // Start when DOM ready
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
})();
