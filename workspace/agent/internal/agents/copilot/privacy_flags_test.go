package copilot

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// pinVersion makes installedVersion answer v for bin without running it.
func pinVersion(t *testing.T, bin string, major, minor, patch int) {
	t.Helper()
	versionMu.Lock()
	prev, had := versionCache[bin]
	versionCache[bin] = versionEntry{v: [3]int{major, minor, patch}, ok: true, at: time.Now()}
	versionMu.Unlock()
	t.Cleanup(func() {
		versionMu.Lock()
		defer versionMu.Unlock()
		if had {
			versionCache[bin] = prev
		} else {
			delete(versionCache, bin)
		}
	})
}

// Each spelling is rejected by the other side of 1.0.103 and the CLI exits before drawing
// anything, so the boundary is exact.
func TestPrivacyFlagsFollowTheInstalledVersion(t *testing.T) {
	for _, c := range []struct {
		v    [3]int
		want []string
	}{
		{[3]int{1, 0, 102}, []string{"--no-remote", "--no-remote-export"}},
		{[3]int{1, 0, 103}, []string{"--sync-session=off"}},
		{[3]int{1, 1, 0}, []string{"--sync-session=off"}},
		{[3]int{0, 9, 999}, []string{"--no-remote", "--no-remote-export"}},
	} {
		bin := fmt.Sprintf("copilot-%d.%d.%d", c.v[0], c.v[1], c.v[2])
		pinVersion(t, bin, c.v[0], c.v[1], c.v[2])
		if got := privacyFlags(bin); !reflect.DeepEqual(got, c.want) {
			t.Errorf("privacyFlags(%v) = %v, want %v", c.v, got, c.want)
		}
	}
}

func TestInstalledVersionReadsTheVersionLine(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := write("old", "echo 'GitHub Copilot CLI 1.0.102.'; echo \"Run 'copilot update' to check for updates.\"")
	if got := privacyFlags(old); !reflect.DeepEqual(got, []string{"--no-remote", "--no-remote-export"}) {
		t.Errorf("1.0.102 binary: %v", got)
	}
	cur := write("cur", "echo 'GitHub Copilot CLI 1.0.103.'")
	if got := privacyFlags(cur); !reflect.DeepEqual(got, []string{"--sync-session=off"}) {
		t.Errorf("1.0.103 binary: %v", got)
	}
	// A binary whose version cannot be read gets the current spelling.
	broken := write("broken", "exit 1")
	if got := privacyFlags(broken); !reflect.DeepEqual(got, []string{"--sync-session=off"}) {
		t.Errorf("unreadable version: %v", got)
	}
	t.Cleanup(func() {
		versionMu.Lock()
		defer versionMu.Unlock()
		for _, b := range []string{old, cur, broken} {
			delete(versionCache, b)
		}
	})
}
