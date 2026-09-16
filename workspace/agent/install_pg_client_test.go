package main

import (
	"archive/tar"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseDebPackages verifies the Packages stanza parser.
func TestParseDebPackages(t *testing.T) {
	fixture := `Package: postgresql-client-17
Version: 17.11-0+deb13u1
Architecture: amd64
Filename: pool/main/p/postgresql-17/postgresql-client-17_17.11-0+deb13u1_amd64.deb
Size: 2071508
SHA256: 9d8558f8dd57c8e92e218a20698383575d53742ca3f9e7c2b7fe5f246d5216ae

Package: libpq5
Version: 17.11-0+deb13u1
Architecture: amd64
Filename: pool/main/p/postgresql-17/libpq5_17.11-0+deb13u1_amd64.deb
Size: 237376
SHA256: 20a4c9ef58b4baf90deda67cfb2cc062871c062830dddc234d28f5ac7931b86b

`
	pkgs := parseDebPackages(fixture)

	want := map[string]debPkg{
		"postgresql-client-17": {
			Name:     "postgresql-client-17",
			Version:  "17.11-0+deb13u1",
			Filename: "pool/main/p/postgresql-17/postgresql-client-17_17.11-0+deb13u1_amd64.deb",
			SHA256:   "9d8558f8dd57c8e92e218a20698383575d53742ca3f9e7c2b7fe5f246d5216ae",
		},
		"libpq5": {
			Name:     "libpq5",
			Version:  "17.11-0+deb13u1",
			Filename: "pool/main/p/postgresql-17/libpq5_17.11-0+deb13u1_amd64.deb",
			SHA256:   "20a4c9ef58b4baf90deda67cfb2cc062871c062830dddc234d28f5ac7931b86b",
		},
	}

	for name, wp := range want {
		got, ok := pkgs[name]
		if !ok {
			t.Errorf("package %q not found", name)
			continue
		}
		if got.Version != wp.Version {
			t.Errorf("%s: Version = %q, want %q", name, got.Version, wp.Version)
		}
		if got.Filename != wp.Filename {
			t.Errorf("%s: Filename = %q, want %q", name, got.Filename, wp.Filename)
		}
		if got.SHA256 != wp.SHA256 {
			t.Errorf("%s: SHA256 = %q, want %q", name, got.SHA256, wp.SHA256)
		}
	}
	if _, ok := pkgs["nonexistent"]; ok {
		t.Error("nonexistent package unexpectedly found")
	}
}

// TestParseDebPackagesMissingFields ensures stanzas without Filename are skipped.
func TestParseDebPackagesMissingFields(t *testing.T) {
	fixture := `Package: incomplete-pkg
Version: 1.0

Package: complete-pkg
Version: 2.0
Filename: pool/main/c/complete-pkg_2.0_amd64.deb
SHA256: abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234

`
	pkgs := parseDebPackages(fixture)
	if _, ok := pkgs["incomplete-pkg"]; ok {
		t.Error("package without Filename should be skipped")
	}
	if _, ok := pkgs["complete-pkg"]; !ok {
		t.Error("complete-pkg should be present")
	}
}

// TestExtractDeb verifies the pure-Go ar reader against a hand-built .deb.
// The .deb contains a data.tar with one file; extractDeb should unpack it.
func TestExtractDeb(t *testing.T) {
	tmp := t.TempDir()

	// Build a minimal tar archive with one file.
	const fileContent = "hello from tar"
	tarData := buildMinimalTar(t, "testfile.txt", []byte(fileContent))

	// Build a minimal ar archive: magic + one member named "data.tar".
	debData := buildArArchive(t, "data.tar", tarData)

	debPath := filepath.Join(tmp, "test.deb")
	if err := os.WriteFile(debPath, debData, 0o644); err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(tmp, "dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := extractDeb(debPath, destDir); err != nil {
		t.Fatalf("extractDeb: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "testfile.txt"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if string(got) != fileContent {
		t.Errorf("extracted content = %q, want %q", got, fileContent)
	}
}

// TestWrapperContent verifies that writePgWrapper produces a script with the
// expected binary path and LD_LIBRARY_PATH setting.
func TestWrapperContent(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	clientRoot := filepath.Join(tmp, "pg-client")
	major := "17"

	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writePgWrapper(binDir, clientRoot, major, "psql"); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filepath.Join(binDir, "psql"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(content)
	wantBin := clientRoot + "/usr/lib/postgresql/17/bin/psql"
	wantLib := clientRoot + "/usr/lib/" + debLibTriplet()
	if !strings.Contains(got, wantBin) {
		t.Errorf("wrapper missing binary path %q\ncontent:\n%s", wantBin, got)
	}
	if !strings.Contains(got, wantLib) {
		t.Errorf("wrapper missing lib dir %q\ncontent:\n%s", wantLib, got)
	}
	if !strings.Contains(got, "LD_LIBRARY_PATH") {
		t.Errorf("wrapper missing LD_LIBRARY_PATH\ncontent:\n%s", got)
	}
}

// buildMinimalTar creates an uncompressed tar archive with one file.
func buildMinimalTar(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// buildArArchive builds a minimal ar archive with one member.
// The ar format: 8-byte magic + per-member 60-byte header + data.
func buildArArchive(t *testing.T, memberName string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("!<arch>\n")
	// ar member header (60 bytes):
	//   name(16) + mtime(12) + uid(6) + gid(6) + mode(8) + size(10) + magic(2)
	hdr := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8s%-10d`\n",
		memberName, 0, 0, 0, "100644", len(data))
	if len(hdr) != 60 {
		t.Fatalf("ar header length = %d, want 60", len(hdr))
	}
	buf.WriteString(hdr)
	buf.Write(data)
	if len(data)%2 != 0 {
		buf.WriteByte('\n') // ar padding
	}
	return buf.Bytes()
}
