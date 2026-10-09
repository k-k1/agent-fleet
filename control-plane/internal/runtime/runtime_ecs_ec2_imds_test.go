package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A workspace task is awsvpc, and an awsvpc task's ENI reaches IMDS whatever the hop limit
// is, so without ECS_AWSVPC_BLOCK_IMDS every agent shell on a slot can take SlotRole's
// credentials. The ECS agent reads the setting from ecs.config, which the slot's user data
// writes: pin it there.
func TestSlotUserDataBlocksIMDSForTasks(t *testing.T) {
	b, err := os.ReadFile("../../../deploy/aws/ecs/cfn/40-ec2-pool.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ecsConfig := regexp.MustCompile(`(?s)cat >> /etc/ecs/ecs\.config <<EOF\n(.*?)\n\s*EOF\n`).FindSubmatch(b)
	if ecsConfig == nil {
		t.Fatal("no ecs.config heredoc in the slot user data")
	}
	if !regexp.MustCompile(`(?m)^\s*ECS_AWSVPC_BLOCK_IMDS=true\s*$`).Match(ecsConfig[1]) {
		t.Errorf("slot ecs.config does not set ECS_AWSVPC_BLOCK_IMDS=true:\n%s", ecsConfig[1])
	}
}

// slotScript returns the lines of the pool template between "# BEGIN <name>" and
// "# END <name>", dedented, so a test can run what the slot would run.
func slotScript(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../../deploy/aws/ecs/cfn/40-ec2-pool.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	in := false
	for _, l := range strings.Split(string(b), "\n") {
		switch tl := strings.TrimSpace(l); {
		case tl == "# BEGIN "+name:
			in = true
		case tl == "# END "+name:
			return strings.Join(out, "\n") + "\n"
		case in:
			out = append(out, tl)
		}
	}
	t.Fatalf("no BEGIN/END %s section in the pool template", name)
	return ""
}

// runSlotShell runs script under bash with stub commands first on PATH. Each stub is a
// shell body; every call is appended to calls.log in dir.
func runSlotShell(t *testing.T, dir, script string, stubs map[string]string) (string, error) {
	t.Helper()
	bin := filepath.Join(dir, "stubbin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range stubs {
		src := "#!/bin/bash\necho \"" + name + " $*\" >> " + dir + "/calls.log\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(src), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	return string(out) + "\n" + string(calls), err
}

// userns is the part of the slot user data that makes SYS_ADMIN safe to hand out. It is run
// here with the sysctl failing, and the ECS agent's guard with the value wrong, because a
// string match on the template cannot tell a command from a comment.
func usernsScript(t *testing.T, dir string) string {
	t.Helper()
	s := slotScript(t, "userns")
	for _, p := range []string{"/usr/local/sbin", "/etc/"} {
		s = strings.ReplaceAll(s, p, dir+p)
	}
	return s
}

func TestSlotUserDataUsernsFailsClosed(t *testing.T) {
	ok := map[string]string{"sysctl": "exit 0", "systemctl": "exit 0"}
	dir := t.TempDir()
	if out, err := runSlotShell(t, dir, usernsScript(t, dir), ok); err != nil {
		t.Fatalf("user data section failed with working sysctl: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "etc/sysctl.d/99-af-userns.conf")); strings.TrimSpace(string(b)) != "user.max_user_namespaces=0" {
		t.Errorf("sysctl.d file = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "etc/systemd/system/ecs.service.d/10-af-userns.conf")); !strings.Contains(string(b), "ExecStartPre="+dir+"/usr/local/sbin/af-require-no-userns") {
		t.Errorf("ecs.service drop-in = %q", b)
	}

	// A failing sysctl must end the script, not fall through to the ECS configuration.
	for _, tc := range []struct{ name, script string }{
		{"sysctl -w fails", usernsScript(t, dir)},
	} {
		d := t.TempDir()
		bad := map[string]string{"sysctl": "exit 1", "systemctl": "exit 0"}
		out, err := runSlotShell(t, d, strings.ReplaceAll(tc.script, dir, d)+"\necho REACHED-NEXT-STAGE\n", bad)
		if err == nil || strings.Contains(out, "REACHED-NEXT-STAGE") {
			t.Errorf("%s: the script went on (err=%v):\n%s", tc.name, err, out)
		}
	}
	// Persisting fails (the target directory cannot be written).
	d := t.TempDir()
	script := usernsScript(t, d)
	if err := os.MkdirAll(filepath.Join(d, "etc/sysctl.d/99-af-userns.conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := runSlotShell(t, d, script+"\necho REACHED-NEXT-STAGE\n", ok); err == nil || strings.Contains(out, "REACHED-NEXT-STAGE") {
		t.Errorf("persisting failed but the script went on (err=%v):\n%s", err, out)
	}
}

// The ECS agent unit runs this before every start, so a slot whose value is not 0 never
// joins the cluster, whichever way it came up.
func TestSlotUsernsGuardRefusesNonZero(t *testing.T) {
	dir := t.TempDir()
	if out, err := runSlotShell(t, dir, usernsScript(t, dir), map[string]string{"sysctl": "exit 0", "systemctl": "exit 0"}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	guard := filepath.Join(dir, "usr/local/sbin/af-require-no-userns")
	for val, wantErr := range map[string]bool{"0": false, "30741": true, "": true} {
		out, err := runSlotShell(t, dir, guard, map[string]string{"sysctl": "echo '" + val + "'"})
		if (err != nil) != wantErr {
			t.Errorf("guard with max_user_namespaces=%q: err=%v, want error=%v\n%s", val, err, wantErr, out)
		}
	}
	if _, err := runSlotShell(t, dir, guard, map[string]string{"sysctl": "exit 3"}); err == nil {
		t.Error("guard passed although sysctl itself failed")
	}
}

// Home mounts: a fresh mount carries nosuid,nodev, and an existing mount that lacks them
// is remounted rather than trusted. The CP holds no copy of the options (it only calls
// af-mount), so this is the one place they are pinned.
func TestSlotAfMountAppliesNosuidNodev(t *testing.T) {
	section := slotScript(t, "home-mount")
	for _, tc := range []struct {
		name, mountpoint, opts string
		want, notWant          string
	}{
		{"fresh mount", "exit 1", "", "mount -o nouuid,nosuid,nodev /dev/x /mp", "remount"},
		{"existing unprotected", "exit 0", "rw,noatime,nouuid", "mount -o remount,nosuid,nodev /mp", "nouuid,nosuid"},
		{"existing half protected", "exit 0", "rw,nosuid,noatime", "mount -o remount,nosuid,nodev /mp", "nouuid,nosuid"},
		{"existing protected", "exit 0", "rw,nosuid,nodev,noatime", "", "mount "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			out, err := runSlotShell(t, dir, "set -euo pipefail\nDEV=/dev/x MP=/mp\n"+section, map[string]string{
				"mountpoint": tc.mountpoint,
				"findmnt":    "echo '" + tc.opts + "'",
				"mount":      "exit 0",
			})
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if tc.want != "" && !strings.Contains(out, tc.want) {
				t.Errorf("missing %q in\n%s", tc.want, out)
			}
			if strings.Contains(out, "mount "+tc.notWant) && tc.notWant != "" || tc.notWant == "mount " && strings.Contains(out, "\nmount ") {
				t.Errorf("unexpected %q in\n%s", tc.notWant, out)
			}
		})
	}
}

// The compose host runs workspaces on docker bridges next to a host-network Control Plane.
// Hop limit 1 is what keeps a bridge container (two hops) off an instance profile the CP
// (one hop) may be given; IMDSv1 has no hop limit at all, so tokens must be required.
func TestSingleVMBlocksIMDSForBridgeContainers(t *testing.T) {
	b, err := os.ReadFile("../../../deploy/aws/ec2-single/cfn.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^\s+MetadataOptions:\s*\{\s*HttpTokens:\s*required,\s*HttpPutResponseHopLimit:\s*1\s*\}`).Match(b) {
		t.Error("ec2-single instance must declare MetadataOptions { HttpTokens: required, HttpPutResponseHopLimit: 1 }")
	}
}
