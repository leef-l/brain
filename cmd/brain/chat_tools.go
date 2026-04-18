package main

import (
	"github.com/leef-l/brain/cmd/brain/chat"
	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/tool"
)

var (
	registerToolsForMode = chat.RegisterToolsForMode
	wrapConfirm          = env.WrapConfirm
)

func buildToolSchemas(reg tool.Registry) []llm.ToolSchema {
	return chat.BuildToolSchemas(reg)
}
