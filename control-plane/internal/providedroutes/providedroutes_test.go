package providedroutes

import (
	"testing"
	"time"
)

const arn = "arn:aws:iam::123456789012:role/af-model-route/bedrock"

// The fingerprint moves with exactly what decides who pays and where the calls go, and with
// nothing else (ADR 0129 decision 1). The allow-list and the budget are not inputs at all (Payer
// has no field for them); the store test shows an allow-list edit leaving a stored source's
// fingerprint alone.
func TestFingerprintCoversThePayerOnly(t *testing.T) {
	base := Payer{RoleARN: arn, ExternalIDVersion: 1, Regions: []string{"us-east-1", "us-west-2"}}
	fp := Fingerprint(base)
	if !fingerprintRe.MatchString(fp) {
		t.Fatalf("Fingerprint = %q, want 16 lower-case hex digits", fp)
	}
	for name, p := range map[string]Payer{
		"another role":        {RoleARN: "arn:aws:iam::123456789012:role/af-model-route/other", ExternalIDVersion: 1, Regions: base.Regions},
		"another account":     {RoleARN: "arn:aws:iam::210987654321:role/af-model-route/bedrock", ExternalIDVersion: 1, Regions: base.Regions},
		"external id rotated": {RoleARN: arn, ExternalIDVersion: 2, Regions: base.Regions},
		"a Region added":      {RoleARN: arn, ExternalIDVersion: 1, Regions: []string{"us-east-1", "us-west-2", "eu-west-1"}},
		"a Region removed":    {RoleARN: arn, ExternalIDVersion: 1, Regions: []string{"us-east-1"}},
		"Regions swapped out": {RoleARN: arn, ExternalIDVersion: 1, Regions: []string{"eu-west-1", "us-west-2"}},
		// Length-prefixed fields: a Region cannot be slid into the ARN's place.
		"fields shifted": {RoleARN: arn + "\n1", ExternalIDVersion: 1, Regions: base.Regions},
	} {
		if Fingerprint(p) == fp {
			t.Errorf("%s: the fingerprint did not change", name)
		}
	}
	for name, p := range map[string]Payer{
		"Regions reordered":  {RoleARN: arn, ExternalIDVersion: 1, Regions: []string{"us-west-2", "us-east-1"}},
		"a Region repeated":  {RoleARN: arn, ExternalIDVersion: 1, Regions: []string{"us-east-1", "us-west-2", "us-east-1"}},
		"padding and blanks": {RoleARN: " " + arn + " ", ExternalIDVersion: 1, Regions: []string{" us-east-1", "", "us-west-2 "}},
	} {
		if Fingerprint(p) != fp {
			t.Errorf("%s: the fingerprint changed, but the payer did not", name)
		}
	}
}

func TestBinding(t *testing.T) {
	fp := Fingerprint(Payer{RoleARN: arn, Regions: []string{"us-east-1"}})
	b := Bind("0123456789abcdef0123456789abcdef", fp)
	if id, got, ok := ParseBinding(b); !ok || id != "0123456789abcdef0123456789abcdef" || got != fp {
		t.Fatalf("ParseBinding(%q) = %q %q %v", b, id, got, ok)
	}
	for _, bad := range []string{"", "#", "0123456789abcdef", "0123456789abcdef#", "0123456789abcdef#00112233AABBCCDD",
		"0123456789abcdef#00112233aabbccdd#00", "p1@us-east-1#00112233aabbccdd", "../x#00112233aabbccdd"} {
		if _, _, ok := ParseBinding(bad); ok {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Payer{RoleARN: arn, Regions: []string{"us-east-1"}}
	allow := []string{"us.anthropic.claude-opus-5-5", "global.anthropic.claude-sonnet-5-5"}
	if err := Validate(SourceDeployment, ok, allow); err != nil {
		t.Fatalf("a valid deployment source: %v", err)
	}
	if err := Validate(SourceTenant, Payer{RoleARN: arn, ExternalIDVersion: 3, Regions: []string{"eu-west-1"}}, nil); err != nil {
		t.Fatalf("a valid tenant source with an empty list: %v", err)
	}
	for name, tc := range map[string]struct {
		source string
		p      Payer
		allow  []string
	}{
		"member source":             {"member", ok, nil},
		"not a role":                {SourceTenant, Payer{RoleARN: "arn:aws:iam::123456789012:user/x", Regions: ok.Regions}, nil},
		"short account":             {SourceTenant, Payer{RoleARN: "arn:aws:iam::1234:role/x", Regions: ok.Regions}, nil},
		"no Region":                 {SourceTenant, Payer{RoleARN: arn}, nil},
		"bad Region":                {SourceTenant, Payer{RoleARN: arn, Regions: []string{"us-east-1.evil.example"}}, nil},
		"deployment with an ext id": {SourceDeployment, Payer{RoleARN: arn, ExternalIDVersion: 1, Regions: ok.Regions}, nil},
		"negative version":          {SourceTenant, Payer{RoleARN: arn, ExternalIDVersion: -1, Regions: ok.Regions}, nil},
		"ARN in the list":           {SourceTenant, ok, []string{"arn:aws:bedrock:us-east-1:123456789012:application-inference-profile/x"}},
		"unpriced model":            {SourceTenant, ok, []string{"us.anthropic.claude-not-a-model"}},
		"folded spelling":           {SourceTenant, ok, []string{"claude-opus-5-5"}},
	} {
		if err := Validate(tc.source, tc.p, tc.allow); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRouteAndSource(t *testing.T) {
	for _, s := range []string{SourceTenant, SourceDeployment} {
		if SourceOf(RouteOf(s)) != s {
			t.Errorf("%s does not round-trip through its route", s)
		}
	}
	if SourceOf("bedrock") != "" || RouteOf("member") != "" || ValidSource("member") {
		t.Error("the member's own bedrock is not a provided source")
	}
}

// A call belongs to the UTC month it was admitted in, whatever the local clock says.
func TestAdmissionMonth(t *testing.T) {
	tokyo := time.FixedZone("JST", 9*3600)
	if got := AdmissionMonth(time.Date(2026, 12, 1, 8, 59, 59, 0, tokyo)); got != "2026-11" {
		t.Errorf("08:59:59 JST on 1 December is 23:59:59 UTC in November: %q", got)
	}
	if got := AdmissionMonth(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)); got != "2026-12" {
		t.Errorf("midnight UTC opens December: %q", got)
	}
}

func TestNormalizeAllowList(t *testing.T) {
	got := NormalizeAllowList([]string{" b", "a", "b", ""})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("NormalizeAllowList = %q", got)
	}
}
