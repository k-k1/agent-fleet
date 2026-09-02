// runtime_health_test.go — 「Agent がまだ応答しない」を起動失敗にしないための回帰。
//
// 直した症状（実報告・2026-08-24）: ローカル docker デプロイの一部の利用者だけが、
// Workspace を起動するたびに赤いトースト `agent did not become healthy within 15s` を
// 踏み、しかも数秒後には普通に使えていた。起動は最初から成功していて、CP が「15 秒で
// /healthz が 200 を返さなければ失敗」と決めていただけ（自己更新 opt-in が ON の人
// だけ 300 秒だったので「一部の人だけ」に見えた）。docs/log/38 ★6 の定時実行障害と同根。
package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// unreadyAgent は /healthz を 503 で返し続ける Agent（＝まだ boot-install 中）。
func unreadyAgent(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func readyAgent(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestWaitAgentHealthyTimeoutIsTypedAndKeepsItsWording — 到達しなかったことを呼び出し側が
// **失敗と区別できる**のが今回の肝。同時に、文言は 1 文字も変えない: スケジュール実行履歴
// （error:wake: …）や運用の grep がこの文字列を拾っている。
func TestWaitAgentHealthyTimeoutIsTypedAndKeepsItsWording(t *testing.T) {
	srv := unreadyAgent(t)
	err := WaitAgentHealthy(context.Background(), srv.URL, 400*time.Millisecond)
	if err == nil {
		t.Fatal("unready agent: want an error")
	}
	var notReady agentNotReadyError
	if !errors.As(err, &notReady) {
		t.Fatalf("timeout must be typed as agentNotReadyError, got %T (%v)", err, err)
	}
	if want := "agent did not become healthy within 400ms"; err.Error() != want {
		t.Fatalf("message drifted: %q, want %q", err.Error(), want)
	}
	// キャンセルは別物（呼び出し側が去っただけ）。取り違えると、起動途中の中断を
	// 「まだ来ていない」と誤読して先へ進んでしまう。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cerr := WaitAgentHealthy(ctx, srv.URL, time.Second)
	if errors.As(cerr, &notReady) {
		t.Fatalf("canceled wait must not look like a readiness overrun: %v", cerr)
	}
}

// TestAgentStartingMarkerIsSelfHealing — 印は「起動途中だ」と名乗る根拠だが、放置されると
// 収束しない starting（Console から停止も再作成もできない箱・docs/log/70 §70.14.6）になる。
// 消える道が 2 本あることを固定する: Agent が上がった / 期限が切れた。
func TestAgentStartingMarkerIsSelfHealing(t *testing.T) {
	t.Run("印が無ければ starting ではない", func(t *testing.T) {
		m := agentStartingMarkerIn(t.TempDir())
		if m.active(context.Background(), unreadyAgent(t).URL) {
			t.Fatal("no marker: want not starting")
		}
	})

	t.Run("dataDir を持たない Runtime では常に無効", func(t *testing.T) {
		m := agentStartingMarkerIn("")
		m.arm(time.Now().Add(time.Hour)) // 書ける場所が無い＝何も起きない
		if m.active(context.Background(), unreadyAgent(t).URL) {
			t.Fatal("marker without a dataDir must never claim starting")
		}
	})

	t.Run("応答が無い間は starting", func(t *testing.T) {
		dir := t.TempDir()
		m := agentStartingMarkerIn(dir)
		m.arm(time.Now().Add(time.Hour))
		if !m.active(context.Background(), unreadyAgent(t).URL) {
			t.Fatal("armed marker + unready agent: want starting")
		}
		if _, err := os.Stat(filepath.Join(dir, ".agent-starting")); err != nil {
			t.Fatalf("marker must survive the probe while still booting: %v", err)
		}
	})

	t.Run("Agent が上がったら印を落として running へ戻る", func(t *testing.T) {
		dir := t.TempDir()
		m := agentStartingMarkerIn(dir)
		m.arm(time.Now().Add(time.Hour))
		if m.active(context.Background(), readyAgent(t).URL) {
			t.Fatal("healthy agent: want not starting")
		}
		if _, err := os.Stat(filepath.Join(dir, ".agent-starting")); !os.IsNotExist(err) {
			t.Fatalf("marker survived a healthy probe: %v", err)
		}
	})

	t.Run("期限切れは running へ落ちる（永遠の starting を作らない）", func(t *testing.T) {
		dir := t.TempDir()
		m := agentStartingMarkerIn(dir)
		m.arm(time.Now().Add(-time.Second))
		if m.active(context.Background(), unreadyAgent(t).URL) {
			t.Fatal("expired marker: want not starting")
		}
		if _, err := os.Stat(filepath.Join(dir, ".agent-starting")); !os.IsNotExist(err) {
			t.Fatalf("expired marker was not cleaned up: %v", err)
		}
	})

	t.Run("壊れた印も starting にしない", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".agent-starting"), []byte("nonsense"), 0o644); err != nil {
			t.Fatal(err)
		}
		if agentStartingMarkerIn(dir).active(context.Background(), unreadyAgent(t).URL) {
			t.Fatal("garbled marker: want not starting")
		}
	})
}

// hostPort splits an httptest URL into the host/port pair the adapters keep.
func hostPort(t *testing.T, raw string) (string, string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname(), u.Port()
}

// TestDockerStateReportsStartingWhileTheAgentBoots — 「コンテナが running = 使える」では
// ないことを状態に出す。ここが running のままだと、起動途中の Workspace にターミナルや
// ファイル取得が繋ぎに行き、誰も居ないソケットで失敗する。
func TestDockerStateReportsStartingWhileTheAgentBoots(t *testing.T) {
	dir := t.TempDir()
	agent := unreadyAgent(t)
	host, port := hostPort(t, agent.URL)
	status := "running"
	d := &dockerRuntime{name: "af-ws-x", dataDir: dir, agentHost: host, agentPort: port}
	d.inspect = func(_ context.Context, typ, _, _ string) string {
		if typ != "container" {
			t.Errorf("State inspected a %q, not the container", typ)
		}
		return status
	}
	ctx := context.Background()

	if got := d.State(ctx); got != "running" {
		t.Fatalf("no marker: State = %q, want running", got)
	}
	d.startingMarker().arm(time.Now().Add(time.Hour))
	if got := d.State(ctx); got != "starting" {
		t.Fatalf("booting agent: State = %q, want starting", got)
	}
	// 印が立っていても、コンテナが落ちていれば starting ではない（起きてすらいない）。
	status = "exited"
	if got := d.State(ctx); got != "stopped" {
		t.Fatalf("exited container: State = %q, want stopped", got)
	}
	status = ""
	if got := d.State(ctx); got != "none" {
		t.Fatalf("absent container: State = %q, want none", got)
	}
}

// TestNativeStateReportsStartingWhileTheAgentBoots — native も同じ。pid が生きている＝
// running だった頃は、rootfs 初回起動（boot-install で数分）がずっと「稼働中」に見えた。
func TestNativeStateReportsStartingWhileTheAgentBoots(t *testing.T) {
	dir := t.TempDir()
	agent := unreadyAgent(t)
	_, port := hostPort(t, agent.URL)
	n := &nativeRuntime{name: "af-ws-x", dataDir: dir, agentBin: os.Args[0], agentPort: port}
	// 自分自身を「生きている agent プロセス」として使う（pidAlive は argv0 の basename 比較）。
	if err := os.WriteFile(n.pidFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got := n.State(ctx); got != "running" {
		t.Fatalf("no marker: State = %q, want running", got)
	}
	n.startingMarker().arm(time.Now().Add(time.Hour))
	if got := n.State(ctx); got != "starting" {
		t.Fatalf("booting agent: State = %q, want starting", got)
	}
}

// TestWorkspaceAliveCoversStarting — home を消す判断（recreate / clean-home）は
// 「running か」ではなく「生きているか」で見る。起動途中のコンテナは bind-mount 配下に
// 書き込みうるので、ここが running 限定だと live な home を消しに行く。
func TestWorkspaceAliveCoversStarting(t *testing.T) {
	for state, want := range map[string]bool{"running": true, "starting": true, "stopped": false, "none": false} {
		if got := WorkspaceAlive(state); got != want {
			t.Errorf("WorkspaceAlive(%q) = %v, want %v", state, got, want)
		}
	}
}
