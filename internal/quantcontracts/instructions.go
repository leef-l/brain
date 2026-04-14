package quantcontracts

const (
	InstructionCollectReviewInput = "collect_review_input"
	InstructionReplayStart        = "replay_start"
	InstructionReplayStop         = "replay_stop"
	InstructionShutdownPrepare    = "shutdown_prepare"

	InstructionDailyReviewRun  = "daily_review_run"
	InstructionHealthCheck     = "health_check"
	InstructionEmergencyAction = "emergency_action"
	InstructionUpdateConfig    = "update_config"
)

func DataInstructions() []string {
	return []string{
		InstructionCollectReviewInput,
		InstructionReplayStart,
		InstructionReplayStop,
		InstructionShutdownPrepare,
		InstructionHealthCheck,
	}
}

func QuantInstructions() []string {
	return []string{
		InstructionCollectReviewInput,
		InstructionShutdownPrepare,
		InstructionHealthCheck,
	}
}

func CentralInstructions() []string {
	return []string{
		InstructionDailyReviewRun,
		InstructionHealthCheck,
		InstructionEmergencyAction,
		InstructionUpdateConfig,
	}
}
