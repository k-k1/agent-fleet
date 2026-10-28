package mcpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchrule"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// mcpToolBranchName is the session-side face of POST /repos/{name}/branch-name (ADR 0103
// decision 7), so agents and skills take the user's naming rules instead of inventing names.
const mcpToolBranchName = "branch_name"

// branchNameArgs is the resolver's request plus the working copy it is asked about.
type branchNameArgs struct {
	Repo    string           `json:"repo,omitempty"`
	Item    *branchrule.Item `json:"item,omitempty"`
	Session string           `json:"session,omitempty"`
	Kind    string           `json:"kind,omitempty"`
	Slug    string           `json:"slug,omitempty"`
}

func mcpBranchName(id json.RawMessage, raw json.RawMessage) []byte {
	if !selfReportOnly() {
		return mcpToolErr(id, "branch_name はセッション側の Agent Fleet サーバー専用です")
	}
	var a branchNameArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return mcpToolErr(id, "branch_name の引数を読めません: "+err.Error())
		}
	}
	a.Repo, a.Session = strings.TrimSpace(a.Repo), strings.TrimSpace(a.Session)

	// The owner is needed for either default. Failing to find it only matters when the repo
	// has to come from it: an explicit repo with no item simply names no item.
	owner, ownerErr := mcpOwningSession()
	if a.Item == nil && a.Session == "" && ownerErr == nil {
		a.Session = owner
	}
	if a.Repo == "" {
		if ownerErr != nil {
			return mcpToolErr(id, "作業コピーを特定できません。repo（~/repos 直下のフォルダ名）を指定してください: "+ownerErr.Error())
		}
		repo, err := sessionWorkingCopy(owner)
		if err != nil {
			return mcpToolErr(id, err.Error())
		}
		a.Repo = repo
	}
	// Checked here with the Agent's own rule: "." and ".." pass url.PathEscape, and the mux
	// redirects them to a route-less path whose bare 404 would read as "no resolver".
	if _, ok := gitx.ResolveRepoDir(a.Repo); !ok {
		return mcpToolErr(id, badRepoMsg(a.Repo))
	}

	body, _ := json.Marshal(struct {
		Item    *branchrule.Item `json:"item,omitempty"`
		Session string           `json:"session,omitempty"`
		Kind    string           `json:"kind,omitempty"`
		Slug    string           `json:"slug,omitempty"`
	}{a.Item, a.Session, a.Kind, a.Slug})
	out, err := agentDo(http.MethodPost, "/repos/"+url.PathEscape(a.Repo)+"/branch-name", body)
	if err != nil {
		return mcpToolErr(id, branchNameErr(a, err))
	}
	return mcpTextResult(id, out)
}

// sessionWorkingCopy is the folder under ~/repos that a session works in: Meta.Dir is the
// working copy root even when the agent runs in a subdirectory of it.
func sessionWorkingCopy(name string) (string, error) {
	m, ok := session.ReadMeta(name)
	if !ok {
		return "", fmt.Errorf("セッション %s の記録が見つからないため作業コピーを特定できません。repo を指定してください", name)
	}
	rel, err := filepath.Rel(gitx.ReposRoot(), m.Dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.ContainsRune(rel, filepath.Separator) {
		return "", fmt.Errorf("このセッションの作業フォルダ %s は ~/repos 直下の作業コピーではありません。repo を指定してください", m.Dir)
	}
	return rel, nil
}

// branchNameErr turns the resolver's refusals into what the caller should do next. A 404 with
// no error code is the mux's own: an Agent from before ADR 0103 has no such route.
func branchNameErr(a branchNameArgs, err error) string {
	var he *agentHTTPError
	if !errors.As(err, &he) {
		return "ブランチ名リゾルバーに接続できません（Workspace Agent）: " + err.Error()
	}
	switch code := he.code(); {
	case code == "bad_repo":
		return badRepoMsg(a.Repo)
	case code == "not_git":
		return fmt.Sprintf("~/repos に git の作業コピー %q がありません", a.Repo)
	case code == "session_not_found":
		return fmt.Sprintf("セッション %q がありません", a.Session)
	case code == "" && he.StatusCode == http.StatusNotFound:
		return "この Workspace Agent にはブランチ名リゾルバーがありません（ADR 0103 より前の版）。名前は利用者に確認してください"
	default:
		return "ブランチ名の解決に失敗しました: " + he.Error()
	}
}

func badRepoMsg(repo string) string {
	return fmt.Sprintf("作業コピー %q は ~/repos 直下のフォルダ名として使えません", repo)
}

// branchNameSchema is branch_name as every session is offered it.
func branchNameSchema() map[string]any {
	return map[string]any{
		"name": "branch_name",
		"description": "Agent Fleet: resolve the branch name and base your user's naming rules give, for a working copy. " +
			"Call it before creating or renaming a branch instead of making a name up. " +
			"With no arguments it names the work your session was launched for, in your own working copy. " +
			"Pass item, or kind and slug, to name other work. Warnings are advice; name_empty means no name could be made.",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"repo": map[string]any{"type": "string", "description": "Working copy folder directly under ~/repos (default: your own)"},
				"item": map[string]any{
					"type": "object", "additionalProperties": false,
					"description": "The issue / ticket to name the branch for",
					"properties": map[string]any{
						"provider": map[string]any{"type": "string", "description": "github, jira, ..."},
						"key":      map[string]any{"type": "string", "description": "Issue number or ticket key, e.g. 1128 or PROJ-12"},
						"title":    map[string]any{"type": "string"},
						"type":     map[string]any{"type": "string"},
						"labels":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
					"required": []string{"key"},
				},
				"session": map[string]any{"type": "string", "description": "Session whose launch work item to use when item is omitted (default: yours)"},
				"kind":    map[string]any{"type": "string", "description": "feature, fix, docs, ... (default: from the item, else feature)"},
				"slug":    map[string]any{"type": "string", "description": "Short English slug (default: from the item's title)"},
			},
		},
	}
}

// branchTools is part of every session's surface: it only reads the rules, and a session that
// cannot ask is the one that falls back to a hard-coded style.
func branchTools() []toolSpec {
	return []toolSpec{{
		name: mcpToolBranchName, session: branchNameSchema, ownArgs: true,
		handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
			return mcpBranchName(req.ID, p.Args)
		},
	}}
}

// mcpStdioBranchTools is the branch family's session list.
func mcpStdioBranchTools() []map[string]any {
	return sessionTools(branchTools())
}
