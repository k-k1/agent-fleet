package writeevidence

import (
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func TestSaveScopesAndConcurrentResults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	scope := Scope{SessionID: "slot", ThreadID: "thread", TurnID: "turn/opaque"}
	var commands sync.WaitGroup
	for _, path := range []string{"second", "first", "second"} {
		commands.Go(func() {
			if err := Save(Evidence{Scope: scope, Root: root, Paths: []string{path}}); err != nil {
				t.Error(err)
			}
		})
	}
	commands.Wait()
	got, err := load(scope)
	if err != nil || got.Root != root || !slices.Equal(got.Paths, []string{"first", "second"}) {
		t.Fatalf("load after concurrent writes = %+v, %v", got, err)
	}
	for _, other := range []Scope{
		{SessionID: "fork", ThreadID: scope.ThreadID, TurnID: scope.TurnID},
		{SessionID: scope.SessionID, ThreadID: "another-thread", TurnID: scope.TurnID},
		{SessionID: scope.SessionID, ThreadID: scope.ThreadID, TurnID: "another-turn"},
	} {
		if got, err := load(other); err != nil || len(got.Paths) > 0 {
			t.Fatalf("load for unrelated scope %v = %+v, %v", other, got, err)
		}
	}
	if err := Save(Evidence{Scope: scope, Root: t.TempDir(), Paths: []string{"unrelated"}}); err == nil {
		t.Fatal("stored evidence after workspace root changed")
	}
	for _, path := range []string{"../other", "/outside", ".", ".git/index", string([]byte{0xff})} {
		if err := Save(Evidence{Scope: scope, Root: root, Paths: []string{path}}); err == nil {
			t.Errorf("stored invalid path %q", path)
		}
	}
	if err := Save(Evidence{Scope: scope, Root: root, Paths: []string{strings.Repeat("x", maxTurnBytes)}}); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversize observation returned %v, want ErrLimit", err)
	}
	if got, err = load(scope); err != nil || !slices.Equal(got.Paths, []string{"first", "second"}) {
		t.Fatalf("rejected write changed the valid record: %+v, %v", got, err)
	}
}

func TestFileEditsRetainsPatchesWhenStoreIsUnreadable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	scope := Scope{SessionID: "slot", ThreadID: "thread", TurnID: "turn"}
	root := t.TempDir()
	rows := []transcript.Turn{
		{Role: "assistant", AnchorID: scope.TurnID, Cwd: root, Parts: []transcript.Part{
			{Kind: "tool", File: "patched", Verb: "delete"},
		}},
	}
	if err := Save(Evidence{Scope: scope, Root: root, Paths: []string{"observed"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(turnPath(scope), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := FileEdits(scope, rows)
	if err == nil || len(got) != 1 || got[0].Path != "patched" || got[0].Verb != "delete" {
		t.Fatalf("FileEdits with unreadable evidence = %+v, %v", got, err)
	}
}
