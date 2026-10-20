package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadBootInstallStatus(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.tsv")
	body := "claude\tfailed\tno_space\n" +
		"codex\tok\t\n" +
		"rtk\tskipped\n" +
		"agy\tfailed\tbogus\n" + // unknown reason degrades to other
		"cursor\tweird\t\n" + // unknown status is dropped
		"copilot\tfailed\tnetwork\r\n" +
		"claude\tfailed\tchecksum\n" + // later line wins
		"\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := bootInstallStatusPath
	bootInstallStatusPath = p
	t.Cleanup(func() { bootInstallStatusPath = old })

	got := readBootInstallStatus()
	want := map[string]bootInstall{
		"claude":  {"failed", "checksum"},
		"codex":   {"ok", ""},
		"rtk":     {"skipped", ""},
		"agy":     {"failed", "other"},
		"copilot": {"failed", "network"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %v, want %v", k, got[k], w)
		}
	}
}

func TestReadBootInstallStatusMissingIsUnknown(t *testing.T) {
	old := bootInstallStatusPath
	bootInstallStatusPath = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { bootInstallStatusPath = old })
	if got := readBootInstallStatus(); got != nil {
		t.Fatalf("a missing file must read as unknown (nil), got %v", got)
	}
}
