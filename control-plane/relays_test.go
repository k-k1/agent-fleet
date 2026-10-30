package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestRelayRowsAreWellFormed — each row names one method and an /api path. A method-less row would
// register a pattern that relays every method, and its audit would never match (relayFor compares
// the row's method with the request's).
func TestRelayRowsAreWellFormed(t *testing.T) {
	methods := map[string]bool{http.MethodGet: true, http.MethodPost: true, http.MethodPut: true,
		http.MethodPatch: true, http.MethodDelete: true}
	for _, table := range relayTables {
		for _, rl := range table {
			method, path, ok := strings.Cut(rl.route, " ")
			if !ok || !methods[method] || !strings.HasPrefix(path, "/api/") || strings.ContainsAny(path, " \t") {
				t.Errorf("relay row %q is not \"METHOD /api/path\"", rl.route)
			}
		}
	}
}

// TestSignedRelaysHaveLiteralPaths — memberOriginRoute looks the request's own path up, so a signed
// row with a wildcard would never be signed, and the Agent would refuse the member on it.
func TestSignedRelaysHaveLiteralPaths(t *testing.T) {
	n := 0
	for _, table := range relayTables {
		for _, rl := range table {
			if rl.sign == unsigned {
				continue
			}
			n++
			if strings.ContainsAny(rl.route, "{}") {
				t.Errorf("signed relay row %q has a wildcard", rl.route)
			}
		}
	}
	if n == 0 {
		t.Fatal("no signed relay row: the scan reads nothing")
	}
}

// TestWrappedRelaysAreRegistered — a wrapped row is only a declaration; routes.go registers the
// route. One that routes.go does not register keeps an audit rule nothing consults.
func TestWrappedRelaysAreRegistered(t *testing.T) {
	golden := map[string]bool{}
	for _, line := range readGoldenLines(t, routesGoldenPath) {
		golden[line] = true
	}
	n := 0
	for _, table := range relayTables {
		for _, rl := range table {
			if !golden[rl.route] {
				t.Errorf("relay row %q is not in %s", rl.route, routesGoldenPath)
			}
			if rl.wrapped {
				n++
			}
		}
	}
	if n == 0 {
		t.Fatal("no wrapped relay row: the scan reads nothing")
	}
}

// TestRelayAuditNeedsTheRoutedMethod — a GET pattern also serves HEAD; the export audit belongs to
// the GET alone, as it did when the classification switched on the method.
func TestRelayAuditNeedsTheRoutedMethod(t *testing.T) {
	mux := http.NewServeMux()
	var got []bool
	mux.HandleFunc("GET /api/agents/memory/export", func(_ http.ResponseWriter, r *http.Request) {
		_, _, ok := auditActionTarget(r)
		got = append(got, ok)
	})
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		r, _ := http.NewRequest(m, "/api/agents/memory/export?format=bundle", nil)
		mux.ServeHTTP(httptest.NewRecorder(), r)
	}
	if len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("export audited (GET, HEAD) = %v, want [true false]", got)
	}
}

// TestEveryRelayTableIsListed — a table that relayTables does not name is neither registered nor
// consulted for audit or signing, and nothing else would say so. Every route row written in a
// relays_*.go file must therefore be in the index.
func TestEveryRelayTableIsListed(t *testing.T) {
	files, err := filepath.Glob("relays_*.go")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			kv, ok := node.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			if key, _ := kv.Key.(*ast.Ident); key == nil || key.Name != "route" {
				return true
			}
			lit, _ := kv.Value.(*ast.BasicLit)
			if lit == nil {
				t.Errorf("%s: a route that is not a string literal", f)
				return true
			}
			route, _ := strconv.Unquote(lit.Value)
			n++
			if _, ok := relayIndex[route]; !ok {
				t.Errorf("%s: %s is in a table relayTables does not list", f, route)
			}
			return true
		})
	}
	if n != len(relayIndex) {
		t.Errorf("relays_*.go hold %d rows, the index %d", n, len(relayIndex))
	}
}
