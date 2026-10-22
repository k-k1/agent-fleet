package muse

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// muse takes its servers on the wire, not from a config file: an OAuth definition must not be
// on that list either until the relay exists.
func TestSessionMCPServersLeavesOutOAuthDefinitions(t *testing.T) {
	for name, auth := range map[string]string{"plainremote": "", "oauthremote": mcpreg.AuthOAuth} {
		if _, err := mcpreg.Create(mcpreg.ServerDef{Name: name, Transport: mcpreg.TransportHTTP, URL: "https://m.example.com/" + name,
			Auth: auth, Enabled: true, Targets: mcpreg.Targets{Session: true}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := sessionMCPServers("s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["oauthremote"]; ok {
		t.Error("the OAuth server is on muse's wire list")
	}
	if _, ok := got["plainremote"]; !ok {
		t.Error("the ordinary remote server went missing")
	}
}
