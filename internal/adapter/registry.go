package adapter

import (
	"fmt"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
)

// Registry manages all registered platform adapters.
// Thread-safe for concurrent access.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]PlatformAdapter
}

// NewRegistry creates an empty adapter registry.
func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[string]PlatformAdapter),
	}
}

// Register adds a platform adapter to the registry.
// Panics if an adapter with the same name is already registered.
func (r *Registry) Register(a PlatformAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := a.Name()
	if _, exists := r.adapters[name]; exists {
		panic(fmt.Sprintf("adapter already registered: %s", name))
	}
	r.adapters[name] = a
}

// Get returns an adapter by platform name, or nil if not found.
func (r *Registry) Get(name string) PlatformAdapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.adapters[name]
}

// List returns all registered adapters.
func (r *Registry) List() []PlatformAdapter {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]PlatformAdapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		result = append(result, a)
	}
	return result
}

// AllTools returns all MCP tools from all registered adapters.
func (r *Registry) AllTools() []mcp.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var tools []mcp.Tool
	for _, a := range r.adapters {
		tools = append(tools, a.Tools()...)
	}
	return tools
}

// FindAdapter returns the adapter that owns a given tool name.
// Tool names are prefixed: "github_list_repos" → adapter "github", tool "list_repos".
func (r *Registry) FindAdapter(toolName string) (PlatformAdapter, string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, a := range r.adapters {
		prefix := a.Name() + "_"
		if len(toolName) > len(prefix) && toolName[:len(prefix)] == prefix {
			return a, toolName[len(prefix):], true
		}
	}
	return nil, "", false
}
