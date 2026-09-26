package core

import "sync"

type registryKey struct {
	channel Channel
	account string
}

// Registry holds adapters keyed by (channel, account) so the Service can
// dispatch write ops to the right one without knowing concrete types.
type Registry struct {
	mu       sync.RWMutex
	adapters map[registryKey]Adapter
}

// NewRegistry returns an empty Registry ready to use.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[registryKey]Adapter)}
}

// Register adds or replaces the adapter for its (Channel, Account).
func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[registryKey{a.Channel(), a.Account()}] = a
}

// Get returns the adapter registered for (channel, account), if any.
func (r *Registry) Get(channel Channel, account string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[registryKey{channel, account}]
	return a, ok
}

// List returns every registered adapter, in no particular order.
func (r *Registry) List() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		out = append(out, a)
	}
	return out
}
