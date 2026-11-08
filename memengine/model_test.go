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
		`{"a":1}`:                    `{"a":1}`,
		"```json\n{\"a\":1}\n```":    `{"a":1}`,
		`Here: {"a":{"b":2}} done.`:  `{"a":{"b":2}}`,
		`no object`:                  `no object`,
		`Schema {"a": ...}: {"a":1}`: `{"a":1}`,
		`{"a":1} then use {x} here`:  `{"a":1}`,
		`{"s":"a } b { c"} tail }`:   `{"s":"a } b { c"}`,
		`{"a":1} {"b":2}`:            `{"a":1}`,
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

func TestCodexJSONLAdapter(t *testing.T) {
	out := `{"type":"thread.started","thread_id":"t"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"i0","type":"reasoning","text":"thinking"}}
{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"{\"edits\":[]}"}}
{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":20}}
`
	rep, err := ParseReply([]byte(out))
	if err != nil || rep.Text != `{"edits":[]}` || !rep.UsageKnown {
		t.Fatalf("%+v %v", rep, err)
	}
	if rep.Usage.TotalInput() != 1000 || rep.Usage.CacheReadTokens != 400 || rep.Usage.OutputTokens != 20 {
		t.Errorf("usage %+v", rep.Usage)
	}
	if _, err := ParseReply([]byte(`{"type":"turn.failed","message":"quota"}`)); err == nil || errors.Is(err, ErrInvalid) {
		t.Errorf("a failed turn is an error: %v", err)
	}
}

// A CLI that reports no usage is a valid model: the answer counts, the tokens are unknown.
func TestUsageUnknownIsValid(t *testing.T) {
	rep, err := ParseReply([]byte(`{"edits":[]}`))
	if err != nil || rep.UsageKnown || rep.Text != `{"edits":[]}` {
		t.Fatalf("%+v %v", rep, err)
	}
	env, _ := ParseReply([]byte(`{"result":"{\"edits\":[]}"}`))
	if env.UsageKnown || env.Text != `{"edits":[]}` {
		t.Errorf("envelope without usage: %+v", env)
	}
}

// A runaway model must not be able to fill the host's memory: past the cap the
// call fails (an error, not an invalid output) and the process is killed.
func TestRunawayOutputIsCappedAndKilled(t *testing.T) {
	c := sh(`cat >/dev/null; yes '{"x":1}'`)
	c.Timeout = 30 * time.Second
	start := time.Now()
	_, err := c.Call(context.Background(), "p")
	if err == nil || errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("want a capped-output error, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %v: the process was not killed at the cap", time.Since(start))
	}
}
