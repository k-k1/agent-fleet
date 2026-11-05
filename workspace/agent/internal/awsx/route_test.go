package awsx

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// What the route gateway asks of the confirm-before-use machinery: a grant is read without
// waiting, ends with the session's incarnation, and the request for one is filed so the member
// sees it (ADR 0126, ADR 0128 decision 7).
func TestApprovalHeldFollowsTheGrantAndTheIncarnation(t *testing.T) {
	s := gateEnv(t)
	m, _ := session.ReadMeta("s1")
	now := time.Now()

	if !ApprovalHeld(s, "base", m, now) {
		t.Fatal("a profile that asks for no confirmation needs no approval")
	}
	if ApprovalHeld(s, "prod-admin", m, now) {
		t.Fatal("an unapproved gated profile was held")
	}
	if ApprovalHeld(s, "no-such-profile", m, now) != true {
		t.Fatal("an unknown profile has no targets; the gateway refuses it elsewhere")
	}

	// Asking files the request, answers with the gate's refusal and does not hold the call open.
	start := time.Now()
	err := AskApproval(s, "prod-admin", "s1", "model route")
	if !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("AskApproval = %v", err)
	}
	if took := time.Since(start); took > 6*time.Second {
		t.Fatalf("AskApproval held the call for %s", took)
	}
	reqs := pending(t, 1)
	if reqs[0].Session != "s1" {
		t.Fatalf("request = %+v", reqs[0])
	}
	if c := answer(t, reqs[0].ID, "approve", true); c != http.StatusOK {
		t.Fatalf("approve = %d", c)
	}
	if !ApprovalHeld(s, "prod-admin", m, time.Now()) {
		t.Fatal("the approval did not count")
	}
	// A grant lasts at most eight hours.
	if ApprovalHeld(s, "prod-admin", m, time.Now().Add(confirmGrantTTL+time.Minute)) {
		t.Fatal("a grant outlived eight hours")
	}
	// A session of the same name started afterwards is another incarnation: the grant is not its.
	session.WriteMeta(session.Meta{Name: "s1", Kind: session.KindShell, Dir: "/w", CreatedAt: "2026-10-31T12:00:00Z"})
	if ApprovalHeld(s, "prod-admin", m, time.Now()) {
		t.Fatal("the old incarnation's meta still holds the grant after the session was recreated")
	}
	m2, _ := session.ReadMeta("s1")
	if ApprovalHeld(s, "prod-admin", m2, time.Now()) {
		t.Fatal("a recreated session inherited the grant")
	}
}

func TestProfileByIDAndIdentity(t *testing.T) {
	s := map[string]Profile{"a": {ID: "1", Name: "a", AccountID: "123456789012", RoleName: "R", StartURL: "https://x/start", SSORegion: "us-east-1"},
		"b": {ID: "2", Name: "b"}}
	if p, ok := ProfileByID(s, "1"); !ok || p.Name != "a" {
		t.Fatalf("ProfileByID = %+v %v", p, ok)
	}
	if _, ok := ProfileByID(s, ""); ok {
		t.Fatal("an empty id found a profile")
	}
	if _, ok := ProfileByID(s, "3"); ok {
		t.Fatal("an unknown id found a profile")
	}
	before := ProfileIdentity(s, s["a"])
	p := s["a"]
	p.RoleName = "Admin"
	if ProfileIdentity(s, p) == before {
		t.Fatal("changing the role did not change the identity")
	}
	p = s["a"]
	p.ModelRoute = true
	if ProfileIdentity(s, p) != before {
		t.Fatal("the model-route mark must not change the identity: marking is not a different connection")
	}
}

// The gateway gets credentials as a value, from exactly what af-aws-exec runs, and never logs in.
func TestResolveCredentialsReturnsTheProfilesCredentialsOnly(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := ResolveCredentials(bin, workloadEnv, ExecOptions{Profile: "prod", Settings: prodSettings})
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessKeyID != "ASIAFAKE" || c.SecretAccessKey != "sekret" || c.SessionToken != "tok" || c.Region != "us-west-2" ||
		!c.Expires.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("credentials = %+v", c)
	}
	// The region the selection names wins over the profile's own.
	c, err = ResolveCredentials(bin, workloadEnv, ExecOptions{Profile: "prod", Settings: prodSettings, Region: "eu-central-1"})
	if err != nil || c.Region != "eu-central-1" {
		t.Fatalf("region = %q, %v", c.Region, err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "leaked")); len(b) != 0 {
		t.Fatalf("the aws CLI saw workload credentials:\n%s", b)
	}
}

func TestResolveCredentialsNeverStartsALogin(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	_, err := ResolveCredentials(bin, workloadEnv, ExecOptions{Profile: "prod", Settings: prodSettings,
		Login: "always", Interactive: true, ConsoleLogin: true, ConsoleWait: time.Minute, TerminalConsole: true})
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v, want ErrLoginRequired", err)
	}
	if _, serr := os.Stat(filepath.Join(state, "loginArgs")); serr == nil {
		t.Fatal("a login was started for a model call")
	}
}
