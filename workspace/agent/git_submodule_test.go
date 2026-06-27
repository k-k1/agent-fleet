package main

import "testing"

// TestSSHToHTTPS covers the submodule URL rewrite (CodeLeaf parity), including the
// real bitbucket SSH submodules that broke clone before the fix.
func TestSSHToHTTPS(t *testing.T) {
	cases := []struct{ in, want string }{
		// scp-like form (the failing lib-usr submodules)
		{"git@bitbucket.org:example-org/lib-bundle.git", "https://bitbucket.org/example-org/lib-bundle.git"},
		{"git@github.com:owner/repo.git", "https://github.com/owner/repo.git"},
		{"git@github.com:owner/repo", "https://github.com/owner/repo"},
		// ssh:// form, optional user and port
		{"ssh://git@github.com/owner/repo.git", "https://github.com/owner/repo.git"},
		{"ssh://git@bitbucket.org:22/example-org/lib-core.git", "https://bitbucket.org/example-org/lib-core.git"},
		{"ssh://github.com/owner/repo", "https://github.com/owner/repo"},
		// self-hosted host (host-agnostic)
		{"git@git.example.com:team/lib.git", "https://git.example.com/team/lib.git"},
		// already HTTPS / other schemes pass through unchanged
		{"https://github.com/owner/repo.git", "https://github.com/owner/repo.git"},
		{"http://example.com/x.git", "http://example.com/x.git"},
		{"../relative/submodule", "../relative/submodule"},
	}
	for _, c := range cases {
		if got := sshToHTTPS(c.in); got != c.want {
			t.Errorf("sshToHTTPS(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
