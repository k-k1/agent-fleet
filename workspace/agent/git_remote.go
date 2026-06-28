package main

import (
	"bytes"
	"encoding/base64"
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
// Agent (the Control Plane only proxies). Both GitHub and Bitbucket are supported;
// Bitbucket's OAuth token is refreshed on the fly (or token-paste falls back to
// HTTP Basic).

type remoteRepo struct {
	FullName  string `json:"full_name"`
	CloneURL  string `json:"clone_url"`
	Private   bool   `json:"private"`
	UpdatedAt string `json:"updated_at"`
}

// remoteBranch is one branch of a remote repo with its last-commit time and subject,
// so the Console can sort newest-first and show context (matching the local branch
// modal). Default flags the repo's default branch.
type remoteBranch struct {
	Name    string `json:"name"`
	Unix    int64  `json:"unix"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
	Default bool   `json:"default"`
}

// parseISOUnix parses an RFC3339 (optionally fractional) timestamp to unix seconds
// (0 on failure). GitHub uses "…Z"; Bitbucket uses "…+00:00", sometimes fractional.
func parseISOUnix(s string) int64 {
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// firstLine returns the first line of s (commit subject from a full message).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
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
		auth, err := bitbucketAuthHeader(s)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "not_connected", err.Error())
			return
		}
		repos, err := bitbucketListRepos(auth)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "provider_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"host": host, "repos": repos})
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
		auth, err := bitbucketAuthHeader(s)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "not_connected", err.Error())
			return
		}
		branches, def, err := bitbucketListBranchesRich(auth, repo)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "provider_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"branches": branches, "default": def})
	default:
		writeErr(w, http.StatusBadRequest, "bad_host", "unsupported host: "+host)
	}
}

// splitRepo splits "owner/name" into its parts.
func splitRepo(repo string) (owner, name string, ok bool) {
	i := strings.IndexByte(repo, '/')
	if i <= 0 || i == len(repo)-1 {
		return "", "", false
	}
	return repo[:i], repo[i+1:], true
}

// githubListBranches lists branches of owner/repo with each branch's last-commit
// time and subject, ordered newest-commit-first. It uses the GraphQL API because the
// REST branches endpoint carries no commit date (and can't order by it); GraphQL's
// refs(orderBy: TAG_COMMIT_DATE) does both in one round trip.
func githubListBranches(token, repo string) ([]remoteBranch, string, error) {
	owner, name, ok := splitRepo(repo)
	if !ok {
		return nil, "", fmt.Errorf("repo must be owner/name")
	}
	const query = `query($owner:String!,$name:String!,$cursor:String){repository(owner:$owner,name:$name){` +
		`defaultBranchRef{name} refs(refPrefix:"refs/heads/",first:100,after:$cursor,` +
		`orderBy:{field:TAG_COMMIT_DATE,direction:DESC}){nodes{name target{... on Commit{committedDate messageHeadline}}}` +
		`pageInfo{hasNextPage endCursor}}}}`
	client := &http.Client{Timeout: 20 * time.Second}
	out := []remoteBranch{}
	def, cursor := "", ""
	for page := 0; page < 10; page++ {
		vars := map[string]any{"owner": owner, "name": name}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
		req, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(body))
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", err
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			if resp.StatusCode == http.StatusUnauthorized {
				return nil, "", fmt.Errorf("github token rejected (re-connect GitHub)")
			}
			return nil, "", fmt.Errorf("github graphql %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		var gr struct {
			Data struct {
				Repository struct {
					DefaultBranchRef struct {
						Name string `json:"name"`
					} `json:"defaultBranchRef"`
					Refs struct {
						Nodes []struct {
							Name   string `json:"name"`
							Target struct {
								CommittedDate   string `json:"committedDate"`
								MessageHeadline string `json:"messageHeadline"`
							} `json:"target"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"refs"`
				} `json:"repository"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(b, &gr); err != nil {
			return nil, "", err
		}
		if len(gr.Errors) > 0 {
			return nil, "", fmt.Errorf("github graphql: %s", gr.Errors[0].Message)
		}
		def = gr.Data.Repository.DefaultBranchRef.Name
		for _, n := range gr.Data.Repository.Refs.Nodes {
			out = append(out, remoteBranch{
				Name: n.Name, Unix: parseISOUnix(n.Target.CommittedDate),
				Date: n.Target.CommittedDate, Subject: n.Target.MessageHeadline,
				Default: n.Name == def,
			})
		}
		if !gr.Data.Repository.Refs.PageInfo.HasNextPage {
			break
		}
		cursor = gr.Data.Repository.Refs.PageInfo.EndCursor
	}
	return out, def, nil
}

// githubAccount returns the authenticated GitHub login (e.g. "octocat") and its
// email (may be "" when the account hides it), via GET /user. Used to show the real
// account in Connections instead of the "x-access-token" git-username placeholder.
func githubAccount(token string) (login, email string, err error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		return "", "", err
	}
	githubHeaders(req, token)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("github /user %d", resp.StatusCode)
	}
	var u struct {
		Login string `json:"login"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", "", err
	}
	return u.Login, u.Email, nil
}

// bitbucketAccount returns the authenticated Bitbucket handle (username/nickname)
// and primary email. The handle comes from GET /2.0/user; the email from a second,
// best-effort GET /2.0/user/emails (empty when the token lacks the email scope).
// Requires the token's "account" scope; callers fall back to email/placeholder.
func bitbucketAccount(auth string) (handle, email string, err error) {
	client := &http.Client{Timeout: 10 * time.Second}
	body, err := bitbucketGet(client, auth, "https://api.bitbucket.org/2.0/user")
	if err != nil {
		return "", "", err
	}
	var u struct {
		Username    string `json:"username"`
		Nickname    string `json:"nickname"`
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return "", "", err
	}
	handle = firstNonEmpty(u.Username, u.Nickname, u.DisplayName)
	if eb, eerr := bitbucketGet(client, auth, "https://api.bitbucket.org/2.0/user/emails"); eerr == nil {
		var er struct {
			Values []struct {
				Email     string `json:"email"`
				IsPrimary bool   `json:"is_primary"`
			} `json:"values"`
		}
		if json.Unmarshal(eb, &er) == nil {
			for _, v := range er.Values {
				if v.IsPrimary {
					email = v.Email
					break
				}
			}
			if email == "" && len(er.Values) > 0 {
				email = er.Values[0].Email
			}
		}
	}
	return handle, email, nil
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

// --- Bitbucket Cloud (read-only listing) ---

// bitbucketAuthHeader returns the Authorization header value for the Bitbucket API.
// OAuth creds (refreshed on the fly, like the git credential helper) are preferred;
// a token-paste connection (Atlassian email + API token) falls back to HTTP Basic.
// May refresh and persist the OAuth token as a side effect.
func bitbucketAuthHeader(s *secretsData) (string, error) {
	if s.Bitbucket != nil && s.Bitbucket.AccessToken != "" {
		c := *s.Bitbucket
		if time.Now().Unix() >= c.Expiry-120 { // refresh within 2 min of expiry
			if nc, err := refreshBitbucket(c); err == nil {
				c = nc
				s.Bitbucket = &c
				_ = s.save()
			}
		}
		return "Bearer " + c.AccessToken, nil
	}
	if e, ok := s.Git["bitbucket.org"]; ok && e.Token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte(e.User + ":" + e.Token))
		return "Basic " + basic, nil
	}
	return "", fmt.Errorf("Bitbucket is not connected")
}

// bitbucketGet does an authorized GET and returns the body, mapping common errors.
func bitbucketGet(client *http.Client, auth, url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("bitbucket token rejected (re-connect Bitbucket)")
		}
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("bitbucket %d: %s", resp.StatusCode, msg)
	}
	return body, nil
}

// bitbucketListRepos enumerates clonable repos. The cross-workspace listing
// (GET /2.0/repositories?role=member) was retired (410, CHANGE-2770), so we list
// the user's workspaces and aggregate each workspace's repositories instead.
func bitbucketListRepos(auth string) ([]remoteRepo, error) {
	const maxRepos = 500
	client := &http.Client{Timeout: 15 * time.Second}
	slugs, err := bitbucketWorkspaces(client, auth)
	if err != nil {
		return nil, err
	}
	out := []remoteRepo{}
	for _, slug := range slugs {
		next := "https://api.bitbucket.org/2.0/repositories/" + slug + "?sort=-updated_on&pagelen=100"
		for page := 0; page < 20 && next != "" && len(out) < maxRepos; page++ {
			batch, n, err := bitbucketRepoPage(client, auth, next)
			if err != nil {
				break // skip a failing workspace; keep aggregating the others
			}
			out = append(out, batch...)
			next = n
		}
		if len(out) >= maxRepos {
			break
		}
	}
	return out, nil
}

// bitbucketWorkspaces lists the slugs of workspaces the token can access.
func bitbucketWorkspaces(client *http.Client, auth string) ([]string, error) {
	next := "https://api.bitbucket.org/2.0/user/workspaces?pagelen=100"
	slugs := []string{}
	for page := 0; page < 10 && next != ""; page++ {
		body, err := bitbucketGet(client, auth, next)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Values []struct {
				Slug      string `json:"slug"`
				Workspace struct {
					Slug string `json:"slug"`
				} `json:"workspace"`
			} `json:"values"`
			Next string `json:"next"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, err
		}
		for _, v := range resp.Values {
			slug := v.Workspace.Slug
			if slug == "" {
				slug = v.Slug
			}
			if slug != "" {
				slugs = append(slugs, slug)
			}
		}
		next = resp.Next
	}
	return slugs, nil
}

// bitbucketRepoPage parses one page of a workspace's repositories.
func bitbucketRepoPage(client *http.Client, auth, url string) ([]remoteRepo, string, error) {
	body, err := bitbucketGet(client, auth, url)
	if err != nil {
		return nil, "", err
	}
	var resp struct {
		Values []struct {
			FullName  string `json:"full_name"`
			IsPrivate bool   `json:"is_private"`
			UpdatedOn string `json:"updated_on"`
			Links     struct {
				Clone []struct {
					Name string `json:"name"`
					Href string `json:"href"`
				} `json:"clone"`
			} `json:"links"`
		} `json:"values"`
		Next string `json:"next"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, "", err
	}
	out := make([]remoteRepo, 0, len(resp.Values))
	for _, v := range resp.Values {
		href := ""
		for _, c := range v.Links.Clone {
			if c.Name == "https" {
				href = c.Href
				break
			}
		}
		out = append(out, remoteRepo{
			FullName: v.FullName, CloneURL: href, Private: v.IsPrivate, UpdatedAt: v.UpdatedOn,
		})
	}
	return out, resp.Next, nil
}

// bitbucketListBranchesRich lists branches of workspace/repo_slug with each branch's
// last-commit time and subject, ordered newest-commit-first. Bitbucket's branch
// listing includes target.date/message, so one paged call (sort=-target.date) does it.
func bitbucketListBranchesRich(auth, repo string) ([]remoteBranch, string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	def := bitbucketDefaultBranch(client, auth, repo) // best-effort

	next := "https://api.bitbucket.org/2.0/repositories/" + repo +
		"/refs/branches?pagelen=100&sort=-target.date&fields=values.name,values.target.date,values.target.message,next"
	out := []remoteBranch{}
	for page := 0; page < 10 && next != ""; page++ {
		body, err := bitbucketGet(client, auth, next)
		if err != nil {
			return nil, "", err
		}
		var resp struct {
			Values []struct {
				Name   string `json:"name"`
				Target struct {
					Date    string `json:"date"`
					Message string `json:"message"`
				} `json:"target"`
			} `json:"values"`
			Next string `json:"next"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, "", err
		}
		for _, b := range resp.Values {
			out = append(out, remoteBranch{
				Name: b.Name, Unix: parseISOUnix(b.Target.Date), Date: b.Target.Date,
				Subject: firstLine(b.Target.Message), Default: b.Name == def,
			})
		}
		next = resp.Next
	}
	return out, def, nil
}

func bitbucketDefaultBranch(client *http.Client, auth, repo string) string {
	body, err := bitbucketGet(client, auth, "https://api.bitbucket.org/2.0/repositories/"+repo)
	if err != nil {
		return ""
	}
	var meta struct {
		MainBranch struct {
			Name string `json:"name"`
		} `json:"mainbranch"`
	}
	if json.Unmarshal(body, &meta) != nil {
		return ""
	}
	return meta.MainBranch.Name
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
