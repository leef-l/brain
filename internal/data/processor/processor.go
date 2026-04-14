package processor

import (
	"strings"

	"github.com/leef-l/brain/internal/data/model"
)

type Processor struct {
	defaultTimeframe string
}

func New(defaultTimeframe string) *Processor {
	if strings.TrimSpace(defaultTimeframe) == "" {
		defaultTimeframe = "1m"
	}
	return &Processor{defaultTimeframe: defaultTimeframe}
}

func (p *Processor) Process(event model.MarketEvent, valid model.ValidationResult) model.MarketSnapshot {
	timeframe := p.defaultTimeframe
	if strings.HasPrefix(event.Topic, "candle.") {
		timeframe = strings.TrimPrefix(event.Topic, "candle.")
	}

	snapshot := model.MarketSnapshot{
		Provider:       event.Provider,
		Topic:          event.Topic,
		Symbol:         event.Symbol,
		Kind:           event.Kind,
		SourceSeq:      event.Sequence,
		Timestamp:      event.Timestamp,
		Price:          event.Price,
		Volume:         event.Volume,
		FeatureVector:  featureVector(event),
		Candles:        make(map[string][]model.Candle),
		Validation:     valid.Action,
		ValidationNote: valid.Reason,
		SourceDigest:   event.Digest,
	}
	snapshot.Candles[timeframe] = []model.Candle{
		{
			Timestamp: event.Timestamp,
			Open:      event.Price,
			High:      event.Price,
			Low:       event.Price,
			Close:     event.Price,
			Volume:    event.Volume,
		},
	}
	return snapshot
}

func featureVector(event model.MarketEvent) []float64 {
	vector := []float64{
		event.Price,
		event.Volume,
		float64(len(event.Symbol)),
		float64(len(event.Topic)),
		float64(event.Sequence),
	}
	if event.Digest != 0 {
		vector = append(vector, float64(event.Digest%1000))
	}
	return vector
}
