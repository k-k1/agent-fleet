// Package wiretest — the shared harness for showing that a map → struct conversion did
// not change the wire by a single byte (CONTRACT-MAP, leg 3).
//
// It is imported from tests only, and that it stays out of the product binary's
// dependency graph is checked mechanically rather than asserted:
// TestWireMapScanExclusionsAreJustified (wiremap_golden_test.go) runs `go list -deps`.
//
// Why one shared harness. Conversion keeps spreading into internal packages, and wire
// types are unexported, so equivalence can only be measured INSIDE the package that owns
// them. Copying the harness would leave many copies within the module, and they drift the
// moment one of them is fixed (ops kit §0.5, "hand-written copies drift").
//
// It is kept byte-identical with the package of the same name under workspace/agent. A
// cross-module shared package was declined (ADR 0012 decision 3), so two copies are
// necessary; wiretest_dup_test.go is what stops them drifting (byte comparison of the
// shared region, plus matching file-name sets).
//
// All five known traps have the same shape — the key and the value look the same and are
// not:
//
//  1. omitempty          — a map omits a key by not setting it; a struct emits the zero
//  2. nil vs empty       — a nil slice is null, []T{} is []
//  3. numeric precision  — a large int64 received as float64 loses digits
//  4. key spelling       — a missing json tag emits Go's exported name as-is
//  5. absence vs zero    — collapsed together when the Console tests `if (x.foo)`
//
// A sixth is the "swap two fields of the same type" mutation the ops kit §4 requires of
// every track: the same tool catches it, and nothing else can — the types match, so the
// type checker can never fire.
//
// Usage, once per conversion site:
//
//	wiretest.AssertEquiv(t, "HandleServersGet", inputs,
//	    func(in fixture) any { /* copy the old map literal verbatim */ },
//	    func(in fixture) any { /* build the new struct */ })
//
// Copy the old map literal into the test rather than deleting it: it is the only
// reference implementation, and without it there is no baseline for "unchanged".
package wiretest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// ===== shared region starts here (byte-identical in control-plane and workspace/agent) =====
// Nothing below this line may differ between the two modules by a single byte;
// wiretest_dup_test.go checks it. Above the sentinel are only the package clause and the
// imports, which cannot match because the module paths differ.

// Mode records which comparison succeeded. Reports have to say which one it was.
type Mode string

const (
	// ModeBytes: the byte strings matched as they were.
	ModeBytes Mode = "bytes"
	// ModeParsed: the bytes differed but a deep comparison of the parsed values matched.
	// A struct is written in declaration order and a map in ascending key order, so a
	// difference in key order alone is not a defect.
	ModeParsed Mode = "parsed"
)

// TB is the part of *testing.T this harness uses. It is an interface so the self-check
// (does the harness really catch the traps?) can pass in a recording implementation.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
}

type Result struct {
	Name  string
	Cases int
	Modes map[Mode]int
}

func (r Result) String() string {
	return fmt.Sprintf("%s: %d ケース (bytes=%d parsed=%d)",
		r.Name, r.Cases, r.Modes[ModeBytes], r.Modes[ModeParsed])
}

// AssertEquiv asserts that the old map and the new struct marshal to the same JSON for
// the same input.
//
// The harness always prepends the zero-value input itself. Traps 1, 3 and 5 only appear
// at the zero value, so a caller who simply forgets to write that case silences the
// check — closed by machine rather than by convention.
func AssertEquiv[In any](t TB, name string, inputs []In, oldFn, newFn func(In) any) Result {
	t.Helper()
	res := Result{Name: name, Modes: map[Mode]int{}}

	var zero In
	all := make([]In, 0, len(inputs)+1)
	all = append(all, zero) // the zero value is always measured
	all = append(all, inputs...)
	res.Cases = len(all)

	for i, in := range all {
		oldB, err := json.Marshal(oldFn(in))
		if err != nil {
			t.Errorf("%s[case %d]: 旧 map の Marshal が失敗した: %v", name, i, err)
			continue
		}
		newB, err := json.Marshal(newFn(in))
		if err != nil {
			t.Errorf("%s[case %d]: 新 struct の Marshal が失敗した: %v", name, i, err)
			continue
		}
		if bytes.Equal(oldB, newB) {
			res.Modes[ModeBytes]++
			continue
		}
		oldV, err := decodeValue(oldB)
		if err != nil {
			t.Errorf("%s[case %d]: 旧 JSON を読み戻せない: %v", name, i, err)
			continue
		}
		newV, err := decodeValue(newB)
		if err != nil {
			t.Errorf("%s[case %d]: 新 JSON を読み戻せない: %v", name, i, err)
			continue
		}
		if diffs := valueDiff("", oldV, newV); len(diffs) > 0 {
			t.Errorf("%s[case %d]: ワイヤが変わった\n  旧: %s\n  新: %s\n  差:\n    %s",
				name, i, oldB, newB, strings.Join(diffs, "\n    "))
			continue
		}
		// Key order only. A struct is written in declaration order and a map in
		// ascending order, so this path is not a defect.
		res.Modes[ModeParsed]++
	}
	return res
}

// decodeValue reads with UseNumber. The default float64 would make 1 and 1.0 equal and
// swallow an int64 losing digits, silencing trap 3.
func decodeValue(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// valueDiff returns the difference between two decoded JSON values as "which key differs
// how". Presence of a key, null vs empty, and the spelling of a number are reported as
// separate differences rather than collapsed together.
func valueDiff(path string, oldV, newV any) []string {
	at := func() string {
		if path == "" {
			return "(root)"
		}
		return path
	}
	switch o := oldV.(type) {
	case map[string]any:
		n, ok := newV.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: オブジェクトが %T になった", at(), newV)}
		}
		var keys []string
		seen := map[string]bool{}
		for k := range o {
			keys = append(keys, k)
			seen[k] = true
		}
		for k := range n {
			if !seen[k] {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		var out []string
		for _, k := range keys {
			ov, oOK := o[k]
			nv, nOK := n[k]
			p := k
			if path != "" {
				p = path + "." + k
			}
			switch {
			case oOK && !nOK:
				out = append(out, fmt.Sprintf("%s: キーが消えた（旧 %v）", p, ov))
			case !oOK && nOK:
				out = append(out, fmt.Sprintf("%s: キーが増えた（新 %v）", p, nv))
			default:
				out = append(out, valueDiff(p, ov, nv)...)
			}
		}
		return out
	case []any:
		n, ok := newV.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: 配列が %T になった（null と [] の取り違えを疑う）", at(), newV)}
		}
		if len(o) != len(n) {
			return []string{fmt.Sprintf("%s: 要素数 %d → %d", at(), len(o), len(n))}
		}
		var out []string
		for i := range o {
			out = append(out, valueDiff(fmt.Sprintf("%s[%d]", path, i), o[i], n[i])...)
		}
		return out
	default:
		if !reflect.DeepEqual(oldV, newV) {
			return []string{fmt.Sprintf("%s: %s → %s", at(), lit(oldV), lit(newV))}
		}
		return nil
	}
}

func lit(v any) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%v (%T)", v, v)
}

// --- below: the harness's own self-check ---
//
// "Zero hits" and "green" are only accepted once the tool itself has been shown to work
// (README §4). A harness that cannot catch the traps is worth nothing, so each trap is
// confirmed by feeding it a deliberately broken conversion and watching it go red.

// Recorder is the recording implementation of TB. It only counts Errorf.
type Recorder struct{ errs []string }

func (r *Recorder) Helper() {}
func (r *Recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

// Errs returns the recorded Errorf bodies. A control outside this package needs it to see
// whether something was actually reported; the field itself stays unexported.
func (r *Recorder) Errs() []string { return r.errs }
