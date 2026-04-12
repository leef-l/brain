package security

// Zone identifies one of the five trust domains defined in 23-安全模型.md §2.1.
//
// A Run crosses zones in a strictly monotonic direction (Kernel → Built-in
// → ThirdParty → Tool → LLMOutput); every boundary crossing MUST honor the
// MUST clauses from 23 §2.2 (credentials policy, reverse-RPC guardrails,
// tool_result sanitization, InternalDetail scrubbing). Runtime code MUST
// always know which Zone its current context belongs to so that the correct
// Sandbox policy and LLMAccess mode are applied.
type Zone int

const (
	// ZoneKernel is Trust Zone 0: the Kernel core process, which owns the
	// error model, the audit logger, and the credentials Vault. This zone
	// is the trusted computing base and is fully trusted. See 23 §2.1.
	ZoneKernel Zone = 1

	// ZoneBuiltin is Trust Zone 1: built-in brain sidecars maintained in
	// the project's own repository (central_brain, code_brain,
	// verifier_brain, ...). Semi-trusted — code is first-party but still
	// runs under sandbox and guardrail. See 23 §2.1.
	ZoneBuiltin Zone = 2

	// ZoneThirdParty is Trust Zone 2: third-party brain sidecars loaded
	// from the plugin market. Untrusted — code is fully opaque and MUST
	// run under signature verification, sandbox, and llm_access=proxied.
	// See 23 §2.1 and §2.3.
	ZoneThirdParty Zone = 3

	// ZoneTool is Trust Zone 3: tool execution (git / bash / docker /
	// playwright / pytest / ...). Inputs (file contents, stdout, stderr)
	// are untrusted and MUST go through the tool_result sanitizer
	// pipeline before re-entering the LLM. See 23 §2.1 and §2.2.
	ZoneTool Zone = 4

	// ZoneLLMOutput is Trust Zone 4: LLM-generated text and tool_call
	// blobs. Untrusted — possibly hallucinated, possibly hijacked by
	// prompt injection — so any tool_call parsed from LLM output MUST be
	// re-validated against schema, guardrail, and resource locks before
	// execution. See 23 §2.1 and §2.2.
	ZoneLLMOutput Zone = 5
)
