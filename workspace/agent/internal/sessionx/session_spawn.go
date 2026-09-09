package sessionx

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Session-spawned sessions (ADR 0073): the refusals that apply to a create whose origin is
// another SESSION rather than a person, an operator conversation or a schedule.
//
// They live here — in the Agent, next to the create — rather than in the MCP tool that calls
// it, for the same reason the peer envelope and its rate limit live in the Agent: the tool is a
// thin layer, and an invariant enforced only there is an invariant anyone can walk around by
// replacing that layer. The checks are all conditioned on origin=session, so no other creation
// route (Console, operator, schedule, fork, recreate) can reach them.

// spawnInflight counts session-spawned creates that have reserved a slot but have not yet
// written their meta. Without it two creates that differ in content (so the idempotency ledger
// does not serialize them — it keys on one intent) can both count the same "two existing" and
// land a fourth child.
var spawnInflight = struct {
	mu sync.Mutex
	n  map[string]int
}{n: map[string]int{}}

// countChildren returns how many sessions this parent CREATED and still hold a slot.
//
// Both halves of the predicate are meant. origin_session alone would also count a fork of a
// child (which keeps the lineage but is origin=handoff), and a fork is a person's action in the
// Console — charging it to the parent's budget would let the user's own fork be the reason the
// parent may not spawn. Nothing escapes through that gap: a fork of a child cannot spawn either
// (the depth rule reads the lineage, deliberately the wider predicate).
//
// ARCHIVED children are NOT counted (ADR 0073 decision 6, amended 2026-09-09). They were, on
// the reasoning that archiving is reversible and freeing its slot would allow "fold up, spawn a
// replacement, restore". What that missed is that nothing ever takes the slot back: the stopped
// TTL prune skips archived metas outright (HandleListSessions), so an archived child holds a
// slot FOREVER and a parent that tidied three of them can never spawn again — the user who
// cleans up is punished hardest. Archiving is a Console-only action a session cannot perform
// (decision 13), which is the same basis on which forks and recreates are kept out of this
// count: a person's action must not spend a session's budget.
//
// What still frees a slot: deleting the meta, archiving it, and — for a child left stopped —
// session.StoppedTTL expiring, which prunes the meta on the next listing.
func countChildren(parent string) int {
	n := 0
	for _, m := range session.ListMetas() {
		if m.Archived {
			continue
		}
		if m.OriginSession == parent && session.OriginOf(m) == session.OriginSession {
			n++
		}
	}
	return n
}

// reserveSpawnSlot claims one of parent's child slots, or reports why it cannot.
//
// ⚠️ Release it the moment the child's META EXISTS, not when the create finishes. From the
// write onwards countChildren sees the child, so holding the reservation as well counts it
// twice: a parent with one child and one launch in flight reads as three, and the next
// legitimate create is refused against a fleet that does not exist yet. The launch is the slow
// part (tmux, a worktree), so that window is not small.
func reserveSpawnSlot(parent string) error {
	spawnInflight.mu.Lock()
	defer spawnInflight.mu.Unlock()
	// Read the limit here, not once at startup: it is a user setting now (Settings > Agents >
	// Session), and a refusal quoting yesterday's number is the invisible limit ADR 0073
	// decision 6 refuses to have.
	limit := session.SpawnChildLimit()
	if have := countChildren(parent) + spawnInflight.n[parent]; have >= limit {
		return fmt.Errorf("このセッションは既に子セッションを %d 本持っています（上限 %d）。"+
			"list_child_sessions で状態を確かめ、不要な子は利用者に Console での削除・アーカイブを頼んでください"+
			"（停止したままの子は %s で自動的に枠が空きます）",
			have, limit, stoppedTTLPhrase())
	}
	spawnInflight.n[parent]++
	return nil
}

// stoppedTTLPhrase renders session.StoppedTTL for the refusal above. The figure is spelled out
// because ADR 0073 refuses to have invisible limits, and read from the setting rather than
// written as "7 days" because AF_SESSION_STOPPED_TTL moves it (and the tests set it to
// minutes).
func stoppedTTLPhrase() string {
	d := session.StoppedTTL()
	if h := d.Hours(); h >= 24 {
		return fmt.Sprintf("%d 日", int(h/24))
	}
	return d.String()
}

// spawnSlot is one create's claim on a child slot. Zero parent = this create is not a spawn, and
// every method is a plain meta write with no bookkeeping, so the create path reads the same
// either way.
type spawnSlot struct {
	parent string
	done   bool
}

// publish writes the child's meta and hands the slot back **in one critical section**.
//
// The two have to be atomic together. reserveSpawnSlot counts metas and in-flight creates under
// this same lock, so a concurrent create must never see an in-between state: meta written but
// slot still held counts one child twice (a parent under the limit is refused), and slot freed
// but meta not yet written counts it zero (the limit is exceeded). Writing then releasing as two
// steps only narrows that window — it does not close it.
func (s *spawnSlot) publish(m session.Meta) {
	if s.parent == "" {
		session.WriteMeta(m)
		return
	}
	spawnInflight.mu.Lock()
	defer spawnInflight.mu.Unlock()
	session.WriteMeta(m)
	s.releaseLocked()
}

// release is the failure net: every path that ends without a meta. Idempotent, because the
// create defers it AND publish has usually already done it — releasing twice would free a slot
// the child now occupies, and the limit would drift upwards one create at a time.
func (s *spawnSlot) release() {
	if s.parent == "" {
		return
	}
	spawnInflight.mu.Lock()
	defer spawnInflight.mu.Unlock()
	s.releaseLocked()
}

func (s *spawnSlot) releaseLocked() {
	if s.done {
		return
	}
	s.done = true
	if spawnInflight.n[s.parent] <= 1 {
		delete(spawnInflight.n, s.parent)
		return
	}
	spawnInflight.n[s.parent]--
}

// handOverSpawnLineage moves a spawned child's lineage from the archived predecessor of a
// recreate onto the successor that replaced it.
//
// Recreate keeps the old meta (archived, restorable) and mints a new one, and both inherit the
// parent — so without this ONE child costs the parent TWO slots, and a user recreating their
// own child is the reason the parent may not spawn again. That is the same objection that keeps
// forks out of the count (countChildren): a person's action must not spend a session's budget.
//
// Called only after the successor has actually launched. The failure paths un-archive the old
// session, and clearing the lineage before knowing which of the two survives would leave a
// restored child that its parent can no longer steer — and that could spawn, because the
// recursion limit reads this same field.
//
// Origin stays `session`: the accounting axis (unattended or human-opened) is unchanged, and it
// is what usage rows bake in. What is dropped is the answer to "whose child was it" for the
// superseded identity.
//
// ⚠️ That identity is archived, not gone — the user can restore it, and it comes back with
// origin=session and NO lineage, which means it may spawn (the recursion limit reads this same
// field). Reaching that takes two deliberate Console actions, recreate and restore, neither of
// which is an MCP tool, so ADR 0073 decision 5 ("one generation between human launches") still
// holds. It is recorded in decision 6 rather than defended against here: defending would mean
// keeping the slot charged to a session the user replaced on purpose.
func handOverSpawnLineage(name string) {
	// Re-read rather than writing back the copy the caller has held since before the launch:
	// the recreate archived it seconds ago and anything that touched it in between would be
	// undone by writing a stale snapshot.
	old, ok := session.ReadMeta(name)
	// Only a lineage that COSTS a slot is handed over. A session a person launched from a
	// handoff proposal also carries origin_session (origin=user), and it holds no slot — for
	// that one this would be pure loss: the superseded identity would forget who proposed it
	// and nothing would be freed.
	if !ok || !session.InUnattendedChain(old) {
		return
	}
	old.OriginSession = ""
	session.WriteMeta(old)
}

// forkLineage is the OriginSession a fork of src inherits (ADR 0073 decision 1).
//
// Forking a CHILD keeps the lineage on purpose: the fork is stamped origin=handoff, so without
// it the successor would leave the unattended chain and be able to spawn — the exact gap the
// depth rule's wider predicate exists to close.
//
// It stops at the second producer. A session a person launched from a handoff proposal is
// origin=user WITH a lineage, and inheriting unconditionally turns its fork into
// origin=handoff + lineage — which reads as unattended and silently takes away a capability
// the fork's own source has. src's origin is what separates the two cases, so ask the same
// question the depth rule asks.
func forkLineage(src session.Meta) string {
	if !session.InUnattendedChain(src) {
		return ""
	}
	return src.OriginSession
}

// SpawnRefusal is a create that origin=session may not perform. Code is the wire error code,
// Status the HTTP status; both are surfaced to the calling model verbatim, so the message says
// what to do instead rather than only what went wrong.
type SpawnRefusal struct {
	Status  int
	Code    string
	Message string
}

// spawnDepthRefusal implements the recursion limit: a session that was itself started by a
// session may not start one.
//
// The predicate is session.InUnattendedChain — deliberately wider than the steering gate's
// origin==session (ADR 0073 decisions 4 and 5), because forking a child stamps origin=handoff
// while keeping the lineage and a rule reading origin alone would let the fork spawn. It is
// NOT "OriginSession is set": origin_session also records which session's handoff PROPOSAL a
// person launched from, and refusing there would take capability away from a session a human
// opened. Go through the named predicate — the bare field test reads as right and is not.
//
// One lookup, no walking: the answer is on the caller's own meta, so it cannot break when a
// session in the middle of the chain has been deleted. What it does not bound is a chain that
// passes through a human — a child may propose a handoff, and a session the user then launches
// from the Console is origin=user and may spawn again. That is the intended re-entry, and the
// lineage it now carries does not close it: what a person opened is a person's launch, whoever
// suggested the work.
func spawnDepthRefusal(parent string) *SpawnRefusal {
	m, ok := session.ReadMeta(parent)
	if !ok {
		// The caller named itself and no meta answers to it. Refuse rather than treat an
		// unknown caller as a root: this is the one branch where guessing grants capability.
		return &SpawnRefusal{Status: 409, Code: "spawn_unknown_parent",
			Message: fmt.Sprintf("起動元のセッション %q が見つかりません", parent)}
	}
	if session.InUnattendedChain(m) {
		return &SpawnRefusal{Status: 409, Code: "spawn_depth",
			Message: fmt.Sprintf("このセッション自身が %s に起こされた子なので、さらに子は起こせません。"+
				"作業を分けたいときは propose_session_handoff で利用者に引き継ぎを提案してください", m.OriginSession)}
	}
	return nil
}

// sessionAliveFn is SessionAlive behind a seam: liveness means "a tmux session or a managed
// runtime handle exists", neither of which a unit test can stage.
var sessionAliveFn = SessionAlive

// workingCopyKey is the identity a working copy is compared BY. Two spellings that reach the
// same directory have to produce the same key or the check below is decorative: one checkout
// can be named `/home/dev/repos/app`, a symlink to it, or a path with `..` in it.
//
// EvalSymlinks is the part that matters and the part that can fail (a path that does not exist,
// a broken link). On failure the cleaned absolute path is still a better key than the raw
// string, so it degrades rather than giving up — giving up would mean "no session is here".
func workingCopyKey(dir string) string {
	abs, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return filepath.Clean(dir)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// spawnWorkingCopyRefusal keeps two agents out of one working copy.
//
// ⚠️ Call it with the RESOLVED dir — the one the session will actually run in — never the raw
// request. The create turns an empty dir into home and joins a relative one onto home, so
// checking the request as sent lets `dir: ""` walk past a session already running in home.
//
// The comparison is the working copy — dir — and subdir takes no part in it: one session at the
// root and another under console/ still share a checkout, an index and a branch, which is the
// accident this refuses. Stopped sessions do not count; what is guarded here is two processes
// running at once, not a quota (that is SpawnChildLimit, which counts the other way round).
func spawnWorkingCopyRefusal(dir string) *SpawnRefusal {
	target := workingCopyKey(dir)
	for _, m := range session.ListMetas() {
		if m.Archived || m.Dir == "" {
			continue
		}
		if workingCopyKey(m.Dir) != target || !sessionAliveFn(m) {
			continue
		}
		return &SpawnRefusal{Status: 409, Code: "spawn_working_copy_busy",
			Message: fmt.Sprintf("%s では既にセッション %s が動いています。"+
				"worktree=true（既定）で起こすか、別の作業コピーを指定してください", m.Dir, m.Name)}
	}
	return nil
}

// spawnKindRefusal refuses raw shells (ADR 0073 decision 8, the reasoning of ADR 0041
// decision 5). Starting a shell is arbitrary command execution, and the approval gate the
// operator's create has for exactly this is a no-op without a conversation — so on this surface
// it is not a weaker gate, it is no gate.
func spawnKindRefusal(kind string) *SpawnRefusal {
	switch NormalizeKind(kind) {
	case session.KindShell, session.KindSSM:
		return &SpawnRefusal{Status: 400, Code: "spawn_kind_refused",
			Message: "セッションからは shell / ssm のセッションを起こせません（任意コマンド実行になるため）。" +
				"利用者に Console から起動してもらってください"}
	}
	return nil
}

// SpawnEnvelope builds the line placed at the head of a session-spawned child's first
// instruction (ADR 0073 decision 14).
//
// Without it the child cannot tell its parent's instruction from one its user typed: delivery
// is typing into the TUI, and with report_to empty nothing else on that turn says where it came
// from. The convention is peerEnvelope's, one axis over — `from=` names the session, and the
// standing rules for receiving one live in workspace-notes.md. reply= is not part of it: a
// child answers through the report-back line the tool appends, or not at all.
//
// Built here, like the peer envelope, so that a caller cannot omit it or forge another
// session's name by replacing the thin MCP layer.
func SpawnEnvelope(parent, prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || !session.ValidName(parent) {
		return prompt
	}
	return "[agent-fleet:spawn from=" + parent + "] " + prompt
}

// SpawnCreateRefusal runs the origin=session refusals that depend only on the REQUEST: who is
// asking and what kind of session it wants.
//
// The working-copy refusal is deliberately not among them. It needs the resolved working
// directory, which the create only knows further down (an empty dir becomes home, a relative
// one is joined onto home, a worktree launch replaces it outright), so it is called separately
// at that point. Folding it in here would mean checking a path nobody will run in.
func SpawnCreateRefusal(parent, kind string) *SpawnRefusal {
	if r := spawnDepthRefusal(parent); r != nil {
		return r
	}
	return spawnKindRefusal(kind)
}
