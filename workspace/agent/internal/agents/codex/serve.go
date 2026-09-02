package codex

// codex app-server の RuntimeSupervisor（docs/log/27 §3・§7、P3）。共有 `codex app-server`
// （WS JSON-RPC、loopback :7798）を 1 プロセス起動・監視し、managed セッションの
// runtime と単一の writer 接続（appClient）を提供する。責務は「プロセスと接続の生涯」
// だけ — thread に何をさせるかは driver.go の ThreadHandle が持つ。
//
// P1 の観測（package main の codexObserver、read-only）とは接続を分ける: 観測は
// TUI（CLI ルート）セッションの圧縮検知・rate limits 用にそのまま残り、managed の
// 書き込みはこの supervisor の writer 接続だけが行う（単一 writer 排他、§2）。
//
// 実測（0.144.4、docs/log/27 §12.3）:
//   - listen ポートは WS と同時に HTTP /healthz・/readyz を提供する — 健全性確認は
//     これを使う（opencode の /global/health と同型）。
//   - thread スコープ通知は「そのスレッドをロードした接続」にのみ配送（§12.1-1）。
//     writer 接続は自分が start/resume したスレッドの通知を必ず受ける。
//   - daemon SIGKILL 後の再起動→thread/resume で、実行中だった turn は status=
//     interrupted として rollout に確定しており、スレッドはそのまま継続できる（§12.3）。
//
// exit recording（docs/log/26・§10.2-2 の supervisor 移設）: daemon は supervisor の
// 子プロセスなので cmd.Wait() の wait status が直接取れる。予期しない死は live な
// managed セッション全員に status.PersistExit で記録し、復旧（reconcile）が成功した
// セッションは baseline 書き込みでクリアされる（opencode serve.go と同型）。

import (
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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// appServerAddrEnv は buildProgram（TUI --remote）と main の観測が参照する既存の
// 契約: 値が在る＝共有 app-server が使える。supervisor が起動成功時に export する。
const appServerAddrEnv = "AF_CODEX_APP_SERVER_ADDR"
const defaultAppServerAddr = "ws://127.0.0.1:7798"

// Supervisor owns the shared codex app-server daemon and the managed writer
// connection: idempotent start, health, generation counter, drain and restart
// （docs/log/27 §3 の RuntimeSupervisor）。
type Supervisor struct {
	mu     sync.Mutex
	gen    int        // runtime generation（§7）。新プロセス/新 writer 接続のたび++
	cmd    *exec.Cmd  // 現世代の子プロセス（adopt した既存プロセスなら nil）
	client *appClient // managed writer 接続（generation 単位）
	up     bool
	// stopping marks a deliberate teardown (Restart/Shutdown) so the waiter doesn't
	// record it as a crash or trigger reconciliation.
	stopping bool
	// watching: 需要ゼロ監視（agents.WatchIdle、idlestop.go）が 1 本回っている。
	watching bool
}

var supervisor = &Supervisor{}

// TUIDependents は「共有 daemon をバックエンドにしている TUI ルートの codex
// セッション数」を返す継ぎ目。この package はセッション台帳も tmux も持たないので、
// package main が起動時に差し替える（既定 0＝managed だけを見る）。
//
// これを数え損ねると需要ゼロ判定が生きている TUI セッションのバックエンドを
// 引き抜く — buildProgram が `codex --remote <addr>` を焼き込んでいるので、
// daemon が消えた TUI は会話ごと止まる。ここは「安全側＝多めに数える」で書くこと。
var TUIDependents = func() int { return 0 }

// dependents は共有 daemon を必要としているものの総数（managed ハンドル＋TUI）。
//
// managed 側は liveHandles ではなく **登録済みハンドル数** で数える: daemon が死ぬと
// runtimeLost が全ハンドルの alive を落とすので、live で数えると「死んだ直後は需要
// ゼロ」に見えて、復旧すべき場面で起こし直さなくなる。ハンドルが消えるのは
// DropHandle（停止・halt・アーカイブ）だけ＝利用者がそのセッションを畳んだとき。
func dependents() int {
	handlesMu.Lock()
	n := len(handles)
	handlesMu.Unlock()
	return n + TUIDependents()
}

// idleGraceEnv / defaultIdleGrace: 需要ゼロがこれだけ続いたら daemon を畳む。
// 0 で自動停止を無効化。冷起動 217 ms（実測）なので、停止→再開の往復は安い。
const idleGraceEnv = "AF_CODEX_APP_SERVER_IDLE_SEC"
const defaultIdleGrace = 2 * time.Minute

// Serve returns the package-wide supervisor instance.
func Serve() *Supervisor { return supervisor }

// Disabled reports whether the shared app-server is switched off (same env the
// pre-P3 startCodexAppServer honored).
func (s *Supervisor) Disabled() bool { return os.Getenv("AF_CODEX_APP_SERVER_DISABLE") == "1" }

// operatorAddr は「運用者が env で指定した listen 先」を一度だけ控える。同じ env は
// Ensure 成功時に『使える daemon がここに居る』印としても書かれる（buildProgram の
// --remote と観測が読む既存契約）ので、自動停止で印を外したときに運用者の指定まで
// 失わないよう、印を書く前の値をここに残しておく。
var (
	configuredOnce sync.Once
	configuredAddr string
)

func operatorAddr() string {
	configuredOnce.Do(func() { configuredAddr = os.Getenv(appServerAddrEnv) })
	return configuredAddr
}

// Addr returns the app-server ws address (live 印 → 運用者指定 → 既定)。
func (s *Supervisor) Addr() string {
	if v := os.Getenv(appServerAddrEnv); v != "" {
		return v
	}
	if v := operatorAddr(); v != "" {
		return v
	}
	return defaultAppServerAddr
}

// Generation returns the current runtime generation（§7）。
func (s *Supervisor) Generation() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// healthy probes the daemon's /healthz (the ws listen port doubles as HTTP —
// 実測 0.144.4). Unix-socket listeners fall back to a ws dial probe.
func healthy(addr string) bool {
	if rest, ok := strings.CutPrefix(addr, "ws://"); ok {
		c := &http.Client{Timeout: 2 * time.Second}
		res, err := c.Get("http://" + strings.TrimSuffix(rest, "/") + "/healthz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode < 500
	}
	conn, err := dialAppServer(addr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ErrNotLoggedIn: codex 未ログインなので共有 daemon を起こさない。app-server 自体は
// ログイン状態を見ずに listen できてしまう（約 110 MB 常駐する）が、認証していない
// ワークスペースでその常駐は丸ごと無駄になる。
var ErrNotLoggedIn = errors.New("codex にログインしていないため app-server を起動しません")

// Ensure starts (or adopts) the shared daemon and the managed writer connection,
// idempotently. Returns the writer client and the generation the caller should
// stamp on its handles.
//
// daemon を新しく起こすのは「codex にログイン済み」かつ「誰かが必要としている」
// ときだけ（起動条件は docs/log/27 §7 の補遺）。起動時に無条件で上げる運用は
// やめた — 冷起動 217 ms に対して常駐 110 MB は割に合わない。
func (s *Supervisor) Ensure() (*appClient, int, error) {
	if s.Disabled() {
		return nil, 0, errors.New("codex app-server is disabled (AF_CODEX_APP_SERVER_DISABLE=1)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	addr := s.Addr()
	if s.up && s.client != nil && s.client.alive() {
		return s.client, s.gen, nil
	}
	// Daemon already listening (started by a previous Agent process, or the writer
	// socket alone dropped): adopt it with a fresh writer connection. cmd==nil for a
	// daemon we didn't spawn — exit recording degrades to socket-loss detection.
	if !healthy(addr) {
		// 未ログインなら「起こさない」。既に listen している daemon の adopt は
		// 新たなメモリを食わないので素通しする（認証は codex 自身が turn 実行時に
		// 弾く — ここは起動コストの門であって認可の門ではない）。
		if !loggedIn() {
			return nil, 0, ErrNotLoggedIn
		}
		if err := s.startDaemonLocked(addr); err != nil {
			return nil, 0, err
		}
	}
	cl, err := newAppClient(addr)
	if err != nil {
		return nil, 0, fmt.Errorf("codex app-server writer 接続に失敗しました: %w", err)
	}
	s.gen++
	s.client = cl
	s.up = true
	s.stopping = false
	gen := s.gen
	cl.onClosed = func() { s.writerLost(gen) }
	go cl.readLoop()
	// buildProgram（TUI --remote）と main の観測が使う既存契約を維持する。
	_ = os.Setenv(appServerAddrEnv, addr)
	s.armIdleWatchLocked()
	log.Printf("codex app-server: writer connected (gen %d, %s)", gen, addr)
	// 観測（package main の read-only オブザーバ）に「今つながる」と知らせる。
	// 自動停止のあと需要で起き直したとき、最大 60 秒の再接続待ちを飛ばすため。
	go DaemonUp()
	return cl, gen, nil
}

// AdoptIfRunning は Agent 起動時の入口: 既に listen している daemon（前の Agent
// プロセスが起こし、graceful shutdown を経ずに残ったもの）だけを引き取る。
// 居なければ何もしない — 起動は需要側（managed の Resume / TUI の BuildLaunch）に
// 任せる。ここで無条件に Ensure すると、codex を一度も使わないワークスペースが
// 起動しただけで 110 MB を払うことになる。
func (s *Supervisor) AdoptIfRunning() {
	if s.Disabled() || !healthy(s.Addr()) {
		return
	}
	if _, _, err := s.Ensure(); err != nil {
		log.Printf("codex app-server: 稼働中の daemon を引き取れませんでした: %v", err)
	}
}

// armIdleWatchLocked starts the "需要ゼロで畳む" watcher, at most one at a time.
// 呼び出し側は s.mu を保持していること。
func (s *Supervisor) armIdleWatchLocked() {
	if s.watching {
		return
	}
	s.watching = true
	go agents.WatchIdle("codex app-server", dependents, s.stopIfIdle,
		agents.IdleGrace(idleGraceEnv, defaultIdleGrace))
}

// stopIfIdle は需要ゼロを **ロック内で再確認してから** daemon を畳む。監視ループの
// 判定と停止の間に Resume / BuildLaunch が走ると、生きているセッションの backend を
// 引き抜くことになるので、ここだけは競合を潰す。false = 需要が戻っていた（監視続行）。
func (s *Supervisor) stopIfIdle() bool {
	s.mu.Lock()
	if !s.up && s.cmd == nil {
		s.watching = false
		s.mu.Unlock()
		return true // 既に落ちている（daemon death 等）— 監視は降りる
	}
	if dependents() > 0 {
		s.mu.Unlock()
		return false
	}
	cmd, cl := s.cmd, s.client
	s.stopping, s.up, s.client, s.watching = true, false, nil, false
	s.mu.Unlock()

	if cl != nil {
		cl.close()
	}
	if cmd == nil {
		// adopt した daemon はプロセスハンドルが無く、シグナルを送れない。書き込み
		// 接続だけ畳んで監視を降りる（次に owned で起こし直したときに効く）。
		log.Printf("codex app-server: 需要ゼロだが adopt した daemon なので停止できません")
	} else {
		// 需要ゼロ＝走っている managed turn は無いので drain は要らない。
		stopProcess(cmd, s.Addr())
		log.Printf("codex app-server: 停止しました（需要ゼロ）")
	}
	s.mu.Lock()
	s.cmd = nil
	s.mu.Unlock()
	// stopping は戻さない: cl.close() が非同期に呼ぶ writerLost が「意図しない切断」と
	// 読み違えると、たった今畳んだ daemon を retryEnsure が起こし直してしまう。
	// 次の Ensure が成功時に false へ戻す（そこが唯一の再開点）。
	//
	// TUI は env が在れば --remote を焼き込む。落ちた daemon のアドレスを残すと
	// 次の起動が死んだ backend を掴むので、印を外して直接起動へ戻す。
	_ = os.Unsetenv(appServerAddrEnv)
	return true
}

// DaemonUp は「daemon が使える状態になった」を知らせる継ぎ目。package main の
// read-only オブザーバが再接続の待ちを打ち切るために差し替える。
var DaemonUp = func() {}

// startDaemonLocked spawns the daemon and waits for readiness. Caller holds mu.
func (s *Supervisor) startDaemonLocked(addr string) error {
	if strings.HasPrefix(addr, "unix://") {
		_ = os.Remove(strings.TrimPrefix(addr, "unix://")) // stale socket
	}
	cmd := exec.Command("codex", "app-server", "--listen", addr)
	if err := cmd.Start(); err != nil {
		_ = os.Unsetenv(appServerAddrEnv) // future TUI launches fall back to direct
		return fmt.Errorf("codex app-server の起動に失敗しました: %w", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if healthy(addr) {
			s.cmd = cmd
			go s.waitDaemon(cmd, s.gen+1) // Ensure increments gen right after
			log.Printf("codex app-server: started (pid %d, %s)", cmd.Process.Pid, addr)
			return nil
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait() // reap: waitDaemon is only started on success — Kill alone leaves a zombie
	_ = os.Unsetenv(appServerAddrEnv)
	return errors.New("codex app-server が時間内に起動しませんでした")
}

// waitDaemon records WHY the owned daemon exited（§10.2-2: pane ラッパー record-exit
// の supervisor 移設）and kicks reconciliation for the surviving sessions.
func (s *Supervisor) waitDaemon(cmd *exec.Cmd, gen int) {
	err := cmd.Wait()
	s.mu.Lock()
	deliberate := s.stopping || s.cmd != cmd
	if s.cmd == cmd {
		s.up = false
		s.cmd = nil
	}
	s.mu.Unlock()
	if deliberate {
		return // Restart/Shutdown own this teardown
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
	// generation 履歴（§9.5 の運用メタデータ）: P3 も opencode 同様ログを台帳とする。
	log.Printf("codex app-server: daemon died unexpectedly (gen %d, code %d, sig %d, oomCount ok=%v)", gen, code, sig, okOOM)
	ClearCompacting()
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
	// daemon は CLI ルート（TUI --remote）の backend でもある — managed セッションが
	// 0 でも、生きている TUI セッションが居るなら起こし直す（reconcileAll は managed
	// メタが無いと Ensure に届かない）。誰も待っていないなら起こし直さない: 需要ゼロで
	// 畳む方針（stopIfIdle）と同じ判断で、死んだ瞬間に 110 MB を取り戻す。
	// 失敗すれば startDaemonLocked が env を落とし、以後の TUI は直接起動へ戻る。
	if dependents() > 0 {
		go s.retryEnsure("daemon death")
	} else {
		_ = os.Unsetenv(appServerAddrEnv)
	}
	go reconcileAll("daemon death")
}

// retryEnsure brings the shared daemon back after an unexpected death, once per
// death with a short settle（即死ループの歯止め — 次の再試行は次の Resume 起点）。
func (s *Supervisor) retryEnsure(reason string) {
	time.Sleep(3 * time.Second)
	if _, _, err := s.Ensure(); err != nil {
		log.Printf("codex app-server: restart after %s failed: %v", reason, err)
	}
}

// writerLost handles the managed writer socket dropping while its generation is
// still current: daemon dead (owned death is recorded by waitDaemon; an adopted
// one only shows up here) or a transient socket loss. Either way handles fall to
// unknown and reconciliation redials（§6-1）.
func (s *Supervisor) writerLost(gen int) {
	s.mu.Lock()
	if s.gen != gen || s.stopping {
		s.mu.Unlock()
		return // superseded or deliberate teardown
	}
	ownerless := s.cmd == nil
	s.up = false
	s.mu.Unlock()
	log.Printf("codex app-server: writer connection lost (gen %d, adopted=%v)", gen, ownerless)
	for _, h := range liveHandles() {
		h.runtimeLost()
	}
	if ownerless {
		// adopt していた daemon の死はここでしか見えない — owned なら waitDaemon が
		// 再起動を持つ。socket blip だけなら Ensure が健在 daemon を adopt し直す。
		go s.retryEnsure("writer loss")
	}
	go reconcileAll("writer connection lost")
}

// Restart は認証・設定変更の反映パス（§7）: drain → 旧プロセス停止 → reconcile が
// 新世代を起こす。共有 daemon なので drain は workspace 内 codex セッション全体の
// 切替窓になる（仕様、§7 — CLI ルートの TUI backend も同じ daemon に乗っている）。
func (s *Supervisor) Restart(reason string) {
	s.mu.Lock()
	if !s.up && s.cmd == nil {
		s.mu.Unlock()
		return // not running — nothing to drain; next Ensure starts fresh
	}
	cmd := s.cmd
	cl := s.client
	s.stopping = true
	s.mu.Unlock()

	log.Printf("codex app-server: restart requested (%s) — draining", reason)
	s.drain()
	if cl != nil {
		cl.close()
	}
	stopProcess(cmd, s.Addr())
	s.mu.Lock()
	s.up = false
	s.cmd = nil
	s.client = nil
	s.stopping = false
	s.mu.Unlock()
	go reconcileAll("restart: " + reason)
}

// Shutdown drains and stops the daemon (graceful workspace stop, §10.2-8).
// tmux（CLI ルート）の C-c は shutdown.go が先に配っている前提。
func (s *Supervisor) Shutdown() {
	s.mu.Lock()
	if !s.up && s.cmd == nil {
		s.mu.Unlock()
		return
	}
	cmd := s.cmd
	cl := s.client
	s.stopping = true
	s.up = false
	s.watching = false
	s.mu.Unlock()
	s.drain()
	if cl != nil {
		cl.close()
	}
	stopProcess(cmd, s.Addr())
	_ = os.Unsetenv(appServerAddrEnv)
}

// drainTimeout bounds how long a drain waits for running managed turns to finish
// before interrupting them（§7: 完走待ち、タイムアウトで interrupt）.
func drainTimeout() time.Duration {
	if v := os.Getenv("AF_CODEX_DRAIN_TIMEOUT_SEC"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil && d > 0 {
			return d
		}
	}
	return 60 * time.Second
}

// drain waits for busy managed handles to go idle, then interrupts stragglers.
func (s *Supervisor) drain() {
	deadline := time.Now().Add(drainTimeout())
	for time.Now().Before(deadline) {
		if len(busyManaged()) == 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, h := range busyManaged() {
		log.Printf("codex app-server: drain timeout — interrupting session %s", h.name)
		_ = h.Interrupt()
	}
	// Give the interrupts a moment to land their turn/completed.
	time.Sleep(2 * time.Second)
}

// busyManaged lists handles with a turn in flight (queued 分は失われない — キューは
// handle 内にあり、reconcile 後の pump が続きを投入する).
func busyManaged() []*threadHandle {
	var out []*threadHandle
	for _, h := range liveHandles() {
		h.mu.Lock()
		busy := h.running
		h.mu.Unlock()
		if busy {
			out = append(out, h)
		}
	}
	return out
}

// stopProcess terminates the owned daemon (SIGTERM → SIGKILL). For an adopted
// daemon (cmd == nil) there is no handle to signal — best-effort skip.
func stopProcess(cmd *exec.Cmd, addr string) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if !healthy(addr) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
}
