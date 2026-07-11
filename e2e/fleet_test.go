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
// 衝突回避: DEV_USER=e2e（コンテナ af-ws-e2e / ネットワーク af-net-e2e）、CP と
// Agent の listen ポートは空きポートを動的に確保 — 実フリートが動く dev ホストでも
// 干渉しない。teardown はコンテナ・ネットワーク・一時データを best-effort で回収する
// （コンテナ内 uid 1000 が書いた home は runner から消せないことがあるため、最後は
// イメージ自身を root で回して rm する）。
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

const devUser = "e2e" // → af-ws-e2e / af-net-e2e / <WS_DATA>/e2e

func TestFleet(t *testing.T) {
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

	cpAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	agentPort := freePort(t)
	logPath := filepath.Join(tmp, "cp.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}

	cp := exec.Command(cpBin)
	cp.Dir = tmp
	cp.Stdout = logFile
	cp.Stderr = logFile
	cp.Env = append(os.Environ(),
		"CP_ADDR="+cpAddr,
		"WS_IMAGE="+image,
		"WS_DATA="+dataDir,
		"DEV_USER="+devUser,
		fmt.Sprintf("WS_AGENT_PORT=%d", agentPort),
		"CONSOLE_DIR="+tmp, // 静的配信は対象外（存在するダミー dir を指す）
	)
	if err := cp.Start(); err != nil {
		t.Fatalf("start CP: %v", err)
	}
	t.Cleanup(func() { teardown(t, cp, image, dataDir, logPath) })

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
	if who["resolved_user"] != devUser {
		t.Fatalf("whoami resolved_user = %v, want %q", who["resolved_user"], devUser)
	}

	// --- workspace 起動（docker run + Agent healthz まで同期）---
	// Start は Agent healthz 待ち（15s）に一度でも間に合わないと 500 を返すが冪等
	// なので、成功するまでリトライしてから running を確認する。
	waitFor(t, 120*time.Second, "workspace/start accepted", func() (bool, string) {
		code, body := tryPost(base+"/api/workspace/start", nil)
		return code == 200, fmt.Sprintf("%d %s", code, body)
	})
	waitFor(t, 60*time.Second, "workspace running", func() (bool, string) {
		ws := getJSON(t, base+"/api/workspace")
		return ws["state"] == "running", fmt.Sprint(ws["state"])
	})

	// --- shell セッション作成（LLM クレデンシャル不要）---
	created := postJSON(t, base+"/api/sessions", map[string]any{"kind": "shell", "title": "e2e"}, 201)
	name, _ := created["name"].(string)
	if name == "" {
		t.Fatalf("session create returned no name: %v", created)
	}
	t.Logf("session created: %s (kind=%v)", name, created["kind"])

	waitFor(t, 20*time.Second, "session alive in list", func() (bool, string) {
		list := getJSON(t, base+"/api/sessions")
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
	prompt := fmt.Sprintf("echo %s > %s", nonce, marker)
	waitFor(t, 20*time.Second, "session input accepted", func() (bool, string) {
		code, body := tryPost(base+"/api/sessions/"+name+"/input", map[string]any{"prompt": prompt})
		return code == 200, fmt.Sprintf("%d %s", code, body)
	})
	waitFor(t, 60*time.Second, "marker file readable via fs API", func() (bool, string) {
		resp, err := http.Get(base + "/api/fs/file?path=" + marker)
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
		return strings.Contains(out.Content, nonce), string(truncate(b))
	})

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

// teardown はコンテナ・ネットワーク・CP プロセス・一時データを回収する。失敗時は
// CP ログの末尾を出す。すべて best-effort — 汚れても次回実行や CI の使い捨て環境で
// 支障が出ない範囲に留める。
func teardown(t *testing.T, cp *exec.Cmd, image, dataDir, logPath string) {
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
	_ = exec.Command("docker", "rm", "-f", "af-ws-"+devUser).Run()
	_ = exec.Command("docker", "network", "rm", "af-net-"+devUser).Run()
	// home はコンテナ内 uid 1000 の所有物を含み、runner の uid では消せないことが
	// ある → イメージ自身を root で回して中身を rm してから RemoveAll。
	if err := os.RemoveAll(dataDir); err != nil {
		_ = exec.Command("docker", "run", "--rm", "--user", "0",
			"-v", dataDir+":/clean", "--entrypoint", "/bin/sh", image,
			"-c", "rm -rf /clean/* /clean/.[!.]* 2>/dev/null || true").Run()
		_ = os.RemoveAll(dataDir)
	}
	if t.Failed() {
		if b, err := os.ReadFile(logPath); err == nil {
			if len(b) > 8000 {
				b = b[len(b)-8000:]
			}
			t.Logf("--- control-plane log (tail) ---\n%s", b)
		}
	}
}
