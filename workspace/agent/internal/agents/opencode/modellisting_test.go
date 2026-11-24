package opencode

import (
	"encoding/json"
	"reflect"
	"testing"
)

// stubLimitsFromListing reads one GET /api/model answer for the contract tests: the limits of the
// stub provider's models, and whether the listing is complete enough to read, meaning the
// directory's instance has finished activating and every stub model in want is listed.
//
// From 2.4.0 the listing fills in steps while the instance boots (measured on 2.4.1, polling every
// 0.1 s: 200 with no data for up to 2 s, then opencode's own models alone for 0.3-1 s, then the stub
// provider's), so neither an empty listing nor one without the stub is an answer yet; 2.3.x held the
// request until the whole listing was there. A test that expects opencode to refuse the provider
// (a context-only limit up to 2.2.5) passes no want: on those versions the first listing with any
// model in it is the whole one.
func stubLimitsFromListing(raw []byte, want ...string) (limits map[string]map[string]float64, ready bool, err error) {
	var listed struct {
		Data []struct {
			ID         string             `json:"id"`
			ProviderID string             `json:"providerID"`
			Limit      map[string]float64 `json:"limit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, false, err
	}
	limits = map[string]map[string]float64{}
	for _, m := range listed.Data {
		if m.ProviderID == "stub" {
			limits[m.ID] = m.Limit
		}
	}
	if len(listed.Data) == 0 {
		return limits, false, nil
	}
	for _, id := range want {
		if _, ok := limits[id]; !ok {
			return limits, false, nil
		}
	}
	return limits, true, nil
}

// The answers below are trimmed from real serves of one config (TestContractV2ModelLimits*'s stub
// provider), so a version whose listing reads differently fails here without the binary.
func TestStubLimitsFromListing(t *testing.T) {
	const builtin = `{"id":"tidepool-alpha-free","providerID":"opencode","status":"active","limit":{"context":1048576,"output":131072}}`
	stub := `{"id":"nolimit","providerID":"stub","status":"active","limit":{"context":200000,"output":32000}},` +
		`{"id":"both","providerID":"stub","status":"active","limit":{"context":20000,"output":2000}},` +
		`{"id":"ctxonly","providerID":"stub","status":"active","limit":{"context":20000,"output":32000}}`
	want := map[string]map[string]float64{
		"nolimit": {"context": 200000, "output": 32000},
		"both":    {"context": 20000, "output": 2000},
		"ctxonly": {"context": 20000, "output": 32000},
	}
	all := []string{"nolimit", "both", "ctxonly"}
	for _, c := range []struct {
		name  string
		raw   string
		want  []string
		limit map[string]map[string]float64
		ready bool
	}{
		// 2.4.x while the instance boots: a 200, not a 503, and nothing in it.
		{"2.4 activating", `{"location":{"directory":"/w"},"data":[]}`, all, map[string]map[string]float64{}, false},
		// 2.4.x a step later: opencode's own models are listed, the configured provider is not yet.
		{"2.4 stub still loading", `{"location":{"directory":"/w"},"data":[` + builtin + `]}`, all, map[string]map[string]float64{}, false},
		// 2.3.2 and 2.4.1 once activated: the same entries, limits included.
		{"activated", `{"location":{"directory":"/w"},"data":[` + builtin + `,` + stub + `]}`, all, want, true},
		// A provider opencode refused (a context-only limit up to 2.2.5): no stub is expected, so
		// the first listing with a model in it is the answer.
		{"provider dropped", `{"location":{"directory":"/w"},"data":[` + builtin + `]}`, nil, map[string]map[string]float64{}, true},
		{"provider dropped, still activating", `{"location":{"directory":"/w"},"data":[]}`, nil, map[string]map[string]float64{}, false},
	} {
		got, ready, err := stubLimitsFromListing([]byte(c.raw), c.want...)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if ready != c.ready || !reflect.DeepEqual(got, c.limit) {
			t.Errorf("%s: got %v ready=%v, want %v ready=%v", c.name, got, ready, c.limit, c.ready)
		}
	}
	if _, _, err := stubLimitsFromListing([]byte(`{"_tag":"UnauthorizedError"`)); err == nil {
		t.Error("a truncated answer parsed without an error")
	}
}
