package msp

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnumTypesDecodeAnyString: an enum value the bundle does not list must decode, never fail
// the message it rides in. A host newer than the bundle sends such values as a matter of course
// (1.4.2 grants `feedback`), and a failed initialize result is a session that never starts.
// Generated enums are bare strings with no decoder of their own; this keeps them that way.
func TestEnumTypesDecodeAnyString(t *testing.T) {
	fset := token.NewFileSet()
	gen, err := parser.ParseFile(fset, "types_gen.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// An enum is a type with a generated <Name>Values list.
	enums := map[string]bool{}
	for _, d := range gen.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			continue
		}
		for _, s := range g.Specs {
			for _, n := range s.(*ast.ValueSpec).Names {
				if name, ok := strings.CutSuffix(n.Name, "Values"); ok {
					enums[name] = true
				}
			}
		}
	}
	for _, want := range []string{"CapabilityName", "SessionStatus", "ItemKind", "ReasoningEffort"} {
		if !enums[want] {
			t.Fatalf("no generated %sValues: the enum scan found nothing to check", want)
		}
	}
	for _, d := range gen.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.TYPE {
			continue
		}
		for _, s := range g.Specs {
			ts := s.(*ast.TypeSpec)
			if !enums[ts.Name.Name] {
				continue
			}
			if id, ok := ts.Type.(*ast.Ident); !ok || id.Name != "string" {
				t.Errorf("enum %s is not a bare string type", ts.Name.Name)
			}
		}
	}

	// No hand-written decoder may sit on one either, in any file of the package.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		pf, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range pf.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || (fn.Name.Name != "UnmarshalJSON" && fn.Name.Name != "UnmarshalText") {
				continue
			}
			recv := fn.Recv.List[0].Type
			if st, ok := recv.(*ast.StarExpr); ok {
				recv = st.X
			}
			if id, ok := recv.(*ast.Ident); ok && enums[id.Name] {
				t.Errorf("%s: %s.%s can reject a value the bundle does not list", f, id.Name, fn.Name.Name)
			}
		}
	}
}

// TestUnknownEnumValuesDecode decodes the messages the muse driver actually reads, with a value
// no bundle lists in each enum it touches.
func TestUnknownEnumValuesDecode(t *testing.T) {
	const unknown = "zzzNotInBundle"
	for _, v := range CapabilityNameValues {
		if v == unknown {
			t.Fatalf("the bundle lists %q: pick another unknown value", unknown)
		}
	}

	var init InitializeResult
	raw := `{"experimentalApi":false,"grantedCapabilities":["sessionMcp","` + unknown + `"],` +
		`"museHome":"/h","platformFamily":"` + unknown + `","platformOs":"` + unknown + `",` +
		`"schema":{"fingerprint":"sha256:x","version":1},` +
		`"serverInfo":{"name":"muse","version":"9"},"userAgent":"ua"}`
	if err := json.Unmarshal([]byte(raw), &init); err != nil {
		t.Fatalf("initialize result with an unknown capability: %v", err)
	}
	if !Granted(&init, CapabilityNameSessionMCP) {
		t.Error("a known capability next to an unknown one was not seen as granted")
	}
	if len(init.GrantedCapabilities) != 2 || init.GrantedCapabilities[1] != unknown {
		t.Errorf("granted = %q, want the unknown value carried verbatim", init.GrantedCapabilities)
	}

	var ed ErrorData
	if err := json.Unmarshal([]byte(`{"kind":"capabilityRequired","capability":"`+unknown+`"}`), &ed); err != nil {
		t.Fatalf("error data with an unknown capability: %v", err)
	}
	if ed.Capability == nil || *ed.Capability != unknown {
		t.Errorf("capability = %v, want %q", ed.Capability, unknown)
	}

	var st SessionStatusChangedParams
	if err := json.Unmarshal([]byte(`{"sessionId":"s","status":"`+unknown+`"}`), &st); err != nil {
		t.Fatalf("status notification with an unknown status: %v", err)
	}

	var m ModelCatalogEntry
	if err := json.Unmarshal([]byte(`{"displayLabel":"d","isActive":false,"isDefault":false,"modelId":"m",`+
		`"providerId":"p","variants":["`+unknown+`"]}`), &m); err != nil {
		t.Fatalf("model row with an unknown effort: %v", err)
	}
}
