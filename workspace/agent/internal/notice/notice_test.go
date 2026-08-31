package notice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOutboxPersistsListsAndAcknowledges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := New("question", "s1", "claude", "Project")
	if err := Put(e); err != nil {
		t.Fatal(err)
	}
	// docs/log/37 契約4: Put fans the event out into the chat-bridge queue too.
	bq, _ := os.ReadDir(filepath.Join(home, ".config", "agent-fleet", "bridge-queue"))
	if len(bq) != 1 {
		t.Fatalf("bridge queue entries=%d, want 1", len(bq))
	}
	got := List()
	if len(got) != 1 || got[0].ID != e.ID || got[0].SessionName != "s1" {
		t.Fatalf("events=%+v", got)
	}
	Ack([]string{e.ID, "../unsafe"})
	if got := List(); len(got) != 0 {
		t.Fatalf("events after ack=%+v", got)
	}
}

// 全文ブリッジ (docs/log/37): the answer-ready event's body payload rides into the
// bridge queue entry so a full-text-mode provider can render it.
func TestPutCarriesBodyIntoBridgeQueue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := New("answer-ready", "s1", "claude", "Project")
	e.Payload["body"] = "final turn prose"
	if err := Put(e); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".config", "agent-fleet", "bridge-queue")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("bridge queue entries=%d, want 1", len(entries))
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var q struct {
		Kind string `json:"kind"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(b, &q); err != nil {
		t.Fatal(err)
	}
	if q.Kind != "answer-ready" || q.Body != "final turn prose" {
		t.Fatalf("queued entry=%+v, want body carried", q)
	}
}
