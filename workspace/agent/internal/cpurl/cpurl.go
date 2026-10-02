// Package cpurl names the two Control Plane base URLs a workspace may be given (ADR 0106
// decision 8). AF_CP_BASE_URL is the public base, the one a person's browser opens.
// AF_CP_INTERNAL_URL, injected only where the workspace cannot reach the CP through its
// public address (Kubernetes), is the CP's workspace-only listener. Every request the
// Agent sends to the CP goes to Request(); every link built for a person uses Public().
package cpurl

import (
	"os"
	"strings"
)

// Public is AF_CP_BASE_URL without surrounding space or a trailing slash. "" = the CP
// injected no base (no PUBLIC_BASE_URL), and the CP bridges are off.
func Public() string { return clean(os.Getenv("AF_CP_BASE_URL")) }

// Internal is AF_CP_INTERNAL_URL, cleaned like Public. "" on every deployment that does
// not run the CP's workspace listener.
func Internal() string { return clean(os.Getenv("AF_CP_INTERNAL_URL")) }

// Request is the base for the Agent's own requests to the CP: the internal URL when the CP
// injected one, else the public base. The internal URL is honoured only alongside the
// public one, because the CP injects the bridge tokens only with AF_CP_BASE_URL; "" means
// there is no CP to ask, exactly as before the internal URL existed.
func Request() string {
	pub := Public()
	if pub == "" {
		return ""
	}
	if in := Internal(); in != "" {
		return in
	}
	return pub
}

// All is every base the workspace knows the CP by, public first, without duplicates —
// for the lists of destinations a browser must not be pointed at.
func All() []string {
	var out []string
	for _, b := range []string{Public(), Internal()} {
		if b != "" && (len(out) == 0 || out[0] != b) {
			out = append(out, b)
		}
	}
	return out
}

func clean(v string) string { return strings.TrimRight(strings.TrimSpace(v), "/") }
