//go:build e2e

// フリート E2E（L2）: Control Plane をヘッドレス（AUTH=dev 既定）で起動し、公開 API
// だけで workspace 起動 → shell セッション作成 → input（echo）→ fs 読み戻し → 停止、
// という利用者の最短経路を実コンテナで検証する。kind=shell を使うので LLM
// クレデンシャルは一切不要。
//
// 前提: docker と build 済み Workspace イメージ（WS_IMAGE、既定
// agent-fleet/workspace:dev）。無ければ skip（CI では E2E_REQUIRE=1 で fail に格上げ）。
// 実行: cd e2e && go test -v -tags e2e -timeout 15m
//
// 衝突回避: テストごとに DEV_USER を分ける（コンテナ af-ws-<user> / ネットワーク
// af-net-<user>）。CP と Agent の listen ポートは空きポートを動的に確保 — 実フリートが
// 動く dev ホストでも干渉しない。teardown はコンテナ・ネットワーク・一時データを
// best-effort で回収する（コンテナ内 uid 1000 が書いた home は runner から消せない
// ことがあるため、最後はイメージ自身を root で回して rm する）。
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFleet(t *testing.T) {
	base := startFleet(t, "e2e")

	// --- shell セッション作成（LLM クレデンシャル不要）---
	created := postJSON(t, base+"/api/sessions", map[string]any{"kind": "shell", "title": "e2e"}, 201)
	name, _ := created["name"].(string)
	if name == "" {
		t.Fatalf("session create returned no name: %v", created)
	}
	t.Logf("session created: %s (kind=%v)", name, created["kind"])

	waitFor(t, 20*time.Second, "session alive in list", func() (bool, string) {
		// poll 条件内は非 fatal な tryGet を使う(一時エラーで即 Fatal だとリトライが死ぬ)
		code, body := tryGet(base + "/api/sessions")
		if code != 200 {
			return false, fmt.Sprintf("%d %s", code, truncate([]byte(body)))
		}
		var list map[string]any
		if err := json.Unmarshal([]byte(body), &list); err != nil {
			return false, "bad JSON: " + string(truncate([]byte(body)))
		}
		sessions, _ := list["sessions"].([]any)
		for _, s := range sessions {
			m, _ := s.(map[string]any)
			if m["name"] == name {
				alive, _ := m["alive"].(bool)
				return alive, fmt.Sprintf("alive=%v", alive)
			}
		}
		return false, "not in list"
	})

	// --- input で pane に echo を打鍵 → fs API で読み戻し（drive I/O の疎通）---
	nonce := fmt.Sprintf("e2e-ok-%d", os.Getpid())
	marker := "e2e-marker.txt" // セッションの cwd = home。fs API も home 相対で読む
	sendPrompt(t, base, name, fmt.Sprintf("echo %s > %s", nonce, marker))
	waitFileContains(t, base, marker, nonce, 60*time.Second)

	// --- status / 後片付け系 API ---
	status := getJSON(t, base+"/api/sessions/"+name+"/status")
	if alive, _ := status["alive"].(bool); !alive {
		t.Fatalf("session status alive=false: %v", status)
	}
	postJSON(t, base+"/api/sessions/"+name+"/stop", nil, 200)
	stop := postJSON(t, base+"/api/workspace/stop", nil, 200)
	if stop["state"] != "stopped" {
		t.Fatalf("workspace/stop state = %v, want stopped", stop["state"])
	}
}

// --- 共通ハーネス ---------------------------------------------------------

// startFleet は前提確認 → CP build/起動（AUTH=dev, DEV_USER=user）→ workspace running
// までを立ち上げ、CP のベース URL を返す。teardown（CP・コンテナ・ネットワーク・
// 一時データ）は t.Cleanup に登録済み。extraEnv は CP プロセスへの追加 env（KEY=VAL）。
func startFleet(t *testing.T, user string, extraEnv ...string) string {
	t.Helper()
	image := envOr("WS_IMAGE", "agent-fleet/workspace:dev")
	requireDockerAndImage(t, image)

	root := repoRoot(t)
	tmp := t.TempDir() // CP バイナリ・ログ置き場（コンテナは触らないので自動削除で安全）
	cpBin := buildCP(t, root, tmp)

	// Workspace データはコンテナ内 uid 1000 が書くため t.TempDir の自動削除（失敗 =
	// テスト失敗）に載せず、自前で best-effort 回収する。
	dataDir, err := os.MkdirTemp("", "af-e2e-data-")
	if err != nil {
		t.Fatal(err)
	}
	// 作成直後に回収を登録: cp.Start 前の Fatal 経路でも /tmp に残さない(#100)。
	// Cleanup は LIFO なので、後で登録する teardown(CP 停止・コンテナ回収)が先に走る。
	t.Cleanup(func() { cleanupDataDir(image, dataDir) })
	// GitHub ランナーなどホスト uid がコンテナの dev(uid 1000) と一致しない環境では、
	// CP(=このプロセスの uid) が 0755 で作る home / claude-config がコンテナ内から
	// 書けず、entrypoint（set -e）が落ちて Agent が healthz に到達しない。mount 先を
	// 先に 0777 で掘っておく（既存 dir は CP の MkdirAll が素通し。uid 1000 のホスト
	// では従来どおり無害）。パスは manager.rootedDataDir の既定テナント形
	// <WS_DATA>/<user>/{home,claude-config}。
	for _, d := range []string{"home", "claude-config"} {
		p := filepath.Join(dataDir, user, d)
		if err := os.MkdirAll(p, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o777); err != nil { // MkdirAll は umask に削られる
			t.Fatal(err)
		}
	}

	cpAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	agentPort := freePort(t)
	logPath := filepath.Join(tmp, "cp.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() }) // どの経路でも fd を閉じる(#100)

	cp := exec.Command(cpBin)
	cp.Dir = tmp
	cp.Stdout = logFile
	cp.Stderr = logFile
	cp.Env = append(append(os.Environ(),
		"CP_ADDR="+cpAddr,
		"WS_IMAGE="+image,
		"WS_DATA="+dataDir,
		"DEV_USER="+user,
		fmt.Sprintf("WS_AGENT_PORT=%d", agentPort),
		"CONSOLE_DIR="+tmp, // 静的配信は対象外（存在するダミー dir を指す）
	), extraEnv...)
	if err := cp.Start(); err != nil {
		t.Fatalf("start CP: %v", err)
	}
	t.Cleanup(func() { teardown(t, cp, logPath, user) })

	base := "http://" + cpAddr
	waitFor(t, 15*time.Second, "CP /healthz", func() (bool, string) {
		resp, err := http.Get(base + "/healthz")
		if err != nil {
			return false, err.Error()
		}
		resp.Body.Close()
		return resp.StatusCode == 200, resp.Status
	})

	// --- identity: dev 認証で固定ユーザーに解決される ---
	who := getJSON(t, base+"/api/whoami")
	if who["resolved_user"] != user {
		t.Fatalf("whoami resolved_user = %v, want %q", who["resolved_user"], user)
	}

	// --- workspace 起動（docker run + Agent healthz まで同期）---
	// Start は Agent healthz 待ち（15s）に一度でも間に合わないと 500 を返すが冪等
	// なので、成功するまでリトライしてから running を確認する。
	waitFor(t, 120*time.Second, "workspace/start accepted", func() (bool, string) {
		code, body := tryPost(base+"/api/workspace/start", nil)
		return code == 200, fmt.Sprintf("%d %s", code, body)
	})
	waitFor(t, 60*time.Second, "workspace running", func() (bool, string) {
		// poll 条件内は非 fatal な tryGet を使う(#99: getJSON は非200で即 Fatal)
		code, body := tryGet(base + "/api/workspace")
		if code != 200 {
			return false, fmt.Sprintf("%d %s", code, truncate([]byte(body)))
		}
		var ws map[string]any
		if err := json.Unmarshal([]byte(body), &ws); err != nil {
			return false, "bad JSON: " + string(truncate([]byte(body)))
		}
		return ws["state"] == "running", fmt.Sprint(ws["state"])
	})
	return base
}

// sendPrompt はセッションの pane にプロンプトを打鍵する（tmux 起動直後の 409 は
// リトライで吸収）。
func sendPrompt(t *testing.T, base, name, prompt string) {
	t.Helper()
	waitFor(t, 20*time.Second, "session input accepted", func() (bool, string) {
		code, body := tryPost(base+"/api/sessions/"+name+"/input", map[string]any{"prompt": prompt})
		return code == 200, fmt.Sprintf("%d %s", code, body)
	})
}

// waitFileContains は home 相対パスのファイルが want を含むまで fs API を poll する。
func waitFileContains(t *testing.T, base, relPath, want string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, relPath+" readable via fs API", func() (bool, string) {
		resp, err := http.Get(base + "/api/fs/file?path=" + relPath)
		if err != nil {
			return false, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			return false, fmt.Sprintf("%d %s", resp.StatusCode, truncate(b))
		}
		var out struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(b, &out)
		return strings.Contains(out.Content, want), string(truncate(b))
	})
}

// --- helpers -------------------------------------------------------------

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// prereqMissing は前提未充足を skip にする（CI は E2E_REQUIRE=1 で fail に格上げ）。
func prereqMissing(t *testing.T, msg string) {
	t.Helper()
	if os.Getenv("E2E_REQUIRE") == "1" {
		t.Fatal(msg)
	}
	t.Skip(msg)
}

func requireDockerAndImage(t *testing.T, image string) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		prereqMissing(t, "docker not on PATH")
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		prereqMissing(t, "workspace image not built: "+image+" (docker build -t "+image+" workspace/)")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(file)) // e2e/ の親 = リポジトリルート
}

func buildCP(t *testing.T, root, tmp string) string {
	t.Helper()
	bin := filepath.Join(tmp, "af-cp")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join(root, "control-plane")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build control-plane: %v\n%s", err, out)
	}
	return bin
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitFor は cond が真になるまで poll し、タイムアウトで最後の観測値を添えて fail する。
func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		ok, obs := cond()
		if ok {
			t.Logf("ok: %s", desc)
			return
		}
		last = obs
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timeout (%s) waiting for %s; last: %s", timeout, desc, last)
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, truncate(b))
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("GET %s: bad JSON: %v: %s", url, err, truncate(b))
	}
	return m
}

func postJSON(t *testing.T, url string, body map[string]any, wantStatus int) map[string]any {
	t.Helper()
	code, b := tryPost(url, body)
	if code != wantStatus {
		t.Fatalf("POST %s: %d (want %d) %s", url, code, wantStatus, b)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(b), &m)
	return m
}

// tryGet は fail せずに (status, body) を返す — リトライループ用。body は全文
// （JSON parse 用）。観測値表示には truncate を通すこと。
func tryGet(url string) (int, string) {
	resp, err := http.Get(url)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// tryPost は fail せずに (status, body) を返す — リトライループ用。
func tryPost(url string, body map[string]any) (int, string) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	resp, err := http.Post(url, "application/json", rd)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(truncate(b))
}

func truncate(b []byte) []byte {
	if len(b) > 400 {
		return append(b[:400:400], []byte("...")...)
	}
	return b
}

// teardown はコンテナ・ネットワーク・CP プロセスを回収する。失敗時は CP ログの
// 末尾を出す。すべて best-effort — 汚れても次回実行や CI の使い捨て環境で支障が
// 出ない範囲に留める。dataDir の回収は cleanupDataDir（作成直後に登録）が担う。
func teardown(t *testing.T, cp *exec.Cmd, logPath, user string) {
	t.Helper()
	// CP を先に落とす（reaper 等がコンテナを触り直さないように）。
	if cp.Process != nil {
		_ = cp.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _, _ = cp.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cp.Process.Kill()
		}
	}
	_ = exec.Command("docker", "rm", "-f", "af-ws-"+user).Run()
	_ = exec.Command("docker", "network", "rm", "af-net-"+user).Run()
	if t.Failed() {
		if b, err := os.ReadFile(logPath); err == nil {
			if len(b) > 8000 {
				b = b[len(b)-8000:]
			}
			t.Logf("--- control-plane log (tail) ---\n%s", b)
		}
	}
}

// cleanupDataDir は Workspace データ dir を best-effort で回収する。home はコンテナ内
// uid 1000 の所有物を含み、runner の uid では消せないことがある → イメージ自身を
// root で回して中身を rm してから RemoveAll。
func cleanupDataDir(image, dataDir string) {
	if err := os.RemoveAll(dataDir); err != nil {
		_ = exec.Command("docker", "run", "--rm", "--user", "0",
			"-v", dataDir+":/clean", "--entrypoint", "/bin/sh", image,
			"-c", "rm -rf /clean/* /clean/.[!.]* 2>/dev/null || true").Run()
		_ = os.RemoveAll(dataDir)
	}
}
