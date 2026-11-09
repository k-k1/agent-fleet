package mcpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// HandleServerTest takes the origin from the store alone (ADR 0133 §8): a body that claims to be a
// built-in must not get a built-in's treatment, and a stored built-in is tested as stored.
func TestServerTestOriginComesFromTheStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	var probed mcpreg.ServerDef
	old := probeServer
	probeServer = func(_ context.Context, d mcpreg.ServerDef) mcpreg.ProbeResult {
		probed = d
		return mcpreg.ProbeResult{OK: true}
	}
	t.Cleanup(func() { probeServer = old })
	post := func(body string) int {
		probed = mcpreg.ServerDef{}
		w := httptest.NewRecorder()
		HandleServerTest(w, httptest.NewRequest(http.MethodPost, "/mcp-servers/test", strings.NewReader(body)))
		return w.Code
	}

	// An unsaved definition that says it is a built-in is a member's.
	if c := post(`{"name":"x","origin":"builtin","id":"pagerduty","transport":"stdio","command":"/bin/sh"}`); c != 200 {
		t.Fatalf("status %d", c)
	}
	if probed.Origin != mcpreg.OriginUser {
		t.Errorf("origin = %q", probed.Origin)
	}
	if c := post(`{"name":"x","origin":"builtin","transport":"stdio","command":"/bin/sh"}`); c != 200 {
		t.Fatalf("status %d", c)
	}
	if probed.Origin != mcpreg.OriginUser {
		t.Errorf("origin without an id = %q", probed.Origin)
	}
	// A stored built-in is tested exactly as stored, whatever the body adds.
	sec, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	sec.PagerDuty = &secrets.PagerDutyCreds{APIKey: "k"}
	if err := sec.Save(); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"builtin", "user"} {
		if c := post(`{"id":"pagerduty","name":"pagerduty","origin":"` + origin + `","transport":"stdio","command":"/bin/sh","args":["-c","evil"]}`); c != 200 {
			t.Fatalf("status %d", c)
		}
		if probed.Origin != mcpreg.OriginBuiltin || probed.Command == "/bin/sh" || slices.Contains(probed.Args, "evil") {
			t.Errorf("a request changed a stored built-in (body origin %s): %+v", origin, probed)
		}
	}
	// A stored member's server keeps its stored origin when edited.
	d, err := mcpreg.Create(mcpreg.ServerDef{Name: "mine", Transport: mcpreg.TransportStdio, Command: "true", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if c := post(`{"id":"` + d.ID + `","name":"mine","origin":"builtin","transport":"stdio","command":"/bin/sh"}`); c != 200 {
		t.Fatalf("status %d", c)
	}
	if probed.Origin != mcpreg.OriginUser {
		t.Errorf("edit of a member's server: origin = %q", probed.Origin)
	}
}
