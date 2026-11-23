// Package channels owns the notification-channel registry and the dispatch
// entry point. Concrete channels are supplied as factories that receive the
// current *gorm.DB, so the domain layer never imports a specific provider.
package channels

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"smallgo/server/reminder/core"

	"gorm.io/gorm"
)

// Factory builds a notifier bound to the current database handle. Factories are
// cheap; a fresh instance is created per dispatch so request-scoped state can
// never leak between deliveries.
type Factory func(db *gorm.DB) core.Notifier

// Registry maps a channel name to its factory.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// Register adds or replaces the factory for a channel. Registration happens at
// startup (single goroutine), but the mutex keeps tests and hot reload safe.
func (r *Registry) Register(channel string, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[channel] = factory
}

// Resolve returns the notifier for a channel, or nil when none is registered.
func (r *Registry) Resolve(db *gorm.DB, channel string) core.Notifier {
	r.mu.RLock()
	factory := r.factories[channel]
	r.mu.RUnlock()
	if factory == nil {
		return nil
	}
	return factory(db)
}

// Channels lists the registered channel names in stable order (useful for
// diagnostics and tests).
func (r *Registry) Channels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Dispatcher sends an outbound message through the registry.
type Dispatcher struct {
	registry *Registry
}

func NewDispatcher(registry *Registry) *Dispatcher {
	if registry == nil {
		registry = NewRegistry()
	}
	return &Dispatcher{registry: registry}
}

// Dispatch resolves the channel's notifier and invokes it.
func (d *Dispatcher) Dispatch(ctx context.Context, db *gorm.DB, msg core.OutboundMessage) (core.SendResult, error) {
	notifier := d.registry.Resolve(db, msg.Channel)
	if notifier == nil {
		return core.SendResult{}, fmt.Errorf("未注册的通知渠道: %s", msg.Channel)
	}
	return notifier.Send(ctx, msg)
}
