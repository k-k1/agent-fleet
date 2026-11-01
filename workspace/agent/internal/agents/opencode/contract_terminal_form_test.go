//go:build contract

// A Terminal slot's form answered through the form API (RespondTerminal) while a REAL opencode 2 TUI
// (`--server`, in tmux) is attached to the same serve: the stub provider asks, the TUI shows the
// tabbed modal, af answers or dismisses by form id, and the modal closes by itself. Skipped on a v1
// binary or without tmux.
//
//	PATH=<dir with opencode 2>:$PATH go test -tags contract -run TerminalForm ./internal/agents/opencode/
//
// Not run in CI, like the other v2 contracts.
package opencode

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

func TestContractTerminalFormRespond(t *testing.T) {
	_, addr, dir := startStubbedV2(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		skipUnlessV2Required(t, "needs tmux")
	}
	testguard.IsolateTmux(t)
	st := agents.ThreadSettings{Model: "stub/m"}
	api := apiTarget{Addr: addr, Client: serveClient}
	v2ActiveTTL = 0
	t.Cleanup(func() { v2ActiveTTL = 500 * time.Millisecond })
	prev := v2TargetFor
	v2TargetFor = func(session.Meta) apiTarget { return api }
	t.Cleanup(func() { v2TargetFor = prev })

	run := func(name, prompt string, reply func(id string) agents.InteractionReply, wantTurnText string) {
		t.Helper()
		ses, err := serveCreateSession(addr, dir, name)
		if err != nil {
			t.Fatal(err)
		}
		m := session.Meta{Name: name, Dir: dir, Kind: session.KindOpencode, Driver: session.DriverTUI}
		sids.Write(session.UUID(m.Dir, m.Name), ses)
		if err := tagSharedSession(addr, ses, session.UUID(m.Dir, m.Name), name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { sids.Remove(session.UUID(m.Dir, m.Name)) })

		target := session.TmuxName(name)
		cmd := []string{"new-session", "-d", "-s", target, "-x", "140", "-y", "40", "-c", dir}
		for _, kv := range append(envWithHome(os.Getenv("HOME")), "OPENCODE_SERVER_USERNAME="+serveUser, "OPENCODE_SERVER_PASSWORD="+servePassword(), "TERM=xterm-256color") {
			cmd = append(cmd, "-e", kv)
		}
		cmd = append(cmd, "opencode --server "+addr+" --session "+ses)
		if out, err := tmuxx.Cmd(cmd...).CombinedOutput(); err != nil {
			t.Fatalf("tmux new-session: %v %s", err, out)
		}
		t.Cleanup(func() { _ = tmuxx.Cmd("kill-session", "-t", session.ExactTarget(target)).Run() })
		pane := func() string { return tmuxx.CapturePane(target) }

		done := make(chan struct{})
		go func() {
			defer close(done)
			req, err := newTurnRequest(addr, ses, agents.TurnInput{Prompt: prompt})
			if err != nil {
				return
			}
			if res, err := sendTurn(addr, ses, req, st); err == nil {
				res.Body.Close()
			}
		}()
		for deadline := time.Now().Add(40 * time.Second); LiveState(m) != "question"; time.Sleep(200 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("LiveState = %q, want question\n%s", LiveState(m), pane())
			}
		}
		// The TUI shows the form (tabbed: the second question and the Submit tab).
		for deadline := time.Now().Add(20 * time.Second); !strings.Contains(pane(), "Submit"); time.Sleep(300 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the TUI never showed the form:\n%s", pane())
			}
		}
		t.Logf("%s: TUI before the API answer:\n%s", name, pane())
		td, _ := readTranscript(m)
		if len(td.Pending) != 2 || td.Pending[0].ID == "" {
			t.Fatalf("pending = %+v", td.Pending)
		}
		if err := (managedDriver{}).RespondTerminal(m, reply(td.Pending[0].ID)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("the turn did not end after the answer:\n%s", pane())
		}
		for deadline := time.Now().Add(20 * time.Second); strings.Contains(pane(), "Submit"); time.Sleep(300 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the TUI modal stayed open after the API answer:\n%s", pane())
			}
		}
		if wantTurnText != "" {
			for deadline := time.Now().Add(10 * time.Second); !strings.Contains(pane(), wantTurnText); time.Sleep(300 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("the TUI never showed %q:\n%s", wantTurnText, pane())
				}
			}
		}
		// A second answer to the settled form is refused, not posted.
		if err := (managedDriver{}).RespondTerminal(m, reply(td.Pending[0].ID)); err == nil {
			t.Error("a settled form was answered twice")
		}
	}

	run("tfans", "ASKMULTI answer", func(id string) agents.InteractionReply {
		return agents.InteractionReply{ID: id, Decision: agents.DecisionAnswer,
			Answers: []agents.InteractionAnswer{{Options: []int{1}}, {Options: []int{0, 1}}}}
	}, "done")
	run("tfdismiss", "ASKMULTI dismiss", func(id string) agents.InteractionReply {
		return agents.InteractionReply{ID: id, Decision: agents.DecisionCancel}
	}, "done")
}
