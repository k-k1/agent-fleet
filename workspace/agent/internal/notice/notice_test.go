package notice

import "testing"

func TestOutboxPersistsListsAndAcknowledges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := New("question", "s1", "claude", "Project")
	if err := Put(e); err != nil {
		t.Fatal(err)
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
