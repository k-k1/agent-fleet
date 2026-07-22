package notice

import (
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
	// docs/37 契約4: Put fans the event out into the chat-bridge queue too.
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
