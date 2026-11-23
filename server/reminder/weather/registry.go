// Package weather holds the provider-neutral weather contract and registry.
// The concrete QWeather client stays in the reminder package (its DTOs are
// shared with persistence) and registers an adapter at startup.
package weather

import (
	"smallgo/server/reminder/core"
	"sync"
)

// ProviderFactory builds a provider for one account's credentials.
type ProviderFactory func(apiKey, host string) core.WeatherProvider

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

func (r *Registry) Resolve(name, apiKey, host string) core.WeatherProvider {
	r.mu.RLock()
	factory := r.factories[name]
	r.mu.RUnlock()
	if factory == nil {
		return nil
	}
	return factory(apiKey, host)
}
