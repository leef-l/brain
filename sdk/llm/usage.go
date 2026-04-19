package llm

// Usage is the per-call token and cost accounting returned alongside
// every ChatResponse. See 22-Agent-Loop规格.md §5 and §3 (Prompt Cache
// accounting). The four token counters align with Anthropic's cache
// accounting model and are normalized by each provider adapter.
type Usage struct {
	// InputTokens is the number of uncached input tokens billed.
	InputTokens int `json:"input_tokens,omitempty"`
	// OutputTokens is the number of output tokens billed.
	OutputTokens int `json:"output_tokens,omitempty"`
	// CacheReadTokens is the number of input tokens served from the
	// provider's prompt cache (lower unit price).
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
	// CacheCreationTokens is the number of input tokens written into
	// the provider's prompt cache on this call (higher unit price).
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
	// CostUSD is the provider-reported cost for this call in USD,
	// computed by the adapter from the vendor's price sheet.
	CostUSD float64 `json:"cost_usd,omitempty"`
}
