// wire_golden_test.go — a golden of the KEY SET of the representative response DTOs the
// Console reads.
//
// Why it exists (ADR 0067 decision 6): the route table (routes_golden_test.go) only guards
// that an endpoint is there, not the shape of the JSON coming out of it. When a struct is
// moved into internal/, a retyped json tag, a field left behind or a mistaken type breaks
// the Console alone without the Go compiler making a sound. sessionWire has hit this three
// times (Title / driver / color, context, the exit fields).
//
// The point is to go red when a json tag changes, not to cover values — round-trip tests
// like session_wire_test.go own the values. This captures keys, the JSON-level type and
// omitempty, nothing else.
//
// Go type names are deliberately not captured: moving a type from main to internal/x always
// changes the name while the wire is unchanged, so recording names would turn every such
// move into a false red.
//
// To update it after an intentional wire change:
//
//	cd control-plane && go test -run TestWireShapeGolden -update-wire-golden ./...
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var updateWireGolden = flag.Bool("update-wire-golden", false,
	"rewrite testdata/wire.golden from the actual DTO shapes (only when the wire was changed on purpose)")

const wireGoldenPath = "testdata/wire.golden"

// wireGoldenTypes picks representatives of what the Console actually reads. Listing every
// DTO is not the goal — such a list is not maintained — so what belongs here is what breaks
// the screen when it breaks.
//
// Two routes are absent because neither has a struct to capture:
//   - GET /api/workspace and /api/workspace/stats build and return a map[string]any
//     (workspace_handlers.go workspacePayload, metrics.go workspaceStats).
//   - the /api/repos family is a pass-through proxy to the Agent, whose DTOs live in
//     workspace/agent and are captured by the wire_golden_test.go over there.
func wireGoldenTypes() []struct {
	name string
	typ  reflect.Type
} {
	return []struct {
		name string
		typ  reflect.Type
	}{
		// Session list / SSE. This is a relay that decodes the Agent's answer and re-emits
		// it, so a field missing here is dropped silently (it has happened three times).
		{"sessionWire", reflect.TypeOf(sessionWire{})},
		{"adminSessionRow", reflect.TypeOf(adminSessionRow{})},
		// Usage: the admin aggregates and the heatmap.
		{"usageTotal", reflect.TypeOf(usageTotal{})},
		{"usageHourlyResponse", reflect.TypeOf(usageHourlyResponse{})},
		// Notification centre.
		{"notificationDTO", reflect.TypeOf(notificationDTO{})},
		// Work-item inbox.
		{"workItemDTO", reflect.TypeOf(workItemDTO{})},
		{"workItemQueryDTO", reflect.TypeOf(workItemQueryDTO{})},
		{"workItemSessionDTO", reflect.TypeOf(workItemSessionDTO{})},
		// Memo queue / scheduled execution.
		{"memoDTO", reflect.TypeOf(memoDTO{})},
		{"scheduleDTO", reflect.TypeOf(scheduleDTO{})},
		// Version info behind the update toast and the restart badge.
		{"imageInfo", reflect.TypeOf(imageInfo{})},
	}
}

func TestWireShapeGolden(t *testing.T) {
	var got []string
	for _, e := range wireGoldenTypes() {
		got = append(got, wireShape(t, e.name, e.typ)...)
	}
	sort.Strings(got)

	if *updateWireGolden {
		writeWireGolden(t, wireGoldenPath, got)
		t.Logf("wrote %s (%d keys)", wireGoldenPath, len(got))
		return
	}
	assertGoldenLines(t, wireGoldenPath, got)
}

// TestWireShapeGoldenCoversSessionWire guards against capturing nothing while looking as if
// it captured something: if wireShape breaks by silently returning empty, the golden stays
// green and protects nothing.
func TestWireShapeGoldenCoversSessionWire(t *testing.T) {
	lines := wireShape(t, "sessionWire", reflect.TypeOf(sessionWire{}))
	for _, want := range []string{
		"sessionWire.exitCode number,omitempty",      // the docs/log/26 exit chip
		"sessionWire.color string,omitempty",         // SSM background colour
		"sessionWire.driver string,omitempty",        // managed-or-not
		"sessionWire.context raw,omitempty",          // ContextBar
		"sessionWire.backgroundBusy bool",            // background-running badge
		"sessionWire.locked bool,omitempty",          // deletion lock
		"sessionWire.workingCopyId string,omitempty", // working-copy generation
	} {
		if !containsLine(lines, want) {
			t.Errorf("wireShape does not return %q (a field that has been dropped by accident before)", want)
		}
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// --- shape extraction ---

var jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

// wireShape folds one type into lines of "<prefix>.<json key> <JSON type>[,omitempty]".
// Nesting is written "a.b.c" and a slice of structs "a[].b".
func wireShape(t *testing.T, name string, typ reflect.Type) []string {
	t.Helper()
	var out []string
	shapeInto(t, name, typ, map[reflect.Type]bool{}, &out)
	if len(out) == 0 {
		t.Fatalf("%s: not a single key was picked up (wireShape is broken)", name)
	}
	sort.Strings(out)
	return out
}

func shapeInto(t *testing.T, prefix string, typ reflect.Type, seen map[reflect.Type]bool, out *[]string) {
	t.Helper()
	typ = deref(typ)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%s: a non-struct type cannot be captured: %s", prefix, typ.Kind())
	}
	if seen[typ] {
		*out = append(*out, prefix+" <recursive>")
		return
	}
	seen[typ] = true
	defer delete(seen, typ)

	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue // never marshalled
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		key, opts, _ := strings.Cut(tag, ",")
		if key == "" {
			key = f.Name
		}
		// An embedded (anonymous) field with no json tag lifts its fields into the parent.
		if f.Anonymous && f.Tag.Get("json") == "" && deref(f.Type).Kind() == reflect.Struct {
			shapeInto(t, prefix, f.Type, seen, out)
			continue
		}
		suffix := ""
		if strings.Contains(","+opts+",", ",omitempty,") {
			suffix = ",omitempty"
		}
		emitField(t, prefix+"."+key, f.Type, suffix, seen, out)
	}
}

func emitField(t *testing.T, path string, typ reflect.Type, suffix string, seen map[reflect.Type]bool, out *[]string) {
	t.Helper()
	typ = deref(typ)
	// A type with its own MarshalJSON emits something unrelated to its fields, so stop
	// there (time.Time, json.RawMessage, custom encoders).
	if typ != reflect.TypeOf(json.RawMessage{}) &&
		(typ.Implements(jsonMarshalerType) || reflect.PointerTo(typ).Implements(jsonMarshalerType)) {
		*out = append(*out, path+" custom"+suffix)
		return
	}
	switch typ.Kind() {
	case reflect.Struct:
		shapeInto(t, path, typ, seen, out)
	case reflect.Slice, reflect.Array:
		elem := deref(typ.Elem())
		switch {
		case elem.Kind() == reflect.Uint8:
			// []byte is base64 and json.RawMessage is raw JSON; neither has its contents
			// captured.
			if typ == reflect.TypeOf(json.RawMessage{}) {
				*out = append(*out, path+" raw"+suffix)
			} else {
				*out = append(*out, path+" base64"+suffix)
			}
		case elem.Kind() == reflect.Struct:
			shapeInto(t, path+"[]", elem, seen, out)
		default:
			*out = append(*out, path+" ["+jsonKind(elem)+"]"+suffix)
		}
	case reflect.Map:
		*out = append(*out, path+" object"+suffix)
	default:
		*out = append(*out, path+" "+jsonKind(typ)+suffix)
	}
}

// jsonKind returns the JSON-level type rather than the Go type name. A name always changes
// when a type moves (main → internal/x), which would go red with an unchanged wire.
func jsonKind(typ reflect.Type) string {
	switch typ.Kind() {
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.String:
		return "string"
	case reflect.Interface:
		return "any"
	case reflect.Map:
		return "object"
	default:
		return typ.Kind().String()
	}
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func writeWireGolden(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	var b strings.Builder
	b.WriteString("# JSON key sets of the DTOs the Console reads. Generated - do not edit by hand.\n")
	b.WriteString("# Update: cd control-plane && go test -run TestWireShapeGolden -update-wire-golden ./...\n")
	b.WriteString("# Format: <type>.<key path> <JSON type>[,omitempty] ([]=array / raw=passed-through JSON)\n")
	fmt.Fprintf(&b, "# count: %d\n", len(lines))
	for _, ln := range lines {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
