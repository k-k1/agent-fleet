package sessionx

// testhelp_test.go — この家系のテストが使っていた **package main のテストヘルパの写し**。
//
// 🔥 **Go はテストヘルパをパッケージを跨いで共有できない。** `_test.go` の中の識別子は
// そのパッケージのテストバイナリにしか存在しないので、`internal/sessionx` からは
// main 側の `withTempHome` も `gitInit` も見えない（エイリアスにも載せられない）。
// 家系 30 枚のテストを一緒に移す以上、ここに写しを置くしか無い —— これは
// `internal/browserx/mux_test.go` / `internal/memoryx/mux_test.go` が採ったのと同じ形である。
//
// ⚠️ **写しは黙って腐る。** 腐り方を減らすために:
//
//   - **本物と同じ綴り・同じ引数・同じ既定値**で写す（振る舞いを「改善」しない。
//     README §4 の「標準ライブラリの近似で作り直すと、両側変異試験でも捕まらないまま
//     被覆だけが縮む」を踏まないため）
//   - どのファイルの写しかを各関数の直前に書く
//   - `buildMux` だけは写しではなく**部分集合**なので、mux_test.go で
//     `routes.golden`（本物の mux から撮った 247 本）と突き合わせて腐りを検出する
//
// 📌 **tmux 隔離（isolatedTmuxSocket / isolateAgentState / paneShowing）だけは、
// main 側にも同じものが残る。** `session_rate_limit_state_test.go` を main に置いたままに
// してあるためで、これは意図的である —— main の `shutdown_isolation_test.go`（所有外）が
// あの 3 本に依存しており、移すと所有外のファイルを書き換えることになる。
// **#311 が「2 度書くと片方だけ古くなる」と書いて 1 本化した経緯があるので、
// ここは債務として報告してある**（パッケージ境界が跨げない以上、共有するには
// 非テストの共有パッケージへ出すしかなく、それは別の作業パッケージになる）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// tmuxSocketSeq は隔離ソケット名の連番（main 側 session_rate_limit_state_test.go の写し）。
var tmuxSocketSeq atomic.Int64

// --- withTempHome: workspace/agent/chat_main_test.go の写し ---
// withTempHome points HOME at a temp dir so the fstore/conversation stores write
// under the test's own tree（移送前の chat_report_test.go と同じ形）。
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

// --- writeFile: workspace/agent/repo_prompts_test.go の写し ---
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- isolateAgentConfigDirs: workspace/agent/routes_test.go の写し ---
// isolateAgentConfigDirs points HOME **and every config dir that is pinned by its own
// environment variable** at one throwaway tree.
//
// HOME alone is not enough, and the gap is not theoretical: paths.ClaudeConfigDir()
// honours $CLAUDE_CONFIG_DIR, which production sets to /var/lib/af/claude (a dedicated
// mount outside home). So a test that only isolated HOME and then hit
// POST /mcp-servers — which materializes the registry into every CLI's config — wrote
// its fixture server into the developer's REAL .claude.json. It was found there on
// 2026-08-09 as a live `wiki` → https://mcp.example.com/mcp entry, straight out of
// mcp_servers_test.go.
//
// Worse than the stray row: the ownership ledger (mcp-managed.json) DID land in the
// temp HOME, so af never learned it wrote that name — the row became an orphan no
// later materialize is allowed to remove (docs/log/48 §8.2), and only a hand-run
// `claude mcp remove` clears it.
//
// The other kinds escaped only by luck: CODEX_HOME / COPILOT_HOME / KIRO_HOME /
// XDG_CONFIG_HOME are unset in this container, so they resolved under the temp HOME.
// Set them here too rather than depend on that.
func isolateAgentConfigDirs(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for k, v := range map[string]string{
		"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"),
		"CODEX_HOME":        filepath.Join(home, ".codex"),
		"COPILOT_HOME":      filepath.Join(home, ".copilot"),
		"KIRO_HOME":         filepath.Join(home, ".kiro"),
		"XDG_CONFIG_HOME":   filepath.Join(home, ".config"),
		"XDG_DATA_HOME":     filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME":    filepath.Join(home, ".cache"),
		"XDG_STATE_HOME":    filepath.Join(home, ".local", "state"),
	} {
		t.Setenv(k, v)
	}
}

// --- isolatedTmuxSocket: workspace/agent/session_rate_limit_state_test.go の写し ---
// isolatedTmuxSocket は**誰とも共有しない** tmux ソケット名を返す。
//
// 🔥 隔離ソケットの名前を固定すると、`kill-server` を撃つテストどうしが競る（理由は
// isolateAgentState の注記）。名前の作り方をここ 1 箇所に置いているのは、**同じ規則を
// 2 度書くと片方だけ古くなる**から —— 実際 `shutdown_isolation_test.go` が同じ名前を
// 独自に組み立てていて、そこだけ直っていなかった（#311 では所有権の外だった）。
//
// この関数がこのファイルに居るのは所有権の都合で、意味の上では tmux 隔離の共有部品である。
func isolatedTmuxSocket() string {
	return fmt.Sprintf("af-test-%d-%d", os.Getpid(), tmuxSocketSeq.Add(1))
}

// --- isolateAgentState: workspace/agent/session_rate_limit_state_test.go の写し ---
func isolateAgentState(t *testing.T) {
	t.Helper()
	// 🔥 **ソケット名はテストごとに変える。** 以前は `af-test-<pid>` 固定で、この隔離を
	// 使う 4 ファイルの全テスト（と同じ名前を使う shutdown_isolation_test.go）が
	// **1 つの tmux サーバを共有**していた。各テストの Cleanup は `kill-server` を撃つが、
	// **tmux はコマンドを受け取った時点で返り、サーバの終了は非同期**である。だから次の
	// テストの `new-session` が死にかけのサーバへ繋がり、`server exited unexpectedly` で
	// 落ちる —— テスト本体とは無関係な、理由の見えない赤になる。
	//
	// 窓は負荷が高いほど広がる（実測 2026-09-02: 無負荷の `-count=30` では 0 回、CPU 負荷
	// 6 本の下の `-count=40` では 7 回。落ちたのは TestDriveStateIdlePaneNotBlocked と
	// TestDriveStateAuthValid ＝ **実 CI の run 33584943716 で落ちたのと同じ形**）。
	//
	// 連番まで入れるのは `-count=N` のため: テスト名だけだと、同じ名前の**前の周回**の
	// kill-server と競る。
	t.Setenv("AF_TMUX_SOCKET", isolatedTmuxSocket())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	// status ストアは HOME 直下（paths.AgentConfigDir）— 実フリートのマーカーを書かない。
	t.Setenv("HOME", t.TempDir())
	// claude の設定/資格情報も隔離する。HOME だけでは足りない: このコンテナでは
	// CLAUDE_CONFIG_DIR が実フリートの木を指しているので、状態判定（認証切れ・docs/log/47
	// §4-8）が実際のログイン期限に左右されてしまう。
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	// 専用ソケットに対してのみ kill-server が許される（dev/04 §4.11）。
	t.Cleanup(func() { _ = tmuxx.Cmd("kill-server").Run() })
}

// --- paneShowing: workspace/agent/session_rate_limit_state_test.go の写し ---
// paneShowing starts an isolated tmux session whose pane displays frame's contents and
// then stays alive, and returns the session meta for it.
func paneShowing(t *testing.T, name, frame string) session.Meta {
	t.Helper()
	// 🔥 **フレームの実在をここで見る。** 無くても `cat` が失敗するだけで `new-session` は
	// 成功するので、**呼び出し側は「空のペイン」を検査対象として素通りさせる**（移送で
	// 相対パスの深さが変わったとき、実際にそうなった）。呼び出し側の一覧を手で並べる検査は
	// 一覧が減ったことしか見られないので、**守るのは呼び出し口であるここ**。
	if _, err := os.Stat(frame); err != nil {
		t.Fatalf("frame %s が無い: %v（相対パスの深さを疑う。放置するとペインに何も出ないまま検査は緑になる）", frame, err)
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tn := session.TmuxName(name)
	// 幅を実ペイン相当に取る: 折返しが変わるとフッタ/選択肢行の見え方が変わる。
	out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "-x", "200", "-y", "50",
		"sh", "-c", fmt.Sprintf("cat %q; sleep 60", frame)).CombinedOutput()
	if err != nil {
		t.Fatalf("new-session %s: %v\n%s", tn, err, out)
	}
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	// cat がペインへ描き終わるのを待つ（capture-pane は描画済みの画面を読む）。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tmuxx.CapturePane(tn) != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return m
}

// --- do: workspace/agent/worktree_flow_test.go の写し ---
// do sends a JSON request, asserts the status, and decodes the body into out (if any).
func do(t *testing.T, srv *httptest.Server, method, path string, body any, want int, out any) {
	t.Helper()
	code, raw := roundtrip(t, srv, method, path, body)
	if code != want {
		t.Fatalf("%s %s = %d (%s), want %d", method, path, code, raw, want)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s: %v (%s)", path, err, raw)
		}
	}
}

// --- httpStatus: workspace/agent/worktree_flow_test.go の写し ---
func httpStatus(t *testing.T, srv *httptest.Server, method, path string, body any) int {
	t.Helper()
	code, _ := roundtrip(t, srv, method, path, body)
	return code
}

// --- roundtrip: workspace/agent/worktree_flow_test.go の写し ---
func roundtrip(t *testing.T, srv *httptest.Server, method, path string, body any) (int, []byte) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(res.Body)
	return res.StatusCode, buf.Bytes()
}

// --- gitInit: workspace/agent/git_integration_helpers_test.go の写し ---
func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f")
	run("commit", "-m", "init")
	run("branch", "feature")
}

// --- runIntegrationGit: workspace/agent/git_integration_helpers_test.go の写し ---
func runIntegrationGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// --- writeUIPrefs: workspace/agent/ui_prefs_test.go の写し ---
func writeUIPrefs(t *testing.T, body string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(homeDir(), ".config", "agent-fleet")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui-prefs.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// --- consoleCatalog: workspace/agent/console_catalog_test.go の写し ---
// consoleCatalog は Console の 1 ロケール分のカタログを、キー存在確認のために
// 1 本の文字列として返す。
//
// ⚠️ カタログはドメイン別に `locales/<locale>/*.ts` へ分かれており（ADR 0067 決定 4）、
// `locales/<locale>.ts` は import と spread しか持たない**合成ファイル**である。
// そちらを読むと「キーが在るのに無い」と言う検査になる —— この関数を経由すること。
//
// console/ を含まない配布物でビルドする場合に備えて、カタログが無ければスキップする。
func consoleCatalog(t *testing.T, locale string) string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "..", "console", "src", "lib", "i18n", "locales", locale)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("catalog not available (%v)", err)
	}
	var b strings.Builder
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s/%s: %v", dir, e.Name(), err)
		}
		b.Write(raw)
		b.WriteString("\n")
		n++
	}
	// 0 件でも「キーが無い」ではなく「読めていない」。黙って通すと、この検査は
	// 存在しないのと同じになる。
	if n == 0 {
		t.Fatalf("%s に .ts が 1 つも無い（カタログの置き場所が変わった？）", dir)
	}
	return b.String()
}

// --- consoleCatalogHasKey: workspace/agent/console_catalog_test.go の写し ---
// consoleCatalogHasKey は "key" がカタログに定義されているかを見る。
func consoleCatalogHasKey(catalog, key string) bool {
	return strings.Contains(catalog, `"`+key+`"`)
}

// --- awaitReported: workspace/agent/chat_main_test.go の写し ---
// awaitReported は「指示行が配送された報告で reported になる」まで待つ（移送前と同じ）。
func awaitReported(t *testing.T, name string) {
	t.Helper()
	for i := 0; i < 150; i++ {
		if !chatx.SessionReportPending(name) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("指示行は配送された報告で reported になるべき (指示1件=報告1回): %s", name)
}

// --- withTestReconciler: workspace/agent/chat_main_test.go の写し ---
// withTestReconciler は本物の reconciler を短い間隔で据える（移送前と同じ駆動）。
// 実体は chatx の内側なので、据え付けだけを継ぎ目から呼ぶ。
func withTestReconciler(t *testing.T, interval time.Duration) {
	t.Helper()
	t.Cleanup(chatx.InstallReconcilerForTest(interval))
}

// --- reportBodyForTest: workspace/agent/prompt_lang_test.go の写し ---
// reportBodyForTest はセッション報告 1 通の**プロンプト本文**（見出し＋事実＋指示＋付記）を、
// 実際の配送（recordSessionReport）と同じ材料の組み方で作る。日本語側の文言を見る既存の
// テストが使う。
func reportBodyForTest(display, name, kind, reason string) string {
	args := chatx.ReportArgs(display, name, kind, reason, 0)
	return chatx.ReportPromptFor(chatx.ChatMessage{
		Role: "report", ReportKind: kind, ReportReason: reason, NoticeArgs: args,
	}, "ja")
}

// TestFixturePathsResolve は、移送で深さが変わった**相対パスが今も解決すること**を見る。
//
// 🔥 これは README §4 の「相対パスの深さは移送で必ず変わり、直し忘れは黙って通る」を
// 実際に踏んだ跡である。移送直後、`paneShowing` に渡す
// `internal/tmuxx/testdata/footers/idle_bypass_hint.txt` は sessionx から見て存在しないが、
// tmux の中で走る `cat` が失敗するだけなので **new-session は成功し、
// TestDriveStateAuthExpired は緑のまま通った**（実測）。フレームが出ていないペインを
// 「認証切れ」と判定していたので、**検査したい枝に 1 度も到達していなかった**。
// `rate_limit_resume_test.go` の方は明示的な Fatal があったので落ちて気付けた。
//
// → **パスは使う前にここで 1 度だけ実体を見る。** 増えた 1 本はこの理由であり、
// 「テスト関数の集合が develop と一致」に対する**意図した +1** である。
func TestFixturePathsResolve(t *testing.T) {
	paths := []string{
		"../tmuxx/testdata/footers/idle_bypass_hint.txt",
		"../tmuxx/testdata/footers/modal_rate_limit.txt",
		filepath.Join("..", "..", "testdata", "routes.golden"),
		filepath.Join("..", "..", "..", "..", "console", "src", "lib", "i18n", "locales", "ja"),
	}
	if len(paths) < 4 {
		t.Fatal("走査したパスが 4 本未満＝この検査が無言化している")
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s が見つからない: %v（移送で相対パスの深さが変わったまま直っていない。"+
				"tmux の中の cat は失敗しても new-session は成功するので、"+
				"直さないと検査は緑のまま空振りする）", p, err)
		}
	}
}
