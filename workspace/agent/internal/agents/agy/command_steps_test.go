package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func TestCompletedCommandSteps(t *testing.T) {
	const (
		conv    = "00010203-0405-4607-8809-0a0b0c0d0e0f"
		foreign = "10111213-1415-4617-9819-1a1b1c1d1e1f"
		launch  = "sleep 20; printf 'background\\n'"
		short   = "printf 'hello\\n'"
	)
	m, sid := hookFixture(t, "test-commands")
	launchGens.Write(sid, "current-launch")
	now := time.Now()
	hook := func(event, gen, raw string) {
		t.Helper()
		var in hookInput
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			t.Fatal(err)
		}
		recordHook(m.Name, gen, event, in, now)
	}
	args := func(id, cmd, step string) string {
		wireCmd, _ := json.Marshal(cmd)
		return `{"conversationId":"` + id + `","stepIdx":` + step +
			`,"toolCall":{"name":"run_command","args":{"CommandLine":` + string(wireCmd) + `}}}`
	}
	hook("PostToolUse", "current-launch", args(conv, "unowned", "8"))
	hook("SessionStart", "current-launch", `{"conversationId":"`+conv+`"}`)
	hook("PreToolUse", "current-launch", args(conv, "proposed", "4"))
	hook("PreToolUse", "current-launch", args(conv, "pending only", "9"))
	hook("Stop", "current-launch", `{"conversationId":"`+conv+`","fullyIdle":true}`)
	before, ok := currentRecord(sid)
	if !ok || before.State != "idle" {
		t.Fatalf("test needs an idle turn, got %+v", before)
	}

	hook("PostToolUse", "current-launch", args(conv, short, "4"))
	hook("PostToolUse", "current-launch", args(conv, launch, "6"))
	hook("PostToolUse", "prior-launch", args(conv, "stale", "4"))
	hook("PostToolUse", "current-launch", args(foreign, "foreign", "4"))
	hook("PostToolUse", "current-launch", args(conv, "missing index", "null"))
	hook("PostToolUse", "current-launch", args(conv, "negative index", "-1"))
	hook("PostToolUse", "current-launch", `{"conversationId":"`+conv+`","stepIdx":9,"error":"execution failed","toolCall":{"name":"run_command","args":{"CommandLine":"failed attempt"}}}`)
	hook("PostToolUse", "current-launch", `{"conversationId":"`+conv+`","stepIdx":4,"toolCall":{"name":"manage_task","args":{"CommandLine":"not a command"}}}`)
	hook("PostToolUse", "current-launch", `{"conversationId":"`+conv+`","stepIdx":4,"toolCall":{"name":"run_command","args":{"CommandLine":2}}}`)
	if after, _ := currentRecord(sid); after.State != before.State || after.At != before.At {
		t.Fatalf("tool completion changed idle lifecycle state: %+v", after)
	}
	if st, _ := status.Read(sid); st.State == "working" {
		t.Fatal("tool completion reopened the turn")
	}
	if _, ok := commandSteps(conv).Read("8"); ok {
		t.Fatal("accepted a completion before identifying the slot's conversation")
	}
	if _, ok := commandSteps(foreign).Read("4"); ok {
		t.Fatal("stored a command for a foreign conversation")
	}
	if _, ok := commandSteps(conv).Read("-1"); ok {
		t.Fatal("stored a negative step index")
	}
	if _, ok := commandSteps(conv).Read("9"); ok {
		t.Fatal("stored a command from an errored tool hook")
	}
	if got, ok := commandSteps(conv).Read("4"); !ok || got != short {
		t.Fatalf("stored command = %q, want %q", got, short)
	}

	sids.Write(sid, conv)
	writeTranscript(t, conv, "transcript_full.jsonl", strings.Join([]string{
		`{"step_index":4,"source":"MODEL","type":"GENERIC","status":"DONE","content":"Created At: first\nCompleted At: last\nCommand exited with code 0.\n\nCommand output:\nhello"}`,
		`{"step_index":6,"source":"MODEL","type":"GENERIC","status":"RUNNING","content":"Tool is running as a background task with task id: ` + conv + `/task-6\nTask Description: sleep 20"}`,
		`{"step_index":8,"source":"MODEL","type":"GENERIC","status":"DONE","content":"Task: ` + conv + `/task-6\nStatus: RUNNING\nLog: file:///tmp/task-6.log"}`,
		`{"step_index":9,"source":"MODEL","type":"GENERIC","status":"DONE","content":"Command exited with code 0."}`,
		`{"step_index":10,"source":"MODEL","type":"GENERIC","status":"DONE","content":"Unrecognized result"}`,
		`{"step_index":11,"source":"MODEL","type":"GENERIC","status":"DONE","content":"The user changed the command to be run to: true\nThe command exited with code 0."}`,
		`{"source":"MODEL","type":"GENERIC","status":"DONE","content":"Command exited with code 2."}`,
	}, "\n"))
	hook("PostToolUse", "current-launch", args(conv, "failed or unknown", "10"))
	hook("PostToolUse", "current-launch", args(conv, "possibly stale proposal", "11"))
	hook("PostToolUse", "current-launch", args(conv, "guessed index", "12"))
	td, _ := agentImpl{}.Transcript(m)
	if len(td.Turns) != 1 || len(td.Turns[0].Parts) != 7 {
		t.Fatalf("unexpected turns: %+v", td.Turns)
	}
	parts := td.Turns[0].Parts
	for i, want := range []struct{ tool, info, command string }{
		{"RUN_COMMAND", short + " · exit code 0", short},
		{"RUN_COMMAND", launch + " · task-6 · started in background", launch},
		{"Check task status", "task-6 · running when checked", ""},
		{"RUN_COMMAND", "exit code 0", ""},
		{"GENERIC", "", ""},
		{"GENERIC", "", ""},
		{"RUN_COMMAND", "exit code 2", ""},
	} {
		p := parts[i]
		if p.Tool != want.tool || p.Info != want.info || p.Command != want.command {
			t.Errorf("part %d: %+v, want %+v", i, p, want)
		}
	}
	if got := parts[0].Output; got != "Command exited with code 0.\n\nCommand output:\nhello" {
		t.Errorf("command output changed: %q", got)
	}

	const invalid = "not-a-conversation-uuid"
	launchGens.Write(sid, "another-launch")
	sids.Write(sid, invalid)
	hook("PostToolUse", "another-launch", args(invalid, "invalid ID", "4"))
	if _, ok := commandSteps(invalid).Read("4"); ok {
		t.Fatal("stored a command for an invalid conversation ID")
	}
}

func stepFiles(t *testing.T, conv string) map[int]bool {
	t.Helper()
	ents, err := os.ReadDir(commandSteps(conv).Dir())
	if err != nil {
		return nil
	}
	got := map[int]bool{}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n, err := strconv.Atoi(e.Name()[:len(e.Name())-len(".txt")])
		if err != nil {
			t.Fatalf("unexpected file %q in the step store", e.Name())
		}
		got[n] = true
	}
	return got
}

func TestCommandStepsAreBoundedToTheNewestIndexes(t *testing.T) {
	const conv = "00010203-0405-4607-8809-0a0b0c0d0e0f"
	const other = "10111213-1415-4617-9819-1a1b1c1d1e1f"
	t.Setenv("HOME", t.TempDir())
	for _, id := range []string{conv, other} {
		for i := 0; i < 12; i++ {
			if err := commandSteps(id).Write(strconv.Itoa(i), "cmd"); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Numeric order, not lexical: 9 is older than 10.
	if err := commandSteps(conv).Write("10", "cmd"); err != nil {
		t.Fatal(err)
	}
	pruneCommandSteps(conv, 5)
	got := stepFiles(t, conv)
	if len(got) != 5 {
		t.Fatalf("kept %v, want 5 entries", got)
	}
	for _, i := range []int{11, 10, 9, 8, 7} {
		if !got[i] {
			t.Errorf("newest step %d was pruned: %v", i, got)
		}
	}
	if len(stepFiles(t, other)) != 12 {
		t.Error("pruning one conversation touched another")
	}
	pruneCommandSteps("no-such-conversation", 5) // a missing directory is not an error
}

func TestRecordCommandStepPrunesPastTheLimit(t *testing.T) {
	const conv = "00010203-0405-4607-8809-0a0b0c0d0e0f"
	t.Setenv("HOME", t.TempDir())
	for i := 0; i < maxCommandSteps+3; i++ {
		var in hookInput
		raw := `{"conversationId":"` + conv + `","stepIdx":` + strconv.Itoa(i) +
			`,"toolCall":{"name":"run_command","args":{"CommandLine":"ls"}}}`
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			t.Fatal(err)
		}
		recordCommandStep(in)
	}
	got := stepFiles(t, conv)
	if len(got) != maxCommandSteps {
		t.Fatalf("kept %d steps, want %d", len(got), maxCommandSteps)
	}
	if got[0] || got[2] || !got[3] || !got[maxCommandSteps+2] {
		t.Fatalf("pruned the wrong end: oldest kept should be 3, newest %d", maxCommandSteps+2)
	}
}

func TestClearResumeAndPurgeSlotRemoveTheConversationsCommandSteps(t *testing.T) {
	const conv = "00010203-0405-4607-8809-0a0b0c0d0e0f"
	const other = "10111213-1415-4617-9819-1a1b1c1d1e1f"
	for name, drop := range map[string]func(session.Meta, string){
		"ClearResume": func(_ session.Meta, sid string) { agentImpl{}.ClearResume(sid) },
		"PurgeSlot":   func(m session.Meta, _ string) { agentImpl{}.PurgeSlot(m) },
	} {
		t.Run(name, func(t *testing.T) {
			m, sid := hookFixture(t, "slot-purge")
			sids.Write(sid, conv)
			for _, id := range []string{conv, other} {
				if err := commandSteps(id).Write("4", "ls"); err != nil {
					t.Fatal(err)
				}
			}
			drop(m, sid)
			if _, err := os.Stat(commandSteps(conv).Dir()); err == nil {
				t.Fatal("the conversation's command steps survived")
			}
			if _, ok := commandSteps(other).Read("4"); !ok {
				t.Fatal("another conversation's command steps were removed")
			}
			drop(m, "never-launched-"+filepath.Base(sid)) // no conversation: nothing to do, no panic
		})
	}
}
