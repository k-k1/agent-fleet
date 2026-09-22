package muse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The two credential shapes, verbatim from the measurement on 1.3.0-R3401.1 (auth.go's
// header). The secrets are obvious fakes but their FIELDS are the real ones, because the
// point of these fixtures is which keys are present.
const (
	// accountJSON is what `muse login` leaves: an access token, a mechanism — and an
	// `api_key`, which is the trap this file exists to keep out of the card.
	accountJSON = `{"schema_version":1,"providers":{"meta":{
		"access_token":"dca:FAKE-ACCESS-TOKEN","api_base_url":"https://api.meta.ai/v1",
		"api_key":"LLM|111111111111111|FAKEKEYFROMTHEACCOUNTLOGIN","mechanism":"oauth",
		"obtained_via":"device_code","user_email":"probe@example.com",
		"user_full_name":"Probe User"}}}`
	// keyOnlyJSON is what `muse auth set --api-key-stdin` leaves: the key alone. Measured
	// over an account login, it replaces the entry — nothing else survives.
	keyOnlyJSON = `{"schema_version":1,"providers":{"meta":{"api_key":"LLM|222222222222222|FAKEPASTEDKEY"}}}`
	// loggedOutJSON is what `muse logout` leaves. The file is still there, mode 600.
	loggedOutJSON = `{"schema_version":1,"providers":{}}`
)

// writeAuth plants an auth.json under a throwaway HOME (museHome, settings_test.go).
func writeAuth(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(ConfigHome(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeMuse points Bin() at a shell script, so the handlers that shell out can be driven
// without the proprietary binary (which CI will never have). body is the script.
func fakeMuse(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "muse")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_MUSE_BIN", p)
	return p
}

// installedMuse is fakeMuse for a test that only needs Installed() to be true.
func installedMuse(t *testing.T) { fakeMuse(t, "exit 1\n") }

func TestReadCredentialShapes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string // "" = write no file at all
		present   bool
		mechanism string
		email     string
	}{
		{"account login (device code)", accountJSON, true, mechanismAccount, "probe@example.com"},
		{"pasted API key", keyOnlyJSON, true, mechanismAPIKey, ""},
		// 🔴 The one that a file-existence check gets wrong: logout leaves the file behind,
		// so this shape must read as signed out.
		{"after logout", loggedOutJSON, false, "", ""},
		// The same trap one level down, and the one a mutation found this table missing: a
		// version that empties the provider entry instead of removing it must still read as
		// signed out. Measured 1.3.0 removes the whole entry, so this is the hole a future
		// logout could fall into rather than one it does.
		{"empty meta entry", `{"schema_version":1,"providers":{"meta":{}}}`, false, "", ""},
		{"blanked-out key", `{"schema_version":1,"providers":{"meta":{"api_key":"","access_token":null}}}`, false, "", ""},
		{"no file", "", false, "", ""},
		{"malformed", `{"providers":`, false, "", ""},
		{"no meta provider", `{"schema_version":1,"providers":{"other":{"api_key":"x"}}}`, false, "", ""},
		// A mechanism AF has never seen still means connected: narrowing to "oauth" would
		// report a future sign-in route as signed out.
		{"unknown mechanism", `{"schema_version":1,"providers":{"meta":{"mechanism":"passkey"}}}`, true, "passkey", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			museHome(t)
			if tc.body != "" {
				writeAuth(t, tc.body)
			}
			c := readCredential()
			if c.Present != tc.present || c.Mechanism != tc.mechanism || c.Email != tc.email {
				t.Fatalf("got present=%v mechanism=%q email=%q, want %v/%q/%q",
					c.Present, c.Mechanism, c.Email, tc.present, tc.mechanism, tc.email)
			}
		})
	}
}

// The measurement this card turns on: the device-code login writes an `api_key` of its own,
// so `metered` cannot be derived from that field. Both arms are here because the account arm
// alone passes against a reader that always says false.
func TestStatusTellsAnAccountLoginFromAMeteredKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		metered any
	}{
		{"account login is on the subscription", accountJSON, false},
		{"a pasted key bills per use", keyOnlyJSON, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			museHome(t)
			installedMuse(t)
			writeAuth(t, tc.body)
			st := Status()
			if st["connected"] != true {
				t.Fatalf("connected = %v", st["connected"])
			}
			if st["metered"] != tc.metered {
				t.Fatalf("metered = %v, want %v (mechanism %v)", st["metered"], tc.metered, st["mechanism"])
			}
		})
	}
}

// supported=false is the pre-install state, and it must not depend on the credential: a
// member who signed in, then landed on a fresh container, has an auth.json and no binary.
func TestStatusReportsTheBinaryAndTheCredentialSeparately(t *testing.T) {
	museHome(t)
	t.Setenv("AGENT_MUSE_BIN", filepath.Join(t.TempDir(), "absent"))
	writeAuth(t, accountJSON)
	st := Status()
	if st["supported"] != false || st["reason"] != "not_installed" || st["connected"] != false {
		t.Fatalf("without the binary: %v", st)
	}

	installedMuse(t)
	st = Status()
	if st["supported"] != true || st["connected"] != true || st["email"] != "probe@example.com" {
		t.Fatalf("with the binary: %v", st)
	}
}

// META_API_KEY in the Agent's environment overrides the stored sign-in (muse's own
// `login --help` says so), so the card has to be able to say that the pill is not the
// whole story. The unset arm is the control: a flag that is always present says nothing.
func TestStatusSurfacesAnEnvironmentAPIKey(t *testing.T) {
	museHome(t)
	installedMuse(t)
	writeAuth(t, accountJSON)

	t.Setenv("META_API_KEY", "")
	if _, ok := Status()["env_key"]; ok {
		t.Fatal("env_key reported with META_API_KEY unset")
	}
	t.Setenv("META_API_KEY", "LLM|333333333333333|FAKEENVKEY")
	if Status()["env_key"] != true {
		t.Fatalf("env_key not reported: %v", Status())
	}
}

// Status is serialised straight into GET /connections, which the Console reads. The struct it
// is built from has no field for either secret, and this is the test that keeps it that way:
// adding one to `authFile` and echoing it turns this red.
func TestStatusCarriesNoSecret(t *testing.T) {
	museHome(t)
	installedMuse(t)
	writeAuth(t, accountJSON)
	t.Setenv("META_API_KEY", "LLM|333333333333333|FAKEENVKEY")
	b, err := json.Marshal(Status())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"FAKE-ACCESS-TOKEN", "FAKEKEYFROMTHEACCOUNTLOGIN", "FAKEENVKEY", "LLM|"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("Status() leaked %q: %s", secret, b)
		}
	}
}

// A host with no credential accepts session/start and then ends EVERY turn `authRequired` —
// the session reads as healthy in the Console and can never answer. So Resume refuses before
// it spawns, and the refusal names where to fix it.
//
// The signed-in arm is the control: it must get PAST this gate (it then fails at the spawn,
// because /bin/true is not an MSP host, and the message tells the two apart).
func TestResumeRefusesWithoutACredential(t *testing.T) {
	museHome(t)
	t.Setenv("AGENT_MUSE_BIN", "/bin/true")
	writeAuth(t, loggedOutJSON)
	_, err := NewDriver().Resume(session.Meta{Kind: session.KindMuse, Name: "no-cred", Dir: t.TempDir()})
	if err == nil {
		t.Fatal("Resume started a session with no credential stored")
	}
	if !strings.Contains(err.Error(), "サインイン") {
		t.Fatalf("the refusal does not point at the sign-in: %v", err)
	}
	if ManagedAlive("no-cred") {
		t.Error("a handle was left alive after the refusal")
	}

	writeAuth(t, accountJSON)
	_, err = NewDriver().Resume(session.Meta{Kind: session.KindMuse, Name: "with-cred", Dir: t.TempDir()})
	if err != nil && strings.Contains(err.Error(), "サインイン") {
		t.Fatalf("a signed-in session was still refused by the credential gate: %v", err)
	}
}

// --- routes --------------------------------------------------------------------------------

func post(t *testing.T, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/connections/muse/x", strings.NewReader(body)))
	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &env) != nil {
		t.Fatalf("not an error envelope: %s", rec.Body.String())
	}
	return env.Error.Code
}

// 🔴 The refusal that protects the member's wallet AND their sign-in: `muse auth set` replaces
// the provider entry, so a key written over an account login both moves them onto metered
// billing and deletes the login. The second arm is the control — with no account login the
// same request must go through, or "it refuses" would be indistinguishable from "it never
// works".
func TestHandleAPIKeyRefusesOverAnAccountLogin(t *testing.T) {
	museHome(t)
	// A fake `muse auth set` that does what the real one does: replace the entry.
	fakeMuse(t, `if [ "$1" = auth ]; then
  cat > /dev/null
  printf '%s' '`+keyOnlyJSON+`' > "$HOME/.config/muse/auth.json"
  echo "credentials saved for provider meta"
  exit 0
fi
exit 1
`)
	writeAuth(t, accountJSON)

	rec := post(t, HandleAPIKey, `{"key":"LLM|444444444444444|PASTED"}`)
	if rec.Code != http.StatusConflict || errCode(t, rec) != "account_login_present" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if c := readCredential(); c.Mechanism != mechanismAccount {
		t.Fatalf("the refused request still changed the credential: %+v", c)
	}

	// Control: after a disconnect there is nothing to destroy, so the key is accepted.
	writeAuth(t, loggedOutJSON)
	rec = post(t, HandleAPIKey, `{"key":"LLM|444444444444444|PASTED"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if c := readCredential(); !c.Present || c.Mechanism != mechanismAPIKey {
		t.Fatalf("credential after the accepted key: %+v", c)
	}
}

// `muse auth set` exits 0 without checking the key against Meta (measured), so the exit code
// proves nothing. What the handler must not do is report success when no credential landed.
func TestHandleAPIKeyVerifiesTheFileNotTheExitCode(t *testing.T) {
	museHome(t)
	fakeMuse(t, "cat > /dev/null; echo credentials saved for provider meta; exit 0\n")
	writeAuth(t, loggedOutJSON)
	rec := post(t, HandleAPIKey, `{"key":"LLM|555555555555555|PASTED"}`)
	if rec.Code != http.StatusBadGateway || errCode(t, rec) != "auth_failed" {
		t.Fatalf("a no-op auth set was reported as success: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAPIKeyRejectsAnEmptyKey(t *testing.T) {
	museHome(t)
	fakeMuse(t, "echo the binary must not be reached >&2; exit 9\n")
	rec := post(t, HandleAPIKey, `{"key":"   "}`)
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "bad_key" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// A signed-in muse prints no login URL, so starting one would only burn the 20-second scrape
// timeout and hand the card a bad error. And with no binary there is nothing to start.
func TestHandleStartRefusesWhenItCannotProduceAURL(t *testing.T) {
	museHome(t)
	t.Setenv("AGENT_MUSE_BIN", filepath.Join(t.TempDir(), "absent"))
	if rec := post(t, HandleStart, "{}"); rec.Code != http.StatusConflict || errCode(t, rec) != "muse_unsupported" {
		t.Fatalf("not installed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	fakeMuse(t, "echo the binary must not be reached >&2; exit 9\n")
	writeAuth(t, accountJSON)
	if rec := post(t, HandleStart, "{}"); rec.Code != http.StatusConflict || errCode(t, rec) != "already_connected" {
		t.Fatalf("already connected: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// The happy path of the scrape, against a fake that prints what muse prints. It also pins
// that the login child is NOT given a terminal — the fake refuses on a TTY the way the real
// binary effectively does, by stopping before the URL.
func TestHandleStartScrapesTheURLAndCode(t *testing.T) {
	museHome(t)
	fakeMuse(t, `if [ -t 1 ]; then echo "Press Enter to open it in your browser:"; sleep 30; fi
echo "Open this page to sign in:"
echo "  https://auth.meta.com/oauth/device/?code=NHBV-RVBT"
echo "confirm this code matches:"
echo "  NHBV-RVBT"
echo "Waiting for approval..."
sleep 30
`)
	writeAuth(t, loggedOutJSON)
	rec := post(t, HandleStart, "{}")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		FlowID   string `json:"flow_id"`
		URL      string `json:"url"`
		UserCode string `json:"user_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://auth.meta.com/oauth/device/?code=NHBV-RVBT" || got.UserCode != "NHBV-RVBT" || got.FlowID == "" {
		t.Fatalf("scraped %+v", got)
	}
	t.Cleanup(func() {
		if f := loginFlows.Take(got.FlowID); f != nil {
			f.Close()
		}
	})

	// The poll signal is the credential, not the child's wording.
	rec = post(t, HandlePoll, `{"flow_id":"`+got.FlowID+`"}`)
	if !strings.Contains(rec.Body.String(), `"connected":false`) {
		t.Fatalf("poll before approval: %s", rec.Body.String())
	}
	writeAuth(t, accountJSON)
	rec = post(t, HandlePoll, `{"flow_id":"`+got.FlowID+`"}`)
	if !strings.Contains(rec.Body.String(), `"connected":true`) {
		t.Fatalf("poll after approval: %s", rec.Body.String())
	}
}

// A login child that has exited without writing a credential is a finished failure (an
// expired code, a refused approval). Reporting it ends the card's poll instead of spinning to
// the 15-minute deadline. The live-child arm is the control: reporting `failed` there would
// abandon every login on its first poll.
func TestHandlePollReportsAnExitedLoginChild(t *testing.T) {
	museHome(t)
	writeAuth(t, loggedOutJSON)

	live, err := agents.StartPipeFlow(exec.Command("sh", "-c", "echo Waiting for approval; sleep 30"))
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	liveID := loginFlows.Put(live)
	defer loginFlows.Take(liveID)
	if body := post(t, HandlePoll, `{"flow_id":"`+liveID+`"}`).Body.String(); strings.Contains(body, "failed") {
		t.Fatalf("a running login was reported as failed: %s", body)
	}

	dead, err := agents.StartPipeFlow(exec.Command("sh", "-c", "echo device code expired >&2; exit 1"))
	if err != nil {
		t.Fatal(err)
	}
	deadID := loginFlows.Put(dead)
	defer loginFlows.Take(deadID)
	// Wait until the drain goroutine observes EOF — i.e. the child has exited and all
	// output is buffered. Only then can HandlePoll reliably report the failure.
	// Polling HandlePoll before Ended() is true races the drain goroutine (the 100-iteration
	// tight loop could complete before the child even starts on a loaded runner).
	select {
	case <-dead.WaitEnded():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for dead login child to exit")
	}
	body := post(t, HandlePoll, `{"flow_id":"`+deadID+`"}`).Body.String()
	if !strings.Contains(body, `"failed":true`) || !strings.Contains(body, "device code expired") {
		t.Fatalf("an exited login child was not reported: %s", body)
	}

	// An unknown id is not a failure — the TTL reaper gets here too, and it says nothing
	// about the approval.
	if body := post(t, HandlePoll, `{"flow_id":"nope"}`).Body.String(); strings.Contains(body, "failed") {
		t.Fatalf("an unknown flow id was reported as a failure: %s", body)
	}
}

// Disconnect judges the credential, not the exit code: `muse logout` reporting success while
// the entry survives must not tell the member they are signed out, and an unhappy exit that
// did remove it is a disconnect.
func TestHandleDisconnectJudgesTheCredential(t *testing.T) {
	del := func(t *testing.T) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		HandleDisconnect(rec, httptest.NewRequest(http.MethodDelete, "/connections/muse", nil))
		return rec
	}

	t.Run("exit 0 with the credential still there", func(t *testing.T) {
		museHome(t)
		fakeMuse(t, "echo logged out; exit 0\n")
		writeAuth(t, accountJSON)
		rec := del(t)
		if rec.Code != http.StatusBadGateway || errCode(t, rec) != "logout_failed" {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-zero exit that did remove it", func(t *testing.T) {
		museHome(t)
		fakeMuse(t, `printf '%s' '`+loggedOutJSON+`' > "$HOME/.config/muse/auth.json"; exit 3`+"\n")
		writeAuth(t, accountJSON)
		rec := del(t)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if readCredential().Present {
			t.Fatal("still connected after a successful disconnect")
		}
	})
}
