package execution

import "context"

// Backend executes trading intents against a concrete venue or simulator.
type Backend interface {
	Name() string
	Execute(ctx context.Context, state *MemoryState, intent OrderIntent) (ExecutionResult, error)
}

// TickProcessor is implemented by backends that can evaluate open orders
// on new market data.
type TickProcessor interface {
	ProcessPriceTick(ctx context.Context, state *MemoryState, symbol string, markPrice float64) ([]ExecutionResult, error)
}

// Executor is the public interface used by callers that only care about
// sending intents and receiving execution results.
type Executor interface {
	Execute(ctx context.Context, intent OrderIntent) (ExecutionResult, error)
}
