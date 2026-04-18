// Package mcphost implements the MCP-backed Brain Runtime host.
//
// BrainHost manages N MCP adapters (each connecting to one MCP server) and
// presents a unified tool surface that is compatible with native brains in the
// BrainPool. AdapterManager handles per-adapter lifecycle, health monitoring,
// and status tracking.
package mcphost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/kernel/mcpadapter"
	"github.com/leef-l/brain/sdk/tool"
)

// ---------------------------------------------------------------------------
// MCPServerSpec
// ---------------------------------------------------------------------------

// MCPServerSpec describes the connection configuration for one MCP server.
type MCPServerSpec struct {
	// Name is the unique identifier, e.g. "github", "filesystem".
	Name string

	// BinPath is the path to the MCP server executable.
	BinPath string

	// Args are extra command-line arguments.
	Args []string

	// Env is additional environment variables for the server process.
	Env map[string]string

	// ToolPrefix is prepended to every tool name exposed by this server
	// to prevent collisions. Example: "mcp.github."
	ToolPrefix string

	// AutoStart controls whether this server is started together with the
	// host in StartAll / BrainHost.Start.
	AutoStart bool
}

// ---------------------------------------------------------------------------
// AdapterStatus
// ---------------------------------------------------------------------------

// AdapterStatus represents the runtime status of a single MCP adapter.
type AdapterStatus int

const (
	// AdapterStarting means the adapter is being initialised.
	AdapterStarting AdapterStatus = iota
	// AdapterReady means the adapter completed the MCP handshake and
	// tool discovery.
	AdapterReady
	// AdapterDegraded means the adapter is running but some tools failed.
	AdapterDegraded
	// AdapterFailed means the adapter could not start or crashed.
	AdapterFailed
	// AdapterStopped means the adapter has been intentionally stopped.
	AdapterStopped
)

// String returns a human-readable label for the status.
func (s AdapterStatus) String() string {
	switch s {
	case AdapterStarting:
		return "starting"
	case AdapterReady:
		return "ready"
	case AdapterDegraded:
		return "degraded"
	case AdapterFailed:
		return "failed"
	case AdapterStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// AdapterHealthEvent
// ---------------------------------------------------------------------------

// AdapterHealthEvent is emitted when an adapter's status changes.
type AdapterHealthEvent struct {
	ServerName string
	OldStatus  AdapterStatus
	NewStatus  AdapterStatus
	Error      error
	Timestamp  time.Time
}

// ---------------------------------------------------------------------------
// adapterEntry (internal bookkeeping per adapter)
// ---------------------------------------------------------------------------

type adapterEntry struct {
	spec    MCPServerSpec
	adapter *mcpadapter.Adapter
	status  AdapterStatus
	schemas []tool.Schema
}

// ---------------------------------------------------------------------------
// AdapterManager
// ---------------------------------------------------------------------------

// AdapterManager manages the lifecycle of N mcpadapter.Adapter instances.
type AdapterManager struct {
	mu       sync.RWMutex
	entries  map[string]*adapterEntry
	order    []string // insertion-order for deterministic iteration
	eventsCh chan AdapterHealthEvent
}

// NewAdapterManager creates an AdapterManager.
// healthBuf sets the buffer size of the internal health-event channel (0 is
// fine for fire-and-forget usage).
func NewAdapterManager(healthBuf int) *AdapterManager {
	if healthBuf < 0 {
		healthBuf = 0
	}
	return &AdapterManager{
		entries:  make(map[string]*adapterEntry),
		eventsCh: make(chan AdapterHealthEvent, healthBuf),
	}
}

// HealthEvents returns a read-only channel that receives adapter status
// change notifications.
func (m *AdapterManager) HealthEvents() <-chan AdapterHealthEvent {
	return m.eventsCh
}

// emitEvent is a best-effort send to the health channel.
func (m *AdapterManager) emitEvent(ev AdapterHealthEvent) {
	select {
	case m.eventsCh <- ev:
	default:
		// drop if channel full — caller should drain
	}
}

// AddServer registers a new MCP server spec. It does not start the adapter.
func (m *AdapterManager) AddServer(spec MCPServerSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if spec.Name == "" {
		return fmt.Errorf("mcphost: server name must not be empty")
	}
	if _, exists := m.entries[spec.Name]; exists {
		return fmt.Errorf("mcphost: duplicate server name %q", spec.Name)
	}

	env := envMapToSlice(spec.Env)
	adapter := &mcpadapter.Adapter{
		BinPath:    spec.BinPath,
		Args:       spec.Args,
		Env:        env,
		ToolPrefix: spec.ToolPrefix,
	}

	m.entries[spec.Name] = &adapterEntry{
		spec:    spec,
		adapter: adapter,
		status:  AdapterStopped,
	}
	m.order = append(m.order, spec.Name)
	return nil
}

// addEntry is an internal helper used by BrainHost to inject a pre-built
// adapterEntry (e.g. with a custom *mcpadapter.Adapter for testing).
func (m *AdapterManager) addEntry(name string, entry *adapterEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[name] = entry
	m.order = append(m.order, name)
}

// StartAll starts every adapter whose spec has AutoStart == true. It tries
// each adapter independently; individual failures are recorded via the health
// event channel and reflected in Status().
func (m *AdapterManager) StartAll(ctx context.Context) error {
	m.mu.RLock()
	names := make([]string, len(m.order))
	copy(names, m.order)
	m.mu.RUnlock()

	var firstErr error
	for _, name := range names {
		m.mu.RLock()
		entry, ok := m.entries[name]
		m.mu.RUnlock()
		if !ok || !entry.spec.AutoStart {
			continue
		}
		if err := m.startOne(ctx, entry); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// startOne launches a single adapter: Start -> DiscoverTools -> update status.
func (m *AdapterManager) startOne(ctx context.Context, entry *adapterEntry) error {
	m.setStatus(entry, AdapterStarting, nil)

	if err := entry.adapter.Start(ctx); err != nil {
		m.setStatus(entry, AdapterFailed, err)
		return err
	}

	schemas, err := entry.adapter.DiscoverTools(ctx)
	if err != nil {
		m.setStatus(entry, AdapterDegraded, err)
		// adapter is running but tool discovery failed
		return err
	}

	m.mu.Lock()
	entry.schemas = schemas
	m.mu.Unlock()

	m.setStatus(entry, AdapterReady, nil)
	return nil
}

// StopAll stops every running adapter.
func (m *AdapterManager) StopAll(ctx context.Context) {
	m.mu.RLock()
	names := make([]string, len(m.order))
	copy(names, m.order)
	m.mu.RUnlock()

	for _, name := range names {
		m.mu.RLock()
		entry, ok := m.entries[name]
		m.mu.RUnlock()
		if !ok {
			continue
		}
		_ = entry.adapter.Stop(ctx)
		m.setStatus(entry, AdapterStopped, nil)
	}
}

// Status returns the current AdapterStatus of the named server.
func (m *AdapterManager) Status(name string) (AdapterStatus, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.entries[name]
	if !ok {
		return AdapterStopped, false
	}
	return entry.status, true
}

// ServerNames returns the names of all registered servers in insertion order.
func (m *AdapterManager) ServerNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(m.order))
	copy(out, m.order)
	return out
}

// setStatus updates the status and emits a health event.
func (m *AdapterManager) setStatus(entry *adapterEntry, newStatus AdapterStatus, err error) {
	m.mu.Lock()
	old := entry.status
	entry.status = newStatus
	name := entry.spec.Name
	m.mu.Unlock()

	if old != newStatus {
		m.emitEvent(AdapterHealthEvent{
			ServerName: name,
			OldStatus:  old,
			NewStatus:  newStatus,
			Error:      err,
			Timestamp:  time.Now(),
		})
	}
}

// allSchemas returns a merged slice of tool schemas from all ready adapters.
func (m *AdapterManager) allSchemas() []tool.Schema {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []tool.Schema
	for _, name := range m.order {
		entry := m.entries[name]
		if entry.status == AdapterReady || entry.status == AdapterDegraded {
			out = append(out, entry.schemas...)
		}
	}
	return out
}

// findAdapter locates the adapter that owns a prefixed tool name and returns
// the adapter + the unprefixed MCP tool name.
func (m *AdapterManager) findAdapter(toolName string) (*mcpadapter.Adapter, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, name := range m.order {
		entry := m.entries[name]
		prefix := entry.spec.ToolPrefix
		if prefix != "" && strings.HasPrefix(toolName, prefix) {
			return entry.adapter, strings.TrimPrefix(toolName, prefix), nil
		}
	}
	return nil, "", fmt.Errorf("mcphost: no adapter owns tool %q", toolName)
}

// ---------------------------------------------------------------------------
// BrainHealthStatus
// ---------------------------------------------------------------------------

// BrainHealthStatus represents the aggregated health of a BrainHost.
type BrainHealthStatus int

const (
	// BrainHealthHealthy means all MCP adapters are ready.
	BrainHealthHealthy BrainHealthStatus = iota
	// BrainHealthDegraded means some adapters are degraded/failed but at
	// least one is still ready.
	BrainHealthDegraded
	// BrainHealthUnhealthy means all adapters have failed or none exist.
	BrainHealthUnhealthy
)

// String returns a human-readable label for the health status.
func (s BrainHealthStatus) String() string {
	switch s {
	case BrainHealthHealthy:
		return "healthy"
	case BrainHealthDegraded:
		return "degraded"
	case BrainHealthUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// AdapterStatusSnapshot — per-adapter status for Dashboard
// ---------------------------------------------------------------------------

// AdapterStatusSnapshot is a point-in-time view of a single MCP adapter's
// state, suitable for JSON serialization in Dashboard responses.
type AdapterStatusSnapshot struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	ToolsCount int    `json:"tools_count"`
}

// AggregateStatus returns the overall health status across all adapters.
// Rules (from design doc §7.4):
//   - All adapters Ready → Healthy
//   - Some Degraded, rest Ready → Degraded
//   - All Failed → Unhealthy
//   - No adapters → Unhealthy
func (m *AdapterManager) AggregateStatus() BrainHealthStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := len(m.entries)
	if total == 0 {
		return BrainHealthUnhealthy
	}

	readyCount, failedCount := 0, 0
	for _, entry := range m.entries {
		switch entry.status {
		case AdapterReady:
			readyCount++
		case AdapterFailed, AdapterStopped:
			failedCount++
		}
	}

	if failedCount == total {
		return BrainHealthUnhealthy
	}
	if readyCount == total {
		return BrainHealthHealthy
	}
	return BrainHealthDegraded
}

// StatusSnapshot returns a slice of per-adapter status snapshots suitable
// for Dashboard JSON responses.
func (m *AdapterManager) StatusSnapshot() []AdapterStatusSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]AdapterStatusSnapshot, 0, len(m.order))
	for _, name := range m.order {
		entry, ok := m.entries[name]
		if !ok {
			continue
		}
		out = append(out, AdapterStatusSnapshot{
			Name:       name,
			Status:     entry.status.String(),
			ToolsCount: len(entry.schemas),
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// BrainHost
// ---------------------------------------------------------------------------

// BrainHost is the runtime host for an MCP-backed brain. It wraps an
// AdapterManager and MCPToolRegistry, exposing a tool surface that is
// compatible with the BrainPool's expectations for native brains.
type BrainHost struct {
	manager  *AdapterManager
	registry *MCPToolRegistry
}

// NewBrainHost creates a BrainHost with the given AdapterManager.
func NewBrainHost(manager *AdapterManager) *BrainHost {
	return &BrainHost{
		manager:  manager,
		registry: NewMCPToolRegistry(),
	}
}

// Manager returns the underlying AdapterManager for direct access.
func (h *BrainHost) Manager() *AdapterManager {
	return h.manager
}

// Registry returns the MCPToolRegistry.
func (h *BrainHost) Registry() *MCPToolRegistry {
	return h.registry
}

// Start launches all auto-start MCP servers and populates the tool registry.
func (h *BrainHost) Start(ctx context.Context) error {
	// Start adapters (non-fatal errors are recorded via health events).
	_ = h.manager.StartAll(ctx)

	// Populate registry from all adapters that have discovered tools.
	h.rebuildRegistry()

	return nil
}

// rebuildRegistry re-populates the MCPToolRegistry from the AdapterManager's
// current tool schemas.
func (h *BrainHost) rebuildRegistry() {
	h.manager.mu.RLock()
	defer h.manager.mu.RUnlock()

	for _, name := range h.manager.order {
		entry := h.manager.entries[name]
		if entry.status == AdapterReady || entry.status == AdapterDegraded {
			h.registry.RegisterFromAdapter(name, entry.schemas)
		}
	}
}

// Stop shuts down all MCP servers.
func (h *BrainHost) Stop(ctx context.Context) {
	h.manager.StopAll(ctx)
}

// ToolSchemas returns the merged tool schemas from all ready MCP adapters.
func (h *BrainHost) ToolSchemas() []tool.Schema {
	return h.manager.allSchemas()
}

// InvokeTool routes a tool call to the correct MCP adapter based on the
// tool name prefix, then forwards the call. Optionally validates arguments
// against the tool's inputSchema before forwarding.
func (h *BrainHost) InvokeTool(ctx context.Context, toolName string, args json.RawMessage) (*tool.Result, error) {
	// Validate args if schema is available in registry.
	if schema, _, ok := h.registry.Lookup(toolName); ok && len(schema.InputSchema) > 0 {
		if err := ValidateMCPArgs(schema.InputSchema, args); err != nil {
			return nil, fmt.Errorf("mcphost: argument validation failed for %s: %w", toolName, err)
		}
	}

	adapter, mcpName, err := h.manager.findAdapter(toolName)
	if err != nil {
		return nil, err
	}
	return adapter.Invoke(ctx, mcpName, args)
}

// HealthStatus returns the aggregated health status of all MCP adapters.
func (h *BrainHost) HealthStatus() BrainHealthStatus {
	return h.manager.AggregateStatus()
}

// HealthSnapshot returns the health status plus per-adapter details,
// suitable for responding to ping requests from BrainPool.
func (h *BrainHost) HealthSnapshot() map[string]interface{} {
	return map[string]interface{}{
		"status":   h.manager.AggregateStatus().String(),
		"adapters": h.manager.StatusSnapshot(),
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// envMapToSlice converts a map[string]string to []string{"K=V", ...}.
// Returns nil if the map is empty (inherits parent env).
func envMapToSlice(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
