package mcpproj

import (
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestParseJSONServersClaudeMCPJSON(t *testing.T) {
	// The docs/56 §1 motivating example (novel-lab's .mcp.json), verbatim shape.
	raw, err := decodeJSONObject([]byte(`{"mcpServers":{"syosetu":{"type":"stdio","command":"uv",
	  "args":["run","--quiet","${HOME}/repos/narou-mcp-stdio/narou_mcp.py"],
	  "env":{"SYOSETU_MIN_INTERVAL":"0.7"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	sp := mcpreg.JSONEntrySpellings[session.KindClaude]
	got, err := parseJSONServers(raw, sp)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := got["syosetu"]
	if !ok {
		t.Fatalf("missing syosetu: %+v", got)
	}
	if s.Transport != TransportStdio || s.Command != "uv" {
		t.Fatalf("got %+v", s)
	}
	wantArgs := []string{"run", "--quiet", "${HOME}/repos/narou-mcp-stdio/narou_mcp.py"}
	if !reflect.DeepEqual(s.Args, wantArgs) {
		t.Fatalf("args: %+v", s.Args)
	}
	if !reflect.DeepEqual(s.Env, map[string]string{"SYOSETU_MIN_INTERVAL": "0.7"}) {
		t.Fatalf("env: %+v", s.Env)
	}
}

func TestParseJSONServersOpencodeFoldedCommand(t *testing.T) {
	raw, err := decodeJSONObject([]byte(`{"mcp":{"syosetu":{"type":"local",
	  "command":["uv","run","--quiet","{env:HOME}/repos/narou-mcp-stdio/narou_mcp.py"],
	  "environment":{"SYOSETU_MIN_INTERVAL":"0.7"},"enabled":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	sp := mcpreg.JSONEntrySpellings[session.KindOpencode]
	got, err := parseJSONServers(raw, sp)
	if err != nil {
		t.Fatal(err)
	}
	s := got["syosetu"]
	if s.Command != "uv" {
		t.Fatalf("command: %q", s.Command)
	}
	wantArgs := []string{"run", "--quiet", "{env:HOME}/repos/narou-mcp-stdio/narou_mcp.py"}
	if !reflect.DeepEqual(s.Args, wantArgs) {
		t.Fatalf("args: %+v", s.Args)
	}
	if !reflect.DeepEqual(s.Env, map[string]string{"SYOSETU_MIN_INTERVAL": "0.7"}) {
		t.Fatalf("env: %+v", s.Env)
	}
	if extra, ok := s.Extra["enabled"]; !ok || extra != true {
		t.Fatalf("extra should keep enabled: %+v", s.Extra)
	}
}

func TestParseJSONServersHTTP(t *testing.T) {
	raw, err := decodeJSONObject([]byte(`{"mcpServers":{"rem":{"type":"http","url":"https://example.com/mcp",
	  "headers":{"Authorization":"Bearer xyz"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	sp := mcpreg.JSONEntrySpellings[session.KindClaude]
	got, err := parseJSONServers(raw, sp)
	if err != nil {
		t.Fatal(err)
	}
	s := got["rem"]
	if s.Transport != TransportHTTP || s.URL != "https://example.com/mcp" {
		t.Fatalf("got %+v", s)
	}
	if s.Headers["Authorization"] != "Bearer xyz" {
		t.Fatalf("headers: %+v", s.Headers)
	}
}

func TestParseJSONServersCursorDiscriminatorFree(t *testing.T) {
	raw, err := decodeJSONObject([]byte(`{"mcpServers":{
	  "loc":{"command":"/bin/echo","args":["a"],"env":{"FOO":"bar"}},
	  "rem":{"url":"https://example.com","headers":{"Authorization":"Bearer x"}}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	sp := mcpreg.JSONEntrySpellings[session.KindCursor]
	got, err := parseJSONServers(raw, sp)
	if err != nil {
		t.Fatal(err)
	}
	if got["loc"].Transport != TransportStdio || got["loc"].Command != "/bin/echo" {
		t.Fatalf("loc: %+v", got["loc"])
	}
	if got["rem"].Transport != TransportHTTP || got["rem"].URL != "https://example.com" {
		t.Fatalf("rem: %+v", got["rem"])
	}
}

func TestDecodeJSONObjectRejectsNonObject(t *testing.T) {
	if _, err := decodeJSONObject([]byte(`[1,2,3]`)); err == nil {
		t.Fatal("expected error for a top-level array")
	}
	if _, err := decodeJSONObject([]byte(`not json`)); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
