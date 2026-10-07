package gcpx

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// Logging out of a Settings profile (#1850). gcloud's store is per account (decision 1), so
// a logout signs the Agent's store out of the account the profile selects, and with it every
// profile that selects the same account. `gcloud auth revoke` is not used: it calls Google
// first and removes nothing locally when that call fails (SDK 587.0.0, store.Revoke), and
// what a revoke of gcloud's grant ends beyond this workspace — the member's gcloud on other
// machines — is not measured. The Agent deletes the account's credential itself, so the
// session at Google runs on, and a token a running command already holds stays valid until
// it expires. The member's own ~/.config/gcloud is never touched.

// attemptExitWait bounds how long a logout waits for an ended login's gcloud to exit. The
// kill is SIGKILL to its process group, so this is only scheduling latency.
const attemptExitWait = 5 * time.Second

type profileLogoutWire struct {
	// Account is the account signed out, "" when the profile selected none.
	Account string `json:"account,omitempty"`
	// Profiles are the configurations that selected Account, by profile name: each is signed
	// out with it.
	Profiles []string `json:"profiles"`
}

// selecting is every af- configuration under root that selects account, by profile name.
func selecting(root, account string) ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(root, "configurations"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		name, ok := strings.CutPrefix(e.Name(), "config_af-")
		if ok && readProperty(configPath(root, name), "core", "account") == account {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// endLoginFor ends the login attempt running for the configuration key, if any, and waits
// for its gcloud to exit, so a code it was given cannot store a credential after the
// logout. A login started after the logout is a new login, and it wins.
func endLoginFor(key string) {
	a := logins.Current(key)
	if a == nil {
		return
	}
	a.End(cloudlogin.PhaseCancelled, "logged out")
	if done := a.Exited(); done != nil {
		select {
		case <-done:
		case <-time.After(attemptExitWait):
		}
	}
}

// removeAccount deletes account's credential and cached access tokens from the store under
// root, the legacy credential files gcloud writes beside them (they hold the refresh token
// too), and the Agent's mark of its last login, for a caller that holds the root's lock.
// The account is the store's key as written for Google's default universe: the Agent's
// configurations never set another.
func removeAccount(root, account string) error {
	for _, f := range []struct{ file, table string }{{"credentials.db", "credentials"}, {"access_tokens.db", "access_tokens"}} {
		if err := deleteRows(filepath.Join(root, f.file), f.table, account); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(filepath.Join(root, "legacy_credentials", strings.ReplaceAll(account, ":", ""))); err != nil {
		return err
	}
	m := readLogins(root)
	if _, ok := m[account]; !ok {
		return nil
	}
	delete(m, account)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	_, err = writeIfChanged(filepath.Join(root, loginsFile), string(out)+"\n")
	return err
}

// deleteRows deletes account's rows from table in the SQLite file at path. A file or table
// gcloud has not created yet holds nothing to delete.
func deleteRows(path, table, account string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(3000)")
	if err != nil {
		return err
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
		return fmt.Errorf("reading %s: %w", filepath.Base(path), err)
	}
	if n == 0 {
		return nil
	}
	if _, err := db.Exec(`DELETE FROM "`+table+`" WHERE account_id = ?`, account); err != nil {
		return fmt.Errorf("deleting from %s: %w", filepath.Base(path), err)
	}
	return nil
}

// clearLoginAccount drops core/account from the profile's configuration when the login
// chose it, so the next run asks which account to use. An account named in Settings stays:
// it is Settings', and without its credential the profile is signed out all the same.
func clearLoginAccount(root, name string) error {
	if sp, ok := readState(root)[name]; !ok || sp.Account != "" {
		return nil
	}
	cfg := configPath(root, name)
	props, err := readProps(cfg)
	if err != nil {
		return err
	}
	if _, ok := props["core/account"]; !ok {
		return nil
	}
	delete(props, "core/account")
	_, err = writeIfChanged(cfg, renderProps(props))
	return err
}

// HandleProfileLogout is POST /gcp-login/profiles/{name}/logout: the "Log out" of a Settings
// profile. Under the root's lock, so no af-gcloud-exec run mints and no login stores a
// credential meanwhile, it ends the login attempts of every profile selecting the account,
// deletes the account's credential, and clears the selection the login made. The answer
// names the profiles that were signed out.
func HandleProfileLogout(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := loginSettings()[name]; !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_a_settings_profile", "no Settings profile with that name reached this workspace")
		return
	}
	// An attempt holds the lock only while it starts and while it redeems a code, both a
	// matter of seconds; one waiting on the member does not, and is ended below.
	root, unlock, err := lockRootWithin(rootBusyWait)
	if err != nil {
		httpx.WriteErr(w, http.StatusConflict, "busy", err.Error())
		return
	}
	defer unlock()
	out := profileLogoutWire{Profiles: []string{}}
	account := readProperty(configPath(root, name), "core", "account")
	if account == "" || !emailRe.MatchString(account) {
		log.Printf("gcp-login: logout profile=%s signed_out=0 relayed=%t", name, cloudlogin.RelayedByCP(r))
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	names, err := selecting(root, account)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "remove_failed", err.Error())
		return
	}
	for _, n := range names {
		endLoginFor(ConfigName(n))
	}
	if err := removeAccount(root, account); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "remove_failed", err.Error())
		return
	}
	for _, n := range names {
		if err := clearLoginAccount(root, n); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "remove_failed", err.Error())
			return
		}
	}
	out.Account, out.Profiles = account, names
	log.Printf("gcp-login: logout profile=%s signed_out=%d relayed=%t", name, len(names), cloudlogin.RelayedByCP(r))
	httpx.WriteJSON(w, http.StatusOK, out)
}
