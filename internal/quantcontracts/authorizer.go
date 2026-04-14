package quantcontracts

import (
	"github.com/leef-l/brain/agent"
	"github.com/leef-l/brain/kernel"
)

func SpecialistToolCallRules() []kernel.SpecialistToolCallRule {
	return []kernel.SpecialistToolCallRule{
		{
			Caller: agent.KindQuant,
			Target: agent.KindData,
			ToolPrefixes: []string{
				"data.get_",
				"data.provider_",
			},
		},
		{
			Caller:       agent.KindQuant,
			Target:       agent.KindCentral,
			ToolPrefixes: []string{ToolCentralReviewTrade, ToolCentralAccountError},
		},
		{
			Caller:       agent.KindData,
			Target:       agent.KindCentral,
			ToolPrefixes: []string{ToolCentralDataAlert, ToolCentralMacroEvent},
		},
		{
			Caller:       agent.KindCentral,
			Target:       agent.KindData,
			ToolPrefixes: []string{"data."},
		},
		{
			Caller:       agent.KindCentral,
			Target:       agent.KindQuant,
			ToolPrefixes: []string{"quant."},
		},
	}
}

func NewSpecialistToolCallAuthorizer() kernel.SpecialistToolCallAuthorizer {
	rules := append(kernel.DefaultSpecialistToolCallRules(), SpecialistToolCallRules()...)
	return kernel.NewStaticSpecialistToolCallAuthorizer(rules)
}
