package msp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
//   - the installed binary exports a different schema model than the bundle (the vendor
//     moved the protocol — a beta that went 1.2 to 1.3 in one month).
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

// TestInstalledBinaryExportsTheSameSchema is the lock the ADR names: the binary a deployment
// actually runs exports its own schema offline, so a protocol change is a red build instead of
// a decode failure on a member's turn. It skips where no binary is installed, which is every
// CI runner today and a Workspace that never ran `workspace-agent install-muse`.
func TestInstalledBinaryExportsTheSameSchema(t *testing.T) {
	bin := museBinary(t)
	out := t.TempDir()
	cmd := exec.Command(bin, "schema", "generate-json-schema", "--out", out)
	// Without this the vendor launcher may try to replace the binary mid-test, which is both
	// a network call and a silent version change under a version assertion.
	cmd.Env = append(os.Environ(), "MUSE_NO_AUTO_UPDATE=1")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("muse schema generate-json-schema: %v\n%s", err, b)
	}

	var man struct {
		Fingerprint   string `json:"fingerprint"`
		SchemaVersion int    `json:"schemaVersion"`
		Experimental  bool   `json:"experimental"`
	}
	b, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatalf("read exported manifest: %v", err)
	}
	if err := json.Unmarshal(b, &man); err != nil {
		t.Fatalf("decode exported manifest: %v", err)
	}
	if man.Fingerprint != SchemaFingerprint {
		t.Errorf("the installed binary speaks a different protocol\n"+
			"  binary:   %s\n  generated from: %s\n"+
			"Re-export the bundle into internal/msp/schema and run `go generate ./internal/msp/...`,"+
			" then re-read ADR 0095 for what moved.", man.Fingerprint, SchemaFingerprint)
	}
	if man.SchemaVersion != SchemaVersion {
		t.Errorf("exported schemaVersion = %d, generated from %d", man.SchemaVersion, SchemaVersion)
	}
	if man.Experimental {
		t.Error("the installed binary exports an experimental schema; these types are rendered from the stable surface")
	}
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
