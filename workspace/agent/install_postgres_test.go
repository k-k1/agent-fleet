package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPgVersionGT verifies the version comparator used for maven-metadata sorting.
func TestPgVersionGT(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"17.11.0", "17.6.0", true},
		{"17.6.0", "17.11.0", false},
		{"17.11.0", "17.11.0", false},
		{"18.1.0", "17.11.0", true},
		{"17.11.0", "", true},
	}
	for _, c := range cases {
		got := pgVersionGT(c.a, c.b)
		if got != c.want {
			t.Errorf("pgVersionGT(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestZonkyLatestVersion verifies that zonkyLatestVersion picks the highest plain
// release version for a major from a maven-metadata.xml served by httptest.
func TestZonkyLatestVersion(t *testing.T) {
	meta := `<?xml version="1.0"?>
<metadata>
  <versioning>
    <versions>
      <version>16.9.0</version>
      <version>17.6.0</version>
      <version>17.6.0-1</version>
      <version>17.11.0</version>
      <version>18.1.0</version>
    </versions>
  </versioning>
</metadata>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, meta)
	}))
	defer srv.Close()

	origURL := zonkyBaseURL
	zonkyBaseURL = srv.URL
	defer func() { zonkyBaseURL = origURL }()

	ver, err := zonkyLatestVersion("linux-amd64", "17")
	if err != nil {
		t.Fatalf("zonkyLatestVersion: %v", err)
	}
	if ver != "17.11.0" {
		t.Errorf("got %q, want 17.11.0 (hyphenated 17.6.0-1 and other majors must be skipped)", ver)
	}
}

// TestInstallPostgresSHAMismatch verifies that a sha mismatch returns *pgSHAMismatch
// and leaves no staging residue.
func TestInstallPostgresSHAMismatch(t *testing.T) {
	const wrongSHA = "0000000000000000000000000000000000000000000000000000000000000000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/maven-metadata.xml"):
			fmt.Fprint(w, `<?xml version="1.0"?><metadata><versioning><versions><version>16.1.0</version></versions></versioning></metadata>`)
		case strings.HasSuffix(r.URL.Path, ".sha256"):
			fmt.Fprint(w, wrongSHA)
		default:
			// Return real bytes so curl exits 0, but sha will not match wrongSHA.
			fmt.Fprint(w, "fake jar content")
		}
	}))
	defer srv.Close()

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	origURL := zonkyBaseURL
	zonkyBaseURL = srv.URL
	defer func() { zonkyBaseURL = origURL }()

	// Use major 16 so the sidecar path is taken (not the versions.json pin path).
	err := installPostgres("16")
	if err == nil {
		t.Fatal("expected error on sha mismatch, got nil")
	}
	if _, ok := err.(*pgSHAMismatch); !ok {
		t.Fatalf("expected *pgSHAMismatch, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), wrongSHA) {
		t.Errorf("error message should include expected sha %q\ngot: %s", wrongSHA, err.Error())
	}

	// Staging dir must be gone.
	share := filepath.Join(tmp, ".local", "share", "agent-fleet")
	if entries, readErr := os.ReadDir(share); readErr == nil {
		for _, e := range entries {
			if e.Name() == "pg-16-install" {
				t.Errorf("staging dir not cleaned up: %s", e.Name())
			}
		}
	}
}
