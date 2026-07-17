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

// statuslineCmd is the command we point claude's statusLine at (absolute exe so it
// resolves in claude's spawn context regardless of PATH).
func statuslineCmd() string { return paths.ExePath() + " statusline" }

// EnsureStatusLine wires claude's statusLine to statuslineCmd so we capture rate_limits.
// Idempotent, called at startup. If the user already configured their OWN statusLine we
// don't clobber it — we WRAP it: our command captures, then delegates to theirs (via
// --delegate) so their status line keeps rendering. A statusLine that's already ours (or
// already wrapped) is left untouched.
func EnsureStatusLine() {
	m := readSettings()
	ours := statuslineCmd()

	cur, _ := m["statusLine"].(map[string]any)
	curCmd, _ := cur["command"].(string)

	// Already ours (capture-only or a wrapper we installed) → nothing to do.
	if strings.HasPrefix(curCmd, ours) {
		return
	}

	next := map[string]any{}
	for k, v := range cur { // preserve padding / refreshInterval / etc.
		next[k] = v
	}
	next["type"] = "command"
	if strings.TrimSpace(curCmd) != "" {
		// Foreign statusLine — wrap it so it still renders after we capture.
		next["command"] = ours + " --delegate " + shellQuote(curCmd)
	} else {
		next["command"] = ours
	}
	m["statusLine"] = next
	_ = writeSettings(m)
}

// shellQuote wraps s so a shell passes it through as a single literal argument.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
