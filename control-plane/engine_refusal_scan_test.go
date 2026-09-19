package main

// engine_refusal_scan_test.go — every 409 in the model-catalogue area names a holder and a next
// act (ADR 0085 decision 5).
//
// The rule cannot be a convention. "The S3 key … is already recorded; choose a new destination for
// this download or use verified reuse" is grammatically a refusal and operationally a dead end:
// the operator has nothing to look for and nothing to press, which is what an af-sandbox repair
// stalled on for four days while the key was held by a failed attempt at the very repair being
// retried. A 409 in this area means something is IN THE WAY, and something in the way has an
// owner and one act that clears it.
//
// So this walks the area's files and fails on a conflict written any other way. Two escapes, both
// deliberate and both narrow:
//
//   - the exempt list below: a handful of 409s that are not about a holder at all (nothing is
//     holding anything, the refusal is a judgement about the row itself). Each is named by
//     function, and the scan fails if one of them stops existing — an exempt list that rots is
//     one that quietly exempts something else;
//   - a file that does not exist yet is skipped, because lane A's `objects` and `complete` land
//     in another pull request and this scan has to be written before them, not after.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// engineRefusalScanFiles is decision 5's list: the files whose 409s a person acts on.
var engineRefusalScanFiles = []string{
	"engine_admin.go",
	"engine_plan.go",
	"engine_file_move.go",
	"engine_family_parts.go",
	// Lane A's, and scanned the day they arrive.
	"engine_objects.go",
	"engine_complete.go",
}

// engineRefusalExempt is the 409s that carry no holder because there is none. Written as
// function names, with the reason, because "which line" rots at the first edit.
var engineRefusalExempt = map[string]string{
	// The row wants more VRAM than the class declares. A refusal to guess, answered by repeating
	// the call with confirm_vram — nothing is holding anything.
	"engineVramGuardRow": "a refusal to guess, cleared by confirm_vram",
	// The row's declared family reads files the row does not have. The gap IS the row's, and the
	// act is the row's own 揃える, which the mark beside it already offers.
	"engineFilesGuard": "the row's own gap, not a holder",
	// This deployment has nothing to exclude / does not manage this engine's box. Neither is an
	// object, a row or a job standing in the way.
	"putNegative": "the engine declares no exclusion setting",
	"replaceBox":  "the box belongs to another deployment",
	// Interruption is being accepted for a role that declares no interruptible offer. Nothing
	// holds the consent: it is a statement about a list that does not exist yet, and the act
	// that makes it possible is the operator's, in CloudFormation.
	"putSpot": "the engine declares no spot offer to accept",
}

func TestEveryCatalogueConflictNamesAHolderAndANextAct(t *testing.T) {
	scanned, conflicts := 0, 0
	seenExempt := map[string]bool{}
	for _, name := range engineRefusalScanFiles {
		if _, err := os.Stat(name); err != nil {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CompositeLit:
					// `&apiError{http.StatusConflict, …}` — a refusal with no room for the two
					// fields. It has to become refuse(…).
					if !engineRefusalIsConflictArg(node.Elts) {
						return true
					}
					conflicts++
					if _, ok := engineRefusalExempt[fn.Name.Name]; ok {
						seenExempt[fn.Name.Name] = true
						return true
					}
					t.Errorf("%s: %s writes a 409 as an apiError literal — use refuse(…) with a holder and a next act",
						name, fn.Name.Name)
				case *ast.CallExpr:
					id, ok := node.Fun.(*ast.Ident)
					if !ok || id.Name != "refuse" || len(node.Args) < 5 {
						return true
					}
					if !engineRefusalIsConflict(node.Args[0]) {
						return true
					}
					conflicts++
					if _, ok := engineRefusalExempt[fn.Name.Name]; ok {
						seenExempt[fn.Name.Name] = true
						return true
					}
					if engineRefusalIsNil(node.Args[3]) || engineRefusalIsNil(node.Args[4]) {
						t.Errorf("%s: %s refuses with 409 and no %s — the Console has no button to draw",
							name, fn.Name.Name, engineRefusalMissing(node.Args[3], node.Args[4]))
					}
				}
				return true
			})
		}
	}
	// 🔴 The controls. An empty result and a scan that never ran look identical from here, and so
	// do a rule that holds and a matcher that stopped matching.
	if scanned < 4 {
		t.Fatalf("only %d of decision 5's files were read = this scan has gone silent", scanned)
	}
	if conflicts < 8 {
		t.Fatalf("only %d 409 site(s) were recognised = the matcher is broken before the rule is", conflicts)
	}
	for fn := range engineRefusalExempt {
		if !seenExempt[fn] {
			t.Errorf("%s is exempted and no longer writes a 409: drop it from the list rather than "+
				"leaving an exemption a future function could inherit by name", fn)
		}
	}
}

// engineRefusalIsConflict answers whether an expression is `http.StatusConflict`.
func engineRefusalIsConflict(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "StatusConflict" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "http"
}

// engineRefusalIsConflictArg answers whether a composite literal's FIRST element is that status,
// which is how `apiError{status, code, message}` is written everywhere in this package.
func engineRefusalIsConflictArg(elts []ast.Expr) bool {
	return len(elts) > 0 && engineRefusalIsConflict(elts[0])
}

func engineRefusalIsNil(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

func engineRefusalMissing(holder, next ast.Expr) string {
	switch {
	case engineRefusalIsNil(holder) && engineRefusalIsNil(next):
		return "holder and no next act"
	case engineRefusalIsNil(holder):
		return "holder"
	default:
		return "next act"
	}
}

// The scan is only worth anything if it catches what it is written for, and an AST matcher that
// silently stops matching reads exactly like a clean file. So it is run against both shapes of
// the defect, in source this test owns.
func TestTheConflictScanCatchesWhatItIsFor(t *testing.T) {
	const bad = `package p
import "net/http"
func handler() any {
	if x {
		return &apiError{http.StatusConflict, "c", "held by something"}
	}
	return refuse(http.StatusConflict, "c", "held by something", nil, nil)
}`
	f, err := parser.ParseFile(token.NewFileSet(), "bad.go", bad, 0)
	if err != nil {
		t.Fatal(err)
	}
	literals, bare := 0, 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CompositeLit:
				if engineRefusalIsConflictArg(node.Elts) {
					literals++
				}
			case *ast.CallExpr:
				id, ok := node.Fun.(*ast.Ident)
				if ok && id.Name == "refuse" && len(node.Args) >= 5 &&
					engineRefusalIsConflict(node.Args[0]) &&
					engineRefusalIsNil(node.Args[3]) && engineRefusalIsNil(node.Args[4]) {
					bare++
				}
			}
			return true
		})
	}
	if literals != 1 || bare != 1 {
		t.Fatalf("the scan found %d literal(s) and %d holderless refusal(s) in a file with one of each",
			literals, bare)
	}
	if got := engineRefusalMissing(&ast.Ident{Name: "nil"}, &ast.Ident{Name: "x"}); got != "holder" {
		t.Errorf("the message for a missing holder reads %q", got)
	}
}
