package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Every path that forgets a meta must settle usage before forgetting it. The moment it is
// forgotten the session drops out of ListMetas and is never folded again, so an open trailing turn
// not settled here never enters the ledger. handleStopSession (the Console's Delete) did miss it.
func TestMetaRemovalPathsFinalizeUsage(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			removes, finalizes := false, false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fn := call.Fun.(type) {
				case *ast.SelectorExpr:
					// RemoveMetaAndLineage (ADR 0096 decision 6) is RemoveMeta plus a
					// fleet-graph erasure; this invariant applies to both equally.
					if x, ok := fn.X.(*ast.Ident); ok && x.Name == "session" &&
						(fn.Sel.Name == "RemoveMeta" || fn.Sel.Name == "RemoveMetaAndLineage") {
						removes = true
					}
					if x, ok := fn.X.(*ast.Ident); ok && x.Name == "usagesvc" && fn.Sel.Name == "FinalizeSessionUsage" {
						finalizes = true
					}
				case *ast.Ident:
					if fn.Name == "FinalizeSessionUsage" {
						finalizes = true
					}
				}
				return true
			})
			if !removes {
				continue
			}
			checked++
			if !finalizes {
				t.Errorf("%s: %s calls session.RemoveMeta/RemoveMetaAndLineage but not FinalizeSessionUsage"+
					" (docs/log/46 §3-b: settle the trailing turn before forgetting the meta)", name, fd.Name.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no call to session.RemoveMeta at all (the scan is broken)")
	}
	t.Logf("checked %d meta-removal paths", checked)
}
