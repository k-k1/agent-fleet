// Package watchhook is the Agent side of ADR 0133 step 4: it creates a tenant-owned webhook hook
// at the Control Plane, installs it at GitHub or Bitbucket Cloud with the member's own token, and
// lists and removes it again. Private repositories get pokes only through such a hook; without
// one they stay on polling.
//
// The hook's secret is returned by the Control Plane once. It lives in this process's memory for
// the length of one Add and goes nowhere but the forge API request body: never a log line, never
// a file, never a result handed back to the caller. A forge install that fails deletes the
// Control Plane hook again, so a refused install leaves nothing behind.
package watchhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Source names.
const (
	GitHub    = "github"
	Bitbucket = "bitbucket"
)

var repoRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,100}/[A-Za-z0-9._~-]{1,100}$`)

// githubEvents are the events control-plane/internal/webhookpoke.ParseGitHub reads; any other
// event would only cost a delivery that names no subject.
var githubEvents = []string{
	"pull_request", "pull_request_review", "pull_request_review_comment", "pull_request_review_thread",
	"issue_comment", "check_run", "check_suite", "workflow_run",
}

// bitbucketEvents are the pullrequest:* family ParseBitbucket reads (it accepts the whole prefix).
var bitbucketEvents = []string{
	"pullrequest:created", "pullrequest:updated", "pullrequest:approved", "pullrequest:unapproved",
	"pullrequest:fulfilled", "pullrequest:rejected", "pullrequest:comment_created",
	"pullrequest:comment_updated", "pullrequest:comment_deleted",
	"pullrequest:changes_request_created", "pullrequest:changes_request_removed",
}

// Cred is the member's credential for one forge host.
type Cred struct {
	User  string
	Token string
	// Basic sends User:Token as HTTP Basic (a pasted Bitbucket API token); otherwise Token is a
	// bearer token (an OAuth connection, a GitHub token).
	Basic bool
}

// Error is a failure whose message is meant for the member. Status is the HTTP status the
// Agent route answers with.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func userErr(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, a...)}
}

// Hook is a tenant hook as the Control Plane lists it. It never carries the secret.
type Hook struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Repo      string `json:"repo,omitempty"`
	URL       string `json:"url"`
	CreatedAt string `json:"created_at,omitempty"`
}

// AddResult is what Add reports. Existing means a hook for the repository was already there and
// nothing was changed (its secret cannot be shown again).
type AddResult struct {
	Hook
	Existing bool   `json:"existing,omitempty"`
	Note     string `json:"note,omitempty"`
}

// RemoveResult is what Remove reports. ForgeRemoved is false when there was nothing to remove at
// the forge or it could not be done; Warning then says what the member still has to do.
type RemoveResult struct {
	Deleted      string `json:"deleted"`
	ForgeRemoved bool   `json:"forge_removed"`
	Warning      string `json:"warning,omitempty"`
}

// Manager holds the seams: the Control Plane base and token, the credential source and the forge
// API bases. Zero values of the bases mean the real forges.
type Manager struct {
	CPBase  string
	CPToken string
	// Creds returns the member's credential for host ("github.com" or "bitbucket.org").
	Creds func(ctx context.Context, host string) (Cred, error)
	HTTP  *http.Client

	GitHubAPI    string // default https://api.github.com
	BitbucketAPI string // default https://api.bitbucket.org/2.0

	mu sync.Mutex // one create or delete at a time, so two adds cannot both pass the existing check
}

func (m *Manager) client() *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (m *Manager) ghBase() string {
	if m.GitHubAPI != "" {
		return strings.TrimRight(m.GitHubAPI, "/")
	}
	return "https://api.github.com"
}

func (m *Manager) bbBase() string {
	if m.BitbucketAPI != "" {
		return strings.TrimRight(m.BitbucketAPI, "/")
	}
	return "https://api.bitbucket.org/2.0"
}

// --- Control Plane ---------------------------------------------------------------------------

type cpHook struct {
	Hook
	Secret string `json:"secret,omitempty"`
}

func (m *Manager) cp(ctx context.Context, method, path string, in, out any) error {
	if m.CPBase == "" || m.CPToken == "" {
		return userErr(http.StatusServiceUnavailable, "no_bridge", "this workspace has no link to the Control Plane's webhook bridge (no public URL configured, or the Agent was started without it)")
	}
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, m.CPBase+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+m.CPToken)
	resp, err := m.client().Do(req)
	if err != nil {
		return userErr(http.StatusBadGateway, "cp_unreachable", "cannot reach the Control Plane")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Code, Message string }
		}
		msg := fmt.Sprintf("the Control Plane answered HTTP %d", resp.StatusCode)
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		status := resp.StatusCode
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			status = http.StatusBadGateway
		}
		return userErr(status, "cp_"+strings.TrimSpace(e.Error.Code), "%s", msg)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

func (m *Manager) cpList(ctx context.Context) ([]Hook, error) {
	var res struct {
		Hooks []Hook `json:"hooks"`
	}
	if err := m.cp(ctx, http.MethodGet, "/internal/watch-hooks", nil, &res); err != nil {
		return nil, err
	}
	return res.Hooks, nil
}

// List is the member's hooks at the Control Plane.
func (m *Manager) List(ctx context.Context) ([]Hook, error) { return m.cpList(ctx) }

func (m *Manager) cpDelete(ctx context.Context, id string) error {
	return m.cp(ctx, http.MethodDelete, "/internal/watch-hooks/"+url.PathEscape(id), nil, nil)
}

// --- Add ---------------------------------------------------------------------------------------

func hostOf(source string) string {
	if source == Bitbucket {
		return "bitbucket.org"
	}
	return "github.com"
}

// Add creates the hook and installs it at the forge. Order matters: the credential is checked
// first (no connection costs nothing), then the Control Plane hook is made, then the install;
// when the install fails the Control Plane hook is deleted again.
func (m *Manager) Add(ctx context.Context, source, repo string) (*AddResult, error) {
	if source != GitHub && source != Bitbucket {
		return nil, userErr(http.StatusBadRequest, "invalid", "source must be github or bitbucket")
	}
	// "." and ".." match the pattern but would climb out of /repos/ or /repositories/ in the
	// forge URL.
	if !repoRE.MatchString(repo) || dotSegment(repo) {
		return nil, userErr(http.StatusBadRequest, "invalid", "repo must be owner/name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	host := hostOf(source)
	cred, err := m.Creds(ctx, host)
	if err != nil || cred.Token == "" {
		return nil, userErr(http.StatusConflict, "no_credential", "no %s connection with a token: add one under Settings > Connections, then retry", host)
	}

	existing, err := m.cpList(ctx)
	if err != nil {
		return nil, err
	}
	for _, h := range existing {
		if h.Source == source && strings.EqualFold(h.Repo, repo) {
			return &AddResult{Hook: h, Existing: true, Note: "a hook for this repository already exists, nothing changed. Its secret is shown only at creation: to start over, run `af-watch-hook rm " + h.ID + "` and add again."}, nil
		}
	}

	var created cpHook
	if err := m.cp(ctx, http.MethodPost, "/internal/watch-hooks", map[string]string{"source": source, "repo": repo}, &created); err != nil {
		return nil, err
	}
	if created.ID == "" || created.URL == "" || created.Secret == "" {
		if created.ID != "" {
			m.rollback(created.ID)
		}
		return nil, userErr(http.StatusBadGateway, "cp_bad_answer", "the Control Plane's answer to the hook creation was incomplete")
	}

	if err := m.install(ctx, cred, source, repo, created.URL, created.Secret); err != nil {
		rbErr := m.rollback(created.ID)
		var ue *Error
		if !errors.As(err, &ue) {
			ue = userErr(http.StatusBadGateway, "forge_failed", "installing the hook at %s failed: %s", host, scrub(err.Error(), created.Secret, cred.Token))
		}
		out := *ue
		if rbErr != nil {
			out.Message += fmt.Sprintf(" The Control Plane hook %s could not be removed again (%v): run `af-watch-hook rm %s`.", created.ID, rbErr, created.ID)
		}
		return nil, &out
	}
	return &AddResult{Hook: created.Hook}, nil
}

// rollback deletes a just-created Control Plane hook. It outlives a cancelled request: the
// caller giving up must not leave a hook behind.
func (m *Manager) rollback(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return m.cpDelete(ctx, id)
}

func scrub(s string, secrets ...string) string {
	for _, x := range secrets {
		if x != "" {
			s = strings.ReplaceAll(s, x, "[redacted]")
		}
	}
	return s
}

// --- forge calls -------------------------------------------------------------------------------

func (m *Manager) forge(ctx context.Context, cred Cred, source, method, rawURL string, in, out any) (int, []byte, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return 0, nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if source == GitHub {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	if cred.Basic {
		req.SetBasicAuth(cred.User, cred.Token)
	} else {
		req.Header.Set("Authorization", "Bearer "+cred.Token)
	}
	resp, err := m.client().Do(req)
	if err != nil {
		// The URL and the transport detail stay out: a client error can carry the request line.
		return 0, nil, errors.New("cannot reach the forge")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 300 && out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, b, errors.New("unreadable answer from the forge")
		}
	}
	return resp.StatusCode, b, nil
}

// forgeMessage is the message field of a forge error body, never the body itself.
func forgeMessage(b []byte) string {
	var e struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) != nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Error.Message
}

// denied turns a forge refusal into advice. GitHub answers 404 for a repository the token cannot
// administer as well as for one that does not exist, so the text names both.
func denied(source, repo string, status int, b []byte) *Error {
	detail := ""
	if m := forgeMessage(b); m != "" {
		detail = " (" + m + ")"
	}
	switch source {
	case GitHub:
		return userErr(http.StatusForbidden, "forge_denied",
			"GitHub refused (HTTP %d%s): the connected token cannot create webhooks on %s, or the repository does not exist. "+
				"Creating a webhook needs admin rights on the repository: a classic token with the repo (or admin:repo_hook) scope, or a fine-grained token with Webhooks: Read and write, owned by someone who administers it. "+
				"No hook was left behind. Without admin rights, ask a repository admin, or use your own GitHub App and paste the hook secret into its webhook settings (see the Webhook watches guide).",
			status, detail, repo)
	default:
		return userErr(http.StatusForbidden, "forge_denied",
			"Bitbucket refused (HTTP %d%s): the connected credential cannot create webhooks on %s, or the repository does not exist. "+
				"It needs the Webhooks scope (read and write) and admin rights on the repository. "+
				"No hook was left behind. Ask a repository admin, or use the long poll.",
			status, detail, repo)
	}
}

func (m *Manager) install(ctx context.Context, cred Cred, source, repo, hookURL, secret string) error {
	switch source {
	case GitHub:
		// content_type json and the sha256 signature (X-Hub-Signature-256) are what watchingress
		// verifies; insecure_ssl stays 0 so the delivery is TLS-verified.
		payload := map[string]any{
			"name": "web", "active": true, "events": githubEvents,
			"config": map[string]string{"url": hookURL, "content_type": "json", "secret": secret, "insecure_ssl": "0"},
		}
		status, b, err := m.forge(ctx, cred, source, http.MethodPost, m.ghBase()+"/repos/"+repo+"/hooks", payload, nil)
		if err != nil {
			return err
		}
		switch {
		case status == http.StatusCreated || status == http.StatusOK:
			return nil
		case status == 401 || status == 403 || status == 404:
			return denied(source, repo, status, b)
		default:
			return fmt.Errorf("GitHub answered HTTP %d %s", status, forgeMessage(b))
		}
	default:
		// Bitbucket Cloud signs with X-Hub-Signature (sha256=…) when a secret is set, the header
		// watchingress checks for this source; the body is JSON.
		payload := map[string]any{
			"description": "Agent Fleet watch", "url": hookURL, "active": true, "secret": secret,
			"skip_cert_verification": false, "events": bitbucketEvents,
		}
		status, b, err := m.forge(ctx, cred, source, http.MethodPost, m.bbBase()+"/repositories/"+repo+"/hooks", payload, nil)
		if err != nil {
			return err
		}
		switch {
		case status == http.StatusCreated || status == http.StatusOK:
			return nil
		case status == 401 || status == 403 || status == 404:
			return denied(source, repo, status, b)
		default:
			return fmt.Errorf("Bitbucket answered HTTP %d %s", status, forgeMessage(b))
		}
	}
}

// --- Remove ------------------------------------------------------------------------------------

// Remove deletes the hook at the forge and then at the Control Plane. The Control Plane hook goes
// even when the forge step cannot be done: it is what makes a delivery verify, so retiring it
// makes any forge hook left behind inert, and the warning tells the member to delete that one.
func (m *Manager) Remove(ctx context.Context, id string) (*RemoveResult, error) {
	if id == "" {
		return nil, userErr(http.StatusBadRequest, "invalid", "a hook id is required (af-watch-hook list)")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	hooks, err := m.cpList(ctx)
	if err != nil {
		return nil, err
	}
	var h *Hook
	for i := range hooks {
		if hooks[i].ID == id {
			h = &hooks[i]
		}
	}
	if h == nil {
		return nil, userErr(http.StatusNotFound, "not_found", "no such hook: %s", id)
	}

	res := &RemoveResult{Deleted: id}
	if h.Repo == "" {
		res.Warning = "this hook is not tied to a repository, so it cannot be looked up at the forge: delete its webhook in the repository settings by hand (URL " + h.URL + ")"
	} else {
		removed, warn := m.removeAtForge(ctx, *h)
		res.ForgeRemoved, res.Warning = removed, warn
	}
	if err := m.cpDelete(ctx, id); err != nil {
		var ue *Error
		if errors.As(err, &ue) && ue.Status == http.StatusNotFound {
			return res, nil // already gone
		}
		return nil, err
	}
	return res, nil
}

func (m *Manager) removeAtForge(ctx context.Context, h Hook) (bool, string) {
	manual := fmt.Sprintf("delete the webhook with URL %s in the %s repository settings by hand; it no longer verifies, so it is harmless meanwhile", h.URL, h.Repo)
	cred, err := m.Creds(ctx, hostOf(h.Source))
	if err != nil || cred.Token == "" {
		return false, "no " + hostOf(h.Source) + " token to remove the forge webhook: " + manual
	}
	switch h.Source {
	case GitHub:
		status, b, err := m.forge(ctx, cred, GitHub, http.MethodGet, m.ghBase()+"/repos/"+h.Repo+"/hooks?per_page=100", nil, nil)
		if err != nil {
			return false, "could not list the repository's webhooks (" + err.Error() + "): " + manual
		}
		if status != http.StatusOK {
			return false, fmt.Sprintf("GitHub refused to list the webhooks (HTTP %d: the token needs admin rights on %s): %s", status, h.Repo, manual)
		}
		var list []struct {
			ID     int64 `json:"id"`
			Config struct {
				URL string `json:"url"`
			} `json:"config"`
		}
		if json.Unmarshal(b, &list) != nil {
			return false, "unreadable webhook list from GitHub: " + manual
		}
		for _, w := range list {
			if w.Config.URL == h.URL {
				st, _, err := m.forge(ctx, cred, GitHub, http.MethodDelete, fmt.Sprintf("%s/repos/%s/hooks/%d", m.ghBase(), h.Repo, w.ID), nil, nil)
				if err != nil || (st != http.StatusNoContent && st != http.StatusNotFound) {
					return false, fmt.Sprintf("GitHub did not delete the webhook (HTTP %d): %s", st, manual)
				}
				return true, ""
			}
		}
		return false, "" // not installed at GitHub (already deleted there): nothing to do
	default:
		next := m.bbBase() + "/repositories/" + h.Repo + "/hooks?pagelen=100"
		for page := 0; next != "" && page < 10; page++ {
			status, b, err := m.forge(ctx, cred, Bitbucket, http.MethodGet, next, nil, nil)
			if err != nil {
				return false, "could not list the repository's webhooks (" + err.Error() + "): " + manual
			}
			if status != http.StatusOK {
				return false, fmt.Sprintf("Bitbucket refused to list the webhooks (HTTP %d: the credential needs the Webhooks scope and admin rights on %s): %s", status, h.Repo, manual)
			}
			var list struct {
				Values []struct {
					UUID string `json:"uuid"`
					URL  string `json:"url"`
				} `json:"values"`
				Next string `json:"next"`
			}
			if json.Unmarshal(b, &list) != nil {
				return false, "unreadable webhook list from Bitbucket: " + manual
			}
			for _, w := range list.Values {
				if w.URL == h.URL {
					st, _, err := m.forge(ctx, cred, Bitbucket, http.MethodDelete, m.bbBase()+"/repositories/"+h.Repo+"/hooks/"+url.PathEscape(w.UUID), nil, nil)
					if err != nil || (st != http.StatusNoContent && st != http.StatusNotFound) {
						return false, fmt.Sprintf("Bitbucket did not delete the webhook (HTTP %d): %s", st, manual)
					}
					return true, ""
				}
			}
			next = list.Next
		}
		return false, ""
	}
}

func dotSegment(repo string) bool {
	for _, p := range strings.Split(repo, "/") {
		if p == "." || p == ".." {
			return true
		}
	}
	return false
}
