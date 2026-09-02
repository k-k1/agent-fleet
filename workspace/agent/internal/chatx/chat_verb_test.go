package chatx

import (
	"bytes"
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Files 翻訳/要約 opens an ad-hoc chat with NO standing assistant (docs/log/30 ②): the verb
// persona is baked onto the conversation directly, so deleting the old 翻訳/汎用 builtins
// costs no capability. Drive handleChatCreate over a mux (real routing) and assert the
// resulting conversation carries the embedded persona, the attached file's dir as
// knowledge, the composed seed, and the persisted SeedVerb (which keeps a translate thread
// language-agnostic — see TestPersonaOfLanguageRule).
func TestChatCreateAdHocVerb(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A file under the browse root (= home) that the verb attaches.
	if err := os.WriteFile(filepath.Join(home, "manual.md"), []byte("# Manual\nhello"), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/conversations", handleChatCreate)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	create := func(t *testing.T, verb string) chatConversation {
		t.Helper()
		body, _ := json.Marshal(chatCreateReq{SeedVerb: verb, AttachPath: "manual.md"})
		res, err := http.Post(srv.URL+"/chat/conversations", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("verb %q: status = %d, want 200", verb, res.StatusCode)
		}
		var c chatConversation
		if err := json.NewDecoder(res.Body).Decode(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}

	t.Run("translate", func(t *testing.T) {
		c := create(t, "translate")
		if c.AssistantID != "" {
			t.Errorf("AssistantID = %q, want empty (no standing assistant)", c.AssistantID)
		}
		if c.SeedVerb != "translate" {
			t.Errorf("SeedVerb = %q, want translate", c.SeedVerb)
		}
		if c.Tools != assistants.ToolsNone {
			t.Errorf("Tools = %q, want none", c.Tools)
		}
		if !strings.Contains(c.Persona, "翻訳") {
			t.Errorf("Persona missing the translate instruction: %q", c.Persona)
		}
		if !strings.Contains(c.Seed, "翻訳してください") || !strings.Contains(c.Seed, "manual.md") {
			t.Errorf("Seed not composed for translate: %q", c.Seed)
		}
		// The attached file's dir must be in knowledge so --add-dir can read it.
		found := false
		for _, d := range c.Knowledge {
			if d == home {
				found = true
			}
		}
		if !found {
			t.Errorf("Knowledge = %v, want it to include %q", c.Knowledge, home)
		}
		// Reload from disk: the ad-hoc verb must persist (Seed is transient and must NOT).
		got, err := loadConv(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.SeedVerb != "translate" {
			t.Errorf("persisted SeedVerb = %q, want translate", got.SeedVerb)
		}
		if got.Seed != "" {
			t.Errorf("Seed must not be persisted, got %q", got.Seed)
		}
	})

	t.Run("summarize", func(t *testing.T) {
		c := create(t, "summarize")
		if c.SeedVerb != "summarize" {
			t.Errorf("SeedVerb = %q, want summarize", c.SeedVerb)
		}
		if !strings.Contains(c.Persona, "要約") {
			t.Errorf("Persona missing the summarize instruction: %q", c.Persona)
		}
	})
}
