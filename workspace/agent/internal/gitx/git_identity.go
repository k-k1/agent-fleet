package gitx

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Git commit identity (user.name / user.email) resolution. The effective identity is
// baked into each repo's LOCAL .git/config so it applies to EVERY commit path — the
// Console commit button, the terminal, claude, a plain shell — not just one. Resolution
// order per repo: manual repo override > provider (by the repo's remote host) > the
// global ~/.gitconfig default. A repo carrying a manual override is marked with the
// git config key af.identitySource=manual so a provider-identity change never clobbers it.

const identitySourceKey = "af.identitySource"

// remoteHost returns the lowercased host of a repo's origin remote ("github.com",
// "bitbucket.org", …), or "" when there's no origin / it can't be parsed.
func remoteHost(dir string) string {
	origin, ok := gitOriginURL(dir)
	if !ok {
		return ""
	}
	p, err := url.Parse(sshToHTTPS(origin))
	if err != nil {
		return ""
	}
	return strings.ToLower(p.Hostname())
}

func gitConfigLocalGet(dir, key string) string {
	out, err := gitx.Run(dir, "config", "--local", "--get", key)
	if err != nil {
		return ""
	}
	return out
}

func gitConfigLocalSet(dir, key, val string) {
	_ = gitx.Cmd(dir, "config", "--local", key, val).Run()
}

func gitConfigLocalUnset(dir, key string) {
	_ = gitx.Cmd(dir, "config", "--local", "--unset", key).Run()
}

func gitConfigGlobalGet(key string) string {
	out, err := gitx.Run("", "config", "--global", "--get", key)
	if err != nil {
		return ""
	}
	return out
}

// resolvedAccount is the provider's connected account name/email (github login+email,
// or the Bitbucket account), used to auto-seed the commit identity. Works for either
// connect method (token entry or OAuth).
func resolvedAccount(s *secrets.Data, host string) (name, email string) {
	if e, ok := s.Git[host]; ok {
		name, email = e.Login, e.Email
	}
	if host == "bitbucket.org" && s.Bitbucket != nil {
		name = firstNonEmpty(name, s.Bitbucket.Account)
		email = firstNonEmpty(email, s.Bitbucket.Email)
	}
	return
}

// providerIdentity is a git host's effective commit identity: the explicit override
// (GitIdentity) per field, falling back to the resolved account (auto-seed). Empty
// strings when nothing is known.
func providerIdentity(host string) (name, email string) {
	if host == "" {
		return "", ""
	}
	s, err := secrets.Load()
	if err != nil {
		return "", ""
	}
	id := s.GitIdentity[host]
	rn, re := resolvedAccount(s, host)
	return firstNonEmpty(id.Name, rn), firstNonEmpty(id.Email, re)
}

// applyGitIdentity writes the effective identity into dir's local .git/config, unless
// the repo carries a manual override (then it's left untouched). With no provider
// identity it clears the local keys so git falls back to the global default.
func applyGitIdentity(dir string) {
	if !isGitRepo(dir) {
		return
	}
	if gitConfigLocalGet(dir, identitySourceKey) == "manual" {
		return
	}
	name, email := providerIdentity(remoteHost(dir))
	if name == "" && email == "" {
		gitConfigLocalUnset(dir, "user.name")
		gitConfigLocalUnset(dir, "user.email")
		gitConfigLocalUnset(dir, identitySourceKey)
		return
	}
	if name != "" {
		gitConfigLocalSet(dir, "user.name", name)
	}
	if email != "" {
		gitConfigLocalSet(dir, "user.email", email)
	}
	gitConfigLocalSet(dir, identitySourceKey, "provider")
}

// reapplyProviderIdentity refreshes every cloned repo of the given host that isn't
// manually overridden — called when the provider's identity changes.
func reapplyProviderIdentity(host string) {
	ents, err := os.ReadDir(reposRoot())
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(reposRoot(), e.Name())
		if isGitRepo(dir) && remoteHost(dir) == host {
			applyGitIdentity(dir)
		}
	}
}

// --- HTTP handlers --------------------------------------------------------------

type identityReq struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// handleGitProviderIdentityPut sets a provider's explicit commit identity (PUT
// /api/connections/git/{host}/identity) and reapplies it to that host's repos. Empty
// fields clear the override (reverting to the auto-seeded account identity).
func handleGitProviderIdentityPut(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if _, ok := gitHosts[host]; !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_host", "unsupported host: "+host)
		return
	}
	var req identityReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	if s.GitIdentity == nil {
		s.GitIdentity = map[string]secrets.GitIdentity{}
	}
	s.GitIdentity[host] = secrets.GitIdentity{Name: strings.TrimSpace(req.Name), Email: strings.TrimSpace(req.Email)}
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	reapplyProviderIdentity(host)
	name, email := providerIdentity(host)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"host": host, "name": name, "email": email})
}

// handleGlobalIdentityGet / Put read and write the global default identity (~/.gitconfig).
func handleGlobalIdentityGet(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"name":  gitConfigGlobalGet("user.name"),
		"email": gitConfigGlobalGet("user.email"),
	})
}

func handleGlobalIdentityPut(w http.ResponseWriter, r *http.Request) {
	var req identityReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if n := strings.TrimSpace(req.Name); n != "" {
		_ = gitConfigGlobal("user.name", n)
	} else {
		_ = gitx.Cmd("", "config", "--global", "--unset", "user.name").Run()
	}
	if e := strings.TrimSpace(req.Email); e != "" {
		_ = gitConfigGlobal("user.email", e)
	} else {
		_ = gitx.Cmd("", "config", "--global", "--unset", "user.email").Run()
	}
	handleGlobalIdentityGet(w, r)
}

// repoIdentityInfo describes a repo's effective identity + where it comes from.
func repoIdentityInfo(dir string) map[string]any {
	source := gitConfigLocalGet(dir, identitySourceKey) // "manual" | "provider" | ""
	localName := gitConfigLocalGet(dir, "user.name")
	localEmail := gitConfigLocalGet(dir, "user.email")
	host := remoteHost(dir)
	pName, pEmail := providerIdentity(host)
	override := map[string]any{"name": "", "email": ""}
	if source == "manual" {
		override = map[string]any{"name": localName, "email": localEmail}
	}
	return map[string]any{
		"effective": map[string]any{
			"name":  firstNonEmpty(localName, gitConfigGlobalGet("user.name")),
			"email": firstNonEmpty(localEmail, gitConfigGlobalGet("user.email")),
		},
		"source":   source, // "manual" | "provider" | "" (global fallback)
		"override": override,
		"provider": map[string]any{"host": host, "name": pName, "email": pEmail},
	}
}

func handleRepoIdentityGet(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, repoIdentityInfo(dir))
}

// handleRepoIdentityPut sets or clears a repo's manual override. A non-empty name or
// email pins the repo (af.identitySource=manual); both empty clears it and reverts to
// the provider / global default.
func handleRepoIdentityPut(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	var req identityReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	name, email := strings.TrimSpace(req.Name), strings.TrimSpace(req.Email)
	if name == "" && email == "" {
		gitConfigLocalUnset(dir, identitySourceKey)
		applyGitIdentity(dir) // revert to provider / global
		httpx.WriteJSON(w, http.StatusOK, repoIdentityInfo(dir))
		return
	}
	if name != "" {
		gitConfigLocalSet(dir, "user.name", name)
	}
	if email != "" {
		gitConfigLocalSet(dir, "user.email", email)
	}
	gitConfigLocalSet(dir, identitySourceKey, "manual")
	httpx.WriteJSON(w, http.StatusOK, repoIdentityInfo(dir))
}
