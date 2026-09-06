package resources

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Machine is what this container is RUNNING ON, as opposed to Stats, which is how much of
// it is being used right now. Stats rides the CP's 4-second tick; this changes only when
// the container is recreated, so it is its own route and is never merged into that stream.
//
// Same rule as Stats: an axis that cannot be read is OMITTED, never zeroed. "2 vCPU" and
// "could not tell" must not render as the same thing.
//
// What the CP is allowed to forward differs per field, and the Agent does not decide that
// — see MemTotal.
type Machine struct {
	// Arch is the CPU architecture, taken from the compiled binary rather than uname:
	// the Agent ships inside the workspace image, so its GOARCH IS the image's
	// architecture and cannot disagree with what the CLIs on PATH will run as.
	Arch string `json:"arch"`
	// VCPU is how many CPUs this container may run on (the affinity mask), which on a
	// runtime that sets no cpuset is the box's own count. It is a count, not a share:
	// CPUQuota is the share, and the two are independent.
	VCPU int `json:"vcpu,omitempty"`
	// CPUQuota is the cgroup bandwidth cap expressed in cores (1.5 = one and a half).
	// Absent = no quota, which is the case on every runtime the fleet ships today; it
	// exists so a deployment that does cap CPU does not silently show VCPU alone.
	CPUQuota *float64 `json:"cpu_quota,omitempty"`
	// MemMax is this container's own memory limit (cgroup), i.e. what a build may spend.
	MemMax *uint64 `json:"mem_max,omitempty"`
	// MemTotal is the BOX's RAM, read from /proc/meminfo — which is not namespaced, so it
	// is the host's figure whether or not the host belongs to this member.
	//
	// ⚠️ Only meaningful where a workspace has its box to itself (`ecs-ec2`, one slot per
	// member — ADR 0045 decision 8). On docker it is the shared fleet host, and printing
	// it as "your machine" would be someone else's number. The Agent cannot tell the
	// difference, so it reports the fact and the CP decides whether to forward it.
	MemTotal *uint64 `json:"mem_total,omitempty"`
	// DiskTotal is the capacity of the filesystem home sits on (statfs), which on
	// `ecs-ec2` is the persistent EBS volume itself.
	DiskTotal *uint64 `json:"disk_total,omitempty"`
	// InstanceType is the EC2 instance type this container is running on, and is present
	// only when the box could be CONFIRMED to be an EC2 instance (see instanceType).
	InstanceType string `json:"instance_type,omitempty"`
}

// ReadMachine returns what could be established about this container's machine.
func ReadMachine() Machine {
	m := Machine{Arch: runtime.GOARCH, VCPU: runtime.NumCPU()}
	if v, ok := memMax(); ok {
		m.MemMax = &v
	}
	if v, ok := cpuQuotaCores(); ok {
		m.CPUQuota = &v
	}
	if v, ok := hostMemTotal(); ok {
		m.MemTotal = &v
	}
	if _, total, ok := homeUsage(); ok {
		m.DiskTotal = &total
	}
	m.InstanceType = instanceType()
	return m
}

// --- CPU bandwidth ---

// cpuQuotaCores converts the cgroup bandwidth cap into cores. v2's cpu.max is
// "<quota|max> <period>" on one line; v1 splits it into two files and spells "no quota" as
// -1 (which is why it is read as a signed value — parsing it unsigned would produce a
// gigantic core count instead of "unlimited").
func cpuQuotaCores() (float64, bool) {
	if b, err := os.ReadFile(cgroupDir() + "/cpu.max"); err == nil {
		f := strings.Fields(string(b))
		if len(f) == 2 && f[0] != "max" {
			quota, err1 := strconv.ParseFloat(f[0], 64)
			period, err2 := strconv.ParseFloat(f[1], 64)
			if err1 == nil && err2 == nil && period > 0 && quota > 0 {
				return quota / period, true
			}
		}
		return 0, false
	}
	quota, ok1 := readCgroupInt("cpu/cpu.cfs_quota_us")
	period, ok2 := readCgroupInt("cpu/cpu.cfs_period_us")
	if !ok1 || !ok2 || quota <= 0 || period <= 0 {
		return 0, false
	}
	return float64(quota) / float64(period), true
}

func readCgroupInt(name string) (int64, bool) {
	b, err := os.ReadFile(cgroupDir() + "/" + name)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return v, err == nil
}

// --- the box ---

func meminfoPath() string {
	if v := os.Getenv("AF_MEMINFO_FILE"); v != "" {
		return v
	}
	return "/proc/meminfo"
}

// hostMemTotal reads MemTotal (in kB) out of /proc/meminfo. Deliberately not the cgroup:
// the cgroup limit is the workspace's share and is already MemMax — this is the box.
func hostMemTotal() (uint64, bool) {
	b, err := os.ReadFile(meminfoPath())
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "MemTotal:" {
			kb, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				return 0, false
			}
			return kb * 1024, true
		}
	}
	return 0, false
}

func dmiDir() string {
	if v := os.Getenv("AF_DMI_DIR"); v != "" {
		return v
	}
	return "/sys/devices/virtual/dmi/id"
}

// dmiEC2Vendor is what SMBIOS reports for an EC2 instance. It is the whole safety of this
// read: on a Nitro instance product_name holds the instance type ("m8g.large"), but on any
// other machine it holds that machine's model name, and there is nothing in the string
// itself to tell the two apart. Measured on a fleet host: sys_vendor "GMKtec",
// product_name "NucBox G11" — which would have been shown, verbatim, as an instance type.
const dmiEC2Vendor = "Amazon EC2"

// instanceType returns the EC2 instance type, or "" when this is not (provably) EC2.
//
// SMBIOS is read rather than IMDS on purpose: IMDSv2 needs a token round trip and a hop
// limit the deployment does not control, while /sys is already mounted in every container.
//
// ⚠️ Unverified on Graviton and on a real ECS host. Callers must therefore treat "" as
// "not measured", never as "not EC2" — the CP has a declared answer to fall back on.
func instanceType() string {
	if !strings.HasPrefix(dmiRead("sys_vendor"), dmiEC2Vendor) {
		return ""
	}
	t := dmiRead("product_name")
	// An instance type is "<family>.<size>". Anything else means SMBIOS is telling us
	// something we do not understand, and a label nobody can act on is worse than none.
	if !strings.Contains(t, ".") || strings.ContainsAny(t, " /") {
		return ""
	}
	return t
}

func dmiRead(name string) string {
	b, err := os.ReadFile(dmiDir() + "/" + name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
