package engine

import (
	"testing"
	"time"
)

func TestLifecyclePredicates(t *testing.T) {
	cases := []struct {
		lifecycle                 string
		external, remote, unowned bool
	}{
		{"", false, false, false},
		{" External ", true, false, true},
		{"REMOTE", false, true, true},
	}
	for _, c := range cases {
		d := Def{Lifecycle: c.lifecycle}
		if d.External() != c.external || d.Remote() != c.remote || d.NotManagedHere() != c.unowned {
			t.Errorf("%q: got external=%v remote=%v unowned=%v", c.lifecycle, d.External(), d.Remote(), d.NotManagedHere())
		}
	}
}

func TestDefaults(t *testing.T) {
	d := Def{Classes: "a|b", Offers: "  "}
	if d.OffersSpec() != "a|b" || d.OfferBudget() != 300*time.Second || d.APIFamily() != APIChat {
		t.Errorf("defaults wrong: %q %v %q", d.OffersSpec(), d.OfferBudget(), d.APIFamily())
	}
	if !ImageProviderServable(Def{API: APIImages, Provider: "comfy"}) || ImageProviderServable(Def{API: APIImages, Provider: "sdcpp"}) {
		t.Error("image provider vocabulary changed")
	}
}
