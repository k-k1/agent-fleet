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
	// Usage is what the command reported, zero when it reported nothing.
	Usage Usage
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

// ParseReply reads a command's stdout into a Reply.
func ParseReply(stdout []byte) (Reply, error) {
	r := Reply{Raw: stdout}
	text := strings.TrimSpace(string(stdout))
	var env struct {
		Result  *string `json:"result"`
		Usage   *Usage  `json:"usage"`
		IsError bool    `json:"is_error"`
	}
	if json.Unmarshal([]byte(text), &env) == nil && env.Result != nil {
		if env.IsError {
			return r, fmt.Errorf("memengine: model reported an error: %s", firstLine(*env.Result))
		}
		if env.Usage != nil {
			r.Usage = *env.Usage
		}
		text = *env.Result
	}
	r.Text = ExtractJSON(text)
	return r, nil
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
