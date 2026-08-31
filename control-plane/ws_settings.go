package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Per-workspace member settings owned by the Control Plane. Unlike the toolchain
// selection (which the in-container Agent reads/writes), these live in the CP DB, so
// they can be edited while the container is STOPPED and are applied at the next
// container start by mapping them to env (see manager.workspaceExtraEnv). This is the
// home for "apply on next start" preferences; add a field + an env mapping as they
// grow — the generic GET/PUT below carries any known key.
type wsSettings struct {
	// AgentUpdate: opt in to updating the baked CLIs (claude/opencode/codex) to
	// latest at container start. Operator-gated by the tenant's allow_agent_self_update
	// (workspaceExtraEnv only emits AF_AGENT_SELF_UPDATE=1 when the tenant allows it).
	AgentUpdate bool `json:"agentUpdate"`

	// --- プレビュー用サブドメイン（docs/81） --------------------------------
	// PreviewPorts はホスト方式のプレビューで外に出してよいポート（空 = 既定の
	// 3000,8080）。列挙に無いポートのサブドメインは 404 —— 「許可されていない」では
	// なく存在も答えない（許可ポートの有無を外から探らせない・ADR 0062 決定 6）。
	PreviewPorts []int `json:"previewPorts,omitempty"`
	// PreviewFixedSlug: slug を起動ごとに引き直さず、この Workspace では固定する
	// （docs/81 §4.1）。既定 false ＝ 要件どおり毎回引き直す。ON にする理由は実質
	// 1 つで、外部 IdP の redirect URI 登録（NextAuth / Auth.js）が前方一致も
	// ワイルドカードも受け付けないこと。
	PreviewFixedSlug bool `json:"previewFixedSlug,omitempty"`
	// PreviewPublic: 認証なしで開ける（docs/81 §6.1）。★ 起動のたびに false へ
	// 戻す（fail-closed）—— この機能の事故は「公開のままにしていたのを忘れる」以外に
	// ほぼ無いので、忘れても閉じる側に倒す。
	PreviewPublic bool `json:"previewPublic,omitempty"`
	// PreviewReservedSlug は PreviewFixedSlug が ON のときだけ使う予約。★ 起動中の
	// slug（workspace.preview_slug 列）とは別物である必要がある —— 列の方は停止で
	// 必ず空に戻る（停止中の URL は解決しない）ので、そこに予約を兼ねさせると
	// 「固定したのに再起動で変わった」になる。
	PreviewReservedSlug string `json:"previewReservedSlug,omitempty"`
}

// parseWSSettings unmarshals the stored JSON blob ("" => zero value).
func parseWSSettings(s string) wsSettings {
	var w wsSettings
	if s != "" {
		_ = json.Unmarshal([]byte(s), &w)
	}
	return w
}

// wsSettingsAPI は CP 管理のワークスペース設定の機能ハンドラ集（docs/23 残③）。
// 解決は埋め込みの memberAuth（登録側で withResolved に包む）。store は
// WorkspaceStore、tenants はオペレータゲート参照用 TenantStore の narrow view。
// キャッシュ破棄（evictMembershipCache）だけは memberAuth 経由の a.mgr を直接呼ぶ。
type wsSettingsAPI struct {
	memberAuth
	store   WorkspaceStore
	tenants TenantStore
}

func newWSSettingsAPI(m *manager) wsSettingsAPI {
	return wsSettingsAPI{memberAuth{m}, m.store, m.store}
}

// tenantAllowsAgentUpdate reports the operator gate for a workspace's tenant.
func (a wsSettingsAPI) tenantAllowsAgentUpdate(r *http.Request, ws Workspace) bool {
	t, err := a.tenants.GetTenant(r.Context(), ws.TenantID)
	if err != nil {
		return false
	}
	return parseLimits(t.Limits).AllowAgentSelfUpdate
}

// get (GET /api/env/ws-settings) returns the workspace's CP-owned
// settings plus the relevant operator gates. DB-backed, so it works whether the
// container is running or stopped.
func (a wsSettingsAPI) get(w http.ResponseWriter, r *http.Request, res *resolved) {
	raw, _ := a.store.GetWorkspaceSettings(r.Context(), res.ws.ID)
	st := parseWSSettings(raw)
	out := map[string]any{
		"agentUpdate":      st.AgentUpdate,
		"allowAgentUpdate": a.tenantAllowsAgentUpdate(r, res.ws),
	}
	a.addPreview(r, res, st, out)
	writeJSON(w, http.StatusOK, out)
}

// addPreview appends the preview-subdomain block (docs/81). The issued URLs are read
// from the STORE rather than res.ws: the resolved workspace comes from the runtime
// cache, which was built before this container start rotated the slug, so trusting it
// would show the previous start's (now dead) URLs.
func (a wsSettingsAPI) addPreview(r *http.Request, res *resolved, st wsSettings, out map[string]any) {
	domain := a.mgr.previewDomain
	out["previewDomain"] = domain
	out["previewPorts"] = previewPortsOf(st)
	out["previewFixedSlug"] = st.PreviewFixedSlug
	out["previewPublic"] = st.PreviewPublic
	out["previewMaxPorts"] = maxPreviewPorts
	urls := map[string]string{}
	if domain != "" {
		if ws, ok, err := a.store.GetWorkspaceByMembership(r.Context(), res.ws.MembershipID); err == nil && ok && ws.PreviewSlug != "" {
			for _, p := range previewPortsOf(st) {
				urls[strconv.Itoa(p)] = previewURLFor(ws.PreviewSlug, p, domain)
			}
		}
	}
	// 停止中は空のまま返す。★ 発行されていない URL を見せない —— 押しても 404 になる
	// リンクは、機能が壊れているという報告になって返ってくる。
	out["previewUrls"] = urls
}

// put (PUT /api/env/ws-settings) merges the posted known keys into the
// workspace's stored JSON. Works while the container is stopped; the value takes
// effect at the next container start. Only known keys are honored, and agentUpdate is
// gated by the tenant policy (a member can't enable it when the operator forbids it).
func (a wsSettingsAPI) put(w http.ResponseWriter, r *http.Request, res *resolved) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	raw, _ := a.store.GetWorkspaceSettings(r.Context(), res.ws.ID)
	st := parseWSSettings(raw)
	if v, ok := body["agentUpdate"].(bool); ok {
		st.AgentUpdate = v && a.tenantAllowsAgentUpdate(r, res.ws)
	}
	// プレビュー（docs/81）。ホスト方式が無いデプロイ（AF_PREVIEW_DOMAIN 未設定）でも
	// 値は保存する —— 設定が「デプロイの都合で黙って消える」より、効かないだけの方が
	// 説明できる。
	if raw, ok := body["previewPorts"].([]any); ok {
		ports := make([]int, 0, len(raw))
		for _, v := range raw {
			if f, ok := v.(float64); ok {
				ports = append(ports, int(f))
			}
		}
		st.PreviewPorts = sanitizePreviewPorts(ports)
	}
	if v, ok := body["previewFixedSlug"].(bool); ok {
		st.PreviewFixedSlug = v
	}
	if v, ok := body["previewPublic"].(bool); ok {
		st.PreviewPublic = v
		auditPreviewPublic(r.Context(), a.mgr, res, v)
	}
	out, _ := json.Marshal(st)
	if err := a.store.SetWorkspaceSettings(r.Context(), res.ws.ID, string(out)); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	// Drop the cached runtime so the next start rebuilds its env from the new setting.
	a.mgr.evictMembershipCache(res.ws.MembershipID)
	body2 := map[string]any{
		"agentUpdate":      st.AgentUpdate,
		"allowAgentUpdate": a.tenantAllowsAgentUpdate(r, res.ws),
	}
	a.addPreview(r, res, st, body2)
	writeJSON(w, http.StatusOK, body2)
}
