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

// countChildren returns how many sessions name parent as the one that started them.
//
// Archived children are counted. Archiving hides a session from the active list but keeps it
// restorable, so treating it as a freed slot would make the limit meaningless: fold up, spawn a
// replacement, restore. Only deleting the meta frees a slot, which is also the only operation
// that makes the child stop existing in any sense the user can undo.
func countChildren(parent string) int {
	n := 0
	for _, m := range session.ListMetas() {
		if m.OriginSession == parent && session.OriginOf(m) == session.OriginSession {
			n++
		}
	}
	return n
}

// reserveSpawnSlot claims one of parent's child slots, or reports why it cannot. The caller
// must release it once the create has either written its meta or failed.
func reserveSpawnSlot(parent string) error {
	spawnInflight.mu.Lock()
	defer spawnInflight.mu.Unlock()
	if have := countChildren(parent) + spawnInflight.n[parent]; have >= session.SpawnChildLimit {
		return fmt.Errorf("このセッションは既に子セッションを %d 本持っています（上限 %d）。"+
			"不要な子を Console で削除してから起こしてください（停止やアーカイブでは枠は空きません）",
			have, session.SpawnChildLimit)
	}
	spawnInflight.n[parent]++
	return nil
}

func releaseSpawnSlot(parent string) {
	spawnInflight.mu.Lock()
	defer spawnInflight.mu.Unlock()
	if spawnInflight.n[parent] <= 1 {
		delete(spawnInflight.n, parent)
		return
	}
	spawnInflight.n[parent]--
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
// The predicate is "OriginSession is set", NOT "origin == session" — deliberately wider than
// the steering gate's (ADR 0073 decisions 4 and 5). Forking a child stamps origin=handoff while
// keeping the lineage, so a rule reading origin alone would let the fork spawn.
//
// One lookup, no walking: the answer is on the caller's own meta, so it cannot break when a
// session in the middle of the chain has been deleted. What it does not bound is a chain that
// passes through a human — a child may propose a handoff, and a session the user then launches
// from the Console is origin=user with no lineage and may spawn again. That is the intended
// re-entry, not a hole: closing it would mean stamping a false provenance onto a session a
// person opened.
func spawnDepthRefusal(parent string) *SpawnRefusal {
	m, ok := session.ReadMeta(parent)
	if !ok {
		// The caller named itself and no meta answers to it. Refuse rather than treat an
		// unknown caller as a root: this is the one branch where guessing grants capability.
		return &SpawnRefusal{Status: 409, Code: "spawn_unknown_parent",
			Message: fmt.Sprintf("起動元のセッション %q が見つかりません", parent)}
	}
	if m.OriginSession != "" {
		return &SpawnRefusal{Status: 409, Code: "spawn_depth",
			Message: fmt.Sprintf("このセッション自身が %s に起こされた子なので、さらに子は起こせません。"+
				"作業を分けたいときは propose_session_handoff で利用者に引き継ぎを提案してください", m.OriginSession)}
	}
	return nil
}

// spawnWorkingCopyRefusal keeps two agents out of one working copy.
//
// The comparison is the working copy — dir — and subdir takes no part in it: one session at the
// root and another under console/ still share a checkout, an index and a branch, which is the
// accident this refuses. Stopped sessions do not count; what is guarded here is two processes
// running at once, not a quota (that is SpawnChildLimit, which counts the other way round).
// sessionAliveFn is SessionAlive behind a seam: liveness means "a tmux session or a managed
// runtime handle exists", neither of which a unit test can stage.
var sessionAliveFn = SessionAlive

func spawnWorkingCopyRefusal(dir string) *SpawnRefusal {
	target, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return nil // an unreadable path fails later, in the create itself, with a better message
	}
	for _, m := range session.ListMetas() {
		if m.Archived || m.Dir == "" {
			continue
		}
		other, err := filepath.Abs(filepath.Clean(m.Dir))
		if err != nil || other != target || !sessionAliveFn(m) {
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

// SpawnCreateRefusal runs every origin=session refusal that does not need a slot reserved.
// Split from the reservation so the caller can validate before taking a slot it may not keep.
func SpawnCreateRefusal(parent, kind, dir string, worktree bool) *SpawnRefusal {
	if r := spawnDepthRefusal(parent); r != nil {
		return r
	}
	if r := spawnKindRefusal(kind); r != nil {
		return r
	}
	if !worktree {
		if r := spawnWorkingCopyRefusal(dir); r != nil {
			return r
		}
	}
	return nil
}
