package main

// git credential-helper glue and startup seeding/migration over the encrypted
// store (internal/secrets). The store itself moved to internal/secrets in
// docs/log/23 remaining item 1 Wave B; the subcommand entry (`workspace-agent cred`), env-driven
// seeding and legacy-file migration stay in package main.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cpurl"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
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
	credHelperGet(os.Stdin, os.Stdout)
}

// credHelperGet answers one `get` request read from r, writing the credential to w.
// The store key is git's `host=` value verbatim, which carries the port whenever the
// remote URL does.
func credHelperGet(r io.Reader, w io.Writer) {
	host := credHelperHost(r)
	// git waits on this process: a store held by a wedged writer must not hold git with it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := secrets.LoadContext(ctx)
	if err != nil {
		return // emit nothing: git falls through / prompts
	}
	if host == "bitbucket.org" && s.Bitbucket != nil {
		c := *s.Bitbucket
		if time.Now().Unix() >= c.Expiry-120 { // refresh within 2 min of expiry
			if nc, rerr := gitx.RefreshBitbucket(c); rerr == nil {
				c = nc
				// Re-read-modify-write under the store lock: this helper runs as a
				// SEPARATE process concurrent with agent handlers, and a blind Save
				// of the stale snapshot would drop their changes.
				_ = secrets.Update(func(cur *secrets.Data) error {
					cur.Bitbucket = &c
					return nil
				})
			}
		}
		fmt.Fprintf(w, "username=x-token-auth\npassword=%s\n", c.AccessToken)
		return
	}
	if host == "github.com" && s.Git[host].RefreshToken != "" {
		// An expiring GitHub App token: renew it if due. After a refusal there is no
		// credential to offer, and git fails with an authentication error instead of
		// sending a dead token.
		tok, err := gitx.GitHubTokenContext(ctx, s)
		if err != nil || tok == "" {
			return
		}
		fmt.Fprintf(w, "username=%s\npassword=%s\n", s.Git[host].User, tok)
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
		fmt.Fprintf(w, "username=%s\npassword=%s\n", user, e.Token)
	}
}

// credHelperHost reads the credential protocol input (key=value lines until a
// blank line) and returns the requested host. Line-oriented (not one fixed-size
// Read): git may deliver the input in several chunks, and a partial read could
// miss the host= line and intermittently break fetch/push.
func credHelperHost(r io.Reader) string {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break // blank line terminates the request
		}
		if strings.HasPrefix(line, "host=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "host="))
		}
	}
	return ""
}

// internalGitHost is the CP-injected host of the tenant's self-hosted git
// (docs/reference/internal-git-provider): the clone URL's authority, port included,
// because that is the `host=` git's credential protocol sends. Empty when internal
// git is disabled.
func internalGitHost() string { return strings.TrimSpace(os.Getenv("AF_INTERNAL_GIT_HOST")) }

// seedInternalGit writes the CP-injected internal git credential into the store
// so the unified cred helper serves clone/push for the tenant's self-hosted repos.
// The token is a deterministic per-membership value the CP re-injects on every
// start, so this is idempotent; it only saves when the stored value differs.
//
// The env is fixed for the container's life, so after an administrator rotated the
// token and the CP pushed the new one (handlePutInternalGitToken) the env still holds
// the dead one. An Agent restart inside the same container must not seed it back over
// the pushed token: the push records which env token it superseded, and that one is
// skipped here.
func seedInternalGit() {
	host := internalGitHost()
	token := strings.TrimSpace(os.Getenv("AF_INTERNAL_GIT_TOKEN"))
	if host == "" || token == "" {
		return
	}
	err := secrets.Update(func(s *secrets.Data) error {
		if s.InternalGitStaleEnv != "" {
			if s.InternalGitStaleEnv == internalGitTokenDigest(token) {
				return errInternalGitCurrent
			}
			// A new container start brought a token the push did not know about.
			s.InternalGitStaleEnv = ""
		}
		epoch := envInternalGitEpoch()
		changed := s.InternalGitEpoch != epoch
		s.InternalGitEpoch = epoch
		if !storeInternalGitToken(s, host, token) && !changed {
			return errInternalGitCurrent
		}
		return nil
	})
	if errors.Is(err, errInternalGitCurrent) {
		return // already current
	}
	if err != nil {
		log.Printf("internal git: save failed: %v", err)
		return
	}
	_ = ensureCredHelper()
}

// envInternalGitEpoch is the epoch the CP minted AF_INTERNAL_GIT_TOKEN under. 0 when
// absent: a CP without epochs only ever minted epoch 0.
func envInternalGitEpoch() int64 {
	e, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("AF_INTERNAL_GIT_EPOCH")), 10, 64)
	if err != nil || e < 0 {
		return 0
	}
	return e
}

// errInternalGitCurrent ends a seed's store update without writing: the store already
// holds what the seed would write.
var errInternalGitCurrent = errors.New("internal git credential already current")

// storeInternalGitToken puts token under every host name git asks for the internal git
// with, and reports whether anything changed.
func storeInternalGitToken(s *secrets.Data, host, token string) bool {
	changed := false
	// A CP that injected the host without its port left the same credential under
	// the bare name, where git sends it to whatever answers on the default port.
	if bare, _, err := net.SplitHostPort(host); err == nil {
		if e, ok := s.Git[bare]; ok && e.User == "x-access-token" && e.Token == token {
			delete(s.Git, bare)
			changed = true
		}
	}
	// Under the internal authority too where the workspace's git is rewritten onto the
	// CP's workspace listener (syncInternalGitRewrite): git asks for the URL it connects to.
	for _, h := range []string{host, internalGitRewriteHost()} {
		if h == "" {
			continue
		}
		if e, ok := s.Git[h]; !ok || e.User != "x-access-token" || e.Token != token {
			s.Git[h] = secrets.GitEntry{User: "x-access-token", Token: token}
			changed = true
		}
	}
	return changed
}

// internalGitTokenDigest is how a superseded env token is remembered: the store only
// needs to recognise it, not to hold a second copy of a credential.
func internalGitTokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// handlePutInternalGitToken (PUT /internal-git/token {token,epoch}) takes the internal
// git token the CP minted after an administrator rotated it (issue #1199), so the running
// workspace keeps cloning and pushing without a restart. It never changes the host: that
// stays the one the CP injected at start.
//
// The CP is the intended caller, but the Agent bearer is the only gate, and every session
// in this workspace holds that bearer. That is the same boundary as PUT /connections/git:
// such a process runs as the Agent's uid, can read AF_SECRET_KEY and rewrite the store
// directly, so a CP signature here would not raise the bar. What a caller can do is replace
// its own workspace's internal git credential, which gains it nothing: the CP verifies
// every token on every request. The checks below keep that write to a well-formed token
// for this workspace's own membership.
//
// Pushes from different CP replicas can arrive out of order, so the store keeps the epoch
// of the token it holds and a push for an older epoch is answered superseded, not stored.
func handlePutInternalGitToken(w http.ResponseWriter, r *http.Request) {
	host := internalGitHost()
	if host == "" {
		httpx.WriteErr(w, http.StatusConflict, "internal_git_disabled", "no internal git host was injected at start")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req struct {
		Token string `json:"token"`
		Epoch int64  `json:"epoch"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	token := strings.TrimSpace(req.Token)
	envToken := strings.TrimSpace(os.Getenv("AF_INTERNAL_GIT_TOKEN"))
	mid, ok := internalGitTokenMembership(token)
	if !ok || req.Epoch < 0 {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_token", "not an internal git token")
		return
	}
	if envMid, envOK := internalGitTokenMembership(envToken); envOK && envMid != mid {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_token", "token is for another membership")
		return
	}
	superseded := false
	err := secrets.Update(func(s *secrets.Data) error {
		if req.Epoch < s.InternalGitEpoch {
			superseded = true
			return nil
		}
		if envToken != "" && envToken != token {
			s.InternalGitStaleEnv = internalGitTokenDigest(envToken)
		} else {
			s.InternalGitStaleEnv = ""
		}
		s.InternalGitEpoch = req.Epoch
		storeInternalGitToken(s, host, token)
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	if superseded {
		httpx.WriteJSON(w, http.StatusOK, internalGitTokenAnswer{Superseded: true})
		return
	}
	_ = ensureCredHelper()
	httpx.WriteJSON(w, http.StatusOK, internalGitTokenAnswer{Updated: true})
}

// internalGitTokenRE is the shape the CP mints (control-plane mintGitToken): "afg_",
// the base64url membership id, ".", and a 16-byte tag in base64url (22 characters).
var internalGitTokenRE = regexp.MustCompile(`^afg_([A-Za-z0-9_-]{1,256})\.[A-Za-z0-9_-]{22}$`)

// internalGitTokenMembership returns the membership id a well-formed internal git token
// names. It proves nothing about the tag: only the CP can.
func internalGitTokenMembership(token string) (string, bool) {
	m := internalGitTokenRE.FindStringSubmatch(token)
	if m == nil {
		return "", false
	}
	id, err := base64.RawURLEncoding.DecodeString(m[1])
	if err != nil || len(id) == 0 {
		return "", false
	}
	return string(id), true
}

// internalGitTokenAnswer is what PUT /internal-git/token answers the CP. Superseded means
// the store already holds a token of a later epoch, so this one was not written.
type internalGitTokenAnswer struct {
	Updated    bool `json:"updated"`
	Superseded bool `json:"superseded,omitempty"`
}

// seedGitOAuthBridge copies the CP-injected bridge coordinates into the store so the
// `workspace-agent cred` PROCESS can reach the CP's refresh endpoint (docs/log/71 §71.8).
//
// ★ Env is read here, at agent startup, and never at the point of use. git spawns the
// credential helper as its own binary, frequently from a shell under a tmux server, and
// that process's environment is not ours to guarantee — the same reason seedInternalGit
// exists rather than runCredHelper reading AF_INTERNAL_GIT_TOKEN directly.
//
// The token is deterministic per membership, so re-seeding on every start is idempotent;
// it only writes when the stored value differs. An unset pair CLEARS a stored bridge:
// otherwise a deployment that removed PUBLIC_BASE_URL would keep a workspace pointing at
// an endpoint that no longer answers.
func seedGitOAuthBridge() {
	base := cpurl.Request()
	token := strings.TrimSpace(os.Getenv("AF_GIT_OAUTH_TOKEN"))
	var want *secrets.CPBridge
	if base != "" && token != "" {
		want = &secrets.CPBridge{BaseURL: base, Token: token}
	}
	err := secrets.Update(func(cur *secrets.Data) error {
		have := cur.GitOAuthBridge
		switch {
		case want == nil && have == nil:
			return nil
		case want != nil && have != nil && *want == *have:
			return nil
		}
		cur.GitOAuthBridge = want
		return nil
	})
	if err != nil {
		log.Printf("git oauth bridge: seed failed: %v", err)
	}
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
