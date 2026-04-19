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

	// SDKLanguage identifies the SDK implementation language.
	SDKLanguage = "go"
)

// Release versions — overridden at build time via -ldflags:
//
//	go build -ldflags "-X github.com/leef-l/brain.CLIVersion=1.0.0 ..."
//
// Default values are the development baseline; release builds inject the
// actual version from the build script's <version> argument.
var (
	// KernelVersion is the Kernel behavior contract version (semver).
	KernelVersion = "1.0.0"

	// SDKVersion is this Go SDK's semver.
	SDKVersion = "1.0.0"

	// CLIVersion is the user-facing `brain` CLI version (tracks SDKVersion in Go SDK).
	CLIVersion = "1.0.0"
)

// BuildInfo is filled in at link time via -ldflags.
// Empty values indicate a non-release build (e.g., `go run`).
var (
	BuildCommit = "unknown"
	BuildTime   = "unknown"
)
