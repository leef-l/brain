package quantcontracts

const (
	ToolDataGetSnapshot        = "data.get_snapshot"
	ToolDataGetFeatureVector   = "data.get_feature_vector"
	ToolDataGetCandles         = "data.get_candles"
	ToolDataGetSimilarPatterns = "data.get_similar_patterns"
	ToolDataProviderHealth     = "data.provider_health"

	ToolQuantGlobalPortfolio  = "quant.global_portfolio"
	ToolQuantTraceQuery       = "quant.trace_query"
	ToolQuantPauseTrading     = "quant.pause_trading"
	ToolQuantResumeTrading    = "quant.resume_trading"
	ToolQuantPauseInstrument  = "quant.pause_instrument"
	ToolQuantResumeInstrument = "quant.resume_instrument"
	ToolQuantForceClose       = "quant.force_close"

	ToolCentralReviewTrade  = "central.review_trade"
	ToolCentralDataAlert    = "central.data_alert"
	ToolCentralAccountError = "central.account_error"
	ToolCentralMacroEvent   = "central.macro_event"
)

func DataToolNames() []string {
	return []string{
		ToolDataGetSnapshot,
		ToolDataGetFeatureVector,
		ToolDataGetCandles,
		ToolDataGetSimilarPatterns,
		ToolDataProviderHealth,
	}
}

func QuantToolNames() []string {
	return []string{
		ToolQuantGlobalPortfolio,
		ToolQuantTraceQuery,
		ToolQuantPauseTrading,
		ToolQuantResumeTrading,
		ToolQuantPauseInstrument,
		ToolQuantResumeInstrument,
		ToolQuantForceClose,
	}
}

func CentralToolNames() []string {
	return []string{
		ToolCentralReviewTrade,
		ToolCentralDataAlert,
		ToolCentralAccountError,
		ToolCentralMacroEvent,
	}
}
