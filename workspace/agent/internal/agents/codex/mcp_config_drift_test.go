//go:build drift

// codex app-server の MCP 設定リロード契約（docs/log/48 P3）。**実 codex バイナリに当てる**
// テストで、`go test ./...` からは build tag `drift` で除外される。
//
// なぜ要るか: MCP レジストリのセッション materialize は `$CODEX_HOME/config.toml` を
// 書き換える方式で、tui セッションは毎回 codex を起動し直すので必ず読み直される。
// ところが **managed セッションは共有 `codex app-server` に相乗りする**（docs/log/27）。
// この daemon が config を「プロセス起動時に 1 度だけ」読むのなら、登録した MCP は
// app-server を再起動するまで managed セッションに現れない — レジストリの UI は
// 「新規セッションから有効」と言っているのに、実際は効かないことになる。
//
// 実測（codex-cli 0.145.0）では **thread/start ごとに読み直す**。materialize →
// thread/start だけで足り、Supervisor.Restart（workspace 内の codex 全セッションを
// drain する重い操作）は要らない。この前提が崩れたらここが赤くなる。
//
// 認証不要: MCP サーバーの spawn は thread/start の内部で起き、モデル呼び出しの前に
// 完結する（thread/start 自体が認証エラーで返っても spawn は観測できる）。
//
// 補足（docs/log/27 §9.3.1）: managed セッションは thread 単位 config で af のエントリ
// **だけ**を上書きし、他は config.toml から継承する。つまりこのリロード契約は managed
// でも依然として効いており、レジストリに登録した MCP が新規 thread に現れるかどうかは
// ここが守っている。slot 空（`threadStart(cl, home, "", "")`）で呼ぶのは、af の上書きを
// 挟まない素の継承経路を測るため。

package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// waitFile polls for path up to d — the MCP child is spawned asynchronously.
func waitFile(path string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func TestDriftCodexAppServerRereadsMCPConfig(t *testing.T) {
	bin := codexBin(t)
	home := t.TempDir()
	marker := func(n string) string { return filepath.Join(home, n) }
	// The MCP "server" only has to be spawned, not to speak MCP: its side effect is
	// the observation. codex logs the handshake failure and moves on.
	serverBlock := func(name, out string) string {
		return "[mcp_servers." + name + "]\ncommand = \"/usr/bin/touch\"\nargs = [\"" + out + "\"]\n\n"
	}
	writeConfig := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(serverBlock("first", marker("first-spawned")))

	addr := "unix://" + filepath.Join(t.TempDir(), "app.sock")
	cmd := exec.Command(bin, "app-server", "--listen", addr)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	// `codex` is a Node shim that runs the vendored native binary as a CHILD, so killing
	// cmd.Process only reaps the shim: the native app-server is reparented to init and
	// keeps running (~115MB each, still holding its socket). That is the same trap
	// reapProcessGroup exists for, but this call site has no context to cancel — so put
	// the pair in its own process group and signal the group. Measured before this fix:
	// a single `-tags drift` run left two orphaned app-servers behind on the host.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("codex app-server: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = cmd.Wait()
	}()

	var cl *appClient
	deadline := time.Now().Add(15 * time.Second)
	for {
		var err error
		if cl, err = newAppClient(addr); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("app-server に接続できませんでした: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	go cl.readLoop()

	// 1st thread: the server present when the daemon started.
	if _, err := threadStart(cl, home, "", ""); err != nil {
		t.Logf("thread/start 1 (認証なしの想定内エラー): %v", err)
	}
	if !waitFile(marker("first-spawned"), 15*time.Second) {
		t.Fatal("起動時 config の MCP サーバーが spawn されなかった（このテストの前提が壊れている）")
	}

	// Now do what materialize does to a LIVE daemon: add a server to config.toml.
	writeConfig(serverBlock("first", marker("first-spawned")) + serverBlock("second", marker("second-spawned")))
	if _, err := threadStart(cl, home, "", ""); err != nil {
		t.Logf("thread/start 2 (認証なしの想定内エラー): %v", err)
	}
	if !waitFile(marker("second-spawned"), 15*time.Second) {
		t.Fatal("稼働中の app-server が config.toml を読み直さなくなった: " +
			"managed codex セッションは materialize した MCP を見なくなる。" +
			"docs/log/48 §8.3 を更新し、レジストリ変更時に Supervisor.Restart を呼ぶ配線が要る")
	}
}
