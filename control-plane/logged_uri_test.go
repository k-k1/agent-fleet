package main

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

// The control plane redacts the same parameter names as the Agent's access log; the
// shared fixture keeps the two lists from drifting apart (the modules cannot import
// each other).
func TestSecretQueryKeysMatchFixture(t *testing.T) {
	b, err := os.ReadFile("../scripts/fixtures/secret-query-keys.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Fields(string(b))
	if len(want) != len(secretQueryKeys) {
		t.Fatalf("fixture has %d names, main.go has %d", len(want), len(secretQueryKeys))
	}
	for _, k := range want {
		if !secretQueryKeys[k] {
			t.Errorf("secretQueryKeys lacks %q", k)
		}
	}
}

// The app's query moves into "next" on the preview entry routes; none of it may reach the
// log, and a request path is logged in its escaped form.
func TestLoggedURI(t *testing.T) {
	const sec = "SECRETVALUE"
	for _, c := range []struct {
		raw, want string
	}{
		{"/preview-open-own?next=%2Fcb%3Fcode%3D" + sec + "%26a%3D1&port=3000", "/preview-open-own?next=%2Fcb%3F%3Credacted%3E&port=3000"},
		{"/preview-open?next=%2Fcb%3Fsig%3D" + sec + "&owner=u&port=3000", "/preview-open?next=%2Fcb%3F%3Credacted%3E&owner=u&port=3000"},
		{"/preview-open-own?next=%2Fplain&port=3000", "/preview-open-own?next=%2Fplain&port=3000"},
		{"/x%0Afake%20entry?code=" + sec, "/x%0Afake%20entry?code=%3Credacted%3E"},
		{"/api/oauth%0Ax/cb?code=" + sec, "/api/oauth%0Ax/cb?<redacted>"},
		{"/x%0Afake?a=%zz", "/x%0Afake?<redacted>"},
		{"/x%0Afake?a=1", "/x%0Afake?a=1"},
	} {
		u, err := url.ParseRequestURI(c.raw)
		if err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		got := loggedURI(u)
		if got != c.want {
			t.Errorf("loggedURI(%q) = %q, want %q", c.raw, got, c.want)
		}
		if strings.Contains(got, sec) || strings.ContainsAny(got, "\n\r") {
			t.Errorf("unsafe log form: %q", got)
		}
	}
	// The devIdP branch uses the escaped path too.
	u, _ := url.ParseRequestURI(devIdPPath + "/%0Ax?a=1")
	if got := loggedURI(u); strings.ContainsAny(got, "\n") || !strings.HasSuffix(got, "?<redacted>") {
		t.Errorf("devidp: %q", got)
	}
}
