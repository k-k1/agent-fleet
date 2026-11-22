package engine

import (
	"strings"
	"testing"
)

func TestParseClasses(t *testing.T) {
	list := ParseClasses(
		"l4|L4 24GB|21000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26;" +
			"l40s|L40S 48GB|44000|g6e.xlarge|4|30000-65536;" +
			"broken|no types|1000||4-8|1000-2000;" +
			"nonum|bad vram|lots|g6.xlarge|4-8|1000-2000;" +
			"l4|duplicate|21000|g6.xlarge|4-8|15000-65536")
	if len(list) != 2 {
		t.Fatalf("parsed %d rungs, want 2 (the malformed and the duplicate are dropped): %+v", len(list), list)
	}
	l4 := list[0]
	if l4.ID != "l4" || l4.VramMiB != 21000 || l4.UsdPerHour != 1.26 {
		t.Errorf("first rung = %+v", l4)
	}
	if got := strings.Join(l4.Types, ","); got != "g6.xlarge,g5.xlarge" {
		t.Errorf("types = %q", got)
	}
	if l4.VCpuMin != 4 || l4.VCpuMax != 8 || l4.MemMinMiB != 15000 || l4.MemMaxMiB != 65536 {
		t.Errorf("bounds = %+v", l4)
	}
	// A single number is both ends, and an absent price is 0 — never a made-up figure, because
	// the panel prints a price only when the operator declared one.
	if list[1].VCpuMin != 4 || list[1].VCpuMax != 4 {
		t.Errorf("single-number vcpu range = %d-%d", list[1].VCpuMin, list[1].VCpuMax)
	}
	if list[1].UsdPerHour != 0 {
		t.Errorf("undeclared price = %v, want 0", list[1].UsdPerHour)
	}
	if ParseClasses("") != nil {
		t.Error("an empty ladder must parse to nothing at all")
	}
}

// A price that does not parse must not cost the rung: it is a label, and dropping the hardware
// to protect a number nothing computes with is the wrong trade.
func TestParseClassesKeepsTheRungWhenOnlyThePriceIsWrong(t *testing.T) {
	list := ParseClasses("l4|L4|21000|g6.xlarge|4-8|15000-65536|free")
	if len(list) != 1 || list[0].UsdPerHour != 0 {
		t.Fatalf("got %+v, want the rung kept with no price", list)
	}
}

func TestClassByID(t *testing.T) {
	list := ParseClasses("a|A|1|t|1-2|1-2;b|B|2|t|1-2|1-2")
	// "" is the default and the default is the FIRST rung — that is what lets the stack declare
	// one without a second parameter naming it.
	if c, ok := ClassByID(list, ""); !ok || c.ID != "a" {
		t.Errorf("default = %+v %v", c, ok)
	}
	if c, ok := ClassByID(list, "b"); !ok || c.ID != "b" {
		t.Errorf("by id = %+v %v", c, ok)
	}
	if _, ok := ClassByID(list, "zzz"); ok {
		t.Error("an id nobody declared must not resolve")
	}
	if _, ok := ClassByID(nil, ""); ok {
		t.Error("no ladder must answer no rung")
	}
}

// The unknown must not be turned into a refusal: a deployment where nobody has measured
// anything would ask for a confirmation on every single model, which teaches people to click
// through the one that matters.
func TestClassFits(t *testing.T) {
	c := Class{VramMiB: 21000}
	for _, tc := range []struct {
		need int
		want bool
	}{{0, true}, {20000, true}, {21000, true}, {21001, false}} {
		if got := ClassFits(c, tc.need); got != tc.want {
			t.Errorf("fits(%d) = %v", tc.need, got)
		}
	}
	if !ClassFits(Class{}, 99999) {
		t.Error("a rung that declares no VRAM cannot say anything about fit")
	}
}
