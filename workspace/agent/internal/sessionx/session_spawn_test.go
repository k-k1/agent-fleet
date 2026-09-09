package sessionx

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// spawnFixture gives each test its own session store, so metas written here cannot leak into
// another test's counts.
func spawnFixture(t *testing.T, metas ...session.Meta) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	for _, m := range metas {
		if m.CreatedAt == "" {
			m.CreatedAt = "2026-09-09T10:00:00+09:00"
		}
		session.WriteMeta(m)
	}
}

// injectionSourceOf reads back the badge recorded for one prompt text.
func injectionSourceOf(name, text string) string {
	list, _ := injectionStore.Read(name)
	for _, e := range list {
		if e.Text == text {
			return e.Source
		}
	}
	return ""
}

func child(name, parent string) session.Meta {
	return session.Meta{Name: name, Kind: session.KindClaude, Dir: "/repos/x",
		Origin: session.OriginSession, OriginSession: parent}
}

// The recursion limit (ADR 0073 decision 5): a session that was itself started by a session
// cannot start one. The predicate is the lineage field, NOT origin — so a fork of a child,
// which is stamped origin=handoff, is refused too.
func TestSpawnDepthRefusesChildrenAndTheirForks(t *testing.T) {
	spawnFixture(t,
		session.Meta{Name: "root", Kind: session.KindClaude, Origin: session.OriginUser},
		child("kid", "root"),
		// A fork of the child: handoff origin, lineage kept.
		session.Meta{Name: "forked", Kind: session.KindClaude, Origin: session.OriginHandoff, OriginSession: "root"},
		// A session a person launched from a handoff proposal: no lineage at all, so it may
		// spawn. This is the documented re-entry, not an oversight.
		session.Meta{Name: "relaunched", Kind: session.KindClaude, Origin: session.OriginUser},
	)
	for _, tc := range []struct {
		parent string
		want   string // "" = allowed
	}{
		{"root", ""},
		{"relaunched", ""},
		{"kid", "spawn_depth"},
		{"forked", "spawn_depth"},
		{"ghost", "spawn_unknown_parent"},
	} {
		got := SpawnCreateRefusal(tc.parent, session.KindClaude, "/repos/x", true)
		switch {
		case tc.want == "" && got != nil:
			t.Errorf("%s: refused with %s, want allowed", tc.parent, got.Code)
		case tc.want != "" && (got == nil || got.Code != tc.want):
			t.Errorf("%s: got %v, want %s", tc.parent, got, tc.want)
		}
	}
}

// Raw shells are refused outright (decision 8): the operator's approval gate for exactly this
// is a no-op without a conversation, so on the session surface it is not weaker, it is absent.
func TestSpawnRefusesShellAndSSM(t *testing.T) {
	spawnFixture(t, session.Meta{Name: "root", Kind: session.KindClaude, Origin: session.OriginUser})
	for _, kind := range []string{session.KindShell, session.KindSSM} {
		got := SpawnCreateRefusal("root", kind, "/repos/x", true)
		if got == nil || got.Code != "spawn_kind_refused" {
			t.Errorf("kind %s: got %v, want spawn_kind_refused", kind, got)
		}
	}
	if got := SpawnCreateRefusal("root", session.KindClaude, "/repos/x", true); got != nil {
		t.Errorf("claude refused: %v", got)
	}
}

// worktree=false onto a working copy someone is already running in is refused (decision 7).
// The comparison is the working copy, so a different subdir is still the same copy; a stopped
// session is not in the way.
func TestSpawnRefusesSharedWorkingCopy(t *testing.T) {
	alive := session.Meta{Name: "busy", Kind: session.KindShell, Dir: "/repos/app", Subdir: "console",
		Origin: session.OriginUser}
	spawnFixture(t,
		session.Meta{Name: "root", Kind: session.KindClaude, Origin: session.OriginUser},
		alive,
	)
	orig := sessionAliveFn
	sessionAliveFn = func(m session.Meta) bool { return m.Name == "busy" }
	t.Cleanup(func() { sessionAliveFn = orig })

	if got := SpawnCreateRefusal("root", session.KindClaude, "/repos/app", false); got == nil || got.Code != "spawn_working_copy_busy" {
		t.Fatalf("same working copy: got %v, want spawn_working_copy_busy", got)
	}
	// The path is normalized before comparison, so an equivalent spelling is caught too.
	if got := SpawnCreateRefusal("root", session.KindClaude, "/repos/app/../app", false); got == nil {
		t.Fatal("an equivalent path spelling walked past the check")
	}
	// A worktree create never touches the parent's copy.
	if got := SpawnCreateRefusal("root", session.KindClaude, "/repos/app", true); got != nil {
		t.Fatalf("worktree create refused: %v", got)
	}
	// Somewhere nobody is working.
	if got := SpawnCreateRefusal("root", session.KindClaude, "/repos/other", false); got != nil {
		t.Fatalf("free working copy refused: %v", got)
	}
}

// The budget (decision 6) counts every child whose meta still exists — stopped and ARCHIVED
// included. Archiving is restorable, so freeing a slot for it would let a caller fold up,
// spawn a replacement and restore: an unbounded fleet through a reversible operation.
func TestSpawnBudgetCountsArchivedAndStoppedChildren(t *testing.T) {
	stopped := child("kid2", "root")
	stopped.StoppedAt = "2026-09-09T11:00:00+09:00"
	archived := child("kid3", "root")
	archived.Archived = true
	spawnFixture(t,
		session.Meta{Name: "root", Kind: session.KindClaude, Origin: session.OriginUser},
		child("kid1", "root"),
		stopped,
		archived,
		// Another parent's child, and a session that merely mentions root without being its
		// child: neither counts against root.
		child("other", "elsewhere"),
		session.Meta{Name: "forked", Kind: session.KindClaude, Origin: session.OriginHandoff, OriginSession: "root"},
	)
	if n := countChildren("root"); n != 3 {
		t.Fatalf("children = %d, want 3 (live + stopped + archived)", n)
	}
	if err := reserveSpawnSlot("root"); err == nil {
		t.Fatal("a fourth child was allowed")
	}
	if err := reserveSpawnSlot("elsewhere"); err != nil {
		t.Fatalf("another parent's budget was consumed: %v", err)
	}
	releaseSpawnSlot("elsewhere")
}

// Two creates that differ in content are not serialized by the idempotency ledger (it keys on
// one intent), so counting metas alone lets both pass the same "two existing". The reservation
// is what closes that.
func TestSpawnBudgetReservesBeforeTheMetaExists(t *testing.T) {
	spawnFixture(t,
		session.Meta{Name: "root", Kind: session.KindClaude, Origin: session.OriginUser},
		child("kid1", "root"),
		child("kid2", "root"),
	)
	if err := reserveSpawnSlot("root"); err != nil {
		t.Fatalf("third child refused: %v", err)
	}
	// The third child's meta does not exist yet — exactly the window a concurrent create runs in.
	if err := reserveSpawnSlot("root"); err == nil {
		t.Fatal("a concurrent create took a fourth slot while the third was still launching")
	}
	// A failed launch gives its slot back.
	releaseSpawnSlot("root")
	if err := reserveSpawnSlot("root"); err != nil {
		t.Fatalf("the slot was not released after a failed create: %v", err)
	}
	releaseSpawnSlot("root")
}

// The envelope (decision 14) is what tells the child this instruction came from another
// session rather than from its user.
func TestSpawnEnvelopeNamesTheParent(t *testing.T) {
	got := SpawnEnvelope("slot07", "  rebase onto develop  ")
	if want := "[agent-fleet:spawn from=slot07] rebase onto develop"; got != want {
		t.Fatalf("envelope = %q, want %q", got, want)
	}
	if got := SpawnEnvelope("slot07", "   "); got != "" {
		t.Fatalf("an empty task got an envelope: %q", got)
	}
	// A name that is not a session name never reaches the body: the envelope is machine-read.
	if got := SpawnEnvelope("../etc", "do it"); strings.Contains(got, "spawn from=") {
		t.Fatalf("a malformed parent was written into the envelope: %q", got)
	}
}

// The mirror badge for a spawned child's first turn. Without the source branch the record
// exists and the turn still renders as the user's own input, because a session-spawned create
// carries no report_to.
func TestBadgeOriginOfSpawn(t *testing.T) {
	if got := badgeOriginOf("", "", TurnSourceSpawn); got != TurnSourceSpawn {
		t.Fatalf("spawn badge = %q, want %q", got, TurnSourceSpawn)
	}
	// The other paths keep their answers.
	if got := badgeOriginOf("slot01", "", TurnSourceSpawn); got != turnSourcePeer {
		t.Fatalf("peer badge = %q", got)
	}
	if got := badgeOriginOf("", "", ""); got != "" {
		t.Fatalf("plain input got badge %q", got)
	}
}

// noteCreateOrigin is what makes the badge reach the mirror at all, and it must run before the
// prompt is delivered. Only origin=session records a spawn; nothing else changes shape.
func TestNoteCreateOriginRecordsSpawnOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		req    CreateReq
		parent string
		want   string
	}{
		{"spawned child", CreateReq{InitialPrompt: "task"}, "root", TurnSourceSpawn},
		{"console launch", CreateReq{InitialPrompt: "task"}, "", ""},
		{"operator create", CreateReq{InitialPrompt: "task", ReportTo: "conv-1"}, "", TurnSourceOperator},
		{"schedule, reporting off", CreateReq{InitialPrompt: "task", Source: TurnSourceSchedule}, "", TurnSourceSchedule},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spawnFixture(t)
			noteCreateOrigin("slot01", &tc.req, tc.parent)
			got := injectionSourceOf("slot01", "task")
			if got != tc.want {
				t.Fatalf("recorded source = %q, want %q", got, tc.want)
			}
		})
	}
}
