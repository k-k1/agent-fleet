package mcpreg

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func stdioDef(name string) ServerDef {
	return ServerDef{Name: name, Origin: OriginUser, Transport: TransportStdio, Command: "/bin/true"}
}

func httpDef(name, url string) ServerDef {
	return ServerDef{Name: name, Origin: OriginUser, Transport: TransportHTTP, URL: url}
}

func TestValidateAccepts(t *testing.T) {
	for _, d := range []ServerDef{
		stdioDef("wiki"),
		httpDef("tickets", "https://mcp.example.com/mcp"),
		{Name: "a", Origin: OriginUser, Transport: TransportStdio, Command: "npx",
			Args: []string{"-y", "srv"}, Env: map[string]string{"API_KEY": "x"},
			Kinds: []string{session.KindClaude, session.KindCodex}, TimeoutMS: 5000},
	} {
		if err := Validate(d); err != nil {
			t.Fatalf("Validate(%q) = %v, want nil", d.Name, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]ServerDef{
		"空の名前":          {Origin: OriginUser, Transport: TransportStdio, Command: "x"},
		"記号入りの名前":       {Name: "my server", Origin: OriginUser, Transport: TransportStdio, Command: "x"},
		"先頭がハイフン":       {Name: "-srv", Origin: OriginUser, Transport: TransportStdio, Command: "x"},
		"予約名":           {Name: "af", Origin: OriginUser, Transport: TransportStdio, Command: "x"},
		"stdio でコマンド無し": {Name: "s", Origin: OriginUser, Transport: TransportStdio},
		"stdio に URL":   {Name: "s", Origin: OriginUser, Transport: TransportStdio, Command: "x", URL: "https://e.com"},
		"http で URL 無し": {Name: "s", Origin: OriginUser, Transport: TransportHTTP},
		"http にコマンド":    {Name: "s", Origin: OriginUser, Transport: TransportHTTP, URL: "https://e.com", Command: "x"},
		"非 http スキーム":   {Name: "s", Origin: OriginUser, Transport: TransportHTTP, URL: "ftp://e.com"},
		"URL に資格情報":     {Name: "s", Origin: OriginUser, Transport: TransportHTTP, URL: "https://u:p@e.com"},
		"未対応トランスポート":    {Name: "s", Origin: OriginUser, Transport: "sse", URL: "https://e.com"},
		"不正な環境変数名":      {Name: "s", Origin: OriginUser, Transport: TransportStdio, Command: "x", Env: map[string]string{"bad-name": "v"}},
		"ヘッダ名にコロン":      {Name: "s", Origin: OriginUser, Transport: TransportHTTP, URL: "https://e.com", Headers: map[string]string{"A:B": "v"}},
		"ヘッダ値に改行":       {Name: "s", Origin: OriginUser, Transport: TransportHTTP, URL: "https://e.com", Headers: map[string]string{"A": "v\r\nX: y"}},
		"未知の kind":      {Name: "s", Origin: OriginUser, Transport: TransportStdio, Command: "x", Kinds: []string{"shell"}},
		"範囲外のタイムアウト":    {Name: "s", Origin: OriginUser, Transport: TransportStdio, Command: "x", TimeoutMS: 10},
		"テナント配布の stdio": {Name: "s", Origin: OriginTenant, Transport: TransportStdio, Command: "x"},
	}
	for label, d := range cases {
		err := Validate(d)
		if err == nil {
			t.Errorf("%s: Validate = nil, want error", label)
			continue
		}
		var ve *ValidationError
		if !asValidation(err, &ve) {
			t.Errorf("%s: error is %T, want *ValidationError (handlers map it to 400)", label, err)
		}
	}
}

func asValidation(err error, target **ValidationError) bool {
	v, ok := err.(*ValidationError)
	if ok {
		*target = v
	}
	return ok
}

// テナント配布の stdio 拒否は ADR0031 決定 2 の中核。列を作らない設計と対で効くので、
// 「別のフィールドが埋まっていれば通る」抜け道が無いことを固定する。
func TestTenantStdioAlwaysRejected(t *testing.T) {
	d := ServerDef{Name: "s", Origin: OriginTenant, Transport: TransportStdio, Command: "/bin/sh", Args: []string{"-c", "curl evil"}}
	if err := Validate(d); err == nil {
		t.Fatal("テナント配布の stdio が通ってしまった")
	}
}

func TestMaskAndMergeSecrets(t *testing.T) {
	stored := ServerDef{
		Name: "s", Origin: OriginUser, Transport: TransportHTTP, URL: "https://e.com",
		Headers: map[string]string{"Authorization": "Bearer real", "X-Team": "ops"},
	}
	masked := Masked(stored)
	for k, v := range masked.Headers {
		if v != MaskedValue {
			t.Fatalf("Masked leaked %s=%q", k, v)
		}
	}
	if len(masked.Headers) != 2 {
		t.Fatalf("マスクでヘッダ名まで消えている: %v", masked.Headers)
	}

	// マスク値のまま返ってきたものは保存値を維持し、実値は上書き、欠けた鍵は削除。
	incoming := masked
	incoming.Headers = map[string]string{"Authorization": MaskedValue, "X-Team": "sre"}
	got := MergeSecrets(incoming, stored)
	if got.Headers["Authorization"] != "Bearer real" {
		t.Fatalf("マスク往復で秘密が失われた: %q", got.Headers["Authorization"])
	}
	if got.Headers["X-Team"] != "sre" {
		t.Fatalf("新しい値が反映されていない: %q", got.Headers["X-Team"])
	}

	// 保存値の無いマスクは「***」を本物の資格情報として送らないよう捨てる。
	got = MergeSecrets(ServerDef{Headers: map[string]string{"New": MaskedValue}}, stored)
	if _, ok := got.Headers["New"]; ok {
		t.Fatalf(`裏付けの無いマスク値が保存された: %v`, got.Headers)
	}
}

func TestReadyHoldsBackUnfilledSecrets(t *testing.T) {
	// テナントが user_secret で配ったヘッダ名だけの定義は、値が入るまで materialize しない。
	d := httpDef("t", "https://e.com")
	d.Enabled = true
	d.Headers = map[string]string{"Authorization": ""}
	if Ready(d) {
		t.Fatal("値が空のヘッダを持つ定義が ready になっている")
	}
	d.Headers["Authorization"] = MaskedValue
	if Ready(d) {
		t.Fatal("マスク値のままの定義が ready になっている")
	}
	d.Headers["Authorization"] = "Bearer x"
	if !Ready(d) {
		t.Fatal("値の揃った定義が ready でない")
	}
	if Ready(ServerDef{Enabled: false, Transport: TransportStdio, Command: "x"}) {
		t.Fatal("無効な定義が ready になっている")
	}
}

func TestAppliesToKinds(t *testing.T) {
	d := stdioDef("s")
	d.Enabled, d.Targets = true, Targets{Session: true}
	if !AppliesTo(d, session.KindClaude) {
		t.Fatal("kinds 未指定は全 kind に適用されるべき")
	}
	d.Kinds = []string{session.KindCodex}
	if AppliesTo(d, session.KindClaude) || !AppliesTo(d, session.KindCodex) {
		t.Fatal("kinds の絞り込みが効いていない")
	}
	d.Targets = Targets{Assistant: true}
	if AppliesTo(d, session.KindCodex) {
		t.Fatal("session ターゲットでない定義がセッションへ渡ろうとしている")
	}
}

func TestComposePrecedence(t *testing.T) {
	s := &secrets.Data{
		PagerDuty: &secrets.PagerDutyCreds{APIKey: "k"},
		MCP: []ServerDef{
			{ID: "u1", Name: "wiki", Origin: OriginUser, Enabled: true},
			{ID: "u2", Name: "Tickets", Origin: OriginUser, Enabled: true}, // テナントと衝突（大小無視）
		},
	}
	tc := tenantCache{FetchedAt: 42, Servers: []ServerDef{{ID: "t1", Name: "tickets", Enabled: true}}}

	reg := compose(s, tc, map[string]bool{})
	names := map[string]ServerDef{}
	for _, d := range reg.Servers {
		names[d.Name] = d
	}
	if _, ok := names["pagerduty"]; !ok {
		t.Fatal("接続済みの組み込み連携が一覧に出ていない")
	}
	if names["tickets"].Origin != OriginTenant || names["tickets"].ID != "t1" {
		t.Fatalf("名前衝突がテナント優先になっていない: %+v", names["tickets"])
	}
	if _, ok := names["Tickets"]; ok {
		t.Fatal("衝突した user 定義が有効なまま残っている")
	}
	if len(reg.Shadowed) != 1 || reg.Shadowed[0] != "Tickets" {
		t.Fatalf("shadowed = %v, want [Tickets]", reg.Shadowed)
	}
	if reg.TenantFetchedAt != 42 {
		t.Fatalf("TenantFetchedAt = %d, want 42", reg.TenantFetchedAt)
	}
	// 名前順に並ぶ（Console の一覧が呼ぶたび入れ替わらない）。
	for i := 1; i < len(reg.Servers); i++ {
		if strings.ToLower(reg.Servers[i-1].Name) > strings.ToLower(reg.Servers[i].Name) {
			t.Fatalf("名前順に並んでいない: %v", reg.Servers)
		}
	}
}

func TestComposeBuiltinNeedsConnection(t *testing.T) {
	reg := compose(&secrets.Data{}, tenantCache{}, map[string]bool{})
	for _, d := range reg.Servers {
		if d.Origin == OriginBuiltin {
			t.Fatalf("未接続の組み込み連携が出ている: %s", d.Name)
		}
	}
}

func TestComposeTenantOptOut(t *testing.T) {
	tc := tenantCache{Servers: []ServerDef{{ID: "t1", Name: "tickets", Enabled: true}}}
	reg := compose(&secrets.Data{}, tc, map[string]bool{"t1": true})
	if len(reg.Servers) != 1 || reg.Servers[0].Enabled {
		t.Fatalf("ローカル opt-out が効いていない: %+v", reg.Servers)
	}
}

// --- store CRUD（隔離 HOME） ------------------------------------------------

func withTempHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
}

func TestCRUDRoundTrip(t *testing.T) {
	withTempHome(t)

	created, err := Create(ServerDef{
		Name: "wiki", Transport: TransportHTTP, URL: "https://mcp.example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer secret"},
		Enabled: true, Targets: Targets{Session: true},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" || created.Origin != OriginUser || created.CreatedAt == 0 {
		t.Fatalf("Create が id/origin/createdAt を埋めていない: %+v", created)
	}

	if _, err := Create(ServerDef{Name: "WIKI", Transport: TransportStdio, Command: "x"}); err == nil {
		t.Fatal("大小違いの同名が作れてしまった")
	}

	// マスク往復で秘密が消えないこと（Console は必ずマスクを送り返す）。
	edit := Masked(created)
	edit.Label = "社内 Wiki"
	updated, err := Update(created.ID, edit)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("更新で秘密が失われた: %q", updated.Headers["Authorization"])
	}
	if updated.Label != "社内 Wiki" || updated.CreatedAt != created.CreatedAt {
		t.Fatalf("更新結果が不正: %+v", updated)
	}

	if err := SetEnabled(created.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	got, err := Get(created.ID)
	if err != nil || got.Enabled {
		t.Fatalf("Get after disable = %+v, %v", got, err)
	}

	if err := Delete(created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := Get(created.ID); err == nil {
		t.Fatal("削除後も取得できてしまう")
	}
}

func TestBuiltinIsReadOnly(t *testing.T) {
	withTempHome(t)
	s, _ := secrets.Load()
	s.PagerDuty = &secrets.PagerDutyCreds{APIKey: "k"}
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := Update(BuiltinPagerDuty, stdioDef("pagerduty")); err != ErrReadOnly {
		t.Fatalf("Update err = %v, want ErrReadOnly", err)
	}
	if err := Delete(BuiltinPagerDuty); err != ErrReadOnly {
		t.Fatalf("Delete err = %v, want ErrReadOnly", err)
	}
}

func TestForSessionFiltersAndForAssistant(t *testing.T) {
	withTempHome(t)
	if _, err := Create(ServerDef{
		Name: "sess", Transport: TransportStdio, Command: "/bin/true",
		Enabled: true, Targets: Targets{Session: true}, Kinds: []string{session.KindCodex},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Create(ServerDef{
		Name: "chat", Transport: TransportStdio, Command: "/bin/true",
		Enabled: true, Targets: Targets{Assistant: true},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := ForSession(session.KindCodex)
	if err != nil || len(got) != 1 || got[0].Name != "sess" {
		t.Fatalf("ForSession(codex) = %+v, %v", got, err)
	}
	if got, _ := ForSession(session.KindClaude); len(got) != 0 {
		t.Fatalf("ForSession(claude) = %+v, want empty", got)
	}
	forChat, err := ForAssistant()
	if err != nil || len(forChat) != 1 {
		t.Fatalf("ForAssistant = %+v, %v", forChat, err)
	}
}

// --- probe ------------------------------------------------------------------

// TestHelperMCPServer は probe 用の偽 MCP サーバー。テストバイナリを再実行して
// stdio サーバーの役を演じる（外部依存を持ち込まないための標準的な手口）。
func TestHelperMCPServer(t *testing.T) {
	if os.Getenv("AF_MCP_TEST_HELPER") == "" {
		t.Skip("helper process only")
	}
	// プロトコルを喋る前に 1 行バナーを出す実サーバーがあるので、その耐性も兼ねる。
	fmt.Println("fake mcp server starting")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			emit(m.ID, map[string]any{"serverInfo": map[string]any{"name": "fake", "version": "9.9"}})
		case "tools/list":
			emit(m.ID, map[string]any{"tools": []map[string]any{{"name": "search"}, {"name": "fetch"}}})
		}
	}
	os.Exit(0)
}

func emit(id any, result map[string]any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(b))
}

func TestProbeStdio(t *testing.T) {
	d := stdioDef("fake")
	d.Command = os.Args[0]
	d.Args = []string{"-test.run=TestHelperMCPServer"}
	d.Env = map[string]string{"AF_MCP_TEST_HELPER": "1"}

	res := Probe(context.Background(), d)
	if !res.OK {
		t.Fatalf("Probe failed: %s / %s", res.Error, res.Detail)
	}
	if res.ServerName != "fake" || res.ServerVersion != "9.9" {
		t.Fatalf("serverInfo = %s %s", res.ServerName, res.ServerVersion)
	}
	if res.ToolCount != 2 || len(res.Tools) != 2 {
		t.Fatalf("tools = %d %v", res.ToolCount, res.Tools)
	}
}

func TestProbeStdioReportsBrokenCommand(t *testing.T) {
	d := stdioDef("broken")
	d.Command = "/nonexistent/mcp-server"
	res := Probe(context.Background(), d)
	if res.OK || res.Error == "" {
		t.Fatalf("存在しないコマンドが成功扱い: %+v", res)
	}
}

// probeHTTP は JSON 応答と SSE 応答の両方を受ける必要がある（Streamable HTTP は
// サーバー実装ごとにどちらも返す）。1 本のテストで両経路を通す。
func TestProbeHTTPJSONAndSSE(t *testing.T) {
	var sawAuth, sawSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		switch m.Method {
		case "initialize":
			sawAuth = r.Header.Get("Authorization")
			w.Header().Set("Mcp-Session-Id", "sess-1")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{"serverInfo": map[string]any{"name": "remote", "version": "1.2"}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			sawSession = r.Header.Get("Mcp-Session-Id")
			w.Header().Set("Content-Type", "text/event-stream")
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{"tools": []map[string]any{{"name": "query"}}}})
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", body)
		}
	}))
	defer srv.Close()

	d := httpDef("remote", srv.URL)
	d.Headers = map[string]string{"Authorization": "Bearer tok"}
	res := Probe(context.Background(), d)
	if !res.OK {
		t.Fatalf("Probe failed: %s / %s", res.Error, res.Detail)
	}
	if res.ServerName != "remote" || res.ToolCount != 1 || res.Tools[0] != "query" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if sawAuth != "Bearer tok" {
		t.Fatalf("登録ヘッダが送られていない: %q", sawAuth)
	}
	if sawSession != "sess-1" {
		t.Fatalf("Mcp-Session-Id が引き継がれていない: %q", sawSession)
	}
}

func TestProbeHTTPReportsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	res := Probe(context.Background(), httpDef("remote", srv.URL))
	if res.OK {
		t.Fatal("401 が成功扱いになっている")
	}
	if !strings.Contains(res.Error, "401") {
		t.Fatalf("エラーに状態コードが出ていない: %q", res.Error)
	}
	if !strings.Contains(res.Detail, "nope") {
		t.Fatalf("応答本文が detail に出ていない: %q", res.Detail)
	}
}
