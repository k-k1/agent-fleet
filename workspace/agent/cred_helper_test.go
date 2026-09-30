package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// What the CP injects as AF_INTERNAL_GIT_HOST is the key the credential is stored
// under, and git asks with `host=<name>:<port>` whenever the clone URL carries a port.
// The seed and the helper have to meet on that key.
func TestInternalGitCredentialResolvesWithPort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	t.Setenv("AF_INTERNAL_GIT_HOST", "127.0.0.1:8080")
	t.Setenv("AF_INTERNAL_GIT_TOKEN", "tok-1")
	seedInternalGit()

	var out bytes.Buffer
	credHelperGet(strings.NewReader("protocol=http\nhost=127.0.0.1:8080\n\n"), &out)
	if want := "username=x-access-token\npassword=tok-1\n"; out.String() != want {
		t.Fatalf("helper answered %q, want %q", out.String(), want)
	}
}

// A store seeded by a CP that dropped the port holds the same token under the bare
// host name; re-seeding moves it rather than leaving it where git hands it to any
// server on the default port.
func TestSeedInternalGitDropsThePortlessEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Git["127.0.0.1"] = secrets.GitEntry{User: "x-access-token", Token: "tok-1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_INTERNAL_GIT_HOST", "127.0.0.1:8080")
	t.Setenv("AF_INTERNAL_GIT_TOKEN", "tok-1")
	seedInternalGit()

	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Git["127.0.0.1"]; ok {
		t.Fatalf("the portless entry survived: %v", s.Git)
	}
	if e := s.Git["127.0.0.1:8080"]; e.Token != "tok-1" {
		t.Fatalf("the credential was not stored under host:port: %v", s.Git)
	}
}
