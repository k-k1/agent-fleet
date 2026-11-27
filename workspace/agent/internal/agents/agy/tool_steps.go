package agy

import (
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// The CLI uses a local ID or prefixes it with the task's conversation UUID.
const taskIDPattern = `(?:` + conversationIDPattern + `/)?([A-Za-z0-9_-]+)`

var (
	commandExitRe       = regexp.MustCompile(`^(?:The command|Command) exited with code (-?[0-9]+)\.$`)
	backgroundCommandRe = regexp.MustCompile(`^Background command ID: ([A-Za-z0-9_-]+)$`)
	backgroundTaskRe    = regexp.MustCompile(`^Tool is running as a background task with task id: ` + taskIDPattern + `$`)
	taskStatusRe        = regexp.MustCompile(`^Task: ` + taskIDPattern + `\nStatus: ((?i:pending|queued|running|waiting|done|completed|error|failed|canceled|interrupted|halted))(?:\n|$)`)
	readFileRe          = regexp.MustCompile("^File Path: `((?:file:///|/)[^`\n]+)`\nTotal Lines: [0-9]+\nTotal Bytes: [0-9]+(?:\n|$)")
)

func toolStep(typ, content string) transcript.Part {
	body := strings.TrimSpace(content[len(stepMetaLineRe.FindString(content)):])
	out := body
	if typ == "RUN_COMMAND" {
		out = stripCommandIndent(out)
	}
	if len(out) > toolOutputMax {
		out = out[:toolOutputMax] + "…"
	}
	p := transcript.Part{Kind: "tool", Tool: typ, Output: out}
	// The edit diff can sit past the display limit. Reads only carry Info because
	// File feeds the changed-files list.
	p.File, p.Verb, p.Edits = stepEdit(body)
	if p.File != "" {
		p.Info = transcript.Clip(p.File)
		if typ == "GENERIC" {
			p.Tool = "EDIT_FILE"
			if p.Verb == "add" {
				p.Tool = "WRITE_FILE"
			}
		}
		return p
	}
	p.Tool, p.Info = describeToolResult(typ, body)
	return p
}

func describeToolResult(typ, body string) (tool, info string) {
	first, _, _ := strings.Cut(body, "\n")
	if typ == "GENERIC" || typ == "RUN_COMMAND" {
		if first == "The command completed successfully." {
			return "RUN_COMMAND", "completed successfully"
		}
		if m := commandExitRe.FindStringSubmatch(first); m != nil {
			return "RUN_COMMAND", transcript.Clip("exit code " + m[1])
		}
		if m := backgroundCommandRe.FindStringSubmatch(first); m != nil {
			return "RUN_COMMAND", transcript.Clip(m[1] + " · started in background")
		}
	}
	if typ == "GENERIC" {
		if m := backgroundTaskRe.FindStringSubmatch(first); m != nil {
			return "Background task", transcript.Clip(m[1] + " · started in background")
		}
		// The whole status header distinguishes a task check from arbitrary output.
		if m := taskStatusRe.FindStringSubmatch(body); m != nil {
			return "Check task status", transcript.Clip(m[1] + " · " + strings.ToLower(m[2]) + " when checked")
		}
	}
	if typ == "GENERIC" || typ == "VIEW_FILE" {
		if m := readFileRe.FindStringSubmatch(body); m != nil {
			return "VIEW_FILE", transcript.Clip(agyPath(m[1]))
		}
		// Legacy rows already name the tool and may only carry a bare path header.
		if path, ok := strings.CutPrefix(first, "File Path: "); typ == "VIEW_FILE" && ok {
			return typ, transcript.Clip(agyPath(path))
		}
	}
	return typ, ""
}
