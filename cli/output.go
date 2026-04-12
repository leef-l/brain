package cli

// OutputFormat is the user-facing output mode of a `brain` subcommand, as
// frozen by 27-CLI命令契约.md §19.
//
// The two formats are NOT interchangeable rendering knobs on the same data —
// they have distinct contracts:
//
//   - FormatHuman is for TTY consumption: aligned tables, fixed Unicode status
//     badges, optional colour, progress bars. Error text MUST be written to
//     stderr. Spec: 27 §19.3.
//
//   - FormatJSON is for scripts and pipelines: snake_case field names, RFC
//     3339 UTC timestamps, the `cost_usd` / `input_tokens` / `output_tokens`
//     field naming, BrainError payload shape from 21-错误模型. Non-streaming
//     commands emit a single JSON object; streaming (`--follow`) commands
//     emit NDJSON (one object per line terminated by `\n`). Spec: 27 §19.4.
//
// The CLI selects between the two values automatically per 27 §19.2: stdout
// not being a TTY forces FormatJSON, `BRAIN_OUTPUT=json` forces FormatJSON,
// and `NO_COLOR` merely disables colour inside FormatHuman.
type OutputFormat string

const (
	// FormatHuman is the default format when stdout is a TTY. See 27 §19.3
	// for the rendering contract (alignment, status badges, stdout/stderr
	// split, `--quiet` behaviour).
	FormatHuman OutputFormat = "human"

	// FormatJSON is the machine-readable format. See 27 §19.4 for the field
	// naming, timestamp, NDJSON, and BrainError embedding rules, and 27 §19.5
	// for the boundary against the stdio wire protocol defined in 20.
	FormatJSON OutputFormat = "json"
)
