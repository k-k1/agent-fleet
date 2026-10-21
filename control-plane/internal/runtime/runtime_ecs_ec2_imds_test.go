package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
		// The guard must already be loaded when the failing step runs.
		if r, w := strings.Index(out, "systemctl daemon-reload"), strings.Index(out, "sysctl -w"); r < 0 || w < 0 || r > w {
			t.Errorf("%s: daemon-reload must be recorded before sysctl -w:\n%s", tc.name, out)
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

// The management plane is given memory.min so that memory pressure reclaims the workspace's
// pages instead of theirs. The ECS agent is a docker container (its own scope, not a child
// of ecs.service), so af-protect-mgmt has to find and protect that scope; a drop-in on
// ecs.service alone protects only ecs-init. The protected total has to fit inside the
// smallest host reserve, and system.slice has to carry the sum for a child's floor to count.

// dropInMiB returns the MemoryMin of a systemd drop-in, requiring it to sit in the given
// section and to be positive. A line in the wrong section is ignored by systemd, and a zero
// floor protects nothing, so neither may pass.
func dropInMiB(content, section string) (int, error) {
	cur := ""
	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			cur = l
			continue
		}
		if m := regexp.MustCompile(`^MemoryMin=(\d+)M$`).FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[1])
			switch {
			case cur != section:
				return 0, fmt.Errorf("MemoryMin in %q, want %q", cur, section)
			case n <= 0:
				return 0, fmt.Errorf("MemoryMin=%dM protects nothing", n)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("no MemoryMin=<n>M")
}

func TestDropInMiBRejectsBrokenDropIns(t *testing.T) {
	if n, err := dropInMiB("[Service]\nMemoryMin=64M\n", "[Service]"); err != nil || n != 64 {
		t.Fatalf("good drop-in: %d, %v", n, err)
	}
	for name, c := range map[string]string{
		"wrong section": "[Unit]\nMemoryMin=64M\n",
		"zero floor":    "[Service]\nMemoryMin=0M\n",
		"missing":       "[Service]\nOOMScoreAdjust=-900\n",
	} {
		if _, err := dropInMiB(c, "[Service]"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSlotUserDataProtectsManagementPlaneMemory(t *testing.T) {
	// slotScript trims every line, so a line that fell out of the YAML block scalar
	// (and would break the whole template) is invisible to it: check the raw indent.
	raw, err := os.ReadFile("../../../deploy/aws/ecs/cfn/40-ec2-pool.yaml")
	if err != nil {
		t.Fatal(err)
	}
	in := false
	for i, l := range strings.Split(string(raw), "\n") {
		switch tl := strings.TrimSpace(l); {
		case tl == "# BEGIN memmin":
			in = true
		case tl == "# END memmin":
			in = false
		case in && tl != "" && !strings.HasPrefix(l, strings.Repeat(" ", 14)):
			t.Errorf("40-ec2-pool.yaml:%d leaves the UserData block scalar: %q", i+1, l)
		}
	}
	dir := t.TempDir()
	section := slotScript(t, "memmin")
	section = strings.ReplaceAll(section, "/etc/systemd/system", dir+"/systemd")
	section = strings.ReplaceAll(section, "/usr/local/sbin", dir+"/sbin")
	units := []string{"ecs", "amazon-ssm-agent", "docker", "containerd"}
	// The reload must find every drop-in already written.
	check := "for u in " + strings.Join(units, " ") + "; do test -s " + dir + "/systemd/$u.service.d/20-af-memmin.conf || exit 9; done; test -s " + dir + "/systemd/system.slice.d/20-af-memmin.conf || exit 9"
	out, err := runSlotShell(t, dir, "set -eu\n"+section, map[string]string{
		"systemctl": `[ "$1" != daemon-reload ] || { ` + check + `; }`,
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "systemctl daemon-reload") || !strings.Contains(out, "systemctl enable --now af-protect-mgmt.service") {
		t.Errorf("missing daemon-reload or enabling the protector:\n%s", out)
	}
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	sum := 0
	for _, u := range units {
		c := read("systemd/" + u + ".service.d/20-af-memmin.conf")
		n, err := dropInMiB(c, "[Service]")
		if err != nil {
			t.Errorf("%s: %v", u, err)
		}
		sum += n
		if (u == "ecs" || u == "amazon-ssm-agent") && !strings.Contains(c, "OOMScoreAdjust=-900") {
			t.Errorf("%s lacks OOMScoreAdjust=-900:\n%s", u, c)
		}
	}
	script := read("sbin/af-protect-mgmt")
	m := regexp.MustCompile(`(?m)^AGENT_MIN_MIB=(\d+)$`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("af-protect-mgmt has no AGENT_MIN_MIB")
	}
	agent, _ := strconv.Atoi(m[1])
	sum += agent
	if got, err := dropInMiB(read("systemd/system.slice.d/20-af-memmin.conf"), "[Slice]"); err != nil || got != sum {
		t.Errorf("system.slice MemoryMin=%dM (%v), want the sum %dM", got, err, sum)
	}
	if sum > ec2HostReserveFloorMiB {
		t.Errorf("protected %d MiB exceeds the host reserve floor %d MiB", sum, ec2HostReserveFloorMiB)
	}

	// Run the protector against a fake /proc and cgroup tree in which the agent lives in
	// its own docker scope, apart from ecs.service and the SSM agent.
	fake := filepath.Join(dir, "fake")
	procs := map[string][]string{ // cgroup -> pids, first is what docker / systemd report
		"/system.slice/ecs.service":              {"100"},
		"/system.slice/docker-abc.scope":         {"200", "201"}, // init + the real agent
		"/system.slice/amazon-ssm-agent.service": {"300", "301"}, // parent + existing worker
	}
	for cg, pids := range procs {
		if err := os.MkdirAll(filepath.Join(fake, "cg", cg), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(fake, "cg", cg, "cgroup.procs"), []byte(strings.Join(pids, "\n")+"\n"), 0o644)
		for _, pid := range pids {
			os.MkdirAll(filepath.Join(fake, "proc", pid), 0o755)
			os.WriteFile(filepath.Join(fake, "proc", pid, "cgroup"), []byte("0::"+cg+"\n"), 0o644)
			os.WriteFile(filepath.Join(fake, "proc", pid, "oom_score_adj"), []byte("0\n"), 0o644)
		}
	}
	pc := exec.Command("bash", filepath.Join(dir, "sbin/af-protect-mgmt"), "--once")
	pc.Env = append(os.Environ(), "AF_CGROUP_ROOT="+fake+"/cg", "AF_PROC="+fake+"/proc",
		"PATH="+filepath.Join(dir, "stubbin")+":"+os.Getenv("PATH"))
	for name, body := range map[string]string{
		"docker":    "echo 200",
		"systemctl": "echo 300",
	} {
		os.WriteFile(filepath.Join(dir, "stubbin", name), []byte("#!/bin/bash\n"+body+"\n"), 0o755)
	}
	if o, err := pc.CombinedOutput(); err != nil {
		t.Fatalf("af-protect-mgmt: %v\n%s", err, o)
	}
	got := func(rel string) string {
		b, _ := os.ReadFile(filepath.Join(fake, rel))
		return strings.TrimSpace(string(b))
	}
	if g, want := got("cg/system.slice/docker-abc.scope/memory.min"), strconv.Itoa(agent*1024*1024); g != want {
		t.Errorf("agent scope memory.min=%q, want %q", g, want)
	}
	if got("proc/100/oom_score_adj") != "0" {
		t.Error("ecs.service's own process was touched by the helper")
	}
	if got("cg/system.slice/ecs.service/memory.min") != "" {
		t.Error("memory.min was written to ecs.service's cgroup, not the agent's scope")
	}
	for _, pid := range []string{"200", "201", "300", "301"} {
		if g := got("proc/" + pid + "/oom_score_adj"); g != "-900" {
			t.Errorf("pid %s oom_score_adj=%q, want -900 on every process of the cgroup", pid, g)
		}
	}
}
