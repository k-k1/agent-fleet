package sessionx

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// Every kind with a Terminal route either has a modal probe, or is one of the two cases that
// need none: claude, whose hooks write its question / plan / permission into the status store,
// and shell / ssm, which have no modal. A kind that is neither falls through to a status store
// nothing fills, and free text typed into its modal answers it silently — the hole codex,
// opencode and cursor sat in (#1227).
func TestPromptBlockerCoversEveryTerminalKind(t *testing.T) {
	storeOrNone := map[string]bool{session.KindClaude: true, session.KindShell: true, session.KindSSM: true}
	for kind, a := range agentRegistry {
		if a.Caps().ManagedOnly {
			if _, ok := kindModalProbes[kind]; ok {
				t.Errorf("%s has no Terminal route, yet has a modal probe", kind)
			}
			continue
		}
		_, probed := kindModalProbes[kind]
		if probed == storeOrNone[kind] {
			t.Errorf("%s: modal probe = %v; every Terminal kind needs exactly one of a probe or the status store (claude) / no modal (shell, ssm)", kind, probed)
		}
	}
}

// fakeModalTmux puts a tmux on PATH with one live session, created at created and showing pane.
func fakeModalTmux(t *testing.T, created time.Time, pane string) {
	t.Helper()
	bin := t.TempDir()
	paneFile := filepath.Join(bin, "pane.txt")
	if err := os.WriteFile(paneFile, []byte(pane), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$*" in
  has-session*) exit 0 ;;
  *session_created*) printf '%s\n' "` + fmt.Sprint(created.Unix()) + `" ;;
  list-panes*) printf '1 %%7\n' ;;
  capture-pane*) /bin/cat "` + paneFile + `" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("AF_TMUX_SOCKET", "")
}

// The gate refuses free text while each kind's modal is up and lets it through once the modal
// is gone, reading the modal from wherever the kind keeps it. The fixtures are in the shapes
// measured for #1227 (codex 0.159.0, opencode 1.18.33, cursor 2026.09.28), where a pasted line
// + Enter answered the first option, approved the command, or built the plan.
func TestPromptBlockerReadsEachTerminalKindsModal(t *testing.T) {
	asked := time.Date(2026, 9, 30, 2, 25, 47, 0, time.UTC)
	before, after := asked.Add(-time.Minute), asked.Add(time.Minute)

	t.Run("codex", func(t *testing.T) {
		isolateAgentConfigDirs(t)
		m := writeModalMeta(t, "gate_codex", session.KindCodex)
		id := "01a0ee31-0000-7000-8000-000000000001"
		agents.NewSidStore("codex-sid").Write(session.UUID(m.Dir, m.Name), id)
		ask := []string{
			`{"timestamp":"2026-09-30T02:25:41.600Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
			`{"timestamp":"2026-09-30T02:25:47.331Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call_q","arguments":"{\"questions\":[{\"question\":\"Which animal?\",\"options\":[{\"label\":\"いぬ1\"}]}]}"}}`,
		}
		writeCodexRollout(t, id, ask...)
		fakeModalTmux(t, before, "")
		wantBlocker(t, m.Name, "question", "the question dialog")
		fakeModalTmux(t, after, "")
		wantBlocker(t, m.Name, "", "a question the relaunched pane never showed")
		writeCodexRollout(t, id, append(ask,
			`{"timestamp":"2026-09-30T02:26:08.176Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_q","output":"aborted by user after 21.4s"}}`,
			`{"timestamp":"2026-09-30T02:26:08.190Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t1","reason":"interrupted"}}`)...)
		fakeModalTmux(t, before, "")
		wantBlocker(t, m.Name, "", "the question interrupted with Esc")
	})

	t.Run("opencode", func(t *testing.T) {
		isolateAgentConfigDirs(t)
		m := writeModalMeta(t, "gate_opencode", session.KindOpencode)
		db := opencodeStore(t)
		agents.NewSidStore("opencode-sid").Write(session.UUID(m.Dir, m.Name), "ses_q")
		mustExec(t, db, `INSERT INTO session(id,parent_id,directory,time_created) VALUES('ses_q',NULL,?,1)`, m.Dir)
		mustExec(t, db, `INSERT INTO message VALUES('m1','ses_q',?,?,'{"role":"assistant","time":{"created":1}}')`, asked.UnixMilli()-1, asked.UnixMilli()-1)
		mustExec(t, db, `INSERT INTO part VALUES('p1','m1','ses_q',?,'{"type":"tool","tool":"question","state":{"status":"running","input":{"questions":[{"question":"Which animal?"}]}}}')`, asked.UnixMilli())
		fakeModalTmux(t, before, "")
		wantBlocker(t, m.Name, "question", "the question tool")
		fakeModalTmux(t, after, "")
		wantBlocker(t, m.Name, "", "a question left running by a SIGKILLed process")
		mustExec(t, db, `UPDATE message SET data='{"role":"assistant","time":{"created":1,"completed":2}}'`)
		mustExec(t, db, `UPDATE part SET data='{"type":"tool","tool":"question","state":{"status":"error","error":"The user dismissed this question"}}'`)
		fakeModalTmux(t, before, "")
		wantBlocker(t, m.Name, "", "the question dismissed with Esc")
	})

	t.Run("cursor", func(t *testing.T) {
		isolateAgentConfigDirs(t)
		m := writeModalMeta(t, "gate_cursor", session.KindCursor)
		fakeModalTmux(t, before, cursorPane(t, "pane-approval.txt"))
		wantBlocker(t, m.Name, "permission", "the command approval")
		fakeModalTmux(t, before, cursorPane(t, "pane-approval-esc.txt"))
		wantBlocker(t, m.Name, "permission", "the approval's what-to-do-instead prompt")
		fakeModalTmux(t, before, cursorPane(t, "pane-plan.txt"))
		wantBlocker(t, m.Name, "plan", "the build approval")
		fakeModalTmux(t, before, "  → Add a follow-up\n\n  Auto · 3.7%\n  /home/dev/repos/proj\n")
		wantBlocker(t, m.Name, "", "the composer")
	})

	t.Run("kiro", func(t *testing.T) {
		isolateAgentConfigDirs(t)
		m := writeModalMeta(t, "gate_kiro", session.KindKiro)
		fakeModalTmux(t, before, " shell requires approval\n ❯ Yes, single permission\n")
		wantBlocker(t, m.Name, "question", "the approval panel")
		fakeModalTmux(t, before, "  ask a question or describe a task ↵\n")
		wantBlocker(t, m.Name, "", "the composer")
	})

	t.Run("copilot", func(t *testing.T) {
		isolateAgentConfigDirs(t)
		m := writeModalMeta(t, "gate_copilot", session.KindCopilot)
		sid := "7d5c1f0e-0000-4000-8000-000000000001"
		agents.NewSidStore("copilot-sid").Write(session.UUID(m.Dir, m.Name), sid)
		events := `{"type":"user.message","data":{"content":"x"},"timestamp":"2026-07-21T01:00:11Z"}
{"type":"permission.requested","data":{"requestId":"p1"},"timestamp":"2026-07-21T01:00:12Z"}
`
		writeFile(t, copilot.EventsPath(sid), events)
		wantBlocker(t, m.Name, "question", "the permission menu")
		writeFile(t, copilot.EventsPath(sid), events+`{"type":"permission.completed","data":{"requestId":"p1"},"timestamp":"2026-07-21T01:00:13Z"}`+"\n")
		wantBlocker(t, m.Name, "", "the answered permission")
	})

	t.Run("agy", func(t *testing.T) {
		isolateAgentConfigDirs(t)
		m := writeModalMeta(t, "gate_agy", session.KindAgy)
		agents.NewSidStore("agy-sid").Write(session.UUID(m.Dir, m.Name), "conv-q")
		payload := append([]byte("\x0a\x08s8twu8rq\x12\x0cask_question\xaa\x01"),
			[]byte(`{"questions":[{"is_multi_select":false,"options":["Mountain (M)","Sea (S)"],"question":"Which do you prefer?"}],"toolAction":"Asking"}`)...)
		conv := agyConversation(t, "conv-q")
		mustExec(t, conv, `INSERT INTO steps VALUES (0, 14, 3, 'user')`)
		mustExec(t, conv, `INSERT INTO steps VALUES (1, 138, 9, ?)`, payload) // 9 = awaiting the user
		wantBlocker(t, m.Name, "question", "the ask_question widget")
		mustExec(t, conv, `UPDATE steps SET status = 3 WHERE idx = 1`)
		wantBlocker(t, m.Name, "", "the answered question")
	})
}

// The chip reads what the badge and the gate read. codex's hooks leave a turn on its question
// dialog "working", so without the rollout the chip showed "in progress" while every send was
// refused with question_pending.
func TestCodexQuestionChipAgreesWithTheGate(t *testing.T) {
	isolateAgentConfigDirs(t)
	m := writeModalMeta(t, "chip_codex", session.KindCodex)
	id := "01a0ee31-0000-7000-8000-000000000002"
	agents.NewSidStore("codex-sid").Write(session.UUID(m.Dir, m.Name), id)
	writeCodexRollout(t, id,
		`{"timestamp":"2026-09-30T02:25:41.600Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		`{"timestamp":"2026-09-30T02:25:47.331Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call_q","arguments":"{\"questions\":[{\"question\":\"Which animal?\"}]}"}}`)
	status.Persist(session.UUID(m.Dir, m.Name), "working") // UserPromptSubmit; no hook fires for the dialog
	fakeModalTmux(t, time.Date(2026, 9, 30, 2, 24, 0, 0, time.UTC), "")
	if got := DriveState(m, true, false); got != "question" {
		t.Errorf("chat chip = %q, want question", got)
	}
	if got := wireSession(m, true).State; got != "question" {
		t.Errorf("sessions-list badge = %q, want question", got)
	}
	if got := promptBlocker(m.Name); got != "question" {
		t.Errorf("promptBlocker = %q, want question", got)
	}
}

// cursor's build approval carries its plan through a fold like claude's plan does: the carried
// card shows the body, and approving it delivers prose after the resume.
func TestPromoteCarriedOtherKeepsThePlanBody(t *testing.T) {
	isolateAgentConfigDirs(t)
	m := session.Meta{Name: "carry_plan", Dir: t.TempDir(), Kind: session.KindOpencode}
	session.WriteMeta(m)
	withFakeAgent(t, m.Kind, fakeModalAgent{modal: agents.PendingModal{Kind: "plan", Plan: "# Create plan.txt\n\nWrite hello."}, ok: true})
	if !promoteCarriedOther(m) {
		t.Fatal("promoteCarriedOther = false, want true")
	}
	c, ok := status.ReadCarried(session.UUID(m.Dir, m.Name))
	if !ok || c.Kind != "plan" || c.Plan != "# Create plan.txt\n\nWrite hello." {
		t.Fatalf("carried = %+v, %v", c, ok)
	}
}

func writeModalMeta(t *testing.T, name, kind string) session.Meta {
	t.Helper()
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: kind}
	session.WriteMeta(m)
	return m
}

func wantBlocker(t *testing.T, name, want, what string) {
	t.Helper()
	if got := promptBlocker(name); got != want {
		t.Errorf("%s: promptBlocker = %q, want %q", what, got, want)
	}
}

func writeCodexRollout(t *testing.T, id string, lines ...string) {
	t.Helper()
	writeFile(t, filepath.Join(paths.HomeDir(), ".codex", "sessions", "2026", "09", "30",
		"rollout-2026-09-30T02-23-46-"+id+".jsonl"), strings.Join(lines, "\n")+"\n")
}

func cursorPane(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "agents", "cursor", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// opencodeStore creates the store where opencode keeps it, with the columns the readers use.
func opencodeStore(t *testing.T) *sql.DB {
	t.Helper()
	dir := filepath.Join(paths.HomeDir(), ".local", "share", "opencode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, s := range []string{
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, time_created INTEGER, data TEXT)`,
		`CREATE TABLE session (id TEXT PRIMARY KEY, parent_id TEXT, directory TEXT, time_created INTEGER, time_compacting INTEGER)`,
	} {
		mustExec(t, db, s)
	}
	return db
}

// agyConversation creates agy's conversation DB with the columns its probe reads.
func agyConversation(t *testing.T, conv string) *sql.DB {
	t.Helper()
	dir := filepath.Join(paths.GeminiHome(), "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, conv+".db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `CREATE TABLE steps (idx INTEGER, step_type INTEGER, status INTEGER, step_payload BLOB)`)
	return db
}
