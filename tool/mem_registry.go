package tool

import (
	"sort"
	"sync"

	brainerrors "github.com/leef-l/brain/errors"
)

// MemRegistry is an in-memory implementation of Registry as described in
// 02-BrainKernel设计.md §6.1. It stores tools in a synchronized map and
// supports lookup, listing, and filtering by brain kind.
type MemRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool // key = tool.Name()
}

// NewMemRegistry constructs an empty MemRegistry with no registered tools.
// Tools are added via Register during Brain bootstrap.
func NewMemRegistry() *MemRegistry {
	return &MemRegistry{
		tools: make(map[string]Tool),
	}
}

// Register adds a Tool to the registry. It enforces three invariants:
//   - Tool name must not already exist (duplicate detection)
//   - Tool.Name() must not be empty
//   - Tool.Schema().Name must match Tool.Name()
//
// If any invariant is violated, a BrainError with ClassInternalBug is returned.
// On success, Register returns nil. See 02-BrainKernel设计.md §6.1.
func (r *MemRegistry) Register(t Tool) error {
	if t == nil {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("register called with nil tool"),
		)
	}

	name := t.Name()
	if name == "" {
		return brainerrors.New(brainerrors.CodeToolInputInvalid,
			brainerrors.WithMessage("tool name must not be empty"),
		)
	}

	schema := t.Schema()
	if schema.Name != name {
		return brainerrors.New(brainerrors.CodeToolInputInvalid,
			brainerrors.WithMessage("tool schema name does not match tool name: "+name),
		)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[name]; exists {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("tool already registered: "+name),
		)
	}

	r.tools[name] = t
	return nil
}

// Lookup returns the Tool registered under name and a found flag. If the tool
// is not found, it returns (nil, false). See 02-BrainKernel设计.md §6.1.
func (r *MemRegistry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, exists := r.tools[name]
	return tool, exists
}

// List returns all registered Tools in lexicographic order by Tool.Name().
// The returned slice is a copy and safe for modification by the caller.
// See 02-BrainKernel设计.md §6.1.
func (r *MemRegistry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.tools) == 0 {
		return []Tool{}
	}

	// Extract names and sort them
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	// Build result slice in sorted order
	result := make([]Tool, len(names))
	for i, name := range names {
		result[i] = r.tools[name]
	}
	return result
}

// ListByBrain returns every Tool whose Schema().Brain equals brainKind.
// If brainKind is an empty string, all registered Tools are returned
// in lexicographic order. The returned slice is a copy and safe for
// modification by the caller. See 02-BrainKernel设计.md §6.1.
func (r *MemRegistry) ListByBrain(brainKind string) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var filtered []Tool
	for _, tool := range r.tools {
		schema := tool.Schema()
		if brainKind == "" || schema.Brain == brainKind {
			filtered = append(filtered, tool)
		}
	}

	// Sort by name for consistent ordering
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Name() < filtered[j].Name()
	})

	return filtered
}
