package brain

// Version numbers frozen by 28-SDK交付规范.md §4 (three-tier versioning).
//
// Each tier advances independently:
//   - ProtocolVersion is the stdio wire protocol version (20-协议规格.md §2).
//   - KernelVersion is the Kernel behavior contract version.
//   - SDKVersion is this SDK's own semver.
//
// A compliant SDK must declare all three in VERSION.json, and `brain version`
// must read them from that file (§4.5).
const (
	// ProtocolVersion is the stdio wire protocol version (major.minor, no patch).
	ProtocolVersion = "1.0"

	// KernelVersion is the Kernel behavior contract version (semver).
	// v0.1.0-skeleton indicates the interface-only skeleton stage;
	// once an implementation passes compliance tests it becomes v1.0.0.
	KernelVersion = "0.1.0-skeleton"

	// SDKVersion is this Go SDK's semver.
	SDKVersion = "0.1.0-skeleton"

	// SDKLanguage identifies the SDK implementation language.
	SDKLanguage = "go"

	// CLIVersion is the user-facing `brain` CLI version (tracks SDKVersion in Go SDK).
	CLIVersion = "0.1.0-skeleton"
)

// BuildInfo is filled in at link time via -ldflags.
// Empty values indicate a non-release build (e.g., `go run`).
var (
	BuildCommit = "unknown"
	BuildTime   = "unknown"
)
