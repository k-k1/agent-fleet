package harness

// approval.go is ADR 0093 decision 5's namesake feature: "ツールはうちのもの。だから
// 承認が本物になる" — since this package's own process is what is about to run the
// tool, blocking here really does block it (a CLI-driven kind can only ask its CLI
// nicely). Every Mutates tool (write, edit, bash) goes through approve() before
// Run executes, unconditionally — bashDanger below only enriches the summary a
// caller's UI shows, it never widens or narrows WHICH calls are gated. There is no
// bypass path: a caller cannot skip the gate for one call without setting
// Runtime.Approve to nil for the whole Runtime (an explicit, all-or-nothing,
// caller-visible choice), which is the deliberate absence of a per-call escape
// hatch.

import (
	"context"
	"regexp"
	"strings"
)

// declinedError marks a Mutates call the approval gate refused. executeOne folds
// this into the RoleTool message text instead of treating it as a fatal loop
// error, so the model sees its own request was declined and can react (ask
// something else, propose an alternative, stop) instead of the whole turn dying.
type declinedError struct{ reason string }

func (e *declinedError) Error() string {
	if e.reason == "" {
		return "the user declined this action"
	}
	return "the user declined this action: " + e.reason
}

// approve asks rt.Approve (if set) before a Mutates tool runs. A nil Approve
// auto-approves — see Runtime.Approve's doc for why that is a caller's explicit
// choice, not this package's default posture in any UI that wires one up.
func approve(ctx context.Context, rt *Runtime, call ToolCall, tool Tool) error {
	if rt.Approve == nil {
		return nil
	}
	summary := approvalSummary(call, tool)
	ok, err := rt.Approve(ctx, call, tool, summary)
	if err != nil {
		return err
	}
	if !ok {
		return &declinedError{reason: summary}
	}
	return nil
}

// approvalSummary is the human-facing line a caller's UI shows when asking for
// approval. For bash, it includes the dangerous-command reason (if any) so the
// person approving sees WHY this one call stands out, not just that "bash" was
// called — every bash call is gated the same way regardless of this reason.
func approvalSummary(call ToolCall, tool Tool) string {
	if tool.Def.Name == "bash" {
		if reason, dangerous := bashDanger(bashCommandArg(call.Arguments)); dangerous {
			return reason
		}
	}
	return "run " + tool.Def.Name
}

// bashDangerPatterns are commands whose blast radius is larger than "this one
// call did something wrong" — irreversible deletes, force-pushes, privilege
// escalation, piping a remote script into a shell, and similar. This is a
// best-effort UX signal for the approval prompt (ADR 0093 decision 5), NOT the
// security boundary itself — the boundary is that EVERY bash call is gated,
// dangerous or not (approve, above). A pattern missing from this list weakens the
// prompt's wording, not the gate.
var bashDangerPatterns = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`\brm\s+(-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*|-[a-zA-Z]*f[a-zA-Z]*r[a-zA-Z]*)\b`), "recursive forced delete (rm -rf)"},
	{regexp.MustCompile(`\bgit\s+push\b.*(--force|-f)\b`), "force-push (can overwrite remote history)"},
	{regexp.MustCompile(`\bgit\s+(reset\s+--hard|clean\s+-[a-zA-Z]*f|filter-branch|filter-repo)\b`), "rewrites or discards git history/working tree"},
	{regexp.MustCompile(`\bsudo\b`), "privilege escalation (sudo)"},
	{regexp.MustCompile(`\bchmod\s+(-R\s+)?(777|a\+w)\b`), "loosens file permissions broadly"},
	{regexp.MustCompile(`\bdd\s+if=`), "raw disk/device write (dd)"},
	{regexp.MustCompile(`\bmkfs`), "formats a filesystem (mkfs)"},
	{regexp.MustCompile(`\b(shutdown|reboot|poweroff)\b`), "stops or restarts the host"},
	{regexp.MustCompile(`\bkill\s+-9\s+-?1\b`), "kills every process this user owns"},
	{regexp.MustCompile(`\b(curl|wget)\b[^|]*\|\s*(sudo\s+)?(sh|bash|zsh)\b`), "pipes a downloaded script straight into a shell"},
	{regexp.MustCompile(`>\s*/dev/sd[a-z]\b`), "writes directly to a block device"},
}

// bashDanger reports whether cmd matches a known-risky pattern, and a short
// human-readable reason if so.
func bashDanger(cmd string) (reason string, dangerous bool) {
	for _, p := range bashDangerPatterns {
		if p.re.MatchString(cmd) {
			return "dangerous command: " + p.reason + " — " + strings.TrimSpace(cmd), true
		}
	}
	return "", false
}

// bashCommandArg pulls the "command" field out of the bash tool's raw arguments
// JSON, best-effort (an unparseable body just yields "", which bashDanger reports
// as not dangerous — the approval gate itself still runs regardless).
func bashCommandArg(argsJSON string) string {
	var args bashArgs
	if err := decodeArgs(argsJSON, &args); err != nil {
		return ""
	}
	return args.Command
}
