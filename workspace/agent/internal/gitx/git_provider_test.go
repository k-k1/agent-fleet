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
