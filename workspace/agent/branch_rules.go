package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchrule"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// Branch naming resolver (ADR 0103 decision 7). The Console and, later, the af MCP tool ask
// here instead of each naming branches in its own style.

var branchBitbucket = branchrule.NewBitbucketCache(fetchBranchingModel)

var branchModelHTTPClient = &http.Client{Timeout: 20 * time.Second}

// fetchBranchingModel reads Bitbucket's branching model with the member's connection.
func fetchBranchingModel(_ context.Context, ws, repo string) ([]byte, error) {
	s, err := secrets.Load()
	if err != nil {
		return nil, err
	}
	auth, err := gitx.BitbucketAuthHeader(s)
	if err != nil {
		return nil, branchrule.ErrNoConnection
	}
	endpoint := bitbucketAPIBase + "/2.0/repositories/" + url.PathEscape(ws) + "/" + url.PathEscape(repo) + "/branching-model"
	var body []byte
	get := func(a string) error {
		b, status, err := gitx.BitbucketGetStatus(branchModelHTTPClient, a, endpoint)
		switch {
		case err != nil:
			return fmt.Errorf("could not reach %s", bitbucketAPIBase)
		case status == http.StatusUnauthorized:
			return gitx.ErrBitbucketUnauthorized
		case status != http.StatusOK:
			return fmt.Errorf("bitbucket %d: %s", status, gitx.BitbucketErrText(b))
		}
		body = b
		return nil
	}
	err = gitx.RefreshBitbucketAndRetry(s, get(auth), get)
	return body, err
}

func branchRulesUserPath() string {
	return filepath.Join(paths.AgentConfigDir(), "branch-rules-user.json")
}

// branchTemplate is the user's default template. It has one home, ui-prefs
// workItemBranchTemplate, which an older Console keeps rendering itself.
func branchTemplate() string {
	s, _ := uiprefs.Read()["workItemBranchTemplate"].(string)
	return s
}

type branchContext struct {
	dir    string
	id     string
	repo   branchrule.Repo
	layers []branchrule.Layer
}

func loadBranchContext(w http.ResponseWriter, r *http.Request, refresh bool) (*branchContext, bool) {
	dir, ok := gitx.ResolveRepoDir(r.PathValue("name"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_repo", "invalid repo name")
		return nil, false
	}
	if !gitx.IsGitRepo(dir) {
		httpx.WriteErr(w, http.StatusNotFound, "not_git", "not a git working copy")
		return nil, false
	}
	return newBranchContext(r.Context(), dir, refresh), true
}

func newBranchContext(ctx context.Context, dir string, refresh bool) *branchContext {
	origin, _ := gitx.GitOriginURL(dir)
	id := branchrule.RepoID(origin)
	repo := branchrule.ReadRepo(ctx, dir, branchrule.ReadOptions{ID: id, Bitbucket: branchBitbucket, Refresh: refresh})
	layers := []branchrule.Layer{repo.Layer}
	layers = append(layers, userBranchLayers(branchTemplate())...)
	return &branchContext{dir: dir, id: id, repo: repo, layers: layers}
}

// userBranchLayers are the layers below the repository: the user's, then the built-in.
func userBranchLayers(template string) []branchrule.Layer {
	user := branchrule.ReadUser(branchRulesUserPath())
	return []branchrule.Layer{branchrule.UserLayer(user.Rules, template), branchrule.Builtin()}
}

// resolvedKindNames is sessionx.BranchKinds: the kinds the AI branch suggestion may pick from.
func resolvedKindNames(ctx context.Context, dir string) []string {
	c := newBranchContext(ctx, dir, false)
	var out []string
	for _, k := range branchrule.Effect(c.layers, c.id).Kinds {
		out = append(out, k.Kind)
	}
	return out
}

func (c *branchContext) sources(fields map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range fields {
		out[k] = v
	}
	if c.repo.Bitbucket != "" {
		out["bitbucket"] = c.repo.Bitbucket
		if c.repo.BitbucketFetchedAt != 0 {
			out["bitbucket_fetched_at"] = c.repo.BitbucketFetchedAt
		}
	}
	return out
}

func warningsOrEmpty(ws []branchrule.Warning) []branchrule.Warning {
	if ws == nil {
		return []branchrule.Warning{}
	}
	return ws
}

// branchRuleOut is GET /repos/{name}/branch-rule.
type branchRuleOut struct {
	RepoID     string                `json:"repo_id"`
	Name       string                `json:"name"`
	Base       string                `json:"base"`
	BaseBranch string                `json:"base_branch"`
	Kinds      []branchrule.KindView `json:"kinds"`
	Sources    map[string]any        `json:"sources"`
	Gitflow    string                `json:"gitflow"`
	Warnings   []branchrule.Warning  `json:"warnings"`
}

// branchNameOut is POST /repos/{name}/branch-name.
type branchNameOut struct {
	Name       string `json:"name"`
	NameEmpty  bool   `json:"name_empty"`
	Base       string `json:"base"`
	BaseBranch string `json:"base_branch"`
	Kind       string `json:"kind"`
	// Provisional is always false until the English slug (P2) exists.
	Provisional bool                 `json:"provisional"`
	Warnings    []branchrule.Warning `json:"warnings"`
	Sources     map[string]any       `json:"sources"`
}

// branchCheckOut is POST /repos/{name}/branch-name/check.
type branchCheckOut struct {
	Warnings []branchrule.Warning `json:"warnings"`
}

// GET /repos/{name}/branch-rule[?refresh=1]
func handleGetBranchRule(w http.ResponseWriter, r *http.Request) {
	c, ok := loadBranchContext(w, r, r.URL.Query().Get("refresh") == "1")
	if !ok {
		return
	}
	eff := branchrule.Effect(c.layers, c.id)
	warns := append([]branchrule.Warning{}, c.repo.Warnings...)
	base, branch, bw := branchrule.ResolveBase(c.dir, eff.Base)
	if bw != nil {
		warns = append(warns, *bw)
	}
	httpx.WriteJSON(w, http.StatusOK, branchRuleOut{
		RepoID: c.id, Name: eff.Name, Base: base, BaseBranch: branch, Kinds: eff.Kinds,
		Sources: c.sources(eff.Sources), Gitflow: c.repo.Gitflow, Warnings: warningsOrEmpty(warns),
	})
}

type branchNameRequest struct {
	Item    *branchrule.Item `json:"item,omitempty"`
	Session string           `json:"session,omitempty"`
	Kind    string           `json:"kind,omitempty"`
	Slug    string           `json:"slug,omitempty"`
}

// POST /repos/{name}/branch-name
func handleBranchName(w http.ResponseWriter, r *http.Request) {
	var req branchNameRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	item := req.Item
	if item == nil && strings.TrimSpace(req.Session) != "" {
		m, ok := session.ReadMeta(req.Session)
		if !ok {
			httpx.WriteErr(w, http.StatusNotFound, "session_not_found", "no such session")
			return
		}
		if wi := m.WorkItem; wi != nil {
			item = &branchrule.Item{Provider: wi.Provider, Key: wi.Key, Title: wi.Title, Type: wi.Type, Labels: wi.Labels}
		}
	}
	c, ok := loadBranchContext(w, r, false)
	if !ok {
		return
	}
	res := branchrule.Name(c.layers, c.id, branchrule.Request{Item: item, Kind: req.Kind, Slug: req.Slug})
	warns := append(append([]branchrule.Warning{}, c.repo.Warnings...), res.Warnings...)
	base, branch, bw := branchrule.ResolveBase(c.dir, res.Base)
	if bw != nil {
		warns = append(warns, *bw)
	}
	httpx.WriteJSON(w, http.StatusOK, branchNameOut{
		Name: res.Name, NameEmpty: res.NameEmpty, Base: base, BaseBranch: branch, Kind: res.Kind,
		Warnings: warningsOrEmpty(warns), Sources: c.sources(res.Sources),
	})
}

// POST /repos/{name}/branch-name/check
func handleBranchNameCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	c, ok := loadBranchContext(w, r, false)
	if !ok {
		return
	}
	eff := branchrule.Effect(c.layers, c.id)
	httpx.WriteJSON(w, http.StatusOK, branchCheckOut{
		Warnings: warningsOrEmpty(branchrule.CheckName(strings.TrimSpace(req.Name), eff.Kinds)),
	})
}

// GET /branch-rules/user
func handleGetUserBranchRules(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, branchrule.ReadUser(branchRulesUserPath()))
}

// PUT /branch-rules/user replaces the user layer. A rule that fails the key or ref-name
// checks is refused here; it is the name a rule produces that is only ever warned about.
func handlePutUserBranchRules(w http.ResponseWriter, r *http.Request) {
	var u branchrule.UserRules
	if !httpx.DecodeJSON(w, r, &u) {
		return
	}
	if err := branchrule.ValidateRules(u.Rules, true); err != nil {
		code := "invalid_rule"
		if errors.Is(err, branchrule.ErrBareStarName) {
			code = "bare_star_name"
		}
		httpx.WriteErr(w, http.StatusBadRequest, code, err.Error())
		return
	}
	if err := branchrule.WriteUser(branchRulesUserPath(), u); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, branchrule.ReadUser(branchRulesUserPath()))
}

func init() {
	sessionx.BranchKinds = resolvedKindNames
}

// branchPreviewRequest is POST /branch-rules/preview: the template as typed, not yet saved,
// and the sample items to render it for.
type branchPreviewRequest struct {
	Template string            `json:"template"`
	Items    []branchrule.Item `json:"items"`
}

type branchPreviewOut struct {
	Names []branchPreviewName `json:"names"`
}

type branchPreviewName struct {
	Name      string               `json:"name"`
	NameEmpty bool                 `json:"name_empty"`
	Kind      string               `json:"kind"`
	Warnings  []branchrule.Warning `json:"warnings"`
}

// POST /branch-rules/preview renders the user's template for the work-items settings, which
// belong to no working copy. It therefore resolves over the user and built-in layers only; a
// repository's own declaration can still change the name at launch.
func handleBranchRulesPreview(w http.ResponseWriter, r *http.Request) {
	var req branchPreviewRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if len(req.Items) > 10 {
		httpx.WriteErr(w, http.StatusBadRequest, "too_many_items", "at most 10 items")
		return
	}
	layers := userBranchLayers(strings.TrimSpace(req.Template))
	out := make([]branchPreviewName, 0, len(req.Items))
	for i := range req.Items {
		res := branchrule.Name(layers, "", branchrule.Request{Item: &req.Items[i]})
		out = append(out, branchPreviewName{Name: res.Name, NameEmpty: res.NameEmpty, Kind: res.Kind, Warnings: warningsOrEmpty(res.Warnings)})
	}
	httpx.WriteJSON(w, http.StatusOK, branchPreviewOut{Names: out})
}
