package quantcontracts

import (
	"context"
	"testing"

	"github.com/leef-l/brain/agent"
)

func TestNewSpecialistToolCallAuthorizer_AllowsQuantRoutes(t *testing.T) {
	authorizer := NewSpecialistToolCallAuthorizer()

	cases := []struct {
		caller agent.Kind
		target agent.Kind
		tool   string
	}{
		{caller: agent.KindQuant, target: agent.KindCentral, tool: ToolCentralReviewTrade},
		{caller: agent.KindQuant, target: agent.KindCentral, tool: ToolCentralAccountError},
		{caller: agent.KindData, target: agent.KindCentral, tool: ToolCentralDataAlert},
		{caller: agent.KindData, target: agent.KindCentral, tool: ToolCentralMacroEvent},
		{caller: agent.KindCentral, target: agent.KindQuant, tool: ToolQuantPauseInstrument},
		{caller: agent.KindCentral, target: agent.KindData, tool: ToolDataGetSnapshot},
	}

	for _, tc := range cases {
		if err := authorizer.AuthorizeSpecialistToolCall(context.Background(), tc.caller, tc.target, tc.tool); err != nil {
			t.Fatalf("AuthorizeSpecialistToolCall(%s -> %s:%s) = %v, want nil", tc.caller, tc.target, tc.tool, err)
		}
	}
}

func TestNewSpecialistToolCallAuthorizer_DeniesUnexpectedRoutes(t *testing.T) {
	authorizer := NewSpecialistToolCallAuthorizer()

	cases := []struct {
		caller agent.Kind
		target agent.Kind
		tool   string
	}{
		{caller: agent.KindQuant, target: agent.KindCentral, tool: ToolCentralDataAlert},
		{caller: agent.KindData, target: agent.KindQuant, tool: ToolQuantPauseTrading},
		{caller: agent.KindCode, target: agent.KindCentral, tool: ToolCentralReviewTrade},
	}

	for _, tc := range cases {
		if err := authorizer.AuthorizeSpecialistToolCall(context.Background(), tc.caller, tc.target, tc.tool); err == nil {
			t.Fatalf("AuthorizeSpecialistToolCall(%s -> %s:%s) = nil, want error", tc.caller, tc.target, tc.tool)
		}
	}
}
