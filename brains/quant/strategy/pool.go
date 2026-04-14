package strategy

import "sort"

// Pool runs a fixed list of strategies against the same market view.
type Pool struct {
	strategies []Strategy
}

func NewPool(strategies ...Strategy) *Pool {
	copied := append([]Strategy(nil), strategies...)
	return &Pool{strategies: copied}
}

func DefaultPool() *Pool {
	return NewPool(
		NewTrendFollower(),
		NewMeanReversion(),
		NewBreakoutMomentum(),
		NewOrderFlow(),
	)
}

func (p *Pool) Strategies() []Strategy {
	return append([]Strategy(nil), p.strategies...)
}

func (p *Pool) Compute(view MarketView) []Signal {
	if p == nil {
		return nil
	}
	out := make([]Signal, 0, len(p.strategies))
	for _, strategy := range p.strategies {
		if strategy == nil {
			continue
		}
		signal := strategy.Compute(view)
		if signal.Strategy == "" {
			signal.Strategy = strategy.Name()
		}
		out = append(out, signal)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Direction == out[j].Direction {
			return out[i].Strategy < out[j].Strategy
		}
		return out[i].Strategy < out[j].Strategy
	})
	return out
}
