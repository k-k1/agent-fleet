package main

import (
	"os"
	"path/filepath"
	"testing"
)

// nvm dir with the given node versions installed.
func nvmHome(t *testing.T, versions ...string) string {
	t.Helper()
	home := t.TempDir()
	for _, v := range versions {
		if err := os.MkdirAll(filepath.Join(home, ".nvm", "versions", "node", v, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	return home
}

// The regression this file exists for: picking the newest installed patch used to be
// `sort.Strings` + last element, which is lexicographic. '2' < '9', so a single stale
// v22.9.x silently outranked v22.23.2 and pinned every session to the old node.
func TestNodeBinForPicksNumericallyHighestPatch(t *testing.T) {
	cases := []struct {
		name     string
		major    string
		have     []string
		wantLeaf string
	}{
		{
			// The case the old code got wrong. Both real: a live workspace held
			// v22.23.1 and v22.23.2; v22.9.0 is what an older boot would have left.
			name:     "double-digit minor beats single-digit",
			major:    "22",
			have:     []string{"v22.23.1", "v22.23.2", "v22.9.0"},
			wantLeaf: "v22.23.2",
		},
		{
			name:     "double-digit patch beats single-digit",
			major:    "20",
			have:     []string{"v20.1.9", "v20.1.10"},
			wantLeaf: "v20.1.10",
		},
		{
			name:     "other majors are never considered",
			major:    "18",
			have:     []string{"v18.20.4", "v20.99.99", "v22.23.2"},
			wantLeaf: "v18.20.4",
		},
		{
			name:     "single install",
			major:    "24",
			have:     []string{"v24.0.1"},
			wantLeaf: "v24.0.1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := nvmHome(t, tc.have...)
			got := nodeBinFor(tc.major)
			want := filepath.Join(home, ".nvm", "versions", "node", tc.wantLeaf, "bin")
			if got != want {
				t.Fatalf("nodeBinFor(%q) = %q, want %q", tc.major, got, want)
			}
		})
	}
}

// A major that is not installed must resolve to "" rather than to some other major —
// session launch never runs a network install, so "" is what makes the caller leave
// PATH alone instead of silently switching node.
func TestNodeBinForMissingMajorIsEmpty(t *testing.T) {
	nvmHome(t, "v22.23.2")
	if got := nodeBinFor("24"); got != "" {
		t.Fatalf("nodeBinFor(24) = %q, want empty", got)
	}
}

// nvm's directory also holds non-numeric entries (aliases such as "system"); they must
// never be chosen, and must not stop a real version from being found.
func TestNodeBinForIgnoresNonNumericDirs(t *testing.T) {
	home := nvmHome(t, "v22.23.2")
	if err := os.MkdirAll(filepath.Join(home, ".nvm", "versions", "node", "v22.x-alias", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".nvm", "versions", "node", "v22.23.2", "bin")
	if got := nodeBinFor("22"); got != want {
		t.Fatalf("nodeBinFor(22) = %q, want %q", got, want)
	}
}

func TestCompareDotted(t *testing.T) {
	cases := []struct {
		a, b []int
		want int
	}{
		{[]int{22, 23, 2}, []int{22, 9, 0}, 1},
		{[]int{22, 9, 0}, []int{22, 23, 2}, -1},
		{[]int{22, 23}, []int{22, 23, 1}, -1}, // shorter prefix sorts lower
		{[]int{22, 23, 1}, []int{22, 23, 1}, 0},
	}
	for _, c := range cases {
		if got := compareDotted(c.a, c.b); got != c.want {
			t.Errorf("compareDotted(%v,%v) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseDottedRejectsNonNumeric(t *testing.T) {
	if got := parseDotted("22.x.1"); got != nil {
		t.Fatalf("parseDotted(22.x.1) = %v, want nil", got)
	}
	if got := parseDotted("22.23.2"); len(got) != 3 || got[1] != 23 {
		t.Fatalf("parseDotted(22.23.2) = %v", got)
	}
}
