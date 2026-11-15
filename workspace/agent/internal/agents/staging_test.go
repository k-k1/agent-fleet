package agents

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A CLI must derive every path from the staging home: a leftover XDG_* would send its writes to
// the live home.
func TestWithHomeReplacesHomeAndDropsXDG(t *testing.T) {
	got := WithHome([]string{"PATH=/bin", "HOME=/live", "XDG_CONFIG_HOME=/live/.config", "XDG_DATA_HOME=/x", "TERM=xterm"}, "/stage")
	want := "PATH=/bin TERM=xterm HOME=/stage"
	if strings.Join(got, " ") != want {
		t.Fatalf("WithHome = %q, want %q", strings.Join(got, " "), want)
	}
}

func TestInstallFileReplacesWithMode(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "sub", "dst")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallFile(src, dst, 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	fi, _ := os.Stat(dst)
	if string(b) != "new" || fi.Mode().Perm() != 0o600 {
		t.Fatalf("dst = %q %v", b, fi.Mode())
	}
	if ents, _ := os.ReadDir(filepath.Dir(dst)); len(ents) != 1 {
		t.Fatalf("temporary files left: %v", ents)
	}
	if err := InstallFile(filepath.Join(dir, "absent"), dst, 0o600); err == nil {
		t.Fatal("installed a file that does not exist")
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Fatalf("a failed install touched dst: %q", b)
	}
}

// Homes left by a dead Agent are swept; one in use (younger than the TTL) is not.
func TestNewStagingHomeSweepsOnlyStaleHomes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stale, err := NewStagingHome("x")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * stagingTTL)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	fresh, err := NewStagingHome("x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStagingHome("y"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale home kept: %v", err)
	}
	if fi, err := os.Stat(fresh); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("fresh home: %v %v", fi, err)
	}
}

type pollAnswer struct {
	Connected bool   `json:"connected"`
	Stored    bool   `json:"stored"`
	Failed    bool   `json:"failed"`
	Detail    string `json:"detail"`
}

func serve(t *testing.T, p *StagedPoll, id string) (int, pollAnswer) {
	t.Helper()
	rec := httptest.NewRecorder()
	p.Serve(rec, id)
	var a pollAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	return rec.Code, a
}

func stagedTestFlow(t *testing.T, store *FlowStore, script string) (string, string) {
	t.Helper()
	f, err := StartFlow(exec.Command("sh", "-c", script))
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	Stage(f, stage)
	id := store.Put(f)
	t.Cleanup(func() {
		if f := store.Take(id); f != nil {
			f.Close()
		}
	})
	return id, stage
}

// The poll's whole contract in one place: the live login never counts, a finished staged
// sign-in is installed once and said "stored" once, a repeat is connected without it, a CLI
// that exited without signing in is "failed", and a failed install is an error.
func TestStagedPollServe(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewFlowStore(time.Minute)
	installed := 0
	var installErr error
	p := &StagedPoll{
		Flows:        store,
		SignedInAt:   func(home string) bool { _, err := os.Stat(filepath.Join(home, "done")); return err == nil },
		Install:      func(string) error { installed++; return installErr },
		LiveSignedIn: func() bool { return true },
	}

	if _, a := serve(t, p, "unknown"); a.Connected {
		t.Fatal("an unknown flow read the live login as its sign-in")
	}
	id, stage := stagedTestFlow(t, store, "sleep 30")
	if _, a := serve(t, p, id); a.Connected || a.Failed {
		t.Fatalf("pending flow = %+v", a)
	}
	_ = os.WriteFile(filepath.Join(stage, "done"), nil, 0o600)
	if _, a := serve(t, p, id); !a.Connected || !a.Stored || installed != 1 {
		t.Fatalf("finished flow = %+v, installs %d", a, installed)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("staging home left behind: %v", err)
	}
	if _, a := serve(t, p, id); !a.Connected || a.Stored || installed != 1 {
		t.Fatalf("repeat = %+v, installs %d", a, installed)
	}

	dead, _ := stagedTestFlow(t, store, "echo code expired; exit 1")
	<-store.Get(dead).WaitEnded()
	if _, a := serve(t, p, dead); a.Connected || !a.Failed || a.Detail != "code expired" {
		t.Fatalf("exited flow = %+v", a)
	}
	if store.Get(dead) != nil {
		t.Fatal("the exited flow is still registered")
	}

	installErr = errors.New("disk full")
	bad, badStage := stagedTestFlow(t, store, "sleep 30")
	_ = os.WriteFile(filepath.Join(badStage, "done"), nil, 0o600)
	if code, a := serve(t, p, bad); code != http.StatusInternalServerError || a.Connected {
		t.Fatalf("failed install = %d %+v", code, a)
	}
	if _, a := serve(t, p, bad); a.Connected {
		t.Fatal("a failed install answered connected on the next poll")
	}
}
