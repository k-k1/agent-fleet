package main

// Re-authenticating an SVN working copy (docs/log/41 amendment).
//
// The checkout dialog's "save credentials" is an opt-in, and declining it used to be a
// one-way door: nothing was written to the encrypted store, `--no-auth-cache` means
// nothing was written under ~/.subversion either, and no surface existed to supply the
// password afterwards. From then on every `svn update` failed with E170001 and the only
// way out was deleting the working copy and checking it out again.
//
// So credentials are enterable AFTER the fact, against a working copy that already
// exists. Two things make that route trustworthy rather than another guess:
//
//   - The URL is asked of the working copy, never typed. The prefix an entry is stored
//     under is the REPOSITORY ROOT (`svn info --show-item repos-root-url`), which is what
//     the longest-prefix lookup wants: one entry then serves every subtree checked out of
//     that repository, instead of one dead entry per trunk/branch folder.
//   - Nothing is stored before it has been proven to work. The credential is tried
//     against the real server first (`svn info <url>`), so "saved" cannot mean "saved a
//     typo" — the failure that sent the user here in the first place.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// errCodeSvnAuth is the machine token every SVN path returns when the server refused the
// credential (or wanted one we do not have). The Console keys the re-authentication
// prompt off it, so the decision "is this fixable by entering a password" is made HERE,
// once, from svn's own output — never by matching svn's message in the browser.
const errCodeSvnAuth = "svn_auth_required"

// svnAuthProbeTimeout bounds the credential check. `svn info <url>` is one round trip
// against the repository root, so a server that has not answered in this long is not
// going to authenticate us either.
const svnAuthProbeTimeout = 45 * time.Second

// svnAuthFailure reports whether svn's output says the credential is the problem.
//
// Only the codes that mean "authentication/authorization" are listed. E170013 ("Unable to
// connect to a repository at URL") is deliberately NOT one of them: svn prints it for an
// unreachable host just as readily as for a rejected password, and treating it as an auth
// failure would answer "enter your password" to a network outage.
func svnAuthFailure(out string) bool {
	s := strings.ToLower(out)
	return strings.Contains(out, "E170001") || // Authorization failed
		strings.Contains(out, "E215004") || // No more credentials or we tried too many times
		strings.Contains(out, "E175013") || // Access to '...' forbidden (HTTP 403)
		strings.Contains(s, "authorization failed") ||
		strings.Contains(s, "authentication failed") ||
		strings.Contains(s, "could not authenticate to server") ||
		strings.Contains(s, "username or password") // the interactive prompt's wording in --non-interactive form
}

// svnAuthPrefixFor picks the URL prefix a credential for this working copy should be
// stored under, and the working copy's own URL to prove it against.
//
// An entry that ALREADY governs this URL wins, even when it is broader than this
// repository. Re-authentication is nearly always aimed at exactly that entry — a checkout
// that saved cert trust but declined the password leaves a username-less entry under the
// base URL, and it is the one failing — and writing a longer prefix instead would leave
// the broken entry in place, still shadowing every other working copy under it.
// Otherwise the REPOSITORY ROOT (`repos-root-url`) is used, so one entry serves every
// subtree checked out of that repository rather than one dead entry per trunk folder.
func svnAuthPrefixFor(dir string) (prefix, url string) {
	url = svnInfoItem(dir, "url")
	if cur := svnCredsFor(url); cur != nil && cur.URLPrefix != "" {
		return cur.URLPrefix, url
	}
	if root := svnInfoItem(dir, "repos-root-url"); root != "" {
		return root, url
	}
	return url, url
}

// svnVerifyCred runs one authenticated `svn info <url>`. Returns "" when the credential
// works, otherwise svn's own output — the caller decides whether that output is an auth
// failure (fixable here) or a connection problem (not).
func svnVerifyCred(ctx context.Context, creds *secrets.SVNCred, url string) (string, bool) {
	out, err := runSvnAuthed(ctx, creds, "info", url)
	if err == nil {
		return "", true
	}
	return out, false
}

type svnAuthReq struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	TrustCert bool   `json:"trustCert"`
}

// handleGetSvnAuth (GET /repos/{name}/svn-auth) answers what the re-authentication dialog
// needs to open already filled in: which server this working copy talks to, and whether a
// credential currently matches it. The password is never returned.
func handleGetSvnAuth(w http.ResponseWriter, r *http.Request) {
	dir, ok := svnDirFromPath(w, r)
	if !ok {
		return
	}
	prefix, url := svnAuthPrefixFor(dir)
	res := map[string]any{"url": url, "urlPrefix": prefix, "hasCred": false, "trustCert": false, "username": ""}
	if cur := svnCredsFor(url); cur != nil {
		// hasCred is "a password will be sent", not "an entry exists": a trust-only entry
		// (cert trust saved, password declined) is precisely the state that needs this
		// dialog, so it must not read as authenticated.
		res["hasCred"] = cur.Username != ""
		res["username"] = cur.Username
		res["trustCert"] = cur.TrustCert
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// handleSvnAuth (POST /repos/{name}/svn-auth) verifies a credential against this working
// copy's server and, only then, stores it. This is the re-authentication route: it exists
// for the working copy that was checked out WITHOUT saving credentials, so it always
// saves — an unsaved credential would leave the very state the user came here to escape.
func handleSvnAuth(w http.ResponseWriter, r *http.Request) {
	if !svnAvailable() {
		httpx.WriteErr(w, http.StatusNotImplemented, "svn_missing", "the 'svn' command is not available in this workspace")
		return
	}
	dir, ok := svnDirFromPath(w, r)
	if !ok {
		return
	}
	var req svnAuthReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	user := strings.TrimSpace(req.Username)
	prefix, url := svnAuthPrefixFor(dir)
	if url == "" {
		httpx.WriteErr(w, http.StatusBadGateway, "no_url", "cannot read this working copy's repository URL")
		return
	}
	creds := &secrets.SVNCred{URLPrefix: prefix, Username: user, Password: req.Password, TrustCert: req.TrustCert}
	ctx, cancel := context.WithTimeout(r.Context(), svnAuthProbeTimeout)
	defer cancel()
	if out, ok := svnVerifyCred(ctx, creds, url); !ok {
		if svnAuthFailure(out) {
			httpx.WriteErr(w, http.StatusUnauthorized, errCodeSvnAuth, out)
			return
		}
		httpx.WriteErr(w, http.StatusBadGateway, "verify_failed", out)
		return
	}
	if err := svnSaveCred(prefix, user, req.Password, req.TrustCert); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"urlPrefix": prefix, "username": user, "trustCert": req.TrustCert})
}

type svnConnReq struct {
	URLPrefix string `json:"urlPrefix"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	TrustCert bool   `json:"trustCert"`
	// VerifyURL is an optional repository URL to prove the credential against. The
	// connections screen edits a PREFIX, which is not necessarily a repository (a whole
	// server's URL usually is not), so verification there is offered, not imposed.
	VerifyURL string `json:"verifyUrl"`
}

// handlePutSvnConn (PUT /connections/svn) upserts a saved SVN server credential from the
// connections screen — the same store the checkout dialog writes, reachable without a
// working copy in hand (adding the credential BEFORE the checkout, or fixing a password
// that was rotated on the server).
func handlePutSvnConn(w http.ResponseWriter, r *http.Request) {
	var req svnConnReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	prefix := strings.TrimSpace(req.URLPrefix)
	if prefix == "" || !strings.Contains(prefix, "://") {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_prefix", "urlPrefix is required and must be an absolute URL")
		return
	}
	user := strings.TrimSpace(req.Username)
	if verify := strings.TrimSpace(req.VerifyURL); verify != "" {
		if !svnAvailable() {
			httpx.WriteErr(w, http.StatusNotImplemented, "svn_missing", "the 'svn' command is not available in this workspace")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), svnAuthProbeTimeout)
		defer cancel()
		creds := &secrets.SVNCred{URLPrefix: prefix, Username: user, Password: req.Password, TrustCert: req.TrustCert}
		if out, ok := svnVerifyCred(ctx, creds, verify); !ok {
			code, status := "verify_failed", http.StatusBadGateway
			if svnAuthFailure(out) {
				code, status = errCodeSvnAuth, http.StatusUnauthorized
			}
			httpx.WriteErr(w, status, code, out)
			return
		}
	}
	if err := svnSaveCred(prefix, user, req.Password, req.TrustCert); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"urlPrefix": prefix, "username": user, "trustCert": req.TrustCert})
}
