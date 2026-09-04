package sessionx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// feedStatusHook drives RunSessionStatusHook once with the given claude hook JSON on
// stdin (as the real claude hook does), restoring os.Stdin afterward.
func feedStatusHook(t *testing.T, state, stdinJSON string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(stdinJSON); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()
	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig }()
	RunSessionStatusHook([]string{state})
	_ = r.Close()
}

func TestWorkingToIdleQueuesDurableNotification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s1", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)
	status.Persist(sid, "working")
	RunSessionStatusHook([]string{"idle", sid})
	events := notice.List()
	if len(events) != 1 || events[0].Kind != "answer-ready" || events[0].SessionName != "s1" || events[0].DisplayName != "Project" {
		t.Fatalf("events=%+v", events)
	}
}

// The pane-based idle heal (claude WireLive) calls status.Remove(sid) when the TUI looks
// like it's back at the ready prompt — which a footer-string drift makes fire MID-turn.
// That wipe deletes the "working" marker, so the Stop hook legitimately ending the turn
// later reads previous=="" instead of "working". Keying answer-ready on previous=="working"
// alone then dropped the terminal transition on the floor: no answer-ready notification and no
// operator session report (docs/log/30) — the instruction's completion never reached the
// operator, while the session still read idle (LiveState defaults to idle with no file).
func TestIdleReportsAfterHealWipedWorkingMarker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s1", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	status.Persist(sid, "working")
	status.Remove(sid) // the WireLive heal wipes the marker mid-turn

	RunSessionStatusHook([]string{"idle", sid})

	events := notice.List()
	if len(events) != 1 || events[0].Kind != "answer-ready" {
		t.Fatalf("completion lost after the heal wiped the working marker: events=%+v", events)
	}
}

// Guard the widened rule: an idle→idle repeat (a Stop with no turn in between, or the
// boot reset that already persisted idle) is NOT a completion and must stay silent, or
// every poll would re-report.
func TestIdleFromIdleDoesNotReport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s1", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	status.Persist(sid, "idle")
	RunSessionStatusHook([]string{"idle", sid})

	if events := notice.List(); len(events) != 0 {
		t.Fatalf("idle→idle must not report: events=%+v", events)
	}
}

// A permission prompt for AskUserQuestion fires between that tool's PreToolUse and
// PostToolUse. It must NOT destroy the captured question — otherwise the Console loses
// the options and shows only the bare permission block.
func TestPermissionPromptKeepsPendingQuestion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "sess-abc"

	// 1) PreToolUse(AskUserQuestion) → question, with the questions payload on stdin.
	feedStatusHook(t, "question", `{"session_id":"`+sid+`","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Pick"}]}}`)
	if _, ok := status.ReadPendingQuestion(sid); !ok {
		t.Fatal("pending question not written on the question hook")
	}

	// 2) Notification(permission_prompt) → permission. Must retain the question.
	feedStatusHook(t, "permission", `{"session_id":"`+sid+`","notification_type":"permission_prompt","message":"needs permission"}`)

	st, ok := status.Read(sid)
	if !ok || st.State != "permission" {
		t.Fatalf("status = %v (ok=%v), want permission", st.State, ok)
	}
	if _, ok := status.ReadPendingQuestion(sid); !ok {
		t.Fatal("permission prompt destroyed the pending question (regression)")
	}
	if _, ok := status.ReadPendingPermission(sid); !ok {
		t.Fatal("pending permission not written")
	}

	// 3) PostToolUse(AskUserQuestion) → working clears the question via its own lifecycle.
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","tool_name":"AskUserQuestion"}`)
	if _, ok := status.ReadPendingQuestion(sid); ok {
		t.Fatal("pending question not cleared when the question was answered")
	}
}

// Regression test for the user report (2026-08-24) that an AUQ raised while a background
// subagent or workflow is running cannot be answered. Measured: a subagent's tools fire
// PostToolUse under the SAME session_id as the parent, so a heartbeat "working" arrives while
// the question modal is still up. Clearing the pending payload on that never gets a second
// chance to be rewritten (WritePendingQuestion is written in exactly one place, the
// PreToolUse of AskUserQuestion), leaving the Console with only the inert transcript-derived
// card = the answer form disappears.
func TestBackgroundToolHeartbeatKeepsPendingQuestion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s-bg-auq", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	feedStatusHook(t, "question", `{"session_id":"`+sid+`","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Pick"}]}}`)

	// A Bash run by the subagent in the background finished (PostToolUse(*) heartbeat).
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","tool_name":"Bash"}`)

	if _, ok := status.ReadPendingQuestion(sid); !ok {
		t.Fatal("PostToolUse of a background tool cleared the pending question (the answer form stops appearing)")
	}
	// Display and decision must agree on "waiting for an answer" too: if only the state turns
	// into working, the list claims the session is running and promptBlocker's guard comes
	// off, so free text is silently swallowed by the modal.
	if got := wireSession(m, true).State; got != "question" {
		t.Errorf("sessions-list badge = %q, want question", got)
	}
	if got := DriveState(m, true, false); got != "question" {
		t.Errorf("chat chip = %q, want question", got)
	}
	if got := promptBlocker(m.Name); got != "question" {
		t.Errorf("promptBlocker = %q, want question (free text must be steered to the question card)", got)
	}

	// Once answered, the question's own PostToolUse does clear it.
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","tool_name":"AskUserQuestion"}`)
	if _, ok := status.ReadPendingQuestion(sid); ok {
		t.Fatal("the pending question survived being answered (ghost card)")
	}
	if got := DriveState(m, true, false); got != "working" {
		t.Errorf("chat chip = %q, want working (back to running once answered)", got)
	}
}

// Same shape: an ExitPlanMode approval card must not be cleared by a background tool's
// heartbeat either.
func TestBackgroundToolHeartbeatKeepsPendingPlan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s-bg-plan", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	feedStatusHook(t, "plan", `{"session_id":"`+sid+`","tool_name":"ExitPlanMode","tool_input":{"plan":"## やること"}}`)
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","tool_name":"Read"}`)

	if plan, ok := status.ReadPendingPlan(sid); !ok || plan == "" {
		t.Fatal("PostToolUse of a background tool cleared the pending plan")
	}
	if got := DriveState(m, true, false); got != "plan" {
		t.Errorf("chat chip = %q, want plan", got)
	}

	feedStatusHook(t, "working", `{"session_id":"`+sid+`","tool_name":"ExitPlanMode"}`)
	if plan, ok := status.ReadPendingPlan(sid); ok && plan != "" {
		t.Fatal("the pending plan survived being approved")
	}
}

// A tool run by a subagent in the background must not make an idle session look like it is
// running. Such a PostToolUse carries agent_id (measured on 2.1.252: only tools originating
// from a subagent have it, while session_id stays the parent's), which is how they are told
// apart. Before that distinction existed, a session sitting at the ready prompt had "working"
// rewritten every 7-12 seconds, showed as running with a stop button, and never once reached
// the background detection that only runs while idle (idle, subagent running) — 2026-09-01
// sf2ykxk.
func TestSubagentHeartbeatDoesNotResurrectIdle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s-bg-idle", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	// The turn is over (idle from the Stop hook); only the background agent is still running.
	feedStatusHook(t, "idle", `{"session_id":"`+sid+`"}`)
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","tool_name":"Bash","agent_id":"a9b653db412c21662","agent_type":"general-purpose"}`)

	if st, _ := status.Read(sid); st.State != "idle" {
		t.Fatalf("status = %q, want idle (a background subagent's tool resurrected the turn)", st.State)
	}
	if got := wireSession(m, true).State; got != "idle" {
		t.Errorf("sessions-list badge = %q, want idle", got)
	}

	// agent_id is the only axis of the distinction. The same tool coming from the parent
	// thread (no agent_id) still re-raises working, as evidence that the turn is alive.
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","tool_name":"Bash"}`)
	if st, _ := status.Read(sid); st.State != "working" {
		t.Fatalf("status = %q, want working (the parent thread's heartbeat must not be stopped too)", st.State)
	}

	// Re-raising a turn that IS running still passes: during a long turn of a foreground
	// subagent its tools are the only heartbeat, and dropping them brings false-idle back.
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","tool_name":"Bash","agent_id":"a9b653db412c21662"}`)
	if st, _ := status.Read(sid); st.State != "working" {
		t.Fatalf("status = %q, want working (dropped the heartbeat of a foreground subagent)", st.State)
	}
}

// A working with no tool (UserPromptSubmit = the start of a new turn) still clears it. Keep
// the payload here and the previous question's ghost card is carried into the next turn.
func TestUserPromptSubmitClearsPendingQuestion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "sess-newturn"
	feedStatusHook(t, "question", `{"session_id":"`+sid+`","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"x"}]}}`)
	feedStatusHook(t, "working", `{"session_id":"`+sid+`"}`) // UserPromptSubmit: no tool_name
	if _, ok := status.ReadPendingQuestion(sid); ok {
		t.Fatal("the pending question was not cleared by the start of a new turn")
	}
}

// …and the DISPLAY must name the modal that is actually on screen. The overlay above
// leaves the raw state at "permission" for the whole time the question menu is up, so a
// badge reading the raw state showed "waiting for permission" for an AskUserQuestion — the
// card in the mirror says question, the chip next to it says permission, and the answer
// path (promptBlocker, which already resolved the overlap) disagrees with both. Pinned on
// both display paths: the sessions-list badge (wireSession→WireLive) and the chat chip
// (DriveState).
func TestPendingQuestionBadgeReadsQuestionNotPermission(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s-auq", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	feedStatusHook(t, "question", `{"session_id":"`+sid+`","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Pick"}]}}`)
	feedStatusHook(t, "permission", `{"session_id":"`+sid+`","notification_type":"permission_prompt","message":"needs permission"}`)

	if got := wireSession(m, true).State; got != "question" {
		t.Errorf("sessions-list badge = %q, want question (the AUQ's permission_prompt is overwriting the state)", got)
	}
	if got := DriveState(m, true, false); got != "question" {
		t.Errorf("chat chip = %q, want question", got)
	}
}

// A plain tool permission (no captured question/plan) keeps saying permission — the
// override is scoped to the overlay, not a blanket rename.
func TestPlainPermissionStaysPermission(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s-perm", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	feedStatusHook(t, "permission", `{"session_id":"`+sid+`","notification_type":"permission_prompt","message":"needs permission"}`)

	if got := wireSession(m, true).State; got != "permission" {
		t.Errorf("sessions-list badge = %q, want permission", got)
	}
}

// A non-permission state (idle/working) still clears a stale pending question, so the
// retention above is scoped to the permission overlay only.
func TestIdleClearsPendingQuestion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "sess-idle"
	feedStatusHook(t, "question", `{"session_id":"`+sid+`","tool_input":{"questions":[{"question":"x"}]}}`)
	if _, ok := status.ReadPendingQuestion(sid); !ok {
		t.Fatal("precondition: question should be pending")
	}
	feedStatusHook(t, "idle", `{"session_id":"`+sid+`"}`)
	if _, ok := status.ReadPendingQuestion(sid); ok {
		t.Fatal("idle should clear the pending question")
	}
}

// boot (the SessionStart hook) resets a session to idle, so a session killed mid-turn and
// then resumed — where no Stop ever fired — doesn't badge as running forever off a stale
// "working" status file.
func TestBootResetsStaleWorkingToIdle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, source := range []string{"startup", "resume", "clear"} {
		t.Run(source, func(t *testing.T) {
			sid := "sess-boot-" + source
			status.Persist(sid, "working") // stale: killed mid-turn, never got a Stop
			feedStatusHook(t, "boot", `{"session_id":"`+sid+`","source":"`+source+`"}`)
			if got := status.LiveState(sid); got != "idle" {
				t.Errorf("after boot(source=%s): state = %q, want idle", source, got)
			}
		})
	}
}

// ...but source=="compact" must NOT reset, because auto-compaction resumes the SAME
// in-flight turn: SessionStart fires mid-turn there, and idling it would false-idle live
// work — the very bug the reverse-heal exists to undo. This guard has no other test and
// its loss is silent (the session merely reads idle while claude is still working), so it
// is pinned here explicitly.
func TestBootSkipsAutoCompactToAvoidFalseIdle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "sess-boot-compact"
	status.Persist(sid, "working") // a real, live turn
	feedStatusHook(t, "boot", `{"session_id":"`+sid+`","source":"compact"}`)
	if got := status.LiveState(sid); got != "working" {
		t.Errorf("boot(source=compact) reset a live turn to %q — auto-compaction resumes the same turn, so it must stay working", got)
	}
}

// A boot reset is not an answer — it must not queue the answer-ready notification. Otherwise
// resuming a session killed mid-turn would ping the user as though claude had replied.
func TestBootDoesNotQueueAnswerReadyNotification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s-boot", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)
	status.Persist(sid, "working")
	feedStatusHook(t, "boot", `{"session_id":"`+sid+`","source":"resume"}`)
	if events := notice.List(); len(events) != 0 {
		t.Errorf("boot queued %d notification(s), want 0: %+v", len(events), events)
	}
}

// TestAbortedTurnReportsAsAnswerReady pins the transition the docs/log/47 fix rests on: a
// turn cut off by a transient error is a TERMINAL event (the session is back at the ready
// prompt, the instruction's one report must fire) but carries the turn-aborted reason so the
// operator is told to resume rather than to stop and fix a cause. previous=="" must work
// too — the pane heal can have removed the working marker before this runs.
func TestAbortedTurnReportsAsAnswerReady(t *testing.T) {
	for _, previous := range []string{"working", ""} {
		t.Run("previous="+previous, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			m := session.Meta{Name: "s-abort", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
			session.WriteMeta(m)
			sid := session.UUID(m.Dir, m.Name)

			RecordSessionNotification(sid, previous, agents.StateAborted, "API Error: Connection closed mid-response.")

			events := notice.List()
			if len(events) != 1 {
				t.Fatalf("queued %d notification(s), want 1: %+v", len(events), events)
			}
			if events[0].Kind != chatx.ReportKindAnswerReady {
				t.Errorf("kind = %q, want %q", events[0].Kind, chatx.ReportKindAnswerReady)
			}
			if body, _ := events[0].Payload["body"].(string); !strings.Contains(body, "Connection closed") {
				t.Errorf("bridge body lost the error text: %q", body)
			}
		})
	}
}

// An aborted turn is a completion, not an interim event: question / plan must NOT be
// reported as one (they leave the arm intact by design).
func TestAbortedStateOnlyFiresFromWorkingOrEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s-abort2", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	RecordSessionNotification(sid, "question", agents.StateAborted, "boom")
	if events := notice.List(); len(events) != 0 {
		t.Errorf("aborted from %q queued %d notification(s), want 0: %+v", "question", len(events), events)
	}
}

func TestManagedAbortedTurnPersistsResumeSignalAndKeepsNotice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "oc-abort", Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverManaged}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	RecordSessionNotification(sid, "working", agents.StateAborted, "[error] APIError (HTTP 500): Internal server error")

	sig, ok := managedAbortSignals.Read(m.Name)
	if !ok || !strings.Contains(sig.Msg, "HTTP 500") || sig.At == "" {
		t.Fatalf("managed signal = %+v ok=%v", sig, ok)
	}
	events := notice.List()
	if len(events) != 1 || events[0].Kind != chatx.ReportKindAnswerReady {
		t.Fatalf("managed abort notice = %+v", events)
	}
}

// TestTurnEndLabelRefinesStopHookIdle covers the decision that refines the Stop hook's idle
// into "how did it end". With no abort in the transcript it passes straight through (= the
// turn's body rides the bridge); with an abort, the label and the reason take its place.
// Putting the reason in the excerpt is the same contract as managed's MarkTurnEndErr, and it
// is where the report and the full-text bridge read the cause of failure from.
func TestTurnEndLabelRefinesStopHookIdle(t *testing.T) {
	orig := claudeAbortInfo
	t.Cleanup(func() { claudeAbortInfo = orig })

	for _, tc := range []struct {
		name      string
		state     string
		abort     claude.Abort
		ok        bool
		wantState string
		wantText  string
	}{
		{"no abort", "idle", claude.Abort{}, false, "idle", "ターンの本文"},
		{"usage limit (a resend gets the same)", "idle",
			claude.Abort{Msg: "You've reached your Fable 5 limit."}, true,
			agents.StateFailed, "You've reached your Fable 5 limit."},
		{"connection dropped (a resend fixes it)", "idle",
			claude.Abort{Msg: "API Error: Connection closed mid-response.", Retryable: true}, true,
			agents.StateAborted, "API Error: Connection closed mid-response."},
		// Anything other than idle is not terminal, so an abort left in the transcript is
		// not acted on.
		{"working passes through", "working",
			claude.Abort{Msg: "You've reached your Fable 5 limit."}, true, "working", "ターンの本文"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claudeAbortInfo = func(string) (claude.Abort, bool) { return tc.abort, tc.ok }
			gotState, gotText := turnEndLabel("sid", tc.state, "ターンの本文")
			if gotState != tc.wantState || gotText != tc.wantText {
				t.Errorf("turnEndLabel = (%q, %q), want (%q, %q)",
					gotState, gotText, tc.wantState, tc.wantText)
			}
		})
	}
}

// TestStopHookOnUsageLimitReportsFailure is the wiring test for the gap measured on
// 2026-08-05 (s6no6jv): on a 429 usage limit claude folds the turn up as complete and FIRES
// STOP, so the marker goes idle first and the pane-driven self-heal (HealIdle) skips past on
// its `state != "idle"` guard. A turn that died on the limit was therefore notified as "the
// answer is ready", with the reason for the failure nowhere to be seen. This plants a real
// transcript, runs the hook path as is, and pins that the reason rides the bridge body.
func TestStopHookOnUsageLimitReportsFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	m := session.Meta{Name: "s-limit", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	dir := filepath.Join(home, ".claude", "projects", "-proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The measured order (s6no6jv): a synthetic assistant record, then turn_duration, then Stop.
	body := `{"type":"user","message":{"content":"go"}}` + "\n" +
		`{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":429,"error":"rate_limit",` +
		`"message":{"content":[{"type":"text","text":"You've reached your Fable 5 limit. ` +
		`Run /usage-credits to continue or switch models with /model."}]}}` + "\n" +
		`{"type":"system","subtype":"turn_duration","durationMs":1136000}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	status.Persist(sid, "working")
	status.AppendPendingText(sid, "上限に当たる前まで書いていた回答")
	RunSessionStatusHook([]string{"idle", sid})

	events := notice.List()
	if len(events) != 1 || events[0].Kind != chatx.ReportKindAnswerReady {
		t.Fatalf("notifications = %+v, want exactly one answer-ready", events)
	}
	got, _ := events[0].Payload["body"].(string)
	if !strings.Contains(got, "Fable 5 limit") {
		t.Errorf("the bridge body does not carry the reason for the failure: %q", got)
	}
	if strings.Contains(got, "上限に当たる前まで") {
		t.Errorf("the bridge body carries the turn's text instead of the reason for the failure: %q", got)
	}
	// The marker stays idle as before (the session really is back at the ready prompt).
	if st, _ := status.Read(sid); st.State != "idle" {
		t.Errorf("status = %q, want idle", st.State)
	}
}

// When claude re-creates its own session (switching to the full-screen TUI, for instance),
// --session-id is dropped from the argv of the restart and every later hook names a different
// id that claude allocated itself (internal/agents/claude/sid.go). Keying off that id writes
// both the status and the notifications where the Console never reads, and the session looks
// as though it vanished entirely — which is what happened on s56ynzz (the status was written
// under claude's id and the slot's was empty). The hook pulls it back to the slot via
// AF_SESSION_NAME and then records the drift in the ledger.
func TestClaudeHookSIDDriftStaysOnSlotAndIsRecorded(t *testing.T) {
	home := t.TempDir()
	cfg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	m := session.Meta{Name: "s56ynzz", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Novel"}
	session.WriteMeta(m)
	t.Setenv("AF_SESSION_NAME", m.Name)
	slot := session.UUID(m.Dir, m.Name)

	// The log claude actually writes (under a fresh random id).
	const drifted = "47153840-14be-4739-9326-93e8657df1bd"
	projects := filepath.Join(cfg, "projects", "-tmp-repo")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projects, drifted+".jsonl"),
		[]byte(`{"type":"user","message":{"content":"hi"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status.Persist(slot, "working")
	feedStatusHook(t, "idle", `{"session_id":"`+drifted+`"}`)

	if st, _ := status.Read(slot); st.State != "idle" {
		t.Fatalf("slot status = %q, want idle — it was written under the drifted id", st.State)
	}
	if _, ok := status.Read(drifted); ok {
		t.Fatal("the status was written under claude's own id — the Console cannot see it there")
	}
	events := notice.List()
	if len(events) != 1 || events[0].Kind != "answer-ready" || events[0].SessionName != "s56ynzz" {
		t.Fatalf("events=%+v, want one answer-ready for the slot", events)
	}
	// The drift is recorded, so from here on the transcript is read from the file claude writes.
	if got := claude.LiveSID(slot); got != drifted {
		t.Fatalf("LiveSID = %q, want the drifted id %q", got, drifted)
	}
}
