package tool

// Risk is the tool-call risk taxonomy declared in 02-BrainKernel设计.md §6.
// Every Tool MUST declare a Risk level so that the Guardrail layer and the
// fault_policy escalation matrix (02-BrainKernel设计.md §6.5) can decide
// whether a call requires human confirmation or triggers a higher fault
// policy. Implementations MUST classify every tool into exactly one Risk.
type Risk string

// Risk levels defined in 02-BrainKernel设计.md §6. Values are lowercase
// short names to match the wire format consumed by Guardrail configs and
// observability labels.
const (
	// RiskSafe is a side-effect-free read (e.g. read_file, list_dir).
	// See 02-BrainKernel设计.md §6.
	RiskSafe Risk = "safe"

	// RiskLow is a low-impact mutation inside the sandbox workspace
	// (e.g. write_file under AllowPaths). See 02-BrainKernel设计.md §6.
	RiskLow Risk = "low"

	// RiskMedium is a reversible action that affects shared state
	// (e.g. git commit on a feature branch). See 02-BrainKernel设计.md §6.
	RiskMedium Risk = "med"

	// RiskHigh is an irreversible action (e.g. git push, database write).
	// See 02-BrainKernel设计.md §6.
	RiskHigh Risk = "high"

	// RiskCritical is an action that escapes the sandbox entirely
	// (e.g. production deploy, money transfer). See 02-BrainKernel设计.md §6.
	RiskCritical Risk = "critical"
)
