package main

// git credential-helper glue and startup seeding/migration over the encrypted
// store (internal/secrets). The store itself moved to internal/secrets in
// docs/23 残① Wave B; the subcommand entry (`workspace-agent cred`), env-driven
// seeding and legacy-file migration stay in package main.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// ensureCredHelper makes `workspace-agent cred` the sole global git credential
// helper for every host, clearing any inherited/legacy helpers (the old `store`
// and the per-host bitbucket helper). Idempotent.
func ensureCredHelper() error {
	// --unset-all exits 5 when the key is absent; that is not an error here.
	_ = gitx.Cmd("", "config", "--global", "--unset-all", "credential.helper").Run()
	_ = gitx.Cmd("", "config", "--global", "--unset-all", "credential.https://bitbucket.org.helper").Run()
	if out, err := gitx.Combined("", "config", "--global", "credential.helper", "!workspace-agent cred"); err != nil {
		return fmt.Errorf("git config credential.helper: %v: %s", err, out)
	}
	return nil
}

// runCredHelper implements the git credential helper protocol backed by the
// encrypted store. git calls `workspace-agent cred get` with `host=...` on
// stdin; we emit username/password, refreshing Bitbucket's token on the fly.
func runCredHelper(args []string) {
	if len(args) == 0 || args[0] != "get" {
		return // store/erase: nothing to do
	}
	host := credHelperHost(os.Stdin)
	s, err := secrets.Load()
	if err != nil {
		return // emit nothing: git falls through / prompts
	}
	if host == "bitbucket.org" && s.Bitbucket != nil {
		c := *s.Bitbucket
		if time.Now().Unix() >= c.Expiry-120 { // refresh within 2 min of expiry
			if nc, rerr := refreshBitbucket(c); rerr == nil {
				c = nc
				s.Bitbucket = &c
				_ = s.Save()
			}
		}
		fmt.Printf("username=x-token-auth\npassword=%s\n", c.AccessToken)
		return
	}
	if e, ok := s.Git[host]; ok {
		user := e.User
		// A Bitbucket API token can't authenticate git-over-HTTPS with the Atlassian
		// email as the username — that email:token form is only accepted by the REST
		// API (see bitbucketAuthHeader). Git requires the static API-token username, so
		// the same pasted credential that lists repos otherwise fails clone/push with
		// "Authentication failed". An app password still uses the Bitbucket account
		// name, so only rewrite when the stored user looks like an email (the
		// token-paste flow stores the Atlassian email; OAuth is handled above).
		if host == "bitbucket.org" && strings.Contains(user, "@") {
			user = "x-bitbucket-api-token-auth"
		}
		fmt.Printf("username=%s\npassword=%s\n", user, e.Token)
	}
}

// credHelperHost reads the credential protocol input (key=value lines until a
// blank line) and returns the requested host.
func credHelperHost(r *os.File) string {
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		if strings.HasPrefix(line, "host=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "host="))
		}
	}
	return ""
}

// internalGitHost is the CP-injected host of the tenant's self-hosted git
// (docs/reference/internal-git-provider). Empty when internal git is disabled.
func internalGitHost() string { return strings.TrimSpace(os.Getenv("AF_INTERNAL_GIT_HOST")) }

// seedInternalGit writes the CP-injected internal git credential into the store
// so the unified cred helper serves clone/push for the tenant's self-hosted repos.
// The token is a deterministic per-membership value the CP re-injects on every
// start, so this is idempotent; it only saves when the stored value differs.
func seedInternalGit() {
	host := internalGitHost()
	token := strings.TrimSpace(os.Getenv("AF_INTERNAL_GIT_TOKEN"))
	if host == "" || token == "" {
		return
	}
	s, err := secrets.Load()
	if err != nil {
		log.Printf("internal git: load secrets failed: %v", err)
		return
	}
	if e, ok := s.Git[host]; ok && e.User == "x-access-token" && e.Token == token {
		return // already current
	}
	s.Git[host] = secrets.GitEntry{User: "x-access-token", Token: token}
	if err := s.Save(); err != nil {
		log.Printf("internal git: save failed: %v", err)
		return
	}
	_ = ensureCredHelper()
}

// migrateLegacySecrets folds any pre-A3 plaintext files into the store on start
// and deletes them, so the bind-mounted disk no longer holds plaintext. Runs
// every start; a no-op once migrated.
func migrateLegacySecrets() {
	s, err := secrets.Load()
	if err != nil {
		log.Printf("secrets migration: load failed: %v", err)
		return
	}
	home := homeDir()
	gcp := filepath.Join(home, ".git-credentials")
	bjp := filepath.Join(home, ".config", "agent-fleet", "bitbucket.json")
	ctp := filepath.Join(home, ".config", "agent-fleet", "claude-oauth-token")

	changed := false
	if data, err := os.ReadFile(gcp); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			if u, err := url.Parse(line); err == nil && u.Host != "" {
				pw, _ := u.User.Password()
				s.Git[u.Host] = secrets.GitEntry{User: u.User.Username(), Token: pw}
				changed = true
			}
		}
	}
	if b, err := os.ReadFile(bjp); err == nil {
		var c secrets.BitbucketCreds
		if json.Unmarshal(b, &c) == nil && c.AccessToken != "" {
			s.Bitbucket = &c
			changed = true
		}
	}
	if b, err := os.ReadFile(ctp); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			s.Claude = t
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := s.Save(); err != nil {
		log.Printf("secrets migration: save failed (keeping legacy files): %v", err)
		return
	}
	for _, p := range []string{gcp, bjp, ctp} {
		_ = os.Remove(p)
	}
	_ = ensureCredHelper()
	log.Printf("secrets: migrated legacy plaintext credentials into %s", filepath.Base(secrets.Path()))
}
