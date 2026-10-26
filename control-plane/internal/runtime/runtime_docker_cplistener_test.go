package runtime

import (
	"slices"
	"strings"
	"testing"
)

// The supported docker setup names the CP's workspace listener host.docker.internal
// (09 §9.3). That name exists only through --add-host, so the container must carry the
// mapping whenever AF_CP_INTERNAL_URL uses it, and only then.
func TestDockerWorkspaceMapsHostGatewayForInternalURL(t *testing.T) {
	for _, tc := range []struct {
		env  []string
		want []string
	}{
		{nil, nil},
		{[]string{"AF_CP_BASE_URL=https://cp.example.com"}, nil},
		{[]string{"AF_CP_INTERNAL_URL=http://host.docker.internal:8098"}, []string{"--add-host", "host.docker.internal:host-gateway"}},
		{[]string{"AF_CP_INTERNAL_URL=http://Host.Docker.Internal:8098/"}, []string{"--add-host", "host.docker.internal:host-gateway"}},
		// An address or a name of the operator's own is left to them.
		{[]string{"AF_CP_INTERNAL_URL=http://172.17.0.1:8098"}, nil},
		{[]string{"AF_CP_INTERNAL_URL=http://cp-internal.example.com:8098"}, nil},
		// The last value is the one the container sees.
		{[]string{"AF_CP_INTERNAL_URL=http://host.docker.internal:8098", "AF_CP_INTERNAL_URL=http://172.17.0.1:8098"}, nil},
	} {
		d := &dockerRuntime{extraEnv: tc.env}
		if got := d.hostArgs(); !slices.Equal(got, tc.want) {
			t.Errorf("hostArgs(%v) = %v, want %v", tc.env, got, tc.want)
		}
	}
}

// With the egress proxy on, the internal host must bypass it: the proxy cannot resolve
// host.docker.internal, and the bridge tokens have no business passing through it.
func TestDockerWorkspaceInternalURLBypassesProxy(t *testing.T) {
	// The CP's template env (main.go, AF_EGRESS_PROXY_ADDR) followed by the per-start env.
	proxied := []string{
		"http_proxy=http://10.0.0.5:3128", "https_proxy=http://10.0.0.5:3128",
		"HTTP_PROXY=http://10.0.0.5:3128", "HTTPS_PROXY=http://10.0.0.5:3128",
		"no_proxy=localhost,127.0.0.1,::1", "NO_PROXY=localhost,127.0.0.1,::1",
	}
	internal := "AF_CP_INTERNAL_URL=http://host.docker.internal:8098"

	final := func(args []string, key string) string {
		v := ""
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-e" && strings.HasPrefix(args[i+1], key+"=") {
				v = strings.TrimPrefix(args[i+1], key+"=")
			}
		}
		return v
	}

	d := &dockerRuntime{extraEnv: append(slices.Clone(proxied), internal)}
	args := d.envArgs()
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		if got, want := final(args, k), "localhost,127.0.0.1,::1,host.docker.internal"; got != want {
			t.Errorf("%s = %q, want %q (args %v)", k, got, want, args)
		}
	}

	// No proxy: nothing to bypass, the env is passed on as it is.
	d = &dockerRuntime{extraEnv: []string{internal}}
	if args := d.envArgs(); final(args, "NO_PROXY") != "" || final(args, "no_proxy") != "" {
		t.Errorf("NO_PROXY added without a proxy: %v", args)
	}
	// No internal URL: today's env, untouched.
	d = &dockerRuntime{extraEnv: slices.Clone(proxied)}
	if got := d.envArgs(); strings.Count(strings.Join(got, " "), "NO_PROXY=") != 1 {
		t.Errorf("NO_PROXY rewritten without AF_CP_INTERNAL_URL: %v", got)
	}
	// Already listed: not appended twice.
	d = &dockerRuntime{extraEnv: append(slices.Clone(proxied), "NO_PROXY=host.docker.internal", "no_proxy=host.docker.internal", internal)}
	if got := d.envArgs(); strings.Count(strings.Join(got, " "), "NO_PROXY=") != 2 {
		t.Errorf("NO_PROXY rewritten although the host is listed: %v", got)
	}
}
