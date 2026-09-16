package main

import (
	"os"
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

Package: postgresql-client-common
Version: 278
Architecture: all
Filename: pool/main/p/postgresql-common/postgresql-client-common_278_all.deb
Size: 47080
SHA256: 023e5b37cdeecedcd32b7ea0d799c9696310c4fc659b8123c494e1ad3ca9e726

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
		"postgresql-client-common": {
			Name:     "postgresql-client-common",
			Version:  "278",
			Filename: "pool/main/p/postgresql-common/postgresql-client-common_278_all.deb",
			SHA256:   "023e5b37cdeecedcd32b7ea0d799c9696310c4fc659b8123c494e1ad3ca9e726",
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

// TestWrapperContent verifies the wrapper script contains the right paths.
func TestWrapperContent(t *testing.T) {
	tmp := t.TempDir()
	binDir := tmp + "/bin"
	clientRoot := tmp + "/pg-client"
	major := "17"

	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writePgWrapper(binDir, clientRoot, major, "psql"); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(binDir + "/psql")
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
		t.Errorf("wrapper missing LD_LIBRARY_PATH")
	}
}
