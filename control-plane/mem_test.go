package main

import (
	"strconv"
	"testing"
)

func TestParseMemBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"", 0, false},
		{"nonsense", 0, false},
		{"1024", 1024, true},        // bare = bytes
		{"512m", 512 * mib, true},   //
		{"8g", 8 * gib, true},       //
		{"2G", 2 * gib, true},       // case-insensitive
		{"2 GiB", 2 * gib, true},    // spacing + ib suffix
		{"1t", gib * 1024, true},    //
		{"1536m", 1536 * mib, true}, // non-power-of-two
		{"1.5g", 3 * gib / 2, true}, // fractional
		{"-4g", 0, false},           // negative rejected
	}
	for _, c := range cases {
		got, ok := parseMemBytes(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseMemBytes(%q) = %d,%v; want %d,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestFormatMemHuman(t *testing.T) {
	cases := map[int64]string{
		8 * gib:    "8g",
		512 * mib:  "512m",
		1536 * mib: "1536m",
		1024:       "1k",
		1000:       "1000",
	}
	for in, want := range cases {
		if got := formatMemHuman(in); got != want {
			t.Errorf("formatMemHuman(%d) = %q; want %q", in, got, want)
		}
	}
}

func TestFargateSize(t *testing.T) {
	cases := []struct {
		bytes   int64
		baseCPU string
		cpu     string
		mem     string
	}{
		// 1.5 GiB with 256 base → smallest tier holding it is 256/2048.
		{3 * gib / 2, "256", "256", "2048"},
		// 3 GiB needs > 2048, so 512 tier (max 4096), snapped to 3072.
		{3 * gib, "256", "512", "3072"},
		// 10 GiB → 1024 tier maxes at 8192, so 2048 tier snapped to 10240.
		{10 * gib, "256", "2048", "10240"},
		// baseCPU floor respected: request fits in 256 but base is 1024.
		{gib, "1024", "1024", "2048"},
		// beyond Fargate ceiling → clamps to the largest tier.
		{500 * gib, "256", "16384", "122880"},
	}
	for _, c := range cases {
		cpu, mem := fargateSize(c.bytes, 0, c.baseCPU)
		if cpu != c.cpu || mem != c.mem {
			t.Errorf("fargateSize(%d,0,%s) = %s,%s; want %s,%s", c.bytes, c.baseCPU, cpu, mem, c.cpu, c.mem)
		}
	}
}

// Every (cpu, memory) pair fargateSize can emit must be one the ECS API actually
// accepts. The table these come from was measured against RegisterTaskDefinition
// (docs/63 §63.2), and the cases below are the ones the pre-measurement table got
// WRONG: it assumed a uniform 1024 MiB step, so a request landing in the 8 or 16 vCPU
// tier produced an invalid size and failed the whole Start.
func TestFargateSizeCoarseTierSteps(t *testing.T) {
	cases := []struct {
		bytes    int64
		cpu, mem string
	}{
		// 8 vCPU tier: 4096 MiB steps from 16384. 34 GiB is NOT a valid memory value.
		{34 * gib, "8192", "36864"},
		{31 * gib, "8192", "32768"}, // 4 vCPU tops out at 30720, so this lands in 8 vCPU
		{60 * gib, "8192", "61440"},
		// 16 vCPU tier: 8192 MiB steps from 32768.
		{61 * gib, "16384", "65536"},
		{100 * gib, "16384", "106496"},
	}
	for _, c := range cases {
		cpu, mem := fargateSize(c.bytes, 0, "256")
		if cpu != c.cpu || mem != c.mem {
			t.Errorf("fargateSize(%d GiB) = %s,%s; want %s,%s", c.bytes/gib, cpu, mem, c.cpu, c.mem)
		}
		assertValidFargateSize(t, cpu, mem)
	}
}

// An explicit CPU request is a floor on the tier, and because the bigger tiers have a
// memory MINIMUM, asking for more CPU can raise memory as well. That is the whole
// reason the two axes cannot be stored as one named size (ADR 0044 決定 1).
func TestFargateSizeExplicitCPU(t *testing.T) {
	cases := []struct {
		bytes    int64
		cpuUnits int
		baseCPU  string
		cpu, mem string
	}{
		// 8 GB stays 8 GB at 1 vCPU, but 4 vCPU forces its 8192 MiB minimum...
		{8 * gib, 1024, "256", "1024", "8192"},
		{8 * gib, 4096, "256", "4096", "8192"},
		// ...and 8 vCPU forces 16 GiB even though only 8 GB was asked for.
		{8 * gib, 8192, "256", "8192", "16384"},
		// The operator floor still wins when it is the larger of the two.
		{2 * gib, 256, "1024", "1024", "2048"},
		// CPU alone, memory unset by the caller (factory passes the deployment memory).
		{2 * gib, 2048, "256", "2048", "4096"},
	}
	for _, c := range cases {
		cpu, mem := fargateSize(c.bytes, c.cpuUnits, c.baseCPU)
		if cpu != c.cpu || mem != c.mem {
			t.Errorf("fargateSize(%d GiB, cpu %d, base %s) = %s,%s; want %s,%s",
				c.bytes/gib, c.cpuUnits, c.baseCPU, cpu, mem, c.cpu, c.mem)
		}
		assertValidFargateSize(t, cpu, mem)
	}
}

// assertValidFargateSize re-checks a pair against the measured matrix independently of
// the code under test, so a future edit to fargateTiers cannot quietly make the snapper
// emit sizes the API would reject.
func assertValidFargateSize(t *testing.T, cpu, mem string) {
	t.Helper()
	valid := map[string]struct{ min, max, step int64 }{
		"512": {1024, 4096, 1024}, "1024": {2048, 8192, 1024}, "2048": {4096, 16384, 1024},
		"4096": {8192, 30720, 1024}, "8192": {16384, 61440, 4096}, "16384": {32768, 122880, 8192},
	}
	if cpu == "256" {
		if mem != "512" && mem != "1024" && mem != "2048" {
			t.Errorf("invalid Fargate size 256/%s", mem)
		}
		return
	}
	v, ok := valid[cpu]
	if !ok {
		t.Fatalf("unknown Fargate cpu %q", cpu)
	}
	m, _ := strconv.ParseInt(mem, 10, 64)
	if m < v.min || m > v.max || (m-v.min)%v.step != 0 {
		t.Errorf("invalid Fargate size %s/%s (tier allows %d-%d step %d)", cpu, mem, v.min, v.max, v.step)
	}
}

// The working-disk axis maps onto what the task definition should carry: absent below
// 21 GiB (Fargate's free default, and the API rejects anything smaller), the size
// itself through 200, and "use managed EBS" above that.
func TestFargateDiskGiB(t *testing.T) {
	cases := []struct {
		req      int
		size     int32
		needsEBS bool
	}{
		{0, 0, false}, {20, 0, false}, {21, 21, false}, {50, 50, false},
		{200, 200, false}, {201, 0, true}, {1000, 0, true},
	}
	for _, c := range cases {
		size, ebs := fargateDiskGiB(c.req)
		if size != c.size || ebs != c.needsEBS {
			t.Errorf("fargateDiskGiB(%d) = %d,%v; want %d,%v", c.req, size, ebs, c.size, c.needsEBS)
		}
	}
}
