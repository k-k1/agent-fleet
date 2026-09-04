// slot_class_test.go — the CP's share of the slot classes.
//
// The adapter-side checks (declaration parsing, placement, task definitions) live in
// internal/runtime. What stays here is the CP chain that decides a class from the tenant's
// and the user's stored values, plus the check on where the migrations sit on disk — that
// one belongs to internal/store and is kept in step from there.
package main

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// --- resolution ------------------------------------------------------------------

// sizingOnlyFactory declares slot classes and nothing else. The real one is the
// ecs-ec2 factory, whose type is unexported in internal/runtime; the CP only ever
// asks it for this profile (sizingProfiler).
type sizingOnlyFactory struct{ sizing runtime.WorkspaceSizing }

func (sizingOnlyFactory) New(runtime.Workspace, string, []string) runtime.Runtime { return nil }

func (f sizingOnlyFactory) SizingProfile() runtime.WorkspaceSizing { return f.sizing }

func TestResolveSlotClass(t *testing.T) {
	ctx := context.Background()
	// The chain below reads ONE thing off the runtime — the declared classes and the
	// deployment default (manager.workspaceSizing). Which ladder each class has, and
	// how the spec string parses into them, is the adapter's business and is tested
	// there (internal/runtime/slot_class_test.go); stating the profile directly keeps
	// this test about the resolution order.
	classSizing := runtime.WorkspaceSizing{
		Runtime: "ecs-ec2", DefaultSlotClass: "standard",
		SlotClasses: []runtime.WorkspaceSlotClass{
			{ID: "standard", Label: "S", Arch: "x86_64"},
			{ID: "arm", Label: "A", Arch: "arm64"},
			{ID: "econ", Label: "E", Arch: "arm64"},
		},
	}

	newMgr := func(t *testing.T) (*manager, string, string) {
		t.Helper()
		m := p3Manager(t, p3Store(t))
		m.rtFactory = sizingOnlyFactory{sizing: classSizing}
		ten, err := m.store.CreateTenant(ctx, "acme", "Acme")
		if err != nil {
			t.Fatal(err)
		}
		ident, err := m.store.UpsertIdentity(ctx, "a@example.com", "a", "")
		if err != nil {
			t.Fatal(err)
		}
		mem, err := m.store.EnsureMembership(ctx, ident.ID, ten.ID, "member")
		if err != nil {
			t.Fatal(err)
		}
		return m, ten.ID, mem.ID
	}
	setLimits := func(t *testing.T, m *manager, tenantID string, lim tenantLimits) {
		t.Helper()
		lj, err := json.Marshal(lim)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.store.SetTenantLimits(ctx, tenantID, string(lj)); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("nothing set anywhere is the deployment default", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		got, note := m.resolveSlotClass(ctx, store.Workspace{TenantID: tid, MembershipID: mid})
		if got != "standard" || note != "" {
			t.Fatalf("got %q note=%q", got, note)
		}
	})

	t.Run("the tenant default applies to a member with no value", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		setLimits(t, m, tid, tenantLimits{SlotClass: "arm"})
		if got, _ := m.resolveSlotClass(ctx, store.Workspace{TenantID: tid, MembershipID: mid}); got != "arm" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("the per-user value wins", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		setLimits(t, m, tid, tenantLimits{SlotClass: "arm"})
		if err := m.store.PutUserLimit(ctx, mid, store.UserQuota{SlotClass: "econ"}); err != nil {
			t.Fatal(err)
		}
		if got, _ := m.resolveSlotClass(ctx, store.Workspace{TenantID: tid, MembershipID: mid}); got != "econ" {
			t.Fatalf("got %q", got)
		}
	})

	// The substitution is REPORTED, not just done: a member silently running on
	// another class is invisible until the invoice.
	t.Run("a class the tenant may not use falls back and says so", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		setLimits(t, m, tid, tenantLimits{SlotClass: "arm", AllowedSlotClasses: []string{"standard", "arm"}})
		if err := m.store.PutUserLimit(ctx, mid, store.UserQuota{SlotClass: "econ"}); err != nil {
			t.Fatal(err)
		}
		got, note := m.resolveSlotClass(ctx, store.Workspace{TenantID: tid, MembershipID: mid})
		if got != "arm" {
			t.Fatalf("got %q, want the tenant default", got)
		}
		if !strings.Contains(note, "econ") {
			t.Fatalf("the note must name the class that was refused, got %q", note)
		}
	})

	// An operator can drop a class at any redeploy. Somebody's stored id then names
	// nothing, and that must not fail their Start.
	t.Run("an id the deployment no longer declares falls back", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		if err := m.store.PutUserLimit(ctx, mid, store.UserQuota{SlotClass: "retired"}); err != nil {
			t.Fatal(err)
		}
		got, note := m.resolveSlotClass(ctx, store.Workspace{TenantID: tid, MembershipID: mid})
		if got != "standard" || note == "" {
			t.Fatalf("got %q note=%q", got, note)
		}
	})

	// A super_admin can restrict a tenant to a set that excludes the deployment
	// default. "no usable class" must not become a failed Start.
	t.Run("an allowed set without the deployment default picks an allowed one", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		setLimits(t, m, tid, tenantLimits{AllowedSlotClasses: []string{"econ"}})
		if got, _ := m.resolveSlotClass(ctx, store.Workspace{TenantID: tid, MembershipID: mid}); got != "econ" {
			t.Fatalf("got %q", got)
		}
	})

	// Every runtime but ecs-ec2, and every ecs-ec2 deployment with one unnamed ladder:
	// the whole feature is inert and nothing about an existing deployment changes.
	t.Run("no classes declared resolves to empty", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		// A single unnamed ladder reports no SlotClasses at all — that is what the
		// adapter does with a bare spec, and it is asserted there.
		m.rtFactory = sizingOnlyFactory{sizing: runtime.WorkspaceSizing{Runtime: "ecs-ec2"}}
		if err := m.store.PutUserLimit(ctx, mid, store.UserQuota{SlotClass: "arm"}); err != nil {
			t.Fatal(err)
		}
		if got, _ := m.resolveSlotClass(ctx, store.Workspace{TenantID: tid, MembershipID: mid}); got != "" {
			t.Fatalf("got %q, want \"\"", got)
		}
	})
}

// The stored value survives a round trip, which is the whole point of the migration.
func TestUserLimitRoundTripsSlotClass(t *testing.T) {
	ctx := context.Background()
	m := p3Manager(t, p3Store(t))
	ten, err := m.store.CreateTenant(ctx, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	ident, err := m.store.UpsertIdentity(ctx, "a@example.com", "a", "")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := m.store.EnsureMembership(ctx, ident.ID, ten.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.store.PutUserLimit(ctx, mem.ID, store.UserQuota{MaxSessions: 3, SlotClass: "arm"}); err != nil {
		t.Fatal(err)
	}
	ul, ok, err := m.store.GetUserLimit(ctx, mem.ID)
	if err != nil || !ok {
		t.Fatalf("read back: ok=%v err=%v", ok, err)
	}
	if ul.SlotClass != "arm" || ul.MaxSessions != 3 {
		t.Fatalf("got %+v", ul)
	}
	// And clearing it goes back to "the tenant default", not to the previous value.
	if err := m.store.PutUserLimit(ctx, mem.ID, store.UserQuota{MaxSessions: 3}); err != nil {
		t.Fatal(err)
	}
	if ul, _, _ := m.store.GetUserLimit(ctx, mem.ID); ul.SlotClass != "" {
		t.Fatalf("clearing left %q", ul.SlotClass)
	}
}

// Two migrations with the same numeric prefix are a SILENT data loss:
// schema_migrations is keyed by that integer, so the first of the pair records the
// version and the second is skipped forever as "already applied" — the column it was
// meant to add never exists, and the failure surfaces as a query error in production.
//
// It is the normal outcome of two branches adding a migration in parallel, which is
// how this repository works. Measured once (2026-08-22: develop's 0030_memo_category
// met a branch's 0030_user_limit_slot_class in migrations-pg). This test is the cheap
// half of the fix — the other half refuses to start (sqlStore.migrate).
func TestMigrationVersionsAreUniquePerDialect(t *testing.T) {
	// Both series live in internal/store: //go:embed cannot look above its own
	// directory, so the SQL travels with the package that embeds it. This test reads
	// the files on disk, so the paths have to follow.
	for _, dir := range []string{"internal/store/migrations", "internal/store/migrations-pg"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		seen := map[int]string{}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".sql") {
				continue
			}
			v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
			if err != nil {
				t.Errorf("%s/%s: version prefix is not a number", dir, name)
				continue
			}
			if prev, dup := seen[v]; dup {
				t.Errorf("%s: %s and %s share version %d — one would be silently skipped; renumber the newer one",
					dir, prev, name, v)
			}
			seen[v] = name
		}
		if len(seen) == 0 {
			t.Errorf("%s: no migrations found (did the directory move?)", dir)
		}
	}
}
