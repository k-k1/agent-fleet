package resources

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeDMI writes an SMBIOS directory and points AF_DMI_DIR at it.
func fakeDMI(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AF_DMI_DIR", dir)
}

// The two DMI cases belong in one test on purpose. An implementation that always returns ""
// passes the negative case alone, so the positive control is what makes the negative one
// mean anything.
func TestInstanceTypeOnlyFromAConfirmedEC2Box(t *testing.T) {
	fakeDMI(t, map[string]string{"sys_vendor": "Amazon EC2\n", "product_name": "m8g.large\n"})
	if got := instanceType(); got != "m8g.large" {
		t.Errorf("instance_type = %q, want m8g.large (positive control: the reader must work at all)", got)
	}

	// The real values of a fleet host that is NOT EC2 (measured: a GMKtec mini PC). The
	// model name is in exactly the field an unguarded reader would print as an instance type.
	fakeDMI(t, map[string]string{"sys_vendor": "GMKtec\n", "product_name": "NucBox G11\n"})
	if got := instanceType(); got != "" {
		t.Errorf("instance_type = %q, want empty on a non-EC2 box", got)
	}

	// EC2 confirmed, but product_name is not a "<family>.<size>": rather than pass a
	// string nobody can act on to the screen, say nothing.
	fakeDMI(t, map[string]string{"sys_vendor": "Amazon EC2\n", "product_name": "HVM domU\n"})
	if got := instanceType(); got != "" {
		t.Errorf("instance_type = %q, want empty for a product_name that is not an instance type", got)
	}

	fakeDMI(t, nil) // no SMBIOS at all
	if got := instanceType(); got != "" {
		t.Errorf("instance_type = %q, want empty when DMI is unreadable", got)
	}
}

func TestMachineReadsCPUAndMemoryOfTheBox(t *testing.T) {
	fakeCgroup(t, map[string]string{
		"memory.max": "7516192768\n",
		"cpu.max":    "150000 100000\n",
	})
	meminfo := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(meminfo, []byte("MemTotal:        8174716 kB\nMemFree:  100 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_MEMINFO_FILE", meminfo)
	fakeDMI(t, map[string]string{"sys_vendor": "Amazon EC2\n", "product_name": "m8g.large\n"})

	m := ReadMachine()
	if m.Arch != runtime.GOARCH {
		t.Errorf("arch = %q, want %q", m.Arch, runtime.GOARCH)
	}
	if m.VCPU <= 0 {
		t.Errorf("vcpu = %d, want the affinity count", m.VCPU)
	}
	if m.MemMax == nil || *m.MemMax != 7516192768 {
		t.Errorf("mem_max = %v, want the cgroup limit 7516192768", m.MemMax)
	}
	// MemTotal is the BOX; MemMax is this container's share of it. Reporting one for the
	// other is the whole reason both are here.
	if m.MemTotal == nil || *m.MemTotal != 8174716*1024 {
		t.Errorf("mem_total = %v, want 8174716 kB in bytes", m.MemTotal)
	}
	if m.CPUQuota == nil || *m.CPUQuota != 1.5 {
		t.Errorf("cpu_quota = %v, want 1.5 cores (150000/100000)", m.CPUQuota)
	}
	if m.InstanceType != "m8g.large" {
		t.Errorf("instance_type = %q, want m8g.large", m.InstanceType)
	}
}

// An uncapped container ("max" in cpu.max, the shape every runtime the fleet ships uses)
// has no quota — the key must disappear rather than arrive as 0, which would read as "no
// CPU at all".
func TestUncappedCPUOmitsTheQuotaKey(t *testing.T) {
	fakeCgroup(t, map[string]string{"cpu.max": "max 100000\n"})
	if m := ReadMachine(); m.CPUQuota != nil {
		t.Errorf("cpu_quota = %v, want the key absent when uncapped", *m.CPUQuota)
	}
}

// cgroup v1 spells "no quota" as -1, which read as unsigned would become a colossal core
// count. The v1 side is fixture-only (no v1 host was provisioned); the v2 side above is
// what real hardware exercises.
func TestCgroupV1CPUQuota(t *testing.T) {
	fakeV1Cgroup(t, map[string]string{
		"cpu/cpu.cfs_quota_us":  "200000\n",
		"cpu/cpu.cfs_period_us": "100000\n",
	})
	if m := ReadMachine(); m.CPUQuota == nil || *m.CPUQuota != 2 {
		t.Errorf("cpu_quota = %v, want 2 cores from the v1 files", m.CPUQuota)
	}
	fakeV1Cgroup(t, map[string]string{
		"cpu/cpu.cfs_quota_us":  "-1\n",
		"cpu/cpu.cfs_period_us": "100000\n",
	})
	if m := ReadMachine(); m.CPUQuota != nil {
		t.Errorf("cpu_quota = %v, want the key absent for v1's -1 (unlimited)", *m.CPUQuota)
	}
}

// Nothing readable: everything the box could not tell us leaves the JSON entirely, so the
// CP can tell "not measured" from a zero it would have to draw.
func TestUnmeasurableMachineFieldsAreOmitted(t *testing.T) {
	fakeCgroup(t, nil)
	fakeDMI(t, nil)
	t.Setenv("AF_MEMINFO_FILE", filepath.Join(t.TempDir(), "absent"))

	b, err := json.Marshal(ReadMachine())
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cpu_quota", "mem_max", "mem_total", "instance_type"} {
		if v, ok := out[k]; ok {
			t.Errorf("%s = %v, want the key absent when unmeasurable", k, v)
		}
	}
	// arch and vcpu come from the binary itself, so they are always answerable.
	if out["arch"] != runtime.GOARCH {
		t.Errorf("arch = %v, want %q even with nothing else readable", out["arch"], runtime.GOARCH)
	}
}
