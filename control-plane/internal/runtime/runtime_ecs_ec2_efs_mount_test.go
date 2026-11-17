package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// efsShims adds, to a slotScriptWorld, the commands the EFS scripts call. mount records
// the mount in the fake mountinfo the way the kernel would; umount (from the world) only
// honours the lazy form, so the plain attempt fails and the script has to fall back.
func efsShims(t *testing.T, w *slotScriptWorld, helperInstalled bool) {
	t.Helper()
	shims := map[string]string{
		"mkdir":   `exit 0`,
		"timeout": `shift; exec "$@"`,
		"dnf":     `exit 0`,
		"mount": `[ -e "$FAKE/mount-fails" ] && exit 32
for a in "$@"; do mp=$a; done
echo "90 22 0:60 / $mp rw,nosuid,nodev - nfs4 127.0.0.1:/ rw" >> "$MI"`,
	}
	if helperInstalled {
		shims["mount.efs"] = `exit 0`
	}
	for name, body := range shims {
		if err := os.WriteFile(filepath.Join(w.dir, "bin", name), []byte(slotShimPrelude+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func efsMountScript(t *testing.T, w *slotScriptWorld) string {
	t.Helper()
	cmd, err := efsMountCommand("fs-1", []string{"fsap-claude", "fsap-keep"}, "/af-efs/M-1", filepath.Join(w.dir, "mountinfo"))
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

const (
	efsMountClaude = "mount -t efs -o tls,accesspoint=fsap-claude,nosuid,nodev fs-1:/ /af-efs/M-1/fsap-claude"
	efsMountKeep   = "mount -t efs -o tls,accesspoint=fsap-keep,nosuid,nodev fs-1:/ /af-efs/M-1/fsap-keep"
	miEFSClaude    = "91 22 0:61 / /af-efs/M-1/fsap-claude rw,nosuid,nodev - nfs4 127.0.0.1:/ rw"
	miEFSKeep      = "92 22 0:62 / /af-efs/M-1/fsap-keep rw,nosuid,nodev - nfs4 127.0.0.1:/ rw"
	miEFSOther     = "93 22 0:63 / /af-efs/M-10/fsap-claude rw,nosuid,nodev - nfs4 127.0.0.1:/ rw"
)

// efsActions drops the calls that change nothing on the slot.
func efsActions(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "mkdir ") || strings.HasPrefix(c, "timeout ") {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Each access point is mounted over TLS, by id, nosuid and nodev, at a path that carries
// the id; the TLS and access point options are what the file-system policy admits.
func TestEFSMountScriptMountsEachAccessPoint(t *testing.T) {
	w := newSlotScriptWorld(t)
	efsShims(t, w, true)
	w.mounts(miRoot)
	calls, ok := w.run(efsMountScript(t, w))
	if got := efsActions(calls); !ok || !equalCalls(got, efsMountClaude, efsMountKeep) {
		t.Fatalf("ok=%v calls=%q", ok, got)
	}
}

// A retried Start, or a restart on a slot the member never left, finds them mounted.
func TestEFSMountScriptLeavesAMountedAccessPointAlone(t *testing.T) {
	w := newSlotScriptWorld(t)
	efsShims(t, w, true)
	w.mounts(miRoot, miEFSClaude, miEFSOther)
	calls, ok := w.run(efsMountScript(t, w))
	if got := efsActions(calls); !ok || !equalCalls(got, efsMountKeep) {
		t.Fatalf("ok=%v calls=%q, want only the missing one mounted", ok, got)
	}
}

// A slot booted from a launch template that predates the helper installs it first.
func TestEFSMountScriptInstallsTheHelperWhenMissing(t *testing.T) {
	w := newSlotScriptWorld(t)
	efsShims(t, w, false)
	if _, err := os.Stat("/sbin/mount.efs"); err == nil {
		t.Skip("this machine has the EFS mount helper")
	}
	w.mounts(miRoot)
	calls, ok := w.run(efsMountScript(t, w))
	if got := efsActions(calls); !ok || len(got) != 3 || !strings.HasPrefix(got[0], "dnf install ") {
		t.Fatalf("ok=%v calls=%q, want the install before the mounts", ok, got)
	}
}

// A mount that fails stops the script (the task must not start on an empty directory) and
// says where to look.
func TestEFSMountScriptFailsWhenAMountFails(t *testing.T) {
	w := newSlotScriptWorld(t)
	efsShims(t, w, true)
	w.mounts(miRoot)
	w.write("mount-fails", "")
	calls, ok := w.run(efsMountScript(t, w))
	if got := efsActions(calls); ok || !equalCalls(got, efsMountClaude) {
		t.Fatalf("ok=%v calls=%q, want a failure after the first mount", ok, got)
	}
}

// Only this member's mounts go, and a busy one is detached lazily. M-10 shares M-1's
// prefix as a string, which is the case a careless match would take with it.
func TestEFSUmountScriptTakesOffOnlyThisMembersMounts(t *testing.T) {
	w := newSlotScriptWorld(t)
	w.mounts(miRoot, miHomeLive, miEFSClaude, miEFSKeep, miEFSOther)
	cmd, err := efsUmountCommand("/af-efs/M-1", filepath.Join(w.dir, "mountinfo"))
	if err != nil {
		t.Fatal(err)
	}
	calls, ok := w.run(cmd)
	want := []string{
		"umount /af-efs/M-1/fsap-claude", "umount -l /af-efs/M-1/fsap-claude",
		"umount /af-efs/M-1/fsap-keep", "umount -l /af-efs/M-1/fsap-keep",
	}
	if !ok || !equalCalls(calls, want...) {
		t.Fatalf("ok=%v calls=%q, want %q", ok, calls, want)
	}
	left, _ := os.ReadFile(filepath.Join(w.dir, "mountinfo"))
	if !strings.Contains(string(left), "/af-efs/M-10/") || !strings.Contains(string(left), "/af-home/M-1") {
		t.Fatalf("another member's mount or the home was taken off:\n%s", left)
	}
}

// The scripts run as root on the slot, so nothing reaches them that would need quoting.
func TestEFSCommandsRefuseValuesTheyCannotCarry(t *testing.T) {
	for name, f := range map[string]func() (string, error){
		"file system": func() (string, error) {
			return efsMountCommand("fs-1;reboot", []string{"fsap-1"}, "/af-efs/M-1", slotMountInfo)
		},
		"access point": func() (string, error) {
			return efsMountCommand("fs-1", []string{"fsap-1 x"}, "/af-efs/M-1", slotMountInfo)
		},
		"empty id": func() (string, error) { return efsMountCommand("fs-1", []string{""}, "/af-efs/M-1", slotMountInfo) },
		"mount dir": func() (string, error) {
			return efsMountCommand("fs-1", []string{"fsap-1"}, "/af-efs/M-1/..", slotMountInfo)
		},
		"outside":     func() (string, error) { return efsUmountCommand("/af-home/M-1", slotMountInfo) },
		"base itself": func() (string, error) { return efsUmountCommand("/af-efs/", slotMountInfo) },
		"mountinfo":   func() (string, error) { return efsUmountCommand("/af-efs/M-1", "/proc/x'; reboot; '") },
		"mountinfo on a mount": func() (string, error) {
			return efsMountCommand("fs-1", []string{"fsap-1"}, "/af-efs/M-1", "/proc/x'; reboot; '")
		},
	} {
		if cmd, err := f(); err == nil {
			t.Errorf("%s: got a script, want a refusal: %q", name, cmd)
		}
	}
}

// The task is not an NFS client: its claude and keep volumes are binds of what the slot
// mounted, so the file system's security group need not admit the workspace group.
func TestECSEC2TaskBindsTheSlotsEFSMounts(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.ecs.regCalls) != 1 {
		t.Fatalf("registered %d task definitions, want 1", len(h.ecs.regCalls))
	}
	binds := map[string]string{}
	for _, v := range h.ecs.regCalls[0].Volumes {
		if v.EfsVolumeConfiguration != nil {
			t.Errorf("volume %s is an EFS volume: the task's own ENI would have to reach NFS", aws.ToString(v.Name))
		}
		if v.Host != nil {
			binds[aws.ToString(v.Name)] = aws.ToString(v.Host.SourcePath)
		}
	}
	var mounted string
	for _, s := range h.ssmc.scripts {
		if strings.Contains(s, "mount -t efs") {
			mounted = s
		}
	}
	for _, name := range []string{"claude", "keep"} {
		src := binds[name]
		if !strings.HasPrefix(src, "/af-efs/M-1/fsap-") {
			t.Errorf("volume %s binds %q, want a path under /af-efs/M-1", name, src)
			continue
		}
		if ap := filepath.Base(src); !strings.Contains(mounted, "mnt "+ap+" "+src+"\n") {
			t.Errorf("volume %s binds %s, which the slot script does not mount:\n%s", name, src, mounted)
		}
	}
	if binds["claude"] == binds["keep"] {
		t.Errorf("claude and keep bind the same path %q", binds["claude"])
	}
	if home, efs := callIndex(h, "SSM af-mount"), callIndex(h, "SSM set -eu"); home < 0 || efs < home {
		t.Fatalf("want the home mounted, then the access points; calls = %q", h.ec2.calls)
	}
}

// A slot that cannot mount the access points is not quarantined: the usual cause is the
// deployment's (the security group rule), and quarantining would cost one box per Start.
func TestECSEC2FailedEFSMountDoesNotQuarantineTheSlot(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	h.ssmc.fail["mount -t efs"] = true

	if err := h.rt.Start(ctx); err == nil {
		t.Fatal("Start returned nil although the access points could not be mounted")
	}
	if got := ec2TagValue(h.ec2.instances["i-hot"].Tags, EC2TagRole); got == ec2RoleQuarantined {
		t.Fatal("the slot was quarantined for a failed EFS mount")
	}
	for _, c := range h.ecs.updateCalls {
		if aws.ToInt32(c.DesiredCount) == 1 {
			t.Fatal("the service was raised although the access points are not mounted")
		}
	}
	for _, c := range h.ecs.createCalls {
		if aws.ToInt32(c.DesiredCount) == 1 {
			t.Fatal("the service was created at 1 although the access points are not mounted")
		}
	}
	if v := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); v != "" {
		t.Errorf("the claim %q is still on the home; the workspace would read starting until it expires", v)
	}
}

// An error or a log line names a script by its first line, not by all of it: the EFS mount
// script is some twenty lines, and it would otherwise be repeated in every line about it.
func TestSlotCommandLabelKeepsAScriptToOneLine(t *testing.T) {
	if got := slotCommandLabel("af-umount /af-home/M-1"); got != "af-umount /af-home/M-1" {
		t.Errorf("a one-line command became %q", got)
	}
	if got := slotCommandLabel("set -eu\nFS=fs-1\nmnt a b\n"); got != "set -eu … (+2 lines)" {
		t.Errorf("label = %q", got)
	}
	cmd, err := efsMountCommand("fs-1", []string{"fsap-claude", "fsap-keep"}, "/af-efs/M-1", slotMountInfo)
	if err != nil {
		t.Fatal(err)
	}
	if got := slotCommandLabel(cmd); strings.Contains(got, "\n") || strings.Contains(got, "fsap-") {
		t.Errorf("the label still carries the script: %q", got)
	}
}
