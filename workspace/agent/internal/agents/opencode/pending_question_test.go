package opencode

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// fakeTmuxSession puts a tmux on PATH whose session was created at created (zero: no session),
// its pane running process pid when one is given.
func fakeTmuxSession(t *testing.T, created time.Time, pid ...int) {
	t.Helper()
	bin := t.TempDir()
	stamp := ""
	if !created.IsZero() {
		stamp = fmt.Sprint(created.Unix())
		if len(pid) > 0 {
			stamp += fmt.Sprintf(" %d", pid[0])
		}
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
	// The asking message comes from the same opencode process as its question, so it must not
	// predate a pane process that started just before the question.
	insMsg(t, db, "m2", ses, int(askedMs)-50, `{"role":"assistant","time":{"created":1}}`)
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

	fakeTmuxSession(t, asked.Truncate(time.Second)) // replaced within the question's second
	if got := TerminalModal(m); got != "" {
		t.Errorf("pane replaced within the question's second: TerminalModal = %q, want none", got)
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

// With the pane's process readable, its start splits even the second tmux stamps: a question the
// new opencode asks within that second is its own, and one left from before it is not.
func TestOpenQuestionIsSplitAtThePaneProcessStart(t *testing.T) {
	pid, start := startPaneProcess(t) // before a fake tmux takes over PATH
	for name, c := range map[string]struct {
		asked time.Time
		want  string
	}{
		"asked by the pane's own opencode": {start.Add(100 * time.Millisecond), "question"},
		"left from before it":              {start.Add(-100 * time.Millisecond), ""},
	} {
		t.Run(name, func(t *testing.T) {
			m := questionStore(t, c.asked.UnixMilli())
			fakeTmuxSession(t, c.asked.Truncate(time.Second), pid)
			if got := TerminalModal(m); got != c.want {
				t.Errorf("TerminalModal = %q, want %q", got, c.want)
			}
		})
	}
}

// startPaneProcess starts a process that stands in for the pane's, and returns it with its start
// as the Agent reads it. Call it before a fake tmux takes over PATH.
func startPaneProcess(t *testing.T) (int, time.Time) {
	t.Helper()
	bin, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("no sleep binary: %v", err)
	}
	cmd := exec.Command(bin, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	start, ok := tmuxx.ProcessStart(cmd.Process.Pid)
	if !ok {
		t.Skip("no /proc to read a process start from")
	}
	return cmd.Process.Pid, start
}

// A SIGKILL mid-turn leaves the conversation's newest message incomplete for good, and the
// relaunch starts a fresh conversation while the slot's mapping still names this one until the
// first prompt. That turn is not running: the badge, the chat chip and the reaper must not read
// it as working (#1265). A turn the pane's own opencode started still is.
func TestLiveStateIgnoresATurnOfADeadProcess(t *testing.T) {
	sent := time.Date(2026, 9, 30, 2, 17, 18, 62e6, time.UTC)
	db := newOpencodeLiveStore(t)
	m := session.Meta{Dir: "/home/dev/repos/x", Name: "oc-dead", Kind: session.KindOpencode}
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES('ses_d',NULL,?,1)`, m.Dir); err != nil {
		t.Fatal(err)
	}
	sids.Write(session.UUID(m.Dir, m.Name), "ses_d")
	ms := sent.UnixMilli()
	insMsg(t, db, "m1", "ses_d", int(ms), `{"role":"user","time":{"created":1}}`)
	insMsg(t, db, "m2", "ses_d", int(ms)+1000, `{"role":"assistant","time":{"created":2}}`)

	fakeTmuxSession(t, sent.Add(-time.Minute)) // the pane running the turn
	if got := LiveState(m); got != "working" {
		t.Errorf("turn of the live process: LiveState = %q, want working", got)
	}

	fakeTmuxSession(t, sent.Add(time.Minute)) // relaunched after a SIGKILL
	if got := LiveState(m); got != "idle" {
		t.Errorf("turn of a dead process: LiveState = %q, want idle", got)
	}

	managed := m
	managed.Driver = session.DriverManaged // its turns run in the serve daemon, not a pane
	if got := LiveState(managed); got != "working" {
		t.Errorf("managed: LiveState = %q, want working", got)
	}

	// The relaunched process's own prompt in the same conversation is live again.
	insMsg(t, db, "m3", "ses_d", int(sent.Add(2*time.Minute).UnixMilli()), `{"role":"user","time":{"created":3}}`)
	if got := LiveState(m); got != "working" {
		t.Errorf("prompt of the relaunched process: LiveState = %q, want working", got)
	}
}

// With the pane's process readable, a turn its own opencode starts within tmux's second is
// working, and one left from before it is not.
func TestLiveStateIsSplitAtThePaneProcessStart(t *testing.T) {
	pid, start := startPaneProcess(t)
	for name, c := range map[string]struct {
		sent time.Time
		want string
	}{
		"sent to the pane's own opencode": {start.Add(100 * time.Millisecond), "working"},
		"left from before it":             {start.Add(-100 * time.Millisecond), "idle"},
	} {
		t.Run(name, func(t *testing.T) {
			db := newOpencodeLiveStore(t)
			m := session.Meta{Dir: "/home/dev/repos/x", Name: "oc-split", Kind: session.KindOpencode}
			if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES('ses_s',NULL,?,1)`, m.Dir); err != nil {
				t.Fatal(err)
			}
			sids.Write(session.UUID(m.Dir, m.Name), "ses_s")
			insMsg(t, db, "m1", "ses_s", int(c.sent.UnixMilli()), `{"role":"user","time":{"created":1}}`)
			fakeTmuxSession(t, c.sent.Truncate(time.Second), pid)
			if got := LiveState(m); got != c.want {
				t.Errorf("LiveState = %q, want %q", got, c.want)
			}
		})
	}
}
