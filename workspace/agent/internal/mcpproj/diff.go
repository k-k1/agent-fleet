package mcpproj

// Cross-file divergence (docs/56 §9.2's "⚠差分" matrix marker): the same server
// name defined in two or more files with different content — the exact shape of the
// motivating novel-lab bug (docs/56 §1), generalized from "these two specific
// files" to "any two files in this snapshot".

import "reflect"

// serverLocation is one (file, server) pairing, carrying the RAW (unmasked) fields
// so divergence is judged on real content — never on the masked wire value, which
// would make every non-empty secret look identical.
type serverLocation struct {
	file string
	s    Server
}

// divergenceWarnings groups locations by server name and, for any name appearing
// with differing content in two or more files, emits one CodeServerDiverged warning
// naming every file it appears in.
func divergenceWarnings(locs []serverLocation) []Warning {
	byName := map[string][]serverLocation{}
	for _, l := range locs {
		byName[l.s.Name] = append(byName[l.s.Name], l)
	}
	var out []Warning
	for name, group := range byName {
		if len(group) < 2 || !anyDiffer(group) {
			continue
		}
		files := make([]string, 0, len(group))
		for _, l := range group {
			files = append(files, l.file)
		}
		out = append(out, Warning{Severity: "yellow", Code: CodeServerDiverged, Server: name, Files: files})
	}
	return out
}

func anyDiffer(group []serverLocation) bool {
	first := group[0].s
	for _, l := range group[1:] {
		if !serversEqual(first, l.s) {
			return true
		}
	}
	return false
}

// serversEqual compares the fields that matter to whether two entries would behave
// the same way if reflected into one file (docs/56 §9.2) — everything except Extra,
// which is kind-specific spelling (e.g. copilot's "tools") that legitimately differs
// between two files without the SERVER itself having diverged.
func serversEqual(a, b Server) bool {
	return a.Transport == b.Transport &&
		a.Command == b.Command &&
		reflect.DeepEqual(a.Args, b.Args) &&
		reflect.DeepEqual(a.Env, b.Env) &&
		a.URL == b.URL &&
		reflect.DeepEqual(a.Headers, b.Headers)
}
