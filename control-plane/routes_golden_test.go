// routes_golden_test.go turns every (method, path) that buildMux registers into a golden
// file.
//
// Why it exists (ADR 0067 decision 6): a parallel-refactor move touches thousands of lines
// in one PR, more than a reviewer can read, so wire compatibility at least is proved
// mechanically. The route table is half of the wire — the other half is the DTO key sets in
// wire_golden_test.go — and dropping a single register call while moving handlers into
// internal/ leaves every other test green, so this is the check that has to go red.
//
// The table is taken from the assembled mux, not by static analysis grepping HandleFunc out
// of the source: a register function that moves into internal/ produces no false red,
// because what is inspected is the registered table itself. The price is touching net/http's
// internal representation (see muxRoutes below).
//
// Updating it after a deliberate change in routes:
//
//	cd control-plane && go test -run TestRouteTableGolden -update-routes-golden ./...
//
// Put the generated diff in the PR. A diff that does not match the intent is exactly the
// accident this was built to catch.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var updateRoutesGolden = flag.Bool("update-routes-golden", false,
	"rewrite testdata/routes.golden from the actual route table (only when routes were added or removed on purpose)")

const routesGoldenPath = "testdata/routes.golden"

// TestRouteTableGolden — every route of buildMux must match testdata/routes.golden.
//
// AF_MCP_ENABLED gates the only env-conditional route in the CP (registerMCPRoutes), so the
// golden is taken with it true in order to capture everything. That the difference when it
// is off is exactly the one /mcp line is pinned separately by
// TestRouteTableMCPIsTheOnlyOptIn.
func TestRouteTableGolden(t *testing.T) {
	t.Setenv("AF_MCP_ENABLED", "true")
	_, mux := smokeEnv(t)
	got := muxRoutes(t, mux)

	if *updateRoutesGolden {
		writeRoutesGolden(t, routesGoldenPath, got)
		t.Logf("wrote %s (%d routes)", routesGoldenPath, len(got))
		return
	}
	assertGoldenLines(t, routesGoldenPath, got)
}

// TestRouteTableMCPIsTheOnlyOptIn — /mcp is the only route the env can change. Another
// conditional registration would let the golden stay green while the production table
// differs, so the arrival of a second conditional route is itself made red here.
func TestRouteTableMCPIsTheOnlyOptIn(t *testing.T) {
	t.Setenv("AF_MCP_ENABLED", "")
	_, mux := smokeEnv(t)
	off := muxRoutes(t, mux)

	want := []string{}
	for _, r := range readGoldenLines(t, routesGoldenPath) {
		if r != "ANY /mcp" {
			want = append(want, r)
		}
	}
	if diff := lineDiff(want, off); diff != "" {
		t.Errorf("with AF_MCP_ENABLED off, the route table does not match golden minus /mcp"+
			" (did another env-conditional route appear?):\n%s", diff)
	}
}

// muxRoutes extracts the registered (method, path) pairs from an assembled *http.ServeMux.
//
// It reaches into net/http's internal representation by reflection. The public API offers no
// enumeration (Handler() answers the match for one request only), so this is the sole way to
// learn everything that was registered. That representation does change: the
// ServeMux.patterns slice present up to Go 1.25 is gone in 1.26 and the tree (routingNode)
// has to be walked instead. When it breaks, do not silently return nothing — fail with a
// message that says what changed. A golden with zero entries is the most dangerous outcome
// there is.
func muxRoutes(t *testing.T, mux *http.ServeMux) []string {
	t.Helper()
	root := reflect.ValueOf(mux).Elem().FieldByName("tree")
	if !root.IsValid() {
		t.Fatalf("http.ServeMux has no tree field: net/http's internal representation changed. " +
			"read go/src/net/http/routing_tree.go and fix muxRoutes")
	}
	var raw []string
	if err := walkRoutingNode(root, &raw); err != nil {
		t.Fatalf("failed to walk the routingNode: %v (net/http's internal representation changed)", err)
	}
	if len(raw) == 0 {
		t.Fatal("not a single route was picked up: the walk is broken (buildMux always registers)")
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		line := routeLine(p)
		if seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	sortRouteLines(out)
	return out
}

// routeLine normalises a registration string ("GET /api/x" / "/api/x/") into "METHOD PATH".
// A registration with no method (accepting all of them) is written ANY: left blank, the
// golden's lines no longer align and "the method disappeared" cannot be told apart from
// "there never was one".
func routeLine(pattern string) string {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok || strings.HasPrefix(pattern, "/") {
		return "ANY " + pattern
	}
	return method + " " + strings.TrimSpace(path)
}

// sortRouteLines orders by path, then by method, so different methods on the same path sit
// next to each other and "only DELETE went missing" shows up as one line in the diff.
func sortRouteLines(lines []string) {
	key := func(s string) (string, string) {
		m, p, _ := strings.Cut(s, " ")
		return p, m
	}
	sort.Slice(lines, func(i, j int) bool {
		pi, mi := key(lines[i])
		pj, mj := key(lines[j])
		if pi != pj {
			return pi < pj
		}
		return mi < mj
	})
}

// walkRoutingNode descends net/http's routingNode tree and collects the pattern.str of each
// leaf. Children live in children (a mapping: the slice s up to 8 entries, switching to the
// map m beyond that — both must be read) plus multiChild and emptyChild.
func walkRoutingNode(n reflect.Value, out *[]string) error {
	if !n.IsValid() {
		return nil
	}
	if n.Kind() == reflect.Pointer {
		if n.IsNil() {
			return nil
		}
		n = n.Elem()
	}
	if n.Kind() != reflect.Struct {
		return fmt.Errorf("routingNode is not a struct: %s", n.Kind())
	}
	pat := n.FieldByName("pattern")
	if !pat.IsValid() {
		return fmt.Errorf("routingNode has no pattern field")
	}
	if !pat.IsNil() {
		str := pat.Elem().FieldByName("str")
		if !str.IsValid() {
			return fmt.Errorf("pattern has no str field")
		}
		*out = append(*out, str.String())
	}
	if ch := n.FieldByName("children"); ch.IsValid() {
		s := ch.FieldByName("s")
		if !s.IsValid() {
			return fmt.Errorf("mapping has no s field")
		}
		for i := 0; i < s.Len(); i++ {
			if err := walkRoutingNode(s.Index(i).FieldByName("value"), out); err != nil {
				return err
			}
		}
		m := ch.FieldByName("m")
		if !m.IsValid() {
			return fmt.Errorf("mapping has no m field")
		}
		if !m.IsNil() {
			for _, k := range m.MapKeys() {
				if err := walkRoutingNode(m.MapIndex(k), out); err != nil {
					return err
				}
			}
		}
	}
	if err := walkRoutingNode(n.FieldByName("multiChild"), out); err != nil {
		return err
	}
	return walkRoutingNode(n.FieldByName("emptyChild"), out)
}

// --- reading and writing golden files (used by this package's other goldens too) ---

// readGoldenLines returns the lines, dropping blank ones and comments starting with `#`.
func readGoldenLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (generate it the first time with -update-routes-golden)", path, err)
	}
	var out []string
	for _, ln := range strings.Split(string(raw), "\n") {
		ln = strings.TrimRight(ln, "\r")
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		out = append(out, ln)
	}
	return out
}

func writeRoutesGolden(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	var b strings.Builder
	b.WriteString("# Every (method, path) buildMux() registers. Generated - do not edit by hand.\n")
	b.WriteString("# Update: cd control-plane && go test -run TestRouteTableGolden -update-routes-golden ./...\n")
	b.WriteString("# ANY = registered without a method. Taken with AF_MCP_ENABLED=true.\n")
	fmt.Fprintf(&b, "# count: %d\n", len(lines))
	for _, ln := range lines {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertGoldenLines(t *testing.T, path string, got []string) {
	t.Helper()
	if diff := lineDiff(readGoldenLines(t, path), got); diff != "" {
		t.Errorf("does not match %s:\n%s\n"+
			"if the gain/loss was intended, retake with -update-routes-golden. "+
			"if you have no memory of it, a move dropped a route.", path, diff)
	}
}

// lineDiff returns the set difference of want / got as "- lost" and "+ added" lines, or an
// empty string when they match.
func lineDiff(want, got []string) string {
	inWant := map[string]bool{}
	for _, s := range want {
		inWant[s] = true
	}
	inGot := map[string]bool{}
	for _, s := range got {
		inGot[s] = true
	}
	var lost, added []string
	for _, s := range want {
		if !inGot[s] {
			lost = append(lost, "- "+s)
		}
	}
	for _, s := range got {
		if !inWant[s] {
			added = append(added, "+ "+s)
		}
	}
	if len(lost) == 0 && len(added) == 0 {
		return ""
	}
	return strings.Join(append(lost, added...), "\n")
}
