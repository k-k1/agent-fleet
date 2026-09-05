package claude

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Claude usage capture via the statusLine command. claude runs its configured
// statusLine on every render, piping the session JSON on stdin — and that JSON carries
// a `rate_limits` block (five_hour / seven_day, each with used_percentage + resets_at;
// docs: code.claude.com/docs/en/statusline). We wire the statusLine to
// `workspace-agent statusline`, capture that block into a small local file, and let
// HandleUsage read it. This replaces the unofficial, IP-rate-limited /api/oauth/usage
// network call entirely — mirroring how codex reads its rate_limits from the rollout.

// usageCapturePath is where the last captured rate_limits live (agent-owned, next to
// claude's other state under ConfigDir).
func usageCapturePath() string { return filepath.Join(ConfigDir(), "af-usage.json") }

type capturedWindow struct {
	UsedPercent float64 `json:"usedPercent"`
	ResetsAt    int64   `json:"resetsAt"` // unix epoch seconds
}

type capturedUsage struct {
	FiveHour   *capturedWindow `json:"fiveHour,omitempty"`
	SevenDay   *capturedWindow `json:"sevenDay,omitempty"`
	CapturedAt int64           `json:"capturedAt"` // unix epoch seconds of the capture
}

// readCapturedUsage loads the last captured rate_limits and the instant it was written.
// nil (zero time) when nothing has been captured yet or the file is unreadable.
func readCapturedUsage() (*capturedUsage, time.Time) {
	b, err := os.ReadFile(usageCapturePath())
	if err != nil {
		return nil, time.Time{}
	}
	var c capturedUsage
	if json.Unmarshal(b, &c) != nil {
		return nil, time.Time{}
	}
	at := time.Time{}
	if c.CapturedAt > 0 {
		at = time.Unix(c.CapturedAt, 0)
	}
	return &c, at
}

// statusLineRateLimit is one window as it appears in the statusLine payload.
//
// used_percentage is claude's `utilization * 100`, and utilization comes straight from
// the `anthropic-ratelimit-unified-{5h,7d}-utilization` response headers, which the
// server reports to two decimals (measured 2026-09-02: 0.03 / 0.01 — read back from
// `claude -p --output-format stream-json`'s rate_limit_event.unifiedWindows). So the
// percent we capture is a whole number, and a weekly window under 0.5% arrives as a
// flat 0 — "0%" here means "below 0.5%", not "nothing used". Don't read a stuck 0 as a
// dead capture (that misdiagnosis cost a whole investigation; the Console says so in
// wsbar.usage.claude.note).
type statusLineRateLimit struct {
	UsedPercent float64 `json:"used_percentage"`
	ResetsAt    int64   `json:"resets_at"`
}

// statusLineInput is the (sole) part of claude's statusLine JSON we consume.
type statusLineInput struct {
	RateLimits *struct {
		FiveHour *statusLineRateLimit `json:"five_hour"`
		SevenDay *statusLineRateLimit `json:"seven_day"`
	} `json:"rate_limits"`
}

// captureFromStatusLine parses a statusLine payload and, when it carries a rate_limits
// block, persists it atomically. Returns false when there's nothing to store — a payload
// without rate_limits (e.g. before the session's first API response) must NOT clobber a
// good earlier capture.
func captureFromStatusLine(b []byte) bool {
	var in statusLineInput
	if json.Unmarshal(b, &in) != nil || in.RateLimits == nil {
		return false
	}
	rl := in.RateLimits
	if rl.FiveHour == nil && rl.SevenDay == nil {
		return false
	}
	c := capturedUsage{CapturedAt: time.Now().Unix()}
	if rl.FiveHour != nil {
		c.FiveHour = &capturedWindow{UsedPercent: rl.FiveHour.UsedPercent, ResetsAt: rl.FiveHour.ResetsAt}
	}
	if rl.SevenDay != nil {
		c.SevenDay = &capturedWindow{UsedPercent: rl.SevenDay.UsedPercent, ResetsAt: rl.SevenDay.ResetsAt}
	}
	writeCapturedUsage(c)
	return true
}

func writeCapturedUsage(c capturedUsage) {
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	p := usageCapturePath()
	tmp := p + ".af-tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// RunStatusLine is the `workspace-agent statusline` entry. claude pipes the session
// JSON on stdin every render; we capture its rate_limits (local, no network) and print
// nothing — UNLESS a delegate command follows (the user had their own statusLine and we
// wrapped it): then we feed it the same stdin and pass its output straight through so
// their status line still renders. Never fails loudly: a broken capture must not disturb
// claude's footer.
func RunStatusLine(args []string) {
	in, _ := io.ReadAll(os.Stdin)
	captureFromStatusLine(in)

	delegate := delegateArg(args)
	if delegate == "" {
		return // capture-only: no output
	}
	cmd := exec.Command("sh", "-c", delegate)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// delegateArg returns the wrapped original statusLine command that follows a
// "--delegate" flag (a single, shell-quoted argument), or "" for capture-only.
func delegateArg(args []string) string {
	for i, a := range args {
		if a == "--delegate" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// captureFlag marks a statusLine command as ours. It carries no behaviour (the
// `statusline` subcommand always captures) — it exists so EnsureStatusLine can
// recognise an install made by a DIFFERENT binary path as ours and peel it instead of
// wrapping it again. See unwrapOurs.
const captureFlag = "--af-capture"

// maxStatusLineCmd bounds what we're willing to write. A single argv entry over
// ~128KiB is rejected by exec(2) (E2BIG) — a statusLine that long doesn't just look
// bad, it never runs, and capture stops dead. Well under that.
const maxStatusLineCmd = 8192

// statuslineCmd is the command we point claude's statusLine at (absolute exe so it
// resolves in claude's spawn context regardless of PATH — and never a volatile one,
// see paths.ConfigExePath: a dev/smoke build that pinned its own /tmp path here took
// the capture down with it when it was deleted).
func statuslineCmd() string { return paths.ConfigExePath() + " statusline " + captureFlag }

// EnsureStatusLine wires claude's statusLine to statuslineCmd so we capture rate_limits.
// Idempotent, called at startup. If the user already configured their OWN statusLine we
// don't clobber it — we WRAP it: our command captures, then delegates to theirs (via
// --delegate) so their status line keeps rendering. A statusLine that's already ours is
// re-pointed at this binary (never wrapped again), keeping any delegate it carried.
func EnsureStatusLine() {
	m := readSettings()
	ours := statuslineCmd()

	cur, _ := m["statusLine"].(map[string]any)
	curCmd, _ := cur["command"].(string)

	// Peel EVERY layer of our own capture command first. An install made by a different
	// binary path (a dev build in /tmp, the e2e binary, a scratchpad copy) is still ours;
	// matching on the current exe path alone made each one look foreign and wrap the
	// whole command again — and since each wrap shell-quotes the previous one, the
	// escaping doubles per layer. Observed in the wild: 13 layers, a 798KB command that
	// exec(2) refused with E2BIG, so claude captured nothing and the Console's usage chip
	// froze at its last value. What survives the peel is the user's own statusLine (or "").
	foreign := strings.TrimSpace(unwrapOurs(curCmd))

	want := ours
	if foreign != "" {
		if w := ours + " --delegate " + shellQuote(foreign); len(w) <= maxStatusLineCmd {
			want = w
		}
		// Over the bound → capture-only. Losing a delegate beats writing a command that
		// can't be exec'd (which would lose the capture too).
	}
	if want == curCmd {
		return
	}

	next := map[string]any{}
	for k, v := range cur { // preserve padding / refreshInterval / etc.
		next[k] = v
	}
	next["type"] = "command"
	next["command"] = want
	m["statusLine"] = next
	_ = writeSettings(m)
}

// unwrapOurs strips our own capture layers from an installed statusLine command and
// returns whatever they wrapped — the user's own command, or "" when the innermost
// layer is a capture-only install of ours. A command that isn't ours comes back
// unchanged, so a foreign statusLine is never mistaken for a layer to discard.
func unwrapOurs(cmd string) string {
	for range 64 { // depth cap: a pathological chain must terminate, not spin
		inner, ok := peelOurs(cmd)
		if !ok {
			return cmd
		}
		cmd = inner
	}
	return ""
}

// peelOurs recognises one layer of our capture command and returns what it delegates
// to. Ours is `<exe> statusline [--af-capture] [--delegate '<cmd>']`: the flag marks
// installs from this version, and the bare 2-token / `--delegate` forms cover the ones
// written before the flag existed (all of which we wrote, from whatever path).
func peelOurs(cmd string) (string, bool) {
	toks := shellSplit(cmd)
	if len(toks) < 2 || toks[1] != "statusline" {
		return "", false
	}
	rest := toks[2:]
	if len(rest) > 0 && rest[0] == captureFlag {
		rest = rest[1:]
	}
	switch {
	case len(rest) == 0:
		return "", true // capture-only leaf
	case len(rest) == 2 && rest[0] == "--delegate":
		return rest[1], true
	}
	return "", false
}

// shellSplit tokenizes the way sh would for the forms we write and are likely to be
// handed: plain words, single-quoted args (as produced by shellQuote, including its
// '\” escape), double quotes, and backslash escapes. Anything it can't tokenize
// (unterminated quote) yields nil, which peelOurs reads as "not ours" — leave it be.
func shellSplit(s string) []string {
	var out []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			inWord = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil // unterminated
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			inWord = true
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil // unterminated
			}
		case c == '\\' && i+1 < len(s):
			inWord = true
			i++
			cur.WriteByte(s[i])
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

// shellQuote wraps s so a shell passes it through as a single literal argument.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
