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
