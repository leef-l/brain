package quantcontracts

import "github.com/leef-l/brain/agent"

const (
	KindData  = agent.KindData
	KindQuant = agent.KindQuant
)

func BrainKinds() []agent.Kind {
	return []agent.Kind{
		agent.KindCentral,
		KindData,
		KindQuant,
	}
}
