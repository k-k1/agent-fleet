package mempressure

import (
	"os"
	"testing"
	"time"
)

func TestCompareMemoryMax(t *testing.T) {
	for _, c := range []struct {
		name, requested, memMax string
		want                    Agreement
	}{
		{"exact", "4294967296", "4294967296\n", AgreeMatch},
		{"kernel rounds down a page", "4294970000", "4294967296\n", AgreeMatch},
		{"within the largest page", "4294967296", "4294901760\n", AgreeMatch},
		{"half a GiB off", "4294967296", "3758096384\n", AgreeMismatch},
		{"unlimited cgroup, limit requested", "4294967296", "max\n", AgreeMismatch},
		{"not told", "", "4294967296\n", AgreeUnknown},
		{"not told, unlimited", "", "max\n", AgreeUnknown},
		{"garbage request", "4g", "4294967296\n", AgreeUnknown},
		{"zero request", "0", "max\n", AgreeUnknown},
		{"unreadable memory.max", "4294967296", "", AgreeUnknown},
		{"garbage memory.max", "4294967296", "lots", AgreeUnknown},
	} {
		got, detail := CompareMemoryMax(c.requested, c.memMax)
		if got != c.want || detail == "" {
			t.Errorf("%s: CompareMemoryMax(%q, %q) = %q (%q), want %q", c.name, c.requested, c.memMax, got, detail, c.want)
		}
	}
}

func TestWireCarriesMemMaxAgreement(t *testing.T) {
	for _, a := range []Agreement{AgreeMatch, AgreeMismatch, AgreeUnknown} {
		s := New("/x", Boundary{Reason: "host root", MemMax: a}, Sources{}, nil)
		if w := s.Wire(); w == nil || w.MemoryMaxAgreement != string(a) {
			t.Errorf("unverified boundary, %q: wire = %+v", a, w)
		}
	}
}

func TestVerifiedSamplerWireCarriesMemMaxAgreement(t *testing.T) {
	dir := fixture(t, fullFixture())
	c := &clock{time.Unix(1000, 0)}
	s := New(dir, Boundary{OK: true, MemMax: AgreeMismatch}, Sources{
		ReadFile: os.ReadFile,
		Now:      c.now,
		Probe:    func() (time.Duration, error) { return time.Millisecond, nil },
	}, nil)
	s.Tick()
	if w := s.Wire(); w == nil || !w.Available || w.MemoryMaxAgreement != "disagree" {
		t.Fatalf("wire = %+v", w)
	}
}

func TestCheckMemoryMaxReadsTheDirectoryOnlyWhenVerified(t *testing.T) {
	dir := fixture(t, map[string]string{"memory.max": "4294967296\n"})
	const req = "4294967296"
	if got, _ := CheckMemoryMax(Boundary{OK: true}, dir, req); got != AgreeMatch {
		t.Errorf("verified, matching file: %q", got)
	}
	if got, _ := CheckMemoryMax(Boundary{OK: true}, dir, "1073741824"); got != AgreeMismatch {
		t.Errorf("verified, different file: %q", got)
	}
	if got, _ := CheckMemoryMax(Boundary{OK: true}, dir+"/missing", req); got != AgreeUnknown {
		t.Errorf("verified, unreadable file: %q", got)
	}
	// Someone else's cgroup would agree here; it must not be reported.
	if got, d := CheckMemoryMax(Boundary{Reason: "host root"}, dir, req); got != AgreeUnknown || d == "" {
		t.Errorf("unverified boundary: %q (%q)", got, d)
	}
}
