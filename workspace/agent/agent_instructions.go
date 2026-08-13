package main

// ユーザー指示の配布器（docs/60 / ADR 0042）。
//
// 3 層のうち真ん中 —「その人の働き方」— を 1 本書けば、対応する全 kind のセッションに
// 効くようにする。正本は internal/userinstr（~/.config/agent-fleet/user-notes.md）で、
// ここはそれを各 CLI の user スコープへ**配る**役。rtk トグルと同じ型
// （durable な設定 → 起動時 reconcile ＋ Console から live 適用）を踏襲する。
//
// 配り方の原則（docs/60 §60.5-6）: **「他人のファイルに書く」より「AF 専用ファイル＋
// 参照」を優先する。** 実測の結果 claude / opencode / copilot は参照で足り、合成が要る
// のは追加指示ファイルを指す手段が無い codex だけになった（agy は P1）。
//
// フリート方針（イメージの workspace-notes.md）の配置もここへ移した。以前は
// entrypoint が毎起動 `cp -f` で AGENTS.md を丸ごと上書きしており、利用者がそのファイルへ
// 書き足した文章は次の起動で黙って消えていた（docs/60 実害①）。いまは同じ 1 本の
// 書き手がマーカー付きで合成するので、マーカー外は残る。
//
// ⚠️ 順序: ファイル内の並びは適用順そのまま（fleet → user-notes → rtk）。だから
// reconcile は必ずこの順で呼ぶ。

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mdblock"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/userinstr"
)

// instrMu serializes every write into a CLI's instruction artifacts. rtk shares it:
// codex's AGENTS.md carries both the rtk block and the user block, and two
// independent read-modify-writes would lose one side (docs/60 §60.7).
var instrMu sync.Mutex

// instrErrs holds the last apply error per kind, so the Console can say "書いたが
// 効いていない" instead of showing a green row for a write that failed.
var instrErrs = map[string]string{}

// instrDelivery は配り方の種類。Console はこれで文言を出し分ける。
const (
	deliveryFile    = "file"    // AF が単独所有するファイルを置く
	deliveryCompose = "compose" // 他人と共有するファイルへマーカー合成する
	deliveryConfig  = "config"  // AF 専用ファイル＋CLI 設定からの参照
)

// instrTarget は 1 kind ぶんの配り先。未対応の kind も**行として出す**（黙って消すと
// 「対応漏れ」に見え、同じ質問が繰り返される — docs/57 §2 の作法）。
type instrTarget struct {
	Kind      string `json:"kind"`
	Supported bool   `json:"supported"`
	// Reason は未対応の理由コード（Console が err/reason カタログで訳す）。
	Reason   string `json:"reason,omitempty"`
	Delivery string `json:"delivery,omitempty"`
	Path     string `json:"path,omitempty"`
	// On は利用者の選択（未対応 kind では常に false）。
	On bool `json:"on"`
	// Applied は「いま実際にそうなっているか」。書いた≠効いている を分けるための実測値。
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}

// instrSupportedKinds は本文を配れる kind（P0）。順序は Console の表示順。
var instrSupportedKinds = []string{"claude", "codex", "opencode", "copilot"}

// instrUnsupported は配れない kind と理由（docs/60 §60.3 の実測結論）。
var instrUnsupported = []struct{ kind, reason string }{
	// ローカルにユーザー層が存在しない。User Rules は Cursor アカウント側
	// （aiserver.v1.UserRules）で、ローカルの rules 収集は全てプロジェクト基準。
	{"cursor", "no_user_scope"},
	{"agy", "not_wired"},   // 挿し口は判明済み（~/.gemini/AGENTS.md）。P1 で配線する
	{"kiro", "unverified"}, // global steering の有無が未実測（docs/60 §60.15-1）
}

// instrOpencodeFile is the AF-owned body opencode's config points at. It lives under
// ~/.config/agent-fleet so the only writer is af (opencode just reads it).
func instrOpencodeFile() string {
	return filepath.Join(paths.AgentConfigDir(), "instructions", "opencode.md")
}

// reconcileAgentInstructions applies the durable state to every artifact. Called at
// startup (after the entrypoint) and on every Console save.
func reconcileAgentInstructions() {
	instrMu.Lock()
	defer instrMu.Unlock()
	applyInstructionsLocked()
}

func applyInstructionsLocked() {
	st := userinstr.Load()
	fleet := userinstr.FleetNotes()
	errs := map[string]string{}
	note := func(kind string, err error) {
		if err != nil {
			errs[kind] = errCode(err)
		}
	}

	// ① フリート方針（配れる kind のみ。順序の都合でユーザー指示より先）。
	note("codex", codex.ApplyFleetNotes(fleet))
	note("opencode", opencode.ApplyFleetNotes(fleet))

	// ② ユーザー指示。
	note("claude", claude.ApplyUserInstructions(st.Body("claude")))
	note("codex", codex.ApplyUserInstructions(st.Body("codex")))
	note("opencode", opencode.ApplyUserInstructions(instrOpencodeFile(), st.Body("opencode")))
	note("copilot", copilot.ApplyUserInstructions(st.Body("copilot")))

	// ③ rtk は常に最後（ファイル内でも最後に来る）。
	applyRTKLocked()

	instrErrs = errs
}

func errCode(err error) string {
	if errors.Is(err, opencode.ErrUnreadableConfig) {
		return "config_unreadable"
	}
	return "write_failed"
}

// instrState builds the REST snapshot, checking each artifact on disk so "applied"
// is measured rather than assumed.
func instrState() map[string]any {
	st := userinstr.Load()
	targets := make([]instrTarget, 0, len(instrSupportedKinds)+len(instrUnsupported))
	for _, kind := range instrSupportedKinds {
		t := instrTarget{Kind: kind, Supported: true, On: st.TargetOn(kind), Error: instrErrs[kind]}
		want := st.Body(kind) != ""
		switch kind {
		case "claude":
			t.Delivery, t.Path = deliveryFile, claude.UserInstructionsPath()
			t.Applied = fileHasBlock(t.Path, "user-notes") == want
		case "codex":
			t.Delivery, t.Path = deliveryCompose, codex.AgentsPath()
			t.Applied = fileHasBlock(t.Path, "user-notes") == want
		case "opencode":
			t.Delivery, t.Path = deliveryConfig, instrOpencodeFile()
			t.Applied = fileExists(t.Path) == want && opencodeRefers(t.Path) == want
		case "copilot":
			t.Delivery, t.Path = deliveryFile, copilot.UserInstructionsPath()
			t.Applied = fileExists(t.Path) == want
		}
		targets = append(targets, t)
	}
	for _, u := range instrUnsupported {
		targets = append(targets, instrTarget{Kind: u.kind, Reason: u.reason})
	}
	return map[string]any{
		"text":      st.Text,
		"bytes":     len(st.Text),
		"max_bytes": userinstr.MaxBytes,
		"enabled":   st.Enabled(),
		"path":      userinstr.NotesPath(),
		"targets":   targets,
		// フリート方針を read-only で覗く導線（なぜ上書きできないかが画面で分かる）。
		"fleet_bytes": len(userinstr.FleetNotes()),
	}
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

func fileHasBlock(path, name string) bool {
	b, err := os.ReadFile(path)
	return err == nil && mdblock.Has(string(b), name)
}

func opencodeRefers(instrPath string) bool {
	b, err := os.ReadFile(opencode.ConfigPath())
	if err != nil {
		return false
	}
	var root struct {
		Instructions []string `json:"instructions"`
	}
	if json.Unmarshal(b, &root) != nil {
		return false
	}
	for _, p := range root.Instructions {
		if p == instrPath {
			return true
		}
	}
	return false
}

func handleUserNotesGet(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, instrState())
}

type userNotesReq struct {
	Text    *string          `json:"text"`
	Enabled *bool            `json:"enabled"`
	Targets map[string]*bool `json:"targets"`
}

func handleUserNotesPut(w http.ResponseWriter, r *http.Request) {
	var req userNotesReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	instrMu.Lock()
	defer instrMu.Unlock()

	if req.Text != nil {
		if err := userinstr.SaveText(*req.Text); err != nil {
			if errors.Is(err, userinstr.ErrTooLarge) {
				httpx.WriteErr(w, http.StatusBadRequest, "too_large", "user instructions exceed the size limit")
				return
			}
			httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
	}
	if req.Enabled != nil || req.Targets != nil {
		p := userinstr.Load().Prefs
		if req.Enabled != nil {
			p.Enabled = req.Enabled
		}
		for kind, on := range req.Targets {
			if p.Targets == nil {
				p.Targets = map[string]*bool{}
			}
			p.Targets[kind] = on
		}
		if err := userinstr.SavePrefs(p); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
	}
	applyInstructionsLocked()
	httpx.WriteJSON(w, http.StatusOK, instrState())
}

// handleUserNotesPreview shows what a kind will actually read — the composed file for
// codex, the referenced file for the others. 「書いた」と「効いている」を分けるための
// 最後の一段で、利用者が自分の目で確かめられるようにする。
func handleUserNotesPreview(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	var path string
	switch kind {
	case "claude":
		path = claude.UserInstructionsPath()
	case "codex":
		path = codex.AgentsPath()
	case "opencode":
		path = instrOpencodeFile()
	case "copilot":
		path = copilot.UserInstructionsPath()
	case "fleet":
		path = paths.FleetNotesPath()
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "unknown_kind", "no instruction target for this kind")
		return
	}
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		httpx.WriteErr(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"kind": kind, "path": path, "exists": err == nil, "content": string(body),
	})
}
