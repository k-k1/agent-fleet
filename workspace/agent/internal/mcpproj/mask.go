package mcpproj

import "github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"

// maskServer returns a copy of s with every non-empty Env/Headers VALUE replaced by
// mcpreg.MaskedValue. Independent of mcpreg.Masked (which operates on ServerDef,
// ADR0040's no-shared-type rule) but the same sentinel and the same "empty stays
// empty" semantics — an absent value is not a secret being withheld, it is a value
// nobody entered (docs/56 §7.3 / docs/48 §5.1).
func maskServer(s Server) Server {
	out := s
	out.Env = maskValues(s.Env)
	out.Headers = maskValues(s.Headers)
	return out
}

func maskValues(m map[string]string) map[string]string {
	if len(m) == 0 {
		return m
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v == "" {
			out[k] = ""
			continue
		}
		out[k] = mcpreg.MaskedValue
	}
	return out
}
