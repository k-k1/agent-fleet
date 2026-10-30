package kiro

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures are lines recorded on kiro-cli 2.31.0 (docs/log/132 §1.3), ids shortened.
// The 2.32.0 pin showed the same behaviour (docs/log/132 §4).
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func appendTo(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderPendingThenResolved(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.jsonl")
	if got := recorderPending(p); len(got) != 0 {
		t.Fatalf("missing file must report nothing: %v", got)
	}
	appendTo(t, p, fixture(t, "acp-recorder-2.31.0.jsonl"))
	got := recorderPending(p)
	if len(got) != 1 || got[0] != "Running: touch probe.txt" {
		t.Fatalf("pending = %v", got)
	}
	// Idempotent poll: the offset advanced, nothing is double counted.
	if got := recorderPending(p); len(got) != 1 {
		t.Fatalf("second poll = %v", got)
	}
	appendTo(t, p, fixture(t, "acp-recorder-2.31.0-answered.jsonl"))
	if got := recorderPending(p); len(got) != 0 {
		t.Fatalf("answered request still pending: %v", got)
	}
}

func TestRecorderResolutions(t *testing.T) {
	req := strings.Split(fixture(t, "acp-recorder-2.31.0.jsonl"), "\n")[3] + "\n"
	cases := map[string]string{
		"cancelled response": `{"dir":"out","msg":{"jsonrpc":"2.0","id":"217c1a80-aaaa","result":{"outcome":{"outcome":"cancelled"}}}}` + "\n",
		"allow_always":       `{"dir":"out","msg":{"jsonrpc":"2.0","id":"217c1a80-aaaa","result":{"outcome":{"outcome":"selected","optionId":"allow_always","_meta":{"trustOption":{"label":"Full command"}}}}}}` + "\n",
		"prompt stopReason":  `{"dir":"in","msg":{"jsonrpc":"2.0","result":{"stopReason":"cancelled"},"id":6}}` + "\n",
		"session/cancel":     `{"dir":"out","msg":{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"x"}}}` + "\n",
	}
	for name, resolve := range cases {
		p := filepath.Join(t.TempDir(), "r.jsonl")
		appendTo(t, p, req)
		if len(recorderPending(p)) != 1 {
			t.Fatalf("%s: request not pending", name)
		}
		appendTo(t, p, resolve)
		if got := recorderPending(p); len(got) != 0 {
			t.Errorf("%s: still pending %v", name, got)
		}
	}
}

// A response for another id, and a partial line, must not resolve or break anything.
func TestRecorderOtherIDAndPartialLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.jsonl")
	req := strings.Split(fixture(t, "acp-recorder-2.31.0.jsonl"), "\n")[3] + "\n"
	appendTo(t, p, req)
	appendTo(t, p, `{"dir":"out","msg":{"jsonrpc":"2.0","id":"other","result":{}}}`+"\n")
	if len(recorderPending(p)) != 1 {
		t.Fatal("a response for another id resolved the request")
	}
	// Written in two halves: the first half must wait for its newline.
	resp := `{"dir":"out","msg":{"jsonrpc":"2.0","id":"217c1a80-aaaa","result":{"outcome":{"outcome":"cancelled"}}}}` + "\n"
	appendTo(t, p, resp[:20])
	if len(recorderPending(p)) != 1 {
		t.Fatal("partial line resolved the request")
	}
	appendTo(t, p, resp[20:])
	if len(recorderPending(p)) != 0 {
		t.Fatal("completed line did not resolve")
	}
}

// Unrecognised shapes are ignored so the pane scrape decides (fail closed).
func TestRecorderUnrecognisedIgnored(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.jsonl")
	appendTo(t, p, "not json\n")
	appendTo(t, p, `{"dir":"in","msg":{"method":"session/request_permission","id":7,"params":{"options":[{"optionId":"a"}]}}}`+"\n") // integer id
	appendTo(t, p, `{"dir":"in","msg":{"method":"session/request_permission","id":"x","params":{}}}`+"\n")                           // no options
	if got := recorderPending(p); len(got) != 0 {
		t.Fatalf("unrecognised lines produced %v", got)
	}
}

// A relaunch removes and recreates the file: the earlier run's pending id must not survive.
func TestRecorderFileReplaced(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.jsonl")
	appendTo(t, p, fixture(t, "acp-recorder-2.31.0.jsonl"))
	if len(recorderPending(p)) != 1 {
		t.Fatal("setup")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if got := recorderPending(p); len(got) != 0 {
		t.Fatalf("removed file still pending: %v", got)
	}
	appendTo(t, p, `{"dir":"out","msg":{"method":"initialize","id":0}}`+"\n")
	if got := recorderPending(p); len(got) != 0 {
		t.Fatalf("new file inherited state: %v", got)
	}
}

func TestStateFromRecorder(t *testing.T) {
	open := []string{"Running: touch probe.txt"}
	working := "output\n  Kiro is working · Type to steer\n"
	idle := "  ask a question or describe a task ↵\n"
	approval := "  shell requires approval\n"
	if got := stateFrom(working, open); got != "question" {
		t.Errorf("recorder pending over a non-idle pane = %q", got)
	}
	if got := stateFrom("", open); got != "" {
		t.Errorf("dead pane must not show a pending card: %q", got)
	}
	if got := stateFrom(idle, open); got != "idle" {
		t.Errorf("stale request over an idle composer = %q", got)
	}
	if got := stateFrom(working, nil); got != "working" {
		t.Errorf("no recorder line must leave the scrape: %q", got)
	}
	if got := stateFrom(approval, nil); got != "question" {
		t.Errorf("scrape fallback lost: %q", got)
	}
}

func TestDetailFromRecorder(t *testing.T) {
	approval := "  shell requires approval\n"
	if got := detailFrom(approval, []string{"Running: touch probe.txt"}); got != "Running: touch probe.txt" {
		t.Errorf("detail = %q", got)
	}
	if got := detailFrom(approval, nil); got != "shell requires approval" {
		t.Errorf("fallback detail = %q", got)
	}
	if got := detailFrom("", []string{"x"}); got != "" {
		t.Errorf("dead pane detail = %q", got)
	}
}

func TestBuildProgramRecorderEnv(t *testing.T) {
	got := buildProgram("", "", "", "", true, "/state/kiro-acp/s.jsonl")
	if !strings.Contains(got, "KIRO_ACP_RECORD_PATH='/state/kiro-acp/s.jsonl' kiro-cli ") &&
		!strings.Contains(got, "KIRO_ACP_RECORD_PATH=/state/kiro-acp/s.jsonl kiro-cli ") {
		t.Errorf("recorder env missing from the launch: %q", got)
	}
	if strings.Contains(buildProgram("", "", "", "", true, ""), "KIRO_ACP_RECORD_PATH") {
		t.Error("empty path must not set the env")
	}
}

const reqLine = `{"ts":4,"dir":"in","msg":{"jsonrpc":"2.0","method":"session/request_permission","params":{"toolCall":{"title":"Running: touch probe.txt"},"options":[{"optionId":"allow_once"}]},"id":"217c1a80-aaaa"}}` + "\n"
const respLine = `{"dir":"out","msg":{"jsonrpc":"2.0","id":"217c1a80-aaaa","result":{"outcome":{"outcome":"cancelled"}}}}` + "\n"

func bigLine(n int) string {
	return `{"dir":"in","msg":{"method":"session/update","x":"` + strings.Repeat("a", n) + `"}}` + "\n"
}

// A single line far larger than one read chunk (and than maxRecorderLine) sits between the
// request and its response: the response behind it must still be reached.
func TestRecorderHugeLineBetweenRequestAndResponse(t *testing.T) {
	for _, n := range []int{5 << 20, maxRecorderLine + 1<<20} {
		p := filepath.Join(t.TempDir(), "r.jsonl")
		appendTo(t, p, reqLine+bigLine(n)+respLine)
		if got := recorderPending(p); len(got) != 0 {
			t.Errorf("line of %d bytes: answered request still pending %v", n, got)
		}
	}
}

// A backlog spanning several chunks is read to the end before anything is published.
func TestRecorderBacklogCaughtUpBeforePublishing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.jsonl")
	appendTo(t, p, reqLine+strings.Repeat(bigLine(100<<10), 30)+respLine)
	if got := recorderPending(p); len(got) != 0 {
		t.Errorf("history pending published: %v", got)
	}
}

// A response line that straddles a chunk boundary is assembled, not lost.
func TestRecorderResponseAcrossChunks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.jsonl")
	// Filler sized so the response line starts 20 bytes before the chunk boundary.
	filler := bigLine(recorderReadChunk - len(reqLine) - 20 - len(bigLine(0)))
	appendTo(t, p, reqLine+filler+respLine)
	if got := recorderPending(p); len(got) != 0 {
		t.Errorf("response across the boundary missed: %v", got)
	}
}

func TestRecorderUnreadableFallsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	p := filepath.Join(t.TempDir(), "r.jsonl")
	appendTo(t, p, reqLine)
	if len(recorderPending(p)) != 1 {
		t.Fatal("setup")
	}
	appendTo(t, p, respLine)
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	if got := recorderPending(p); len(got) != 0 {
		t.Errorf("unreadable file returned %v", got)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := recorderPending(p); len(got) != 0 {
		t.Errorf("after recovery the answered request is pending: %v", got)
	}
}

func TestForgetRecorder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := prepareRecorder("slotx")
	appendTo(t, p, reqLine)
	if len(recorderPending(p)) != 1 {
		t.Fatal("setup")
	}
	forgetRecorder("slotx")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("file survived: %v", err)
	}
	recordersMu.Lock()
	_, ok := recorders[p]
	recordersMu.Unlock()
	if ok {
		t.Error("cache entry survived")
	}
}
