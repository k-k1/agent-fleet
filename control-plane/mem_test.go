package main

import "testing"

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
		cpu, mem := fargateSize(c.bytes, c.baseCPU)
		if cpu != c.cpu || mem != c.mem {
			t.Errorf("fargateSize(%d,%s) = %s,%s; want %s,%s", c.bytes, c.baseCPU, cpu, mem, c.cpu, c.mem)
		}
	}
}
