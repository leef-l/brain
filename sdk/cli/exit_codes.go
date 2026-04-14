// Package cli defines the constant / data-structure surface of the `brain`
// command-line interface contract frozen in 27-CLI命令契约.md.
//
// This sub-package intentionally contains ONLY:
//
//   - exit code constants                 (27 §18)
//   - output format enum (human / json)   (27 §19)
//   - version info JSON schema struct     (27 §17.3)
//
// The dispatcher, the `flag` parsing glue, and every subcommand implementation
// are wired up in a later wave and live in sibling files of this package. This
// file deliberately stays dependency-free (only stdlib) so the contract layer
// can be imported by tests and downstream tooling without pulling the full
// command tree.
//
// See 27-CLI命令契约.md for the normative spec.
package cli

// Exit codes frozen by 27-CLI命令契约.md §18.
//
// v1 MUST NOT change the meaning of any value in this block. Introducing a new
// exit code requires a minor version bump per 27 §18 "扩展策略". Values in the
// 8..63 range are reserved for future subcommand-specific codes; values >= 100
// that are NOT one of the listed signal codes MUST NOT be used, to avoid
// collisions with POSIX signal numbers.
//
// The constants below map to the BSD sysexits.h conventions where applicable:
//
//	64  EX_USAGE     command line usage error
//	65  EX_DATAERR   data format error
//	66  EX_NOINPUT   cannot open input
//	67  EX_NOPERM    permission denied (port / filesystem)
//	70  EX_SOFTWARE  internal software error
//	71  EX_OSERR     system error (fork, fs, net)
//	77  EX_NOPERM    credential / Vault access failure (sysexits reuses 77 for
//	                 "permission denied"; we narrow it to credential-missing)
//
// Codes 0..5 are BrainKernel-specific. Codes 130 and 143 follow the POSIX
// convention of `128 + signal_number` for SIGINT (2) and SIGTERM (15).
const (
	// ExitOK signals successful completion. Spec: 27 §18 (OK).
	ExitOK = 0

	// ExitFailed signals a Run failure or a check failure (e.g. `brain doctor`
	// reporting an unhealthy environment). Spec: 27 §18 (ERR_FAILED).
	ExitFailed = 1

	// ExitCanceled signals that a Run was canceled, either by the user or by a
	// higher-level orchestrator. Spec: 27 §18 (ERR_CANCELED).
	ExitCanceled = 2

	// ExitBudgetExhausted signals that a Run stopped because its token / cost
	// / time budget was exhausted. Spec: 27 §18 (ERR_BUDGET_EXHAUSTED).
	ExitBudgetExhausted = 3

	// ExitNotFound signals that the referenced run, tool, or configuration
	// entry does not exist. Spec: 27 §18 (ERR_NOT_FOUND).
	ExitNotFound = 4

	// ExitInvalidState signals that the requested operation is not allowed in
	// the current state (for example cancelling an already-completed Run).
	// Spec: 27 §18 (ERR_INVALID_STATE).
	ExitInvalidState = 5

	// ExitUsage signals a command-line parsing / usage error. Maps to BSD
	// sysexits EX_USAGE (64). Spec: 27 §18 (EX_USAGE).
	ExitUsage = 64

	// ExitDataErr signals that a configuration file had an invalid format.
	// Maps to BSD sysexits EX_DATAERR (65). Spec: 27 §18 (EX_DATAERR).
	ExitDataErr = 65

	// ExitNoInput signals that an input file or database could not be read.
	// Maps to BSD sysexits EX_NOINPUT (66). Spec: 27 §18 (EX_NOINPUT).
	ExitNoInput = 66

	// ExitNoPerm signals insufficient permissions, for example a filesystem
	// permission denial or a port already bound by another process. Maps to
	// BSD sysexits EX_NOPERM (67). Spec: 27 §18 (EX_NOPERM).
	ExitNoPerm = 67

	// ExitSoftware signals an internal software error, typically a Kernel
	// communication (RPC) failure. Maps to BSD sysexits EX_SOFTWARE (70).
	// Spec: 27 §18 (EX_SOFTWARE).
	ExitSoftware = 70

	// ExitOSErr signals an operating-system error (fork, filesystem, network).
	// Maps to BSD sysexits EX_OSERR (71). Spec: 27 §18 (EX_OSERR).
	ExitOSErr = 71

	// ExitCredMissing signals a credential / Vault access failure (e.g.
	// required API key not present). Shares numeric value 77 with BSD
	// sysexits EX_NOPERM but is narrowed to credential semantics.
	// Spec: 27 §18 (EX_NOPERM / credential missing).
	ExitCredMissing = 77

	// ExitSignalInt signals that the process was interrupted by SIGINT
	// (Ctrl-C). Follows the POSIX `128 + signal_number` convention
	// (128 + 2 = 130). Spec: 27 §18 (SIGINT).
	ExitSignalInt = 130

	// ExitSignalTerm signals that the process was terminated by SIGTERM.
	// Follows the POSIX `128 + signal_number` convention (128 + 15 = 143).
	// Spec: 27 §18 (SIGTERM).
	ExitSignalTerm = 143
)
