package gitx

import "testing"

func TestGitProviderHost(t *testing.T) {
	cases := []struct {
		remote   string
		provider string
		host     string
	}{
		{"https://github.com/k-k1/agent-fleet.git", "github", "github.com"},
		{"git@github.com:k-k1/agent-fleet.git", "github", "github.com"},
		{"https://bitbucket.org/team/repo.git", "bitbucket", "bitbucket.org"},
		{"git@bitbucket.org:team/repo.git", "bitbucket", "bitbucket.org"},
		{"https://gitlab.com/group/proj.git", "gitlab", "gitlab.com"},
		{"https://user@github.com/o/r.git", "github", "github.com"},
		{"ssh://git@git.example.com:2222/o/r.git", "git.example.com", "git.example.com"},
		{"https://git.internal.corp/o/r", "git.internal.corp", "git.internal.corp"},
		{"", "", ""},
	}
	for _, c := range cases {
		p, h := gitProviderHost(c.remote)
		if p != c.provider || h != c.host {
			t.Errorf("gitProviderHost(%q) = (%q,%q), want (%q,%q)", c.remote, p, h, c.provider, c.host)
		}
	}
}

// The path half of the same identity. Host + path is what the Console groups the sessions
// overview by, so the two forms of the SAME repository (https and scp-like, with or without
// ".git") have to reduce to one string — and no form may carry credentials.
func TestGitRemotePath(t *testing.T) {
	cases := []struct{ remote, want string }{
		{"https://github.com/k-k1/agent-fleet.git", "k-k1/agent-fleet"},
		{"git@github.com:k-k1/agent-fleet.git", "k-k1/agent-fleet"},
		{"https://github.com/k-k1/agent-fleet", "k-k1/agent-fleet"},
		{"https://github.com/k-k1/agent-fleet/", "k-k1/agent-fleet"},
		{"ssh://git@git.example.com:2222/o/r.git", "o/r"},
		{"https://gitlab.com/group/sub/proj.git", "group/sub/proj"}, // nested groups stay whole
		{"https://user:token@github.com/o/r.git", "o/r"},            // credentials never ride along
		{"https://github.com", ""},                                  // host only
		{"", ""},
	}
	for _, c := range cases {
		if got := gitRemotePath(c.remote); got != c.want {
			t.Errorf("gitRemotePath(%q) = %q, want %q", c.remote, got, c.want)
		}
	}
}

// The injected internal host carries the port (it is the credential key), but a remote
// on that host must still badge as "internal".
func TestGitProviderHostInternalWithPort(t *testing.T) {
	t.Setenv("AF_INTERNAL_GIT_HOST", "127.0.0.1:8080")
	p, h := gitProviderHost("http://127.0.0.1:8080/git/acme/app.git")
	if p != "internal" || h != "127.0.0.1" {
		t.Fatalf("gitProviderHost = (%q,%q), want (internal, 127.0.0.1)", p, h)
	}
}

// Where the Agent rewrites internal git onto the CP's workspace listener, `git remote
// get-url` reports the listener's host; that remote is still the internal provider.
func TestGitProviderHostInternalThroughWorkspaceListener(t *testing.T) {
	t.Setenv("AF_INTERNAL_GIT_HOST", "af.example")
	t.Setenv("AF_CP_INTERNAL_URL", "http://af-cp-internal.ns.svc:8098")
	for _, remote := range []string{"https://af.example/git/acme/app.git", "http://af-cp-internal.ns.svc:8098/git/acme/app.git"} {
		if p, _ := gitProviderHost(remote); p != "internal" {
			t.Errorf("gitProviderHost(%q) provider = %q, want internal", remote, p)
		}
	}
	t.Setenv("AF_INTERNAL_GIT_HOST", "")
	if p, _ := gitProviderHost("http://af-cp-internal.ns.svc:8098/git/acme/app.git"); p == "internal" {
		t.Error("the listener's host badges as internal with internal git off")
	}
}

func TestGitHubRepoOf(t *testing.T) {
	cases := map[string]string{
		"git@github.com:k-k1/agent-fleet.git":     "k-k1/agent-fleet",
		"https://x-access-token:t@github.com/o/r": "o/r",
		"https://github.com/o/r/":                 "o/r",
		"https://github.example.com/o/r.git":      "", // Enterprise: not the token's host
		"https://bitbucket.org/w/r.git":           "",
		"https://github.com/o":                    "",
		"https://gitlab.com/group/sub/r.git":      "",
	}
	for in, want := range cases {
		if got := GitHubRepoOf(in); got != want {
			t.Errorf("GitHubRepoOf(%q) = %q, want %q", in, got, want)
		}
	}
}
