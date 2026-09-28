package adapters

import (
	"sort"
	"sync"
)

// Factory constructs a fresh Adapter for one session.
type Factory func() Adapter

// Registry maps an agent_type (the wire string on Task.AgentType) to the
// adapter that implements it. It is safe for concurrent use.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// NewRegistry returns an empty registry. Most callers use Default.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// Register binds agentType to a factory. A later registration replaces an
// earlier one.
func (r *Registry) Register(agentType string, f Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[agentType] = f
}

// Get constructs an adapter for agentType, reporting whether one is registered.
func (r *Registry) Get(agentType string) (Adapter, bool) {
	r.mu.RLock()
	f, ok := r.factories[agentType]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return f(), true
}

// Types returns the registered agent_type keys, sorted.
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.factories))
	for k := range r.factories {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Default is the process-wide registry. Concrete adapters register themselves
// here from an init() function (see internal/adapters/fake).
var Default = NewRegistry()
