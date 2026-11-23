package sessionx

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The session list carries the SSM target the meta already stores (ADR 0142 decision 10), so the
// Terminal view can group by machine without reading the title.
func TestWireSessionCarriesTheSSMTarget(t *testing.T) {
	m := session.Meta{
		Name: "s1", Kind: session.KindSSM, Dir: "/home/dev",
		SSM: &session.SSMMeta{
			Profile: "ops", Target: "i-0abc123", Document: "AF-Shell", Region: "ap-northeast-1",
			StartURL: "https://example.invalid/start", SSORegion: "us-east-1", AccountID: "111122223333", RoleName: "Admin",
			RoleARN: "arn:aws:iam::111122223333:role/x", ExternalID: "ext-secret-ish",
		},
	}
	w := wireSession(m, false)
	if w.SSM == nil {
		t.Fatal("an SSM session's row has no ssm target")
	}
	want := session.SSMTarget{Target: "i-0abc123", Region: "ap-northeast-1", AccountID: "111122223333", Profile: "ops", Document: "AF-Shell"}
	if *w.SSM != want {
		t.Fatalf("ssm = %+v, want %+v", *w.SSM, want)
	}
	// Only the non-secret target travels: not the sign-in's start URL, role or chaining.
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"example.invalid", "Admin", "arn:aws", "ext-secret-ish", "us-east-1"} {
		if strings.Contains(string(b), leaked) {
			t.Errorf("the session row leaks %q: %s", leaked, b)
		}
	}
}

func TestWireSessionOfAShellHasNoSSMTarget(t *testing.T) {
	w := wireSession(session.Meta{Name: "s2", Kind: session.KindShell, Dir: "/home/dev"}, false)
	if w.SSM != nil {
		t.Fatalf("a shell row carries an ssm target: %+v", *w.SSM)
	}
	b, _ := json.Marshal(w)
	if strings.Contains(string(b), `"ssm"`) {
		t.Fatalf("a shell row emits an ssm key: %s", b)
	}
}

// A role-chained profile stores the SOURCE sign-in's account in SSMMeta.AccountID; the machine's
// account is the one in the role ARN. Without it a direct login and an assume-role login to the
// same instance would be two groups, the second headed by the account that signed in.
func TestWireSessionSSMTargetAccountOfAChainedProfile(t *testing.T) {
	chained := session.Meta{Name: "s3", Kind: session.KindSSM, SSM: &session.SSMMeta{
		Profile: "via-b", Target: "i-0abc123", Region: "ap-northeast-1",
		AccountID: "222222222222", RoleARN: "arn:aws:iam::111122223333:role/ops", SourceProfile: "b-sso",
	}}
	direct := session.Meta{Name: "s4", Kind: session.KindSSM, SSM: &session.SSMMeta{
		Profile: "a-sso", Target: "i-0abc123", Region: "ap-northeast-1", AccountID: "111122223333",
	}}
	if got := wireSession(chained, false).SSM.AccountID; got != "111122223333" {
		t.Fatalf("chained login's account = %q, want the role's 111122223333", got)
	}
	if got := wireSession(direct, false).SSM.AccountID; got != "111122223333" {
		t.Fatalf("direct login's account = %q, want 111122223333", got)
	}
	// Not an ARN of a role: the stored account stands.
	for _, bad := range []string{"not-an-arn", "arn:aws:iam::111122223333:user/alice", "arn:aws:iam:us-east-1:111122223333:role/x", "arn:aws:iam::1111:role/x", "arn:aws:iam::11112222333a:role/x", "arn:aws:s3:::bucket"} {
		odd := session.Meta{Name: "s5", Kind: session.KindSSM, SSM: &session.SSMMeta{Target: "i-1", AccountID: "333344445555", RoleARN: bad}}
		if got := wireSession(odd, false).SSM.AccountID; got != "333344445555" {
			t.Fatalf("%q changed the account: %q", bad, got)
		}
	}
}
