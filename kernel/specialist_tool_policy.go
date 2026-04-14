package kernel

import (
	"context"
	"fmt"
	"strings"

	"github.com/leef-l/brain/agent"
)

// SpecialistToolCallAuthorizer governs which sidecars may use
// specialist.call_tool to invoke which specialist tools through the Kernel.
// The host-side Orchestrator.CallTool API remains available to trusted host
// code; this authorizer applies to sidecar reverse-RPC callers.
type SpecialistToolCallAuthorizer interface {
	AuthorizeSpecialistToolCall(ctx context.Context, callerKind, targetKind agent.Kind, toolName string) error
}

// SpecialistToolCallAuthorizerFunc adapts a function to the authorizer
// interface.
type SpecialistToolCallAuthorizerFunc func(context.Context, agent.Kind, agent.Kind, string) error

func (f SpecialistToolCallAuthorizerFunc) AuthorizeSpecialistToolCall(ctx context.Context, callerKind, targetKind agent.Kind, toolName string) error {
	return f(ctx, callerKind, targetKind, toolName)
}

// SpecialistToolCallRule is one static allowlist rule for cross-brain tool
// calls. Tool prefixes are matched literally using strings.HasPrefix.
type SpecialistToolCallRule struct {
	Caller       agent.Kind
	Target       agent.Kind
	ToolPrefixes []string
}

// StaticSpecialistToolCallAuthorizer is a conservative explicit allowlist.
type StaticSpecialistToolCallAuthorizer struct {
	rules []SpecialistToolCallRule
}

func NewStaticSpecialistToolCallAuthorizer(rules []SpecialistToolCallRule) *StaticSpecialistToolCallAuthorizer {
	cloned := make([]SpecialistToolCallRule, 0, len(rules))
	for _, rule := range rules {
		cloned = append(cloned, SpecialistToolCallRule{
			Caller:       rule.Caller,
			Target:       rule.Target,
			ToolPrefixes: append([]string(nil), rule.ToolPrefixes...),
		})
	}
	return &StaticSpecialistToolCallAuthorizer{rules: cloned}
}

func (a *StaticSpecialistToolCallAuthorizer) AuthorizeSpecialistToolCall(_ context.Context, callerKind, targetKind agent.Kind, toolName string) error {
	for _, rule := range a.rules {
		if rule.Caller != callerKind || rule.Target != targetKind {
			continue
		}
		for _, prefix := range rule.ToolPrefixes {
			if strings.HasPrefix(toolName, prefix) {
				return nil
			}
		}
	}
	return fmt.Errorf("specialist.call_tool is not allowed from %s to %s:%s", callerKind, targetKind, toolName)
}

// DefaultSpecialistToolCallRules returns the built-in conservative allowlist.
// Callers that need to extend the policy should append their own rules rather
// than rebuilding the default verifier→browser route by hand.
func DefaultSpecialistToolCallRules() []SpecialistToolCallRule {
	return []SpecialistToolCallRule{
		{
			Caller:       agent.KindVerifier,
			Target:       agent.KindBrowser,
			ToolPrefixes: []string{"browser."},
		},
	}
}

// DefaultSpecialistToolCallAuthorizer returns the built-in conservative policy.
// Third-party integrations may override it on the Orchestrator.
func DefaultSpecialistToolCallAuthorizer() SpecialistToolCallAuthorizer {
	return NewStaticSpecialistToolCallAuthorizer(DefaultSpecialistToolCallRules())
}
