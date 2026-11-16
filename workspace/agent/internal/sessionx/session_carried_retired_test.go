package sessionx

// A carried answer to a session an overdue run retired and stopped is a person's start (ADR 0137
// §6): it records the act, launches, takes the retired mark off after its own launch, and only
// then delivers.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// carriedEnv is an actEnv whose tmux remembers whether a session is up: has-session answers from a
// marker file, new-session creates it, kill-session removes it. newSession is extra shell run at
// new-session (a launch that fails, say; @META@ is the meta directory).
type carriedEnv struct {
	*actEnv
	delivered chan string
}

func newCarriedEnv(t *testing.T, newSession string) *carriedEnv {
	t.Helper()
	e := newActEnv(t)
	newSession = strings.ReplaceAll(newSession, "@META@", "'"+session.MetaDir()+"'")
	dir := t.TempDir()
	script := `#!/bin/sh
up='` + filepath.Join(dir, "up") + `'
if [ "$1" = "-L" ]; then shift 2; fi
case "$*" in
*new-session*) ` + newSession + `
  touch "$up"; exit 0 ;;
has-session*) [ -e "$up" ]; exit $? ;;
kill-session*) rm -f "$up"; exit 0 ;;
list-panes*|display-message*) echo 1 %1; exit 0 ;;
esac
exit 0
`
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(dir, "up"), nil, 0o644); err != nil { // running at first
		t.Fatal(err)
	}
	c := &carriedEnv{actEnv: e, delivered: make(chan string, 4)}
	prev := deliverInitialPromptFn
	deliverInitialPromptFn = func(_, prompt string) { c.delivered <- prompt }
	t.Cleanup(func() { deliverInitialPromptFn = prev })
	return c
}

type carriedKind struct {
	label, kind, driver string
}

var carriedKinds = []carriedKind{
	{"Terminal (CLI)", session.KindClaude, ""},
	{"Managed", session.KindCodex, session.DriverManaged},
}

// retiredWithCarried makes a session that an overdue run retired and stopped, with a permission
// carried, and returns its meta.
func (c *carriedEnv) retiredWithCarried(t *testing.T, k carriedKind) session.Meta {
	t.Helper()
	m := session.Meta{Name: "s", Kind: k.kind, Driver: k.driver, Dir: t.TempDir()}
	session.WriteMeta(m)
	if code, out := postRetireIf(t, "s", -1); code != http.StatusOK || !reportOf(out, "s").Stopped {
		t.Fatalf("retire = %d %+v", code, out)
	}
	if !status.PutCarried(session.UUID(m.Dir, "s"), status.Carried{Kind: "permission", Permission: "Bash · ls"}, "stopped") {
		t.Fatal("carried not stored")
	}
	if cur, _ := session.ReadMeta("s"); cur.RetiredAt == "" {
		t.Fatal("not retired")
	}
	return m
}

func (c *carriedEnv) answer(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	return postTo(t, HandleSessionCarriedAnswer, "s", "carried-answer", `{"decision":"continue"}`)
}

func (c *carriedEnv) awaitDelivery(t *testing.T, k carriedKind) string {
	t.Helper()
	select {
	case p := <-c.delivered:
		return p
	case in := <-c.handle.got:
		return in.Prompt
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: nothing was delivered", k.label)
		return ""
	}
}

// The carried answer starts the retired, stopped session, is delivered, and leaves no mark.
func TestCarriedAnswerStartsARetiredStoppedSession(t *testing.T) {
	for _, k := range carriedKinds {
		t.Run(k.label, func(t *testing.T) {
			c := newCarriedEnv(t, "")
			m := c.retiredWithCarried(t, k)
			rec := c.answer(t)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("answer = %d %s", rec.Code, rec.Body)
			}
			if p := c.awaitDelivery(t, k); !strings.Contains(p, "許可") {
				t.Fatalf("delivered %q", p)
			}
			cur, _ := session.ReadMeta("s")
			if cur.RetiredAt != "" || cur.RetireClearedID == "" || cur.PersonActMark == 0 {
				t.Fatalf("after the answer: retiredAt=%q clearedID=%q actMark=%d", cur.RetiredAt, cur.RetireClearedID, cur.PersonActMark)
			}
			if _, still := status.ReadCarried(session.UUID(m.Dir, "s")); still {
				t.Fatal("the carried entry was kept after delivery")
			}
			if k.driver == "" && !SessionAlive(cur) {
				t.Fatal("the session was not launched")
			}
		})
	}
}

// A retire decided on a list from before the answer finds the act and releases the session.
func TestRetireDecidedBeforeACarriedAnswerReleases(t *testing.T) {
	for _, k := range carriedKinds {
		t.Run(k.label, func(t *testing.T) {
			c := newCarriedEnv(t, "")
			c.retiredWithCarried(t, k)
			listed := listActMark(t)
			if rec := c.answer(t); rec.Code != http.StatusAccepted {
				t.Fatalf("answer = %d %s", rec.Code, rec.Body)
			}
			c.awaitDelivery(t, k)
			code, out := postRetireIf(t, "s", listed)
			if rep := reportOf(out, "s"); code != http.StatusOK || !rep.Released || rep.Stopped {
				t.Fatalf("a retire older than the answer: %d %+v", code, rep)
			}
		})
	}
}

// A launch that fails leaves the mark as it was, and the carried entry for another try.
func TestCarriedAnswerWithAFailedLaunchLeavesTheMark(t *testing.T) {
	t.Run("Terminal (CLI)", func(t *testing.T) {
		c := newCarriedEnv(t, "exit 1")
		m := c.retiredWithCarried(t, carriedKinds[0])
		before, _ := session.ReadMeta("s")
		rec := c.answer(t)
		cur, _ := session.ReadMeta("s")
		if rec.Code != http.StatusInternalServerError || cur.RetiredAt != before.RetiredAt || cur.RetiredAt == "" {
			t.Fatalf("%d %s retiredAt=%q (was %q)", rec.Code, rec.Body, cur.RetiredAt, before.RetiredAt)
		}
		if _, ok := status.ReadCarried(session.UUID(m.Dir, "s")); !ok {
			t.Fatal("the carried entry was dropped")
		}
	})
	t.Run("Managed", func(t *testing.T) {
		c := newCarriedEnv(t, "")
		m := c.retiredWithCarried(t, carriedKinds[1])
		useLockingDriver(t, session.KindCodex, &lockingDriver{resumeErr: os.ErrInvalid})
		rec := c.answer(t)
		cur, _ := session.ReadMeta("s")
		if rec.Code < 500 || cur.RetiredAt == "" {
			t.Fatalf("%d %s retiredAt=%q", rec.Code, rec.Body, cur.RetiredAt)
		}
		if _, ok := status.ReadCarried(session.UUID(m.Dir, "s")); !ok {
			t.Fatal("the carried entry was dropped")
		}
	})
}

// A mark that cannot be taken off after the launch is reported and nothing is claimed: the carried
// entry stays so the member can press again.
func TestCarriedAnswerReportsAMarkItCouldNotClear(t *testing.T) {
	c := newCarriedEnv(t, "chmod 555 @META@;")
	m := c.retiredWithCarried(t, carriedKinds[0])
	t.Cleanup(func() { _ = os.Chmod(session.MetaDir(), 0o755) })
	rec := c.answer(t)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "retire_clear_failed") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, ok := status.ReadCarried(session.UUID(m.Dir, "s")); !ok {
		t.Fatal("the carried entry was dropped")
	}
	select {
	case p := <-c.delivered:
		t.Fatalf("delivered %q although the start was not claimed", p)
	default:
	}
	// The session is running and still marked. Once the directory is writable again, pressing
	// the card again clears the mark and delivers, instead of delivering over it.
	if err := os.Chmod(session.MetaDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if cur, _ := session.ReadMeta("s"); cur.RetiredAt == "" || !SessionAlive(cur) {
		t.Fatalf("expected a running, marked session: retiredAt=%q", cur.RetiredAt)
	}
	// A modal that came up meanwhile still blocks the prose, marked or not.
	status.Persist(session.UUID(m.Dir, "s"), "question")
	if rec := c.answer(t); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "question_pending") {
		t.Fatalf("answer over a modal = %d %s", rec.Code, rec.Body)
	}
	if cur, _ := session.ReadMeta("s"); cur.RetiredAt == "" {
		t.Fatal("a refused answer took the mark off")
	}
	status.Persist(session.UUID(m.Dir, "s"), "idle")
	if rec := c.answer(t); rec.Code != http.StatusAccepted {
		t.Fatalf("second answer = %d %s", rec.Code, rec.Body)
	}
	c.awaitDelivery(t, carriedKinds[0])
	if cur, _ := session.ReadMeta("s"); cur.RetiredAt != "" || cur.RetireClearedID == "" {
		t.Fatalf("mark left after the retry: %q", cur.RetiredAt)
	}
	if _, ok := status.ReadCarried(session.UUID(m.Dir, "s")); ok {
		t.Fatal("the carried entry was kept after delivery")
	}
}

// A meta that cannot be read is in doubt: refused, and nothing is launched.
func TestCarriedAnswerIsRefusedWhenTheMetaCannotBeRead(t *testing.T) {
	c := newCarriedEnv(t, "")
	c.retiredWithCarried(t, carriedKinds[0])
	if err := os.WriteFile(session.MetaPath("s"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if startForCarriedAnswer(rec, nil, "s") || rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "session_retired") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if SessionAlive(session.Meta{Name: "s"}) {
		t.Fatal("launched on a meta in doubt")
	}
}

// The execution-method switch and the route switch are not a carried answer: they keep refusing
// a session a retire has marked.
func TestSwitchesStillRefuseAMarkedSession(t *testing.T) {
	c := newCarriedEnv(t, "")
	c.retiredWithCarried(t, carriedKinds[1])
	for _, tc := range []struct {
		h          http.HandlerFunc
		path, body string
	}{
		{HandleSessionDriver, "driver", `{"driver":"tui"}`},
		{HandleSessionRouteSwitch, "route-switch", `{"route":"x"}`},
	} {
		rec := postTo(t, tc.h, "s", tc.path, tc.body)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "session_retired") {
			t.Errorf("%s = %d %s", tc.path, rec.Code, rec.Body)
		}
	}
	if cur, _ := session.ReadMeta("s"); cur.RetiredAt == "" {
		t.Fatal("a switch took the mark off")
	}
}
