package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Role chaining (issue #1109): the profile type "assume a role from another Settings
// profile". These tests pin what the API accepts, what the store refuses, and what the
// bridge exports, so a chained profile can never be written with a source that is not an
// SSO profile of the same member.

const (
	chainSSOStart = "https://example.awsapps.com/start"
	chainRoleARN  = "arn:aws:iam::210987654321:role/deploy"
)

func TestSSMAssumeRoleProfileAPI(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			e := newSSMAPIEnv(t, st)
			var src ssmProfileDTO
			if c := e.do(t, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: "sso-main", StartURL: chainSSOStart, SSORegion: "us-east-1", AccountID: "123456789012", RoleName: "Dev"}, &src); c != http.StatusCreated {
				t.Fatalf("create source = %d", c)
			}
			if src.Kind != "sso" {
				t.Errorf("an sso profile reads kind %q, want sso", src.Kind)
			}

			ok := ssmProfileDTO{Kind: "assume_role", Label: "deploy", SourceProfileID: src.ID, RoleARN: chainRoleARN,
				ExternalID: "ext-123", SessionName: "af", DurationSeconds: 3600, Region: "eu-west-1"}
			var made ssmProfileDTO
			if c := e.do(t, "POST", "/api/ssm/profiles", ok, &made); c != http.StatusCreated {
				t.Fatalf("create chained = %d", c)
			}
			if made.AccountID != "210987654321" || made.Kind != "assume_role" || made.SourceProfileID != src.ID {
				t.Errorf("chained = %+v: the account must come from the role ARN", made)
			}

			bad := map[string]func(d *ssmProfileDTO){
				"role arn not a role":      func(d *ssmProfileDTO) { d.RoleARN = "arn:aws:iam::210987654321:user/bob" },
				"role arn with newline":    func(d *ssmProfileDTO) { d.RoleARN = chainRoleARN + "\ncredential_process = x" },
				"role arn trailing slash":  func(d *ssmProfileDTO) { d.RoleARN = chainRoleARN + "/" },
				"account differs from arn": func(d *ssmProfileDTO) { d.AccountID = "111111111111" },
				"external id with newline": func(d *ssmProfileDTO) { d.ExternalID = "a\nb" },
				"session name too short":   func(d *ssmProfileDTO) { d.SessionName = "x" },
				"duration below minimum":   func(d *ssmProfileDTO) { d.DurationSeconds = 60 },
				"portal on an assume-role": func(d *ssmProfileDTO) { d.StartURL = chainSSOStart },
				"no source":                func(d *ssmProfileDTO) { d.SourceProfileID = "" },
				"unknown source":           func(d *ssmProfileDTO) { d.SourceProfileID = "nope" },
				"source is a chained":      func(d *ssmProfileDTO) { d.SourceProfileID = made.ID },
				"region with newline":      func(d *ssmProfileDTO) { d.Region = "a\nb" },
				"unknown kind":             func(d *ssmProfileDTO) { d.Kind = "keys" },
			}
			for what, mut := range bad {
				d := ok
				d.Label = "x-" + strings.ReplaceAll(what, " ", "-")
				mut(&d)
				if c := e.do(t, "POST", "/api/ssm/profiles", d, nil); c != http.StatusBadRequest {
					t.Errorf("%s: status %d, want 400", what, c)
				}
			}
			// An sso profile with assume-role fields is a mistake, not something to ignore.
			if c := e.do(t, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: "mixed", StartURL: chainSSOStart, SSORegion: "us-east-1", RoleARN: chainRoleARN}, nil); c != http.StatusBadRequest {
				t.Errorf("sso with roleArn: status %d, want 400", c)
			}

			// The source cannot go while a chained profile uses it, nor become a chained profile.
			var refused ssmProfileInUseResp
			if c := e.do(t, "DELETE", "/api/ssm/profiles/"+src.ID, nil, &refused); c != http.StatusConflict {
				t.Fatalf("delete of a source = %d, want 409", c)
			}
			if refused.Error.Code != "ssm_profile_in_use" || strings.Join(refused.Profiles, ",") != "deploy" {
				t.Errorf("refusal = %+v, want ssm_profile_in_use naming deploy", refused)
			}
			turn := ssmProfileDTO{Kind: "assume_role", Label: "sso-main", SourceProfileID: made.ID, RoleARN: chainRoleARN}
			if c := e.do(t, "PUT", "/api/ssm/profiles/"+src.ID, turn, nil); c != http.StatusBadRequest {
				t.Errorf("turning the source into a chain onto the chained profile = %d, want 400 (source is chained)", c)
			}
			turn.SourceProfileID = "elsewhere"
			var sec ssmProfileDTO
			if c := e.do(t, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: "other", StartURL: chainSSOStart, SSORegion: "us-east-1"}, &sec); c != http.StatusCreated {
				t.Fatal(c)
			}
			turn.SourceProfileID = sec.ID
			if c := e.do(t, "PUT", "/api/ssm/profiles/"+src.ID, turn, &refused); c != http.StatusConflict {
				t.Errorf("turning a used source into an assume-role profile = %d, want 409", c)
			}

			// Once the chained profile is gone the source deletes as before.
			if c := e.do(t, "DELETE", "/api/ssm/profiles/"+made.ID, nil, nil); c != http.StatusNoContent {
				t.Fatalf("delete chained = %d", c)
			}
			if c := e.do(t, "DELETE", "/api/ssm/profiles/"+src.ID, nil, nil); c != http.StatusNoContent {
				t.Errorf("delete source after = %d, want 204", c)
			}
		})
	}
}

// A chained profile is exported with its source's exported name, and an sso profile's wire
// bytes carry none of the new keys.
func TestAWSProfilesWireChained(t *testing.T) {
	rows := []store.SSMProfile{
		{ID: "s", Label: "sso main", StartURL: chainSSOStart, SSORegion: "us-east-1", AccountID: "123456789012", RoleName: "Dev", Kind: "sso"},
		{ID: "c", Label: "deploy", Kind: "assume_role", SourceProfileID: "s", RoleARN: chainRoleARN, AccountID: "210987654321",
			ExternalID: "ext-1", SessionName: "af", DurationSeconds: 1800, Region: "eu-west-1"},
		{ID: "o", Label: "orphan", Kind: "assume_role", SourceProfileID: "gone", RoleARN: chainRoleARN},
		{ID: "n", Label: "nested", Kind: "assume_role", SourceProfileID: "c", RoleARN: chainRoleARN},
	}
	got, _ := awsProfilesWire(rows)
	b, _ := json.Marshal(got)
	want := `[{"name":"sso-main","label":"sso main","startUrl":"https://example.awsapps.com/start","ssoRegion":"us-east-1","accountId":"123456789012","roleName":"Dev"},` +
		`{"name":"deploy","label":"deploy","startUrl":"","ssoRegion":"","accountId":"210987654321","region":"eu-west-1","kind":"assume_role","roleArn":"arn:aws:iam::210987654321:role/deploy","sourceProfile":"sso-main","externalId":"ext-1","sessionName":"af","durationSeconds":1800}]`
	if string(b) != want {
		t.Errorf("wire =\n%s\nwant\n%s\n(a profile whose source is gone or is itself chained must not be exported)", b, want)
	}
}
