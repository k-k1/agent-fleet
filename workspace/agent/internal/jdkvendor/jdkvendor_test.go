package jdkvendor

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	for _, c := range []struct {
		id, dir, major string
		ok             bool
	}{
		{"21", "temurin", "21", true},
		{"8", "temurin", "8", true},
		{"corretto-21", "corretto", "21", true},
		{"corretto-8", "corretto", "8", true},
		{"temurin-21", "", "", false}, // Temurin ids stay bare: one spelling per JDK
		{"Corretto-21", "", "", false},
		{"corretto-", "", "", false},
		{"corretto-21a", "", "", false},
		{"corretto-21/../x", "", "", false},
		{"corretto--21", "", "", false},
		{"zulu-21", "", "", false},
		{"", "", "", false},
		{"system", "", "", false},
	} {
		v, major, ok := Parse(c.id)
		if ok != c.ok || (ok && (v.Dir != c.dir || major != c.major)) {
			t.Errorf("Parse(%q) = %v %q %v, want %q %q %v", c.id, v, major, ok, c.dir, c.major, c.ok)
		}
	}
}

func TestLabel(t *testing.T) {
	for id, want := range map[string]string{"21": "Temurin 21", "corretto-17": "Corretto 17", "x": "x"} {
		if got := Label(id); got != want {
			t.Errorf("Label(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestCompareCorrettoVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"21.0.13.0.12", "21.0.13.0.11", 1}, // the fifth field decides
		{"21.0.13.0.11", "21.0.5.11.1", 1},  // numeric, not lexical
		{"8.512.09.1", "8.492.09.1", 1},
		{"8.512.09.1", "8.512.09.1", 0},
	} {
		if got := CompareCorretto(c.a, c.b); got != c.want {
			t.Errorf("CompareCorretto(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIDOfTree(t *testing.T) {
	for name, want := range map[string]string{
		"temurin-21-jdk-amd64":    "21",
		"corretto-8-jdk-arm64":    "corretto-8",
		"corretto-21-jdk":         "corretto-21",
		"zulu-21-jdk-amd64":       "",
		"temurin-jdk-21":          "",
		"java-21-amazon-corretto": "",
	} {
		if got := IDOfTree(name); got != want {
			t.Errorf("IDOfTree(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCorrettoTreeVersion(t *testing.T) {
	dir := t.TempDir()
	if got := CorrettoTreeVersion(dir); got != "" {
		t.Fatalf("no version.txt: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "version.txt"), []byte("8.512.09.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := CorrettoTreeVersion(dir); got != "8.512.09.1" {
		t.Fatalf("CorrettoTreeVersion = %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "version.txt"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := CorrettoTreeVersion(dir); got != "" {
		t.Fatalf("unparsable version.txt: %q", got)
	}
}

// Every answer that is not a redirect naming a version is an error, never a version: a
// wrong "latest" would offer an update that installs something else.
func TestCorrettoLatestAnswers(t *testing.T) {
	var status int
	var location string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if location != "" {
			w.Header().Set("Location", location)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	prev := CorrettoBaseURL
	CorrettoBaseURL = srv.URL
	t.Cleanup(func() { CorrettoBaseURL = prev })

	for _, c := range []struct {
		label    string
		status   int
		location string
		want     string
	}{
		{"relative redirect", http.StatusFound, "/downloads/resources/21.0.13.0.11/amazon-corretto-21.0.13.0.11-linux-x64.tar.gz", "21.0.13.0.11"},
		{"absolute redirect", http.StatusMovedPermanently, "https://cdn.example/downloads/resources/8.512.09.1/amazon-corretto-8.512.09.1-linux-x64.tar.gz", "8.512.09.1"},
		{"no redirect", http.StatusOK, "", ""},
		{"redirect without Location", http.StatusFound, "", ""},
		{"redirect elsewhere", http.StatusFound, "https://cdn.example/amazon-corretto-21-linux-x64.tar.gz", ""},
		{"not found", http.StatusNotFound, "", ""},
	} {
		status, location = c.status, c.location
		got, err := CorrettoLatest("21", "x64")
		if got != c.want || (c.want == "") != (err != nil) {
			t.Errorf("%s: CorrettoLatest = %q, %v; want %q", c.label, got, err, c.want)
		}
	}
}
