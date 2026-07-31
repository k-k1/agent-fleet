package tmuxx

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// verdict is what the pane-reading pair (IsBusy / AtIdlePrompt) should conclude about a
// frame. They are not mutually exclusive by construction — busy and idle are independent
// predicates — so the corpus pins BOTH for every frame, which is what catches a change
// that makes a frame read as neither (or both).
type verdict struct {
	busy      bool
	idle      bool
	agents    bool // the pane's input box is bound to a background agent, not the session
	rateLimit bool // parked on the 利用上限メニュー (/rate-limit-options)
}

var (
	busyV    = verdict{busy: true, idle: false}  // a turn is in flight
	idleV    = verdict{busy: false, idle: true}  // sitting at the ready input box
	modalV   = verdict{busy: false, idle: false} // a dialog is up: neither working nor takeable as idle
	corpusWD = "testdata/footers"
)

// corpus pins the expected reading of every recorded pane in testdata/footers. See that
// directory's SOURCE.txt for provenance (claude 2.1.212) and how to re-capture.
//
// This locks the two regressions that shipped to the fleet on 2026-07-17:
//   - busy_thinking_no_tokens: the spinner carries NO token count while claude is still
//     thinking. The old spinnerRe required "tokens" and false-idled the whole phase.
//   - idle_manual_mode / idle_bypass_bg_shell: the footer's trailing hint is contextual —
//     absent in the default mode, displaced by "· 1 shell ·" when background work runs.
//     The old AtIdlePrompt keyed on that hint and never fired the stale→idle self-heal.
//
// NOTE ON WHAT THIS CANNOT DO: these are recordings, so they cannot detect a FUTURE drift
// — a 4th change in claude's TUI would leave this test green. It guards the code against
// regressing on formats we have already seen. Detecting new drift needs the real CLI
// driven live and this corpus re-captured (see SOURCE.txt).
var corpus = map[string]verdict{
	"busy_thinking_no_tokens.txt":    busyV,
	"busy_tokens_early.txt":          busyV,
	"busy_tokens_glyph_asterisk.txt": busyV,
	"idle_manual_mode.txt":           idleV,
	"idle_bypass_bg_shell.txt":       idleV,
	"idle_bypass_hint.txt":           idleV,
	"idle_plan_mode.txt":             idleV,
	"idle_post_turn_summary.txt":     idleV,
	"modal_plan_approval.txt":        modalV,
	"modal_folder_trust.txt":         modalV,
	// バックグラウンド・エージェント関連（2026-07-30 の誤配達事故を制御プローブで再現）。
	// main が選択されているか否かだけが違い、footer も入力欄も同じに見える点が事故の本体。
	// レール操作中はモード表示フッタごと差し替わり、agents ホームは入力欄の意味が
	// 「新しいセッションを作る」に変わる — どれも本体の会話には届かない。
	"agents_rail_main_selected.txt":    idleV,
	"agents_rail_agent_selected.txt":   {busy: true, agents: true},
	"agents_rail_navigating_main.txt":  {},
	"agents_rail_navigating_agent.txt": {busy: true, agents: true},
	"agents_home_screen.txt":           {agents: true},
	// 利用上限でターンが切れたあとの /rate-limit-options メニュー。idle でも busy でも
	// ないのは modal_* と同じだが、その「どちらでもない」が永久に続くのがこの状態の
	// 特徴 — 上限モーダルは自分では消えず、AtIdlePrompt が恒久的に false を返すので
	// 自己修復が効かず 進行中 に貼り付く（2026-07-31 実測・約16時間）。
	"modal_rate_limit.txt": {rateLimit: true},
}

// TestFooterCorpus replays every recorded pane through the real predicates.
func TestFooterCorpus(t *testing.T) {
	for _, name := range corpusFiles(t) {
		t.Run(name, func(t *testing.T) {
			want, ok := corpus[name]
			if !ok {
				t.Fatalf("%s is in testdata/footers but not in the corpus table — add it with its expected verdict (or delete the file)", name)
			}
			s := readFrame(t, name)
			if got := spinnerActive(s); got != want.busy {
				t.Errorf("IsBusy(%s) = %v, want %v\nspinner line: %s", name, got, want.busy, spinnerLine(s))
			}
			if got := atIdlePrompt(s); got != want.idle {
				t.Errorf("AtIdlePrompt(%s) = %v, want %v\nfooter line: %s", name, got, want.idle, footerLine(s))
			}
			// 誤検知は「送れるはずの注入を弾く」方向なので、レールの無いフレームが
			// false であることも含めて全フレームで固定する。
			if got := agentsViewActive(s); got != want.agents {
				t.Errorf("AgentsViewActive(%s) = %v, want %v", name, got, want.agents)
			}
			// 上限メニュー判定も全フレームで固定する。false 側が大事: 誤検知すると
			// 走っているターンを「上限で停止」と読んで HealIdle を呼びに行くので、
			// スピナー中のフレームやプラン承認ダイアログが引っかからないことを
			// コーパス全体で押さえておく。
			if got := atRateLimitModal(s); got != want.rateLimit {
				t.Errorf("AtRateLimitModal(%s) = %v, want %v", name, got, want.rateLimit)
			}
		})
	}
}

// TestFooterCorpusComplete fails when a frame is recorded but never pinned. Without it a
// dropped table entry would silently shrink the coverage this corpus exists to provide.
func TestFooterCorpusComplete(t *testing.T) {
	files := corpusFiles(t)
	if len(files) != len(corpus) {
		t.Errorf("testdata/footers has %d frames but the corpus table pins %d — they must agree\nframes: %v", len(files), len(corpus), files)
	}
}

// corpusFiles lists the recorded frames (SOURCE.txt is prose, not a frame).
func corpusFiles(t *testing.T) []string {
	t.Helper()
	es, err := os.ReadDir(corpusWD)
	if err != nil {
		t.Fatalf("read %s: %v", corpusWD, err)
	}
	var out []string
	for _, e := range es {
		if e.Name() == "SOURCE.txt" || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func readFrame(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(corpusWD, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// spinnerLine / footerLine surface the line a failure is about, so a drift shows the
// actual new wording in the test output instead of just a false/true.
func spinnerLine(s string) string { return findLine(s, spinnerRe.MatchString) }
func footerLine(s string) string  { return findLine(s, modeFooterRe.MatchString) }

func findLine(s string, match func(string) bool) string {
	for _, ln := range strings.Split(s, "\n") {
		if match(ln) {
			return strings.TrimSpace(ln)
		}
	}
	return "(none in frame)"
}

// TestRateLimitModalDismissed pins the reason atRateLimitModal needs TWO markers. The
// banner ("You've hit your session limit …") is transcript text and stays on screen after
// the menu is answered, and claude echoes the chosen option into the transcript too — so
// keying on either alone would report a menu that is long gone, and the session would be
// badged 上限で停止 forever instead of returning to 入力待ち. Only the confirm footer
// disappears with the menu.
func TestRateLimitModalDismissed(t *testing.T) {
	frame := readFrame(t, "modal_rate_limit.txt")
	if !atRateLimitModal(frame) {
		t.Fatal("the recorded menu frame must read as the rate-limit modal")
	}
	// The menu is answered: its footer goes away, the banner and the echoed option stay.
	dismissed := strings.Replace(frame, "  Enter to confirm · Esc to cancel", "⏵⏵ bypass permissions on", 1)
	if atRateLimitModal(dismissed) {
		t.Error("atRateLimitModal = true after the menu was dismissed (banner/option text lingering in the transcript)")
	}
	if !atIdlePrompt(dismissed) {
		t.Error("a dismissed menu must read as the ready prompt again, or the session stays stuck")
	}
}

// TestComposerEmpty pins the precondition LeaveAgentsView uses before it sends any key:
// a draft in the input box must block the automatic return to the main conversation.
// The bare prompt is "❯" followed by a NON-BREAKING space (U+00A0) — a real capture, and
// the reason the check trims Unicode space rather than ASCII blanks.
func TestComposerEmpty(t *testing.T) {
	for _, tc := range []struct {
		frame string
		want  bool
	}{
		{"agents_rail_agent_selected.txt", true},
		{"agents_rail_main_selected.txt", true},
		{"idle_bypass_hint.txt", true},
	} {
		if got := composerEmpty(readFrame(t, tc.frame)); got != tc.want {
			t.Errorf("composerEmpty(%s) = %v, want %v", tc.frame, got, tc.want)
		}
	}
	// A draft makes it false — otherwise the recovery keys could submit a human's
	// half-written message.
	drafted := strings.Replace(readFrame(t, "agents_rail_agent_selected.txt"),
		"❯ ", "❯ DRAFT-NOT-SUBMITTED", 1)
	if composerEmpty(drafted) {
		t.Error("composerEmpty = true with a draft in the composer")
	}
}
