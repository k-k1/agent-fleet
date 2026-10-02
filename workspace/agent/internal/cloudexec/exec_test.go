package cloudexec

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// /dev/null is a character device; a redirected run must not count as interactive.
func TestIsTerminalRejectsDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Fatal("/dev/null was treated as a terminal")
	}
}

// A backend scrubs by exact name and by prefix (ADR 0107 decision 2 removes every
// CLOUDSDK_*, GOOGLE_* and GCLOUD_* variable); SetEnv then leaves exactly one entry per
// key, because a child reads the first of a duplicated key.
func TestScrubAndSetEnv(t *testing.T) {
	env := []string{"PATH=/bin", "CLOUDSDK_CONFIG=/x", "GOOGLE_APPLICATION_CREDENTIALS=/k", "GCLOUDX=1",
		"AWS_PROFILE=p", "AWS_REGION=a", "AWS_REGION=b"}
	got := Scrub(env, DropPrefixes("CLOUDSDK_", "GOOGLE_"))
	got = Scrub(got, DropNames("AWS_PROFILE"))
	got = SetEnv(got, "AWS_REGION=c")
	want := []string{"PATH=/bin", "GCLOUDX=1", "AWS_REGION=c"}
	if !slices.Equal(got, want) {
		t.Fatalf("env = %v, want %v", got, want)
	}
	if EnvValue([]string{"K=1", "K=2"}, "K") != "1" || EnvHas([]string{"K="}, "K") || !EnvHas(got, "AWS_REGION") {
		t.Fatal("EnvValue/EnvHas do not read env the way getenv does")
	}
}

func TestRunDirIsPrivateAndPerRun(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "gcp-exec")
	a, err := RunDir(parent, "run-")
	if err != nil {
		t.Fatal(err)
	}
	b, err := RunDir(parent, "run-")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(parent)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("parent = %v %v, want 0700", fi, err)
	}
	if a == b || !strings.HasPrefix(a, parent) {
		t.Fatalf("run dirs %s and %s", a, b)
	}
	// A link in place of the parent is refused: the child's credentials would follow it.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := RunDir(link, "run-"); err == nil {
		t.Fatal("a symlinked parent was accepted")
	}
}
