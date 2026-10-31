package awsx

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// gateEnv gives the test a HOME, a session "s1" and Settings with a gated SSO profile
// "prod-admin", an ungated source "base" with a gated chain "deploy" over it, and a gated
// source "admin-src" with an ungated chain "via-admin".
func gateEnv(t *testing.T) map[string]Profile {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "s1", Kind: session.KindShell, Dir: "/w", CreatedAt: "2026-10-31T00:00:00Z"})
	admin := prof("prod-admin")
	admin.ID, admin.ConfirmBeforeUse = "id-admin", true
	base := prof("base")
	base.ID = "id-base"
	deploy := Profile{ID: "id-deploy", Name: "deploy", Kind: KindAssumeRole, SourceProfile: "base", RoleARN: chainRoleARN,
		AccountID: "210987654321", ConfirmBeforeUse: true}
	adminSrc := prof("admin-src")
	adminSrc.ID, adminSrc.ConfirmBeforeUse = "id-admin-src", true
	via := Profile{ID: "id-via", Name: "via-admin", Kind: KindAssumeRole, SourceProfile: "admin-src", RoleARN: chainRoleARN,
		AccountID: "210987654321"}
	settings := map[string]Profile{"prod-admin": admin, "base": base, "deploy": deploy, "admin-src": adminSrc, "via-admin": via}
	old := confirmSettings
	confirmSettings = func() map[string]Profile { return settings }
	oldPoll, oldCur := confirmPoll, currentSettings
	confirmPoll = 10 * time.Millisecond
	// The waiting run reads the same Settings the Agent's routes do, as both read the poll's.
	currentSettings = func() map[string]Profile { return confirmSettings() }
	t.Cleanup(func() { confirmSettings, confirmPoll, currentSettings = old, oldPoll, oldCur })
	return settings
}

// gate runs the gate in the background and returns its result channel.
func gate(settings map[string]Profile, profile, sess string, wait time.Duration) chan error {
	ch := make(chan error, 1)
	go func() {
		_, err := ConfirmGate(ConfirmOptions{Settings: settings, Profile: profile, Session: sess, Wait: wait, Command: "aws"})
		ch <- err
	}()
	return ch
}

// pending waits for the Console's list to show n requests and returns them.
func pending(t *testing.T, n int) []ConfirmRequestWire {
	t.Helper()
	for i := 0; i < 300; i++ {
		rec := httptest.NewRecorder()
		HandleConfirmList(rec, httptest.NewRequest("GET", "/aws-confirm", nil))
		if strings.Count(rec.Body.String(), `"id":"`) == n {
			var got confirmListWire
			mustJSON(t, rec.Body.Bytes(), &got)
			return got.Requests
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the Console never listed %d request(s)", n)
	return nil
}

func answer(t *testing.T, id, verb string, relayed bool) int {
	t.Helper()
	r := httptest.NewRequest("POST", "/aws-confirm/"+id+"/"+verb, nil)
	r.SetPathValue("id", id)
	if relayed {
		r.Header.Set("X-AF-Relay", "cp")
	}
	rec := httptest.NewRecorder()
	if verb == "approve" {
		HandleConfirmApprove(rec, r)
	} else {
		HandleConfirmDeny(rec, r)
	}
	return rec.Code
}

func TestConfirmGateApproves(t *testing.T) {
	s := gateEnv(t)
	ch := gate(s, "prod-admin", "s1", 5*time.Second)
	reqs := pending(t, 1)
	if reqs[0].Session != "s1" || len(reqs[0].Targets) != 1 || reqs[0].Targets[0].AccountID != "123456789012" || reqs[0].Targets[0].RoleName != "Dev" {
		t.Fatalf("request = %+v", reqs[0])
	}
	if c := answer(t, reqs[0].ID, "approve", true); c != http.StatusOK {
		t.Fatalf("approve = %d", c)
	}
	if err := <-ch; err != nil {
		t.Fatalf("approved run refused: %v", err)
	}
	// The next run of the same session and profile does not ask again.
	res, err := ConfirmGate(ConfirmOptions{Settings: s, Profile: "prod-admin", Session: "s1", Wait: time.Millisecond})
	if err != nil || !res.Confirmed || res.OutsideConsole {
		t.Fatalf("second run = %+v, %v", res, err)
	}
	if n := len(pending(t, 0)); n != 0 {
		t.Fatalf("%d requests left after the approval", n)
	}
}

func TestConfirmGateDenialAndTimeout(t *testing.T) {
	s := gateEnv(t)
	ch := gate(s, "prod-admin", "s1", 5*time.Second)
	reqs := pending(t, 1)
	answer(t, reqs[0].ID, "deny", true)
	if err := <-ch; !errors.Is(err, ErrConfirmRequired) || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("denied run = %v", err)
	}
	// Within the hold a new run is denied at once and files nothing new.
	start := time.Now()
	if _, err := ConfirmGate(ConfirmOptions{Settings: s, Profile: "prod-admin", Session: "s1", Wait: 5 * time.Second}); !errors.Is(err, ErrConfirmRequired) || time.Since(start) > time.Second {
		t.Fatalf("held run = %v after %s", err, time.Since(start))
	}

	// Unanswered: the run ends after its wait and the request stays pending for a later run.
	var buf bytes.Buffer
	_, err := ConfirmGate(ConfirmOptions{Settings: s, Profile: "deploy", Session: "s1", Wait: 50 * time.Millisecond, Stderr: &buf})
	if !errors.Is(err, ErrConfirmRequired) || !strings.Contains(err.Error(), "waiting for confirmation") {
		t.Fatalf("timed-out run = %v", err)
	}
	if !strings.Contains(buf.String(), "waiting in the Agent Fleet Console") {
		t.Fatalf("the run did not say where it waits: %q", buf.String())
	}
	if n := len(pending(t, 1)); n != 1 {
		t.Fatalf("the request did not stay pending")
	}
}

// No session: nothing is filed and the run ends at once; never a shared, empty-session grant.
func TestConfirmGateNeedsASession(t *testing.T) {
	s := gateEnv(t)
	for _, sess := range []string{"", "nosuch", "../x"} {
		start := time.Now()
		if _, err := ConfirmGate(ConfirmOptions{Settings: s, Profile: "prod-admin", Session: sess, Wait: 5 * time.Second}); !errors.Is(err, ErrConfirmRequired) || time.Since(start) > time.Second {
			t.Fatalf("session %q: %v", sess, err)
		}
	}
	pending(t, 0)
	if ents, _ := os.ReadDir(confirmDir("grants")); len(ents) != 0 {
		t.Fatalf("grants written: %v", ents)
	}
}

// A grant is for one profile id, one identity and one session incarnation: an edit of the
// profile, a new session of the same name, or another profile asks again.
func TestConfirmGrantScope(t *testing.T) {
	s := gateEnv(t)
	ch := gate(s, "prod-admin", "s1", 5*time.Second)
	answer(t, pending(t, 1)[0].ID, "approve", true)
	if err := <-ch; err != nil {
		t.Fatal(err)
	}
	needsAsking := func(settings map[string]Profile, profile string) bool {
		_, err := ConfirmGate(ConfirmOptions{Settings: settings, Profile: profile, Session: "s1", Wait: 20 * time.Millisecond})
		return errors.Is(err, ErrConfirmRequired)
	}
	if needsAsking(s, "prod-admin") {
		t.Fatal("the approved profile asks again")
	}
	edited := map[string]Profile{}
	for k, v := range s {
		edited[k] = v
	}
	p := edited["prod-admin"]
	p.RoleName = "AdministratorAccess"
	edited["prod-admin"] = p
	if !needsAsking(edited, "prod-admin") {
		t.Fatal("an edited role kept the grant")
	}
	session.WriteMeta(session.Meta{Name: "s1", Kind: session.KindShell, Dir: "/w", CreatedAt: "2026-10-31T09:00:00Z"})
	if !needsAsking(s, "prod-admin") {
		t.Fatal("a new session of the same name kept the grant")
	}
}

// A chain whose source is gated needs the source's approval; a gated chain needs its own; the
// source's approval does not count for the gated chain.
func TestConfirmTargetsOfChains(t *testing.T) {
	s := gateEnv(t)
	names := func(profile string) string {
		var out []string
		for _, t := range confirmTargets(s, profile) {
			out = append(out, t.Profile)
		}
		return strings.Join(out, ",")
	}
	if got := names("via-admin"); got != "admin-src" {
		t.Fatalf("chain over a gated source needs %q", got)
	}
	if got := names("deploy"); got != "deploy" {
		t.Fatalf("gated chain over an ungated source needs %q", got)
	}
	if got := names("base"); got != "" {
		t.Fatalf("an ungated profile needs %q", got)
	}
	// Approving the gated source does not let the gated target through.
	both := map[string]Profile{}
	for k, v := range s {
		both[k] = v
	}
	d := both["deploy"]
	d.SourceProfile = "admin-src"
	both["deploy"] = d
	confirmSettings = func() map[string]Profile { return both }
	ch := gate(both, "admin-src", "s1", 5*time.Second)
	answer(t, pending(t, 1)[0].ID, "approve", true)
	if err := <-ch; err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmGate(ConfirmOptions{Settings: both, Profile: "deploy", Session: "s1", Wait: 20 * time.Millisecond}); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("the source's approval let the gated chain through: %v", err)
	}
	reqs := pending(t, 1)
	if len(reqs[0].Targets) != 1 || reqs[0].Targets[0].Profile != "deploy" || reqs[0].Targets[0].Source != "admin-src" {
		t.Fatalf("chain request = %+v", reqs[0].Targets)
	}
}

// A Settings change while the request waits drops it from the Console's list; an approval that
// did not come through the CP's relay is reported as such.
func TestConfirmRequestFollowsSettings(t *testing.T) {
	s := gateEnv(t)
	gate(s, "prod-admin", "s1", 200*time.Millisecond)
	reqs := pending(t, 1)
	edited := map[string]Profile{}
	for k, v := range s {
		edited[k] = v
	}
	p := edited["prod-admin"]
	p.AccountID = "999999999999"
	edited["prod-admin"] = p
	confirmSettings = func() map[string]Profile { return edited }
	pending(t, 0)
	if c := answer(t, reqs[0].ID, "approve", true); c != http.StatusNotFound {
		t.Fatalf("approving a request Settings no longer matches = %d", c)
	}

	confirmSettings = func() map[string]Profile { return s }
	ch := gate(s, "prod-admin", "s1", 5*time.Second)
	answer(t, pending(t, 1)[0].ID, "approve", false)
	if err := <-ch; err != nil {
		t.Fatal(err)
	}
	if res, _ := ConfirmGate(ConfirmOptions{Settings: s, Profile: "prod-admin", Session: "s1"}); !res.OutsideConsole {
		t.Fatal("an unrelayed approval is not reported")
	}
}

func mustJSON(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
}

// G1: Settings changing what a waiting run would run as ends the run with exit 4 at once,
// instead of re-asking forever with what it read at its start.
func TestConfirmGateEndsWhenSettingsChangeWhileWaiting(t *testing.T) {
	s := gateEnv(t)
	ch := gate(s, "prod-admin", "s1", 5*time.Second)
	pending(t, 1)
	edited := map[string]Profile{}
	for k, v := range s {
		edited[k] = v
	}
	p := edited["prod-admin"]
	p.RoleName = "AdministratorAccess"
	edited["prod-admin"] = p
	confirmSettings = func() map[string]Profile { return edited }
	select {
	case err := <-ch:
		if !errors.Is(err, ErrConfirmRequired) || !strings.Contains(err.Error(), "changed in Settings") {
			t.Fatalf("run = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the run kept waiting on what it read at its start")
	}
}

// G2: a request of a session that ended, or that is another of the same name now, is not
// listed and cannot be approved, and its waiting run ends.
func TestConfirmStaleSessionCannotBeApproved(t *testing.T) {
	s := gateEnv(t)
	ch := gate(s, "prod-admin", "s1", 5*time.Second)
	id := pending(t, 1)[0].ID
	session.WriteMeta(session.Meta{Name: "s1", Kind: session.KindShell, Dir: "/w", CreatedAt: "2026-10-31T12:00:00Z"})
	if c := answer(t, id, "approve", true); c != http.StatusNotFound {
		t.Fatalf("approving the old session's request = %d", c)
	}
	pending(t, 0)
	select {
	case err := <-ch:
		if !errors.Is(err, ErrConfirmRequired) || !strings.Contains(err.Error(), "ended") {
			t.Fatalf("run = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the old session's run kept waiting")
	}
	if ents, _ := os.ReadDir(confirmDir("grants")); len(ents) != 0 {
		t.Fatalf("a grant was written for the ended session: %v", ents)
	}
}

// G5: a held requests lock never keeps a run past its own wait; it ends with exit 4 and files
// nothing.
func TestConfirmGateBoundsTheLockWait(t *testing.T) {
	s := gateEnv(t)
	unlock, err := lockConfirm(time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	start := time.Now()
	_, gerr := ConfirmGate(ConfirmOptions{Settings: s, Profile: "prod-admin", Session: "s1", Wait: 100 * time.Millisecond})
	if !errors.Is(gerr, ErrConfirmRequired) || time.Since(start) > time.Second {
		t.Fatalf("gate = %v after %s", gerr, time.Since(start))
	}
	ents, _ := os.ReadDir(confirmDir("requests"))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Fatalf("a request was filed past the wait: %s", e.Name())
		}
	}
}

// G6: a denial holds each profile it named, whatever set of profiles the next run asks about.
func TestDenyHoldIsPerProfile(t *testing.T) {
	s := gateEnv(t)
	both := map[string]Profile{}
	for k, v := range s {
		both[k] = v
	}
	d := both["deploy"]
	d.SourceProfile = "admin-src"
	both["deploy"] = d
	confirmSettings = func() map[string]Profile { return both }
	ch := gate(both, "deploy", "s1", 5*time.Second)
	reqs := pending(t, 1)
	if len(reqs[0].Targets) != 2 {
		t.Fatalf("targets = %+v", reqs[0].Targets)
	}
	answer(t, reqs[0].ID, "deny", true)
	if err := <-ch; !errors.Is(err, ErrConfirmRequired) {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := ConfirmGate(ConfirmOptions{Settings: both, Profile: "admin-src", Session: "s1", Wait: 5 * time.Second})
	if !errors.Is(err, ErrConfirmRequired) || !strings.Contains(err.Error(), "denied") || time.Since(start) > time.Second {
		t.Fatalf("the source alone = %v after %s", err, time.Since(start))
	}
}

// G1: a grant for what the run planned does not let it through once Settings changed, whether
// the grant was there before the run started or lands while it waits.
func TestConfirmGrantDoesNotSkipTheSettingsCheck(t *testing.T) {
	s := gateEnv(t)
	inc := incarnation(session.Meta{Name: "s1", Kind: session.KindShell, Dir: "/w", CreatedAt: "2026-10-31T00:00:00Z"})
	target := confirmTargets(s, "prod-admin")[0]
	grant := func() {
		t.Helper()
		g := confirmGrant{Session: "s1", Incarnation: inc, ProfileID: target.ProfileID, Identity: target.Identity,
			Expires: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), Relayed: true}
		if err := writeJSONAtomic(grantPath(inc, target.ProfileID), g); err != nil {
			t.Fatal(err)
		}
	}
	edited := map[string]Profile{}
	for k, v := range s {
		edited[k] = v
	}
	p := edited["prod-admin"]
	p.RoleName = "AdministratorAccess"
	edited["prod-admin"] = p

	// Control: the grant lets the unchanged plan through.
	grant()
	if res, err := ConfirmGate(ConfirmOptions{Settings: s, Profile: "prod-admin", Session: "s1", Wait: time.Millisecond}); err != nil || !res.Confirmed {
		t.Fatalf("control = %+v, %v", res, err)
	}
	// The grant exists from the start, but Settings changed.
	confirmSettings = func() map[string]Profile { return edited }
	if _, err := ConfirmGate(ConfirmOptions{Settings: s, Profile: "prod-admin", Session: "s1", Wait: time.Millisecond}); !errors.Is(err, ErrConfirmRequired) || !strings.Contains(err.Error(), "changed in Settings") {
		t.Fatalf("existing grant, changed Settings = %v", err)
	}

	// The grant lands while the run waits, together with a Settings change.
	os.Remove(grantPath(inc, target.ProfileID))
	confirmSettings = func() map[string]Profile { return s }
	ch := gate(s, "prod-admin", "s1", 5*time.Second)
	pending(t, 1)
	confirmSettings = func() map[string]Profile { return edited }
	grant()
	select {
	case err := <-ch:
		if !errors.Is(err, ErrConfirmRequired) || !strings.Contains(err.Error(), "changed in Settings") {
			t.Fatalf("grant landing with a Settings change = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the run never ended")
	}
}
