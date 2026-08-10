package mcpproj

import (
	"reflect"
	"sort"
	"testing"
)

func TestParseCodexServersStdio(t *testing.T) {
	src := `# a comment before anything
[projects."/home/dev/repos/x"]
trust_level = "trusted"

[mcp_servers.srv]
command = "/bin/echo"
args = ["a", "b"]
startup_timeout_sec = 12.0

[mcp_servers.srv.env]
FOO = "bar"
`
	got, err := parseCodexServers(src)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := got["srv"]
	if !ok {
		t.Fatalf("missing srv: %+v", got)
	}
	if s.Transport != TransportStdio || s.Command != "/bin/echo" {
		t.Fatalf("got %+v", s)
	}
	if !reflect.DeepEqual(s.Args, []string{"a", "b"}) {
		t.Fatalf("args: %+v", s.Args)
	}
	if !reflect.DeepEqual(s.Env, map[string]string{"FOO": "bar"}) {
		t.Fatalf("env: %+v", s.Env)
	}
	if s.Extra["startup_timeout_sec"] != 12.0 {
		t.Fatalf("extra: %+v", s.Extra)
	}
	// The [projects."…"] trust table must never be mistaken for a server.
	if len(got) != 1 {
		t.Fatalf("unexpected extra servers: %+v", got)
	}
}

func TestParseCodexServersHTTPHeaders(t *testing.T) {
	src := `[mcp_servers.rem]
url = "https://example.com/mcp"

[mcp_servers.rem.http_headers]
Authorization = "Bearer secret-token"
X-Custom = "v"
`
	got, err := parseCodexServers(src)
	if err != nil {
		t.Fatal(err)
	}
	s := got["rem"]
	if s.Transport != TransportHTTP || s.URL != "https://example.com/mcp" {
		t.Fatalf("got %+v", s)
	}
	if s.Headers["Authorization"] != "Bearer secret-token" || s.Headers["X-Custom"] != "v" {
		t.Fatalf("headers: %+v", s.Headers)
	}
}

func TestParseCodexServersEnvHTTPHeadersNamesOnly(t *testing.T) {
	src := `[mcp_servers.rem]
url = "https://example.com/mcp"
env_http_headers = { Authorization = "AF_MCP_TOKEN_1234" }
`
	got, err := parseCodexServers(src)
	if err != nil {
		t.Fatal(err)
	}
	s := got["rem"]
	if len(s.Headers) != 0 {
		t.Fatalf("env_http_headers must not appear as a resolved header value: %+v", s.Headers)
	}
	names, _ := s.Extra["env_http_headers"].([]string)
	if len(names) != 1 || names[0] != "Authorization" {
		t.Fatalf("extra env_http_headers: %+v", s.Extra)
	}
}

func TestParseCodexServersEscapedString(t *testing.T) {
	src := `[mcp_servers.srv]
command = "/bin/echo"
args = ["a \"quoted\" # not-a-comment", "b"]
`
	got, err := parseCodexServers(src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`a "quoted" # not-a-comment`, "b"}
	if !reflect.DeepEqual(got["srv"].Args, want) {
		t.Fatalf("got %q want %q", got["srv"].Args, want)
	}
}

func TestParseCodexServersMultipleSorted(t *testing.T) {
	src := `[mcp_servers.b]
command = "b"

[mcp_servers.a]
command = "a"
`
	got, err := parseCodexServers(src)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"a", "b"}) {
		t.Fatalf("got %v", names)
	}
}
