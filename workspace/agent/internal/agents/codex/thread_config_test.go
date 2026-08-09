package codex

// threadConfig の組み立て。ここが壊れると managed セッションから MCP が消えるか、
// セッション名が届かず引き継ぎ提案が cwd 推定へ落ちる。

import (
	"errors"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

func stubSessionMCPDefs(t *testing.T, defs []mcpreg.ServerDef, err error) {
	t.Helper()
	old := sessionMCPDefs
	sessionMCPDefs = func() ([]mcpreg.ServerDef, error) { return defs, err }
	t.Cleanup(func() { sessionMCPDefs = old })
}

func afDef() mcpreg.ServerDef {
	return mcpreg.ServerDef{
		ID: mcpreg.BuiltinAF, Name: mcpreg.BuiltinAF, Origin: mcpreg.OriginBuiltin,
		Transport: mcpreg.TransportStdio, Command: "/usr/local/bin/workspace-agent",
		Args: []string{"mcp-stdio", "--self-report", "--chromium-attach"}, Enabled: true,
	}
}

func TestThreadConfigCarriesSessionNameToTheMCPChild(t *testing.T) {
	stubSessionMCPDefs(t, []mcpreg.ServerDef{afDef()}, nil)

	cfg := threadConfig("slot01")
	servers, ok := cfg["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("thread config has no mcp_servers: %v", cfg)
	}
	af, _ := servers[mcpreg.BuiltinAF].(map[string]any)
	env, _ := af["env"].(map[string]any)
	if env["AF_SESSION_NAME"] != "slot01" {
		t.Fatalf("af env = %v, want AF_SESSION_NAME=slot01", af["env"])
	}
	// The feature flag the questions path depends on must survive the restructuring.
	f, _ := cfg["features"].(map[string]any)
	if f["default_mode_request_user_input"] != true {
		t.Fatalf("features = %v, want default_mode_request_user_input — questions break without it", cfg["features"])
	}
}

// The failure mode that matters: a thread-local map REPLACES config.toml, so anything
// short of a complete answer must send NO map at all. Sending one built from an error
// or an empty registry would strip the user's MCP servers from every managed session.
func TestThreadConfigOmitsServersRatherThanSendingAPartialSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		defs []mcpreg.ServerDef
		err  error
		slot string
	}{
		{name: "registry unreadable", err: errors.New("store locked"), slot: "slot01"},
		{name: "no servers for codex", defs: nil, slot: "slot01"},
		{name: "session name unknown", defs: []mcpreg.ServerDef{afDef()}, slot: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubSessionMCPDefs(t, tc.defs, tc.err)
			cfg := threadConfig(tc.slot)
			if _, present := cfg["mcp_servers"]; present {
				t.Fatalf("mcp_servers = %v, want the key absent so the thread inherits config.toml",
					cfg["mcp_servers"])
			}
			if cfg["features"] == nil {
				t.Fatal("features dropped along with the servers")
			}
		})
	}
}

// threadFeatures is package state reused by every thread; building a config must not
// write through to it.
func TestThreadConfigDoesNotMutateSharedFeatures(t *testing.T) {
	stubSessionMCPDefs(t, []mcpreg.ServerDef{afDef()}, nil)
	before := len(threadFeatures)
	cfg := threadConfig("slot01")
	cfg["features"] = "clobbered"
	if len(threadFeatures) != before {
		t.Fatalf("threadFeatures mutated: %v", threadFeatures)
	}
	if again, _ := threadConfig("slot02")["features"].(map[string]any); again == nil {
		t.Fatal("a later thread lost its features")
	}
}
