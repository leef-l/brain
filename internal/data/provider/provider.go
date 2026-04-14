package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/leef-l/brain/internal/data/model"
)

var ErrNotFound = errors.New("provider not found")

type Provider interface {
	Name() string
	State() string
	Health(context.Context) model.ProviderHealth
	Next(context.Context) (model.MarketEvent, bool, error)
}

type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

func (r *Registry) Register(p Provider) error {
	if p == nil {
		return errors.New("nil provider")
	}
	name := p.Name()
	if name == "" {
		return errors.New("provider name is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("provider %q already registered", name)
	}
	r.providers[name] = p
	return nil
}

func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p)
	}
	return out
}

func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.providers)
}

type StaticProvider struct {
	name   string
	state  string
	events []model.MarketEvent
	index  int
}

func NewStaticProvider(name string, events ...model.MarketEvent) *StaticProvider {
	return &StaticProvider{
		name:   name,
		state:  model.ProviderStateActive,
		events: append([]model.MarketEvent(nil), events...),
	}
}

func (p *StaticProvider) Name() string { return p.name }

func (p *StaticProvider) State() string {
	if p.state == "" {
		return model.ProviderStateStopped
	}
	return p.state
}

func (p *StaticProvider) Health(context.Context) model.ProviderHealth {
	return model.ProviderHealth{
		Name:  p.name,
		State: p.State(),
	}
}

func (p *StaticProvider) Next(context.Context) (model.MarketEvent, bool, error) {
	if p.index >= len(p.events) {
		return model.MarketEvent{}, false, nil
	}
	ev := p.events[p.index]
	p.index++
	return ev, true, nil
}

func (p *StaticProvider) WithState(state string) *StaticProvider {
	p.state = state
	return p
}
