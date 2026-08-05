package opencode

import (
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// opencode provider auth: mirrors the claude "settings-driven" model — the user
// pastes a provider API key in the Console, it's kept in the encrypted store
// (internal/secrets, at-rest sealed), and the Agent injects it as the provider's env var
// when it launches an opencode session. opencode natively reads provider keys from
// the environment (ANTHROPIC_API_KEY, OPENAI_API_KEY, …), so no auth.json is written
// and the key never lands in a plaintext file on the bind-mounted disk.

// envNameRe constrains the env var name to the conventional ALL_CAPS form so an
// arbitrary value can't be smuggled into the container environment.
var envNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// opencodeKeyEnv is the one env var that pays opencode.ai（Zen も Go もこれ一本）。
const opencodeKeyEnv = "OPENCODE_API_KEY"

// UsagePref reports the selected billing route（ui-prefs opencodeCatalog）。Agent 本体が
// ui-prefs を読むので、その読み手を注入してもらう（internal パッケージから main の
// 設定ファイルを触らないため）。未設定なら UsageZen＝従来の見え方。
var UsagePref = func() string { return UsageZen }

// env loads the stored provider keys as "NAME=value" entries for the
// session launcher to pass via `docker`/tmux `-e`. Order is stable (sorted).
//
// 無料枠（UsageFree）では OPENCODE_API_KEY を落とす — 「無料枠で使う」と決めた
// ワークスペースが、鍵が残っているというだけで課金経路に乗ってしまわないように。
// 他プロバイダの鍵（ANTHROPIC_API_KEY など）は利用者自身の課金なので触らない。
func env() []string {
	s, err := secrets.Load()
	if err != nil || len(s.Opencode) == 0 {
		return nil
	}
	free := UsagePref() == UsageFree
	names := make([]string, 0, len(s.Opencode))
	for k := range s.Opencode {
		if free && k == opencodeKeyEnv {
			continue
		}
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, k := range names {
		out = append(out, k+"="+s.Opencode[k])
	}
	return out
}

// Env is the exported form of env for the assistant chat's headless `opencode run`,
// which needs the same provider keys the interactive launcher injects.
func Env() []string { return env() }

// Available reports whether opencode can run a headless turn at all. Unlike
// claude/codex (whose CLIs hard-fail without a login), opencode always works when
// installed — with stored provider keys, its own login, or its zero-auth free tier
// (verified live: a fresh data dir answers via the free model). So availability is
// simply "the binary is on PATH"; it sits LAST in the preferred-backend order, so a
// logged-in claude/codex still wins for defaults.
func Available() bool {
	_, err := exec.LookPath("opencode")
	return err == nil
}

// Status reports which provider env vars are configured (names only,
// never the keys) for the Console Connections panel (GET /connections), plus the
// state of the second, independent path: the opencode Console account
// （OAuth device flow — oauth.go）。connected は「opencode を認証済みで使えるか」
// なので、どちらの経路でも真になる（registry.ts の kind ゲートがこれを見る）。
func Status(s *secrets.Data) map[string]any {
	names := []string{}
	for k := range s.Opencode {
		names = append(names, k)
	}
	sort.Strings(names)
	oa := oauthStatus()
	usage := UsagePref()
	m := map[string]any{
		// connected は「この kind を起動できるか」の判定材料（registry.ts）。無料枠は
		// 認証ゼロで実際に動く（実測: 未接続のまま free モデルが応答）ので、枠として
		// 選ばれていれば接続扱いにする。
		"connected":      usage == UsageFree || len(names) > 0 || oa.connected,
		"envs":           names,
		"usage":          usage,
		"supported":      Available(), // バイナリ不在（旧イメージ）なら無料枠でも起動できない
		"oauth":          oa.connected,
		"oauth_known":    oa.known, // false = daemon 未起動で未確認（未接続とは限らない）
		"oauth_disabled": Serve().Disabled(),
	}
	if oa.label != "" {
		m["oauth_label"] = oa.label // Console org 名（実測の label 解決）
	}
	return m
}

type connReq struct {
	Env string `json:"env"` // provider env var name, e.g. ANTHROPIC_API_KEY
	Key string `json:"key"` // the API key
}

// HandlePutConn stores a provider API key under its env var name
// (PUT /connections/opencode).
func HandlePutConn(w http.ResponseWriter, r *http.Request) {
	var req connReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	env := strings.TrimSpace(req.Env)
	if !envNameRe.MatchString(env) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_env", "env must be ALL_CAPS like ANTHROPIC_API_KEY")
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_key", "key is required")
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	if s.Opencode == nil {
		s.Opencode = map[string]string{}
	}
	s.Opencode[env] = key
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	applyKeyChange("provider key stored: " + env)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true, "env": env})
}

// applyKeyChange propagates a stored-key change to the places that cached it.
//
// 鍵は起動時に env として注入されるので（docs/27 §7）、**保存しただけでは動いている
// serve daemon には効かない**。実測: Console でキーを消しても daemon は自分の環境に
// 持ったままで、connections[] に env 接続を出し続け、そのキーで課金され得るモデルも
// 一覧に残る（Agent を再起動しても Ensure は生きている daemon を adopt するので直らない）。
// 反映パスは generation++ ＋ drain ＝ Supervisor.Restart。drain は最大60秒かかるので
// ハンドラは待たず、別ゴルーチンに委ねる。
func applyKeyChange(reason string) {
	InvalidateModels()
	go restartServe("opencode " + reason)
}

// restartServe is the seam tests replace (a real Restart drains live turns).
var restartServe = func(reason string) { Serve().Restart(reason) }

// ApplyUsageChange is applyKeyChange for a billing-route switch: 無料枠に入る/出ると
// 注入する OPENCODE_API_KEY の有無が変わるので、鍵の変更と同じ反映が要る。
func ApplyUsageChange(reason string) { applyKeyChange("usage changed: " + reason) }

// HandleDeleteConn removes a stored provider key
// (DELETE /connections/opencode/{env}).
func HandleDeleteConn(w http.ResponseWriter, r *http.Request) {
	env := r.PathValue("env")
	if !envNameRe.MatchString(env) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_env", "invalid env name")
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	delete(s.Opencode, env)
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	applyKeyChange("provider key removed: " + env)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": env})
}
