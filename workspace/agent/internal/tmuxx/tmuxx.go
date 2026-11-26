// Package tmuxx holds the pure tmux probing primitives: existence checks, pane resolution,
// pane capture, the live-session list and pane-kind sniffing (docs/log/23 remaining item 1
// Wave A). It runs tmux commands and nothing else — orchestration (launching, metas,
// toolchains) stays in main. The dependency runs one way only, tmuxx → session.
package tmuxx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// launchUnset / launchSet patch the server's global environment ahead of every new-session
// (SetLaunchEnv).
var launchUnset, launchSet []string

// SetLaunchEnv makes every new-session first unset the named variables in the tmux server's
// global environment and set the given NAME=VALUE pairs there. A pane takes its environment
// from the server, not from the client that asked for it, so a server started before the
// Agent dropped a variable (an in-place Agent restart, an operator's own tmux) would keep
// handing it to new sessions and to every window a member opens in them. It rides the same
// tmux invocation as the new-session, so it also covers a server that invocation starts.
// Call once at boot, before any session is launched.
func SetLaunchEnv(unset, set []string) {
	launchUnset, launchSet = unset, set
}

// AddLaunchEnv adds to what SetLaunchEnv set, for a boot step that decides later. The same
// rule holds: before any session is launched.
func AddLaunchEnv(unset, set []string) {
	launchUnset = append(launchUnset, unset...)
	launchSet = append(launchSet, set...)
}

func withLaunchEnv(args []string) []string {
	if len(launchUnset) == 0 && len(launchSet) == 0 {
		return args
	}
	var out []string
	for _, k := range launchUnset {
		out = append(out, "set-environment", "-g", "-u", k, ";")
	}
	for _, kv := range launchSet {
		k, v, _ := strings.Cut(kv, "=")
		out = append(out, "set-environment", "-g", k, v, ";")
	}
	return append(out, args...)
}

// Cmd is the single funnel for every tmux invocation in the agent (enforced by
// tmux_guard_test.go — exec tmux only through here). It scopes
// the invocation to this instance's tmux server: AF_TMUX_SOCKET=<name> maps to
// `tmux -L <name>`, which beats both the default socket and an inherited $TMUX —
// so a second agent instance (dev / in-container E2E) launched with it can NEVER
// reach the shared production server, even when started from inside one of its
// panes. Unset (production) it is plain `tmux` on the default socket.
//
// Background: a test instance's shutdown once ran `tmux kill-server` against the
// shared default socket and took down every live session in the workspace
// (docs/log/32 M1 E2E incident, 2026-07-20). Socket scoping here plus the
// owned-sessions-only shutdown (shutdown.go) are the two halves of the fix.
func Cmd(args ...string) *exec.Cmd {
	if len(args) > 0 && args[0] == "new-session" {
		args = withLaunchEnv(args)
	}
	if s := os.Getenv("AF_TMUX_SOCKET"); s != "" {
		return exec.Command("tmux", append([]string{"-L", s}, args...)...)
	}
	return exec.Command("tmux", args...)
}

// OneShotTimeout bounds one request/response tmux call (OneShotCmd and the Output / Run /
// CombinedOutput wrappers). Such a call takes milliseconds when tmux answers, so 30 s is
// three orders of magnitude of slack: a slow, loaded host still finishes, while a tmux
// process that stopped answering ends in an error instead of holding its caller (and, for a
// when_ready delivery, every later input of the session) forever. A var so a test can
// shorten it.
var OneShotTimeout = 30 * time.Second

// killGrace is how long Wait lets a killed tmux client's pipes drain before giving up on them.
const killGrace = 2 * time.Second

// OneShotCmd is Cmd with a deadline of OneShotTimeout; the caller must call cancel. On expiry
// only the tmux client is killed: the tmux server is a separate process and is left as it
// was, but the request may already have reached it, so a timed-out call is "outcome unknown",
// not "not applied". Never use it for attach or control-mode streams, which are meant to
// outlive any deadline.
func OneShotCmd(args ...string) (*exec.Cmd, context.CancelFunc) {
	c, _, cancel := oneShot(args)
	return c, cancel
}

func oneShot(args []string) (*exec.Cmd, context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), OneShotTimeout)
	c := Cmd(args...)
	// Rebuild on the same binary and arguments so exec owns the deadline.
	cc := exec.CommandContext(ctx, c.Path, c.Args[1:]...)
	cc.Env, cc.WaitDelay = c.Env, killGrace
	return cc, ctx, cancel
}

// timedOut turns the "signal: killed" a deadline leaves behind into an error that satisfies
// errors.Is(err, context.DeadlineExceeded), so a caller can tell a hung tmux from a refusal.
func timedOut(ctx context.Context, args []string, err error) error {
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("tmux %s timed out after %v (outcome unknown): %w (%v)", args[0], OneShotTimeout, ctx.Err(), err)
	}
	return err
}

// Output runs a one-shot tmux call under OneShotTimeout and returns its stdout.
func Output(args ...string) ([]byte, error) {
	c, ctx, cancel := oneShot(args)
	defer cancel()
	out, err := c.Output()
	return out, timedOut(ctx, args, err)
}

// CombinedOutput is Output with stderr folded into the result.
func CombinedOutput(args ...string) ([]byte, error) {
	c, ctx, cancel := oneShot(args)
	defer cancel()
	out, err := c.CombinedOutput()
	return out, timedOut(ctx, args, err)
}

// CombinedOutputStdin is CombinedOutput with stdin supplied (load-buffer -).
func CombinedOutputStdin(stdin string, args ...string) ([]byte, error) {
	c, ctx, cancel := oneShot(args)
	defer cancel()
	c.Stdin = strings.NewReader(stdin)
	out, err := c.CombinedOutput()
	return out, timedOut(ctx, args, err)
}

// Run runs a one-shot tmux call under OneShotTimeout, discarding its output.
func Run(args ...string) error {
	c, ctx, cancel := oneShot(args)
	defer cancel()
	return timedOut(ctx, args, c.Run())
}

// spinnerRe matches claude's live working-spinner header — the "<glyph> <Gerund>…
// (<elapsed> · …)" line that ticks while a turn is in flight, e.g.
//
//	✢ Tempering… (6s · thinking with high effort)
//	✽ Perusing… (5m 42s · ↓ 17.8k tokens · thought for 3s)
//	✢ Adding regression tests… (13m 31s · ↓ 48.5k tokens)
//
// Only the gerund + "…" is truly dependable; this regex adds the parenthesised elapsed
// timer, which is dependable only while the pane is wide enough to hold it (see
// spinnerThinkingRe for the narrow-pane frame that has no timer). Everything else rotates:
//
//   - "esc to interrupt" is swapped out for a random Tip (hence the separate check in
//     spinnerActive, which still honours it on older builds);
//   - the "· ↓ <n> tokens" segment is NOT always there. It appears only once output
//     tokens have accrued, so a turn that is still thinking renders bare
//     "(6s · thinking with high effort)". Requiring "tokens" (as we did) therefore
//     false-idles the whole thinking phase — and a short turn start to finish;
//   - the timer is not even first inside the parens: while a hook runs, claude renders
//     "… (running stop hook · 6s · ↓ 279 tokens)" (found by the live contract probe,
//     tui_contract_test.go). Hence [^)\n]* before the timer.
//   - "shift+tab to cycle" stays visible mid-turn, so it can't tell busy from idle.
//
// The gerund is NOT a single word, and it is NOT even constrained in its characters: when
// a todo is in progress claude renders that item's activeForm as the spinner phrase, and
// an activeForm is arbitrary user-authored text. Real captures have shown it to be a
// multi-word phrase ("✢ Adding regression tests…"), Japanese ("· 検証ハーネスを作成中…",
// claude_sdfruv7), and parenthesised ("* nativeRuntime アダプタ実装 (AF_RUNTIME=native)…",
// claude_sx5m7yp). Each of those broke a narrower run in turn — [^\s(]* stopped at the
// first space, and [^(\n\x{2026}]* stopped at the "(" — so the run is now everything up to
// the ellipsis: [^\n\x{2026}]*. Assume nothing about the phrase's contents.
//
// The head is where the remaining discipline lives, and it is deliberately weak: one
// optional glyph, one optional space, then a letter or digit (\p{L}/\p{N}). An earlier
// head demanded [A-Z] or a non-ASCII char, which looked like it screened the phrase but in
// practice was satisfied by the *glyph* (✽ is non-ASCII) — so it went unexercised until a
// frame arrived with an ASCII glyph and a lower-case activeForm ("* nativeRuntime …"),
// which it then read idle. \p{L}\p{N} admits every activeForm we have seen while still
// doing the two jobs the head must do: a ≥2-space-indented transcript quote cannot match
// (^\S? ? absorbs at most one leading space, and the head is not a space), and a source
// line quoting a spinner inside a "//" comment cannot either (the head is not punctuation)
// — which matters because sessions asked to debug the TUI read their own pane.
//
// A slash command running as a todo renders ITS OWN NAME as the activeForm ("· /copyedit
// 02-noir A6… (20m 1s · almost done thinking with high effort)", real capture) — '/' is
// punctuation, so \p{L}\p{N} alone reads that idle for the whole command, same failure
// mode as the ASCII-glyph and CJK cases above. It can't simply join the \p{L}\p{N} class:
// a "//" comment's second slash sits in exactly the position a lone-glyph '/' would
// occupy once \S? backtracks to empty, so a bare-alternative class would match this file's
// own "// ..." examples. The second alternative below requires the glyph AND the space
// both present (\S, not \S?; a literal space, not " ?") before the '/' — a "//" comment
// has no space between its two slashes, so it can only ever feed the '/' to the empty-glyph
// path, which the mandatory-glyph alternative does not accept.
//
// It must not match the post-turn summary claude leaves in the transcript
// ("✻ Worked for 13m 53s", "✻ Sautéed for 5s · 1 shell still running"): those use a
// past-tense verb with no "…" and no parenthesised timer, so the ellipsis is what
// separates a live turn from a finished one. Anchored at line start (the spinner always
// renders at column 0, so a ≥2-space-indented transcript line that merely quotes a
// spinner — including this file's own examples, when a session is asked to debug the TUI
// — can't match). Best-effort; one captured frame.
// spinnerHead is the shared prefix of both spinner patterns: the head discipline above,
// the phrase run, the ellipsis, and the opening paren. What may follow inside the parens
// is what the two patterns disagree about.
const spinnerHead = `^(?:\S? ?[\p{L}\p{N}]|\S /)[^\n\x{2026}]*\x{2026} \(`

var spinnerRe = regexp.MustCompile(`(?m)` + spinnerHead + `[^)\n]*[0-9]+(?:h|m|s)\b`)

// spinnerThinkingRe matches the one live-turn frame that carries NO timer at all:
//
//	✳ Calculating… (almost done thinking with high effort)
//
// (real 60-column capture, claude_s36uuiv — the Console badged that session "waiting for
// input" for the whole "almost done thinking" window of a 14-minute turn).
//
// The parenthesised segments are NOT a fixed set that merely gains and loses members with
// the turn's phase — they are laid out against the pane width, and the status phrase wins.
// claude 2.1.246 computes an available width from the columns and the gerund, fits the
// status phrase ("thinking" / "still thinking" / "thinking more" / "thinking some more" /
// "almost done thinking", each + the effort suffix) FIRST, and only then adds the elapsed
// timer if what is left still fits, and the token count after that. So on a narrow pane —
// 60 columns is what the Console gives a session on a phone — a long status phrase alone
// exceeds the budget and the timer is dropped, not the phrase. Requiring the timer (as
// spinnerRe does) therefore false-idles by pane width: the same turn reads busy in a wide
// pane and idle in a narrow one, which is why this survived every earlier fix.
//
// Keyed on "thinking" because that is the only timer-less content the layout can produce:
// every other status ("running tool for 5s", "ran tool for 5s", "thought for 3s") embeds a
// timer of its own and is already spinnerRe's business, and a token count is wider than
// the timer it would have to displace, so it can never be the sole survivor.
//
// Tightened with a ")" end-of-line anchor that spinnerRe does not need: without a timer to
// vouch for it, "…" + "(thinking" is weak enough that col-0 prose could reach it, and the
// cost of a false positive here is a session stuck badged running (the transcript line does
// not scroll away by itself). The real spinner always ends its line at the closing paren.
var spinnerThinkingRe = regexp.MustCompile(`(?m)` + spinnerHead + `[^)\n]*\bthinking\b[^)\n]*\)[ \t]*$`)

// spinnerActive reports whether the captured pane text shows a turn actively running —
// the classic "esc to interrupt" affordance, or the live spinner header with a timer
// (spinnerRe) or with the timer squeezed out by a narrow pane (spinnerThinkingRe).
func spinnerActive(s string) bool {
	return strings.Contains(s, "esc to interrupt") ||
		spinnerRe.MatchString(s) || spinnerThinkingRe.MatchString(s)
}

// modeFooterRe matches claude's permission-mode footer strip — the "⏸ manual mode on" /
// "⏵⏵ auto mode on" / "⏵⏵ accept edits on" / "⏸ plan mode on" / "⏵⏵ bypass permissions on"
// line drawn under the input box. It is the one part of that strip that is always
// rendered, so it — not the hint that trails it — is what tells us the TUI is sitting at
// its input box.
//
// The trailing hint is contextual and must NOT be relied on: claude 2.1.212 omits
// "(shift+tab to cycle)" entirely in the default (manual) mode, and swaps it for other
// segments when there is background work to report ("⏵⏵ bypass permissions on · 1 shell ·
// ← for agents"). "? for shortcuts", which used to stand in as the default-mode footer,
// is gone from that strip too. Keying idle off those hints (as we did) false-negatives on
// every default-mode session — the stale-status→idle self-heal then never fires and a
// session that is plainly at its prompt stays badged running with a stop bar.
//
// Anchored at line start so prose in the transcript that merely quotes a mode name can't
// match. Both symbols are matched: ⏵⏵ (U+23F5 ×2) for the go-ahead modes, ⏸ (U+23F8) for
// manual/plan.
var modeFooterRe = regexp.MustCompile(`(?m)^\s*(?:\x{23F5}\x{23F5}|\x{23F8}) .*\bon\b`)

// ClaudeModeFooter reports whether s carries claude's permission-mode footer strip,
// regardless of WHICH mode is named. It is the last line of defence of claude's PaneMode
// (internal/agents/claude) against reading an added or renamed mode name as "footer not
// drawn" — an empty PaneMode is also the launch-seed readiness gate, so a misread costs the
// first prompt a 30-second wait.
func ClaudeModeFooter(s string) bool { return modeFooterRe.MatchString(s) }

// atPromptFooter reports whether the capture shows claude's input-box footer — the
// permission-mode strip, or the older builds' mode-cycle / shortcuts hints (kept so a
// pinned-older CLI still reads correctly). Modals draw over the strip instead of under
// it: a permission or plan-approval dialog replaces the whole input box + footer, so a
// missing footer is itself a signal that a dialog is up.
func atPromptFooter(s string) bool {
	return modeFooterRe.MatchString(s) ||
		strings.Contains(s, "shift+tab to cycle") ||
		strings.Contains(s, "? for shortcuts")
}

func HasSession(tn string) bool {
	return Run("has-session", "-t", session.ExactTarget(tn)) == nil
}

// CLIRecordsSince is the instant that separates what the CLI now running in tmux session tn
// has recorded from what an earlier process in that pane left behind; ok is false when there
// is no such session. Every launch is a fresh new-session whose pane closes with the CLI, and
// the CLI runs under the pane's own process, so the start of that process is the bound: an
// earlier process in the pane was dead before it began, and the CLI records nothing before it.
//
// tmux stamps the session's creation only to the second, too coarse to split a question the
// new CLI asked in its first second from one a process killed in that same second left behind,
// so the pane process's start is read from /proc instead. Where /proc is missing (a native
// macOS Agent) the bound falls back to the second after the stamp: a question of that second
// is then read as the earlier process's, which lets a send through the way it went before
// the gate knew about questions, rather than refusing one with nothing on screen to answer.
func CLIRecordsSince(tn string) (time.Time, bool) {
	sec, start, exact, ok := paneStart(tn)
	if !ok || exact {
		return start, ok
	}
	return time.Unix(sec+1, 0), true
}

// CLIStartedAfter is CLIRecordsSince rounded the other way where /proc is missing: the stamped
// second itself, so only a record certainly older than the pane's process falls before it.
// Use it to call something dead — a turn read as finished hides the stop button and lets the
// reaper take the workspace, so a record of the ambiguous first second has to stay live.
func CLIStartedAfter(tn string) (time.Time, bool) {
	sec, start, exact, ok := paneStart(tn)
	if !ok || exact {
		return start, ok
	}
	return time.Unix(sec, 0), true
}

// PaneLaunch identifies the launch running in session tn's pane. id is tmux's creation stamp,
// the pane id and the pane process's pid read verbatim, so it stays equal for as long as that
// launch lives and changes with the next one; at is when it started, as CLIRecordsSince puts
// it. Compare launches by id, never by at: where /proc has the start, at is recomputed against
// the clock on every read and moves by milliseconds between two reads of one launch.
func PaneLaunch(tn string) (id string, at time.Time, ok bool) {
	out, err := Output("list-panes", "-t", session.ExactTarget(tn), "-F", "#{session_created} #{pane_pid} #{pane_id}")
	if err != nil {
		return "", time.Time{}, false
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	f := strings.Fields(first)
	if len(f) == 0 {
		return "", time.Time{}, false
	}
	sec, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil || sec <= 0 {
		return "", time.Time{}, false
	}
	at = time.Unix(sec+1, 0)
	if len(f) > 1 {
		if pid, err := strconv.Atoi(f[1]); err == nil {
			if start, ok := ProcessStart(pid); ok {
				at = start
			}
		}
	}
	return strings.Join(f, " "), at, true
}

// paneStart reads tmux session tn's creation stamp (whole seconds) and, where /proc has it,
// the start of the pane's process (exact). ok is false when there is no such session.
func paneStart(tn string) (sec int64, start time.Time, exact, ok bool) {
	out, err := Output("list-panes", "-t", session.ExactTarget(tn), "-F", "#{session_created} #{pane_pid}")
	if err != nil {
		return 0, time.Time{}, false, false
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	f := strings.Fields(first)
	if len(f) == 0 {
		return 0, time.Time{}, false, false
	}
	sec, err = strconv.ParseInt(f[0], 10, 64)
	if err != nil || sec <= 0 {
		return 0, time.Time{}, false, false
	}
	if len(f) > 1 {
		if pid, err := strconv.Atoi(f[1]); err == nil {
			if start, ok := ProcessStart(pid); ok {
				return sec, start, true, true
			}
		}
	}
	return sec, time.Time{}, false, true
}

// clockTicks is USER_HZ, the unit of a /proc/<pid>/stat start time: 100 on every Linux ABI.
const clockTicks = 100

// ProcessStart returns when process pid started, to the clock tick: its start in ticks since
// boot (/proc/<pid>/stat, field 22) set against the uptime now (/proc/uptime). ok is false
// where /proc is missing or once the process is gone.
func ProcessStart(pid int) (time.Time, bool) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, false
	}
	// The command (field 2) is in parentheses and may itself hold spaces and ")".
	i := strings.LastIndexByte(string(stat), ')')
	if i < 0 {
		return time.Time{}, false
	}
	f := strings.Fields(string(stat[i+1:])) // f[0] is field 3
	if len(f) < 20 {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseInt(f[19], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	up, err := os.ReadFile("/proc/uptime")
	now := time.Now()
	if err != nil {
		return time.Time{}, false
	}
	upStr, _, _ := strings.Cut(strings.TrimSpace(string(up)), " ")
	uptime, err := strconv.ParseFloat(upStr, 64)
	if err != nil {
		return time.Time{}, false
	}
	age := uptime - float64(ticks)/clockTicks
	return now.Add(-time.Duration(age * float64(time.Second))), true
}

// SessionPaneID returns the active pane id (e.g. "%0") of a session's current
// window, or "" if none. Uses the "=" exact target for list-panes (a target-
// SESSION context, where "=" is honored), then returns the active pane.
func SessionPaneID(tn string) string {
	out, err := Output("list-panes", "-t", session.ExactTarget(tn), "-F", "#{pane_active} #{pane_id}")
	if err != nil {
		return ""
	}
	first := ""
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		if first == "" {
			first = f[1]
		}
		if f[0] == "1" {
			return f[1]
		}
	}
	return first // fall back to the first pane if none flagged active
}

// CapturePane returns the session's visible pane text, targeting the active pane by its
// id. NOTE: capture-pane does NOT accept the "=<session>" exact-target syntax that
// send-keys/list-panes take ("can't find pane: =name") — that silently returned empty and
// broke all pane scraping — so we resolve the pane id via SessionPaneID first.
func CapturePane(tn string) string {
	pane := SessionPaneID(tn)
	if pane == "" {
		return ""
	}
	out, err := Output("capture-pane", "-p", "-t", pane)
	if err != nil {
		return ""
	}
	return string(out)
}

// LiveSessionNames returns the set of currently-running claude_* tmux session slugs, and an
// empty set when tmux cannot be asked. Shared by the session list and the branch-switch guard so
// both agree on what "running" means. A decision that destroys something uses
// LiveSessionNamesChecked instead: a failed probe here reads as "nothing runs".
func LiveSessionNames() map[string]bool {
	live, _ := LiveSessionNamesChecked()
	return live
}

// LiveSessionNamesChecked is LiveSessionNames that tells "no session runs" from "tmux could not
// say". tmux's "no server running", a socket that does not exist, or no tmux binary to ask, is an
// empty set only when no tmux server process for that socket exists either: a server outlives its
// socket being removed or replaced and its binary being replaced. Any other
// failure (a socket the Agent may not open, a timeout, an unexpected exit) is an error, and the set
// is empty.
func LiveSessionNamesChecked() (map[string]bool, error) {
	live := map[string]bool{}
	out, err := Output("list-sessions", "-F", "#{session_name}")
	if err != nil {
		var ee *exec.ExitError
		switch {
		case errors.Is(err, exec.ErrNotFound) && !serverMayRun(ownSocket()):
			return live, nil
		case errors.As(err, &ee) && ee.ExitCode() == 1 && SaysAbsent(string(ee.Stderr)) && !serverMayRun(socketNamed(string(ee.Stderr))):
			return live, nil
		case errors.As(err, &ee):
			return live, fmt.Errorf("tmux list-sessions: %w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return live, fmt.Errorf("tmux list-sessions: %w", err)
	}
	for _, tn := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if tn == "" || !strings.HasPrefix(tn, session.TmuxPrefix) {
			continue
		}
		live[strings.TrimPrefix(tn, session.TmuxPrefix)] = true
	}
	return live, nil
}

// serverMayRun reports whether a tmux server process serving the socket at path may be running. A
// var so a test can answer for the host's own tmux.
var serverMayRun = tmuxServerMayRun

// ownSocket is the socket Cmd addresses, as tmux derives it: -L AF_TMUX_SOCKET, else the socket of
// an inherited $TMUX, else the default one.
func ownSocket() string {
	if l := os.Getenv("AF_TMUX_SOCKET"); l != "" {
		return socketPath(os.Getenv("TMUX_TMPDIR"), l, "")
	}
	if t := os.Getenv("TMUX"); t != "" {
		if p, _, _ := strings.Cut(t, ","); p != "" {
			return socketPath("", "", p)
		}
	}
	return socketPath(os.Getenv("TMUX_TMPDIR"), "", "")
}

// socketNamed is the socket path in tmux's "no server running on <path>" or "error connecting to
// <path> (...)", and the socket Cmd addresses when the message names none.
func socketNamed(msg string) string {
	if _, rest, ok := strings.Cut(msg, "error connecting to "); ok {
		if p, _, ok := strings.Cut(rest, " ("); ok && p != "" {
			return physical(p)
		}
	}
	if _, rest, ok := strings.Cut(msg, "no server running on "); ok {
		if p := strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0]); p != "" {
			return physical(p)
		}
	}
	return ownSocket()
}

// tmuxServerMayRun looks for a process named "tmux: server" (the name tmux gives its server) whose
// socket is want: the socket path follows from the server's own command line (-L, -S), its
// inherited $TMUX and its TMUX_TMPDIR, as tmux derives it. A process whose command line or
// environment cannot be read, or a process list that cannot be read, may be it.
func tmuxServerMayRun(want string) bool {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		comm, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err != nil || !strings.HasPrefix(string(comm), "tmux: server") {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			return true
		}
		env, err := os.ReadFile("/proc/" + e.Name() + "/environ")
		if err != nil {
			return true
		}
		tmpdir, inherited := "", ""
		for _, kv := range strings.Split(string(env), "\x00") {
			if v, ok := strings.CutPrefix(kv, "TMUX_TMPDIR="); ok {
				tmpdir = v
			}
			if v, ok := strings.CutPrefix(kv, "TMUX="); ok {
				inherited, _, _ = strings.Cut(v, ",")
			}
		}
		label, path := serverSocketFlags(strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00"))
		if label == "" && path == "" {
			path = inherited // as for ownSocket: no -L / -S, an inherited $TMUX names the socket
		}
		if socketPath(tmpdir, label, path) == want {
			return true
		}
	}
	return false
}

// socketPath is the socket tmux uses, its directory resolved: path when -S gave one, else
// <TMUX_TMPDIR or /tmp>/tmux-<uid>/<-L label or "default">.
func socketPath(tmpdir, label, path string) string {
	if path != "" {
		return physical(path)
	}
	if tmpdir == "" {
		tmpdir = "/tmp"
	}
	if label == "" {
		label = "default"
	}
	return physical(filepath.Join(tmpdir, fmt.Sprintf("tmux-%d", os.Getuid()), label))
}

// physical is p with its directory resolved through its nearest existing ancestor, the form tmux
// reports a socket in (it resolves TMUX_TMPDIR); the socket itself may be gone, so its own name is
// kept as it is.
func physical(p string) string {
	p = filepath.Clean(p)
	rest := filepath.Base(p)
	for d := filepath.Dir(p); ; {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			return filepath.Join(r, rest)
		}
		up := filepath.Dir(d)
		if up == d {
			return p
		}
		rest = filepath.Join(filepath.Base(d), rest)
		d = up
	}
}

// serverSocketFlags reads the socket a tmux command line addresses from its global options, which
// come before the command: the -L label and the -S path.
func serverSocketFlags(args []string) (label, path string) {
	for i := 1; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			break
		}
		if len(a) < 2 {
			continue
		}
		switch flag, rest := a[:2], a[2:]; flag {
		case "-L", "-S", "-f", "-c", "-T":
			v := rest
			if v == "" && i+1 < len(args) {
				i++
				v = args[i]
			}
			switch flag {
			case "-L":
				label = v
			case "-S":
				path = v
			}
		}
	}
	return label, path
}

// SaysAbsent reports whether tmux's message on exit 1 means "not there": its own "can't find
// session", "no server running", or a socket that does not exist. Exit 1 alone is not enough: tmux
// uses it for "error connecting to <socket> (Permission denied)" as well, a running server the
// Agent cannot reach.
func SaysAbsent(msg string) bool {
	return strings.Contains(msg, "can't find session") || strings.Contains(msg, "no server running") ||
		(strings.Contains(msg, "error connecting to") && strings.Contains(msg, "No such file or directory"))
}

// PaneStartCommand returns a session's tmux pane start command, and false when tmux cannot
// say. PaneKind in sessionx sniffs a kind from it, for an orphan session that has no meta.
func PaneStartCommand(name string) (string, bool) {
	out, err := Output("list-panes", "-t", session.ExactTarget(session.TmuxName(name)), "-F", "#{pane_start_command}")
	if err != nil {
		return "", false
	}
	return string(out), true
}

// AtIdlePrompt reports whether a claude pane is sitting at its ready input
// prompt — used to self-heal a stale status cache (a killed+resumed session, or a
// rejected permission / abandoned question, where no resolving hook fired). Idle means
// the input-box footer is drawn (atPromptFooter) with no live spinner (spinnerActive) and
// no modal over it ("Enter to select", "Esc to cancel", "Do you want to", …).
// Best-effort TUI read.
func AtIdlePrompt(name string) bool {
	// capture-pane needs a PANE target; the "=name" exact-SESSION form fails with
	// "can't find pane" (same reason send-keys resolves a %N pane id first).
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return false
	}
	out, err := Output("capture-pane", "-p", "-t", pane)
	if err != nil {
		return false
	}
	return atIdlePrompt(string(out))
}

// AgentsViewActive reports whether a claude pane's input box is bound to something
// OTHER than the session's own conversation. Typing there does not reach the session:
//
//   - agent selected in the rail — the input becomes a STEERING message for that
//     background agent, recorded in its own subagents/agent-*.jsonl as "The user sent a
//     new message while you were working:" and never in the session transcript;
//   - agents home screen (opened with ←) — the composer reads "describe a task for a new
//     session" and submitting there CREATES A NEW SESSION.
//
// Measured (control probe, claude 2.1.220 / 2026-07-30, testdata/footers/agents_*): typing a
// prompt with an agent selected in the rail marked that rail row "1 queued", and the text
// landed only in the agent's transcript, never in the session's own conversation — the same
// shape as the misdelivery seen in the fleet (sannme2). The characters do appear in the pane,
// so the sender sees it as a success.
func AgentsViewActive(name string) bool {
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return false
	}
	out, err := Output("capture-pane", "-p", "-t", pane)
	if err != nil {
		return false
	}
	return agentsViewActive(string(out))
}

// railMainUnselectedRe matches the background-agent rail's "main" row when it is NOT the
// selected one. The rail is drawn under the composer as
//
//	  ◯ main
//	❯ ● general-purpose  Sleep then reply DONE   1m 18s · ↓ 16.6k tokens
//
// with the FILLED glyph (U+25CF) on the selected row and a hollow one (U+25EF) elsewhere;
// "❯" marks the cursor while the rail is being navigated. We key on the hollow glyph in
// front of "main" — hollow variants are enumerated so that ANY unknown glyph reads as "not
// in the agents view". That direction matters: a drift then costs us the guard (the old
// behaviour) instead of blocking every legitimate injection.
var railMainUnselectedRe = regexp.MustCompile(`(?m)^\s*(?:\x{276F} )?[\x{25EF}\x{25CB}\x{25CC}] main\s*$`)

// composerBorderRe matches the full-width rule claude draws under its input box. It is the
// structural anchor for reading the footer/rail region: everything after the LAST one
// belongs to the chrome, everything before it is transcript text. Anchoring here rather
// than on the mode-footer strip matters because that strip is REPLACED while the rail is
// being navigated ("↑/↓ to select · Enter to view") — the state the incident passed
// through — and on the agents home screen.
var composerBorderRe = regexp.MustCompile(`(?m)^\s*\x{2500}{8,}\s*$`)

// agentsViewActive is the pure decision over one captured frame.
func agentsViewActive(s string) bool {
	tail := afterLastComposerBorder(s)
	if tail == "" {
		return false // no input box drawn (a dialog, or still starting) — nothing to judge here
	}
	// The agents home screen's footer offers "enter to return" (to the conversation);
	// its composer creates a NEW session, so it is just as wrong a place to type into.
	if strings.Contains(tail, "enter to return") {
		return true
	}
	return railMainUnselectedRe.MatchString(tail)
}

// afterLastComposerBorder returns the chrome region: everything below the input box's
// bottom rule (footer / hint / agent rail). "" when the box is not drawn.
func afterLastComposerBorder(s string) string {
	loc := composerBorderRe.FindAllStringIndex(s, -1)
	if len(loc) == 0 {
		return ""
	}
	return s[loc[len(loc)-1][1]:]
}

// LeaveAgentsView tries to put a claude pane's input box back on the session's own
// conversation, and reports whether it ended up there. Both routes are measured (control
// probe, claude 2.1.220 / 2026-07-30):
//
//   - bound to the rail: ↓ opens the rail selection with the cursor always on the first row
//     (main) → Enter returns to main's view → ↑ closes the rail selection (↑ on the first row
//     is the key that leaves it).
//   - agents home screen: Esc returns to the conversation (the screen itself says "esc
//     returns to it").
//
// Does nothing while a draft is present: Enter during rail selection has been confirmed to
// open the selection rather than send, but if the view had drifted, sending someone's
// half-written text would be unrecoverable. Callers should treat false as "could not get
// back" and give up on sending — failing beats silently misdelivering.
func LeaveAgentsView(name string) bool {
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return false
	}
	frame := CapturePane(session.TmuxName(name))
	if !agentsViewActive(frame) {
		return true
	}
	keys := []string{"Down", "Enter", "Up"}
	if strings.Contains(afterLastComposerBorder(frame), "enter to return") {
		keys = []string{"Escape"} // agents home screen
	} else if !composerEmpty(frame) {
		return false
	}
	for _, k := range keys {
		if err := Run("send-keys", "-t", pane, k); err != nil {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
	return !agentsViewActive(CapturePane(session.TmuxName(name)))
}

// composerRuleRe matches EITHER of the input box's rules. The top one carries the title
// ("───── Sleep then reply DONE ──"), so it is not a pure run like the bottom one —
// composerEmpty needs both to bound the region, hence the looser form.
var composerRuleRe = regexp.MustCompile(`(?m)^\s*\x{2500}{4,}.*$`)

// composerEmpty reports whether the input box holds no draft: the composer region (between
// its two rules) is just the bare "❯" prompt.
func composerEmpty(s string) bool {
	loc := composerRuleRe.FindAllStringIndex(s, -1)
	if len(loc) < 2 {
		return false
	}
	region := s[loc[len(loc)-2][1]:loc[len(loc)-1][0]]
	for _, ln := range strings.Split(region, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || t == "\u276f" {
			continue
		}
		return false
	}
	return true
}

// modalMarkers are fragments of the dialogs that claude draws OVER the input box
// (permission prompt, plan approval, folder trust, AskUserQuestion). Matched against a
// width-wrapped capture, so only fragments that survive wrapping are reliable:
// "Would you like to proceed" wraps mid-phrase in an 80-col pane, where the plan dialog is
// caught by "to approve" instead. Mostly redundant now that idle requires the footer strip
// (a dialog replaces it), but kept as defence in depth.
var modalMarkers = []string{
	"Enter to select", "Esc to cancel", "to approve",
	"Do you want to", "Would you like to proceed", "Ready to submit",
}

// ModalActive reports whether a claude pane is showing one of its dialogs (permission,
// plan approval, folder trust, AskUserQuestion). Callers that drive the TUI with named
// keys use it to tell "there is a modal to navigate" from "the keys will hit the plain
// input box", where arrows mean something else entirely (← switches to the agents view).
func ModalActive(name string) bool {
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return false
	}
	out, err := Output("capture-pane", "-p", "-t", pane)
	if err != nil {
		return false
	}
	return modalActive(string(out))
}

func modalActive(s string) bool {
	for _, m := range modalMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// atIdlePrompt is the pure decision over one captured frame — split out from AtIdlePrompt
// (which supplies the frame) so the real judgement, not a reimplementation of it, can be
// replayed against the recorded panes in testdata/footers.
func atIdlePrompt(s string) bool {
	// A live turn's spinner means NOT idle — checked first because the footer strip stays
	// drawn mid-turn, so it alone does not imply the ready prompt.
	if spinnerActive(s) {
		return false
	}
	for _, m := range modalMarkers {
		if strings.Contains(s, m) {
			return false
		}
	}
	return atPromptFooter(s)
}

// rateLimitOptionRe matches the usage-limit menu's numbered option lines. claude draws it
// when a turn is cut off by a usage limit:
//
//	  What do you want to do?
//	❯ 1. Stop and wait for limit to reset
//	  2. Ask your admin for more usage
//	  Enter to confirm · Esc to cancel
//
// Keyed on the numbered option line (like resumeMenuRe), NOT on the banner
// "You've hit your session limit …" — that banner is transcript text and STAYS on screen
// after the menu is dismissed, so it would keep reporting a menu that is long gone (the
// trap isCodexUpdateMenu documents). Either option is accepted so a reworded first choice
// does not lose the whole detection, and each is truncated well short of the full sentence
// because the menu wraps in a narrow pane.
//
// It is as width-fragile as modalMarkers; the line-start anchor is there so the repo's own
// prose (this comment included) cannot match.
var rateLimitOptionRe = regexp.MustCompile(`(?m)^\s*(?:\x{276F} )?\d+\.\s+(?:Stop and wait for limit|Ask your admin for more usage)`)
var currentRateLimitOptionRe = regexp.MustCompile(`(?m)^\s*(?:\x{276F} )?\d+\.\s+(?:Switch to usage|Add funds to continue with usage|Upgrade your plan|Wait here, then continue automatically|Don.t continue automatically)`)

// AtRateLimitModal reports whether a claude pane is parked on that menu.
//
// Why a dedicated detection is needed: when a turn is cut off by the usage limit claude does
// not fire the Stop hook, so status stays "working" (docs/log/47). The one route that repairs
// that is "HealIdle once the pane is back at the ready prompt", but this menu always contains
// "Esc to cancel" (modalMarkers) and replaces the input box's mode footer wholesale, so
// AtIdlePrompt returns false forever. The session then sticks on "in progress"
// permanently (measured 2026-07-31: about 16 hours, claude 2.1.220).
//
// A best-effort TUI read like the other detections. Missing it only costs the original
// sticking behaviour, and a false positive is bounded because HealIdle still decides from the
// transcript's tail, so a turn that has not actually finished is never treated as complete.
func AtRateLimitModal(name string) bool {
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return false
	}
	out, err := Output("capture-pane", "-p", "-t", pane)
	if err != nil {
		return false
	}
	return atRateLimitModal(string(out))
}

// atRateLimitModal is the pure decision over one captured frame. Both markers are
// required: the option lines say WHICH menu, and the confirm footer says it is still up
// (claude echoes the chosen option into the transcript, so the option text alone can
// linger after the menu is gone — the same reason isCodexUpdateMenu needs two markers).
func atRateLimitModal(s string) bool {
	return strings.Contains(s, "Enter to confirm") &&
		(rateLimitOptionRe.MatchString(s) || currentRateLimitOptionRe.MatchString(s))
}

// rateLimitDefaultRe recognizes the selected stop action regardless of menu order.
// Enter can otherwise spend credits or start Claude's own continuation.
var rateLimitDefaultRe = regexp.MustCompile(`(?m)^\s*\x{276F} \d+\.\s+(?:Stop and wait for limit|Stop[ \t]*$)`)

// ClaudeUsageLimitWait uses the live footer because the transcript banner survives cancellation.
func ClaudeUsageLimitWait(frame string) bool {
	lines := strings.Split(strings.TrimSpace(frame), "\n")
	for i := max(0, len(lines)-12); i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line != "Usage limit reached" && !strings.HasPrefix(line, "Usage limit reached ·") {
			continue
		}
		end := i + 1
		for end < len(lines) && end < i+4 {
			next := strings.TrimSpace(lines[end])
			if next == "" || strings.HasPrefix(next, "❯") || strings.HasPrefix(next, "──") {
				break
			}
			end++
		}
		footer := strings.Join(strings.Fields(strings.Join(lines[i:end], " ")), " ")
		if nativeLimitWaitFooter.MatchString(footer) {
			return true
		}
	}
	return false
}

var nativeLimitWaitFooter = regexp.MustCompile(`^Usage limit reached(?: · limit resets [^·]+)? (?:Continuing automatically (?:at [^·]+|when it resets)|Continuing shortly) · Esc to cancel(?:$| · )`)

var selectedFirstLimitOption = regexp.MustCompile(`(?m)^\s*\x{276F} 1\.\s+`)

func dismissLimitKey(frame string) string {
	if rateLimitDefaultRe.MatchString(frame) {
		return "Enter"
	}
	if selectedFirstLimitOption.MatchString(frame) && strings.Contains(frame, "Esc to cancel") {
		return "Escape"
	}
	return ""
}

// DismissRateLimitModal confirms a selected stop action and reports whether the menu is gone.
//
// Why pressing it automatically is acceptable: the choices are "wait for the reset" and "ask
// your admin for more usage", and only the latter carries a billing decision. The former
// waits, i.e. buys nothing, and until a human clears the menu the session can do nothing at
// all (measured: stuck for about 16 hours, docs/log/47 §4-3). Picking the default 1 is
// therefore recovery, not deciding in the user's stead. A user who wants option 2 can choose
// it while the menu is up (this auto-dismiss only acts while 1 is the selected option).
//
// Same shape as LeaveAgentsView: read the pane to confirm the premise, send the keys, then
// confirm the outcome from the pane again. Sending is not taken for success on its own,
// because send-keys returning 0 does not mean the TUI received anything.
func DismissRateLimitModal(name string) bool {
	tn := session.TmuxName(name)
	pane := SessionPaneID(tn)
	if pane == "" {
		return false
	}
	frame := CapturePane(tn)
	if !atRateLimitModal(frame) {
		return true // the menu is already gone
	}
	key := dismissLimitKey(frame)
	if key == "" {
		return false
	}
	if err := Run("send-keys", "-t", pane, key); err != nil {
		return false
	}
	time.Sleep(400 * time.Millisecond) // wait for the repaint (same interval as LeaveAgentsView)
	return !atRateLimitModal(CapturePane(tn))
}

// PaneRead is one capture classified by every predicate the live-state code needs. The
// sessions list polls every few seconds for EVERY session, so asking each predicate
// separately would spend one capture-pane per predicate per session per tick; the pane is
// a single frame, so read it once and judge it three ways. OK=false means the pane could
// not be read (no session / capture failed) — every verdict is then false, which is what
// each individual predicate returns in that case too.
type PaneRead struct {
	Busy bool // a turn is in flight (IsBusy)
	Idle bool // this ONE frame looks like the ready input box (AtIdlePrompt)
	// IdleSettled: Idle AND the pane has not been repainted for idleSettleWindow. Use
	// this — not Idle — to conclude "the turn is over" (self-heal, turn-end events),
	// because a frame that merely LOOKS idle is also what claude draws while it renders
	// an answer, for up to 21s at a time (idlesettle.go).
	IdleSettled    bool
	RateLimitMenu  bool // parked on the usage-limit menu (AtRateLimitModal)
	UsageLimitWait bool
	OK             bool
}

// ReadPane captures a session's pane once and returns all pane-derived verdicts.
//
// It also records the frame for the idle-settle clock (idlesettle.go), so callers that
// poll get IdleSettled for free. That makes ReadPane stateful across calls by design:
// the only thing that separates "waiting for input" from "printing the answer" is how
// long the same picture has stood still.
func ReadPane(name string) PaneRead {
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		ForgetPane(name)
		return PaneRead{}
	}
	out, err := Output("capture-pane", "-p", "-t", pane)
	if err != nil {
		return PaneRead{}
	}
	s := string(out)
	idle := atIdlePrompt(s)
	settled := observeFrame(name, s)
	return PaneRead{
		Busy:           spinnerActive(s),
		Idle:           idle,
		IdleSettled:    idle && settled,
		RateLimitMenu:  atRateLimitModal(s),
		UsageLimitWait: ClaudeUsageLimitWait(s),
		OK:             true,
	}
}

// IsBusy reports whether a claude pane is actively running a turn — its transcript shows
// the live spinner header (see spinnerActive), which ticks only while a turn is in flight
// (thinking or a running tool) and is replaced by a past-tense summary the moment the turn
// ends. It's the positive
// inverse of AtIdlePrompt, used to *reverse*-heal a status cache that reads idle while
// the pane is plainly working: the "working" status file can go missing (never written,
// or removed by the working→idle self-heal during a transient prompt frame) and then no
// mid-turn hook rewrites it — in bypass mode MessageDisplay/permtool don't touch status
// — so a busy session would wrongly badge "waiting for input" with no stop button until
// the next Stop. Best-effort TUI read; false when the pane can't be read.
func IsBusy(name string) bool {
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return false
	}
	out, err := Output("capture-pane", "-p", "-t", pane)
	if err != nil {
		return false
	}
	return spinnerActive(string(out))
}
