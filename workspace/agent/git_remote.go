package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Remote repository listing: enumerate the repositories the connected provider
// account can clone, using the stored token, so the Console can offer a picker
// instead of asking the user to paste a clone URL. The token never leaves the
// Agent (the Control Plane only proxies). GitHub is implemented; Bitbucket — whose
// OAuth token needs on-the-fly refresh — is a follow-up.

type remoteRepo struct {
	FullName  string `json:"full_name"`
	CloneURL  string `json:"clone_url"`
	Private   bool   `json:"private"`
	UpdatedAt string `json:"updated_at"`
}

// GET /connections/git/{host}/repos
func handleListRemoteRepos(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	s, err := loadSecrets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	switch host {
	case "github.com":
		e, ok := s.Git[host]
		if !ok || e.Token == "" {
			writeErr(w, http.StatusBadRequest, "not_connected", "GitHub is not connected")
			return
		}
		repos, err := githubListRepos(e.Token)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "provider_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"host": host, "repos": repos})
	case "bitbucket.org":
		writeErr(w, http.StatusNotImplemented, "not_implemented", "Bitbucket repo listing is not implemented yet")
	default:
		writeErr(w, http.StatusBadRequest, "bad_host", "unsupported host: "+host)
	}
}

// GET /connections/git/{host}/branches?repo=owner/name — list the remote
// branches of one repository so the Console can offer a branch dropdown before
// cloning. Default branch is returned first. GitHub implemented; Bitbucket TBD.
func handleListRemoteBranches(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" || !strings.Contains(repo, "/") {
		writeErr(w, http.StatusBadRequest, "bad_repo", "repo=owner/name is required")
		return
	}
	s, err := loadSecrets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	switch host {
	case "github.com":
		e, ok := s.Git[host]
		if !ok || e.Token == "" {
			writeErr(w, http.StatusBadRequest, "not_connected", "GitHub is not connected")
			return
		}
		branches, def, err := githubListBranches(e.Token, repo)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "provider_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"branches": branches, "default": def})
	case "bitbucket.org":
		writeErr(w, http.StatusNotImplemented, "not_implemented", "Bitbucket branch listing is not implemented yet")
	default:
		writeErr(w, http.StatusBadRequest, "bad_host", "unsupported host: "+host)
	}
}

// githubListBranches returns branch names for owner/repo with the default branch
// first (so the dropdown preselects it).
func githubListBranches(token, repo string) ([]string, string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	def := githubDefaultBranch(client, token, repo) // best-effort; "" on failure

	next := "https://api.github.com/repos/" + repo + "/branches?per_page=100"
	names := []string{}
	for page := 0; page < 10 && next != ""; page++ {
		req, err := http.NewRequest("GET", next, nil)
		if err != nil {
			return nil, "", err
		}
		githubHeaders(req, token)
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", err
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				return nil, "", fmt.Errorf("github token rejected (re-connect GitHub)")
			}
			return nil, "", fmt.Errorf("github %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		var batch []struct {
			Name string `json:"name"`
		}
		err = json.NewDecoder(resp.Body).Decode(&batch)
		link := resp.Header.Get("Link")
		resp.Body.Close()
		if err != nil {
			return nil, "", err
		}
		for _, b := range batch {
			names = append(names, b.Name)
		}
		next = nextLink(link)
	}
	return orderDefaultFirst(names, def), def, nil
}

func githubDefaultBranch(client *http.Client, token, repo string) string {
	req, err := http.NewRequest("GET", "https://api.github.com/repos/"+repo, nil)
	if err != nil {
		return ""
	}
	githubHeaders(req, token)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var meta struct {
		DefaultBranch string `json:"default_branch"`
	}
	if json.NewDecoder(resp.Body).Decode(&meta) != nil {
		return ""
	}
	return meta.DefaultBranch
}

func orderDefaultFirst(names []string, def string) []string {
	if def == "" {
		return names
	}
	out := make([]string, 0, len(names))
	out = append(out, def)
	for _, n := range names {
		if n != def {
			out = append(out, n)
		}
	}
	if len(out) == 1 && len(names) == 0 {
		return names // default wasn't actually in the list and list was empty
	}
	return out
}

func githubHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

// githubListRepos returns repos the token can access (owner + collaborator + org
// member), most-recently-updated first, following pagination up to a sane cap.
func githubListRepos(token string) ([]remoteRepo, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	next := "https://api.github.com/user/repos?per_page=100&sort=updated&affiliation=owner,collaborator,organization_member"
	out := []remoteRepo{}
	for page := 0; page < 10 && next != ""; page++ {
		batch, link, err := githubReposPage(client, token, next)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		next = nextLink(link)
	}
	return out, nil
}

func githubReposPage(client *http.Client, token, url string) ([]remoteRepo, string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, "", err
	}
	githubHeaders(req, token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := strings.TrimSpace(string(b))
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, "", fmt.Errorf("github token rejected (re-connect GitHub)")
		}
		return nil, "", fmt.Errorf("github %d: %s", resp.StatusCode, msg)
	}
	var batch []remoteRepo
	if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
		return nil, "", err
	}
	return batch, resp.Header.Get("Link"), nil
}

// nextLink extracts the rel="next" URL from a GitHub Link header, or "" if none.
func nextLink(link string) string {
	for _, part := range strings.Split(link, ",") {
		segs := strings.Split(strings.TrimSpace(part), ";")
		if len(segs) < 2 {
			continue
		}
		url := strings.Trim(strings.TrimSpace(segs[0]), "<>")
		for _, p := range segs[1:] {
			if strings.TrimSpace(p) == `rel="next"` {
				return url
			}
		}
	}
	return ""
}
