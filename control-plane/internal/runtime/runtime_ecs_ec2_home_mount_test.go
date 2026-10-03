package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// --- the slot scripts, run for real against a fake /proc and /sys ---

// slotScriptWorld is a directory standing in for one slot: a mountinfo file, a
// /sys/dev/block with the devices that exist, and shims for the commands the scripts call.
// The shims log every call and do to mountinfo what the real command would.
type slotScriptWorld struct {
	t   *testing.T
	dir string
}

const slotShimPrelude = `#!/bin/sh
MI="$FAKE/mountinfo"
echo "$(basename "$0") $*" >> "$FAKE/calls"
drop_top() { awk -v m="$1" '{ l[NR] = $0; if ($5 == m) last = NR } END { for (i = 1; i <= NR; i++) if (i != last) print l[i] }' "$MI" > "$MI.new" && mv "$MI.new" "$MI"; }
`

func newSlotScriptWorld(t *testing.T) *slotScriptWorld {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	w := &slotScriptWorld{t: t, dir: t.TempDir()}
	for _, d := range []string{"bin", "sysblock"} {
		if err := os.Mkdir(filepath.Join(w.dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w.write("mountinfo", "")
	shims := map[string]string{
		// Only the lazy form is expected from the scripts; anything else is logged and fails.
		"umount": `[ "$1" = -l ] || exit 1
drop_top "$2"`,
		"stat":     `[ ! -e "$FAKE/stat-eio" ]`,
		"af-mount": `exit 0`,
		"af-umount": `[ -e "$FAKE/af-umount-fails" ] && exit 1
[ -e "$FAKE/af-umount-stuck" ] || drop_top "$1"`,
	}
	for name, body := range shims {
		p := filepath.Join(w.dir, "bin", name)
		if err := os.WriteFile(p, []byte(slotShimPrelude+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func (w *slotScriptWorld) write(name, body string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.dir, name), []byte(body), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *slotScriptWorld) device(devt string) { w.write(filepath.Join("sysblock", devt), "") }

func (w *slotScriptWorld) mounts(lines ...string) {
	w.write("mountinfo", strings.Join(lines, "\n")+"\n")
}

func (w *slotScriptWorld) run(script string) (calls []string, ok bool) {
	w.t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "FAKE="+w.dir, "PATH="+filepath.Join(w.dir, "bin")+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		w.t.Fatalf("running the script: %v\n%s", err, out)
	}
	b, _ := os.ReadFile(filepath.Join(w.dir, "calls"))
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		// stat is an implementation detail of the liveness check, not an action.
		if l != "" && !strings.HasPrefix(l, "stat ") {
			calls = append(calls, l)
		}
	}
	return calls, err == nil
}

func (w *slotScriptWorld) mountCommand() string {
	return homeMountCommand("vol-1", "/af-home/M-1", filepath.Join(w.dir, "mountinfo"), filepath.Join(w.dir, "sysblock"))
}

func (w *slotScriptWorld) umountCommand() string {
	return homeUmountCommand("/af-home/M-1", filepath.Join(w.dir, "mountinfo"), filepath.Join(w.dir, "sysblock"))
}

// mountinfo lines. 259:x is an NVMe namespace; whether it exists is the sysblock dir's say.
const (
	miRoot      = "22 1 259:0 / / rw,noatime shared:1 - xfs /dev/nvme0n1p1 rw"
	miHomeDead  = "40 22 259:1 / /af-home/M-1 rw,noatime shared:20 - xfs /dev/nvme1n1 rw,nouuid"
	miHomeLive  = "41 40 259:2 / /af-home/M-1 rw,noatime shared:21 - xfs /dev/nvme2n1 rw,nouuid"
	miHomeLive2 = "42 41 259:2 / /af-home/M-1 rw,noatime shared:22 - xfs /dev/nvme2n1 rw,nouuid"
	miOtherDead = "43 22 259:3 / /af-home/M-2 rw,noatime - xfs /dev/nvme3n1 rw,nouuid"
	miHomeTmpfs = "44 22 0:50 / /af-home/M-1 rw - tmpfs tmpfs rw"
)

func equalCalls(got []string, want ...string) bool {
	return strings.Join(got, "|") == strings.Join(want, "|")
}

// The slot state both quarantined sandbox slots were in: the previous attach of this
// membership's home was detached while mounted, so a dead XFS mount sits at its path.
func TestHomeMountScriptClearsADeadMountFirst(t *testing.T) {
	w := newSlotScriptWorld(t)
	w.device("259:0")
	w.mounts(miRoot, miHomeDead)
	calls, ok := w.run(w.mountCommand())
	if !ok || !equalCalls(calls, "umount -l /af-home/M-1", "af-mount vol-1 /af-home/M-1 --mkfs") {
		t.Fatalf("ok=%v calls=%q, want the dead mount lazily unmounted, then af-mount", ok, calls)
	}
}

// A volume stacked on top of the dead one (the sandbox's second attach) does not hide it:
// both layers are dead once that volume is gone too.
func TestHomeMountScriptClearsStackedDeadMounts(t *testing.T) {
	w := newSlotScriptWorld(t)
	w.device("259:0")
	w.mounts(miRoot, miHomeDead, strings.Replace(miHomeLive, "259:2", "259:4", 1))
	calls, ok := w.run(w.mountCommand())
	if !ok || !equalCalls(calls, "umount -l /af-home/M-1", "umount -l /af-home/M-1", "af-mount vol-1 /af-home/M-1 --mkfs") {
		t.Fatalf("ok=%v calls=%q", ok, calls)
	}
}

// The device is still there but the filesystem has shut down: stat answers EIO.
func TestHomeMountScriptClearsAnUnreadableMount(t *testing.T) {
	w := newSlotScriptWorld(t)
	w.device("259:0")
	w.device("259:2")
	w.mounts(miRoot, miHomeLive)
	w.write("stat-eio", "")
	calls, _ := w.run(w.mountCommand())
	if !equalCalls(calls, "umount -l /af-home/M-1", "af-mount vol-1 /af-home/M-1 --mkfs") {
		t.Fatalf("calls=%q", calls)
	}
}

// What must never be touched: a live home (a retried mount finds it already there), a dead
// mount at somebody else's path, and anything that is not XFS.
func TestHomeMountScriptLeavesLiveAndForeignMountsAlone(t *testing.T) {
	for name, lines := range map[string][]string{
		"live home":         {miRoot, miHomeLive},
		"other membership":  {miRoot, miOtherDead},
		"not xfs":           {miRoot, miHomeTmpfs},
		"nothing mounted":   {miRoot},
		"dead root, ignore": {strings.Replace(miRoot, "259:0", "259:9", 1)},
	} {
		t.Run(name, func(t *testing.T) {
			w := newSlotScriptWorld(t)
			w.device("259:0")
			w.device("259:2")
			w.mounts(lines...)
			calls, ok := w.run(w.mountCommand())
			if !ok || !equalCalls(calls, "af-mount vol-1 /af-home/M-1 --mkfs") {
				t.Fatalf("ok=%v calls=%q, want af-mount alone", ok, calls)
			}
		})
	}
}

func TestHomeUmountScriptConfirmsNothingIsLeftMounted(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		flag  string
		ok    bool
		calls []string
	}{
		{"nothing mounted", []string{miRoot}, "", true, nil},
		{"one live mount", []string{miRoot, miHomeLive}, "", true,
			[]string{"af-umount /af-home/M-1"}},
		// Two concurrent mounts of the same home stack; one af-umount takes off one.
		{"stacked live mounts", []string{miRoot, miHomeLive, miHomeLive2}, "", true,
			[]string{"af-umount /af-home/M-1", "af-umount /af-home/M-1"}},
		// The sandbox's state when the release ran: live volume B on top of dead volume A.
		{"live over dead", []string{miRoot, miHomeDead, miHomeLive}, "", true,
			[]string{"af-umount /af-home/M-1", "umount -l /af-home/M-1"}},
		{"af-umount fails", []string{miRoot, miHomeLive}, "af-umount-fails", false,
			[]string{"af-umount /af-home/M-1"}},
		// An af-umount that reports success over a mount that is still there is the
		// "not mounted" answer the sandbox got; the detach must not follow it.
		{"af-umount claims success", []string{miRoot, miHomeLive}, "af-umount-stuck", false,
			[]string{"af-umount /af-home/M-1", "af-umount /af-home/M-1", "af-umount /af-home/M-1", "af-umount /af-home/M-1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newSlotScriptWorld(t)
			w.device("259:0")
			w.device("259:2")
			w.mounts(c.lines...)
			if c.flag != "" {
				w.write(c.flag, "")
			}
			calls, ok := w.run(w.umountCommand())
			if ok != c.ok || !equalCalls(calls, c.calls...) {
				t.Fatalf("ok=%v calls=%q, want ok=%v calls=%q", ok, calls, c.ok, c.calls)
			}
		})
	}
}

// Outside one plain directory under /af-home the scripts are the bare helper calls: the
// dead-mount pass unmounts things, so it gets no path it was not written for.
func TestHomeMountCommandsScopeToAHomePath(t *testing.T) {
	for _, mp := range []string{"/af-home/M-1/x", "/af-home/", "/af-home/..", "/srv/M-1", "/af-home/M 1", "/af-home/M-1;reboot"} {
		if got, want := homeMountCommand("vol-1", mp, slotMountInfo, slotSysBlock), "af-mount vol-1 "+mp+" --mkfs"; got != want {
			t.Errorf("mount %q = %q, want the bare helper", mp, got)
		}
		if got, want := homeUmountCommand(mp, slotMountInfo, slotSysBlock), "af-umount "+mp; got != want {
			t.Errorf("umount %q = %q, want the bare helper", mp, got)
		}
	}
	if got := homeMountCommand("vol-1", "/af-home/06ad4a2b-1c3d", slotMountInfo, slotSysBlock); !strings.Contains(got, "umount -l") {
		t.Errorf("a real membership path did not get the dead-mount pass: %q", got)
	}
}

// New slots get the same lines in their own af-mount; slots from an older template get
// them from the CP. Two copies of a recovery are one copy that drifts, so pin them.
func TestSlotUserDataAfMountCarriesTheDeadMountPass(t *testing.T) {
	b, err := os.ReadFile("../../../deploy/aws/ecs/cfn/40-ec2-pool.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)cat > /usr/local/bin/af-mount <<'SH'\n(.*?)\n\s*SH\n`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no af-mount heredoc in the slot user data")
	}
	var have []string
	for _, l := range strings.Split(string(m[1]), "\n") {
		have = append(have, strings.TrimSpace(l))
	}
	at := 0
	for _, l := range strings.Split(staleHomeMountScript, "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		for at < len(have) && have[at] != l {
			at++
		}
		if at == len(have) {
			t.Fatalf("af-mount in 40-ec2-pool.yaml is missing (or reorders) the line\n  %s\nof staleHomeMountScript", l)
		}
	}
	if !strings.Contains(string(m[1]), "MOUNTINFO=/proc/self/mountinfo SYSBLOCK=/sys/dev/block") {
		t.Error("af-mount does not point the dead-mount pass at the real /proc and /sys")
	}
}

// --- the CP side: who may mount while a release is under way ---

// The sandbox interleaving, forced: a launch's mount reaches the slot while a release is
// between its umount and its detach. The mount must wait for the detach, then find the
// home gone and leave the slot alone; it must never land in between.
func TestECSEC2ReleaseKeepsAMountOutUntilItsDetach(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	var started sync.Once
	mountErr := make(chan error, 1)
	h.ssmc.onSend = func(cmd string) {
		if strings.HasPrefix(ssmHelperLine(cmd), "af-umount") {
			started.Do(func() {
				go func() {
					mountErr <- h.rt.mountHome(ctx, ec2Placement{volumeID: "vol-1", instanceID: "i-hot"})
				}()
			})
		}
	}
	// The release's first wait (polling its umount) gives the racing mount every chance to
	// reach the slot first, which is what happened on the sandbox.
	var waited atomic.Bool
	h.rt.sleep = func(context.Context, time.Duration) error {
		if waited.CompareAndSwap(false, true) {
			for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
				h.ssmc.mu.Lock()
				n := len(h.ssmc.commands)
				h.ssmc.mu.Unlock()
				if n > 1 {
					break
				}
			}
		}
		return nil
	}

	if err := h.rt.releaseSlot(ctx); err != nil {
		t.Fatalf("releaseSlot: %v", err)
	}
	var err error
	select {
	case err = <-mountErr:
	case <-time.After(5 * time.Second):
		t.Fatal("the racing mount never finished")
	}
	h.ec2.mu.Lock()
	calls := append([]string(nil), h.ec2.calls...)
	h.ec2.mu.Unlock()
	umount, detach, mount := -1, -1, -1
	for i, c := range calls {
		switch {
		case strings.HasPrefix(c, "SSM af-umount") && umount < 0:
			umount = i
		case strings.HasPrefix(c, "DetachVolume") && detach < 0:
			detach = i
		case strings.HasPrefix(c, "SSM af-mount") && mount < 0:
			mount = i
		}
	}
	if mount >= 0 && mount < detach {
		t.Fatalf("a mount landed between the release's umount and its detach — the detach pulls a mounted filesystem: %q", calls)
	}
	if umount < 0 || detach < umount {
		t.Fatalf("umount=%d detach=%d in %q", umount, detach, calls)
	}
	if !errors.Is(err, errHomeLeftSlot) {
		t.Errorf("racing mount = %v, want errHomeLeftSlot (the home is gone, nothing to mount)", err)
	}
}

// A launch whose home a release took off in the meantime fails, and the slot stays a
// slot: quarantining it for the release's doing took healthy boxes out of the pool.
func TestECSEC2LaunchDoesNotQuarantineWhenTheHomeWasReleased(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.deferred) != 1 {
		t.Fatalf("deferred = %d, want the convergence handed off", len(h.deferred))
	}
	h.ec2.instances["i-new1"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
	h.ci.registered["i-new1"] = true
	// A release (the golden baker's teardown on the sandbox) runs while the slot boots:
	// once the background half has attached the home, it is taken straight off again.
	h.ec2.onAttach = func(string) {
		h.ec2.onAttach = nil
		h.ec2.mu.Lock()
		h.ec2.volumes["vol-1"].Attachments = nil
		h.ec2.mu.Unlock()
	}
	h.runDeferred(ctx)

	slot := h.ec2.instances["i-new1"]
	if got := ec2TagValue(slot.Tags, EC2TagRole); got == ec2RoleQuarantined {
		t.Fatalf("the slot was quarantined for a home that a release had taken off it")
	}
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "SSM af-mount") || strings.HasPrefix(c, "StopInstances") {
			t.Fatalf("%q: nothing may be mounted or stopped for a home that has left the slot; calls %q", c, h.ec2.calls)
		}
	}
}

// A failed af-mount can leave the home mounted; quarantine unmounts before it detaches.
func TestECSEC2QuarantineUnmountsBeforeDetach(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-bad", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-bad"] = true
	h.ssmc.fail["af-mount"] = true

	if err := h.rt.Start(ctx); err == nil {
		t.Fatal("Start returned nil although the home could not be mounted")
	}
	if got := ec2TagValue(h.ec2.instances["i-bad"].Tags, EC2TagRole); got != ec2RoleQuarantined {
		t.Fatalf("af-role = %q, want quarantined", got)
	}
	umount, detach := callIndex(h, "SSM af-umount"), callIndex(h, "DetachVolume")
	if umount < 0 || detach < 0 || umount > detach {
		t.Fatalf("want umount before detach, got umount=%d detach=%d in %q", umount, detach, h.ec2.calls)
	}
}
