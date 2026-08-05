package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// feedStatusHook drives runSessionStatusHook once with the given claude hook JSON on
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
	runSessionStatusHook([]string{state})
	_ = r.Close()
}

func TestWorkingToIdleQueuesDurableNotification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s1", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)
	status.Persist(sid, "working")
	runSessionStatusHook([]string{"idle", sid})
	events := notice.List()
	if len(events) != 1 || events[0].Kind != "answer-ready" || events[0].SessionName != "s1" || events[0].DisplayName != "Project" {
		t.Fatalf("events=%+v", events)
	}
}

// The pane-based idle heal (claude WireLive) calls status.Remove(sid) when the TUI looks
// like it's back at the ready prompt — which a footer-string drift makes fire MID-turn.
// That wipe deletes the "working" marker, so the Stop hook legitimately ending the turn
// later reads previous=="" instead of "working". Keying answer-ready on previous=="working"
// alone then dropped the terminal transition on the floor: no 応答あり notification and no
// operator session report (docs/30) — the instruction's completion never reached the
// operator, while the session still read idle (LiveState defaults to idle with no file).
func TestIdleReportsAfterHealWipedWorkingMarker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "s1", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	status.Persist(sid, "working")
	status.Remove(sid) // the WireLive heal wipes the marker mid-turn

	runSessionStatusHook([]string{"idle", sid})

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
	runSessionStatusHook([]string{"idle", sid})

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
	feedStatusHook(t, "working", `{"session_id":"`+sid+`"}`)
	if _, ok := status.ReadPendingQuestion(sid); ok {
		t.Fatal("pending question not cleared when the question was answered")
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
// then resumed — where no Stop ever fired — doesn't badge 進行中 forever off a stale
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

// A boot reset is not an answer — it must not queue the 応答あり notification. Otherwise
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

// TestAbortedTurnReportsAsAnswerReady pins the transition the docs/47 fix rests on: a
// turn cut off by a transient error is a TERMINAL event (the session is back at 入力待ち,
// the instruction's one report must fire) but carries the turn-aborted reason so the
// operator is told to resume rather than to stop and fix a cause. previous=="" must work
// too — the pane heal can have removed the working marker before this runs.
func TestAbortedTurnReportsAsAnswerReady(t *testing.T) {
	for _, previous := range []string{"working", ""} {
		t.Run("previous="+previous, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			m := session.Meta{Name: "s-abort", Dir: t.TempDir(), Kind: session.KindClaude, Title: "Project"}
			session.WriteMeta(m)
			sid := session.UUID(m.Dir, m.Name)

			recordSessionNotification(sid, previous, agents.StateAborted, "API Error: Connection closed mid-response.")

			events := notice.List()
			if len(events) != 1 {
				t.Fatalf("queued %d notification(s), want 1: %+v", len(events), events)
			}
			if events[0].Kind != reportKindAnswerReady {
				t.Errorf("kind = %q, want %q", events[0].Kind, reportKindAnswerReady)
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

	recordSessionNotification(sid, "question", agents.StateAborted, "boom")
	if events := notice.List(); len(events) != 0 {
		t.Errorf("aborted from %q queued %d notification(s), want 0: %+v", "question", len(events), events)
	}
}

// TestTurnEndLabelRefinesStopHookIdle: Stop フックの idle を「どう終わったか」に精緻化する
// 判定そのもの。転写に中断が無ければ素通し（＝ターンの本文がブリッジに乗る）、中断が
// あればラベルと理由に差し替わる。理由を excerpt に載せるのは managed の MarkTurnEndErr と
// 同じ契約で、報告と全文ブリッジはそこから失敗の原因を読む。
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
		{"中断なし", "idle", claude.Abort{}, false, "idle", "ターンの本文"},
		{"上限（再送しても同じ）", "idle",
			claude.Abort{Msg: "You've reached your Fable 5 limit."}, true,
			agents.StateFailed, "You've reached your Fable 5 limit."},
		{"接続断（再送で直る）", "idle",
			claude.Abort{Msg: "API Error: Connection closed mid-response.", Retryable: true}, true,
			agents.StateAborted, "API Error: Connection closed mid-response."},
		// idle 以外は終端ではないので、転写に中断が残っていても触らない。
		{"working は素通し", "working",
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

// TestStopHookOnUsageLimitReportsFailure is the wiring test for the hole measured on
// 2026-08-05 (s6no6jv): 利用上限の 429 では claude はターンを完了として畳んで **Stop を
// 鳴らす**ので、マーカーが先に idle になり、ペイン由来の自己修復（HealIdle）は
// `state != "idle"` ガードで素通りする。その結果、上限で落ちたターンが「応答が完了」として
// 通知され、失敗の理由がどこにも出なかった。実物の転写を植えて hook 経路をそのまま走らせ、
// 理由がブリッジ本文に乗ることを固定する。
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
	// 実測の並び（s6no6jv）: 合成 assistant レコードのあとに turn_duration が続き、Stop が鳴る。
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
	runSessionStatusHook([]string{"idle", sid})

	events := notice.List()
	if len(events) != 1 || events[0].Kind != reportKindAnswerReady {
		t.Fatalf("通知 = %+v, want answer-ready 1件", events)
	}
	got, _ := events[0].Payload["body"].(string)
	if !strings.Contains(got, "Fable 5 limit") {
		t.Errorf("ブリッジ本文に失敗の理由が出ていない: %q", got)
	}
	if strings.Contains(got, "上限に当たる前まで") {
		t.Errorf("失敗の理由ではなくターンの本文が乗っている: %q", got)
	}
	// マーカーは従来どおり idle（セッションは本当に入力待ちに戻っている）。
	if st, _ := status.Read(sid); st.State != "idle" {
		t.Errorf("status = %q, want idle", st.State)
	}
}
