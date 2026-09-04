package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The Runtime port must be constructed only through the profile-selected factory
// (docs/09, P3-7 stage 1). These tests lock the seam: the local profile builds a
// docker adapter that threads the workspace record through correctly, the ecs
// profile builds the AWS skeleton, and an unknown profile fails fast at boot
// rather than silently defaulting.
func TestNewRuntimeFactory(t *testing.T) {
	m := Config{
		Image:      "agent-fleet/workspace:test",
		AgentHost:  "127.0.0.1",
		Memory:     "1g",
		SessionCmd: "",
		// defaultTenantID unset: the test workspace is a non-default tenant, so
		// the re-basing keeps <slug>/<key>.
		RootDataDir: StaticRootDataDir("/srv/data", ""),
	}

	for _, profile := range []string{"", "local", "docker"} {
		f, err := NewFactory(profile, m)
		if err != nil {
			t.Fatalf("NewFactory(%q): unexpected error %v", profile, err)
		}
		if _, ok := f.(*dockerFactory); !ok {
			t.Fatalf("NewFactory(%q): got %T, want *dockerFactory", profile, f)
		}
	}

	for _, profile := range []string{"ecs", "aws"} {
		f, err := NewFactory(profile, m)
		if err != nil {
			t.Fatalf("NewFactory(%q): unexpected error %v", profile, err)
		}
		if _, ok := f.(*ecsFactory); !ok {
			t.Fatalf("NewFactory(%q): got %T, want *ecsFactory", profile, f)
		}
	}

	if _, err := NewFactory("kubernetes", m); err == nil {
		t.Fatal("NewFactory(unknown profile): expected error, got nil")
	}
}

// A resolved per-workspace RAM cap (Workspace.MemBytes) must reach the concrete
// adapter: docker as a raw --memory byte count, ECS as a valid Fargate task size
// (snapped up, CPU bumped when needed). MemBytes 0 falls back to the deployment
// default the factory captured.
func TestFactoryMemoryOverride(t *testing.T) {
	m := Config{Image: "img", AgentHost: "127.0.0.1", Memory: "1g", RootDataDir: StaticRootDataDir("/srv/data", "")}

	dockerF, _ := NewFactory("local", m)
	if d := dockerF.New(Workspace{ContainerName: "c", MemBytes: 2 * gib}, "", nil).(*dockerRuntime); d.memory != "2147483648" {
		t.Errorf("docker override: memory=%q, want 2147483648", d.memory)
	}
	if d := dockerF.New(Workspace{ContainerName: "c"}, "", nil).(*dockerRuntime); d.memory != "1g" {
		t.Errorf("docker default: memory=%q, want 1g", d.memory)
	}

	ecsF, _ := NewFactory("ecs", m) // cfg defaults: cpu 1024 / memory 2048
	if e := ecsF.New(Workspace{ContainerName: "c", MemBytes: 10 * gib}, "", nil).(*ecsRuntime); e.cpu != "2048" || e.memory != "10240" {
		t.Errorf("ecs override: cpu=%q memory=%q, want 2048/10240", e.cpu, e.memory)
	}
	if e := ecsF.New(Workspace{ContainerName: "c"}, "", nil).(*ecsRuntime); e.cpu != "1024" || e.memory != "2048" {
		t.Errorf("ecs default: cpu=%q memory=%q, want 1024/2048", e.cpu, e.memory)
	}
}

// The CPU and disk axes must reach the adapters the same way memory does: docker as
// --cpus (fractional cores, since it stores Fargate units), ECS as the task size plus
// ephemeral storage — and past Fargate's 200 GiB ephemeral ceiling as a managed EBS
// volume instead (ADR 0044 decision 2).
func TestFactoryCPUAndDiskOverride(t *testing.T) {
	m := Config{Image: "img", AgentHost: "127.0.0.1", Memory: "1g", RootDataDir: StaticRootDataDir("/srv/data", "")}

	dockerF, _ := NewFactory("local", m)
	if d := dockerF.New(Workspace{ContainerName: "c", CPUUnits: 2048}, "", nil).(*dockerRuntime); d.cpus != "2" {
		t.Errorf("docker cpus=%q, want 2", d.cpus)
	}
	if d := dockerF.New(Workspace{ContainerName: "c", CPUUnits: 512}, "", nil).(*dockerRuntime); d.cpus != "0.5" {
		t.Errorf("docker cpus=%q, want 0.5", d.cpus)
	}
	// Unset must stay unset: an empty --cpus is "every core", the pre-P1 behaviour.
	if d := dockerF.New(Workspace{ContainerName: "c"}, "", nil).(*dockerRuntime); d.cpus != "" {
		t.Errorf("docker default cpus=%q, want empty", d.cpus)
	}

	ecsF, _ := NewFactory("ecs", m) // cfg defaults: cpu 1024 / memory 2048
	// CPU alone still yields a VALID pair: 4 vCPU cannot run with the 2048 default.
	e := ecsF.New(Workspace{ContainerName: "c", CPUUnits: 4096}, "", nil).(*ecsRuntime)
	if e.cpu != "4096" || e.memory != "8192" {
		t.Errorf("ecs cpu-only: cpu=%q memory=%q, want 4096/8192", e.cpu, e.memory)
	}
	// Disk: the deployment default must land ABOVE the entrypoint's arming threshold
	// (AF_WS_SCRATCH_MIN_GB, 30 GiB), or the cache relocation of ADR 0044 decision 3 never
	// runs — which is exactly what shipping 0 here did.
	if e := ecsF.New(Workspace{ContainerName: "c"}, "", nil).(*ecsRuntime); int(e.diskGiB) != ecsDefaultWorkDiskGiB || e.ebsGiB != 0 {
		t.Errorf("ecs disk default: ephemeral=%d ebs=%d, want %d/0", e.diskGiB, e.ebsGiB, ecsDefaultWorkDiskGiB)
	}
	if ecsDefaultWorkDiskGiB <= 30 {
		t.Errorf("ecsDefaultWorkDiskGiB=%d must exceed the 30 GiB scratch threshold", ecsDefaultWorkDiskGiB)
	}
	// An explicit 0 is still "free tier": a deployment can opt out of paying for disk.
	t.Setenv("AF_ECS_WS_DISK_GB", "0")
	ecsFree, _ := NewFactory("ecs", m)
	if e := ecsFree.New(Workspace{ContainerName: "c"}, "", nil).(*ecsRuntime); e.diskGiB != 0 || e.ebsGiB != 0 {
		t.Errorf("ecs disk opt-out: ephemeral=%d ebs=%d, want 0/0", e.diskGiB, e.ebsGiB)
	}
	if e := ecsF.New(Workspace{ContainerName: "c", DiskGB: 60}, "", nil).(*ecsRuntime); e.diskGiB != 60 || e.ebsGiB != 0 {
		t.Errorf("ecs disk 60: ephemeral=%d ebs=%d, want 60/0", e.diskGiB, e.ebsGiB)
	}
	if e := ecsF.New(Workspace{ContainerName: "c", DiskGB: 500}, "", nil).(*ecsRuntime); e.diskGiB != 0 || e.ebsGiB != 500 {
		t.Errorf("ecs disk 500: ephemeral=%d ebs=%d, want 0/500", e.diskGiB, e.ebsGiB)
	}
}

// The stop grace drives docker stop -t AND the ECS stopTimeout from one env knob;
// the Agent budget must stay under it (safety margin) so the in-container graceful
// shutdown finishes before the runtime's SIGKILL. Clamps: >=1, <=120 (Fargate
// stopTimeout ceiling).
func TestStopGraceSec(t *testing.T) {
	cases := []struct {
		env         string
		want, agent int
	}{
		{"", 30, 25},      // default
		{"60", 60, 55},    // explicit
		{"300", 120, 115}, // clamped to the Fargate ceiling
		{"3", 3, 5},       // tiny grace: agent floor is 5 (runtime kills first; still bounded)
		{"0", 1, 5},       // nonsense → minimal but valid
	}
	for _, tc := range cases {
		t.Setenv("AF_STOP_GRACE_SEC", tc.env)
		if got := stopGraceSec(); got != tc.want {
			t.Errorf("AF_STOP_GRACE_SEC=%q: stopGraceSec = %d, want %d", tc.env, got, tc.want)
		}
		if got := agentStopGraceSec(); got != tc.agent {
			t.Errorf("AF_STOP_GRACE_SEC=%q: agentStopGraceSec = %d, want %d", tc.env, got, tc.agent)
		}
	}
}

// dockerFactory.New must thread the Workspace record and the per-call secretKey
// into the concrete dockerRuntime, and re-root the data dir via the manager's
// closure — otherwise a restored/moved deployment would mount the wrong home.
func TestDockerFactoryNew(t *testing.T) {
	m := Config{
		Image: "img:1", AgentHost: "127.0.0.1", Memory: "2g",
		RootDataDir: StaticRootDataDir("/srv/data", "T-default"),
	}
	f, err := NewFactory("local", m)
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

// Every adapter destroys (ADR 0045 decision 13-3), and the two local ones own exactly one
// thing beyond the container: the data directory that holds the home bind-mount. The
// ordering is the part worth asserting — unlinking a home out from under a live
// container is how you get a half-written home back on the next start.
func TestLocalAdaptersDestroyRemoveTheDataDir(t *testing.T) {
	t.Run("docker", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("home"), 0o600); err != nil {
			t.Fatal(err)
		}
		stopped := false
		d := &dockerRuntime{
			name: "af-ws-acme-alice", dataDir: dir,
			stopFn: func(context.Context) error {
				if _, err := os.Stat(filepath.Join(dir, "marker")); err != nil {
					t.Errorf("data dir was removed before the container stopped: %v", err)
				}
				stopped = true
				return nil
			},
		}
		leftovers, err := d.Destroy(context.Background())
		if err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		if !stopped {
			t.Error("Destroy did not stop the container first")
		}
		if len(leftovers) != 0 {
			t.Errorf("the local adapters leave nothing behind, got %v", leftovers)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("data dir survived Destroy: %v", err)
		}
	})

	t.Run("docker refuses to remove the home when the container will not stop", func(t *testing.T) {
		dir := t.TempDir()
		d := &dockerRuntime{
			name: "af-ws-acme-alice", dataDir: dir,
			stopFn: func(context.Context) error { return errors.New("docker stop: no such daemon") },
		}
		if _, err := d.Destroy(context.Background()); err == nil {
			t.Fatal("Destroy must fail when Stop fails")
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("data dir removed despite a failed Stop: %v", err)
		}
	})

	t.Run("native", func(t *testing.T) {
		dir := t.TempDir()
		// No pid file: Stop finds nothing to kill, which is the state a Destroy runs in.
		n := &nativeRuntime{name: "af-ws-acme-alice", dataDir: dir, rootfs: "x"}
		if _, err := n.Destroy(context.Background()); err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("data dir survived Destroy: %v", err)
		}
	})

	t.Run("an absent data dir is success, not an error", func(t *testing.T) {
		d := &dockerRuntime{
			name: "af-ws-acme-alice", dataDir: filepath.Join(t.TempDir(), "gone"),
			stopFn: func(context.Context) error { return nil },
		}
		if _, err := d.Destroy(context.Background()); err != nil {
			t.Errorf("re-running a partial Destroy must succeed: %v", err)
		}
	})
}
