package cursor

// Reading back the model and mode a chat switched to in the TUI, so a resume does not
// revert them (#987).

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	_ "modernc.org/sqlite"
)

// RecallSettings reads the chat's own settings from store.db. The package otherwise never
// reads that private store (cursor.go); this is the one exception, because the model and
// mode of a TUI chat are recorded nowhere else. It is read once per resume, and any
// failure — schema drift included — returns nothing, which keeps today's launch flags.
//
// Measured on 2026.09.23: `lastUsedModel` is written when a prompt is sent (a `/model` with
// no prompt after it is lost by cursor itself), `mode` the moment shift+tab changes it, and
// a resume without `--model` / `--plan` restores both — while passing them wins.
func (agentImpl) RecallSettings(m session.Meta) agents.RecalledSettings {
	st, ok := storedChatMeta(m)
	if !ok {
		return agents.RecalledSettings{}
	}
	return recallFrom(st, m.Model, cachedModelIDs())
}

// RecordedModel is the model id store.db holds for the chat, as written ("default" read as
// "auto"), or "" when nothing is recorded. Unlike RecallSettings it is not mapped through the
// catalog or the meta, so a contract can compare what cursor itself kept with what was
// requested.
func (agentImpl) RecordedModel(m session.Meta) string {
	st, ok := storedChatMeta(m)
	if !ok {
		return ""
	}
	return recordedModelOf(st)
}

func recordedModelOf(st chatMeta) string {
	if st.LastUsedModel == "default" {
		return "auto"
	}
	return st.LastUsedModel
}

func storedChatMeta(m session.Meta) (chatMeta, bool) {
	return chatMetaFor(sids.Read(session.UUID(m.Dir, m.Name)))
}

func chatMetaFor(chatID string) (chatMeta, bool) {
	st, _, ok := chatStore(chatID)
	return st, ok
}

// chatStore reads the chat's store.db: exists is false while cursor has none for it (a chat
// that has not started yet), ok is false when there is none or it could not be read.
func chatStore(chatID string) (st chatMeta, exists, ok bool) {
	if chatID == "" {
		return chatMeta{}, false, false
	}
	hits, _ := filepath.Glob(filepath.Join(Home(), "chats", "*", chatID, "store.db"))
	if len(hits) == 0 {
		return chatMeta{}, false, false
	}
	st, ok = readChatMeta(hits[0])
	return st, true, ok
}

// chatMeta is the part of store.db's meta row this reads. The row also holds the chat's
// blob encryption key, so it is decoded into this struct and nothing else is kept or logged.
type chatMeta struct {
	Mode          string `json:"mode"`
	LastUsedModel string `json:"lastUsedModel"`
}

func readChatMeta(path string) (chatMeta, bool) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return chatMeta{}, false
	}
	defer db.Close()
	var v string
	if db.QueryRow(`SELECT value FROM meta WHERE key = '0'`).Scan(&v) != nil {
		return chatMeta{}, false
	}
	raw, err := hex.DecodeString(v)
	if err != nil {
		return chatMeta{}, false
	}
	var st chatMeta
	if json.Unmarshal(raw, &st) != nil {
		return chatMeta{}, false
	}
	return st, true
}

// recallFrom maps the chat's record onto meta values. lastUsedModel is the base id without
// the parameters the catalog ids carry (gemini-3.7-flash vs gemini-3.7-flash-high), and
// "default" for Auto. When the meta's id already has that base it is kept, parameters
// included. Otherwise the base id is used only if the catalog lists it verbatim; failing
// that the flag is dropped, and cursor resumes on the chat's own model by itself.
func recallFrom(st chatMeta, metaModel string, catalog map[string]bool) agents.RecalledSettings {
	var r agents.RecalledSettings
	switch base := st.LastUsedModel; {
	case base == "":
	case base == "default":
		r.Model = "auto"
	case metaModel == base || strings.HasPrefix(metaModel, base+"-"):
	case catalog[base]:
		r.Model = base
	default:
		r.ClearModel = true
	}
	switch st.Mode {
	case "":
	case "plan":
		r.Mode = "plan"
	default: // default (Agent), search (Ask), debug
		r.Mode = "normal"
	}
	return r
}

// cachedModelIDs is the model catalog as last fetched, without fetching: a resume must not
// wait on (or spawn) `cursor-agent models`.
func cachedModelIDs() map[string]bool {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	ids := make(map[string]bool, len(modelsList))
	for _, c := range modelsList {
		ids[c.ID] = true
	}
	return ids
}
