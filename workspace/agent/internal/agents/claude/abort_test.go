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
// that survives an English-wording change (docs/log/47 §4-6 / B).
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
// fleet's transcripts (docs/log/47 §2). The retryable/blocked split is the safety valve for
// auto-resume: re-sending a blocked turn reproduces the same error forever.
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
		// Measured sp2qemx (2026-07-30): the apiErrorStatus field is missing entirely, so
		// only the text can call it transient. Falling to blocked drops it from auto-resume.
		{"server error mid-response", "API Error: Server error mid-response. The response above may be incomplete.", 0, true},
		{"internal server error", "API Error: 500 Internal server error", 0, true},
		{"usage limit", "You've reached your Fable 5 limit. Run /usage-credits to continue or switch models with /model", 429, false},
		// Measured s5jjqv4 (2026-07-31, claude 2.1.220). It says "hit your", so it misses
		// "reached your", and "session limit", so it misses "usage limit" too — until the
		// marker was added it fell to "undecidable, therefore blocked" and was right only by
		// accident. The verdict is the same either way, but unless the classification is the
		// intended one, changing the default side later breaks it silently.
		{"session limit", "You've hit your session limit · resets 7:50pm (Asia/Tokyo)", 0, false},
		{"prompt too long", "Prompt is too long · the request is ~242785 tokens (limit 200000) but this conversation is longer", 400, false},
		// Measured 2026-08-05 (a g3-manage session in another workspace): claude 2.1.x's
		// stream watchdog with its internal retries used up — wording the corpus did not
		// have. Without auto-resume, a turn that ran for 15 minutes is thrown away.
		{"stream idle timeout", "API Error: Stream idle timeout - no chunks received", 0, true},
		{"stream idle timeout (partial)", "API Error: Stream idle timeout - partial response received", 0, true},
		// Measured 2026-08-06 (an expired credential). The text carries "Re-authenticate", so
		// it missed the old "authentication" marker and the 401 fell to the default, landing
		// on blocked by accident. Re-sending fails the same way until the user logs in again,
		// so pin it as the intended classification.
		{"auth expired", "Please run /login · API Error: 401 OAuth access token has expired. Re-authenticate to continue.", 401, false},
		{"unknown wording", "API Error: something nobody has seen before", 0, false}, // undecidable = blocked
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

// TestAbortedTurnErrorKind pins the `error` field as the FALLBACK classifier (docs/log/47
// §4-6): the English prose is reworded from release to release, while this value is claude's
// own classification and rarely moves. The ORDER is the point — the text leads and `error`
// only applies when the text said nothing, because only the text can express a negation such
// as "not a usage limit". Reversed, the retryable and blocked 429s get mixed up.
func TestAbortedTurnErrorKind(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		status    int
		kind      string
		retryable bool
	}{
		// Shapes where the text offers no clue at all — this is where `error` earns its keep.
		{"unknown wording + server_error", "API Error: something nobody has seen before", 0, "server_error", true},
		{"unknown wording + invalid_request", "API Error: something nobody has seen before", 0, "invalid_request", false},
		// rate_limit is the ambiguous side of 429 (usage limit / temporary rate limit), so it
		// decides nothing and falls to the blocked default. Retryable here would keep
		// re-sending into a usage limit.
		{"unknown wording + rate_limit", "API Error: something nobody has seen before", 429, "rate_limit", false},
		{"an unknown value decides nothing", "API Error: something nobody has seen before", 0, "brand_new_kind", false},
		{"unknown wording + authentication_failed", "API Error: something nobody has seen before", 0, "authentication_failed", false},
		// The text leads: usage-limit wording stays blocked even when it calls itself a server_error.
		{"limit wording outranks error", "You've reached your Fable 5 limit.", 429, "server_error", false},
		{"transient wording outranks error", "API Error: Connection closed mid-response.", 0, "invalid_request", true},
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
		// The measured ordering: turn_duration and file-history-snapshot follow the error
		{"bookkeeping after error", toLines(userLine("go"), err429,
			`{"type":"system","subtype":"turn_duration","durationMs":257395}`,
			`{"type":"file-history-snapshot"}`,
			`{"type":"last-prompt"}`, `{"type":"custom-title"}`, `{"type":"mode"}`,
			`{"type":"permission-mode"}`, `{"type":"agent-name"}`), true},
		{"resumed by user", toLines(userLine("go"), err429, userLine("続けてください"), asstLine("はい")), false},
		{"normal completion", toLines(userLine("go"), asstLine("done")), false},
		{"empty transcript", nil, false},
		// A subagent's error is not the end of the main turn
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
	// A machine's transcripts run to hundreds of files and several MB. The point is drift
	// detection, so a fixed number of the newest ones is enough (older releases' shapes were
	// verified by the runs of the day).
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
		// An error at the tail must be detected — and anything else must not be.
		msg, _, ok := abortedTurnFrom(lines)
		if ok && msg == "" {
			t.Errorf("%s: detected an abort with an empty message", filepath.Base(p))
		}
	}
	t.Logf("corpus: %d transcripts, %d api-error records", len(paths), seen)
}

// TestHealIdleRoutesAbortToNotifier is the seam test: the pane heal used to call
// status.Remove and swallow the turn end, which is the bug docs/log/47 fixes. Here the
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

// TestUsageLimitAbortIsTheLimitSubset pins two things: a limit episode is entered from the
// USAGE-LIMIT subset only, not from blockedMarkers as a whole, and which KIND of limit it is
// (a window that waiting clears, or a spend/balance cap that it never does). Notifying "usage
// limit reached" for an over-long prompt or an authentication error leaves the user waiting
// for a reset that never comes; reading a spend cap as a window shows "waiting for the limit
// to lift" forever (docs/log/47 §4-10). Dropping the retryable side — the 429 that calls
// itself "(not your usage limit)" — is part of the point.
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
		kind LimitKind
	}{
		{"per-model limit", apiErr("You've reached your Fable 5 limit. Run /usage-credits to continue or switch models with /model.", 429), true, LimitWindow},
		{"account window", apiErr("You've hit your session limit · resets 7:50pm (Asia/Tokyo)", 0), true, LimitWindow},
		// Measured corpus (2026-08-20): it matches none of the three existing words, so the
		// weekly one alone opened no episode.
		{"weekly window", apiErr("You've hit your weekly limit · resets 9am (Asia/Tokyo)", 429), true, LimitWindow},
		// User-reported (2026-08-20). The same 429 / rate_limit, but the side waiting never clears.
		{"org monthly spend limit", apiErr("You've hit your org's monthly spend limit · run /usage-credits to raise it, or visit claude.ai/admin-settings/usage", 429), true, LimitSpend},
		{"credit balance too low", apiErr("Your credit balance is too low to access the API", 429), true, LimitSpend},
		{"a temporary rate limit is not a usage limit", apiErr("API Error: Server is temporarily limiting requests (not your usage limit) · Rate limited", 429), false, ""},
		{"an over-long prompt does not clear by waiting", apiErr("Prompt is too long · the request is ~242785 tokens (limit 200000)", 400), false, ""},
		{"an authentication error does not clear by waiting", apiErr("API Error (HTTP 401): authentication failed", 401), false, ""},
		{"connection dropped", apiErr("API Error: Connection closed mid-response.", 0), false, ""},
		{"normal completion", asstLine("done"), false, ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sid := fmt.Sprintf("11111111-2222-5333-8444-%012d", i)
			body := userLine("go") + "\n" + tc.tail + "\n"
			if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			a, kind, ok := UsageLimitAbort(sid)
			if ok != tc.want {
				t.Fatalf("UsageLimitAbort = %v, want %v", ok, tc.want)
			}
			if kind != tc.kind {
				t.Errorf("kind = %q, want %q (a window and a spend cap call for opposite next moves)", kind, tc.kind)
			}
			if ok && a.Msg == "" {
				t.Error("classified as a usage limit, yet the reason text is empty")
			}
		})
	}
}
