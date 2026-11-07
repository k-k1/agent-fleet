package main

import "testing"

// #3485: AUTH has no default. Unset / dev without opt-in / dev with opt-in / https + dev.
func TestResolveAuthMode(t *testing.T) {
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
		{"proxy on ecs", "proxy", "ecs", "", "", "proxy", false},
	} {
		got, err := resolveAuthMode(c.auth, c.runtime, c.allow, c.base)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%s: resolveAuthMode = %q, %v; want %q (err=%v)", c.name, got, err, c.want, c.wantErr)
		}
	}
}

// AUTH=dev on a container runtime raises the auth_dev deployment warning; native does not.
func TestAuthDevDeploymentWarning(t *testing.T) {
	for _, c := range []struct {
		auth   string
		native bool
		want   bool
	}{
		{"dev", false, true}, {"dev", true, false}, {"oauth", false, false}, {"proxy", false, false},
	} {
		m := &manager{authMode: c.auth, nativeRuntime: c.native}
		warned := false
		for _, w := range m.deploymentWarnings() {
			warned = warned || w == deploymentWarnAuthDev
		}
		if warned != c.want {
			t.Errorf("AUTH=%s native=%v: auth_dev warned=%v, want %v", c.auth, c.native, warned, c.want)
		}
	}
}
