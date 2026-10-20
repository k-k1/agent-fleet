package msp

import (
	"os"
	"strings"
	"testing"
)

func TestStaleMuseHint(t *testing.T) {
	df := "FROM x\nARG MUSE_VERSION=1.4.3-R5018.1\n"
	got := staleMuseHint(df, "Muse Code 1.4.2 (1.4.2-R4684.1)\n")
	if !strings.Contains(got, "1.4.2-R4684.1") || !strings.Contains(got, "1.4.3-R5018.1") || !strings.Contains(got, "install-muse") {
		t.Errorf("stale binary hint = %q", got)
	}
	if got := staleMuseHint(df, "Muse Code 1.4.3 (1.4.3-R5018.1)\n"); got != "" {
		t.Errorf("binary at the pin must give no hint, got %q", got)
	}
	if got := staleMuseHint("FROM x\n", "Muse Code 1.4.2 (1.4.2-R4684.1)\n"); got != "" {
		t.Errorf("no pin must give no hint, got %q", got)
	}
}

// The hint reads the real Dockerfile; a renamed ARG would silence it without anyone noticing.
func TestDockerfileDeclaresTheMusePin(t *testing.T) {
	df, err := os.ReadFile("../../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if dockerfileMusePin.FindStringSubmatch(string(df)) == nil {
		t.Error("workspace/Dockerfile no longer has `ARG MUSE_VERSION=`: museVersionHint would go silent")
	}
}

func TestMusePinStateRefusesWhatItCannotCompare(t *testing.T) {
	good := "Muse Code 1.4.3 (1.4.3-R5018.1)\n"
	pin := "FROM x\nARG MUSE_VERSION=1.4.3-R5018.1\n"
	if p, h, err := musePinState(pin, good); err != nil || p != h {
		t.Fatalf("matching: %q %q %v", p, h, err)
	}
	if p, h, err := musePinState(pin, "Muse Code 1.4.2 (1.4.2-R4684.1)\n"); err != nil || p == h {
		t.Fatalf("mismatch must compare as different without error: %q %q %v", p, h, err)
	}
	for name, c := range map[string][2]string{
		"no pin":         {"FROM x\n", good},
		"malformed pin":  {"ARG MUSE_VERSION=not-a-version\n", good},
		"no build id":    {pin, "Muse Code 1.4.3\n"},
		"malformed have": {pin, "Muse Code (garbage)\n"},
	} {
		if _, _, err := musePinState(c[0], c[1]); err == nil {
			t.Errorf("%s: want an error, the test would skip or run on a guess", name)
		}
	}
}

// A fake binary keeps the comparison independent of whichever muse the host has installed.
func TestInstalledMuseAgainstPinWithFakeBinaries(t *testing.T) {
	df, err := os.ReadFile("../../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	want := dockerfileMusePin.FindStringSubmatch(string(df))[1]
	fake := func(out string) string {
		p := t.TempDir() + "/muse"
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho '"+out+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if pin, have, err := installedMuseAgainstPin(fake("Muse Code x (" + want + ")")); err != nil || pin != have {
		t.Errorf("at the pin: %q %q %v", pin, have, err)
	}
	if pin, have, err := installedMuseAgainstPin(fake("Muse Code 1.0.0 (1.0.0-R1.1)")); err != nil || pin == have {
		t.Errorf("older: %q %q %v", pin, have, err)
	}
	if _, _, err := installedMuseAgainstPin(fake("garbage")); err == nil {
		t.Error("unreadable version output must be an error")
	}
	if _, _, err := installedMuseAgainstPin(t.TempDir() + "/missing"); err == nil {
		t.Error("a binary that cannot run must be an error")
	}
}
