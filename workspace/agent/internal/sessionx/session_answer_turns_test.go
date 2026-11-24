package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A run whose last lines are one very large edit before its answer is read through the slim
// route: the reply stays small, the answer's head and the tool part's kind survive, and the edit
// body does not travel (issue #4891 item 7).
func TestAnswerTurnsDropsEditBodies(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	dir := t.TempDir()
	const name = "answer_turns_slim"
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude})
	sid := session.UUID(dir, name)
	jsonl := filepath.Join(home, ".claude", "projects", "p", sid+".jsonl")
	huge := strings.Repeat("x", 3<<20)
	lines := []string{
		`{"type":"user","uuid":"u1","parentUuid":null,"timestamp":"2026-11-05T10:00:00Z","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","uuid":"r1","parentUuid":"u1","timestamp":"2026-11-05T10:00:01Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"/a.txt","content":"` + huge + `"}}]}}`,
		`{"type":"assistant","uuid":"r2","parentUuid":"r1","timestamp":"2026-11-05T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"Done: shipped\n` + strings.Repeat("y", 10<<10) + `"}]}}`,
	}
	writeFile(t, jsonl, strings.Join(lines, "\n")+"\n")

	req := httptest.NewRequest(http.MethodGet, "/sessions/"+name+"/answer-turns?limit=50", nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleSessionAnswerTurns(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %.200s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() > 64<<10 {
		t.Fatalf("reply is %d bytes: the edit body travelled", rec.Body.Len())
	}
	var reply answerTurnsReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Name != name || reply.HasMore || reply.FirstLine != 0 {
		t.Fatalf("reply = %+v", reply)
	}
	var sawTool, sawAnswer bool
	for _, m := range reply.Messages {
		for _, p := range m.Parts {
			if p.Kind == "tool" {
				sawTool = true
				if p.Text != "" {
					t.Errorf("tool part kept a body: %.40q", p.Text)
				}
			}
			if p.Kind == "text" && strings.HasPrefix(p.Text, "Done: shipped") {
				sawAnswer = true
				if len(p.Text) > answerTextMax {
					t.Errorf("answer text is %d bytes, want at most %d", len(p.Text), answerTextMax)
				}
			}
		}
	}
	if !sawTool || !sawAnswer {
		t.Fatalf("tool=%v answer=%v in %.300s", sawTool, sawAnswer, rec.Body.String())
	}
}

func TestHeadOfCutsOnARuneBoundary(t *testing.T) {
	s := strings.Repeat("あ", answerTextMax) // 3 bytes each
	got := headOf(s)
	if len(got) > answerTextMax || len(got)%3 != 0 || !strings.HasPrefix(s, got) {
		t.Fatalf("len = %d", len(got))
	}
	if headOf("short") != "short" {
		t.Fatal("a short text must pass through")
	}
}
