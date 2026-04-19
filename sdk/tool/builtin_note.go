package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// NoteTool 是 Agent 的轻量 Scratchpad / TODO list。
// 解决复杂任务里 LLM 每轮都要重新"想起"自己计划做什么的问题。
// 参考 Claude Code 的 TodoWrite 设计，但更轻量：只维护 pending/done 两态。
//
// 状态在 sidecar 进程内，每个大脑独占一份（通过工具名前缀）。
//
// action 支持：
//   - add     {"text": "步骤 1"}         → 追加一条待办
//   - update  {"id": 3, "text": "..."}   → 修改内容
//   - done    {"id": 3}                  → 标记为完成
//   - list    {}                         → 列出全部
//   - clear   {}                         → 清空
type NoteTool struct {
	brainKind string
	store     *noteStore
}

// noteStore 是进程内的状态持有者，所有 NoteTool 实例共享同一大脑的 store。
type noteStore struct {
	mu     sync.Mutex
	nextID int
	items  map[int]*noteItem
}

type noteItem struct {
	ID        int       `json:"id"`
	Text      string    `json:"text"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"created_at"`
	DoneAt    *time.Time `json:"done_at,omitempty"`
}

// noteStores 按 brainKind 隔离，避免 code brain 的 TODO 被 browser brain 看到。
var (
	noteStoresMu sync.Mutex
	noteStores   = map[string]*noteStore{}
)

func getNoteStore(brainKind string) *noteStore {
	noteStoresMu.Lock()
	defer noteStoresMu.Unlock()
	s, ok := noteStores[brainKind]
	if !ok {
		s = &noteStore{items: map[int]*noteItem{}}
		noteStores[brainKind] = s
	}
	return s
}

// NewNoteTool 构造一个给 brainKind 使用的 Note 工具。
func NewNoteTool(brainKind string) *NoteTool {
	return &NoteTool{brainKind: brainKind, store: getNoteStore(brainKind)}
}

func (t *NoteTool) Name() string { return t.brainKind + ".note" }
func (t *NoteTool) Risk() Risk   { return RiskSafe }

func (t *NoteTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: "Agent scratchpad / TODO list for multi-step tasks. " +
			"Use this at the start of a complex task to plan steps, then mark them done as you go. " +
			"Prevents getting lost mid-task and repeating work. " +
			"Actions: add, update, done, list, clear. " +
			"NOTE: state is in-memory only — it persists across turns within the same sidecar process " +
			"but is lost if the sidecar restarts. Not intended as durable storage.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": { "type": "string", "enum": ["add", "update", "done", "list", "clear"] },
    "text":   { "type": "string", "description": "Step description (add/update)" },
    "id":     { "type": "integer", "description": "Item ID (update/done)" }
  },
  "required": ["action"]
}`),
		OutputSchema: noteOutputSchema,
		Brain:        t.brainKind,
	}
}

type noteInput struct {
	Action string `json:"action"`
	Text   string `json:"text"`
	ID     int    `json:"id"`
}

type noteOutput struct {
	Action string      `json:"action"`
	OK     bool        `json:"ok"`
	ID     int         `json:"id,omitempty"`
	Items  []*noteItem `json:"items,omitempty"`
	Note   string      `json:"note,omitempty"`
}

func (t *NoteTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input noteInput
	if err := json.Unmarshal(args, &input); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid arguments: %v", err)), IsError: true}, nil
	}

	t.store.mu.Lock()
	defer t.store.mu.Unlock()

	switch input.Action {
	case "add":
		if input.Text == "" {
			return &Result{Output: jsonStr("add: text is required"), IsError: true}, nil
		}
		t.store.nextID++
		id := t.store.nextID
		t.store.items[id] = &noteItem{ID: id, Text: input.Text, CreatedAt: time.Now()}
		return noteResult(noteOutput{Action: "add", OK: true, ID: id, Items: t.listItemsLocked()}), nil

	case "update":
		item, ok := t.store.items[input.ID]
		if !ok {
			return &Result{Output: jsonStr(fmt.Sprintf("update: id %d not found", input.ID)), IsError: true}, nil
		}
		if input.Text != "" {
			item.Text = input.Text
		}
		return noteResult(noteOutput{Action: "update", OK: true, ID: input.ID, Items: t.listItemsLocked()}), nil

	case "done":
		item, ok := t.store.items[input.ID]
		if !ok {
			return &Result{Output: jsonStr(fmt.Sprintf("done: id %d not found", input.ID)), IsError: true}, nil
		}
		now := time.Now()
		item.Done = true
		item.DoneAt = &now
		return noteResult(noteOutput{Action: "done", OK: true, ID: input.ID, Items: t.listItemsLocked()}), nil

	case "list":
		return noteResult(noteOutput{Action: "list", OK: true, Items: t.listItemsLocked()}), nil

	case "clear":
		t.store.items = map[int]*noteItem{}
		t.store.nextID = 0
		return noteResult(noteOutput{Action: "clear", OK: true, Note: "all items cleared"}), nil

	default:
		return &Result{Output: jsonStr(fmt.Sprintf("unknown action: %s (use add, update, done, list, clear)", input.Action)), IsError: true}, nil
	}
}

// listItemsLocked 返回按 ID 排序的 item 列表。必须在持有 store.mu 时调用。
func (t *NoteTool) listItemsLocked() []*noteItem {
	items := make([]*noteItem, 0, len(t.store.items))
	for _, it := range t.store.items {
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func noteResult(out noteOutput) *Result {
	raw, _ := json.Marshal(out)
	return &Result{Output: raw}
}

// resetNoteStore 测试用，清空指定 brain 的 store。
func resetNoteStore(brainKind string) {
	noteStoresMu.Lock()
	defer noteStoresMu.Unlock()
	delete(noteStores, brainKind)
}
