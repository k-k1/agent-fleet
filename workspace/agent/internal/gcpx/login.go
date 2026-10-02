package gcpx

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
)

// Console login requests for Google Cloud profiles (ADR 0107 decision 3). af-gcloud-exec
// files one when a profile needs a login and nobody is at a terminal; the Console shows a
// toast, and gcloud's login starts only when the member presses "Log in" there
// (login_agent.go). The request and attempt lifecycle is cloudlogin's; this file says what
// "logged in" and "resolved" mean in the Agent's gcloud store.

// NoticeKindGCPLogin is the notification kind that carries a request id to the Console.
const NoticeKindGCPLogin = "gcp-login-required"

// LoginState is what the Agent's gcloud root holds for one profile at one moment, as far
// as a login request cares. States are compared, never shown; none carries a secret.
type LoginState struct {
	// Profile fingerprints the version of the profile the last sync applied: its id, login
	// method and Settings account (decision 1: a change of any of them resets the
	// selection and drops the profile's pending requests).
	Profile string `json:"profile,omitempty"`
	// Account is the configuration's core/account; Credential says the store holds a user
	// credential for it.
	Account    string `json:"account,omitempty"`
	Credential bool   `json:"credential,omitempty"`
	// Login marks the last login completed through the Agent for Account (the Console's or
	// the terminal's). gcloud's store is per account, so the mark is too.
	Login string `json:"login,omitempty"`
}

// rejected reports whether a request recorded against s was filed because Google rejected
// a stored credential rather than because there was none: a run files a request while a
// user credential is selected only when the mint failed with invalid_grant or a
// reauthentication error (errCredentialRejected).
func (s LoginState) rejected() bool { return s.Account != "" && s.Credential }

// loginsFile records, per account, the mark of the last login the Agent saw complete.
const loginsFile = ".agent-fleet-logins.json"

// readLoginState reads the state of the profile called name. It takes no lock: every file
// it reads is replaced whole, and a state read mid-change only makes a waiter look again.
func readLoginState(name string) LoginState {
	root := ConfigRoot()
	var st LoginState
	if sp, ok := readState(root)[name]; ok {
		sum := sha256.Sum256([]byte(sp.ID + "\x00" + sp.LoginMethod + "\x00" + sp.Account))
		st.Profile = hex.EncodeToString(sum[:8])
	}
	acct := readProperty(configPath(root, name), "core", "account")
	if acct == "" || !emailRe.MatchString(acct) {
		return st
	}
	st.Account = acct
	if kind, err := credentialType(root, acct); err == nil && kind == "authorized_user" {
		st.Credential = true
	}
	st.Login = readLogins(root)[acct]
	return st
}

func readLogins(root string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(filepath.Join(root, loginsFile))
	if err != nil {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

// recordLogin marks a completed login of account, for a caller that holds the root's lock.
func recordLogin(root, account string) error {
	m := readLogins(root)
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	m[account] = hex.EncodeToString(b)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	_, err = writeIfChanged(filepath.Join(root, loginsFile), string(out)+"\n")
	return err
}

// loginBackend tells cloudlogin what a login is for a Google Cloud profile (decision 3
// step 5). The request key is the configuration name, af-<name>.
type loginBackend struct{}

func (loginBackend) State(key string) LoginState {
	return readLoginState(strings.TrimPrefix(key, "af-"))
}

// Landed: a request is settled when
//   - the profile changed in Settings since it was filed (decision 3 step 5: it is dropped;
//     the waiting run then fails its check with ErrSettingsChanged and asks for a rerun);
//   - a login completed through the Agent after it was filed, for the account the
//     configuration now selects, and that account holds a user credential. A login is
//     recorded only once a token was minted from it in the clean environment (finishLogin;
//     the terminal login records after its own mint), so this is step 5's "print-access-token
//     succeeds", checked once per login instead of on every sweep. For a request filed
//     because Google rejected the stored credential it also means what step 5 asks: the same
//     cached credential succeeding again settles nothing, only a login after the request does.
//
// A credential that appears in the store without such a login (written by hand, or by a
// process the Agent did not run) settles nothing.
func (loginBackend) Landed(cur, recorded LoginState, _ time.Time) bool {
	if cur.Profile != recorded.Profile {
		return true
	}
	return cur.Account != "" && cur.Credential && cur.Login != "" &&
		(cur.Login != recorded.Login || cur.Account != recorded.Account)
}

// logins holds the Console login requests and attempts of the Google Cloud profiles, keyed
// by configuration name.
var logins = &cloudlogin.Store[LoginState]{Dir: "gcp-login", NoticeKind: NoticeKindGCPLogin, NoticeKey: "gcp-login",
	LogPrefix: "gcp-login", Backend: loginBackend{}}

// errRootBusy means the gcloud root's lock stayed taken for the whole bounded wait: a login
// in a terminal holds it for as long as the person takes.
var errRootBusy = errorString("another Google Cloud login (in a terminal, or a code being redeemed) holds the Agent's gcloud store; try again in a moment")

type errorString string

func (e errorString) Error() string { return string(e) }

// rootBusyWait bounds how long a route waits for the gcloud root's lock. A var for tests.
var rootBusyWait = 10 * time.Second

// lockRootWithin is lockRoot that gives up after d instead of waiting for as long as a
// terminal login takes: an HTTP route must answer.
func lockRootWithin(d time.Duration) (string, func(), error) {
	root, unlock, err := lockRootNonBlocking()
	for deadline := time.Now().Add(d); err == errRootBusy && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		root, unlock, err = lockRootNonBlocking()
	}
	return root, unlock, err
}

func lockRootNonBlocking() (string, func(), error) {
	root, f, err := openRootLock()
	if err != nil {
		return "", nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return "", nil, errRootBusy
		}
		return "", nil, err
	}
	return root, func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// AttemptRef is how logs and the CP's audit name an attempt: enough to tell two apart and
// to match the Agent's log line with the audit row, while the id itself, which lets its
// holder read the attempt's URL, is written nowhere. The CP computes the same (gcpAttemptRef).
func AttemptRef(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:4])
}
