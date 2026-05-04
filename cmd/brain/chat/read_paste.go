// read_paste.go — central.read_paste 工具
//
// 用户输入超长粘贴时,PreprocessUserInput 会把原文存进 PasteStore,
// 发给 LLM 的拷贝换成"[PASTE id=xxx 共 N 行] head ... tail"摘要。
// 如果 LLM 后续需要看完整原文(如 review、引用、对比),调 central.read_paste(id) 取回。

package chat

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leef-l/brain/sdk/tool"
)

type readPasteTool struct {
	store *PasteStore
}

// NewReadPasteTool 创建 central.read_paste 工具。
// store 为 nil 时使用全局 SharedPasteStore。
func NewReadPasteTool(store *PasteStore) tool.Tool {
	if store == nil {
		store = SharedPasteStore()
	}
	return &readPasteTool{store: store}
}

func (t *readPasteTool) Name() string { return "central.read_paste" }

func (t *readPasteTool) Schema() tool.Schema {
	return tool.Schema{
		Name: "central.read_paste",
		Description: "Retrieve the full original text of a long paste that was previously summarized in the user message. " +
			"When user input exceeds the long-paste threshold, the input is replaced by a [PASTE id=xxx ...] marker; " +
			"call this tool with that id to get the full content. " +
			"Returns IsError=true if id is not found (paste may have been evicted by LRU).",
		Brain: "central",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {
					"type": "string",
					"description": "The paste id from a [PASTE id=xxx] marker in user message"
				}
			},
			"required": ["id"]
		}`),
	}
}

func (t *readPasteTool) Risk() tool.Risk { return tool.RiskSafe }

func (t *readPasteTool) Execute(_ context.Context, args json.RawMessage) (*tool.Result, error) {
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"invalid arguments: %v"`, err)),
			IsError: true,
		}, nil
	}
	if params.ID == "" {
		return &tool.Result{
			Output:  json.RawMessage(`"id is required"`),
			IsError: true,
		}, nil
	}

	entry := t.store.Get(params.ID)
	if entry == nil {
		return &tool.Result{
			Output: json.RawMessage(fmt.Sprintf(
				`"paste id %q not found (may have been evicted by LRU; ask user to paste again if needed)"`,
				params.ID)),
			IsError: true,
		}, nil
	}

	out, _ := json.Marshal(map[string]interface{}{
		"id":        entry.ID,
		"lines":     entry.Lines,
		"chars":     entry.Chars,
		"truncated": entry.Truncated,
		"original":  entry.Original,
	})
	return &tool.Result{Output: out}, nil
}
