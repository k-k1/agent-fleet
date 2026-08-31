package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// P3-9 idle-stop (scale-to-zero). A single background goroutine drives a
// two-tier reclaim so an OOM-prone single host (see host-oom-fleet-risk) sheds
// RAM as workspaces go cold:
//
//	Tier 1 — session auto-stop: an idle claude session (jsonl-durable, so
//	  resumable) is halted into 停止中 once it has sat idle past the tenant's
//	  session_idle_timeout. This frees the heavy claude process while the
//	  container keeps running. Shells are NOT halted here (halt = kill and a
//	  shell's live process/jobs are not durable) — they ride tier 2.
//
//	Tier 2 — workspace stop: a workspace with no live activity (no open
//	  terminal/preview connection, no working/question session, no recent
//	  request) past ws_idle_timeout is docker-stopped, reclaiming the rest.
//
//	Tier 3 — home hibernation: an ALREADY-STOPPED workspace whose home nobody
//	  has opened since home_hibernate_after is asked to put that home to sleep
//	  (ecs-ec2: snapshot the EBS volume and delete it; the next Start restores
//	  it). Runs on stopped workspaces, so it is the mirror image of tiers 1–2,
//	  and only a runtime that implements hibernatingRuntime does anything.
//
//	Tier 4 — home backup: every home_backup_every, a copy of the home is taken
//	  somewhere its Availability Zone is not. Unlike the other three this one is
//	  not about idleness at all — it runs whatever the workspace is doing, because
//	  the thing it protects against (losing the AZ) does not wait for people to go
//	  home. Only ecs-ec2 implements it; on every other runtime the home is not
//	  pinned to one AZ in the first place.
//
// Timeouts are per-tenant (tenantLimits, super_admin-editable) with a
// deployment default from env; "0" disables idle-stop for that tenant.

// connRegistry tracks the live signals the reaper needs but the DB doesn't
// carry: which workspaces have an open long-lived connection (someone is
// watching), which sessions have an attached terminal, and when each workspace
// was last touched by any request. All in-memory (resets on CP restart, which
// the reaper treats as a fresh grace window via bootTime).
type connRegistry struct {
	mu      sync.Mutex
	wsConns map[string]int // workspaceID -> open long-lived conns (terminal/preview)
	// wsAttn は wsConns のうち「人が触っていること」を条件に在席と数える接続の数
	// （＝端末）。定時実行の起床が張る presence は無条件側に残る（人の打鍵が無いのは
	// 当たり前で、それを不在と読むと配達中に Workspace を止めてしまう）。
	wsAttn    map[string]int
	attached  map[string]map[string]int // workspaceID -> session name -> attached terminals
	lastSeen  map[string]time.Time      // workspaceID -> last request activity
	lastInput map[string]time.Time      // workspaceID -> last TERMINAL keystroke
}

const (
	workspacePresenceHeartbeat = 5 * time.Second
	workspacePresenceTTL       = 15 * time.Second
)

// presenceGrace は「打鍵の無い端末を、あと何分だけ在席と見なすか」（docs/75 P3）。
//
// なぜ要るか: 端末ペインを開いた Console のタブを 1 枚閉じ忘れると、presence lease が
// 5 秒ごとに更新され続け、**Workspace は永久に停止しない**。これは question と並ぶ
// 「止まらない」の主因で、しかも question と違って利用者は自分が課金を続けていることに
// 気づけない（画面は何も言わない）。
//
// テナント別にしないのは、これが課金方針ではなく**人の注意**の定数だから — 30 分
// 打鍵が無ければキーボードの前に人は居ない。実際に止まるまでの時間を決めるのは
// 従来どおり ws_idle_timeout（テナント別）で、この値はその時計を「開きっぱなしの
// ソケット」が止めてしまうのを防ぐだけ。0 で無効（＝従来どおりソケットがある限り在席）。
var presenceGrace = 30 * time.Minute

var errWorkspaceStopping = errors.New("workspace idle stop is in progress")

func workspaceActivityAPIError(err error) *apiError {
	if errors.Is(err, errWorkspaceStopping) {
		return &apiError{http.StatusConflict, "workspace_stopping", "workspace is stopping; retry after it has stopped"}
	}
	return &apiError{http.StatusServiceUnavailable, "activity_unavailable", "workspace activity could not be recorded"}
}

func newConnRegistry() *connRegistry {
	return &connRegistry{
		wsConns:   map[string]int{},
		wsAttn:    map[string]int{},
		attached:  map[string]map[string]int{},
		lastSeen:  map[string]time.Time{},
		lastInput: map[string]time.Time{},
	}
}

// touch records request activity against a workspace (called from every CP
// ingress). Cheap in-memory stamp — no DB write on the hot path.
func (r *connRegistry) touch(wsID string) {
	if r == nil || wsID == "" {
		return
	}
	r.mu.Lock()
	r.lastSeen[wsID] = time.Now()
	r.mu.Unlock()
}

// addConn/doneConn bracket a long-lived connection. session may be "" (preview /
// a fresh shell with no session name); a non-empty session also marks
// that specific session as terminal-attached so tier 1 won't halt it.
func (r *connRegistry) addConn(wsID, session string, attention bool) {
	if r == nil || wsID == "" {
		return
	}
	r.mu.Lock()
	r.wsConns[wsID]++
	r.lastSeen[wsID] = time.Now()
	if attention {
		r.wsAttn[wsID]++
		// 開いた瞬間は「人が今そこに居る」— 打鍵を待たずに在席から始める。
		r.lastInput[wsID] = time.Now()
	}
	if session != "" {
		if r.attached[wsID] == nil {
			r.attached[wsID] = map[string]int{}
		}
		r.attached[wsID][session]++
	}
	r.mu.Unlock()
}

func (r *connRegistry) doneConn(wsID, session string, attention bool) {
	if r == nil || wsID == "" {
		return
	}
	r.mu.Lock()
	if r.wsConns[wsID] > 0 {
		r.wsConns[wsID]--
	}
	if r.wsConns[wsID] == 0 {
		delete(r.wsConns, wsID)
		delete(r.lastInput, wsID)
	}
	if attention {
		if r.wsAttn[wsID] > 0 {
			r.wsAttn[wsID]--
		}
		if r.wsAttn[wsID] == 0 {
			delete(r.wsAttn, wsID)
		}
	}
	r.lastSeen[wsID] = time.Now() // a disconnect is itself recent activity
	if session != "" && r.attached[wsID] != nil {
		if r.attached[wsID][session] > 0 {
			r.attached[wsID][session]--
		}
		if r.attached[wsID][session] == 0 {
			delete(r.attached[wsID], session)
		}
		if len(r.attached[wsID]) == 0 {
			delete(r.attached, wsID)
		}
	}
	r.mu.Unlock()
}

// snapshot returns (open conns, last-seen, ok) for a workspace.
func (r *connRegistry) snapshot(wsID string) (conns int, lastSeen time.Time, ok bool) {
	if r == nil {
		return 0, time.Time{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ls, has := r.lastSeen[wsID]
	return r.wsConns[wsID], ls, has
}

// noteInput records a terminal keystroke — the one signal that says a human is
// actually there. Pings and resizes deliberately do NOT come through here: a
// background tab sends both, and counting them would restore exactly the "an
// open socket warms the workspace forever" behaviour this exists to remove.
func (r *connRegistry) noteInput(wsID string) {
	if r == nil || wsID == "" {
		return
	}
	r.mu.Lock()
	now := time.Now()
	r.lastInput[wsID] = now
	r.lastSeen[wsID] = now
	r.mu.Unlock()
}

// watched reports whether someone is present at this workspace right now.
//
// 接続の**有無**ではなく「人が触っているか」で答える（docs/75 原則 3）。端末以外の
// 長命接続（定時実行の起床）は打鍵という概念を持たないので無条件に在席とする。
func (r *connRegistry) watched(wsID string, grace time.Duration, now time.Time) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	total := r.wsConns[wsID]
	if total == 0 {
		return false
	}
	if total > r.wsAttn[wsID] {
		return true // 端末以外の接続が 1 本でもあれば在席
	}
	if grace <= 0 {
		return true // 機能オフ＝従来どおりソケットがある限り在席
	}
	li, ok := r.lastInput[wsID]
	return ok && now.Sub(li) < grace
}

// attentionFresh は presence lease を更新し続けてよいかの判定（heartbeat から）。
// 純関数に切ってあるのは、5 秒周期の goroutine を待たずにテストで固定するため。
func (r *connRegistry) attentionFresh(wsID string, grace time.Duration, now time.Time) bool {
	if r == nil || grace <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	li, ok := r.lastInput[wsID]
	return ok && now.Sub(li) < grace
}

func (r *connRegistry) isAttached(wsID, session string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attached[wsID] != nil && r.attached[wsID][session] > 0
}

// touchWorkspace mirrors request activity both locally and into a single
// monotonic DB row. The shared watermark closes the HA blind spot where the
// reaper on CP-B could not see traffic served by CP-A.
func (m *manager) activityLockFor(wsID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activityLocks == nil {
		m.activityLocks = map[string]*sync.Mutex{}
	}
	if m.activityLocks[wsID] == nil {
		m.activityLocks[wsID] = &sync.Mutex{}
	}
	return m.activityLocks[wsID]
}

func (m *manager) touchWorkspace(ctx context.Context, wsID string) error {
	m.conns.touch(wsID)
	return m.recordWorkspaceActivity(ctx, wsID, false)
}

func (m *manager) recordWorkspaceActivity(ctx context.Context, wsID string, force bool) error {
	lock := m.activityLockFor(wsID)
	lock.Lock()
	defer lock.Unlock()
	now := time.Now().UTC()
	if !force {
		m.mu.Lock()
		protected := m.activityProtectedUntil[wsID]
		m.mu.Unlock()
		if protected.After(now) {
			return nil
		}
	}
	_, lastSeen, seen := m.conns.snapshot(wsID)
	if !seen {
		lastSeen = time.Now()
	}
	accepted, err := m.store.RecordWorkspaceActivity(ctx, wsID, leaseTS(lastSeen),
		leaseTS(now.Add(workspacePresenceTTL)), leaseTS(now))
	if err != nil {
		return err
	}
	if !accepted {
		return errWorkspaceStopping
	}
	m.mu.Lock()
	if m.activityProtectedUntil == nil {
		m.activityProtectedUntil = map[string]time.Time{}
	}
	m.activityProtectedUntil[wsID] = now.Add(workspacePresenceHeartbeat)
	m.mu.Unlock()
	return nil
}

// trackWorkspaceConnection publishes a renewable cross-replica presence lease
// for a scheduler keepalive. One goroutine per long-lived connection is
// bounded by the number of actual connections; DB storage remains one row per WS.
func (m *manager) trackWorkspaceConnection(ctx context.Context, wsID, session string) (func(), error) {
	return m.trackPresence(ctx, wsID, session, false)
}

// trackWorkspaceTerminal is the same lease for an attached TERMINAL, with one
// difference that decides whether idle-stop works at all: the lease is renewed only
// while the human is still typing (docs/75 P3).
//
// なぜ端末だけ別扱いか: 端末ペインを開いた Console のタブを閉じ忘れると、この lease が
// 5 秒ごとに更新され続け、**Workspace は永久に停止しない**。lease は「ソケットがある」
// ことしか語れないのに、reaper はそれを「人が見ている」と読んでいた。返る noteInput を
// 打鍵のたびに呼ぶことで、lease は「人が触っている」を語るようになる。
// ★noteInput は打鍵の中継経路上で呼ばれる（proxy.go relay: onInput() → キーの転送）。
// つまりここでブロックした時間は、そのまま**そのキーがエコーバックされるまでの遅延**に
// なる。以前はここで recordWorkspaceActivity を同期呼び出ししており、5 秒に 1 回、
// ある打鍵だけが DB 1 往復ぶん（activityLockFor を握ったまま）止まっていた。畳み込みが
// 効いていても「1 打鍵に 1 書き込みではない」ことしか保証せず、当たった打鍵の遅延は
// DB の応答時間そのものになる。端末は 1 文字の往復で品質が決まる面なので、在席の記録の
// ために打鍵を待たせてはいけない。
//
// 書き込みは専用の goroutine へ出し、容量 1 のチャネルで畳む: 書き込み中に来た打鍵は
// 「もう 1 回やる」を 1 個だけ残して素通りする（在席は「最近打鍵があった」という単調な
// 事実なので、取りこぼしても次の打鍵か heartbeat が同じ結論を書く）。in-memory 側の
// noteInput は残す — reaper の在席判定はこれを読むので、即時であることに意味がある。
func (m *manager) trackWorkspaceTerminal(ctx context.Context, wsID, session string) (release func(), noteInput func(), err error) {
	rel, err := m.trackPresence(ctx, wsID, session, true)
	if err != nil {
		return nil, nil, err
	}
	pending := make(chan struct{}, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-pending:
				// 共有ウォーターマーク（last_seen_at）。force=false なので 5 秒に 1 回へ
				// 畳まれ、大半の周回はロックとマップ参照だけで返る。
				ctxHB, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_ = m.recordWorkspaceActivity(ctxHB, wsID, false)
				cancel()
			}
		}
	}()
	return func() {
			close(stop)
			<-done
			rel()
		}, func() {
			m.conns.noteInput(wsID)
			select {
			case pending <- struct{}{}:
			default: // 書き込みが既に予約済み — 打鍵は待たない
			}
		}, nil
}

func (m *manager) trackPresence(ctx context.Context, wsID, session string, attention bool) (func(), error) {
	m.conns.addConn(wsID, session, attention)
	if err := m.recordWorkspaceActivity(ctx, wsID, true); err != nil {
		m.conns.doneConn(wsID, session, attention)
		return nil, err
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(workspacePresenceHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// 打鍵が途絶えた端末は在席を名乗るのをやめる。ticker は止めない —
				// また打ち始めたら次の tick から lease が復活する。
				if attention && !m.conns.attentionFresh(wsID, presenceGrace, time.Now()) {
					continue
				}
				hbCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				if err := m.recordWorkspaceActivity(hbCtx, wsID, true); err != nil {
					log.Printf("workspace presence: %s: %v", wsID, err)
				}
				cancel()
			case <-stop:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
			m.conns.doneConn(wsID, session, attention)
			flushCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = m.recordWorkspaceActivity(flushCtx, wsID, true)
			cancel()
		})
	}, nil
}

// reaper owns the sweep loop. sessionDef/wsDef are the deployment-default
// timeouts; per-tenant overrides come from tenantLimits.
type reaper struct {
	mgr        *manager
	interval   time.Duration
	sessionDef time.Duration
	// interactionDef は「人の判断待ちで止まっているセッション」用の既定（docs/75 §75.5）。
	// idle と分けるのは、答えが返るまでコンテナを起こし続けるのが費用そのものだから。
	interactionDef time.Duration
	wsDef          time.Duration
	hibDef         time.Duration
	backupDef      time.Duration
	bootTime       time.Time

	// idleSince tracks, per live claude session, when it was first observed
	// idle-and-unattached. Reset when it goes busy/attached/away. Reaper is the
	// only writer (single goroutine) so no lock needed.
	idleSince map[string]time.Time // workspaceID|session -> first idle
}

func newReaper(mgr *manager, interval, sessionDef, interactionDef, wsDef, hibDef, backupDef time.Duration) *reaper {
	return &reaper{
		mgr:            mgr,
		interval:       interval,
		sessionDef:     sessionDef,
		interactionDef: interactionDef,
		wsDef:          wsDef,
		hibDef:         hibDef,
		backupDef:      backupDef,
		bootTime:       time.Now(),
		idleSince:      map[string]time.Time{},
	}
}

func (rp *reaper) run(ctx context.Context) {
	log.Printf("idle-stop reaper: interval=%s session_default=%s interaction_default=%s ws_default=%s hibernate_default=%s backup_default=%s",
		rp.interval, rp.sessionDef, rp.interactionDef, rp.wsDef, rp.hibDef, rp.backupDef)
	t := time.NewTicker(rp.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rp.sweep(ctx)
		}
	}
}

// sweep runs one pass over every running workspace, applying both tiers.
func (rp *reaper) sweep(ctx context.Context) {
	tenants, err := rp.mgr.store.ListTenants(ctx)
	if err != nil {
		log.Printf("idle-stop: list tenants: %v", err)
		return
	}
	live := map[string]bool{} // sessions seen this pass, to prune idleSince
	for _, t := range tenants {
		lim := parseLimits(t.Limits)
		var cl tierClocks
		cl.session, cl.sessionOn = idleTimeout(lim.SessionIdleTimeout, rp.sessionDef)
		cl.interaction, cl.interactionOn = interactionTimeout(lim, cl.session, cl.sessionOn, rp.interactionDef)
		cl.ws, cl.wsOn = idleTimeout(lim.WSIdleTimeout, rp.wsDef)
		cl.hibernate, cl.hibernateOn = idleTimeout(lim.HomeHibernateAfter, rp.hibDef)
		cl.backup, cl.backupOn = idleTimeout(lim.HomeBackupEvery, rp.backupDef)
		if !cl.anyOn() {
			continue // nothing enabled for this tenant
		}
		wss, err := rp.mgr.store.ListWorkspaces(ctx, t.ID)
		if err != nil {
			log.Printf("idle-stop: list workspaces (%s): %v", t.Slug, err)
			continue
		}
		for _, ws := range wss {
			rp.sweepWorkspace(ctx, ws, cl, live)
		}
	}
	// Drop trackers for sessions that no longer exist so a resumed session
	// starts its idle clock fresh.
	for k := range rp.idleSince {
		if !live[k] {
			delete(rp.idleSince, k)
		}
	}
}

// tierClocks は 1 テナントぶんの解決済みタイムアウト。5 つの (duration, enabled) を位置引数で
// 回すと、段を足すたび全呼び出しを直すことになり、しかも取り違えても型は通る。
type tierClocks struct {
	session     time.Duration // tier1: ターンが終わっただけの idle
	interaction time.Duration // tier1: 人の判断待ち（docs/75 §75.5）
	ws          time.Duration // tier2
	hibernate   time.Duration // tier3
	backup      time.Duration // tier4（唯一「アイドル」ではない — 周期）

	sessionOn     bool
	interactionOn bool
	wsOn          bool
	hibernateOn   bool
	backupOn      bool
}

// tier1For は 1 セッションに当てる時計を分類から選ぶ。畳んでよくないものは on=false。
func (c tierClocks) tier1For(s sessionWire) (time.Duration, bool) {
	switch sessionActivity(s) {
	case activityIdleWait:
		return c.session, c.sessionOn
	case activityHumanWait:
		return c.interaction, c.interactionOn
	}
	return 0, false
}

func (c tierClocks) anyOn() bool {
	return c.sessionOn || c.interactionOn || c.wsOn || c.hibernateOn || c.backupOn
}

func (rp *reaper) sweepWorkspace(ctx context.Context, ws Workspace, cl tierClocks, live map[string]bool) {
	rt := rp.mgr.runtimeFor(ws, "") // secretKey unused for read/halt calls
	// Tier 4 first, and outside every state test below: a backup is not about whether
	// anybody is using this workspace. It takes no locks and changes nothing — a snapshot
	// of a volume is invisible to the volume — so there is no fence to wait behind either.
	if cl.backupOn {
		rp.backupHome(ctx, rt, ws, cl.backup)
	}
	// Only a "running" workspace is swept by tiers 1–2. "starting" (ECS cold pull) is
	// deliberately left alone — idle-stopping a workspace that is still converging would
	// cancel a legitimate launch; its idle clock starts once it actually runs.
	state := rt.State(ctx)
	if state != "running" {
		// Tier 3 is the mirror image: it only ever looks at a workspace that is ALREADY
		// stopped, which is why it lives on this side of the return. "starting" and "none"
		// are excluded on purpose — the first is a launch in flight, the second has no home
		// to put away (it may already be a snapshot).
		if cl.hibernateOn && state == "stopped" {
			rp.hibernateHome(ctx, rt, ws, cl.hibernate)
		}
		return
	}
	now := time.Now()

	// Ask the Agent for the live session list once; drives both tiers.
	env, err := rp.mgr.agentSessionsEnv(ctx, rt)
	if err != nil {
		// Agent unreachable (starting/unhealthy) — leave it be this pass.
		return
	}
	sessions := env.Sessions
	// busy = この Workspace を起こし続ける理由があるか。判定は sessionActivity
	// （session_activity.go）に集約してある — 状態がどちらに属するかを reaper の中の
	// インライン式で決めていた頃は、状態が増えるたび 2 箇所を手で合わせる必要があり、
	// 実際 blocked / limited / spend_limit で 2 回ドリフトした。
	// 取り込み中（docs/78）はセッションが 1 つも無くても仕事中。ここを見落とすと、
	// 1 時間かかる clone / checkout の途中で Workspace が止まり、作業コピーは半端なまま
	// 残る（中断そのものは検出できるが、失われた時間は戻らない）。
	busy := env.RepoJobs > 0
	for _, s := range sessions {
		if holdsWorkspace(s) {
			busy = true
		}
		// 畳むまでの時間は分類で変わる: ターンが終わっただけの idle は
		// session_idle_timeout、人の判断待ちは interaction_idle_timeout。テナントは
		// 「質問は早く畳んで安くしたい／うちは数時間待ってほしい」を独立に決められる。
		to, on := cl.tier1For(s)
		// kind の門（docs/75 P5）: shell / ssm は halt が「走っているジョブを殺す」意味に
		// なるので tier1 の対象にしない（p3-9 からの割り切り。守りたいときは
		// 「自動停止しない」ピン）。それ以外＝エージェントのセッションは、claude も
		// managed（codex / opencode / …）も halt が resumable なので畳んでよい。
		if !on || !tier1Foldable(s.Kind) || !tier1Reapable(s) {
			continue
		}
		key := ws.ID + "|" + s.Name
		live[key] = true
		if rp.mgr.conns.isAttached(ws.ID, s.Name) {
			delete(rp.idleSince, key) // someone is watching it
			continue
		}
		since, ok := rp.idleSince[key]
		if !ok {
			rp.idleSince[key] = now
			continue
		}
		if now.Sub(since) >= to {
			rp.haltSession(ctx, rt, ws, s.Name)
			delete(rp.idleSince, key)
		}
	}

	// Tier 2: stop the whole workspace once it is fully cold.
	_, lastSeen, seen := rp.mgr.conns.snapshot(ws.ID)
	// 在席は「接続の有無」ではなく「直近に人が触ったか」で見る（docs/75 P3）。
	watched := rp.mgr.conns.watched(ws.ID, presenceGrace, now)
	base := rp.idleBase(seen, lastSeen, ws.LastActiveAt)
	// ★観測をそのまま公開する（docs/75 P4）。管理画面はこれを読むだけで判定をやり直さない
	// ので、「なぜ止まらないか」を調べる画面が reaper と別の答えを出すことがない。
	// wsOn が false のときも記録する — 「予定なし」と「機能が切ってある」は別物。
	rp.mgr.putIdleForecast(ws.ID, idleForecast{
		Enabled:    cl.wsOn,
		StopAt:     base.Add(cl.ws),
		Holders:    holdersOf(sessions, watched, now, env.RepoJobs),
		ObservedAt: now,
	})
	if !cl.wsOn {
		return
	}
	if watched || busy {
		return // being watched or actively working
	}
	if now.Sub(base) >= cl.ws {
		rp.stopWorkspace(ctx, rt, ws, cl.ws)
	}
}

// idleBase is the tier-2 idle clock's start: the LATEST of the three activity
// signals — the reaper boot time (a CP restart grants a fresh grace window
// instead of reaping everything that looks stale), the in-memory last request
// (proxy/preview/chat traffic), and the DB last_active_at (bumped on every
// explicit start/stop). All three are considered unconditionally: an earlier
// version only consulted the DB when there was NO in-memory record, so a stale
// in-memory lastSeen (from an old terminal, hours ago) masked the fresh
// last_active_at written by a just-issued Start — and the reaper stopped the
// workspace within one sweep of it coming up (a manual start looked "cold").
func (rp *reaper) idleBase(seen bool, lastSeen time.Time, dbLastActive string) time.Time {
	base := rp.bootTime
	if seen && lastSeen.After(base) {
		base = lastSeen
	}
	if dbTS, err := time.Parse(time.RFC3339, dbLastActive); err == nil && dbTS.After(base) {
		base = dbTS
	}
	return base
}

// haltSession halts one idle claude session (Agent POST /sessions/{name}/halt).
func (rp *reaper) haltSession(ctx context.Context, rt Runtime, ws Workspace, name string) {
	req, _ := http.NewRequestWithContext(ctx, "POST", rt.Endpoint()+"/sessions/"+url.PathEscape(name)+"/halt", nil)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		log.Printf("idle-stop: halt %s/%s: %v", ws.ContainerName, name, err)
		return
	}
	_ = resp.Body.Close()
	log.Printf("idle-stop: halted idle claude session %s in %s (tenant %s)", name, ws.ContainerName, ws.TenantID)
}

// stopWorkspace stops a cold workspace through the same distributed lifecycle
// fence as explicit/admin stops. The idle reaper runs independently in every CP
// replica, so a direct Runtime.Stop could otherwise cross an approved shared
// operation or another holder's recreate/clean/start.
func (rp *reaper) stopWorkspace(ctx context.Context, rt Runtime, ws Workspace, wsTO time.Duration) {
	lock := rp.mgr.startLockFor(ws.ID)
	lock.Lock()
	defer lock.Unlock()
	lease, err := acquireWorkspaceLifecycleLease(ctx, rp.mgr.store, ws.MembershipID)
	if err != nil {
		log.Printf("idle-stop: lifecycle busy %s: %v", ws.ContainerName, err)
		return
	}
	defer lease.Close()
	releaseFence, err := rp.mgr.acquireWorkspaceOperationFence(lease.Context(), ws.ID, rt)
	if err != nil {
		log.Printf("idle-stop: runtime fence %s: %v", ws.ContainerName, err)
		return
	}
	defer releaseFence()
	if err := lease.checkpoint(ctx); err != nil {
		log.Printf("idle-stop: lifecycle lost %s: %v", ws.ContainerName, err)
		return
	}
	// A start/recreate may have won the local lock while this sweep was waiting.
	// Never turn its non-running transitional state into a fresh Stop.
	if rt.State(lease.Context()) != "running" {
		return
	}
	// The initial idle decision was made before potentially waiting on three
	// fences. Re-read every activity signal while we own them; otherwise a proxy
	// reconnect or a session becoming busy during that wait would be stopped by a
	// stale sweep decision.
	freshWS, ok, err := rp.mgr.store.GetWorkspaceByMembership(lease.Context(), ws.MembershipID)
	if err != nil || !ok {
		log.Printf("idle-stop: refresh workspace %s: found=%v err=%v", ws.ContainerName, ok, err)
		return
	}
	sessions, err := rp.mgr.agentSessions(lease.Context(), rt)
	if err != nil {
		log.Printf("idle-stop: refresh sessions %s: %v", ws.ContainerName, err)
		return
	}
	for _, s := range sessions {
		if holdsWorkspace(s) {
			return
		}
	}
	_, lastSeen, seen := rp.mgr.conns.snapshot(ws.ID)
	if rp.mgr.conns.watched(ws.ID, presenceGrace, time.Now()) ||
		time.Since(rp.idleBase(seen, lastSeen, freshWS.LastActiveAt)) < wsTO {
		return
	}
	checkNow := time.Now().UTC()
	recent, err := rp.mgr.store.WorkspaceHasRecentActivity(lease.Context(), ws.ID,
		leaseTS(checkNow.Add(-wsTO)), leaseTS(checkNow))
	if err != nil {
		log.Printf("idle-stop: shared activity %s: %v", ws.ContainerName, err)
		return
	}
	if recent {
		return
	}
	claimed, err := rp.mgr.store.ClaimWorkspaceIdleStop(lease.Context(), ws.ID, ws.MembershipID,
		lease.token, leaseTS(checkNow.Add(-wsTO)), leaseTS(checkNow))
	if err != nil || !claimed {
		if err != nil {
			log.Printf("idle-stop: claim %s: %v", ws.ContainerName, err)
		}
		return
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = rp.mgr.store.ReleaseWorkspaceIdleStop(releaseCtx, ws.ID, lease.token)
		cancel()
	}()
	// A CP can be paused after the intent commit. Revalidate ownership before the
	// irreversible Runtime call so an expired old holder cannot resume into Stop.
	if err := lease.checkpoint(ctx); err != nil {
		log.Printf("idle-stop: lifecycle lost after claim %s: %v", ws.ContainerName, err)
		return
	}
	// 止める前にアウトボックスを吸い出す（docs/75）。Agent の通知は Console が見に来た
	// ときにしか drain されないので、ここで拾っておかないと「未回答のまま停止しました」が
	// 次に Workspace を起こすまで誰にも届かない — 費用のために止めた結果、止めたことを
	// 知らせる通知だけが止めたせいで消える。失敗しても停止は続ける（通知は次回拾える）。
	drainCtx, cancelDrain := context.WithTimeout(lease.Context(), 5*time.Second)
	drainAgentOutbox(drainCtx, rp.mgr.store, rt, ws.MembershipID)
	cancelDrain()
	if err := rt.Stop(lease.Context()); err != nil {
		log.Printf("idle-stop: stop %s: %v", ws.ContainerName, err)
		return
	}
	if err := lease.checkpoint(ctx); err != nil {
		log.Printf("idle-stop: lifecycle lost after stop %s: %v", ws.ContainerName, err)
		return
	}
	if err := rp.mgr.store.SetWorkspaceState(ctx, ws.ID, "stopped"); err != nil {
		log.Printf("idle-stop: mark stopped %s: %v", ws.ContainerName, err)
	}
	log.Printf("idle-stop: stopped cold workspace %s (tenant %s)", ws.ContainerName, ws.TenantID)
}

// hibernatingRuntime is the optional capability behind tier 3: a runtime that can park a
// stopped workspace's home somewhere cheaper and bring it back on the next Start. Only
// ecs-ec2 implements it (snapshot the EBS home, delete the volume — ADR 0045 決定 4);
// every other runtime keeps the home where it is, so tier 3 is simply absent for them
// rather than half-working.
//
// The trigger lives HERE, in the reaper, and not in the ecs-ec2 sweeper, because the
// answer to "how long before this tenant's homes go to sleep" is in the database and the
// sweeper deliberately has no view of it — it derives its whole world from EC2 tags
// (ADR 0012). BeginHibernate is one STEP: it starts the capture. Advancing and finishing
// it stays with the pool sweeper, which is what makes the operation resumable across a CP
// restart (docs/64 §64.18.2.1).
type hibernatingRuntime interface {
	BeginHibernate(ctx context.Context) error
}

// hibernateHome is tier 3. It runs under the same three fences as tier 2, because
// hibernation releases the slot and starts a snapshot — a Start that wins the race while
// this is deciding must not find its home being taken apart underneath it.
func (rp *reaper) hibernateHome(ctx context.Context, rt Runtime, ws Workspace, hibTO time.Duration) {
	h, ok := rt.(hibernatingRuntime)
	if !ok {
		return // this runtime has nowhere cheaper to put a home
	}
	if !rp.homeIdleFor(ws.ID, ws.LastActiveAt, hibTO) {
		return
	}
	lock := rp.mgr.startLockFor(ws.ID)
	lock.Lock()
	defer lock.Unlock()
	lease, err := acquireWorkspaceLifecycleLease(ctx, rp.mgr.store, ws.MembershipID)
	if err != nil {
		return // someone else owns the lifecycle; the next sweep tries again
	}
	defer lease.Close()
	releaseFence, err := rp.mgr.acquireWorkspaceOperationFence(lease.Context(), ws.ID, rt)
	if err != nil {
		return
	}
	defer releaseFence()
	if err := lease.checkpoint(ctx); err != nil {
		return
	}
	// Re-decide while holding the fences: everything below was read before we waited on
	// them, and the owner coming back in that window is exactly the case this protects.
	if rt.State(lease.Context()) != "stopped" {
		return
	}
	freshWS, ok2, err := rp.mgr.store.GetWorkspaceByMembership(lease.Context(), ws.MembershipID)
	if err != nil || !ok2 {
		return
	}
	if !rp.homeIdleFor(ws.ID, freshWS.LastActiveAt, hibTO) {
		return
	}
	if err := h.BeginHibernate(lease.Context()); err != nil {
		log.Printf("idle-stop: hibernating the home of %s: %v", ws.ContainerName, err)
	}
}

// backingUpRuntime is tier 4's capability: a runtime whose home lives in one place that
// can be lost on its own. Only ecs-ec2 has that shape — an EBS volume is pinned to a
// single Availability Zone and cannot be evacuated — so only it implements this. On
// docker and native the home is on the host it is already on, and on Fargate it is EFS,
// which is regional to begin with.
type backingUpRuntime interface {
	BackupHome(ctx context.Context, every time.Duration) error
}

// backupHome is tier 4. It is the one tier that takes no locks: a snapshot does not touch
// the volume, so there is nothing for a Start, a Stop or a hibernation to collide with,
// and whether one is due is decided from AWS rather than from anything the reaper
// remembers. Two CP replicas can therefore both fire in the same window; the cost is one
// extra incremental copy, which the retention then drops.
func (rp *reaper) backupHome(ctx context.Context, rt Runtime, ws Workspace, every time.Duration) {
	b, ok := rt.(backingUpRuntime)
	if !ok {
		return // this runtime's home is not pinned to one AZ
	}
	if err := b.BackupHome(ctx, every); err != nil {
		log.Printf("idle-stop: backing up the home of %s: %v", ws.ContainerName, err)
	}
}

// homeIdleFor answers "has nobody opened this workspace for hibTO". Unlike idleBase it
// does NOT consider rp.bootTime: a hibernation window is days or weeks, so a CP that
// restarts more often than that would push the deadline out forever and the setting would
// look enabled while never firing once. Only the persisted last_active_at can carry a
// clock that long — and an unreadable one therefore means "leave it alone", never
// "hibernate it now".
func (rp *reaper) homeIdleFor(wsID, dbLastActive string, hibTO time.Duration) bool {
	last, err := time.Parse(time.RFC3339, dbLastActive)
	if err != nil {
		return false
	}
	if _, lastSeen, seen := rp.mgr.conns.snapshot(wsID); seen && lastSeen.After(last) {
		last = lastSeen
	}
	return time.Since(last) >= hibTO
}

// tier1 / tier2 が見る述語は session_activity.go（sessionActivity / holdsWorkspace /
// tier1Reapable）に移した。ここに reapableIdle があった頃の集合は tier1Reapable が
// そのまま引き継いでいる。
