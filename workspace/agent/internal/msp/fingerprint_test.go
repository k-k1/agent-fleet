package msp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/schemagen"
)

// The drift lock of ADR 0095. Three checks, each catching a different way the wire and these
// types can part company:
//
//   - the generated file is not what the generator produces from the checked-in bundle
//     (someone edited types_gen.go by hand, or replaced the bundle without regenerating);
//   - the constant the rest of the package compares against is not the bundle's own
//     fingerprint;
//   - the installed binary exports a schema these types cannot speak (the vendor moved the
//     protocol — a beta that went 1.2 to 1.3 in one month).
//
// Only the third needs a binary, and it skips without one, so the first two are what CI runs.

func TestGeneratedTypesMatchTheBundle(t *testing.T) {
	want, err := schemagen.Render(".")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got, err := os.ReadFile("types_gen.go")
	if err != nil {
		t.Fatalf("read types_gen.go: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("types_gen.go is stale: run `go generate ./internal/msp/...`\n"+
			"generated %d bytes, checked in %d bytes", len(want), len(got))
	}
}

// TestGeneratedTypesMatchTheBundle_negativeControl proves the check above can fail. Without
// it a generator that silently returned the file it was comparing against would look green.
func TestGeneratedTypesMatchTheBundle_negativeControl(t *testing.T) {
	dir := t.TempDir()
	copyBundleInto(t, dir)

	// Rename one property in one type. The wire name is what a real vendor change moves, and
	// it must travel into the generated source.
	path := filepath.Join(dir, "schema", "msp.schema.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(b), `"workspaceRoot"`, `"workspaceRootZZZ"`, 1)
	if mutated == string(b) {
		t.Fatal("mutation did not apply: the bundle no longer declares workspaceRoot")
	}
	if err := os.WriteFile(path, []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := schemagen.Render(dir)
	if err != nil {
		t.Fatalf("render mutated bundle: %v", err)
	}
	pristine, err := schemagen.Render(".")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == string(pristine) {
		t.Fatal("a renamed property produced identical output: the comparison above proves nothing")
	}
	if !strings.Contains(string(got), "workspaceRootZZZ") {
		t.Error("the renamed property did not reach the generated source")
	}
}

func TestSchemaFingerprintMatchesTheBundle(t *testing.T) {
	want, err := schemagen.Fingerprint(".")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if SchemaFingerprint != want {
		t.Errorf("SchemaFingerprint = %q, bundle manifest says %q", SchemaFingerprint, want)
	}
	if SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1 (MSP v1 is what this client speaks)", SchemaVersion)
	}
}

// TestInstalledBinaryIsCompatibleWithTheBundle is the lock the ADR names: the binary a
// deployment actually runs exports its own schema offline, so a protocol change is a red build
// instead of a decode failure on a member's turn. It skips where no binary is installed, which
// is every CI runner outside muse-contract.yml and a Workspace that never ran
// `workspace-agent install-muse`. It also skips a PATH binary that is not the Dockerfile pin: a
// home outlives its image, so the older muse that survives there reads the newer bundle as
// removals (measured: installed 1.4.7 against the 1.4.10 bundle) and the red says nothing about
// the types. AF_MUSE_BIN never skips on version, so muse-contract.yml still turns real drift red.
//
// It asks for compatibility, not an equal fingerprint: the fingerprint moves on every change to
// the vendor's model, including the additive ones these types survive (1.3.0 -> 1.4.0-R4161.1
// and R4161.1 -> R4302.1 were both additions only). schemagen.Compare holds the rules.
func TestInstalledBinaryIsCompatibleWithTheBundle(t *testing.T) {
	bin := museBinary(t)
	if os.Getenv("AF_MUSE_BIN") == "" {
		pin, have, err := installedMuseAgainstPin(bin)
		if err != nil {
			t.Fatalf("cannot compare the installed muse with the Dockerfile pin: %v", err)
		}
		if pin != have {
			t.Skipf("the installed muse is %s but the Dockerfile pin (the version the bundle was exported for) is %s;"+
				" run `workspace-agent install-muse` (or set AF_MUSE_BIN to check this binary regardless of the pin)", have, pin)
		}
	}
	out := t.TempDir()
	cmd := exec.Command(bin, "schema", "generate-json-schema", "--out", out)
	// Without this the vendor launcher may try to replace the binary mid-test, which is both
	// a network call and a silent version change under a version assertion.
	cmd.Env = append(os.Environ(), "MUSE_NO_AUTO_UPDATE=1")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("muse schema generate-json-schema: %v\n%s", err, b)
	}

	r, err := schemagen.Compare(".", out)
	if err != nil {
		t.Fatalf("compare with the exported schema: %v", err)
	}
	// muse-contract.yml turns these lines into notices, so an additive release is visible
	// without being red.
	for _, a := range r.Additions {
		t.Logf("msp-compat addition: %s", a)
	}
	if !r.Compatible() {
		t.Errorf("the installed binary speaks a protocol these types cannot:\n%s%s"+
			"Re-export the bundle into internal/msp/schema and run `go generate ./internal/msp/...`,"+
			" then re-read ADR 0095 for what moved.", r, museVersionHint(bin))
	}
}

var (
	dockerfileMusePin = regexp.MustCompile(`(?m)^ARG MUSE_VERSION=(\S+)`)
	museBuildID       = regexp.MustCompile(`\(([^()\s]+)\)`)
)

// museVersionHint says whether the red above is a stale binary or a real protocol move. The
// bundle is exported for the Dockerfile's pin, and a home that outlives an image keeps the
// older binary (a bundle newer than the binary reads as removals, ADR 0095 2026-10-06 note), so
// a binary that is not the pin is fixed by `workspace-agent install-muse`, not by touching the
// bundle. Empty when the binary is the pin or either side cannot be read.
func museVersionHint(bin string) string {
	df, err := os.ReadFile(filepath.Join("..", "..", "..", "Dockerfile"))
	if err != nil {
		return ""
	}
	cmd := exec.Command(bin, "--version")
	cmd.Env = append(os.Environ(), "MUSE_NO_AUTO_UPDATE=1")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return staleMuseHint(string(df), string(out))
}

// museBuildFormat is the shape of a build id, 1.4.10-R6427.1. A pin that does not match is a
// broken Dockerfile, not a version to compare against.
var museBuildFormat = regexp.MustCompile(`^\d+\.\d+\.\d+-R\d+\.\d+$`)

// installedMuseAgainstPin reads the Dockerfile pin and the installed build id. Anything it
// cannot read or recognise is an error: the skip decision must never be reachable through a
// missing or malformed pin.
func installedMuseAgainstPin(bin string) (pin, have string, err error) {
	df, err := os.ReadFile(filepath.Join("..", "..", "..", "Dockerfile"))
	if err != nil {
		return "", "", err
	}
	cmd := exec.Command(bin, "--version")
	cmd.Env = append(os.Environ(), "MUSE_NO_AUTO_UPDATE=1")
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("%s --version: %w", bin, err)
	}
	return musePinState(string(df), string(out))
}

func musePinState(dockerfile, versionOutput string) (pin, have string, err error) {
	p := dockerfileMusePin.FindStringSubmatch(dockerfile)
	if p == nil {
		return "", "", errors.New("the Dockerfile has no `ARG MUSE_VERSION=`")
	}
	h := museBuildID.FindStringSubmatch(versionOutput)
	if h == nil {
		return "", "", fmt.Errorf("no build id in `muse --version`: %q", versionOutput)
	}
	if !museBuildFormat.MatchString(p[1]) {
		return "", "", fmt.Errorf("the Dockerfile pin %q is not a build id like 1.4.10-R6427.1", p[1])
	}
	if !museBuildFormat.MatchString(h[1]) {
		return "", "", fmt.Errorf("the installed build id %q is not like 1.4.10-R6427.1", h[1])
	}
	return p[1], h[1], nil
}

func staleMuseHint(dockerfile, versionOutput string) string {
	pin := dockerfileMusePin.FindStringSubmatch(dockerfile)
	have := museBuildID.FindStringSubmatch(versionOutput)
	if pin == nil || have == nil || pin[1] == have[1] {
		return ""
	}
	return "note: the installed muse is " + have[1] + " but the Dockerfile pin (the version the bundle was" +
		" exported for) is " + pin[1] + "; run `workspace-agent install-muse` before changing the bundle.\n"
}

// museBinary resolves the binary the deployment installed, or skips. AF_MUSE_BIN is the
// escape hatch for a probe build that is deliberately outside PATH.
func museBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("AF_MUSE_BIN"); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("AF_MUSE_BIN=%s: %v", p, err)
		}
		return p
	}
	p, err := exec.LookPath("muse")
	if err != nil {
		t.Skip("no muse binary: set AF_MUSE_BIN or install one (workspace-agent install-muse)")
	}
	return p
}

func copyBundleInto(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "schema"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"msp.schema.json", "manifest.json"} {
		b, err := os.ReadFile(filepath.Join("schema", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "schema", name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
