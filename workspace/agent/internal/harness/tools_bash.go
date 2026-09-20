package harness

// tools_bash.go is the bash builtin plus the rtk pass-through docs/log/99 §4.13
// describes: "自前 bash ツールの exec 前に rtk を通すだけ（rtk はコマンドを書き換える
// CLI）". Every other kind wires rtk through a hook/plugin/AGENTS.md block because
// the CLI, not us, owns the exec — here we own the exec ourselves, so the
// integration is exactly one subprocess call in front of the real one: no hook, no
// plugin, no file (ADR 0093 decision 5). rtk being absent or misbehaving is not a
// reason to fail the command — it only ever affects how many bytes come back to
// the model, never whether the command runs, so this is fail-open by design
// (unlike the approval gate in approval.go, which is fail-closed by design).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultBashTimeout = 5 * time.Minute
	maxBashTimeout     = 30 * time.Minute
)

type bashArgs struct {
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec,omitempty"`
}

var bashToolDef = ToolDef{
	Name:        "bash",
	Description: "Run a shell command in the working directory. Always requires user approval before it runs.",
	Parameters: json.RawMessage(`{
		"type":"object",
		"properties":{
			"command":{"type":"string","description":"The shell command to run (sh -c semantics)."},
			"timeout_sec":{"type":"integer","description":"Timeout in seconds (default 300, max 1800)."}
		},
		"required":["command"]
	}`),
}

func runBash(ctx context.Context, rt *Runtime, argsJSON string) (string, error) {
	var a bashArgs
	if err := decodeArgs(argsJSON, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	if rt.Cwd == "" {
		return "", fmt.Errorf("no working directory is configured for this session")
	}
	timeout := defaultBashTimeout
	if a.TimeoutSec > 0 {
		timeout = time.Duration(a.TimeoutSec) * time.Second
		if timeout > maxBashTimeout {
			timeout = maxBashTimeout
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := rtkRewrite(runCtx, a.Command)

	// exec.CommandContext kills the child (its whole process group is NOT
	// implied, but the direct child is) as soon as runCtx is done — this is how
	// ADR 0093's "途中 cancel" requirement reaches a running bash command: the
	// caller cancelling ctx (loop.go's Run) or the per-call timeout above both
	// flow through the same runCtx.
	c := exec.CommandContext(runCtx, "bash", "-c", cmd)
	c.Dir = rt.Cwd
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = &out
	runErr := c.Run()

	result := out.String()
	switch {
	case runCtx.Err() != nil && ctx.Err() == nil:
		// The per-call timeout fired, not the caller's own ctx — report it as
		// part of the tool result (not a loop-stopping cancellation) so the
		// model learns its command timed out and can retry with a longer one.
		result += fmt.Sprintf("\n[command timed out after %s]", timeout)
	case ctx.Err() != nil:
		return result, ctx.Err()
	case runErr != nil:
		result += fmt.Sprintf("\n[exit error: %s]", runErr)
	}
	return result, nil
}

// rtkRewrite passes cmd through `rtk rewrite` and uses its stdout if it produced
// any (rtk's own contract: prints the rewritten command and exits when it knows a
// more compact equivalent, prints nothing otherwise — exit CODE has drifted across
// rtk releases, so this reads stdout rather than depending on which code means
// what). Any failure to run rtk at all (not installed, or any other error) falls
// back to the original command unchanged.
func rtkRewrite(ctx context.Context, cmd string) string {
	if _, err := exec.LookPath("rtk"); err != nil {
		return cmd
	}
	rewriteCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(rewriteCtx, "rtk", "rewrite", cmd).Output()
	if err != nil {
		return cmd
	}
	rewritten := strings.TrimSpace(string(out))
	if rewritten == "" {
		return cmd
	}
	return rewritten
}
