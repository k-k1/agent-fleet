package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func toLines(ss ...string) [][]byte {
	out := make([][]byte, 0, len(ss))
	for _, s := range ss {
		out = append(out, []byte(s))
	}
	return out
}

// apiErr builds the synthetic assistant record claude writes when a turn dies on an
// API error (model "<synthetic>", isApiErrorMessage true, optional apiErrorStatus).
func apiErr(text string, status int) string {
	rec := map[string]any{
		"type": "assistant", "isApiErrorMessage": true,
		"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}},
	}
	if status != 0 {
		rec["apiErrorStatus"] = status
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// apiErrKind is apiErr plus claude's own machine-readable cause (`error`), the field
// that survives an English-wording change (docs/47 §4-6 / B).
func apiErrKind(text string, status int, kind string) string {
	var rec map[string]any
	_ = json.Unmarshal([]byte(apiErr(text, status)), &rec)
	rec["error"] = kind
	b, _ := json.Marshal(rec)
	return string(b)
}

func asstLine(text string) string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}`
}

func userLine(text string) string {
	return `{"type":"user","message":{"content":"` + text + `"}}`
}

// TestAbortedTurnClassification pins the four error classes actually observed in the
// fleet's transcripts (docs/47 §2). The retryable/blocked split is the safety valve for
// 自動再開: re-sending a blocked turn reproduces the same error forever.
func TestAbortedTurnClassification(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		status    int
		retryable bool
	}{
		{"connection closed", "API Error: Connection closed mid-response. The response above may be incomplete.", 0, true},
		{"rate limited", "API Error: Server is temporarily limiting requests (not your usage limit) · Rate limited", 429, true},
		{"overloaded 5xx", "API Error: 529 Overloaded", 529, true},
		// 実測 sp2qemx (2026-07-30): apiErrorStatus フィールドごと無いので、文言でしか
		// 一過性と判定できない。ここが blocked に倒れると自動再開の対象から外れる。
		{"server error mid-response", "API Error: Server error mid-response. The response above may be incomplete.", 0, true},
		{"internal server error", "API Error: 500 Internal server error", 0, true},
		{"usage limit", "You've reached your Fable 5 limit. Run /usage-credits to continue or switch models with /model", 429, false},
		// 実測 s5jjqv4 (2026-07-31, claude 2.1.220)。"hit your" なので "reached your" に
		// 当たらず、"session limit" なので "usage limit" にも当たらない — マーカーを足す
		// までは「判定不能なので blocked」に落ちて偶然だけ正解していた。結論が同じでも
		// 意図した分類にしておかないと、次に既定側を変えたときに黙って壊れる。
		{"session limit", "You've hit your session limit · resets 7:50pm (Asia/Tokyo)", 0, false},
		{"prompt too long", "Prompt is too long · the request is ~242785 tokens (limit 200000) but this conversation is longer", 400, false},
		// 実測 2026-08-05（別ワークスペースの g3-manage セッション）。claude 2.1.x の
		// ストリーム番犬が内部リトライを使い切った形で、コーパスには無かった新しい文言。
		// 自動再開の対象でなければ、15 分走ったターンがそのまま捨てられる。
		{"stream idle timeout", "API Error: Stream idle timeout - no chunks received", 0, true},
		{"stream idle timeout (partial)", "API Error: Stream idle timeout - partial response received", 0, true},
		{"unknown wording", "API Error: something nobody has seen before", 0, false}, // 判定不能は blocked 側
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, retryable, ok := abortedTurnFrom(toLines(userLine("go"), apiErr(tc.text, tc.status)))
			if !ok {
				t.Fatalf("ok=false, want an aborted turn")
			}
			if msg != tc.text {
				t.Errorf("msg = %q, want %q", msg, tc.text)
			}
			if retryable != tc.retryable {
				t.Errorf("retryable = %v, want %v", retryable, tc.retryable)
			}
		})
	}
}

// TestAbortedTurnErrorKind pins the `error` field as the FALLBACK classifier (docs/47
// §4-6): 英文言は版ごとに変わるが、この値は claude 自身の分類なので変わりにくい。
// 順序が要点 — 文言が主で、`error` はそれが何も言わなかったときだけ効く。文言の方が
// 「上限ではない」といった否定を表現できるからで、逆順にすると 429 の retryable と
// blocked が混ざる。
func TestAbortedTurnErrorKind(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		status    int
		kind      string
		retryable bool
	}{
		// 文言に手掛かりが無い形。ここが `error` の出番。
		{"未知の文言 + server_error", "API Error: something nobody has seen before", 0, "server_error", true},
		{"未知の文言 + invalid_request", "API Error: something nobody has seen before", 0, "invalid_request", false},
		// rate_limit は 429 の両義（利用上限 / 一時的なレート制限）なので何も決めない
		// → 既定の blocked に倒れる。ここを retryable にすると上限を再送し続ける。
		{"未知の文言 + rate_limit", "API Error: something nobody has seen before", 429, "rate_limit", false},
		{"未知の値は決めない", "API Error: something nobody has seen before", 0, "brand_new_kind", false},
		// 文言が主: server_error を名乗っていても利用上限の文言なら blocked のまま。
		{"上限の文言は error より強い", "You've reached your Fable 5 limit.", 429, "server_error", false},
		{"一過性の文言は error より強い", "API Error: Connection closed mid-response.", 0, "invalid_request", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, retryable, ok := abortedTurnFrom(toLines(userLine("go"), apiErrKind(tc.text, tc.status, tc.kind)))
			if !ok {
				t.Fatalf("ok=false, want an aborted turn")
			}
			if retryable != tc.retryable {
				t.Errorf("retryable = %v, want %v", retryable, tc.retryable)
			}
		})
	}
}

// TestAbortedTurnTailShape covers WHICH tail counts as "the turn died here": bookkeeping
// records written after the error must not hide it, and anything the user/session did
// afterwards must clear it (or a resumed session would be reported as aborted forever).
func TestAbortedTurnTailShape(t *testing.T) {
	err429 := apiErr("API Error: Connection closed mid-response.", 0)
	cases := []struct {
		name  string
		lines [][]byte
		want  bool
	}{
		{"error is last", toLines(userLine("go"), err429), true},
		// 実測の並び: エラーの直後に turn_duration と file-history-snapshot が続く
		{"bookkeeping after error", toLines(userLine("go"), err429,
			`{"type":"system","subtype":"turn_duration","durationMs":257395}`,
			`{"type":"file-history-snapshot"}`,
			`{"type":"last-prompt"}`, `{"type":"custom-title"}`, `{"type":"mode"}`,
			`{"type":"permission-mode"}`, `{"type":"agent-name"}`), true},
		{"resumed by user", toLines(userLine("go"), err429, userLine("続けてください"), asstLine("はい")), false},
		{"normal completion", toLines(userLine("go"), asstLine("done")), false},
		{"empty transcript", nil, false},
		// サブエージェントのエラーは本体ターンの終端ではない
		{"sidechain error ignored", toLines(userLine("go"), asstLine("spawning"),
			`{"type":"assistant","isSidechain":true,"isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"API Error: Connection closed"}]}}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := abortedTurnFrom(tc.lines); ok != tc.want {
				t.Errorf("ok = %v, want %v", ok, tc.want)
			}
		})
	}
}

// TestAbortedTurnLiveCorpus is a drift check against the transcripts this workspace has
// actually accumulated: every isApiErrorMessage record in the corpus must classify, and
// a transcript whose tail is an api error must be detected. Skips where there is no
// corpus (CI, a fresh container) — it guards against claude changing the record shape,
// which is the contract this feature rests on.
func TestAbortedTurnLiveCorpus(t *testing.T) {
	root := filepath.Join(os.Getenv("HOME"), "..", "..", "var", "lib", "af", "claude", "projects")
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		root = filepath.Join(v, "projects")
	}
	if testing.Short() {
		t.Skip("reads the whole local transcript corpus")
	}
	paths, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if len(paths) == 0 {
		t.Skip("no local claude transcript corpus")
	}
	// 端末の転写は数百ファイル・数 MB になる。ドリフト検知が目的なので、新しい方から
	// 一定数だけ見れば十分（古い版の形は既にここまでの版で検証済み）。
	sort.Slice(paths, func(i, j int) bool {
		fi, _ := os.Stat(paths[i])
		fj, _ := os.Stat(paths[j])
		return fi != nil && fj != nil && fi.ModTime().After(fj.ModTime())
	})
	if len(paths) > 150 {
		t.Logf("corpus capped: %d transcripts available, checking the 150 newest", len(paths))
		paths = paths[:150]
	}
	seen := 0
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var lines [][]byte
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(ln) != "" {
				lines = append(lines, []byte(ln))
			}
		}
		for _, ln := range lines {
			var r abortRecord
			if json.Unmarshal(ln, &r) == nil && r.IsAPIError && !r.IsSidechain {
				seen++
				if txt := AssistantText(ln); strings.TrimSpace(txt) == "" {
					t.Errorf("%s: api-error record carries no text — record shape drifted: %.200s", filepath.Base(p), ln)
				}
			}
		}
		// 末尾がエラーなら検知されること（逆に、そうでなければ検知されないこと）
		msg, _, ok := abortedTurnFrom(lines)
		if ok && msg == "" {
			t.Errorf("%s: detected an abort with an empty message", filepath.Base(p))
		}
	}
	t.Logf("corpus: %d transcripts, %d api-error records", len(paths), seen)
}

// TestHealIdleRoutesAbortToNotifier is the seam test: the pane heal used to call
// status.Remove and swallow the turn end, which is the bug docs/47 fixes. Here the
// whole path runs — planted transcript → HealIdle → agents notifier — and asserts both
// that a terminal event is emitted with the right label and that status lands on idle
// (NOT removed; a removed marker lets the heal fire again and re-report).
func TestHealIdleRoutesAbortToNotifier(t *testing.T) {
	type call struct{ previous, state, excerpt string }

	setup := func(t *testing.T, tail string) (string, *[]call) {
		t.Helper()
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
		sid := "11111111-2222-5333-8444-555555555555"
		dir := filepath.Join(home, ".claude", "projects", "-proj")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := userLine("go") + "\n" + tail + "\n"
		if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		var calls []call
		agents.SetStateNotifier(func(_, previous, state, excerpt string) {
			calls = append(calls, call{previous, state, excerpt})
		})
		t.Cleanup(func() { agents.SetStateNotifier(nil) })
		status.Persist(sid, "working")
		return sid, &calls
	}

	// notify() fires the notifier on its own goroutine; wait for it rather than sleeping.
	waitCalls := func(t *testing.T, calls *[]call) []call {
		t.Helper()
		for i := 0; i < 200; i++ {
			if len(*calls) > 0 {
				return *calls
			}
			time.Sleep(5 * time.Millisecond)
		}
		return nil
	}

	t.Run("retryable abort → StateAborted", func(t *testing.T) {
		sid, calls := setup(t, apiErr("API Error: Connection closed mid-response.", 0))
		HealIdle(sid)
		got := waitCalls(t, calls)
		if len(got) != 1 {
			t.Fatalf("notifier calls = %d, want 1", len(got))
		}
		if got[0].state != agents.StateAborted || got[0].previous != "working" {
			t.Errorf("call = %+v, want previous=working state=%s", got[0], agents.StateAborted)
		}
		if !strings.Contains(got[0].excerpt, "Connection closed") {
			t.Errorf("excerpt lost the error text: %q", got[0].excerpt)
		}
		if st, _ := status.Read(sid); st.State != "idle" {
			t.Errorf("status = %q, want idle (a removed marker would let the heal re-report)", st.State)
		}
	})

	t.Run("blocked abort → StateFailed", func(t *testing.T) {
		sid, calls := setup(t, apiErr("You've reached your Fable 5 limit. Run /usage-credits to continue", 429))
		HealIdle(sid)
		got := waitCalls(t, calls)
		if len(got) != 1 || got[0].state != agents.StateFailed {
			t.Fatalf("calls = %+v, want one %s", got, agents.StateFailed)
		}
	})

	t.Run("ordinary heal stays silent", func(t *testing.T) {
		sid, calls := setup(t, asstLine("done"))
		HealIdle(sid)
		time.Sleep(50 * time.Millisecond)
		if len(*calls) != 0 {
			t.Fatalf("silent heal emitted %+v", *calls)
		}
		if st, ok := status.Read(sid); ok && st.State != "" {
			t.Errorf("status = %q, want the marker removed as before", st.State)
		}
	})
}

// TestUsageLimitAbortIsTheLimitSubset: 上限エピソードの入口は blockedMarkers 全体では
// なく「待てば解ける上限」だけ、という切り分けを固定する。プロンプト超過や認証エラーで
// 「利用上限に達しました」と通知したら、利用者は来ないリセットを待つことになる。
// retryable 側（"(not your usage limit)" と自称する 429）も落ちることが要点。
func TestUsageLimitAbortIsTheLimitSubset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	dir := filepath.Join(home, ".claude", "projects", "-proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		tail string
		want bool
	}{
		{"モデル別上限", apiErr("You've reached your Fable 5 limit. Run /usage-credits to continue or switch models with /model.", 429), true},
		{"アカウントの窓", apiErr("You've hit your session limit · resets 7:50pm (Asia/Tokyo)", 0), true},
		{"一時的なレート制限は上限ではない", apiErr("API Error: Server is temporarily limiting requests (not your usage limit) · Rate limited", 429), false},
		{"プロンプト超過は待っても解けない", apiErr("Prompt is too long · the request is ~242785 tokens (limit 200000)", 400), false},
		{"認証エラーは待っても解けない", apiErr("API Error (HTTP 401): authentication failed", 401), false},
		{"接続断", apiErr("API Error: Connection closed mid-response.", 0), false},
		{"通常の完了", asstLine("done"), false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sid := fmt.Sprintf("11111111-2222-5333-8444-%012d", i)
			body := userLine("go") + "\n" + tc.tail + "\n"
			if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			a, ok := UsageLimitAbort(sid)
			if ok != tc.want {
				t.Fatalf("UsageLimitAbort = %v, want %v", ok, tc.want)
			}
			if ok && a.Msg == "" {
				t.Error("上限と判定したのに理由の文言が空")
			}
		})
	}
}
