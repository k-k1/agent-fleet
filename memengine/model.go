package memengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Reply is what one model call returned.
type Reply struct {
	// Text is the model's answer, with any command envelope and code fence
	// removed. It is not yet validated.
	Text string
	// Usage is what the command reported. UsageKnown is false when the CLI
	// reports none, which is a valid answer: tokens are then unknown, not zero.
	Usage      Usage
	UsageKnown bool
	// Raw is the command's stdout untouched, kept for the evaluation record.
	Raw []byte
}

// Model is the plug-in seam: a prompt in, an answer out.
type Model interface {
	Call(ctx context.Context, prompt string) (Reply, error)
}

// Command is a Model backed by a one-shot command: the prompt goes to stdin,
// the answer comes from stdout, and argv is the whole configuration. Nothing
// else (no environment, no key) is injected, so a model is swapped by
// changing argv.
//
// Stdout is either the answer JSON itself, or an envelope object with a string
// "result" and an optional "usage" object, which is the shape of
// `claude -p --output-format json`. Both are accepted so that a plain script
// can serve as a model.
type Command struct {
	Argv    []string
	Timeout time.Duration // zero means two minutes
}

// Call runs the command once.
func (c Command) Call(ctx context.Context, prompt string) (Reply, error) {
	if len(c.Argv) == 0 {
		return Reply{}, errors.New("memengine: empty model command")
	}
	to := c.Timeout
	if to <= 0 {
		to = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Stdin = strings.NewReader(prompt)
	// A killed command can leave a grandchild holding the pipes open; do not wait for it.
	cmd.WaitDelay = time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return Reply{Raw: out.Bytes()}, fmt.Errorf("memengine: model command failed: %w: %s", err, firstLine(errb.String()))
	}
	return ParseReply(out.Bytes())
}

// UsageAdapter reads one CLI's stdout shape: the answer text and, when the CLI
// reports it, the token usage. ok is false when stdout is not that CLI's shape.
// Adding a CLI is adding an adapter here; a CLI that reports no usage is valid
// and leaves Reply.UsageKnown false.
type UsageAdapter func(stdout string) (text string, u Usage, usageKnown bool, ok bool, err error)

// Adapters are tried in order; the first whose shape matches wins, and plain
// stdout (the answer JSON itself) is the fallback.
var Adapters = []UsageAdapter{claudeEnvelope, codexJSONL}

// ParseReply reads a command's stdout into a Reply.
func ParseReply(stdout []byte) (Reply, error) {
	r := Reply{Raw: stdout}
	text := strings.TrimSpace(string(stdout))
	for _, a := range Adapters {
		t, u, known, ok, err := a(text)
		if !ok {
			continue
		}
		if err != nil {
			return r, err
		}
		r.Usage, r.UsageKnown, text = u, known, t
		break
	}
	r.Text = ExtractJSON(text)
	return r, nil
}

// claudeEnvelope is `claude -p --output-format json`: an object with a string
// "result" and a "usage" object.
func claudeEnvelope(text string) (string, Usage, bool, bool, error) {
	var env struct {
		Result  *string `json:"result"`
		Usage   *Usage  `json:"usage"`
		IsError bool    `json:"is_error"`
	}
	if json.Unmarshal([]byte(text), &env) != nil || env.Result == nil {
		return "", Usage{}, false, false, nil
	}
	if env.IsError {
		return "", Usage{}, false, true, fmt.Errorf("memengine: model reported an error: %s", firstLine(*env.Result))
	}
	if env.Usage == nil {
		return *env.Result, Usage{}, false, true, nil
	}
	return *env.Result, *env.Usage, true, true, nil
}

// codexJSONL is `codex exec --json`: one event per line; the last
// agent_message item is the answer and turn.completed carries the usage
// (input_tokens includes cached_input_tokens there, so the cached part is
// reported as cache read and subtracted from plain input). Written from the
// documented event shape and covered by a fixture; not yet run against the CLI.
func codexJSONL(text string) (string, Usage, bool, bool, error) {
	var answer string
	var u Usage
	seen, known := false, false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage *struct {
				Input  int `json:"input_tokens"`
				Cached int `json:"cached_input_tokens"`
				Output int `json:"output_tokens"`
			} `json:"usage"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type == "" {
			continue
		}
		seen = true
		switch {
		case ev.Type == "item.completed" && ev.Item.Type == "agent_message":
			answer = ev.Item.Text
		case ev.Type == "turn.completed" && ev.Usage != nil:
			u = u.Add(Usage{InputTokens: ev.Usage.Input - ev.Usage.Cached, CacheReadTokens: ev.Usage.Cached, OutputTokens: ev.Usage.Output})
			known = true
		case ev.Type == "turn.failed" || ev.Type == "error":
			return "", Usage{}, false, true, fmt.Errorf("memengine: model reported an error: %s", firstLine(ev.Message))
		}
	}
	if !seen || !strings.Contains(text, `"type":"`) || (answer == "" && !known) {
		return "", Usage{}, false, false, nil
	}
	return answer, u, known, true, nil
}

// ExtractJSON returns the outermost JSON object in s, tolerating a code fence
// or a sentence around it. When there is none it returns s trimmed, and the
// schema check rejects it.
func ExtractJSON(s string) string {
	s = strings.TrimSpace(s)
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
