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
	script := "#!/bin/sh\ncase \"$*\" in *alive*) exit 0;; esac\nexit 1\n"
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
