package opencode

// opencode serve の RuntimeSupervisor（docs/log/27 §3・§7、P2）。共有 `opencode serve`
// （HTTP＋SSE、loopback）を 1 プロセス起動・監視し、managed セッションの runtime を
// 提供する。責務は「プロセスの生涯」だけ — thread（opencode session）に何をさせるかは
// driver.go の ThreadHandle が持つ。
//
// 実測（1.17.18、docs/log/27 §12.2）:
//   - 認証: OPENCODE_SERVER_PASSWORD 未設定なら無認証（起動ログに unsecured 警告）。
//     コンテナ network namespace 内の loopback 限定なので codex app-server と同じ判断
//     （§9.1）で無認証運用。TUI アタッチも同条件で素通し。
//   - provider 鍵は env 注入（auth.go の env()）なので、鍵の変更は再起動が必須＝
//     generation＋drain（§7）がそのまま反映パス。
//   - serve は SQLite（message/part）へ v1 フローで書く。read 層（transcript.go）は
//     無傷のまま managed セッションの正本を読める。
//
// exit recording（docs/log/26・§10.2-2 の supervisor 移設）: daemon は supervisor の
// 子プロセスなので cmd.Wait() の wait status が直接取れる。予期しない死は
// (a) generation 履歴としてログへ、(b) thread レベルでは live な managed セッション
// 全員に status.PersistExit（既存の session-exit ストア・reason enum 共用）で記録し、
// 復旧（reconcile）が成功したセッションは baseline 書き込みでクリアされる。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

const serveAddrEnv = "AF_OPENCODE_SERVE_ADDR"

// defaultServeAddr は codex app-server（:7798）の隣。コンテナの network namespace
// 内 loopback なので衝突リスクはこのコンテナ内のプロセスに限る。
const defaultServeAddr = "http://127.0.0.1:7799"

// serveClient は制御系（create/abort/question/status）用の短タイムアウト HTTP。
// blocking の /message（turn 実行、時間無制限）だけは driver.go が独自 client を使う。
var serveClient = &http.Client{Timeout: 10 * time.Second}

// Supervisor owns the shared `opencode serve` daemon: idempotent start, health,
// generation counter, drain and restart（docs/log/27 §3 の RuntimeSupervisor）。
type Supervisor struct {
	mu   sync.Mutex
	addr string
	gen  int       // runtime generation（§7）。Ensure が新プロセスを起こすたび++
	cmd  *exec.Cmd // 現世代の子プロセス（adopt した既存プロセスなら nil）
	up   bool
	// stopping marks a deliberate teardown (Restart/Shutdown) so the waiter doesn't
	// record it as a crash or trigger reconciliation.
	stopping bool
}

var supervisor = &Supervisor{}

// Serve returns the package-wide supervisor instance.
func Serve() *Supervisor { return supervisor }

func serveAddr() string {
	if v := os.Getenv(serveAddrEnv); v != "" {
		return v
	}
	return defaultServeAddr
}

// Disabled reports whether managed opencode is switched off for this workspace.
func (s *Supervisor) Disabled() bool { return os.Getenv("AF_OPENCODE_SERVE_DISABLE") == "1" }

// Generation returns the current runtime generation（§7）。
func (s *Supervisor) Generation() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// Addr returns the serve base URL (also when not yet ensured — callers only use it
// after a successful Ensure).
func (s *Supervisor) Addr() string { return serveAddr() }

// healthy is a cheap liveness probe (GET /global/health).
func healthy(addr string) bool {
	req, err := http.NewRequest("GET", addr+"/global/health", nil)
	if err != nil {
		return false
	}
	res, err := serveClient.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode < 500
}

// Ensure starts (or adopts) the shared serve daemon, idempotently. Returns the
// base URL and the runtime generation the caller should stamp on its handles.
func (s *Supervisor) Ensure() (string, int, error) {
	if s.Disabled() {
		return "", 0, errors.New("opencode serve is disabled (AF_OPENCODE_SERVE_DISABLE=1)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	addr := serveAddr()
	if s.up && healthy(addr) {
		return addr, s.gen, nil
	}
	// Adopt a serve already listening (a previous Agent process spawned it and we
	// restarted — same pattern as connectCodexAppServer's connect-first). No cmd:
	// exit recording degrades to SSE-disconnect detection for an adopted daemon.
	if healthy(addr) {
		s.gen++
		s.cmd = nil
		s.up = true
		s.stopping = false
		go s.monitorEvents(addr, s.gen)
		log.Printf("opencode serve: adopted running daemon at %s (gen %d)", addr, s.gen)
		return addr, s.gen, nil
	}
	host, port, err := splitServeAddr(addr)
	if err != nil {
		return "", 0, err
	}
	cmd := exec.Command("opencode", "serve", "--hostname", host, "--port", port)
	// Provider keys are env-injected (auth.go) — the same set a TUI launch gets, so
	// managed turns authenticate identically. Re-keying requires a Restart（§7）.
	cmd.Env = append(os.Environ(), env()...)
	cmd.Dir = paths.HomeDir()
	if err := cmd.Start(); err != nil {
		return "", 0, fmt.Errorf("opencode serve の起動に失敗しました: %w", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if healthy(addr) {
			s.gen++
			s.cmd = cmd
			s.up = true
			s.stopping = false
			gen := s.gen
			go s.waitDaemon(cmd, gen)
			go s.monitorEvents(addr, gen)
			log.Printf("opencode serve: started (gen %d, pid %d, %s)", gen, cmd.Process.Pid, addr)
			return addr, gen, nil
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait() // reap: waitDaemon is only started on success — Kill alone leaves a zombie
	return "", 0, errors.New("opencode serve が時間内に起動しませんでした")
}

// splitServeAddr derives --hostname/--port from the configured http URL.
func splitServeAddr(addr string) (host, port string, err error) {
	rest, ok := strings.CutPrefix(addr, "http://")
	if !ok {
		return "", "", fmt.Errorf("unsupported %s (http://host:port only): %s", serveAddrEnv, addr)
	}
	host, port, ok = strings.Cut(strings.TrimSuffix(rest, "/"), ":")
	if !ok || host == "" || port == "" {
		return "", "", fmt.Errorf("unsupported %s (http://host:port only): %s", serveAddrEnv, addr)
	}
	return host, port, nil
}

// waitDaemon records WHY the owned daemon exited（§10.2-2: pane ラッパー record-exit
// の supervisor 移設）and kicks reconciliation for the surviving sessions.
func (s *Supervisor) waitDaemon(cmd *exec.Cmd, gen int) {
	err := cmd.Wait()
	s.mu.Lock()
	// `s.cmd != cmd`（codex 同型）: Restart は gen を変えずに stopping を戻すので、
	// gen 比較だけだと意図的停止を「died unexpectedly」に誤記録しうる。
	deliberate := s.stopping || s.cmd != cmd
	if s.cmd == cmd {
		s.up = false
		s.cmd = nil
	}
	s.mu.Unlock()
	if deliberate {
		return // Restart/Shutdown own this teardown; they log/rebuild themselves
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			} else {
				code = ee.ExitCode()
			}
		} else {
			code = -1
		}
	}
	sig := 0
	if code >= 128 {
		sig = code - 128
	}
	oomNow, okOOM := status.OOMKillCount()
	// generation 履歴（§9.5 の運用メタデータ）: P2 はログを台帳とする。
	log.Printf("opencode serve: daemon died unexpectedly (gen %d, code %d, sig %d, oomCount ok=%v)", gen, code, sig, okOOM)
	// thread レベルの記録: live だった managed セッション全員に。復旧に成功した
	// セッションは reconcile の baseline 書き込みでクリアされる（tui の再起動と同じ）。
	for _, h := range liveHandles() {
		base, _ := status.ReadExit(h.name)
		oom := okOOM && oomNow > base.OOMBase
		status.PersistExit(h.name, status.ExitInfo{
			Reason: status.ExitReasonFor(code, sig, oom),
			Code:   code, Signal: sig,
			At:      time.Now().Format(time.RFC3339),
			OOMBase: base.OOMBase,
		})
		h.runtimeLost()
	}
	go reconcileAll("daemon death")
}

// Restart は認証・設定変更の反映パス（§7）: generation++ → drain → 再生成 → 全
// handle の再 resume。共有 daemon なので drain は workspace 内 opencode managed
// セッション全体の切替窓になる（仕様、§7）。
func (s *Supervisor) Restart(reason string) {
	s.mu.Lock()
	if !s.up {
		s.mu.Unlock()
		return // not running — nothing to drain; next Ensure starts fresh
	}
	addr := serveAddr()
	cmd := s.cmd
	s.stopping = true
	s.mu.Unlock()

	log.Printf("opencode serve: restart requested (%s) — draining", reason)
	s.drain(addr)
	stopProcess(cmd, addr)
	s.mu.Lock()
	s.up = false
	s.cmd = nil
	s.stopping = false
	s.mu.Unlock()
	go reconcileAll("restart: " + reason)
}

// Shutdown drains and stops the daemon (graceful workspace stop, §10.2-8).
func (s *Supervisor) Shutdown() {
	s.mu.Lock()
	if !s.up {
		s.mu.Unlock()
		return
	}
	addr := serveAddr()
	cmd := s.cmd
	s.stopping = true
	s.up = false
	s.mu.Unlock()
	s.drain(addr)
	stopProcess(cmd, addr)
}

// drainTimeout bounds how long a drain waits for running turns to finish before
// aborting them（§7: 完走待ち、タイムアウトで interrupt）.
func drainTimeout() time.Duration {
	if v := os.Getenv("AF_OPENCODE_DRAIN_TIMEOUT_SEC"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil && d > 0 {
			return d
		}
	}
	return 60 * time.Second
}

// drain waits for the busy managed sessions to go idle, then aborts the stragglers.
func (s *Supervisor) drain(addr string) {
	deadline := time.Now().Add(drainTimeout())
	for time.Now().Before(deadline) {
		if len(busyManaged(addr)) == 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, h := range busyManaged(addr) {
		log.Printf("opencode serve: drain timeout — aborting session %s", h.sessionID())
		abortSession(addr, h.sessionID(), h.dir)
	}
	// Give the aborts a moment to unwind the blocked turn goroutines.
	time.Sleep(2 * time.Second)
}

// busyManaged lists the opencode sessions that are (a) busy per the runtime and
// (b) owned by a managed handle — a TUI-attached user's own experiments outside AF
// sessions are not ours to wait on. /session/status はプロジェクト（directory）に
// スコープされる（実測）ので、handle の dir ごとに照会して合成する。
func busyManaged(addr string) []*threadHandle {
	byDir := map[string][]*threadHandle{}
	for _, h := range liveHandles() {
		byDir[h.dir] = append(byDir[h.dir], h)
	}
	var out []*threadHandle
	for dir, hs := range byDir {
		res, err := serveClient.Get(dirQ(addr+"/session/status", dir))
		if err != nil {
			continue
		}
		var m map[string]struct {
			Type string `json:"type"`
		}
		err = json.NewDecoder(res.Body).Decode(&m)
		res.Body.Close()
		if err != nil {
			continue
		}
		for _, h := range hs {
			// /session/status は idle を省略する（実測）ので、載っている＝busy/retry。
			if st, ok := m[h.sessionID()]; ok && st.Type != "idle" {
				out = append(out, h)
			}
		}
	}
	return out
}

func abortSession(addr, ses, dir string) {
	req, err := http.NewRequest("POST", dirQ(addr+"/session/"+ses+"/abort", dir), strings.NewReader("{}"))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if res, err := serveClient.Do(req); err == nil {
		res.Body.Close()
	}
}

// stopProcess terminates the owned daemon (SIGTERM → SIGKILL). For an adopted
// daemon (cmd == nil) there is no handle to signal — best-effort skip.
func stopProcess(cmd *exec.Cmd, addr string) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	// Poll health only (codex 同型): cmd.ProcessState は waitDaemon の cmd.Wait() と
	// データレースになるので触らない。reap は waitDaemon の仕事。
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if !healthy(addr) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
}

// --- SSE event monitor ------------------------------------------------------

// monitorEvents subscribes to the serve's cross-project SSE stream（GET
// /global/event。実測: 素の /event は serve の cwd のプロジェクトにスコープされ、
// 別ディレクトリのセッションの question.asked 等が届かない — /global/event は
// {"payload": {...}} に包んで全プロジェクト分を配信する。codex と違いアタッチは
// 不要）and dispatches to the thread handles. A disconnect while the generation is still current means the
// daemon is gone or wedged: verify via health and reconcile.
func (s *Supervisor) monitorEvents(addr string, gen int) {
	for {
		if s.Generation() != gen {
			return // superseded generation — its monitor exits quietly
		}
		err := s.streamEvents(addr, gen)
		if s.Generation() != gen {
			return
		}
		if !healthy(addr) {
			// Owned daemon death is recorded by waitDaemon; an ADOPTED daemon's death
			// is only visible here. Either way handles must fall to unknown（§6-1）.
			s.mu.Lock()
			wasUp := s.up
			ownerless := s.cmd == nil
			if s.up && s.gen == gen {
				s.up = false
			}
			s.mu.Unlock()
			if wasUp && ownerless {
				log.Printf("opencode serve: adopted daemon lost (gen %d): %v", gen, err)
				for _, h := range liveHandles() {
					h.runtimeLost()
				}
				go reconcileAll("adopted daemon lost")
			}
			return
		}
		// Daemon alive, socket dropped (transient): resubscribe.
		time.Sleep(time.Second)
	}
}

// streamEvents reads one SSE connection until it breaks. Returns the read error.
func (s *Supervisor) streamEvents(addr string, gen int) error {
	req, err := http.NewRequest("GET", addr+"/global/event", nil)
	if err != nil {
		return err
	}
	// No timeout: SSE is a long-lived stream (heartbeats keep it warm).
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if s.Generation() != gen {
			return nil
		}
		handleServeEvent([]byte(data))
	}
	return sc.Err()
}

// WithControlCtx builds a request context for short control calls.
func controlCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
