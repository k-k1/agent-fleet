package main

import "testing"

func TestOcwebBasePath(t *testing.T) {
	cases := map[string]string{
		"":              "/ocweb/",
		"/":             "/ocweb/",
		"/agent-fleet":  "/agent-fleet/ocweb/",
		"/agent-fleet/": "/agent-fleet/ocweb/",
		"agent-fleet":   "/agent-fleet/ocweb/",
	}
	for in, want := range cases {
		if got := ocwebBasePath(in); got != want {
			t.Errorf("ocwebBasePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOpencodeWebPrefRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// absent → zero value (disabled).
	if p := readOpencodeWebPref(); p.Enabled {
		t.Fatal("absent pref should be disabled")
	}
	if err := writeOpencodeWebPref(opencodeWebPref{Enabled: true, BasePrefix: "/agent-fleet"}); err != nil {
		t.Fatal(err)
	}
	p := readOpencodeWebPref()
	if !p.Enabled || p.BasePrefix != "/agent-fleet" {
		t.Fatalf("round-trip mismatch: %+v", p)
	}
}
