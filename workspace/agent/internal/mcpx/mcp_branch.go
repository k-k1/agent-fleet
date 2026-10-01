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
		return fmt.Sprintf("作業コピー %q は ~/repos 直下のフォルダ名として使えません", a.Repo)
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
