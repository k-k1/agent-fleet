package opencode

// opencode.ai の workspace ID（`wrk_…`）と、上限に当たったときの枠情報。
//
// なぜ持つのか（docs/log/54 §54.7）: 利用枠の画面
// `https://opencode.ai/workspace/{wrk}/go` はブラウザセッション前提で、素の GET は
// `/auth/authorize` へ 302 する。JSON API も opencode.ai 側には無く（api/* は 404）、
// Console 側 API は Bearer で開いているが（/api/orgs と /api/user が 401＝経路は存在、
// /api/usage は 404）、そのトークンは opencode 自身の資格情報ストアにあり読み出す口が
// 無い。したがって**数値を取り込むことはできない**。できるのは
//   (1) ID を持って利用枠ページへの導線を出すこと
//   (2) 上限に当たったときにエラーが運んでくる枠情報（どの枠か・いつ戻るか）を見せること
// の 2 つで、ID はどちらにも要る。手入力と、エラーからの自動学習の両方で埋める。
//
// 実測の材料:
//   - 残高切れ: message に `…Manage your billing here: https://opencode.ai/workspace/wrk_x/billing`
//     （errors_test.go の固定データ）。
//   - Go の上限: opencode 本体は responseBody を JSON として読み、`metadata.workspace` と
//     `metadata.limitName` を取り、`retry-after` ヘッダからリセットまでの秒数を出して
//     `https://opencode.ai/workspace/{workspace}/go` を案内する（バイナリ実測）。
//     保存されたメッセージにその全部が載るかは版によるので、**どれか一つでも拾えたら
//     使う**という読み方にしてある。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// workspaceIDRe matches the ULID-shaped id（Crockford base32・実測 26 文字）。
var workspaceIDRe = regexp.MustCompile(`\bwrk_[0-9A-HJKMNP-TV-Za-hjkmnp-tv-z]{26}\b`)

// NormalizeWorkspaceID extracts the id out of whatever the user pasted. 利用枠ページの
// URL をそのまま貼るのが自然な操作なので（実機でそうなった）、`wrk_…` だけを取り出す。
// 見つからなければ空 — 呼び出し側が入力エラーとして扱う。
func NormalizeWorkspaceID(s string) string { return workspaceIDRe.FindString(strings.TrimSpace(s)) }

// ValidWorkspaceID reports whether s CONTAINS an opencode workspace id（URL 可）。
func ValidWorkspaceID(s string) bool { return NormalizeWorkspaceID(s) != "" }

type workspaceState struct {
	ID string `json:"id"`
	// Source は "manual"（利用者が入力）か "learned"（エラーから拾った）。手入力を
	// 学習で黙って上書きしないための区別。
	Source string `json:"source"`
	At     string `json:"at"`
}

var (
	wsIDMu    sync.Mutex
	wsIDCache *workspaceState
)

func workspaceIDPath() string {
	return filepath.Join(paths.AgentDataDir(), "opencode-workspace.json")
}

// WorkspaceID returns the stored id ("" when unknown) and where it came from.
func WorkspaceID() (string, string) {
	wsIDMu.Lock()
	defer wsIDMu.Unlock()
	st := loadWorkspaceIDLocked()
	return st.ID, st.Source
}

func loadWorkspaceIDLocked() workspaceState {
	if wsIDCache != nil {
		return *wsIDCache
	}
	var st workspaceState
	if b, err := os.ReadFile(workspaceIDPath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	// 正規化前に書かれたファイル（URL 丸ごと）はここで直す — 読むたびに正しい id へ。
	if id := NormalizeWorkspaceID(st.ID); id != st.ID {
		st.ID = id
	}
	if st.ID == "" {
		st = workspaceState{}
	}
	wsIDCache = &st
	return st
}

func saveWorkspaceIDLocked(st workspaceState) error {
	if err := os.MkdirAll(filepath.Dir(workspaceIDPath()), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.WriteFile(workspaceIDPath(), b, 0o600); err != nil {
		return err
	}
	wsIDCache = &st
	return nil
}

// SetWorkspaceID records a manually entered id; an empty value clears it.
func SetWorkspaceID(id string) error {
	id = strings.TrimSpace(id)
	wsIDMu.Lock()
	defer wsIDMu.Unlock()
	if id == "" {
		wsIDCache = &workspaceState{}
		return os.Remove(workspaceIDPath())
	}
	return saveWorkspaceIDLocked(workspaceState{ID: NormalizeWorkspaceID(id), Source: "manual", At: nowRFC3339()})
}

// learnWorkspaceID records an id seen in a failure. 手入力は上書きしない — 利用者が
// 選んだ workspace のほうが、たまたまエラーに出た id より意図に近い。
func learnWorkspaceID(id string) {
	if !ValidWorkspaceID(id) {
		return
	}
	wsIDMu.Lock()
	defer wsIDMu.Unlock()
	id = NormalizeWorkspaceID(id)
	st := loadWorkspaceIDLocked()
	if st.Source == "manual" || st.ID == id {
		return
	}
	_ = saveWorkspaceIDLocked(workspaceState{ID: id, Source: "learned", At: nowRFC3339()})
}

var nowRFC3339 = func() string { return time.Now().Format(time.RFC3339) }

// --- 上限に当たったときの枠情報 ------------------------------------------------

// LimitInfo is what a usage-limit failure tells us about the plan window.
type LimitInfo struct {
	Name    string `json:"name,omitempty"`     // "rolling" / "weekly" / "monthly" など
	ResetAt string `json:"reset_at,omitempty"` // RFC3339。retry-after から算出
}

// limitPayload は opencode 本体が読むのと同じ形（responseBody を JSON として読む）。
type limitPayload struct {
	Metadata struct {
		Workspace string `json:"workspace"`
		LimitName string `json:"limitName"`
	} `json:"metadata"`
}

// lastLimit is the most recent usage-limit observation, for the Console card.
var (
	limitMu   sync.Mutex
	lastLimit LimitInfo
)

// LastLimit returns the most recent usage-limit observation (zero value = 未観測)。
func LastLimit() LimitInfo {
	limitMu.Lock()
	defer limitMu.Unlock()
	return lastLimit
}

// scanFailure harvests what a failed turn can tell us: the workspace id（文面にも
// メタデータにも出る）and, for a usage-limit failure, which window it was and when it
// resets. 拾えなかった項目は空のまま返す。
func scanFailure(e messageError) LimitInfo {
	learnWorkspaceID(string(workspaceIDRe.Find([]byte(e.Data.Message))))
	learnWorkspaceID(e.Data.Metadata.Workspace)

	info := LimitInfo{Name: strings.TrimSpace(e.Data.Metadata.LimitName)}
	// opencode 本体は responseBody（プロバイダ応答の生文字列）を JSON として読み直す。
	if body := e.Data.ResponseBody; body != "" {
		learnWorkspaceID(string(workspaceIDRe.Find([]byte(body))))
		var p limitPayload
		if json.Unmarshal([]byte(body), &p) == nil {
			learnWorkspaceID(p.Metadata.Workspace)
			if info.Name == "" {
				info.Name = strings.TrimSpace(p.Metadata.LimitName)
			}
		}
	}
	info.ResetAt = resetAt(headerValue(e.Data.ResponseHeaders, "retry-after"))
	if info.Name != "" || info.ResetAt != "" {
		limitMu.Lock()
		lastLimit = info
		limitMu.Unlock()
	}
	return info
}

// headerValue looks a header up case-insensitively（応答ヘッダの正規化は版で揺れる）。
func headerValue(h map[string]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// resetAt turns a retry-after value (seconds, or an HTTP date) into an absolute time.
func resetAt(retryAfter string) string {
	v := strings.TrimSpace(retryAfter)
	if v == "" {
		return ""
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
		return time.Now().Add(time.Duration(secs * float64(time.Second))).Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC1123, v); err == nil {
		return t.Format(time.RFC3339)
	}
	return ""
}

// WorkspaceURL builds the deep link for the Go plan page（利用枠の画面）。空 id では空。
func WorkspaceURL(id, page string) string {
	norm := NormalizeWorkspaceID(id)
	if norm == "" {
		return ""
	}
	if page == "" {
		page = "go"
	}
	return "https://opencode.ai/workspace/" + norm + "/" + page
}

// --- HTTP ---------------------------------------------------------------------

type workspaceReq struct {
	ID string `json:"id"` // "" = 登録解除
}

// HandlePutWorkspace records the workspace id the user pasted from their browser
// (PUT /connections/opencode/workspace). ID is not a secret — it is the path segment
// of the billing page URL — so it lives in the Agent's own data dir, not the sealed
// store.
func HandlePutWorkspace(w http.ResponseWriter, r *http.Request) {
	var req workspaceReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if id != "" && !ValidWorkspaceID(id) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_workspace_id",
			"workspace ID は wrk_ で始まる 30 文字です（利用枠ページの URL から取れます）")
		return
	}
	if err := SetWorkspaceID(id); err != nil && !os.IsNotExist(err) {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	id = NormalizeWorkspaceID(id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"workspace_id": id, "workspace_url": WorkspaceURL(id, "go")})
}
