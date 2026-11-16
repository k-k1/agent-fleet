package sessionx

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// tmuxWhereOnlyAlive answers has-session for a session whose name contains "alive" and fails for
// every other.
func tmuxWhereOnlyAlive(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in *alive*) exit 0;; esac\necho \"can't find session: x\" >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func brokenFiles(t *testing.T) []string {
	t.Helper()
	ents, _ := os.ReadDir(session.MetaDir())
	var out []string
	for _, e := range ents {
		if strings.Contains(e.Name(), ".json.broken-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestSetAsideBrokenMetas(t *testing.T) {
	for label, body := range cutMetaBodies {
		t.Run(label, func(t *testing.T) {
			newRetireEnv(t)
			tmuxWhereOnlyAlive(t)
			writeMeta(t, session.Meta{Name: "p"})
			writeMeta(t, session.Meta{Name: "fine"})
			writeCutMeta(t, "gone", body)
			writeCutMeta(t, "alive-one", body)
			tmpPath := filepath.Join(session.MetaDir(), ".meta-123.tmp")
			if err := os.WriteFile(tmpPath, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}

			// In doubt before: a retire of another root cannot be complete.
			if _, resp := postRetire(t, "p"); resp.Complete {
				t.Fatal("precondition: the unreadable files should keep the retire from completing")
			}

			moved := SetAsideBrokenMetas()
			if len(moved) != 1 || !strings.Contains(moved[0], "gone.json.broken-") {
				t.Fatalf("set aside %v, want only gone", moved)
			}
			if _, st := session.ReadMetaState("gone"); st != session.MetaAbsent {
				t.Fatalf("gone: state %v, want absent", st)
			}
			if got, err := os.ReadFile(moved[0]); err != nil || string(got) != body {
				t.Fatalf("the set-aside file lost its content: %q %v", got, err)
			}
			if _, st := session.ReadMetaState("alive-one"); st != session.MetaUnreadable {
				t.Fatalf("a file whose tmux session runs was moved: state %v", st)
			}
			if _, st := session.ReadMetaState("fine"); st != session.MetaOK {
				t.Fatalf("a readable meta was touched: %v", st)
			}
			if _, err := os.Stat(tmpPath); err != nil {
				t.Fatalf("a .meta-*.tmp file was touched: %v", err)
			}

			// A second start does nothing more.
			if again := SetAsideBrokenMetas(); len(again) != 0 || len(brokenFiles(t)) != 1 {
				t.Fatalf("second start set aside %v, files %v", again, brokenFiles(t))
			}

			// alive-one still keeps the doubt; once it is out of the way a retire completes.
			if _, resp := postRetire(t, "p"); resp.Complete {
				t.Fatal("a file left in doubt no longer keeps the retire from completing")
			}
			if err := os.Remove(session.MetaPath("alive-one")); err != nil {
				t.Fatal(err)
			}
			if code, resp := postRetire(t, "p"); code != http.StatusOK || !resp.Complete {
				t.Fatalf("retire after the set-aside = %d complete=%v %+v", code, resp.Complete, resp.Sessions)
			}
		})
	}
}

// A tmux that fails for any reason other than "no such session" proves nothing: the file stays.
func TestSetAsideBrokenMetasKeepsWhatTmuxCouldNotAnswer(t *testing.T) {
	for label, script := range map[string]string{
		"exit 2":                    "#!/bin/sh\nexit 2\n",
		"exit 1, no reason":         "#!/bin/sh\nexit 1\n",
		"exit 1, permission denied": "#!/bin/sh\necho 'error connecting to /tmp/tmux-1/default (Permission denied)' >&2\nexit 1\n",
	} {
		t.Run(label, func(t *testing.T) {
			newRetireEnv(t)
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			writeCutMeta(t, "x", "")
			if moved := SetAsideBrokenMetas(); len(moved) != 0 {
				t.Fatalf("set aside %v although tmux could not say the session is absent", moved)
			}
			if _, st := session.ReadMetaState("x"); st != session.MetaUnreadable {
				t.Fatalf("state %v, want unreadable", st)
			}
		})
	}
	t.Run("no server running is absent", func(t *testing.T) {
		newRetireEnv(t)
		bin := t.TempDir()
		script := "#!/bin/sh\necho 'no server running on /tmp/tmux-1/default' >&2\nexit 1\n"
		if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		writeCutMeta(t, "x", "")
		if moved := SetAsideBrokenMetas(); len(moved) != 1 {
			t.Fatalf("set aside %v, want x", moved)
		}
	})
}

// A Managed runtime adopted from the previous Agent may be running a session whose meta is
// unreadable: nothing is set aside while any may be up, and only a definite "not running" counts.
func TestSetAsideBrokenMetasWaitsForAdoptableRuntimes(t *testing.T) {
	up := RuntimeProbe{Name: "stub daemon", MayBeRunning: func() bool { return true }}
	down := RuntimeProbe{Name: "stub gone", MayBeRunning: func() bool { return false }}
	cases := []struct {
		name   string
		probes []RuntimeProbe
		moved  int
	}{
		{"a daemon answers", []RuntimeProbe{down, up}, 0},
		{"the probe could not tell (reported as maybe running)", []RuntimeProbe{up}, 0},
		{"no daemon", []RuntimeProbe{down, down}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			newRetireEnv(t)
			tmuxWhereOnlyAlive(t)
			writeCutMeta(t, "x", "")
			if got := SetAsideBrokenMetas(c.probes...); len(got) != c.moved {
				t.Fatalf("set aside %v, want %d", got, c.moved)
			}
			want := session.MetaUnreadable
			if c.moved > 0 {
				want = session.MetaAbsent
			}
			if _, st := session.ReadMetaState("x"); st != want {
				t.Fatalf("state %v, want %v", st, want)
			}
		})
	}
}
