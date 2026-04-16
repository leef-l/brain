package execution

import (
	"context"
	"fmt"
)

// Client ties a backend to a mutable execution state.
type Client struct {
	backend Backend
	state   *MemoryState
}

// ClientOption configures a Client.
type ClientOption func(*Client)

func NewClient(backend Backend, opts ...ClientOption) *Client {
	client := &Client{
		backend: backend,
		state:   NewMemoryState(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(client)
		}
	}
	return client
}

func WithClientState(state *MemoryState) ClientOption {
	return func(c *Client) {
		if state != nil {
			c.state = state
		}
	}
}

// WithIDPrefix sets the account-specific ID prefix for order IDs.
// This ensures globally unique order IDs across multiple paper exchanges.
func WithIDPrefix(prefix string) ClientOption {
	return func(c *Client) {
		if prefix != "" {
			c.state = NewMemoryStateWithPrefix(prefix)
		}
	}
}

func (c *Client) Execute(ctx context.Context, intent OrderIntent) (ExecutionResult, error) {
	if c == nil || c.backend == nil {
		return ExecutionResult{}, fmt.Errorf("execution client is not initialized")
	}
	if c.state == nil {
		c.state = NewMemoryState()
	}
	return c.backend.Execute(ctx, c.state, intent)
}

func (c *Client) Snapshot() StateSnapshot {
	if c == nil || c.state == nil {
		return StateSnapshot{}
	}
	return c.state.Snapshot()
}

func (c *Client) State() *MemoryState {
	if c == nil {
		return nil
	}
	if c.state == nil {
		c.state = NewMemoryState()
	}
	return c.state
}

func (c *Client) ProcessPriceTick(ctx context.Context, symbol string, markPrice float64) ([]ExecutionResult, error) {
	if c == nil || c.backend == nil {
		return nil, fmt.Errorf("execution client is not initialized")
	}
	processor, ok := c.backend.(TickProcessor)
	if !ok {
		return nil, fmt.Errorf("backend %q does not support price ticks", c.backend.Name())
	}
	if c.state == nil {
		c.state = NewMemoryState()
	}
	return processor.ProcessPriceTick(ctx, c.state, symbol, markPrice)
}
