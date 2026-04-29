package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/sdk/llm"
)

// Conversation 保存一个工作目录下的完整对话上下文。
type Conversation struct {
	Workdir    string       `json:"workdir"`
	Messages   []llm.Message `json:"messages"`
	TurnCount  int          `json:"turn_count"`
	Mode       string       `json:"mode"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

// ConversationDir 返回 conversations 存储目录。
func ConversationDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".brain", "conversations")
}

// conversationPath 根据 workdir 生成唯一的 conversation 文件路径。
func conversationPath(workdir string) string {
	h := sha256.Sum256([]byte(workdir))
	hash := hex.EncodeToString(h[:8])
	return filepath.Join(ConversationDir(), fmt.Sprintf("%s.json", hash))
}

// LoadConversation 加载指定 workdir 的 conversation。如果不存在返回 nil（不报错）。
func LoadConversation(workdir string) (*Conversation, error) {
	path := conversationPath(workdir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var conv Conversation
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, fmt.Errorf("parse conversation %s: %w", path, err)
	}
	conv.Workdir = workdir // 确保一致
	return &conv, nil
}

// SaveConversation 保存 conversation 到磁盘。
func SaveConversation(conv *Conversation) error {
	if conv == nil {
		return nil
	}
	conv.UpdatedAt = time.Now().UTC()
	path := conversationPath(conv.Workdir)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(conv, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SnapshotStateToConversation 把当前 State 的快照转为 Conversation。
func SnapshotStateToConversation(state *State, workdir string) *Conversation {
	if state == nil {
		return nil
	}
	msgs := make([]llm.Message, len(state.Messages))
	copy(msgs, state.Messages)
	return &Conversation{
		Workdir:   workdir,
		Messages:  msgs,
		TurnCount: state.TurnCount,
		Mode:      string(state.Mode),
		UpdatedAt: time.Now().UTC(),
	}
}

// ApplyConversationToState 把 Conversation 恢复到 State。
func ApplyConversationToState(conv *Conversation, state *State) {
	if conv == nil || state == nil {
		return
	}
	state.Messages = make([]llm.Message, len(conv.Messages))
	copy(state.Messages, conv.Messages)
	state.TurnCount = conv.TurnCount
	if m, err := env.ParsePermissionMode(conv.Mode); err == nil {
		state.SwitchMode(m)
	}
}
