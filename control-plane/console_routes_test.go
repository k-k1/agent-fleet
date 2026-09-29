// console_routes_test.go checks that every `api/...` path the Console calls has a route in
// buildMux.
//
// The CP relays to the Agent by explicit allowlist (docs/build/05-api.md §5.2), so an Agent
// route the Console calls but routes.go never registers falls through to the static
// catch-all and answers 404. The Console usually swallows that error, so the feature just
// shows nothing and every other test stays green.
//
// The Console side is read as text: string and template literals that start with `api/` or
// `/api/`. The match is deliberately loose — a `${…}` segment matches any route segment and
// the method is not compared — because the target is a fully literal path that no route can
// serve, not a proof that each call is well-formed.
package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const consoleSrcDir = "../console/src"

// dynSeg stands in for a `${…}` substitution inside an extracted Console path.
const dynSeg = "\x00"

// TestConsoleAPIPathsHaveCPRoutes — a literal Console path with no matching CP route is a
// relay allowlist miss: add the route to routes.go and regenerate testdata/routes.golden.
func TestConsoleAPIPathsHaveCPRoutes(t *testing.T) {
	// Switch on every env-conditional registration so the table is the widest one any
	// deployment serves: registerMCPRoutes and the native-only registerUpdateRoutes.
	t.Setenv("AF_MCP_ENABLED", "true")
	t.Setenv("AF_SELF_LINK", filepath.Join(t.TempDir(), "af"))
	_, mux := smokeEnv(t)
	var routes [][]string
	for _, line := range muxRoutes(t, mux) {
		_, path, _ := strings.Cut(line, " ")
		if strings.HasPrefix(path, "/api/") {
			routes = append(routes, strings.Split(strings.TrimPrefix(path, "/"), "/"))
		}
	}

	calls := consoleAPIPaths(t)
	// An extractor that silently finds nothing would pass; the Console makes hundreds of calls.
	if len(calls) < 100 {
		t.Fatalf("only %d api/ paths extracted from %s: the scan is broken", len(calls), consoleSrcDir)
	}

	var missing []string
	for path, sites := range calls {
		if !anyRouteMatches(routes, path) {
			missing = append(missing, strings.ReplaceAll(path, dynSeg, "${…}")+"  ("+strings.Join(sites, ", ")+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("Console calls %s but buildMux has no matching /api route", m)
	}
}

// consoleAPIPaths maps each extracted path ("api/sessions/\x00/committed") to the file:line
// sites that use it. Test files are skipped: they may call paths on purpose that no server has.
func consoleAPIPaths(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	err := filepath.WalkDir(consoleSrcDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() || !(strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".tsx")) ||
			strings.Contains(name, ".test.") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(consoleSrcDir, p)
		for i, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			for _, path := range extractAPIPaths(line) {
				out[path] = append(out[path], rel+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", consoleSrcDir, err)
	}
	return out
}

// extractAPIPaths returns the path part of every literal in line that opens with `api/` or
// `/api/`, without the leading slash and without the query. Each `${…}` becomes dynSeg; its
// braces are balanced so a nested template (`${p ? `?x=${v}` : ""}`) is consumed whole.
// A literal bound to a variable (`const base = `api/x/${id}“) is a base the code extends
// later, so it comes back with a trailing "/" and matches as a prefix.
func extractAPIPaths(line string) []string {
	var out []string
	for i := 0; i < len(line); i++ {
		quote := line[i]
		if quote != '"' && quote != '\'' && quote != '`' {
			continue
		}
		start := i + 1
		if strings.HasPrefix(line[start:], "/api/") {
			start++
		}
		if !strings.HasPrefix(line[start:], "api/") {
			continue
		}
		var sb strings.Builder
		ended := false // past the path: the query or anything after a space is skipped
		j := start
		for ; j < len(line) && line[j] != quote; j++ {
			switch c := line[j]; {
			case quote == '`' && strings.HasPrefix(line[j:], "${"):
				for depth := 0; j < len(line); j++ {
					if line[j] == '{' {
						depth++
					} else if line[j] == '}' {
						if depth--; depth == 0 {
							break
						}
					}
				}
				if !ended {
					sb.WriteString(dynSeg)
				}
			case c == '?' || c == ' ':
				ended = true
			case !ended:
				sb.WriteByte(c)
			}
		}
		path := sb.String()
		before := strings.TrimRight(line[:i], " ")
		if strings.HasSuffix(before, "=") && !strings.HasSuffix(path, "/") &&
			!strings.HasSuffix(before, "==") && !strings.HasSuffix(before, "!=") {
			path += "/"
		}
		out = append(out, path)
		i = j
	}
	return out
}

// anyRouteMatches reports whether some route could serve path. Path segments: dynSeg alone
// matches any segment; text before an embedded dynSeg must prefix the route segment
// (`cancel${hint}`, where hint is a query); a trailing "/" means the literal may be extended
// with more segments, so the route at that prefix or any route below it counts.
func anyRouteMatches(routes [][]string, path string) bool {
	segs := strings.Split(path, "/")
	open := segs[len(segs)-1] == ""
	if open {
		segs = segs[:len(segs)-1]
	}
	for _, r := range routes {
		if routeMatches(r, segs, open) {
			return true
		}
	}
	return false
}

func routeMatches(route, segs []string, open bool) bool {
	for i, s := range segs {
		if i >= len(route) {
			return false
		}
		rs := route[i]
		if strings.HasSuffix(rs, "...}") {
			return true
		}
		if strings.HasPrefix(rs, "{") || s == dynSeg {
			continue
		}
		if lit, _, dyn := strings.Cut(s, dynSeg); dyn {
			if !strings.HasPrefix(rs, lit) {
				return false
			}
			continue
		}
		if rs != s {
			return false
		}
	}
	if open {
		return len(route) >= len(segs)
	}
	return len(route) == len(segs)
}

func TestExtractAPIPaths(t *testing.T) {
	cases := map[string][]string{
		"api(\"api/fs/changes\")":                                                            {"api/fs/changes"},
		"api(`api/sessions/${encodeURIComponent(s)}/committed`)":                             {"api/sessions/" + dynSeg + "/committed"},
		"api(`/api/update/status?x=1`)":                                                      {"api/update/status"},
		"api(`api/repos/${enc}/status${path ? `?path=${encodeURIComponent(path)}` : \"\"}`)": {"api/repos/" + dynSeg + "/status" + dynSeg},
		"apiJSON(\"api/a/\" + encodeURIComponent(id) + \"/state\", \"POST\")":                {"api/a/"},
		"fetch(\"https://example.test/\")":                                                   nil,
		"const path = `api/shared-sessions/${encodeURIComponent(id)}`;":                      {"api/shared-sessions/" + dynSeg + "/"},
		"if (p === \"api/x\") {":                                                             {"api/x"},
		"api(\"api/a?q=b c\") + api('api/b')":                                                {"api/a", "api/b"},
	}
	for in, want := range cases {
		got := extractAPIPaths(in)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("extractAPIPaths(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnyRouteMatches(t *testing.T) {
	routes := [][]string{
		strings.Split("api/sessions/{name}/stop", "/"),
		strings.Split("api/aws-login/{id}/cancel", "/"),
		strings.Split("api/admin/egress/allowlist/{id}/state", "/"),
		strings.Split("api/drawio/stencils/{name...}", "/"),
	}
	for path, want := range map[string]bool{
		"api/sessions/" + dynSeg + "/stop":                 true,
		"api/sessions/" + dynSeg + "/committed":            false,
		"api/sessions/x/stop/more":                         false,
		"api/aws-login/" + dynSeg + "/cancel" + dynSeg:     true,
		"api/admin/egress/allowlist/":                      true,
		"api/admin/egress/":                                true,
		"api/admin/egress/allowlist/" + dynSeg + "/state/": true,
		"api/admin/egress/nope/":                           false,
		"api/drawio/stencils/a/b":                          true,
		"api/drawio/stencils":                              false,
	} {
		if got := anyRouteMatches(routes, path); got != want {
			t.Errorf("anyRouteMatches(%q) = %v, want %v", strings.ReplaceAll(path, dynSeg, "${…}"), got, want)
		}
	}
}
