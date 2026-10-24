package mcpreg

import (
	"errors"
	"testing"
)

func TestSameMCPEndpoint(t *testing.T) {
	for _, c := range []struct {
		name, url, base string
		want            bool
	}{
		{"exact", "https://af.example.com/mcp", "https://af.example.com", true},
		{"trailing slash", "https://af.example.com/mcp/", "https://af.example.com/", true},
		{"default port", "https://af.example.com:443/mcp", "https://af.example.com", true},
		{"scheme and case", "http://AF.example.com/mcp", "https://af.example.com", true},
		{"internal with port", "http://cp.internal:8080/mcp", "http://cp.internal:8080", true},
		{"dot segment", "https://af.example.com/mcp/../mcp", "https://af.example.com", true},
		{"double slash", "https://af.example.com//mcp", "https://af.example.com", true},
		{"base prefix equal", "https://af.example.com/af/mcp", "https://af.example.com/af/", true},
		{"base prefix dot segment", "https://af.example.com/af/x/../mcp", "https://af.example.com/af", true},
		{"base prefix other path", "https://af.example.com/mcp", "https://af.example.com/af", false},
		{"dot segment escapes base", "https://af.example.com/af/../mcp", "https://af.example.com/af", false},
		{"absolute dns name", "https://af.example.com./mcp", "https://af.example.com", true},
		{"ipv6 spelling", "http://[0:0:0:0:0:0:0:1]:8080/mcp", "http://[::1]:8080", true},
		{"other ipv6", "http://[::2]:8080/mcp", "http://[::1]:8080", false},
		{"other host", "https://other.example.com/mcp", "https://af.example.com", false},
		{"lookalike host", "https://af.example.com.evil.test/mcp", "https://af.example.com", false},
		{"other port", "https://af.example.com:8443/mcp", "https://af.example.com", false},
		{"other path", "https://af.example.com/api", "https://af.example.com", false},
		{"unset base", "https://af.example.com/mcp", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := SameMCPEndpoint(c.url, c.base); got != c.want {
				t.Fatalf("SameMCPEndpoint(%q, %q) = %v, want %v", c.url, c.base, got, c.want)
			}
		})
	}
}

func TestCreateAndUpdateRefuseOwnMCP(t *testing.T) {
	withTempHome(t)
	t.Setenv("AF_CP_BASE_URL", "https://af.example.com/")
	t.Setenv("AF_CP_INTERNAL_URL", "http://cp.internal:8080")
	mk := func(u string) ServerDef {
		return ServerDef{Name: "x", Transport: TransportHTTP, URL: u, Enabled: true, Targets: Targets{Session: true}}
	}
	var ve *ValidationError
	for _, u := range []string{"https://af.example.com/mcp", "http://cp.internal:8080/mcp/"} {
		if _, err := Create(mk(u)); !errors.As(err, &ve) || ve.Code != CodeURLOwnMCP {
			t.Fatalf("Create(%s) = %v, want %s", u, err, CodeURLOwnMCP)
		}
	}
	d, err := Create(mk("https://other.example.com/mcp"))
	if err != nil {
		t.Fatalf("another deployment must pass: %v", err)
	}
	in := mk("https://af.example.com/mcp")
	if _, err := Update(d.ID, in); !errors.As(err, &ve) || ve.Code != CodeURLOwnMCP {
		t.Fatalf("Update to own endpoint = %v", err)
	}
	// A stored row keeps its URL on Update (rename), only a changed URL is checked.
	// An unset base refuses nothing.
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv("AF_CP_INTERNAL_URL", "")
	own := mk("https://af.example.com/mcp")
	own.Name = "y"
	if _, err := Create(own); err != nil {
		t.Fatalf("no CP base: %v", err)
	}
}
