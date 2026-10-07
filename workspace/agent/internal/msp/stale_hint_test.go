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
