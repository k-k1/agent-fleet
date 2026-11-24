package opencode

import (
	"encoding/json"
	"reflect"
	"testing"
)

// stubLimitsFromListing reads one GET /api/model answer for the contract tests: the limits of the
// stub provider's models, and whether the directory's instance had finished activating.
//
// An empty listing means "still activating", not "no models": from 2.4.0 the endpoint answers 200
// with no data while the instance boots (measured on 2.4.0 and 2.4.1: about 3 s of `data: []`
// before all 13 models appear at once), where 2.3.x held the request until it could answer. A
// provider opencode rejected is still a finished listing, since opencode's own models are there.
func stubLimitsFromListing(raw []byte) (limits map[string]map[string]float64, activated bool, err error) {
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
	return limits, len(listed.Data) > 0, nil
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
	for _, c := range []struct {
		name      string
		raw       string
		want      map[string]map[string]float64
		activated bool
	}{
		// 2.4.x while the instance boots: a 200, not a 503, and nothing in it.
		{"2.4 activating", `{"location":{"directory":"/w"},"data":[]}`, map[string]map[string]float64{}, false},
		// 2.3.2 and 2.4.1 once activated: the same entries, limits included.
		{"activated", `{"location":{"directory":"/w"},"data":[` + builtin + `,` + stub + `]}`, want, true},
		// A provider opencode refused (a context-only limit up to 2.2.5): activated, stub absent.
		{"provider dropped", `{"location":{"directory":"/w"},"data":[` + builtin + `]}`, map[string]map[string]float64{}, true},
	} {
		got, activated, err := stubLimitsFromListing([]byte(c.raw))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if activated != c.activated || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v activated=%v, want %v activated=%v", c.name, got, activated, c.want, c.activated)
		}
	}
	if _, _, err := stubLimitsFromListing([]byte(`{"_tag":"UnauthorizedError"`)); err == nil {
		t.Error("a truncated answer parsed without an error")
	}
}
