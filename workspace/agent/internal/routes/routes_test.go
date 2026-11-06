package routes

import (
	"slices"
	"strings"
	"testing"
)

func TestCatalogueShape(t *testing.T) {
	var ids []string
	for _, r := range Catalogue() {
		ids = append(ids, r.ID)
		if r.Wire == "" || r.Credential == "" {
			t.Errorf("%s: wire %q credential %q, both required", r.ID, r.Wire, r.Credential)
		}
	}
	if !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != len(ids) {
		t.Errorf("ids = %v, want sorted and unique", ids)
	}
}

func TestCheckStatic(t *testing.T) {
	sel := func(id string) *Selection { return &Selection{Route: id} }
	for name, tc := range map[string]struct {
		sel     *Selection
		allowed []string
		code    string
	}{
		"no selection, no adapter":   {nil, nil, ""},
		"no selection, with adapter": {nil, []string{"anthropic-api"}, ""},
		"empty id":                   {sel(" "), []string{"anthropic-api"}, CodeBadRoute},
		"unknown id":                 {sel("nope"), []string{"anthropic-api"}, CodeBadRoute},
		"exact match only":           {sel("ANTHROPIC-API"), []string{"anthropic-api"}, CodeBadRoute},
		"known but harness has none": {sel("anthropic-api"), nil, CodeRouteUnsupported},
		"known but not on the list":  {sel("bedrock"), []string{"anthropic-api"}, CodeRouteUnsupported},
		"on the list":                {sel("anthropic-api"), []string{"anthropic-api"}, ""},
		"on the list, padded":        {sel(" anthropic-api "), []string{"anthropic-api"}, ""},
	} {
		code, err := CheckStatic("claude", tc.sel, tc.allowed)
		if code != tc.code || (code == "") != (err == nil) {
			t.Errorf("%s: code %q err %v, want %q", name, code, err, tc.code)
		}
	}
	if _, err := CheckStatic("opencode", sel("anthropic-subscription"), []string{"opencode-zen"}); err == nil ||
		!strings.Contains(err.Error(), "opencode") || !strings.Contains(err.Error(), "opencode-zen") {
		t.Errorf("the refusal should name the kind and what it accepts: %v", err)
	}
}

func TestSelectionCloneAndNormalize(t *testing.T) {
	var nilSel *Selection
	if nilSel.Clone() != nil || nilSel.Normalize() != nil {
		t.Error("nil must stay nil")
	}
	s := &Selection{Route: "a", Connection: "b"}
	c := s.Clone()
	c.Connection = "x"
	if s.Connection != "b" {
		t.Error("Clone aliases")
	}
}

func TestAWSConnectionRoundTripAndRefusals(t *testing.T) {
	c := AWSConnection("0123abcd", "us-east-1")
	if c != "0123abcd@us-east-1" {
		t.Fatalf("AWSConnection = %q", c)
	}
	if id, region, ok := ParseAWSConnection(c); !ok || id != "0123abcd" || region != "us-east-1" {
		t.Fatalf("ParseAWSConnection = %q %q %v", id, region, ok)
	}
	if id, region, ok := ParseAWSConnection("0123abcd@"); !ok || id != "0123abcd" || region != "" {
		t.Fatalf("an empty region means the profile's own: %q %q %v", id, region, ok)
	}
	for _, bad := range []string{"", "@us-east-1", "no-at-sign", "a@b@c", "a b@us-east-1", "a@us east", "a@us-east-1.evil.example", "../x@us-east-1"} {
		if _, _, ok := ParseAWSConnection(bad); ok {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

// The stored form carries the identity it was bound to; anything else after the region is refused.
func TestBoundAWSConnection(t *testing.T) {
	c := BindAWSConnection("0123abcd", "us-east-1", "00112233aabbccdd")
	if c != "0123abcd@us-east-1#00112233aabbccdd" {
		t.Fatalf("BindAWSConnection = %q", c)
	}
	if id, region, fp, ok := ParseBoundAWSConnection(c); !ok || id != "0123abcd" || region != "us-east-1" || fp != "00112233aabbccdd" {
		t.Fatalf("ParseBoundAWSConnection = %q %q %q %v", id, region, fp, ok)
	}
	if id, region, ok := ParseAWSConnection(c); !ok || id != "0123abcd" || region != "us-east-1" {
		t.Fatalf("ParseAWSConnection of a bound form = %q %q %v", id, region, ok)
	}
	if _, _, fp, ok := ParseBoundAWSConnection("0123abcd@us-east-1"); !ok || fp != "" {
		t.Fatalf("unbound: %q %v", fp, ok)
	}
	for _, bad := range []string{"0123abcd@us-east-1#", "0123abcd@us-east-1#XYZ", "0123abcd@us-east-1#00112233AABBCCDD",
		"0123abcd@us-east-1#00112233aabbccdd#00112233aabbccdd", "0123abcd#00112233aabbccdd"} {
		if _, _, _, ok := ParseBoundAWSConnection(bad); ok {
			t.Errorf("%q was accepted", bad)
		}
	}
}
