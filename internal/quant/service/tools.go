package service

import (
	"context"
	"encoding/json"

	"github.com/leef-l/brain/internal/quantcontracts"
	"github.com/leef-l/brain/tool"
)

type jsonTool struct {
	name        string
	description string
	inputSchema json.RawMessage
	risk        tool.Risk
	run         func(context.Context, json.RawMessage) (any, error)
}

func newJSONTool(name, description, schema string, risk tool.Risk, run func(context.Context, json.RawMessage) (any, error)) tool.Tool {
	return &jsonTool{
		name:        name,
		description: description,
		inputSchema: json.RawMessage(schema),
		risk:        risk,
		run:         run,
	}
}

func (t *jsonTool) Name() string { return t.name }

func (t *jsonTool) Schema() tool.Schema {
	return tool.Schema{
		Name:         t.name,
		Description:  t.description,
		InputSchema:  append(json.RawMessage(nil), t.inputSchema...),
		OutputSchema: json.RawMessage(`true`),
		Brain:        string(quantcontracts.KindQuant),
	}
}

func (t *jsonTool) Risk() tool.Risk { return t.risk }

func (t *jsonTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	if t.run == nil {
		return &tool.Result{Output: json.RawMessage(`null`)}, nil
	}
	value, err := t.run(ctx, args)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &tool.Result{Output: raw}, nil
}
