package cursor

import (
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestRecallFrom(t *testing.T) {
	catalog := map[string]bool{"composer-2.5": true, "gemini-3.7-flash-high": true}
	cases := []struct {
		name      string
		st        chatMeta
		metaModel string
		want      agents.RecalledSettings
	}{
		{"nothing recorded", chatMeta{}, "composer-2.5", agents.RecalledSettings{}},
		{"Auto", chatMeta{LastUsedModel: "default", Mode: "default"}, "gemini-3.7-flash-high",
			agents.RecalledSettings{Model: "auto", Mode: "normal"}},
		{"same base keeps the parameters", chatMeta{LastUsedModel: "gemini-3.7-flash"}, "gemini-3.7-flash-high",
			agents.RecalledSettings{}},
		{"switched to a catalog id", chatMeta{LastUsedModel: "composer-2.5", Mode: "plan"}, "auto",
			agents.RecalledSettings{Model: "composer-2.5", Mode: "plan"}},
		{"a base the catalog does not list drops the flag", chatMeta{LastUsedModel: "gpt-6"}, "composer-2.5",
			agents.RecalledSettings{ClearModel: true}},
		{"ask mode is not plan", chatMeta{Mode: "search"}, "", agents.RecalledSettings{Mode: "normal"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := recallFrom(c.st, c.metaModel, catalog); got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestReadChatMeta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// The value is hex-encoded JSON, as cursor 2026.09.23 writes it.
	v := hex.EncodeToString([]byte(`{"agentId":"a","name":"Just Ok","mode":"plan","isRunEverything":true,"lastUsedModel":"composer-2.5","blobEncryptionKey":"k"}`))
	for _, s := range []string{`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`, `INSERT INTO meta VALUES ('0', '` + v + `')`} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	st, ok := readChatMeta(path)
	if !ok || st.Mode != "plan" || st.LastUsedModel != "composer-2.5" {
		t.Errorf("readChatMeta = %+v, %v", st, ok)
	}
	if _, ok := readChatMeta(filepath.Join(t.TempDir(), "missing.db")); ok {
		t.Error("a missing store.db read as ok")
	}
}
