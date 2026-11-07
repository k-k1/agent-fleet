package auth

import "testing"

// #3485: AUTH has no default. Unset / dev without opt-in / dev with opt-in / https + dev.
func TestResolveMode(t *testing.T) {
	for _, c := range []struct {
		name, auth, runtime, allow, base string
		want                             string
		wantErr                          bool
	}{
		{"unset on docker", "", "local", "", "", "", true},
		{"unset on ecs", "", "ecs", "1", "", "", true},
		{"unset on gke", "", "gke", "1", "", "", true},
		{"unset on native is dev", "", "native", "", "", "dev", false},
		{"unset on wsl is dev", "", "wsl", "", "", "dev", false},
		{"dev on docker without opt-in", "dev", "local", "", "", "", true},
		{"dev on docker with wrong opt-in", "dev", "local", "true", "", "", true},
		{"dev on docker with opt-in", "dev", "local", "1", "http://localhost:8080", "dev", false},
		{"dev on native needs no opt-in", "dev", "native", "", "", "dev", false},
		{"dev with https", "dev", "local", "1", "https://af.example.test", "", true},
		{"dev with https on native", "dev", "native", "", "HTTPS://af.example.test", "", true},
		{"oauth on docker", "oauth", "local", "", "https://af.example.test", "oauth", false},
		{"typo Oauth", "Oauth", "local", "1", "", "", true},
		{"trailing space", "oauth ", "ecs", "1", "", "", true},
		{"typo deev", "deev", "local", "1", "http://localhost:8080", "", true},
		{"uppercase DEV", "DEV", "native", "", "", "", true},
		{"blank", " ", "local", "1", "", "", true},
		{"unknown on native", "none", "native", "", "", "", true},
		{"proxy on ecs", "proxy", "ecs", "", "", "proxy", false},
	} {
		got, err := ResolveMode(c.auth, c.runtime, c.allow, c.base)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%s: ResolveMode = %q, %v; want %q (err=%v)", c.name, got, err, c.want, c.wantErr)
		}
	}
}
