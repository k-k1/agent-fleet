package chatx

// アシスタントチャットの永続化層：会話 JSON の保存/読込・ID 生成・会話単位ロック。
// chat.go からの機械的分割（docs/log/23 残②）。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Per-conversation lock so concurrent turns to the SAME conversation serialize
// (load-modify-save), while different conversations proceed in parallel.
var convLocks sync.Map // id -> *sync.Mutex

func lockConv(id string) func() {
	m, _ := convLocks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// --- store ---

func chatDir() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "chats")
}

func convPath(id string) string { return filepath.Join(chatDir(), id+".json") }

func loadConv(id string) (*chatConversation, error) {
	if !paths.ValidIDSegment(id) {
		return nil, errors.New("invalid conversation id")
	}
	b, err := os.ReadFile(convPath(id))
	if err != nil {
		return nil, err
	}
	var c chatConversation
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	normalizeChatAgentMetadata(&c)
	return &c, nil
}

// normalizeChatAgentMetadata upgrades conversations written before each assistant
// message recorded its executing backend. Codex thread IDs are UUIDv7, so their first
// 48 bits give the exact fallback start time. Messages before that point came from the
// preferred backend; messages from that point came from Codex. Once new code writes an
// explicit Agent it always wins (including a later switch back to Claude).
func normalizeChatAgentMetadata(c *chatConversation) {
	codexAt, hasCodexAt := uuidV7Millis(c.CodexSessionID)
	lastAgent := ""
	for i := range c.Messages {
		m := &c.Messages[i]
		if m.Role != "assistant" {
			continue
		}
		if m.Agent == "" {
			m.Agent = c.Agent
			if hasCodexAt && m.TS >= codexAt {
				m.Agent = "codex"
			}
		}
		lastAgent = m.Agent
	}
	if c.ActiveAgent == "" {
		c.ActiveAgent = lastAgent
	}
}

func uuidV7Millis(id string) (int64, bool) {
	h := strings.ReplaceAll(id, "-", "")
	if len(h) != 32 || h[12] != '7' {
		return 0, false
	}
	ms, err := strconv.ParseInt(h[:12], 16, 64)
	return ms, err == nil
}

func saveConv(c *chatConversation) error {
	if err := os.MkdirAll(chatDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(convPath(c.ID), append(b, '\n'), 0o600)
}

func listConvs() ([]chatMeta, error) {
	ents, err := os.ReadDir(chatDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []chatMeta{}, nil
		}
		return nil, err
	}
	out := []chatMeta{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		c, err := loadConv(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue // skip unreadable entries rather than failing the whole list
		}
		out = append(out, chatMeta{
			ID: c.ID, Slug: c.Slug, Agent: c.Agent, ActiveAgent: c.ActiveAgent, AssistantID: c.AssistantID, Title: c.Title, Model: c.Model,
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, MessageCount: len(c.Messages),
			Context: c.Context, Locked: c.Locked,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

// randUUID returns a random RFC-4122 v4 UUID, used for both conversation IDs and
// the claude --session-id we pin.
func randUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

func nowMs() int64 { return time.Now().UnixMilli() }
