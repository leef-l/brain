// paste_store.go — 长粘贴原文存储
//
// 用户在 chat 输入超长文本(代码片段、日志、文档段落)时,直接送 LLM 浪费 token,
// 但裁剪又会丢失原文。PasteStore 提供进程内 ID→原文映射:
//
//   1. PreprocessUserInput 命中长粘贴阈值时,把原文 Put 进 PasteStore,
//      送 LLM 的拷贝换成"[PASTE id=xxx 共 N 行] head\n...\ntail"摘要
//   2. central.read_paste(id) 工具让 LLM 取回原文
//   3. state.Messages / Activity / 持久化保留原文(input_preprocess.go 文件头约束)
//
// 容量上限:默认 64 条,超过按 LRU 淘汰。每条最多 256KB,超过截断。
// 进程退出后 PasteStore 不持久化(粘贴是临时性的,跨重启价值不大)。
//
// 并发安全:全局 sharedPasteStore 用 RWMutex 保护。

package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
)

const (
	// pasteStoreCapacity LRU 上限,超过淘汰最旧条目。
	pasteStoreCapacity = 64
	// pasteMaxBytes 单条原文最大字节数,超过截断。
	pasteMaxBytes = 256 * 1024
)

// PasteEntry 存储的单条粘贴。
type PasteEntry struct {
	ID         string
	Original   string
	Lines      int
	Chars      int
	Truncated  bool // 原文超过 pasteMaxBytes 被截断
	insertSeq  uint64
}

// PasteStore 进程内 ID→原文映射。
type PasteStore struct {
	mu      sync.RWMutex
	entries map[string]*PasteEntry
	seq     uint64
}

// NewPasteStore 创建空的 PasteStore。
func NewPasteStore() *PasteStore {
	return &PasteStore{entries: make(map[string]*PasteEntry, pasteStoreCapacity)}
}

// Put 存入原文,返回稳定的 ID(基于内容哈希前缀,相同内容复用同一 ID)。
// 超过 pasteMaxBytes 截断并标记 Truncated=true。
// 容量超过 pasteStoreCapacity 时按 insertSeq 淘汰最旧。
func (s *PasteStore) Put(content string) *PasteEntry {
	if s == nil {
		return nil
	}

	truncated := false
	if len(content) > pasteMaxBytes {
		content = content[:pasteMaxBytes]
		truncated = true
	}

	// 内容哈希前 12 位作 ID,相同内容多次粘贴复用同一 entry
	sum := sha256.Sum256([]byte(content))
	id := hex.EncodeToString(sum[:6]) // 12 hex chars

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.entries[id]; ok {
		s.seq++
		existing.insertSeq = s.seq // 触新避免被 LRU 淘汰
		return existing
	}

	s.seq++
	entry := &PasteEntry{
		ID:        id,
		Original:  content,
		Lines:     countLines(content),
		Chars:     len(content),
		Truncated: truncated,
		insertSeq: s.seq,
	}
	s.entries[id] = entry

	// LRU 淘汰
	if len(s.entries) > pasteStoreCapacity {
		var oldestID string
		var oldestSeq uint64 = ^uint64(0)
		for k, v := range s.entries {
			if v.insertSeq < oldestSeq {
				oldestSeq = v.insertSeq
				oldestID = k
			}
		}
		if oldestID != "" {
			delete(s.entries, oldestID)
		}
	}

	return entry
}

// Get 按 ID 取原文,不存在返回 nil。
func (s *PasteStore) Get(id string) *PasteEntry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entries[id]
}

// Len 当前条目数。
func (s *PasteStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// countLines 计算行数(空字符串记 0,无换行单行记 1)。
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	return n
}

// ─── 全局单例 ────────────────────────────────────────────────────

var (
	pasteStoreOnce sync.Once
	pasteStore     *PasteStore
)

// SharedPasteStore 返回进程内全局 PasteStore,首次访问 lazy 初始化。
func SharedPasteStore() *PasteStore {
	pasteStoreOnce.Do(func() {
		pasteStore = NewPasteStore()
	})
	return pasteStore
}

// FormatPasteSummary 把 entry 渲染成发给 LLM 的摘要文本。
// LLM 看到 [PASTE id=xxx] 标记知道可调 central.read_paste(xxx) 取原文。
func FormatPasteSummary(entry *PasteEntry, head, tail string) string {
	if entry == nil {
		return ""
	}
	suffix := ""
	if entry.Truncated {
		suffix = " (原文已截断到 256KB)"
	}
	return fmt.Sprintf(
		"[PASTE id=%s 共 %d 行 / %d 字符%s。如需完整原文调 central.read_paste(id=%q)。]\n\n%s\n\n[... 中间省略 ...]\n\n%s",
		entry.ID, entry.Lines, entry.Chars, suffix, entry.ID, head, tail,
	)
}
