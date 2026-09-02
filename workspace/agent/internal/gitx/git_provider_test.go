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
