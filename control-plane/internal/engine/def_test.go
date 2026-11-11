package engine

import (
	"encoding/json"
	"strings"
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

func TestOriginIsNeverInJSON(t *testing.T) {
	var tbl Table
	if err := json.Unmarshal([]byte(`{"engines":[{"key":"x","url":"http://x","origin":"panel","Origin":"env"}]}`), &tbl); err != nil {
		t.Fatal(err)
	}
	if got := tbl.Engines[0].Origin; got != "" {
		t.Errorf("an operator-written origin was read: %q", got)
	}
	b, _ := json.Marshal(Def{Key: "x", Origin: OriginPanel})
	if strings.Contains(strings.ToLower(string(b)), "origin") {
		t.Errorf("origin leaked into JSON: %s", b)
	}
}

func TestIngestDefOKAndConstants(t *testing.T) {
	if (IngestDef{}).OK() || (IngestDef{TaskDef: "t"}).OK() || (IngestDef{Subnets: []string{"s"}}).OK() || (IngestDef{TaskDef: " ", Subnets: []string{"s"}}).OK() {
		t.Error("incomplete ingest block reported OK")
	}
	if !(IngestDef{TaskDef: "t", Subnets: []string{"s"}}).OK() {
		t.Error("complete ingest block not OK")
	}
	if OriginEnv != "env" || OriginPanel != "panel" || LifecycleExternal != "external" || LifecycleRemote != "remote" {
		t.Error("stored string values changed")
	}
	if (Def{OfferBudgetSec: 7}).OfferBudget() != 7*time.Second {
		t.Error("explicit offer budget ignored")
	}
}
