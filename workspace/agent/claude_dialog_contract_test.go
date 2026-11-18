//go:build contract

// Live TUI contract probe for the two dialogs claude draws over the composer besides plan
// approval: the tool-permission dialog and AskUserQuestion (run from claude-tui-contract.yml).
//
// What it protects: ParsePermissionMenu and ModalActive read these dialogs off a captured pane by
// text that is neither a contract nor versioned, and the goldens under
// internal/agents/claude/testdata/permmenu only pin shapes already seen. claude 2.1.327 dropped
// the footer text `Enter to select` and the heading `Ready to submit your answers?` from its
// bundle, and no existing contract draws either dialog, so a drift here would have gone
// unnoticed until a member's answer landed on the wrong row (#4484).
//
// Two parts, one real turn each:
//
//	A. A Bash call in the default mode raises the permission dialog. The production parser must
//	   read it (rows numbered from 1, a deny and an allow choice), the pane must read as a modal
//	   and not as the idle prompt.
//	B. AskUserQuestion draws its own dialog. The pane must read as a modal and not as idle, and
//	   the permission parser must NOT read it as a permission menu: a question misread as a
//	   permission dialog is answered with a digit meant for another dialog.
//
// Both frames are logged in full, so a run on a new pin doubles as the capture to add under
// testdata.
//
// Cost: two real turns, one short tool call each.
package main

import (
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

const dialogWait = 4 * time.Minute

// The command is a harmless echo; in the default mode claude still asks before running it.
const dialogPermissionPrompt = "Run exactly this command with the Bash tool and do nothing else: echo agent-fleet-dialog-probe"

// One question, two options: the review screen only appears with several questions, which this
// probe leaves alone.
const dialogQuestionPrompt = "Use the AskUserQuestion tool right now to ask me one question, \"Pick one\", with the " +
	"two options A and B. Do nothing else."

func TestClaudeDialogContractLive(t *testing.T) {
	spec := tuiMirrorContractSpec{kind: session.KindClaude, agent: claude.New()}

	t.Run("permission", func(t *testing.T) {
		meta, tn, stop := startTUIContractSession(t, spec, "dialog-perm", "")
		defer stop()
		sendDialogPrompt(t, tn, dialogPermissionPrompt)

		var frame string
		var menu claude.PermissionMenu
		ok := waitDialog(tn, func(f string) bool {
			m, parsed := claude.ParsePermissionMenu(f)
			frame, menu = f, m
			return parsed
		})
		if !ok {
			t.Fatalf("no permission dialog the production parser reads appeared within %s (the model never ran Bash, "+
				"or the dialog changed shape: ParsePermissionMenu anchors on a footer line starting with \"Esc to cancel\" "+
				"and a \"Do you want to\" question above row 1)\nModalActive=%v\npane:\n%s",
				dialogWait, tmuxx.ModalActive(meta.Name), tmuxx.CapturePane(tn))
		}
		t.Logf("permission dialog:\n%s", frame)

		if len(menu.Rows) < 2 {
			t.Fatalf("the permission dialog read as %d row(s): %+v", len(menu.Rows), menu.Rows)
		}
		for i, r := range menu.Rows {
			if r.Num != i+1 {
				t.Errorf("row %d is numbered %d: rows must run 1..n without a gap (a digit sent for a row answers another)\n%+v", i, r.Num, menu.Rows)
			}
		}
		var allow, deny bool
		for _, r := range menu.Rows {
			allow = allow || r.Choice == claude.PermAllow
			deny = deny || r.Choice == claude.PermDeny
		}
		if !allow || !deny {
			t.Errorf("the dialog offers allow=%v deny=%v: the Console's permission card cannot answer it\n%+v", allow, deny, menu.Rows)
		}
		if !tmuxx.ModalActive(meta.Name) {
			t.Errorf("a permission dialog is up but ModalActive=false: bare arrow keys would hit the plain composer\n%s", frame)
		}
		if tmuxx.AtIdlePrompt(meta.Name) {
			t.Errorf("a permission dialog is up but AtIdlePrompt=true: a turn waiting on a decision would read as finished\n%s", frame)
		}
	})

	t.Run("question", func(t *testing.T) {
		meta, tn, stop := startTUIContractSession(t, spec, "dialog-ask", "")
		defer stop()
		sendDialogPrompt(t, tn, dialogQuestionPrompt)

		var frame string
		ok := waitDialog(tn, func(f string) bool {
			frame = f
			return strings.Contains(f, "Pick one") && tmuxx.ModalActive(meta.Name)
		})
		if !ok {
			t.Fatalf("no AskUserQuestion dialog read as a modal appeared within %s (the model never asked, or ModalActive "+
				"no longer recognises the dialog)\nModalActive=%v\npane:\n%s",
				dialogWait, tmuxx.ModalActive(meta.Name), tmuxx.CapturePane(tn))
		}
		t.Logf("AskUserQuestion dialog:\n%s", frame)

		if tmuxx.AtIdlePrompt(meta.Name) {
			t.Errorf("a question is waiting but AtIdlePrompt=true: the turn would read as finished\n%s", frame)
		}
		if m, parsed := claude.ParsePermissionMenu(frame); parsed {
			t.Errorf("a question dialog was read as a permission menu (%+v): a digit meant for the permission card would answer it\n%s", m.Rows, frame)
		}
	})
}

// sendDialogPrompt types the prompt and presses Enter, with the production tmux wrapper on the
// contract's private socket.
func sendDialogPrompt(t *testing.T, tn, prompt string) {
	t.Helper()
	if out, err := tmuxx.Cmd("send-keys", "-t", tn, "-l", prompt).CombinedOutput(); err != nil {
		t.Fatalf("send prompt: %v: %s", err, out)
	}
	time.Sleep(time.Second)
	if out, err := tmuxx.Cmd("send-keys", "-t", tn, "Enter").CombinedOutput(); err != nil {
		t.Fatalf("submit prompt: %v: %s", err, out)
	}
}

// waitDialog polls the pane until ok(frame) holds or dialogWait passes.
func waitDialog(tn string, ok func(frame string) bool) bool {
	for deadline := time.Now().Add(dialogWait); time.Now().Before(deadline); time.Sleep(time.Second) {
		if ok(tmuxx.CapturePane(tn)) {
			return true
		}
	}
	return false
}
