package mcpreg

import (
	"os"
	"sort"
)

// stdioBaseEnv is what a member's stdio server that the watch engine starts inherits from the
// Agent: enough to find binaries and a home, nothing else. The Agent's own environment holds
// the secret-store keys and its internal tokens (ADR 0133 §8).
var stdioBaseEnv = []string{"PATH", "HOME", "LANG", "TMPDIR"}

// StdioEnv builds the environment of a stdio server per definition: the allowlist, the
// variables ForwardEnvNames gives this definition (the built-in af server needs the Agent's
// token, a member's server gets only the isolation flag), then the definition's own variables,
// which win. It never starts from os.Environ().
func StdioEnv(d ServerDef) []string {
	env := map[string]string{}
	for _, name := range append(append([]string(nil), stdioBaseEnv...), ForwardEnvNames(d)...) {
		if v, ok := os.LookupEnv(name); ok {
			env[name] = v
		}
	}
	for k, v := range d.Env {
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// NarrowEnv reports whether the connection test and the watch engine start d with StdioEnv. Only
// a definition that is a watch source is narrowed: the environment of an existing session-target
// server is not changed by the watch feature.
func NarrowEnv(d ServerDef) bool { return d.Targets.Watch && d.Origin != OriginBuiltin }

// WatchDefs returns the member's own enabled, ready definitions that are watch sources, whether
// or not the tenant switch is on (the caller checks WatchSourcesAllowed; a definition that
// exists while the switch is off is shown as paused, not as missing). A tenant-distributed or built-in definition is never one: a
// source's events belong to the member who registered it (ADR 0133 §8). An OAuth server is
// reached through the relay, as sessions reach it, and only while a grant is live.
func WatchDefs() ([]ServerDef, error) {
	reg, err := Load()
	if err != nil {
		return nil, err
	}
	var out []ServerDef
	for _, d := range reg.Servers {
		if d.Origin != OriginUser || !d.Targets.Watch || !Ready(d) {
			continue
		}
		if d.Auth == AuthOAuth {
			var ok bool
			if d, ok = relayDef(d); !ok {
				continue
			}
		}
		out = append(out, d)
	}
	return out, nil
}
