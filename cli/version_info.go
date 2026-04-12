package cli

// VersionInfo is the JSON schema emitted by `brain version --json`, frozen by
// 27-CLI命令契约.md §17.3.
//
// The human-readable rendering of the same data lives in 27 §17.2 and the
// `--short` flag defined in 27 §17.4 prints only CLIVersion. The field set
// MUST NOT change in v1; adding a field requires a minor version bump per the
// general CLI compatibility rules in 27 §0 and §18.
//
// Field semantics (all strings; empty values are permitted only for Commit
// and BuiltAt in local developer builds):
//
//   - CLIVersion       semver of the `brain` executable itself
//   - ProtocolVersion  wire protocol version negotiated via `initialize` (20)
//   - KernelVersion    semver of the embedded BrainKernel
//   - SDKLanguage      implementation language of the SDK (e.g. "go")
//   - SDKVersion       semver of the SDK module
//   - Commit           short git commit SHA the binary was built from
//   - BuiltAt          RFC 3339 UTC build timestamp
//   - OS               GOOS-style operating system identifier
//   - Arch             GOARCH-style CPU architecture identifier
//
// The JSON tags are the normative wire names; callers that unmarshal this
// struct from another SDK's `brain version --json` output MUST see the
// snake_case names below.
type VersionInfo struct {
	// CLIVersion is the semver of the `brain` binary. Spec: 27 §17.3.
	CLIVersion string `json:"cli_version"`

	// ProtocolVersion is the stdio wire protocol version (see 20). Spec: 27 §17.3.
	ProtocolVersion string `json:"protocol_version"`

	// KernelVersion is the semver of the embedded BrainKernel. Spec: 27 §17.3.
	KernelVersion string `json:"kernel_version"`

	// SDKLanguage is the implementation language tag of the SDK, e.g. "go".
	// Spec: 27 §17.3.
	SDKLanguage string `json:"sdk_language"`

	// SDKVersion is the semver of the SDK module. Spec: 27 §17.3.
	SDKVersion string `json:"sdk_version"`

	// Commit is the short git commit SHA the binary was built from. Spec: 27 §17.3.
	Commit string `json:"commit"`

	// BuiltAt is the RFC 3339 UTC build timestamp. Spec: 27 §17.3.
	BuiltAt string `json:"built_at"`

	// OS is the GOOS-style operating system identifier (e.g. "linux").
	// Spec: 27 §17.3.
	OS string `json:"os"`

	// Arch is the GOARCH-style CPU architecture identifier (e.g. "amd64").
	// Spec: 27 §17.3.
	Arch string `json:"arch"`
}
