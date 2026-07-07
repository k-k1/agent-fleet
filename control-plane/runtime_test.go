package main

import "testing"

// The Runtime port must be constructed only through the profile-selected factory
// (docs/09, P3-7 段1). These tests lock the seam: the local profile builds a
// docker adapter that threads the workspace record through correctly, the ecs
// profile builds the AWS skeleton, and an unknown profile fails fast at boot
// rather than silently defaulting.
func TestNewRuntimeFactory(t *testing.T) {
	m := &manager{
		image:      "agent-fleet/workspace:test",
		agentHost:  "127.0.0.1",
		memory:     "1g",
		sessionCmd: "",
		dataRoot:   "/srv/data",
		// defaultTenantID unset: the test workspace is a non-default tenant, so
		// rootedDataDir keeps <slug>/<key>.
	}

	for _, profile := range []string{"", "local", "docker"} {
		f, err := newRuntimeFactory(profile, m)
		if err != nil {
			t.Fatalf("newRuntimeFactory(%q): unexpected error %v", profile, err)
		}
		if _, ok := f.(*dockerFactory); !ok {
			t.Fatalf("newRuntimeFactory(%q): got %T, want *dockerFactory", profile, f)
		}
	}

	for _, profile := range []string{"ecs", "aws"} {
		f, err := newRuntimeFactory(profile, m)
		if err != nil {
			t.Fatalf("newRuntimeFactory(%q): unexpected error %v", profile, err)
		}
		if _, ok := f.(*ecsFactory); !ok {
			t.Fatalf("newRuntimeFactory(%q): got %T, want *ecsFactory", profile, f)
		}
	}

	if _, err := newRuntimeFactory("kubernetes", m); err == nil {
		t.Fatal("newRuntimeFactory(unknown profile): expected error, got nil")
	}
}

// dockerFactory.New must thread the Workspace record and the per-call secretKey
// into the concrete dockerRuntime, and re-root the data dir via the manager's
// closure — otherwise a restored/moved deployment would mount the wrong home.
func TestDockerFactoryNew(t *testing.T) {
	m := &manager{
		image: "img:1", agentHost: "127.0.0.1", memory: "2g",
		dataRoot: "/srv/data", defaultTenantID: "T-default",
	}
	f, err := newRuntimeFactory("local", m)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	ws := Workspace{
		TenantID: "T-acme", ContainerName: "af-ws-acme-alice", Network: "af-net-acme-alice",
		DataDir: "/old/root/acme/alice", AgentPort: "7731", AgentToken: "tok-xyz",
	}
	rt, ok := f.New(ws, "dek-hex", []string{"AF_AGENT_SELF_UPDATE_ALLOWED=1"}).(*dockerRuntime)
	if !ok {
		t.Fatalf("New returned %T, want *dockerRuntime", f.New(ws, "dek-hex", nil))
	}
	// Per-workspace extraEnv is appended after the (empty here) template env.
	if len(rt.extraEnv) == 0 || rt.extraEnv[len(rt.extraEnv)-1] != "AF_AGENT_SELF_UPDATE_ALLOWED=1" {
		t.Errorf("extraEnv = %v, want it to include the per-workspace gate", rt.extraEnv)
	}
	if rt.name != ws.ContainerName || rt.network != ws.Network {
		t.Errorf("name/network = %q/%q, want %q/%q", rt.name, rt.network, ws.ContainerName, ws.Network)
	}
	if rt.agentPort != ws.AgentPort || rt.token != ws.AgentToken || rt.secretKey != "dek-hex" {
		t.Errorf("port/token/secretKey = %q/%q/%q, want %q/%q/dek-hex", rt.agentPort, rt.token, rt.secretKey, ws.AgentPort, ws.AgentToken)
	}
	if rt.image != "img:1" || rt.memory != "2g" || rt.agentHost != "127.0.0.1" {
		t.Errorf("template fields not carried: image=%q memory=%q host=%q", rt.image, rt.memory, rt.agentHost)
	}
	// non-default tenant: rootedDataDir keeps <slug>/<key> under the current root.
	if want := "/srv/data/acme/alice"; rt.dataDir != want {
		t.Errorf("dataDir = %q, want %q (re-rooted)", rt.dataDir, want)
	}
}
