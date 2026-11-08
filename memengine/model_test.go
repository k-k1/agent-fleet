package memengine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func sh(script string) Command {
	return Command{Argv: []string{"sh", "-c", script}, Timeout: 10 * time.Second}
}

func TestCommandPlainJSON(t *testing.T) {
	rep, err := sh(`cat >/dev/null; printf '{"operation":"add","targets":[],"reason":"r"}'`).Call(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Usage != (Usage{}) || !strings.HasPrefix(rep.Text, `{"operation"`) {
		t.Fatalf("%+v", rep)
	}
}

func TestCommandReadsPromptFromStdin(t *testing.T) {
	rep, err := sh(`p=$(cat); printf '{"echo":"%s"}' "$p"`).Call(context.Background(), "hello-prompt")
	if err != nil || rep.Text != `{"echo":"hello-prompt"}` {
		t.Fatalf("%q %v", rep.Text, err)
	}
}

func TestCommandEnvelopeWithUsage(t *testing.T) {
	env := `{"type":"result","is_error":false,"result":"` + "```json\\n{\\\"edits\\\":[]}\\n```" + `","usage":{"input_tokens":2,"cache_creation_input_tokens":600,"cache_read_input_tokens":10,"output_tokens":9,"service_tier":"standard"}}`
	rep, err := ParseReply([]byte(env))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Text != `{"edits":[]}` {
		t.Errorf("text %q", rep.Text)
	}
	if rep.Usage.TotalInput() != 612 || rep.Usage.OutputTokens != 9 {
		t.Errorf("usage %+v", rep.Usage)
	}
}

func TestEnvelopeError(t *testing.T) {
	if _, err := ParseReply([]byte(`{"is_error":true,"result":"rate limited"}`)); err == nil || errors.Is(err, ErrInvalid) {
		t.Fatalf("a failed call is an error, not an invalid output: %v", err)
	}
}

func TestCommandFailureAndTimeout(t *testing.T) {
	if _, err := sh(`echo boom >&2; exit 3`).Call(context.Background(), "p"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("exit status not surfaced: %v", err)
	}
	c := sh(`sleep 5`)
	c.Timeout = 100 * time.Millisecond
	start := time.Now()
	if _, err := c.Call(context.Background(), "p"); err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("timeout not enforced: %v after %v", err, time.Since(start))
	}
	if _, err := (Command{}).Call(context.Background(), "p"); err == nil {
		t.Error("empty argv must be refused")
	}
}

func TestExtractJSON(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1}`:                   `{"a":1}`,
		"```json\n{\"a\":1}\n```":   `{"a":1}`,
		`Here: {"a":{"b":2}} done.`: `{"a":{"b":2}}`,
		`no object`:                 `no object`,
	} {
		if got := ExtractJSON(in); got != want {
			t.Errorf("ExtractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassifyAndPatchDropInvalidWithoutRetry(t *testing.T) {
	calls := 0
	m := funcModel(func(string) (Reply, error) { calls++; return Reply{Text: `{"operation":"bogus"}`}, nil })
	if _, _, err := Classify(context.Background(), m, shown); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
	if _, _, err := Patch(context.Background(), m, PatchRequest{Body: "b"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
	if calls != 2 {
		t.Errorf("an invalid answer must not be retried: %d calls for 2 requests", calls)
	}
}

type funcModel func(string) (Reply, error)

func (f funcModel) Call(_ context.Context, p string) (Reply, error) { return f(p) }
