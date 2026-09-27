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
	chatID := sids.Read(session.UUID(m.Dir, m.Name))
	if chatID == "" {
		return agents.RecalledSettings{}
	}
	hits, _ := filepath.Glob(filepath.Join(Home(), "chats", "*", chatID, "store.db"))
	if len(hits) == 0 {
		return agents.RecalledSettings{}
	}
	st, ok := readChatMeta(hits[0])
	if !ok {
		return agents.RecalledSettings{}
	}
	return recallFrom(st, m.Model, cachedModelIDs())
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
