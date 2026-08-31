package mcpreg

// テナント配布（docs/log/48 P4）の Workspace 側。ここで固定したいのは 3 点:
//
//   - **配布された定義でコマンドは動かない**。ADR0031 決定 2 の 3 段目の砦で、
//     コマンドを実行する当のマシン上で走る唯一の検査。
//   - **fail-open**。CP が落ちていてもキャッシュは残る。
//   - **user_secret はメンバー自身の値で埋まる**（テナントは名前だけを配る）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func tenantDef(name string) ServerDef {
	return ServerDef{
		ID: "t-" + name, Name: name, Origin: OriginTenant, Transport: TransportHTTP,
		URL: "https://" + name + ".corp.example/mcp", Enabled: true,
		Targets: Targets{Assistant: true, Session: true},
	}
}

// serveTenant stands in for the CP's GET /internal/mcp-servers and points the env at it.
func serveTenant(t *testing.T, body any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_MCP_TOKEN", "tok")
}

func TestAcceptTenantDropsStdio(t *testing.T) {
	// The CP has no column to hold a command and its API refuses stdio — but this is the
	// check that runs where the command would actually execute, so it must not trust either.
	stdio := ServerDef{ID: "t1", Name: "evil", Transport: TransportStdio, Command: "/bin/sh"}
	kept, dropped := acceptTenant([]ServerDef{stdio, tenantDef("wiki")})
	if dropped != 1 || len(kept) != 1 || kept[0].Name != "wiki" {
		t.Fatalf("stdio が落ちていない: kept=%+v dropped=%d", kept, dropped)
	}
	// Even a "remote" definition that smuggles a command along is refused — Validate's
	// CodeHTTPNoCommand rule — so there is no shape that reaches materialize with a command.
	sneaky := tenantDef("sneaky")
	sneaky.Command = "/bin/sh"
	if kept, dropped := acceptTenant([]ServerDef{sneaky}); dropped != 1 || len(kept) != 0 {
		t.Fatalf("http にコマンドを紛れ込ませた定義が通った: kept=%+v dropped=%d", kept, dropped)
	}
}

func TestAcceptTenantForcesOriginAndEnabled(t *testing.T) {
	// A definition claiming to be a user row must not be able to bypass the "tenant rows
	// are read-only" rule by lying about its origin.
	d := tenantDef("wiki")
	d.Origin, d.Enabled = OriginUser, false
	kept, _ := acceptTenant([]ServerDef{d})
	if len(kept) != 1 || kept[0].Origin != OriginTenant || !kept[0].Enabled {
		t.Fatalf("origin/enabled が正規化されていない: %+v", kept)
	}
}

func TestFetchTenantWritesCacheAndDetectsChange(t *testing.T) {
	withTempHome(t)
	serveTenant(t, map[string]any{"servers": []ServerDef{tenantDef("wiki")}, "unreadable": 0})

	res, err := FetchTenant()
	if err != nil {
		t.Fatalf("FetchTenant: %v", err)
	}
	if !res.Changed || res.Servers != 1 {
		t.Fatalf("初回取得が変更として扱われていない: %+v", res)
	}
	if reg, err := Load(); err != nil || len(dropAF(reg.Servers)) != 1 || dropAF(reg.Servers)[0].Origin != OriginTenant {
		t.Fatalf("キャッシュがレジストリに現れない: %+v %v", reg, err)
	}

	// 2 回目は中身が同じ → changed=false。ここが true になると 5 分ごとに全 CLI の
	// 設定を書き直すことになり、claude 自身の .claude.json 書き込みと競合する（§8.2）。
	res2, err := FetchTenant()
	if err != nil {
		t.Fatalf("FetchTenant 2: %v", err)
	}
	if res2.Changed {
		t.Fatal("同一内容の再取得が変更扱いになっている")
	}
	// 取得時刻は前進する（Console の「最終取得」が古いままだと確認済みなのに stale に見える）。
	if res2.FetchedAt < res.FetchedAt {
		t.Fatalf("fetchedAt が巻き戻った: %d -> %d", res.FetchedAt, res2.FetchedAt)
	}
	if got := loadTenantCache().FetchedAt; got != res2.FetchedAt {
		t.Fatalf("無変更でも fetchedAt はキャッシュへ書かれるべき: %d != %d", got, res2.FetchedAt)
	}
}

func TestFetchTenantKeepsCacheWhenCPFails(t *testing.T) {
	// fail-open（§6）。ここを fail-closed にすると CP の瞬断で全メンバーのセッションから
	// MCP が消える。
	withTempHome(t)
	serveTenant(t, map[string]any{"servers": []ServerDef{tenantDef("wiki")}})
	if _, err := FetchTenant(); err != nil {
		t.Fatalf("FetchTenant: %v", err)
	}
	t.Setenv("AF_CP_BASE_URL", "http://127.0.0.1:1") // 到達不能
	if _, err := FetchTenant(); err == nil {
		t.Fatal("到達不能な CP がエラーにならない")
	}
	if reg, _ := Load(); len(dropAF(reg.Servers)) != 1 {
		t.Fatalf("失敗した取得がキャッシュを消した: %+v", dropAF(reg.Servers))
	}
}

func TestFetchTenantWithoutBridge(t *testing.T) {
	withTempHome(t)
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv("AF_MCP_TOKEN", "")
	if _, err := FetchTenant(); err != ErrTenantBridgeOff {
		t.Fatalf("err = %v, want ErrTenantBridgeOff", err)
	}
	// 未設定は正常な状態なので、キャッシュファイルも作らない。
	if _, err := os.Stat(tenantCachePath()); !os.IsNotExist(err) {
		t.Fatalf("ブリッジ未設定でキャッシュを作ってしまった: %v", err)
	}
}

func TestFetchTenantRejectsBadToken(t *testing.T) {
	withTempHome(t)
	serveTenant(t, map[string]any{"servers": []ServerDef{}})
	t.Setenv("AF_MCP_TOKEN", "wrong")
	if _, err := FetchTenant(); err == nil {
		t.Fatal("401 がエラーとして返っていない")
	}
}

// --- user_secret --------------------------------------------------------------------

func TestUserSecretIsHeldBackUntilFilled(t *testing.T) {
	withTempHome(t)
	d := tenantDef("tickets")
	d.UserSecret = true
	d.Headers = map[string]string{"Authorization": ""} // 名前だけが配られる
	serveTenant(t, map[string]any{"servers": []ServerDef{d}})
	if _, err := FetchTenant(); err != nil {
		t.Fatalf("FetchTenant: %v", err)
	}

	// 値が無いうちは materialize もアシスタント配線もされない（起動して失敗させるより出さない）。
	got, err := Get(d.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if Ready(got) {
		t.Fatal("値未入力の user_secret 定義が Ready になっている")
	}
	if defs, _ := ForSession("claude"); len(dropAF(defs)) != 0 {
		t.Fatalf("値未入力なのに materialize 対象になっている: %+v", dropAF(defs))
	}

	if err := SetTenantSecrets(d.ID, map[string]string{"Authorization": "Bearer mine"}); err != nil {
		t.Fatalf("SetTenantSecrets: %v", err)
	}
	got, err = Get(d.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Headers["Authorization"] != "Bearer mine" || !Ready(got) {
		t.Fatalf("メンバーの値が合成されていない: %+v", got.Headers)
	}
	if defs, _ := ForSession("claude"); len(dropAF(defs)) != 1 {
		t.Fatalf("値入力後に materialize 対象にならない: %+v", dropAF(defs))
	}

	// マスク往復: Console は保存済みを *** で送り返す。
	if err := SetTenantSecrets(d.ID, map[string]string{"Authorization": MaskedValue}); err != nil {
		t.Fatalf("SetTenantSecrets masked: %v", err)
	}
	if got, _ := Get(d.ID); got.Headers["Authorization"] != "Bearer mine" {
		t.Fatalf("マスク往復で値が失われた: %q", got.Headers["Authorization"])
	}
	// 空文字は「消す」。
	if err := SetTenantSecrets(d.ID, map[string]string{"Authorization": ""}); err != nil {
		t.Fatalf("SetTenantSecrets clear: %v", err)
	}
	if got, _ := Get(d.ID); Ready(got) {
		t.Fatal("値を消したのに Ready のまま")
	}
}

func TestSetTenantSecretsRefusals(t *testing.T) {
	withTempHome(t)
	plain := tenantDef("wiki") // user_secret ではない
	us := tenantDef("tickets")
	us.UserSecret = true
	us.Headers = map[string]string{"Authorization": ""}
	serveTenant(t, map[string]any{"servers": []ServerDef{plain, us}})
	if _, err := FetchTenant(); err != nil {
		t.Fatalf("FetchTenant: %v", err)
	}

	if err := SetTenantSecrets("nope", map[string]string{"A": "b"}); err != ErrNotFound {
		t.Fatalf("未知の id: err = %v, want ErrNotFound", err)
	}
	// 値込みで配られた定義の値をメンバーが上書きできてしまうと、テナントの意図した資格情報で
	// 繋がらなくなる。書けるのは user_secret のときだけ。
	if err := SetTenantSecrets(plain.ID, map[string]string{"Authorization": "mine"}); err != ErrReadOnly {
		t.Fatalf("user_secret でない定義: err = %v, want ErrReadOnly", err)
	}
	// テナントが配っていないヘッダは黙って捨てる（保存しても誰も読まない値になる）。
	if err := SetTenantSecrets(us.ID, map[string]string{"X-Unasked": "v"}); err != nil {
		t.Fatalf("SetTenantSecrets: %v", err)
	}
	s, _ := secrets.Load()
	if _, ok := s.MCPSecrets[us.ID]; ok {
		t.Fatalf("配布外のヘッダを保存してしまった: %+v", s.MCPSecrets)
	}
	if err := SetTenantSecrets(us.ID, map[string]string{"Authorization": "a\nb"}); err == nil {
		t.Fatal("改行入りのヘッダ値が通った")
	}
}

func TestWithMemberSecretsIgnoresStaleNames(t *testing.T) {
	// テナントが要求しなくなったヘッダのローカル値は送らない — どのヘッダを送るかは
	// あくまでテナントが決める。
	got := withMemberSecrets(
		map[string]string{"Authorization": ""},
		map[string]string{"Authorization": "mine", "X-Old": "stale"},
	)
	if got["Authorization"] != "mine" {
		t.Fatalf("自分の値が入っていない: %+v", got)
	}
	if _, ok := got["X-Old"]; ok {
		t.Fatalf("配布外のヘッダが送られる: %+v", got)
	}
}

func TestMaskedKeepsUnsetValuesVisible(t *testing.T) {
	// 未入力（""）は「秘密を隠している」のではなく「誰も入れていない」。ここを *** に
	// すると、値を入れるべきなのが自分だと Console から分からなくなる。
	m := Masked(ServerDef{Headers: map[string]string{"Authorization": "", "X-Team": "sre"}})
	if m.Headers["Authorization"] != "" || m.Headers["X-Team"] != MaskedValue {
		t.Fatalf("マスクが不正: %+v", m.Headers)
	}
}
