package sessionx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
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

// spawnLimitPref writes the user's child-limit setting into the fixture's HOME. Call it AFTER
// spawnFixture, which is what points HOME at a temp dir.
//
// It writes the prefs FILE rather than setting session.SpawnChildLimitPref, so the assertions
// below run the route production runs: ui-prefs.json -> uiprefs.SpawnChildLimit -> the init hook
// -> session.SpawnChildLimit. Setting the hook by hand would pass with that wiring deleted.
func spawnLimitPref(t *testing.T, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(uiprefs.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"sessionSpawnChildLimit": n})
	if err := os.WriteFile(uiprefs.Path(), b, 0o600); err != nil {
		t.Fatal(err)
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
		got := SpawnCreateRefusal(tc.parent, session.KindClaude)
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
		got := SpawnCreateRefusal("root", kind)
		if got == nil || got.Code != "spawn_kind_refused" {
			t.Errorf("kind %s: got %v, want spawn_kind_refused", kind, got)
		}
	}
	if got := SpawnCreateRefusal("root", session.KindClaude); got != nil {
		t.Errorf("claude refused: %v", got)
	}
}

// worktree=false onto a working copy someone is already running in is refused (decision 7).
// The comparison is the working copy, so a different subdir is still the same copy; a stopped
// session is not in the way.
func TestSpawnRefusesSharedWorkingCopy(t *testing.T) {
	// Real directories, because the comparison resolves symlinks: the whole point is that two
	// spellings of one checkout cannot read as two different targets.
	root := t.TempDir()
	repo := filepath.Join(root, "repos", "app")
	if err := os.MkdirAll(filepath.Join(repo, "console"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "app-link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	free := filepath.Join(root, "repos", "other")
	if err := os.MkdirAll(free, 0o755); err != nil {
		t.Fatal(err)
	}
	spawnFixture(t,
		session.Meta{Name: "root", Kind: session.KindClaude, Origin: session.OriginUser},
		// The busy session is registered under the REAL path, and it sits in a subdir.
		session.Meta{Name: "busy", Kind: session.KindShell, Dir: repo, Subdir: "console",
			Origin: session.OriginUser},
	)
	orig := sessionAliveFn
	sessionAliveFn = func(m session.Meta) bool { return m.Name == "busy" }
	t.Cleanup(func() { sessionAliveFn = orig })

	// Subdir takes no part: the same checkout is the same checkout.
	if got := spawnWorkingCopyRefusal(repo); got == nil || got.Code != "spawn_working_copy_busy" {
		t.Fatalf("same working copy: got %v, want spawn_working_copy_busy", got)
	}
	// A `..` spelling and a SYMLINK to the same checkout are the same target. Without resolving
	// them the refusal is decorative — a caller only has to spell the path differently.
	if got := spawnWorkingCopyRefusal(filepath.Join(repo, "console", "..")); got == nil {
		t.Fatal("a `..` spelling walked past the check")
	}
	if got := spawnWorkingCopyRefusal(link); got == nil {
		t.Fatal("a symlink to the busy working copy walked past the check")
	}
	// The reverse direction too: the session registered under a symlinked path, the spawn
	// naming the real one.
	spawnFixture(t,
		session.Meta{Name: "root", Kind: session.KindClaude, Origin: session.OriginUser},
		session.Meta{Name: "busy", Kind: session.KindShell, Dir: link, Origin: session.OriginUser},
	)
	if got := spawnWorkingCopyRefusal(repo); got == nil {
		t.Fatal("a session registered under a symlinked path was not seen")
	}
	// That a worktree create skips this check entirely is the CALLER's decision, so it is
	// asserted where the caller is: TestCreateSessionSpawnWorkingCopyGuard.
	// Somewhere nobody is working.
	if got := spawnWorkingCopyRefusal(free); got != nil {
		t.Fatalf("free working copy refused: %v", got)
	}
}

// The budget (decision 6, amended) counts a STOPPED child — the slot is held until that meta
// goes — but not an ARCHIVED one.
//
// Archived used to count, so that "fold up, spawn a replacement, restore" could not walk past
// the limit. The cost of that was permanent: StoppedTTL's prune skips archived metas, so an
// archived child held its slot for ever and a parent that tidied three of them could never
// spawn again. Archiving is a Console-only action a session cannot perform, which is the same
// ground on which a fork and a recreate are kept out of this count.
func TestSpawnBudgetCountsStoppedButNotArchivedChildren(t *testing.T) {
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
	if n := countChildren("root"); n != 2 {
		t.Fatalf("children = %d, want 2 (live + stopped; the archived one holds no slot)", n)
	}
	// Two of three used, so the archived child's slot is genuinely available again.
	if err := reserveSpawnSlot("root"); err != nil {
		t.Fatalf("the slot an archived child used to pin was still refused: %v", err)
	}
	(&spawnSlot{parent: "root"}).release()

	// Un-archiving takes the slot back, and the limit holds at three.
	archived.Archived = false
	session.WriteMeta(archived)
	if n := countChildren("root"); n != 3 {
		t.Fatalf("children after restore = %d, want 3", n)
	}
	if err := reserveSpawnSlot("root"); err == nil {
		t.Fatal("a fourth child was allowed")
	}
	if err := reserveSpawnSlot("elsewhere"); err != nil {
		t.Fatalf("another parent's budget was consumed: %v", err)
	}
	(&spawnSlot{parent: "elsewhere"}).release()
}

// The budget is the user's setting (ADR 0073 decision 6, amendment 2026-09-10), and BOTH things
// that number does have to follow it: what is enforced, and what the refusal says.
//
// The second half is the point. Enforcing a configured limit while the refusal still reads
// "上限 3" produces the invisible limit decision 6 exists to prevent — the caller is told a
// ceiling it is not being held to, and a unit test on the count alone would never notice.
func TestSpawnBudgetFollowsTheUsersSetting(t *testing.T) {
	for _, tc := range []struct {
		name string
		pref int
		want int // the limit actually in force
	}{
		{"one child", 1, 1},
		{"the ceiling", session.SpawnChildLimitMax, session.SpawnChildLimitMax},
		// Not a value the Console can produce, so it falls back to the default rather than to a
		// ceiling nobody chose.
		{"past the ceiling", session.SpawnChildLimitMax + 1, session.SpawnChildLimitDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spawnFixture(t, session.Meta{Name: "root", Kind: session.KindClaude, Origin: session.OriginUser})
			spawnLimitPref(t, tc.pref)

			for i := 0; i < tc.want; i++ {
				if err := reserveSpawnSlot("root"); err != nil {
					t.Fatalf("child %d of %d refused: %v", i+1, tc.want, err)
				}
				(&spawnSlot{parent: "root"}).publish(child(fmt.Sprintf("kid%d", i), "root"))
			}
			err := reserveSpawnSlot("root")
			if err == nil {
				t.Fatalf("child %d was allowed although the limit is %d", tc.want+1, tc.want)
			}
			// The number the caller is shown is the number it was held to.
			if want := fmt.Sprintf("上限 %d）", tc.want); !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal %q does not name the limit in force (%q)", err, want)
			}
		})
	}
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
	(&spawnSlot{parent: "root"}).release()
	if err := reserveSpawnSlot("root"); err != nil {
		t.Fatalf("the slot was not released after a failed create: %v", err)
	}
	(&spawnSlot{parent: "root"}).release()
}

// The slot is handed over to the meta the moment the meta exists. Holding both counts one child
// twice for the length of a launch (tmux, a worktree — seconds), and a parent well under the
// limit is refused against a fleet that does not exist.
func TestSpawnSlotIsHandedOverToTheMeta(t *testing.T) {
	spawnFixture(t,
		session.Meta{Name: "root", Kind: session.KindClaude, Origin: session.OriginUser},
		child("kid1", "root"),
	)
	// A create in flight: reserved, meta not yet written. One real child, one launching.
	slot := &spawnSlot{parent: "root"}
	if err := reserveSpawnSlot("root"); err != nil {
		t.Fatalf("second child refused: %v", err)
	}
	// The meta lands and the slot goes with it, as ONE step: a concurrent create holding the
	// same lock sees either "meta absent, one in flight" or "meta present, none in flight", and
	// both count this child exactly once.
	slot.publish(child("kid2", "root"))
	if n := countChildren("root"); n != 2 {
		t.Fatalf("children = %d, want 2", n)
	}
	if got := spawnInflight.n["root"]; got != 0 {
		t.Fatalf("inflight after publish = %d, want 0 (the slot was not handed over)", got)
	}
	// A third create is legitimate now — two children, limit three.
	third := &spawnSlot{parent: "root"}
	if err := reserveSpawnSlot("root"); err != nil {
		t.Fatalf("third child refused although only two exist: %v (slot counted twice?)", err)
	}
	// The deferred release on the create that already published must not give a second slot
	// back: that would free one the child now occupies and let the limit drift upwards.
	slot.release()
	if got := spawnInflight.n["root"]; got != 1 {
		t.Fatalf("inflight = %d, want 1 (a double release freed an occupied slot)", got)
	}
	third.release()
}

// Concurrent creates must not overshoot the limit. This is the direction that matters: a
// spurious refusal costs a caller one retry, while an overshoot puts agents on the host that the
// budget exists to prevent.
//
// It does NOT prove the reserve/publish pair is atomic — the interleaving that double-counts
// produces a spurious refusal, and another goroutine simply takes the slot instead, so the
// totals look the same. What it does catch is the opposite mistake: releasing before the meta is
// visible, where a window exists in which the child is counted by nobody.
func TestSpawnBudgetHoldsUnderConcurrentCreates(t *testing.T) {
	spawnFixture(t, session.Meta{Name: "root", Kind: session.KindClaude, Origin: session.OriginUser})

	const racers = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := reserveSpawnSlot("root"); err != nil {
				return
			}
			mu.Lock()
			won++
			mu.Unlock()
			(&spawnSlot{parent: "root"}).publish(child(fmt.Sprintf("kid%d", i), "root"))
		}(i)
	}
	wg.Wait()

	limit := session.SpawnChildLimit()
	if won != limit {
		t.Fatalf("%d creates got a slot, want exactly %d", won, limit)
	}
	if n := countChildren("root"); n != limit {
		t.Fatalf("children = %d, want %d", n, limit)
	}
	if err := reserveSpawnSlot("root"); err == nil {
		t.Fatal("the limit is not in force after the race")
	}
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

// A recreate replaces one child with another, and both metas carry the parent — so ONE child
// would cost TWO slots, and a user recreating their own child would be the reason the parent
// may not spawn again. Same objection that keeps forks out of the count.
//
// Since archived children stopped counting (decision 6, amended) the predecessor costs nothing
// WHILE it is archived, which is where a recreate leaves it. What the hand-over still buys is
// the restore: un-archiving the superseded identity would otherwise charge the parent a second
// slot for a session it does not have. So the assertion is made after a restore — a count taken
// with the predecessor archived cannot tell the hand-over from its absence.
func TestRecreateHandsTheChildSlotToTheSuccessor(t *testing.T) {
	spawnFixture(t,
		session.Meta{Name: "root", Kind: session.KindClaude, Origin: session.OriginUser},
		child("kid1", "root"),
	)
	// The archived predecessor of a recreate, plus the successor that replaced it.
	old := child("kid2", "root")
	old.Archived = true
	session.WriteMeta(old)
	session.WriteMeta(child("kid2b", "root"))

	handOverSpawnLineage(old.Name)

	// Restored by the user: the predecessor is back in the active list, and it must not bring a
	// second slot charge with it.
	restored, _ := session.ReadMeta("kid2")
	restored.Archived = false
	session.WriteMeta(restored)
	if n := countChildren("root"); n != 2 {
		t.Fatalf("children after the hand-over and a restore = %d, want 2 (one recreated child is one child)", n)
	}
	// The successor is still the parent's to steer, and the predecessor keeps its accounting
	// origin — only "whose child was it" is dropped, for a session that no longer stands for one.
	m, _ := session.ReadMeta("kid2")
	if m.OriginSession != "" || session.OriginOf(m) != session.OriginSession {
		t.Fatalf("predecessor = %q/%q, want session/(no parent)", m.Origin, m.OriginSession)
	}
	if m2, _ := session.ReadMeta("kid2b"); m2.OriginSession != "root" {
		t.Fatalf("successor lost its parent: %q", m2.OriginSession)
	}
	// The predecessor can still be restored by the user, and it comes back able to spawn —
	// recorded in ADR 0073 decision 6 rather than defended against, because reaching it takes
	// two deliberate Console actions.
	if r := spawnDepthRefusal("kid2"); r != nil {
		t.Fatalf("the superseded identity is still lineage-bound: %v", r)
	}
	// Nothing happens to a session that was never a child.
	plain := session.Meta{Name: "solo", Kind: session.KindClaude, Origin: session.OriginUser}
	session.WriteMeta(plain)
	handOverSpawnLineage(plain.Name)
	if m3, ok := session.ReadMeta("solo"); !ok || m3.Origin != session.OriginUser {
		t.Fatalf("an unrelated session was rewritten: %+v", m3)
	}
}
