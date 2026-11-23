package calendar

import (
	"smallgo/server/reminder/core"
	"sync"
)

// ProviderFactory builds a provider for one set of credentials. Calendars are
// account-scoped, so the factory (not a long-lived instance) is what the app
// registers at startup.
type ProviderFactory func(username, password string) core.CalendarProvider

// Registry holds the available calendar backends.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]ProviderFactory
}

func NewRegistry() *Registry { return &Registry{factories: make(map[string]ProviderFactory)} }

func (r *Registry) Register(name string, factory ProviderFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = factory
}

// Resolve returns the named provider, or nil when unknown.
func (r *Registry) Resolve(name, username, password string) core.CalendarProvider {
	r.mu.RLock()
	factory := r.factories[name]
	r.mu.RUnlock()
	if factory == nil {
		return nil
	}
	return factory(username, password)
}
