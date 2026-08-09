package mcpreg

// docs/48 §13「materialize の非破壊性」: 利用者手書きの設定に対して書き→消しを
// 往復させ、**手書き分が残り af 分だけ消える**ことを固定する。ここが崩れると
// 「af が MCP を登録したら claude の trust ダイアログが飛んだ / codex の config が
// 読めなくなった」という、機能そのものより重い壊れ方をする。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// withTempCLIHomes isolates BOTH the af store and every CLI config tree. HOME covers
// most of them, but the three that have their own environment override do NOT: the
// workspace points CLAUDE_CONFIG_DIR at the shared claude state mount, so a test that
// forgot these would rewrite the developer's real config files.
func withTempCLIHomes(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SECRET_KEY", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude-cfg"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-home"))
	t.Setenv("COPILOT_HOME", filepath.Join(home, "copilot-home"))
	return home
}

func sessionDef(d ServerDef) ServerDef {
	d.Enabled = true
	d.Targets = Targets{Session: true}
	return d
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// --- claude ------------------------------------------------------------------

func TestMaterializeClaudeKeepsUserState(t *testing.T) {
	withTempCLIHomes(t)
	path := claudeJSONPath()
	// claude 自身が書く状態（オンボーディング済み・trust 済み）と、利用者が
	// `claude mcp add` で入れた自前サーバー。
	writeFile(t, path, `{
  "hasCompletedOnboarding": true,
  "projects": {"/home/dev/repos/x": {"hasTrustDialogAccepted": true}},
  "mcpServers": {"mine": {"type": "stdio", "command": "/usr/bin/mine"}}
}`)

	defs := []ServerDef{
		sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio,
			Command: "npx", Args: []string{"-y", "wiki-mcp"}, Env: map[string]string{"TOKEN": "s3cret"}}),
		sessionDef(ServerDef{Name: "tickets", Origin: OriginUser, Transport: TransportHTTP,
			URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t"}}),
	}
	written, removed, changed, err := materializeClaude(defs, nil)
	if err != nil || !changed {
		t.Fatalf("materializeClaude = %v, changed=%v", err, changed)
	}
	if len(written) != 2 || len(removed) != 0 {
		t.Fatalf("written=%v removed=%v", written, removed)
	}

	root := map[string]any{}
	if err := json.Unmarshal([]byte(readFile(t, path)), &root); err != nil {
		t.Fatal(err)
	}
	if v, _ := root["hasCompletedOnboarding"].(bool); !v {
		t.Fatal("claude 自身の状態が消えた（オンボーディングやり直しになる）")
	}
	if root["projects"] == nil {
		t.Fatal("trust 済みプロジェクトが消えた")
	}
	srv, _ := root["mcpServers"].(map[string]any)
	for _, want := range []string{"mine", "wiki", "tickets"} {
		if srv[want] == nil {
			t.Fatalf("mcpServers に %q が無い: %v", want, srv)
		}
	}
	if got := srv["tickets"].(map[string]any)["headers"].(map[string]any)["Authorization"]; got != "Bearer t" {
		t.Fatalf("リモートのヘッダが materialize されていない: %v", got)
	}

	// 2 回目は何も変わらない（claude が絶えず書き換えるファイルなので、無駄な書き戻しを
	// させない = 並走する claude の書き込みを踏み潰す窓を広げない）。
	if _, _, changed, err := materializeClaude(defs, written); err != nil || changed {
		t.Fatalf("2 回目 = changed %v, err %v（冪等でない）", changed, err)
	}

	// レジストリから全部消す: af が書いた 2 件だけが消え、利用者の "mine" は残る。
	_, removed, changed, err = materializeClaude(nil, written)
	if err != nil || !changed {
		t.Fatalf("削除 materialize = %v, changed=%v", err, changed)
	}
	if len(removed) != 2 {
		t.Fatalf("removed=%v, want 2 件", removed)
	}
	root = map[string]any{}
	if err := json.Unmarshal([]byte(readFile(t, path)), &root); err != nil {
		t.Fatal(err)
	}
	srv, _ = root["mcpServers"].(map[string]any)
	if len(srv) != 1 || srv["mine"] == nil {
		t.Fatalf("利用者の手書き分が巻き添えになった: %v", srv)
	}
}

func TestMaterializeClaudeNoKeyWhenEmpty(t *testing.T) {
	withTempCLIHomes(t)
	path := claudeJSONPath()
	writeFile(t, path, `{"hasCompletedOnboarding": true}`)

	def := sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio, Command: "x"})
	if _, _, _, err := materializeClaude([]ServerDef{def}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := materializeClaude(nil, []string{"wiki"}); err != nil {
		t.Fatal(err)
	}
	root := map[string]any{}
	if err := json.Unmarshal([]byte(readFile(t, path)), &root); err != nil {
		t.Fatal(err)
	}
	if _, ok := root["mcpServers"]; ok {
		t.Fatalf("空の mcpServers を残してしまった: %v", root)
	}
}

func TestMaterializeClaudeRefusesUnparseable(t *testing.T) {
	withTempCLIHomes(t)
	path := claudeJSONPath()
	broken := "{ this is not json"
	writeFile(t, path, broken)

	def := sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio, Command: "x"})
	if _, _, _, err := materializeClaude([]ServerDef{def}, nil); err == nil {
		t.Fatal("壊れた .claude.json を黙って上書きした")
	}
	if got := readFile(t, path); got != broken {
		t.Fatalf("拒否したのにファイルを触った: %q", got)
	}
}

func TestMaterializeClaudeCreatesFile(t *testing.T) {
	withTempCLIHomes(t)
	def := sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio, Command: "x"})
	if _, _, changed, err := materializeClaude([]ServerDef{def}, nil); err != nil || !changed {
		t.Fatalf("= %v, changed=%v", err, changed)
	}
	fi, err := os.Stat(claudeJSONPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600（秘密が入るファイル）", fi.Mode().Perm())
	}
}

// --- codex -------------------------------------------------------------------

const codexUserConfig = `# 利用者のコメント
model = "gpt-5"

[projects."/home/dev/repos/x"]
trust_level = "trusted"

[mcp_servers.mine]
command = "/usr/bin/mine"
`

func TestMaterializeCodexRoundTrip(t *testing.T) {
	withTempCLIHomes(t)
	path := codexConfigPath()
	writeFile(t, path, codexUserConfig)

	defs := []ServerDef{
		sessionDef(ServerDef{Name: "tickets", Origin: OriginUser, Transport: TransportHTTP,
			URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t", "X-Team": "sre"},
			TimeoutMS: 12000}),
		sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio,
			Command: "npx", Args: []string{"-y", "wiki-mcp"}, Env: map[string]string{"TOKEN": "s3cret"}}),
	}
	written, _, changed, err := materializeCodex(defs, nil)
	if err != nil || !changed {
		t.Fatalf("materializeCodex = %v, changed=%v", err, changed)
	}
	got := readFile(t, path)
	for _, want := range []string{
		"# 利用者のコメント",
		`[projects."/home/dev/repos/x"]`,
		"[mcp_servers.mine]",
		"[mcp_servers.tickets]",
		`url = "https://mcp.example.com/mcp"`,
		"startup_timeout_sec = 12.0",
		"[mcp_servers.tickets.http_headers]",
		`Authorization = "Bearer t"`,
		"[mcp_servers.wiki]",
		`args = ["-y","wiki-mcp"]`,
		"[mcp_servers.wiki.env]",
		`TOKEN = "s3cret"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("生成に %q が無い:\n%s", want, got)
		}
	}

	// 冪等: 同じ定義でもう一度書いても内容が動かない。
	if _, _, changed, err := materializeCodex(defs, written); err != nil || changed {
		t.Fatalf("2 回目 = changed %v, err %v（冪等でない）", changed, err)
	}

	// 全部消すと、**元のファイルに 1 バイト残らず戻る**。
	if _, _, _, err := materializeCodex(nil, written); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != codexUserConfig {
		t.Fatalf("往復で元に戻らない:\n--- got\n%s\n--- want\n%s", got, codexUserConfig)
	}
}

// Codex gives stdio MCP children a default-deny environment. The built-in af
// server needs these names forwarded or af_report/Chromium calls reach the Agent REST
// without the bearer token and get 401.
func TestMaterializeCodexBuiltinAFForwardsAgentAuth(t *testing.T) {
	withTempCLIHomes(t)
	defs := []ServerDef{sessionDef(ServerDef{
		ID: BuiltinAF, Name: BuiltinAF, Origin: OriginBuiltin,
		Transport: TransportStdio, Command: "/usr/bin/workspace-agent",
		Args: []string{"mcp-stdio", "--self-report", "--chromium-attach"},
	})}
	if _, _, _, err := materializeCodex(defs, nil); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, codexConfigPath())
	if want := `env_vars = ["AF_SESSION_NAME","AGENT_ADDR","AGENT_TOKEN"]`; !strings.Contains(got, want) {
		t.Fatalf("af の Agent 認証環境が Codex MCP 子プロセスへ転送されない:\n%s", got)
	}
	if strings.Contains(got, "AF_SECRET_KEY") {
		t.Fatalf("af session tools に不要な秘密ストア鍵を転送している:\n%s", got)
	}
}

// TestMaterializeCodexReplacesSameName は、同名の既存テーブルを必ず 1 つに畳むこと。
// TOML は重複テーブルをエラーにするので、ここを外すと config.toml 全体が読めなくなる
// （MCP が 1 本増えないどころか、codex が起動しなくなる）。
func TestMaterializeCodexReplacesSameName(t *testing.T) {
	withTempCLIHomes(t)
	path := codexConfigPath()
	writeFile(t, path, "[mcp_servers.wiki]\ncommand = \"/old/wiki\"\n")

	def := sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio, Command: "/new/wiki"})
	if _, _, _, err := materializeCodex([]ServerDef{def}, nil); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if n := strings.Count(got, "[mcp_servers.wiki]"); n != 1 {
		t.Fatalf("[mcp_servers.wiki] が %d 個（TOML の重複テーブル）:\n%s", n, got)
	}
	if strings.Contains(got, "/old/wiki") {
		t.Fatalf("古い定義が残った:\n%s", got)
	}
}

func TestMaterializeCodexQuotesOddHeaderKey(t *testing.T) {
	withTempCLIHomes(t)
	def := sessionDef(ServerDef{Name: "s", Origin: OriginUser, Transport: TransportHTTP,
		URL: "https://e.com/mcp", Headers: map[string]string{"X.Odd": "v"}})
	if _, _, _, err := materializeCodex([]ServerDef{def}, nil); err != nil {
		t.Fatal(err)
	}
	// クオートしないと `X.Odd` がドット記法で入れ子テーブルになり、別のヘッダになる。
	if got := readFile(t, codexConfigPath()); !strings.Contains(got, `"X.Odd" = "v"`) {
		t.Fatalf("ヘッダ名がクオートされていない:\n%s", got)
	}
}

func TestStripCodexServersLeavesOtherTables(t *testing.T) {
	src := "[mcp_servers.a]\ncommand = \"a\"\n\n[[profiles]]\nname = \"p\"\n\n[mcp_servers.b]\ncommand = \"b\"\n"
	got := stripCodexServers(src, func(n string) bool { return n == "a" })
	if strings.Contains(got, "[mcp_servers.a]") {
		t.Fatalf("a が消えていない:\n%s", got)
	}
	for _, want := range []string{"[[profiles]]", `name = "p"`, "[mcp_servers.b]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q を巻き添えで消した:\n%s", want, got)
		}
	}
}

// --- ディスパッチと台帳 --------------------------------------------------------

func TestMaterializeUsesSessionTargetAndKind(t *testing.T) {
	withTempCLIHomes(t)
	mustCreate := func(d ServerDef) {
		t.Helper()
		if _, err := Create(d); err != nil {
			t.Fatalf("Create(%s): %v", d.Name, err)
		}
	}
	mustCreate(ServerDef{Name: "both", Transport: TransportStdio, Command: "x",
		Enabled: true, Targets: Targets{Assistant: true, Session: true}})
	mustCreate(ServerDef{Name: "chatonly", Transport: TransportStdio, Command: "x",
		Enabled: true, Targets: Targets{Assistant: true}})
	mustCreate(ServerDef{Name: "off", Transport: TransportStdio, Command: "x",
		Targets: Targets{Session: true}})
	mustCreate(ServerDef{Name: "codexonly", Transport: TransportStdio, Command: "x",
		Enabled: true, Targets: Targets{Session: true}, Kinds: []string{session.KindCodex}})

	res := Materialize(session.KindClaude)
	if res.Err != "" {
		t.Fatalf("claude: %s", res.Err)
	}
	// af は自己申告ファストパスの組み込みサーバー（docs/51 Phase 3）で、接続不要・全 kind
	// 配布なので、どの kind の materialize にも必ず入る。
	if !reflect.DeepEqual(res.Written, []string{"af", "both"}) {
		t.Fatalf("claude written = %v, want [af both]", res.Written)
	}
	res = Materialize(session.KindCodex)
	if res.Err != "" {
		t.Fatalf("codex: %s", res.Err)
	}
	if !reflect.DeepEqual(res.Written, []string{"af", "both", "codexonly"}) {
		t.Fatalf("codex written = %v, want [af both codexonly]", res.Written)
	}

	// 台帳が kind 別に記録されていること（削除を許すのはこの一覧だけ）。
	m, err := loadManagedNames()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Kinds[session.KindClaude], []string{"af", "both"}) ||
		!reflect.DeepEqual(m.Kinds[session.KindCodex], []string{"af", "both", "codexonly"}) {
		t.Fatalf("台帳が不正: %+v", m.Kinds)
	}
}

// TestMaterializeSkipsKindsWithoutCLI: エージェント CLI を持たない kind（shell / ssm）は
// 書く先が無い。エラーではなく Skipped で戻ること。
func TestMaterializeSkipsKindsWithoutCLI(t *testing.T) {
	withTempCLIHomes(t)
	for _, k := range []string{"shell", "ssm"} {
		if res := Materialize(k); !res.Skipped || res.Err != "" {
			t.Fatalf("%s = %+v, want skipped（書く先が無いのは失敗ではない）", k, res)
		}
	}
}

func TestMaterializeAllCoversImplementedKinds(t *testing.T) {
	withTempCLIHomes(t)
	res := MaterializeAll()
	if len(res) != len(MaterializedKinds) {
		t.Fatalf("MaterializeAll = %d 件, want %d", len(res), len(MaterializedKinds))
	}
	for _, r := range res {
		if r.Err != "" || r.Skipped {
			t.Fatalf("%s = %+v", r.Kind, r)
		}
	}
}

// TestMaterializeRefusesCorruptLedger: 台帳が壊れたら「af は何も所有していない」と
// 解釈して既存行を孤児化するのではなく、書き込みごと止める。
func TestMaterializeRefusesCorruptLedger(t *testing.T) {
	withTempCLIHomes(t)
	writeFile(t, managedNamesPath(), "not json")
	if res := Materialize(session.KindClaude); res.Err == "" {
		t.Fatalf("壊れた台帳で materialize を続行した: %+v", res)
	}
}
