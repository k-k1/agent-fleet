package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeDocker records the docker CLI calls of the fence and answers from fail, keyed by a
// prefix of the joined arguments.
type fakeDocker struct {
	calls []string
	fail  map[string]string
	out   map[string]string
}

func (f *fakeDocker) run(_ context.Context, args ...string) ([]byte, error) {
	line := strings.Join(args, " ")
	f.calls = append(f.calls, line)
	for prefix, msg := range f.fail {
		if strings.HasPrefix(line, prefix) {
			return []byte(msg), errors.New("exit status 1")
		}
	}
	for prefix, out := range f.out {
		if strings.HasPrefix(line, prefix) {
			return []byte(out), nil
		}
	}
	return nil, nil
}

// fencedRuntime is a docker runtime whose network inspect answers internal ("" = no
// such network).
func fencedRuntime(gateway, internal string, f *fakeDocker) *dockerRuntime {
	return &dockerRuntime{
		name: "af-ws-u1", network: "af-net-u1", egressGateway: gateway, docker: f.run,
		inspect: func(_ context.Context, typ, _, _ string) string {
			if typ == "network" {
				return internal
			}
			return ""
		},
	}
}

func TestDockerFenceEnsureNetwork(t *testing.T) {
	const (
		create       = "network create af-net-u1"
		createFenced = "network create --internal --label agent-fleet.io/egress-fence=1 af-net-u1"
		connect      = "network connect af-net-u1 af-egress"
		list         = "network inspect -f {{range .Containers}}{{println .Name}}{{end}} af-net-u1"
		rm           = "network rm af-net-u1"
	)
	for _, tc := range []struct {
		name, gateway, internal string
		want                    []string
	}{
		{"unfenced, new", "", "", []string{create}},
		{"unfenced, present", "", "false", nil},
		{"fenced, new", "af-egress", "", []string{createFenced, connect}},
		{"fenced, present", "af-egress", "true", []string{connect}},
		// A network left in the other mode is never reused.
		{"fenced, plain network left over", "af-egress", "false",
			[]string{list, "network disconnect -f af-net-u1 old-gw", rm, createFenced, connect}},
		{"unfenced, internal network left over", "", "true",
			[]string{list, "network disconnect -f af-net-u1 old-gw", rm, create}},
	} {
		f := &fakeDocker{out: map[string]string{"network inspect": "old-gw\n"}}
		if err := fencedRuntime(tc.gateway, tc.internal, f).ensureNetwork(context.Background()); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !slices.Equal(f.calls, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, f.calls, tc.want)
		}
	}
}

// A fenced workspace without the gateway has no way out at all, so Start must fail
// rather than leave it running cut off. Already attached is the one benign refusal.
func TestDockerFenceGatewayAttachFailure(t *testing.T) {
	f := &fakeDocker{fail: map[string]string{"network connect": "Error response from daemon: No such container: af-egress"}}
	err := fencedRuntime("af-egress", "true", f).ensureNetwork(context.Background())
	if err == nil || !strings.Contains(err.Error(), "No such container") {
		t.Errorf("a missing gateway did not fail the start: %v", err)
	}
	for _, msg := range []string{
		"Error response from daemon: endpoint with name af-egress already exists in network af-net-u1",
		"Error response from daemon: Endpoint With Name af-egress Already Exists In Network af-net-u1",
	} {
		f = &fakeDocker{fail: map[string]string{"network connect": msg}}
		if err := fencedRuntime("af-egress", "true", f).ensureNetwork(context.Background()); err != nil {
			t.Errorf("an attached gateway failed the start (%q): %v", msg, err)
		}
	}
	// A network that cannot be replaced is not reused in the wrong mode.
	f = &fakeDocker{fail: map[string]string{"network rm": "network af-net-u1 has active endpoints"}}
	if err := fencedRuntime("af-egress", "false", f).ensureNetwork(context.Background()); err == nil {
		t.Error("a plain network that could not be removed was accepted for a fenced workspace")
	}
	if err := (&dockerRuntime{name: "af-ws-u1", egressGateway: "af-egress", docker: f.run}).ensureNetwork(context.Background()); err == nil {
		t.Error("a fenced workspace with no per-user network was accepted")
	}
}

func TestDockerFenceStopDetachesGateway(t *testing.T) {
	f := &fakeDocker{}
	fencedRuntime("af-egress", "true", f).removeNetwork(context.Background())
	want := []string{"network disconnect -f af-net-u1 af-egress", "network rm af-net-u1"}
	if !slices.Equal(f.calls, want) {
		t.Errorf("fenced: got %q, want %q", f.calls, want)
	}
	f = &fakeDocker{}
	fencedRuntime("", "false", f).removeNetwork(context.Background())
	if want := []string{"network rm af-net-u1"}; !slices.Equal(f.calls, want) {
		t.Errorf("unfenced: got %q, want %q", f.calls, want)
	}
}

// The CP reaches a fenced Agent by the address recorded at Start, and a container with a
// published port the old way. The file decides, so flipping the setting under a running
// workspace does not strand it.
func TestDockerFenceEndpoint(t *testing.T) {
	dir := t.TempDir()
	d := &dockerRuntime{
		name: "af-ws-u1", network: "af-net-u1", dataDir: dir, agentHost: "127.0.0.1", agentPort: "7712",
		egressGateway: "af-egress",
		inspect: func(_ context.Context, typ, ref, format string) string {
			if typ == "container" && ref == "af-ws-u1" && strings.Contains(format, `"af-net-u1"`) {
				return "172.20.0.2"
			}
			return ""
		},
	}
	if got := d.Endpoint(); got != "http://127.0.0.1:7712" {
		t.Errorf("before Start: %s", got)
	}
	if got := d.publishArgs(); got != nil {
		t.Errorf("a fenced container publishes %v", got)
	}
	if err := d.recordAgentAddr(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := d.Endpoint(); got != "http://172.20.0.2:7700" {
		t.Errorf("fenced: %s", got)
	}
	if fi, err := os.Stat(filepath.Join(dir, ".agent-addr")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("address file: %v %v", fi, err)
	}

	// The setting was turned off while the workspace runs: still the recorded address.
	d.egressGateway = ""
	if got := d.Endpoint(); got != "http://172.20.0.2:7700" {
		t.Errorf("after the fence was turned off: %s", got)
	}
	if got, want := d.publishArgs(), []string{"-p", "127.0.0.1:7712:7700"}; !slices.Equal(got, want) {
		t.Errorf("publishArgs = %v, want %v", got, want)
	}
	if err := d.recordAgentAddr(context.Background()); err != nil {
		t.Errorf("unfenced record: %v", err)
	}
	d.clearAgentAddr()
	if got := d.Endpoint(); got != "http://127.0.0.1:7712" {
		t.Errorf("after Stop: %s", got)
	}

	// Garbage in the file is not an address to dial.
	if err := os.WriteFile(filepath.Join(dir, ".agent-addr"), []byte("evil.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := d.Endpoint(); got != "http://127.0.0.1:7712" {
		t.Errorf("a non-address was used: %s", got)
	}
}

func TestDockerFenceRecordAgentAddrNeedsAnAddress(t *testing.T) {
	d := &dockerRuntime{
		name: "af-ws-u1", network: "af-net-u1", dataDir: t.TempDir(), egressGateway: "af-egress",
		inspect: func(context.Context, string, string, string) string { return "" },
	}
	if err := d.recordAgentAddr(context.Background()); err == nil {
		t.Error("a container with no address was recorded")
	}
	if d.fencedAgentAddr() != "" {
		t.Error("an address file was written")
	}
}

// A fenced workspace has no route to the host, so the CP's internal URL goes through the
// proxy like everything else: no NO_PROXY entry and no host mapping.
func TestDockerFenceKeepsInternalURLOnTheProxy(t *testing.T) {
	env := []string{
		"http_proxy=http://af-egress:3128", "https_proxy=http://af-egress:3128",
		"no_proxy=localhost", "NO_PROXY=localhost",
		"AF_CP_INTERNAL_URL=http://host.docker.internal:8098",
	}
	plain := &dockerRuntime{extraEnv: env}
	if plain.cpProxyBypass() == nil || plain.hostArgs() == nil {
		t.Fatal("the unfenced runtime no longer bypasses the proxy: this test would pass on nothing")
	}
	fenced := &dockerRuntime{extraEnv: env, egressGateway: "af-egress"}
	if got := fenced.cpProxyBypass(); got != nil {
		t.Errorf("cpProxyBypass = %v", got)
	}
	if got := fenced.hostArgs(); got != nil {
		t.Errorf("hostArgs = %v", got)
	}
}

func TestReattachDockerGateway(t *testing.T) {
	f := &fakeDocker{out: map[string]string{
		"network ls":               "af-net-u1\naf-net-u2\naf-net-u3\n",
		"inspect --type=container": "compose_default\naf-net-u2\n",
	}}
	if err := reattachDockerGateway(context.Background(), f.run, "af-egress"); err != nil {
		t.Fatal(err)
	}
	want := []string{"network connect af-net-u1 af-egress", "network connect af-net-u3 af-egress"}
	if got := f.calls[2:]; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if !strings.Contains(f.calls[0], "label=agent-fleet.io/egress-fence=1") {
		t.Errorf("the listing is not scoped to fenced networks: %s", f.calls[0])
	}

	// No fenced network: the gateway is not even looked at.
	f = &fakeDocker{}
	if err := reattachDockerGateway(context.Background(), f.run, "af-egress"); err != nil || len(f.calls) != 1 {
		t.Errorf("err %v, calls %q", err, f.calls)
	}

	f = &fakeDocker{fail: map[string]string{"network ls": "Cannot connect to the Docker daemon"}}
	if err := reattachDockerGateway(context.Background(), f.run, "af-egress"); err == nil || len(f.calls) != 1 {
		t.Errorf("a failed listing: err %v, calls %q", err, f.calls)
	}

	// A gateway that is gone is reported, and one failed attach does not stop the rest.
	f = &fakeDocker{out: map[string]string{"network ls": "af-net-u1\n"}, fail: map[string]string{"inspect": "No such container"}}
	if err := reattachDockerGateway(context.Background(), f.run, "af-egress"); err == nil {
		t.Error("a missing gateway was not reported")
	}
	f = &fakeDocker{
		out:  map[string]string{"network ls": "af-net-u1\naf-net-u2\n"},
		fail: map[string]string{"network connect af-net-u1": "boom"},
	}
	if err := reattachDockerGateway(context.Background(), f.run, "af-egress"); err == nil {
		t.Error("a failed attach was not reported")
	}
	if last := f.calls[len(f.calls)-1]; last != "network connect af-net-u2 af-egress" {
		t.Errorf("the pass stopped at the first failure: %q", f.calls)
	}
}

func TestValidateDockerFence(t *testing.T) {
	proxy := []string{"http_proxy=http://af-egress:3128"}
	for _, tc := range []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"off", Config{AgentHost: "host.docker.internal"}, true},
		{"on", Config{DockerEgressGateway: "af-egress", EgressProxyEnv: proxy, AgentHost: "127.0.0.1"}, true},
		{"on, name in another case", Config{DockerEgressGateway: "af-egress", AgentHost: "127.0.0.1",
			EgressProxyEnv: []string{"http_proxy=http://AF-Egress:3128"}}, true},
		{"on, localhost", Config{DockerEgressGateway: "af-egress", EgressProxyEnv: proxy, AgentHost: "localhost"}, true},
		{"no proxy address", Config{DockerEgressGateway: "af-egress", AgentHost: "127.0.0.1"}, false},
		{"proxy elsewhere", Config{DockerEgressGateway: "af-egress", AgentHost: "127.0.0.1",
			EgressProxyEnv: []string{"http_proxy=http://af-egress:3128", "HTTPS_PROXY=http://proxy.example.com:3128"}}, false},
		{"no_proxy is not a proxy address", Config{DockerEgressGateway: "af-egress", AgentHost: "127.0.0.1",
			EgressProxyEnv: append([]string{"no_proxy=localhost", "NO_PROXY=localhost"}, proxy...)}, true},
		{"bridged CP", Config{DockerEgressGateway: "af-egress", EgressProxyEnv: proxy, AgentHost: "host.docker.internal"}, false},
	} {
		if err := validateDockerFence(tc.cfg); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
	if _, err := NewFactory("local", Config{DockerEgressGateway: "af-egress", AgentHost: "127.0.0.1"}); err == nil {
		t.Error("NewFactory accepted a fence with no proxy address")
	}
}

// A CP killed between `docker run` and the address record leaves a running fenced
// container with no file. The next Start records it instead of calling that running.
func TestDockerFenceHealsMissingAddr(t *testing.T) {
	internal := "true"
	d := &dockerRuntime{
		name: "af-ws-u1", network: "af-net-u1", dataDir: t.TempDir(), agentHost: "127.0.0.1", agentPort: "7712",
		egressGateway: "af-egress",
		inspect: func(_ context.Context, typ, _, _ string) string {
			if typ == "network" {
				return internal
			}
			return "172.20.0.2"
		},
	}
	d.healAgentAddr(context.Background())
	if got := d.Endpoint(); got != "http://172.20.0.2:7700" {
		t.Errorf("healed endpoint: %s", got)
	}

	// A container started before the fence was turned on keeps its published port.
	d.clearAgentAddr()
	internal = "false"
	if d.healAgentAddr(context.Background()); d.fencedAgentAddr() != "" {
		t.Errorf("a plain-network container was given an address: %q", d.fencedAgentAddr())
	}
	internal = "true"
	d.egressGateway = ""
	if d.healAgentAddr(context.Background()); d.fencedAgentAddr() != "" {
		t.Errorf("the unfenced runtime recorded an address: %q", d.fencedAgentAddr())
	}
}

// Start on a running workspace has always succeeded. A heal that cannot learn the
// address (one empty inspect) must not turn it into an error.
func TestDockerFenceStartOnRunningSurvivesFailedHeal(t *testing.T) {
	d := &dockerRuntime{
		name: "af-ws-u1", network: "af-net-u1", dataDir: t.TempDir(), agentHost: "127.0.0.1", agentPort: "7712",
		egressGateway: "af-egress",
		inspect: func(_ context.Context, typ, _, format string) string {
			switch {
			case typ == "network":
				return "true"
			case format == "{{.State.Status}}":
				return "running"
			}
			return "" // the address lookup comes back empty
		},
	}
	if err := d.Start(context.Background()); err != nil {
		t.Errorf("Start on a running workspace: %v", err)
	}
	if d.fencedAgentAddr() != "" {
		t.Error("an address was recorded from an empty inspect")
	}
}

// An address on file for a container docker no longer has may be someone else's by now.
// State drops it; an inspect that merely failed does not.
func TestDockerFenceStateDropsStaleAddr(t *testing.T) {
	dir := t.TempDir()
	write := func() {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, ".agent-addr"), []byte("172.20.0.2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := &dockerRuntime{
		name: "af-ws-u1", network: "af-net-u1", dataDir: dir, agentHost: "127.0.0.1", agentPort: "7712",
		inspect: func(context.Context, string, string, string) string { return "" },
	}
	write()
	if got := d.State(context.Background()); got != "none" {
		t.Fatalf("State = %s", got)
	}
	if got := d.Endpoint(); got != "http://127.0.0.1:7712" {
		t.Errorf("the stale address survived: %s", got)
	}

	// The status probe came back empty but the container is there: keep the address.
	write()
	d.inspect = func(_ context.Context, _, _, format string) string {
		if format == "{{.Id}}" {
			return "0123abcd"
		}
		return ""
	}
	if got := d.State(context.Background()); got != "none" {
		t.Fatalf("State = %s", got)
	}
	if got := d.Endpoint(); got != "http://172.20.0.2:7700" {
		t.Errorf("a failed probe dropped the address: %s", got)
	}
}
