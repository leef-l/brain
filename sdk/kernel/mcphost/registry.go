// registry.go — MCPToolRegistry for mcp-backed brains.
//
// MCPToolRegistry holds all tools discovered from MCP servers, tracks which
// adapter owns each tool, and handles name-conflict resolution.
//
// Design reference: 35-MCP-backed-Runtime设计.md §3.4
package mcphost

import (
	"fmt"
	"sync"

	"github.com/leef-l/brain/sdk/tool"
)

// mcpToolEntry tracks a single tool and its owning adapter.
type mcpToolEntry struct {
	schema      tool.Schema
	adapterName string
}

// MCPToolRegistry is a thread-safe registry of tools discovered from MCP
// servers. It is separate from the kernel's global tool.Registry — BrainHost
// merges MCPToolRegistry contents into the global registry at startup.
type MCPToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]*mcpToolEntry // key = prefixed tool name
	order []string                 // insertion order for deterministic iteration
}

// NewMCPToolRegistry creates an empty MCPToolRegistry.
func NewMCPToolRegistry() *MCPToolRegistry {
	return &MCPToolRegistry{
		tools: make(map[string]*mcpToolEntry),
	}
}

// RegisterFromAdapter registers all tool schemas discovered from a single
// adapter. If a tool name already exists, it is skipped (first-registered
// wins). Returns the count of registered and skipped tools.
func (r *MCPToolRegistry) RegisterFromAdapter(adapterName string, schemas []tool.Schema) (registered int, skipped int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, schema := range schemas {
		if _, exists := r.tools[schema.Name]; exists {
			skipped++
			continue
		}
		r.tools[schema.Name] = &mcpToolEntry{
			schema:      schema,
			adapterName: adapterName,
		}
		r.order = append(r.order, schema.Name)
		registered++
	}
	return
}

// ReRegisterAdapter replaces all tools owned by the named adapter with new
// schemas. This is used after an MCP server restart when the tool set may
// have changed.
func (r *MCPToolRegistry) ReRegisterAdapter(adapterName string, schemas []tool.Schema) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Remove old entries for this adapter.
	newOrder := make([]string, 0, len(r.order))
	for _, name := range r.order {
		if entry, ok := r.tools[name]; ok && entry.adapterName == adapterName {
			delete(r.tools, name)
		} else {
			newOrder = append(newOrder, name)
		}
	}
	r.order = newOrder

	// Add new entries.
	for _, schema := range schemas {
		if _, exists := r.tools[schema.Name]; exists {
			continue // conflict with another adapter's tool
		}
		r.tools[schema.Name] = &mcpToolEntry{
			schema:      schema,
			adapterName: adapterName,
		}
		r.order = append(r.order, schema.Name)
	}
}

// Lookup returns the tool schema and adapter name for the given prefixed tool
// name. Returns false if not found.
func (r *MCPToolRegistry) Lookup(name string) (tool.Schema, string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.tools[name]
	if !ok {
		return tool.Schema{}, "", false
	}
	return entry.schema, entry.adapterName, true
}

// AllSchemas returns all registered tool schemas in insertion order.
func (r *MCPToolRegistry) AllSchemas() []tool.Schema {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]tool.Schema, 0, len(r.order))
	for _, name := range r.order {
		if entry, ok := r.tools[name]; ok {
			out = append(out, entry.schema)
		}
	}
	return out
}

// Len returns the number of registered tools.
func (r *MCPToolRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// AdapterToolCount returns the number of tools owned by the named adapter.
func (r *MCPToolRegistry) AdapterToolCount(adapterName string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, entry := range r.tools {
		if entry.adapterName == adapterName {
			count++
		}
	}
	return count
}

// String returns a debug representation.
func (r *MCPToolRegistry) String() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return fmt.Sprintf("MCPToolRegistry{tools=%d}", len(r.tools))
}
