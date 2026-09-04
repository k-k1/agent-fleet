// wiretest_test.go — the harness's own self-diagnosis.
//
// A green result is only worth taking once the tool that produced it has been validated
// (operating kit §4), and a harness that cannot catch a trap is worth nothing: every trap
// gets a deliberately broken conversion aimed at it and must turn red. Faithful conversions
// must still pass — a tool that reddens everything protects nothing.
package wiretest

import (
	"strings"
	"testing"
)

// ===== shared region starts here (byte-identical in control-plane and workspace/agent) =====
// Below this line the two modules must not differ by a single byte; wiretest_dup_test.go
// checks it. Above the sentinel are only the package clause and imports, which cannot match
// because the module paths differ.

// equivFixture is the input to the self-diagnosis: only values shaped so a trap shows up.
type equivFixture struct {
	Name string
	Note string // same type as Name; trap ⑥ (swapped fields) needs it
	N    int64  // large enough that receiving it as float64 loses digits
	Tags []string
	Flag bool
}

// equivInputs holds only non-zero shapes; the harness adds the zero value itself.
func equivInputs() []equivFixture {
	return []equivFixture{
		{Name: "a", Note: "b", N: 1 << 62, Tags: []string{"x"}, Flag: true},
		{Name: "", Note: "b", N: 0, Tags: []string{}, Flag: false}, // empty slice, not nil
	}
}

// oldFaithful plays the pre-move map literal: the reference implementation.
func oldFaithful(f equivFixture) any {
	m := map[string]any{
		"name": f.Name,
		"note": f.Note,
		"n":    f.N,
		"tags": f.Tags,
	}
	if f.Flag { // conditional key, i.e. the omitempty equivalent
		m["flag"] = true
	}
	return m
}

// equivNewFaithful is a faithful conversion. Its declaration order differs from the map's
// ascending key order, so the comparison goes down the parsed path.
type equivNewFaithful struct {
	Name string   `json:"name"`
	Note string   `json:"note"`
	N    int64    `json:"n"`
	Tags []string `json:"tags"`
	Flag bool     `json:"flag,omitempty"`
}

// equivNewSorted is a second faithful conversion, declared in ascending key order, so it
// must match on the bytes path.
type equivNewSorted struct {
	Flag bool     `json:"flag,omitempty"`
	N    int64    `json:"n"`
	Name string   `json:"name"`
	Note string   `json:"note"`
	Tags []string `json:"tags"`
}

func newFaithful(f equivFixture) any {
	return equivNewFaithful{Name: f.Name, Note: f.Note, N: f.N, Tags: f.Tags, Flag: f.Flag}
}

func newSorted(f equivFixture) any {
	return equivNewSorted{Name: f.Name, Note: f.Note, N: f.N, Tags: f.Tags, Flag: f.Flag}
}

// TestWireEquivAcceptsFaithfulConversion is the positive side: a faithful conversion has to
// pass, because a tool that never passes is only ever red and protects nothing. It also
// checks that both the bytes and the parsed path actually run.
func TestWireEquivAcceptsFaithfulConversion(t *testing.T) {
	rec := &Recorder{}
	got := AssertEquiv(rec, "faithful", equivInputs(), oldFaithful, newFaithful)
	if len(rec.errs) > 0 {
		t.Fatalf("reddened a faithful conversion (the harness is too strict):\n%s", strings.Join(rec.errs, "\n"))
	}
	if got.Modes[ModeParsed] != got.Cases {
		t.Errorf("the declaration order differs from the map's ascending key order, so every case must be parsed: %s", got)
	}

	rec2 := &Recorder{}
	got2 := AssertEquiv(rec2, "faithful-sorted", equivInputs(), oldFaithful, newSorted)
	if len(rec2.errs) > 0 {
		t.Fatalf("reddened a faithful conversion declared in ascending key order:\n%s", strings.Join(rec2.errs, "\n"))
	}
	if got2.Modes[ModeBytes] != got2.Cases {
		t.Errorf("declared in ascending key order, so every case must match on bytes: %s", got2)
	}
	t.Logf("measured comparison modes: %s / %s", got, got2)
}

// TestWireEquivCatchesEachTrap is the negative control: one broken conversion per trap,
// each of which must turn red for that trap and no other. A trap the harness misses is one
// it is powerless against, and the claim "the wire did not change" is a lie by that much.
func TestWireEquivCatchesEachTrap(t *testing.T) {
	traps := []struct {
		trap string
		newF func(equivFixture) any
		want string // must appear in the diff, so a red for the wrong trap is not mistaken for a pass
	}{
		{
			// ① forgotten omitempty: the zero-value case grows a "flag": false.
			trap: "① forgotten omitempty",
			newF: func(f equivFixture) any {
				return struct {
					Name string   `json:"name"`
					Note string   `json:"note"`
					N    int64    `json:"n"`
					Tags []string `json:"tags"`
					Flag bool     `json:"flag"`
				}{f.Name, f.Note, f.N, f.Tags, f.Flag}
			},
			want: "flag: key appeared",
		},
		{
			// ①⑤ inverted: an omitempty that should not be there makes the key vanish
			// on the zero value. This is also the control for ⑤ (key absence versus
			// zero value) — a Console reading `if (x.foo)` collapses "no key" and `""`
			// into the same thing, but the wire does not, and the harness reports them
			// as two different diffs.
			trap: "①⑤ an omitempty that should not be there (key absence versus zero value)",
			newF: func(f equivFixture) any {
				return struct {
					Name string   `json:"name,omitempty"`
					Note string   `json:"note"`
					N    int64    `json:"n"`
					Tags []string `json:"tags"`
					Flag bool     `json:"flag,omitempty"`
				}{f.Name, f.Note, f.N, f.Tags, f.Flag}
			},
			want: "name: key disappeared",
		},
		{
			// ② nil versus empty: normalising a nil slice to [] turns null into [].
			trap: "② normalising nil into an empty slice",
			newF: func(f equivFixture) any {
				tags := f.Tags
				if tags == nil {
					tags = []string{}
				}
				return equivNewFaithful{f.Name, f.Note, f.N, tags, f.Flag}
			},
			want: "tags",
		},
		{
			// ③ numeric type: received as float64, an int64 loses digits at 1<<62.
			trap: "③ receiving an int64 as float64",
			newF: func(f equivFixture) any {
				return struct {
					Name string   `json:"name"`
					Note string   `json:"note"`
					N    float64  `json:"n"`
					Tags []string `json:"tags"`
					Flag bool     `json:"flag,omitempty"`
				}{f.Name, f.Note, float64(f.N), f.Tags, f.Flag}
			},
			want: "n:",
		},
		{
			// ④ missing json tag: the Go exported name goes out on the wire as-is.
			trap: "④ forgotten json tag",
			newF: func(f equivFixture) any {
				return struct {
					Name string
					Note string   `json:"note"`
					N    int64    `json:"n"`
					Tags []string `json:"tags"`
					Flag bool     `json:"flag,omitempty"`
				}{f.Name, f.Note, f.N, f.Tags, f.Flag}
			},
			want: "Name: key appeared",
		},
		{
			// ⑥ swap two fields of the same type (the mutation README §4 requires).
			// The types line up, so the compiler never says a word.
			trap: "⑥ swapping two fields of the same type",
			newF: func(f equivFixture) any {
				return equivNewFaithful{Name: f.Note, Note: f.Name, N: f.N, Tags: f.Tags, Flag: f.Flag}
			},
			want: "name:",
		},
	}

	for _, tc := range traps {
		t.Run(tc.trap, func(t *testing.T) {
			rec := &Recorder{}
			AssertEquiv(rec, tc.trap, equivInputs(), oldFaithful, tc.newF)
			if len(rec.errs) == 0 {
				t.Fatalf("let trap %q through = the harness is powerless against this trap", tc.trap)
			}
			joined := strings.Join(rec.errs, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("trap %q did go red, but for the wrong reason (it may be red from tripping another trap)\n"+
					"  must contain %q\n  got:\n%s", tc.trap, tc.want, joined)
			}
		})
	}
}

// TestWireEquivAlwaysMeasuresZeroValue pins the harness's central guarantee. Traps ①③⑤
// only appear on zero-value input, so the check must not go silent when the caller forgets
// to write a zero-value case: with no inputs at all, the trap is still caught.
func TestWireEquivAlwaysMeasuresZeroValue(t *testing.T) {
	noOmitEmpty := func(f equivFixture) any {
		return struct {
			Name string   `json:"name"`
			Note string   `json:"note"`
			N    int64    `json:"n"`
			Tags []string `json:"tags"`
			Flag bool     `json:"flag"`
		}{f.Name, f.Note, f.N, f.Tags, f.Flag}
	}
	rec := &Recorder{}
	res := AssertEquiv(rec, "zero-only", nil, oldFaithful, noOmitEmpty)
	if res.Cases != 1 {
		t.Fatalf("even with nil inputs the zero value must be measured as 1 case: %d", res.Cases)
	}
	if len(rec.errs) == 0 {
		t.Fatal("the zero-value case was not added = the check goes silent when the caller forgets to write one")
	}
}
