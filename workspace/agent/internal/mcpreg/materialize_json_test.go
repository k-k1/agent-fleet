package mcpreg

// P5 で足した JSON 設定型 kind（opencode / copilot / cursor / kiro / agy）の materialize。
//
// 1 本目は claude と同じ「非破壊性」の固定（docs/log/48 §13）を全 kind に横断で当てる。
// 共通エンジン（materialize_json.go）を通っているので中身は同じ検証だが、**書く先の
// ファイルと map のキーが kind ごとに違う**のがこのフェーズの実体であり、そこを取り違えると
// 「登録したのに何も起きない」（別ファイルへ書いた）か「利用者の設定を壊した」になる。
//
// 2 本目以降は kind ごとの**エントリの形**。実測した契約（各 materialize_<kind>.go の
// 冒頭コメント）のうち、取りこぼすと機能が黙って無効になるキーを名指しで押さえる。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// jsonKindCases are the P5 kinds, each with the writer, the file it owns and the
// member that holds the server map.
var jsonKindCases = []struct {
	kind string
	fn   writer
	path func() string
	key  string
}{
	{"opencode", materializeOpencode, opencodeConfigPath, "mcp"},
	{"copilot", materializeCopilot, copilotMCPConfigPath, "mcpServers"},
	{"cursor", materializeCursor, cursorMCPConfigPath, "mcpServers"},
	{"kiro", materializeKiro, kiroMCPConfigPath, "mcpServers"},
	{"agy", materializeAgy, agyMCPConfigPath, "mcpServers"},
}

func p5Defs() []ServerDef {
	return []ServerDef{
		sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio,
			Command: "npx", Args: []string{"-y", "wiki-mcp"}, Env: map[string]string{"TOKEN": "s3cret"}}),
		sessionDef(ServerDef{Name: "tickets", Origin: OriginUser, Transport: TransportHTTP,
			URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t"},
			TimeoutMS: 12000}),
	}
}

// serverMap decodes the server map out of a kind's config file.
func serverMap(t *testing.T, path, key string) map[string]any {
	t.Helper()
	root := map[string]any{}
	if err := json.Unmarshal([]byte(readFile(t, path)), &root); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	m, _ := root[key].(map[string]any)
	return m
}

func TestMaterializeJSONKindsKeepUserState(t *testing.T) {
	for _, tc := range jsonKindCases {
		t.Run(tc.kind, func(t *testing.T) {
			withTempCLIHomes(t)
			path := tc.path()
			// 利用者が手で書いた（あるいは `<cli> mcp add` で入れた）自前サーバーと、
			// af が知らない他のキー。
			writeFile(t, path, `{
  "someOtherSetting": {"keep": true},
  "`+tc.key+`": {"mine": {"command": "/usr/bin/mine"}}
}`)

			defs := p5Defs()
			written, removed, changed, err := tc.fn(defs, nil)
			if err != nil || !changed {
				t.Fatalf("= %v, changed=%v", err, changed)
			}
			if len(written) != 2 || len(removed) != 0 {
				t.Fatalf("written=%v removed=%v", written, removed)
			}
			srv := serverMap(t, path, tc.key)
			for _, want := range []string{"mine", "wiki", "tickets"} {
				if srv[want] == nil {
					t.Fatalf("%s に %q が無い: %v", tc.key, want, srv)
				}
			}
			root := map[string]any{}
			if err := json.Unmarshal([]byte(readFile(t, path)), &root); err != nil {
				t.Fatal(err)
			}
			if root["someOtherSetting"] == nil {
				t.Fatal("af が知らない設定キーを巻き添えで消した")
			}

			// 冪等: 2 回目は書かない（CLI 自身も書くファイルなので、無変更の起動で
			// 書き戻すと相手の書き込みを踏み潰す窓が広がる）。
			if _, _, changed, err := tc.fn(defs, written); err != nil || changed {
				t.Fatalf("2 回目 = changed %v, err %v（冪等でない）", changed, err)
			}

			// レジストリから全部消す: af が書いた 2 件だけが消え、利用者の "mine" は残る。
			_, removed, changed, err = tc.fn(nil, written)
			if err != nil || !changed {
				t.Fatalf("削除 = %v, changed=%v", err, changed)
			}
			if len(removed) != 2 {
				t.Fatalf("removed=%v, want 2 件", removed)
			}
			srv = serverMap(t, path, tc.key)
			if len(srv) != 1 || srv["mine"] == nil {
				t.Fatalf("利用者の手書き分が巻き添えになった: %v", srv)
			}
		})
	}
}

// TestMaterializeJSONKindsRefuseUnparseable: 読めない設定は触らない。claude の
// オンボーディングフラグと同じ理由で、opencode.jsonc のコメントもここで守られる。
func TestMaterializeJSONKindsRefuseUnparseable(t *testing.T) {
	for _, tc := range jsonKindCases {
		t.Run(tc.kind, func(t *testing.T) {
			withTempCLIHomes(t)
			broken := "// 利用者のコメント\n{\"mcp\": {}}\n"
			writeFile(t, tc.path(), broken)
			if _, _, _, err := tc.fn(p5Defs(), nil); err == nil {
				t.Fatal("素の JSON として読めない設定を黙って上書きした")
			}
			if got := readFile(t, tc.path()); got != broken {
				t.Fatalf("拒否したのにファイルを触った: %q", got)
			}
		})
	}
}

func TestMaterializeJSONKindsCreateFile0600(t *testing.T) {
	for _, tc := range jsonKindCases {
		t.Run(tc.kind, func(t *testing.T) {
			withTempCLIHomes(t)
			if _, _, changed, err := tc.fn(p5Defs(), nil); err != nil || !changed {
				t.Fatalf("= %v, changed=%v", err, changed)
			}
			fi, err := os.Stat(tc.path())
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Fatalf("mode = %v, want 0600（秘密が入るファイル）", fi.Mode().Perm())
			}
		})
	}
}

// TestMaterializeJSONKindsLeaveMissingFileMissing: 書くものが無い kind は設定ファイルを
// 作らない。空の設定を置くと、利用者が触っていない CLI にも af の痕跡が増える。
func TestMaterializeJSONKindsLeaveMissingFileMissing(t *testing.T) {
	for _, tc := range jsonKindCases {
		t.Run(tc.kind, func(t *testing.T) {
			withTempCLIHomes(t)
			if _, _, changed, err := tc.fn(nil, nil); err != nil || changed {
				t.Fatalf("= %v, changed=%v", err, changed)
			}
			if _, err := os.Stat(tc.path()); !os.IsNotExist(err) {
				t.Fatalf("空のまま設定ファイルを作った: %v", tc.path())
			}
		})
	}
}

// --- opencode --------------------------------------------------------------------

// TestOpencodeConfigPathPrefersExisting: opencode は opencode.jsonc と opencode.json の
// **両方を読んでマージする**（実測 1.18.7）。af が「もう一方」へ書くと、同じサーバーが
// 二重に載る。実在する方を編集し、どちらも無ければ .jsonc を作ること。
func TestOpencodeConfigPathPrefersExisting(t *testing.T) {
	t.Run("neither", func(t *testing.T) {
		withTempCLIHomes(t)
		if got := filepath.Base(opencodeConfigPath()); got != "opencode.jsonc" {
			t.Fatalf("新規作成先 = %s, want opencode.jsonc", got)
		}
	})
	t.Run("json only", func(t *testing.T) {
		home := withTempCLIHomes(t)
		writeFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"), "{}")
		if got := filepath.Base(opencodeConfigPath()); got != "opencode.json" {
			t.Fatalf("既存の opencode.json ではなく %s を選んだ（設定が二重になる）", got)
		}
	})
	t.Run("both", func(t *testing.T) {
		home := withTempCLIHomes(t)
		dir := filepath.Join(home, ".config", "opencode")
		writeFile(t, filepath.Join(dir, "opencode.json"), "{}")
		writeFile(t, filepath.Join(dir, "opencode.jsonc"), "{}")
		if got := filepath.Base(opencodeConfigPath()); got != "opencode.jsonc" {
			t.Fatalf("両方ある場合 = %s, want opencode.jsonc（CLI 自身と entrypoint が作る方）", got)
		}
	})
}

func TestMaterializeOpencodeSeedsSchema(t *testing.T) {
	withTempCLIHomes(t)
	if _, _, _, err := materializeOpencode(p5Defs(), nil); err != nil {
		t.Fatal(err)
	}
	root := map[string]any{}
	if err := json.Unmarshal([]byte(readFile(t, opencodeConfigPath())), &root); err != nil {
		t.Fatal(err)
	}
	if root["$schema"] != "https://opencode.ai/config.json" {
		t.Fatalf("新規作成した設定に $schema が無い: %v", root)
	}
}

func TestOpencodeServersShape(t *testing.T) {
	got := OpencodeServers(p5Defs())
	// local は command と args を **1 本の配列** に畳む（実測）。分けて書くと起動しない。
	want := map[string]any{
		"type":        "local",
		"command":     []any{"npx", "-y", "wiki-mcp"},
		"environment": map[string]any{"TOKEN": "s3cret"},
		"enabled":     true,
	}
	if !reflect.DeepEqual(got["wiki"], want) {
		t.Fatalf("local = %#v, want %#v", got["wiki"], want)
	}
	rem, _ := got["tickets"].(map[string]any)
	if rem["type"] != "remote" || rem["url"] != "https://mcp.example.com/mcp" {
		t.Fatalf("remote = %#v", rem)
	}
	if _, ok := rem["timeout"]; ok {
		t.Fatal("opencode には per-server の timeout キーが無い（実測）— 効かない場所へ書いている")
	}
}

// --- copilot ---------------------------------------------------------------------

func TestCopilotServersShape(t *testing.T) {
	got := copilotServers(p5Defs())
	loc, _ := got["wiki"].(map[string]any)
	if loc["type"] != "local" || loc["command"] != "npx" {
		t.Fatalf("local = %#v", loc)
	}
	// tools を落とすと、`copilot mcp add` の既定（"*" = 全ツール）から外れる。
	if !reflect.DeepEqual(loc["tools"], []any{"*"}) {
		t.Fatalf("tools = %#v, want [\"*\"]（省略するとツールが出ない可能性）", loc["tools"])
	}
	rem, _ := got["tickets"].(map[string]any)
	if rem["type"] != "http" || rem["url"] != "https://mcp.example.com/mcp" {
		t.Fatalf("remote = %#v", rem)
	}
	// copilot の timeout は **ミリ秒**（codex の startup_timeout_sec と違い変換しない）。
	if rem["timeout"] != 12000 {
		t.Fatalf("timeout = %#v, want 12000（ms のまま）", rem["timeout"])
	}
	if rem["headers"].(map[string]any)["Authorization"] != "Bearer t" {
		t.Fatalf("headers = %#v", rem["headers"])
	}
}

// --- kiro ------------------------------------------------------------------------

func TestKiroServersShape(t *testing.T) {
	got := kiroServers(p5Defs())
	loc, _ := got["wiki"].(map[string]any)
	// kiro には type の判別子が無い。command / url の有無で決まる（実測 2.14.2）。
	if _, ok := loc["type"]; ok {
		t.Fatalf("kiro に type を書いている: %#v", loc)
	}
	if loc["command"] != "npx" || !reflect.DeepEqual(loc["args"], []any{"-y", "wiki-mcp"}) {
		t.Fatalf("local = %#v", loc)
	}
	rem, _ := got["tickets"].(map[string]any)
	if rem["url"] != "https://mcp.example.com/mcp" || rem["timeout"] != 12000 {
		t.Fatalf("remote = %#v", rem)
	}
	// ヘッダはテナント配布サーバーの認証手段そのもの（配布はリモート専用・ADR0031 決定 2）。
	// ここを落とすと「管理者が配ったサーバーだけ 401 になる」形で壊れる。
	if rem["headers"].(map[string]any)["Authorization"] != "Bearer t" {
		t.Fatalf("headers = %#v", rem["headers"])
	}
}

// --- cursor / agy ----------------------------------------------------------------

func TestCursorServersShape(t *testing.T) {
	got := cursorServers(p5Defs())
	loc, _ := got["wiki"].(map[string]any)
	// cursor のパーサは `"command" in o` で stdio を判定する（バンドル実測）。
	if loc["command"] != "npx" || !reflect.DeepEqual(loc["env"], map[string]any{"TOKEN": "s3cret"}) {
		t.Fatalf("local = %#v", loc)
	}
	rem, _ := got["tickets"].(map[string]any)
	if rem["url"] != "https://mcp.example.com/mcp" {
		t.Fatalf("remote = %#v", rem)
	}
	if _, ok := rem["timeout"]; ok {
		t.Fatal("cursor のエントリパーサに timeout は無い（実測）— 効かない場所へ書いている")
	}
}

func TestMaterializeAgyWritesGeminiConfig(t *testing.T) {
	home := withTempCLIHomes(t)
	if _, _, _, err := materializeAgy(p5Defs(), nil); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	if agyMCPConfigPath() != want {
		t.Fatalf("path = %s, want %s（agy は ~/.gemini をハードコードする）", agyMCPConfigPath(), want)
	}
	if got := readFile(t, want); !strings.Contains(got, `"mcpServers"`) {
		t.Fatalf("mcpServers が無い:\n%s", got)
	}
}
