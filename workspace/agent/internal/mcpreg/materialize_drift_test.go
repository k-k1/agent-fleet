//go:build drift

// materialize のドリフト検知（docs/48 §13）。**実 claude / codex バイナリに当てる**
// テストで、通常の `go test ./...` からは build tag `drift` で除外される。
//
// なぜ要るか: materialize は「その CLI の設定ファイルはこういう形だ」という、こちらが
// 一方的に信じている契約の上に立っている。CLI が形を変えても af のユニットテストは
// 緑のまま（自分の出力を自分で読んでいるだけ）で、壊れたことに気付くのは利用者が
// 「登録したのにツールが出ない」と言うときになる。claude の TUI 文字列契約が版ごとに
// 壊れた件（false-idle）と同じ構図なので、CI で先に赤くする層を置く。
//
// 検証の作り: **期待値を手で書き写さない**。CLI 自身の `mcp add` を隔離 HOME で走らせて
// 生成させた設定と、af が同じ定義から materialize した設定を**構造比較**する。手写しの
// 期待値だと「af のテストが af と一致するだけ」の同語反復になる。
//
// 認証不要: `mcp add` / `mcp list` は設定ファイルの読み書きだけで完結する。

package mcpreg

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func cliBin(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("E2E_REQUIRE") == "1" {
			t.Fatalf("%s not on PATH and E2E_REQUIRE=1: %v", name, err)
		}
		t.Skipf("%s not on PATH (set E2E_REQUIRE=1 to make this fatal): %v", name, err)
	}
	return p
}

func runCLI(t *testing.T, env []string, bin string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", bin, args, err, out)
	}
	return out
}

// --- claude: $CLAUDE_CONFIG_DIR/.claude.json の mcpServers.<name> ----------------

func claudeServerEntry(t *testing.T, dir, name string) any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	var root struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("parse .claude.json: %v", err)
	}
	e, ok := root.MCPServers[name]
	if !ok {
		t.Fatalf("mcpServers に %q が無い: %v", name, root.MCPServers)
	}
	return e
}

func TestDriftClaudeMatchesMCPAdd(t *testing.T) {
	bin := cliBin(t, "claude")
	cases := []struct {
		name string
		def  ServerDef
		add  []string
	}{
		{
			name: "stdio",
			def: sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportStdio,
				Command: "/bin/echo", Args: []string{"a", "b"}, Env: map[string]string{"K": "v"}}),
			add: []string{"mcp", "add", "-s", "user", "afdrift", "-e", "K=v", "--", "/bin/echo", "a", "b"},
		},
		{
			name: "remote",
			def: sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportHTTP,
				URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t", "X-Team": "sre"}}),
			add: []string{"mcp", "add", "-s", "user", "-t", "http", "afdrift", "https://mcp.example.com/mcp",
				"-H", "Authorization: Bearer t", "-H", "X-Team: sre"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// CLI に書かせた側。
			cliDir := t.TempDir()
			runCLI(t, []string{"CLAUDE_CONFIG_DIR=" + cliDir}, bin, tc.add...)
			want := claudeServerEntry(t, cliDir, "afdrift")

			// af が materialize した側。
			afHome := t.TempDir()
			t.Setenv("HOME", afHome)
			afDir := filepath.Join(afHome, "claude-cfg")
			t.Setenv("CLAUDE_CONFIG_DIR", afDir)
			if _, _, _, err := materializeClaude([]ServerDef{tc.def}, nil); err != nil {
				t.Fatalf("materializeClaude: %v", err)
			}
			got := claudeServerEntry(t, afDir, "afdrift")

			if !reflect.DeepEqual(got, want) {
				gj, _ := json.MarshalIndent(got, "", "  ")
				wj, _ := json.MarshalIndent(want, "", "  ")
				t.Fatalf("claude の user スコープ設定形が変わった（docs/48 §8.1 の更新が要る）\n--- af\n%s\n--- claude mcp add\n%s", gj, wj)
			}
		})
	}
}

// --- codex: $CODEX_HOME/config.toml の [mcp_servers.<name>] ---------------------

// codexListed returns what `codex mcp list --json` makes of the config in home —
// i.e. codex's OWN reading of what af wrote, not af's reading of it.
func codexListed(t *testing.T, bin, home, name string) map[string]any {
	t.Helper()
	out := runCLI(t, []string{"CODEX_HOME=" + home}, bin, "mcp", "list", "--json")
	// The CLI prefixes a PATH-alias warning on a tempdir CODEX_HOME; the JSON array
	// is the tail of the output.
	i := 0
	for i < len(out) && out[i] != '[' {
		i++
	}
	var servers []map[string]any
	if err := json.Unmarshal(out[i:], &servers); err != nil {
		t.Fatalf("parse `codex mcp list --json`: %v\n%s", err, out)
	}
	for _, s := range servers {
		if s["name"] == name {
			return s
		}
	}
	t.Fatalf("codex が %q を認識しなかった（af が書いた config.toml を読めていない）:\n%s", name, out)
	return nil
}

func TestDriftCodexMatchesMCPAdd(t *testing.T) {
	bin := cliBin(t, "codex")
	def := sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportStdio,
		Command: "/bin/echo", Args: []string{"a", "b"}, Env: map[string]string{"K": "v"}})

	cliHome := t.TempDir()
	runCLI(t, []string{"CODEX_HOME=" + cliHome}, bin,
		"mcp", "add", "afdrift", "--env", "K=v", "--", "/bin/echo", "a", "b")
	want := codexListed(t, bin, cliHome, "afdrift")

	afHome := t.TempDir()
	t.Setenv("HOME", afHome)
	codexHome := filepath.Join(afHome, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	if _, _, _, err := materializeCodex([]ServerDef{def}, nil); err != nil {
		t.Fatalf("materializeCodex: %v", err)
	}
	got := codexListed(t, bin, codexHome, "afdrift")

	if !reflect.DeepEqual(got["transport"], want["transport"]) {
		gj, _ := json.MarshalIndent(got["transport"], "", "  ")
		wj, _ := json.MarshalIndent(want["transport"], "", "  ")
		t.Fatalf("codex の stdio 設定形が変わった（docs/48 §8.1 の更新が要る）\n--- af\n%s\n--- codex mcp add\n%s", gj, wj)
	}
}

// TestDriftCodexRemoteKeys pins the remote half, which `codex mcp add` cannot
// express (it has no header flags — that gap is what the old docs/48 note mistook
// for "codex has no remote headers"). So the reference here is codex's own reader:
// af writes the file, codex parses it back.
func TestDriftCodexRemoteKeys(t *testing.T) {
	bin := cliBin(t, "codex")
	def := sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportHTTP,
		URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t"},
		TimeoutMS: 12000})

	home := t.TempDir()
	t.Setenv("HOME", home)
	codexHome := filepath.Join(home, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	if _, _, _, err := materializeCodex([]ServerDef{def}, nil); err != nil {
		t.Fatalf("materializeCodex: %v", err)
	}
	got := codexListed(t, bin, codexHome, "afdrift")

	tr, _ := got["transport"].(map[string]any)
	if tr["type"] != "streamable_http" || tr["url"] != "https://mcp.example.com/mcp" {
		t.Fatalf("リモートが streamable_http として読まれていない: %v", tr)
	}
	hdr, _ := tr["http_headers"].(map[string]any)
	if hdr["Authorization"] != "Bearer t" {
		t.Fatalf("http_headers が届いていない: %v", tr)
	}
	if got["startup_timeout_sec"] != 12.0 {
		t.Fatalf("startup_timeout_sec = %v, want 12（TimeoutMS の ms→s 変換）", got["startup_timeout_sec"])
	}
}

// TestDriftCodexAcceptsUserFileWithAFBlocks: 利用者の手書き config に af が追記した
// あとでも codex が**ファイル全体を**読めること。TOML の重複テーブルや壊れた追記は
// MCP が 1 本増えない程度では済まず、codex 自体が起動しなくなる。
func TestDriftCodexAcceptsUserFileWithAFBlocks(t *testing.T) {
	bin := cliBin(t, "codex")
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexHome := filepath.Join(home, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"),
		[]byte(codexUserConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	def := sessionDef(ServerDef{Name: "mine", Origin: OriginUser, Transport: TransportStdio,
		Command: "/bin/echo"}) // 手書きと同名 = 重複テーブルになりうる組み合わせ
	if _, _, _, err := materializeCodex([]ServerDef{def}, nil); err != nil {
		t.Fatalf("materializeCodex: %v", err)
	}
	got := codexListed(t, bin, codexHome, "mine")
	tr, _ := got["transport"].(map[string]any)
	if tr["command"] != "/bin/echo" {
		t.Fatalf("同名の手書きテーブルが af 定義に置き換わっていない: %v", tr)
	}
}
