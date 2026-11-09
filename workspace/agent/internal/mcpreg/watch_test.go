package mcpreg

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestStdioEnvIsPerDefinitionAndNeverTheAgentsEnvironment(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/m")
	t.Setenv("AF_SECRET_KEY", "store-key")
	t.Setenv("AGENT_TOKEN", "agent-token")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	member := ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio, Command: "x", Env: map[string]string{"WIKI_TOKEN": "w", "HOME": "/override"}}
	got := envMap(StdioEnv(member))
	for _, leak := range []string{"AF_SECRET_KEY", "AGENT_TOKEN", "AWS_SECRET_ACCESS_KEY"} {
		if _, ok := got[leak]; ok {
			t.Errorf("a member's server got %s", leak)
		}
	}
	if got["PATH"] != "/usr/bin" || got["WIKI_TOKEN"] != "w" || got["AWS_EC2_METADATA_DISABLED"] != "true" {
		t.Errorf("missing allowlisted, own or isolation variables: %v", got)
	}
	if got["HOME"] != "/override" {
		t.Errorf("the definition's own variable must win: %v", got["HOME"])
	}

	// A built-in keeps what ForwardEnvNames gives it.
	af := ServerDef{Name: "af", ID: BuiltinAF, Origin: OriginBuiltin, Transport: TransportStdio, Command: "x"}
	if g := envMap(StdioEnv(af)); g["AGENT_TOKEN"] != "agent-token" {
		t.Errorf("the built-in af server lost its token: %v", g)
	}
	ops := ServerDef{Name: "pd", ID: "pagerduty", Origin: OriginBuiltin, Transport: TransportStdio, Command: "x"}
	if g := envMap(StdioEnv(ops)); g["AF_SECRET_KEY"] != "store-key" {
		t.Errorf("an ops built-in lost the store key: %v", g)
	}
}

func TestNarrowEnvOnlyForWatchSources(t *testing.T) {
	if NarrowEnv(ServerDef{Origin: OriginUser}) {
		t.Error("an existing session-target server must keep the Agent's environment")
	}
	if !NarrowEnv(ServerDef{Origin: OriginUser, Targets: Targets{Watch: true}}) {
		t.Error("a watch source must be narrowed")
	}
	if NarrowEnv(ServerDef{Origin: OriginBuiltin, Targets: Targets{Watch: true}}) {
		t.Error("a built-in is never narrowed")
	}
}

func TestProbeNarrowsOnlyAWatchSource(t *testing.T) {
	t.Setenv("AF_SECRET_KEY", "store-key")
	out := t.TempDir() + "/env"
	d := ServerDef{Name: "w", Origin: OriginUser, Transport: TransportStdio, Command: "sh",
		Args: []string{"-c", "env > " + out + "; sleep 1"}}
	for _, watch := range []bool{false, true} {
		d.Targets.Watch = watch
		_ = os.Remove(out)
		Probe(t.Context(), withTimeout(d, 1500))
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if has := strings.Contains(string(b), "AF_SECRET_KEY=store-key"); has == watch {
			t.Errorf("watch=%v: Agent secret visible=%v", watch, has)
		}
	}
}

func withTimeout(d ServerDef, ms int) ServerDef { d.TimeoutMS = ms; return d }

func TestTenantSwitchIsOffUntilTheCPSaysTrue(t *testing.T) {
	withTempHome(t)
	if WatchSourcesAllowed() {
		t.Fatal("on by default")
	}
	// A CP that predates the switch sends no key.
	serveTenant(t, map[string]any{"servers": []ServerDef{tenantDef("wiki")}})
	if _, err := FetchTenant(); err != nil || WatchSourcesAllowed() {
		t.Fatalf("absent key must mean off: %v", err)
	}
	serveTenant(t, map[string]any{"servers": []ServerDef{tenantDef("wiki")}, "watchSources": true})
	if _, err := FetchTenant(); err != nil || !WatchSourcesAllowed() {
		t.Fatalf("not on: %v", err)
	}
	// Rewriting the server set keeps the switch.
	if err := writeTenantCache(nil, time.Now().Unix()); err != nil || !WatchSourcesAllowed() {
		t.Fatalf("a rewrite of the set cleared the switch: %v", err)
	}
	serveTenant(t, map[string]any{"servers": []ServerDef{tenantDef("wiki")}, "watchSources": false})
	if _, err := FetchTenant(); err != nil || WatchSourcesAllowed() {
		t.Fatalf("turning it off did not reach the Agent: %v", err)
	}
}

func TestTenantCannotDistributeAWatchSource(t *testing.T) {
	d := tenantDef("wiki")
	d.Targets.Watch = true
	kept, _ := acceptTenant([]ServerDef{d})
	if len(kept) != 1 || kept[0].Targets.Watch {
		t.Fatalf("a distributed definition kept Targets.Watch: %+v", kept)
	}
}

func TestWatchDefsAreTheMembersOwnReadyServers(t *testing.T) {
	withTempHome(t)
	mk := func(name string, watch bool) ServerDef {
		return ServerDef{Name: name, Transport: TransportHTTP, URL: "https://" + name + ".example/mcp", Enabled: true, Targets: Targets{Watch: watch}}
	}
	for _, d := range []ServerDef{mk("src", true), mk("plain", false)} {
		if _, err := Create(d); err != nil {
			t.Fatal(err)
		}
	}
	off := mk("off", true)
	off.Enabled = false
	if _, err := Create(off); err != nil {
		t.Fatal(err)
	}
	// A tenant row that claims Watch is not one (compose keeps the cache as written).
	tc := tenantCache{Servers: []ServerDef{{ID: "t1", Name: "tenantwatch", Enabled: true, Transport: TransportHTTP, URL: "https://t.example/mcp", Targets: Targets{Watch: true}}}}
	if err := writeTenantCache(tc.Servers, 1); err != nil {
		t.Fatal(err)
	}
	got, err := WatchDefs()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range got {
		names = append(names, d.Name)
	}
	if !slices.Equal(names, []string{"src"}) {
		t.Errorf("WatchDefs = %v", names)
	}
}
