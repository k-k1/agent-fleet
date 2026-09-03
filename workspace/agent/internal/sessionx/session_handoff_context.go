package sessionx

// 引き継ぎの座標（docs/log/77 §77.5）。
//
// **サーバが自分で調べる**ためのエンドポイントである。引き継ぎ先は別メンバーの Workspace で、
// そこから所有者のディスクは見えない。だから「どの remote の・どのブランチの・どの commit を
// 引き継ぐのか」は、モデルにも Console にも書かせず、ここが git に聞いて答える。
//
// **remote URL をモデル入力にしない理由**（ADR 0057 決定5）: Console はこの値をクローン導線に
// 変える。モデルが書ける構造化フィールドにすると、汚染されたリポジトリを読ませるだけで
// 相手に任意の remote をクローンさせられる形になる。
//
// 返す `blocked` は**引き継ぎを止めるべきか**の判断そのものを載せる。呼び出し側（CP）が
// ahead や upstream の有無から組み立て直すと、条件が 2 か所に分かれて必ずずれる。

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// handoffContext はセッションの作業コピーの「引き継げる状態か」を 1 つに畳んだもの。
type handoffContext struct {
	Repo          string `json:"repo"`
	Dir           string `json:"dir"`
	WorkingCopyID string `json:"workingCopyId,omitempty"`
	// Vcs は git / svn / ""（作業コピーではない）。git 以外は push の概念が違うので
	// ゲートを掛けない（Blocked は空になる）。
	Vcs     string `json:"vcs"`
	Branch  string `json:"branch,omitempty"`
	Remote  string `json:"remote,omitempty"`
	HeadSha string `json:"headSha,omitempty"`
	Ahead   int    `json:"ahead"`
	Dirty   bool   `json:"dirty"`
	// Detached / NoUpstream は Ahead では表せない「push 済みか判定できない」状態。
	// ⚠️ upstream が無いブランチの Ahead は 0 になる（`# branch.ab` 行自体が出ない）ので、
	// ahead>0 だけを見るゲートは**一度も push していないブランチを素通しする**。
	Detached   bool `json:"detached,omitempty"`
	NoUpstream bool `json:"noUpstream,omitempty"`
	// Blocked は引き継ぎを止める理由（"" = 止めない）。Dirty は止めず Warning に載せる。
	Blocked string `json:"blocked,omitempty"`
	Warning string `json:"warning,omitempty"`
}

// handoffBlockUnpushed / handoffBlockNoUpstream / handoffBlockDetached / handoffWarnDirty は
// CP と Console が突き合わせる機械トークン（表示文言は Console 側の i18n）。
const (
	handoffBlockUnpushed   = "unpushed_commits"
	handoffBlockNoUpstream = "no_upstream"
	handoffBlockDetached   = "detached_head"
	handoffWarnDirty       = "uncommitted_changes"
)

// sanitizeRemoteURL は remote URL から資格情報を落とす。`https://x-access-token:ghp_…@host/…`
// のような URL がそのまま offer に載って別メンバーへ渡るのを防ぐ。SSH 形式は既存の
// sshToHTTPS で HTTPS へ寄せてから同じ処理に通す（比較にも表示にも同じ形が要る）。
func sanitizeRemoteURL(raw string) string {
	s := gitx.SSHToHTTPS(strings.TrimSpace(raw))
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		// パースできない形（ローカルパス等）は host を持たない。資格情報を含み得ないので
		// そのまま返すが、`@` を含むなら安全側に倒して落とす。
		if strings.Contains(s, "@") {
			return ""
		}
		return s
	}
	u.User = nil
	return u.String()
}

// gitHeadSha は HEAD の commit id。取れなければ空（履歴の無い作業コピー）。
func gitHeadSha(dir string) string {
	out, err := gitx.Run(dir, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// gitHasUpstream は現在のブランチに upstream が設定されているか。
func gitHasUpstream(dir string) bool {
	_, err := gitx.Run(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	return err == nil
}

// buildHandoffContext は dir の状態を 1 つに畳む。dir が git 作業コピーでなければ
// Vcs を空（または svn）にして、ゲート判定を行わない。
func buildHandoffContext(dir string) handoffContext {
	c := handoffContext{Repo: filepath.Base(dir), Dir: dir, WorkingCopyID: gitx.WorkingCopyID(dir)}
	switch {
	case gitx.IsGitRepo(dir):
		c.Vcs = "git"
	case isSvnRepo(dir):
		c.Vcs = "svn"
		return c
	default:
		return c
	}
	st, err := gitx.GitStatus(dir)
	if err != nil {
		// git が答えられないなら「引き継げる」と言ってはいけない。判定不能は止める側へ倒す。
		c.Blocked = handoffBlockNoUpstream
		return c
	}
	c.Branch, c.Dirty, c.Ahead, c.Detached = st.Branch, st.Dirty, st.Ahead, st.Detached
	c.HeadSha = gitHeadSha(dir)
	if origin, ok := gitx.GitOriginURL(dir); ok {
		c.Remote = sanitizeRemoteURL(origin)
	}
	c.NoUpstream = !c.Detached && !gitHasUpstream(dir)
	switch {
	case c.Detached:
		c.Blocked = handoffBlockDetached
	case c.NoUpstream:
		c.Blocked = handoffBlockNoUpstream
	case c.Ahead > 0:
		c.Blocked = handoffBlockUnpushed
	}
	if c.Dirty {
		c.Warning = handoffWarnDirty
	}
	return c
}

// HandleSessionHandoffContext — GET /sessions/{name}/handoff-context.
func HandleSessionHandoffContext(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if strings.TrimSpace(m.Dir) == "" {
		httpx.WriteErr(w, http.StatusConflict, "no_working_copy", "session has no working copy")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, buildHandoffContext(m.Dir))
}
