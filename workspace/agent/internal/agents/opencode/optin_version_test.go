package opencode

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestV2VersionFollowsRecordedPin(t *testing.T) {
	cases := []struct {
		name, json, want string
	}{
		{"recorded 2.x wins over the 1.x pin of a rollback", `{"opencode":"1.19.0","opencode_v2":"2.1.3"}`, "2.1.3"},
		{"no key (older image)", `{"opencode":"1.19.0"}`, V2FallbackVersion},
		{"a recorded 1.x is never handed to the opt-in", `{"opencode":"1.19.0","opencode_v2":"1.19.0"}`, V2FallbackVersion},
		{"a later major is never handed to the opt-in", `{"opencode":"1.19.0","opencode_v2":"3.0.0"}`, V2FallbackVersion},
		{"empty value", `{"opencode_v2":""}`, V2FallbackVersion},
		{"not json", `nope`, V2FallbackVersion},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := buildPinsPath
			buildPinsPath = filepath.Join(t.TempDir(), "versions.json")
			t.Cleanup(func() { buildPinsPath = old })
			if err := os.WriteFile(buildPinsPath, []byte(c.json), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := V2Version(); got != c.want {
				t.Fatalf("V2Version() = %q, want %q", got, c.want)
			}
		})
	}
	old := buildPinsPath
	buildPinsPath = filepath.Join(t.TempDir(), "missing.json")
	t.Cleanup(func() { buildPinsPath = old })
	if got := V2Version(); got != V2FallbackVersion {
		t.Fatalf("no versions.json: V2Version() = %q, want the fallback", got)
	}
}

// The image records the opt-in's version from OPENCODE_V2_VERSION. While OPENCODE_VERSION is
// 2.x the two must be equal, or a rollback built from this Dockerfile installs a release the
// contracts did not run on; cli-pin-bump.sh moves them together. A 1.x pin (a rollback)
// only requires that the V2 ARG is still a 2.x release (exactly major 2).
func TestDockerfileV2ArgFollowsOpencodePin(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	arg := func(name string) string {
		m := regexp.MustCompile(`(?m)^ARG ` + name + `=(\S+)$`).FindStringSubmatch(string(b))
		if m == nil {
			t.Fatalf("workspace/Dockerfile has no `ARG %s=`", name)
		}
		return m[1]
	}
	pin, v2 := arg("OPENCODE_VERSION"), arg("OPENCODE_V2_VERSION")
	if majorOf(v2) != 2 {
		t.Fatalf("OPENCODE_V2_VERSION=%s is not a 2.x release", v2)
	}
	if majorOf(pin) == 2 && pin != v2 {
		t.Fatalf("OPENCODE_V2_VERSION=%s differs from OPENCODE_VERSION=%s; move them together", v2, pin)
	}
	if !strings.Contains(string(b), `"opencode_v2":"%s"`) || !strings.Contains(string(b), `"$OPENCODE_V2_VERSION"`) {
		t.Fatal("the versions.json printf in workspace/Dockerfile does not record opencode_v2")
	}
	if majorOf(V2FallbackVersion) != 2 {
		t.Fatalf("V2FallbackVersion=%s is not a 2.x release", V2FallbackVersion)
	}
}
