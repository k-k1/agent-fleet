package lcpp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
)

// longSession writes rounds of user → assistant (reasoning, prose, one edit call) → tool
// result → usage, about 7 KB per round: 1000 rounds is a ~7 MB log, the order a long coding
// session reaches (docs/log/99's 434-turn run, with fatter tool results).
func longSession(tb testing.TB, s *Store, rounds int) {
	tb.Helper()
	reasoning := strings.Repeat("think ", 170)
	prose := strings.Repeat("reply ", 85)
	args := fmt.Sprintf(`{"path":"a.go","old_string":%q,"new_string":%q}`, strings.Repeat("o", 500), strings.Repeat("n", 500))
	result := strings.Repeat("output line\n", 340)
	for i := 0; i < rounds; i++ {
		id := fmt.Sprintf("call-%d", i)
		must := func(_ Record, err error) {
			if err != nil {
				tb.Fatal(err)
			}
		}
		must(s.AppendUser(fmt.Sprintf("question %d", i)))
		must(s.AppendMessageFrom(harness.Message{
			Role: harness.RoleAssistant, Content: prose, Reasoning: reasoning,
			ToolCalls: []harness.ToolCall{{ID: id, Name: "edit", Arguments: args}},
		}, "qwen3"))
		must(s.AppendMessage(harness.Message{Role: harness.RoleTool, Content: result, ToolCallID: id}))
		must(s.AppendUsage(harness.Usage{PromptTokens: 1000 + i, CompletionTokens: 50}, 262144))
	}
}

// mirrorPoll is what one Console poll of an lcpp session costs the store: agentImpl.Transcript
// and agentImpl.WireLive each open the store afresh, the same as here.
func mirrorPoll(tb testing.TB, sid string) {
	if _, err := Open(sid).TranscriptFor("qwen3"); err != nil {
		tb.Fatal(err)
	}
	if _, _, ok := Open(sid).LastUsage(); !ok {
		tb.Fatal("LastUsage: no usage record")
	}
}

// BenchmarkMirrorPoll measures a poll on a long session: "unchanged" is a poll with nothing
// written since the last one, "appended" one after a single new record.
func BenchmarkMirrorPoll(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	const sid = "bench-sid"
	s := Open(sid)
	defer s.Close()
	longSession(b, s, 1000)
	mirrorPoll(b, sid) // the first read of a session is a full parse either way

	b.Run("unchanged", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			mirrorPoll(b, sid)
		}
	})
	b.Run("appended", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := s.AppendUsage(harness.Usage{PromptTokens: i}, 262144); err != nil {
				b.Fatal(err)
			}
			mirrorPoll(b, sid)
		}
	})
}
