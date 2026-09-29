package opencode

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeTmuxSession puts a tmux on PATH whose session was created at created (zero: no session).
func fakeTmuxSession(t *testing.T, created time.Time) {
	t.Helper()
	bin := t.TempDir()
	stamp := ""
	if !created.IsZero() {
		stamp = fmt.Sprint(created.Unix())
	}
	script := `#!/bin/sh
[ -n "` + stamp + `" ] || exit 1
case "$*" in
  *session_created*) printf '%s\n' "` + stamp + `" ;;
  list-panes*) printf '1 %%7\n' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("AF_TMUX_SOCKET", "")
}

// questionStore lays out a slot whose conversation's in-flight turn is waiting on a question
// asked at askedMs, and returns the Terminal meta that reads it.
func questionStore(t *testing.T, askedMs int64) session.Meta {
	t.Helper()
	db := newOpencodeLiveStore(t)
	m := session.Meta{Dir: "/home/dev/repos/x", Name: "oc-q", Kind: session.KindOpencode}
	ses := "ses_q"
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,NULL,?,1)`, ses, m.Dir); err != nil {
		t.Fatal(err)
	}
	sids.Write(session.UUID(m.Dir, m.Name), ses)
	insMsg(t, db, "m1", ses, int(askedMs)-2000, `{"role":"user","time":{"created":1}}`)
	insMsg(t, db, "m2", ses, int(askedMs)-1000, `{"role":"assistant","time":{"created":1}}`)
	insPart(t, db, "p1", "m2", ses, int(askedMs),
		`{"type":"tool","tool":"question","state":{"status":"running","input":{"questions":[{"question":"Which animal?","options":[{"label":"いぬ1"},{"label":"ねこ2"}]}]}}}`)
	return m
}

// The question tool's part stays "running" after a SIGKILL (measured 1.18.33: Esc and a SIGHUP
// both close it), and the relaunch starts a fresh conversation while the slot's mapping still
// names the old one. Read as live, that question refuses the very prompt that would move the
// mapping on. A question older than the pane's opencode process is on nobody's screen.
func TestOpenQuestionIgnoresADeadProcess(t *testing.T) {
	asked := time.Date(2026, 9, 30, 2, 17, 18, 62e6, time.UTC)
	m := questionStore(t, asked.UnixMilli())

	fakeTmuxSession(t, asked.Add(-time.Minute)) // the pane that asked it
	if got := LiveState(m); got != "question" {
		t.Errorf("live question: LiveState = %q, want question", got)
	}
	if got := TerminalModal(m); got != "question" {
		t.Errorf("live question: TerminalModal = %q, want question", got)
	}
	if td, _ := readTranscript(m); len(td.Pending) != 1 {
		t.Errorf("live question: Pending = %+v, want the question", td.Pending)
	}

	fakeTmuxSession(t, asked.Add(time.Minute)) // relaunched after a SIGKILL
	if got := LiveState(m); got == "question" {
		t.Errorf("dead question: LiveState = %q", got)
	}
	if got := TerminalModal(m); got != "" {
		t.Errorf("dead question: TerminalModal = %q, want none", got)
	}
	if td, _ := readTranscript(m); len(td.Pending) != 0 {
		t.Errorf("dead question: Pending = %+v, want none", td.Pending)
	}

	fakeTmuxSession(t, time.Time{}) // no pane at all
	if got := TerminalModal(m); got != "" {
		t.Errorf("no pane: TerminalModal = %q, want none", got)
	}

	managed := m
	managed.Driver = session.DriverManaged
	if got := TerminalModal(managed); got != "" {
		t.Errorf("managed: TerminalModal = %q, want none (its driver refuses on its own)", got)
	}
}

// Esc and a SIGHUP (tmux kill-session) close the part and complete the message (measured
// 1.18.33): a dismissed question must not keep refusing sends.
func TestOpencodeQuestionClosedByDismissOrHangup(t *testing.T) {
	for name, part := range map[string]string{
		"Esc":    `{"type":"tool","tool":"question","state":{"status":"error","error":"The user dismissed this question","input":{"questions":[{"question":"Which color?"}]}}}`,
		"SIGHUP": `{"type":"tool","tool":"question","state":{"status":"error","error":"Tool execution aborted","input":{"questions":[{"question":"Which animal?"}]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			db := newOpencodeLiveStore(t)
			m := session.Meta{Dir: "/home/dev/repos/x", Name: "oc-closed", Kind: session.KindOpencode}
			if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES('ses_c',NULL,?,1)`, m.Dir); err != nil {
				t.Fatal(err)
			}
			sids.Write(session.UUID(m.Dir, m.Name), "ses_c")
			insMsg(t, db, "m1", "ses_c", 1000, `{"role":"user","time":{"created":1}}`)
			insMsg(t, db, "m2", "ses_c", 2000, `{"role":"assistant","time":{"created":2,"completed":3}}`)
			insPart(t, db, "p1", "m2", "ses_c", 2500, part)
			fakeTmuxSession(t, time.UnixMilli(500))
			if got := TerminalModal(m); got != "" {
				t.Errorf("TerminalModal = %q, want none", got)
			}
			if got := LiveState(m); got != "idle" {
				t.Errorf("LiveState = %q, want idle", got)
			}
		})
	}
}
