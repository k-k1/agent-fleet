package sessionx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The MANAGED half of the create handler, which session_spawn_wiring_test.go does not reach.
//
// The majority of kinds (codex / opencode / copilot / cursor / kiro) launch through a driver
// instead of tmux, and that branch calls slot.publish, noteCreateOrigin and — on recreate —
// handOverSpawnLineage from its own lines (session_handlers.go:849,861 / :1338,1358). An
// implementation that wired only the tui path passes every test next door, which is what
// docs/log/87 §87.11 recorded as untested. This file is that work.
//
// What makes it cheap: the registry the branch resolves through (managedDrivers) is a package
// var, so a fake driver in it turns the whole launch into a function call. No runtime, no
// daemon, no CLI.

// spawnDelivery is one first instruction as the child's runtime received it, paired with what
// the injection record said AT THAT MOMENT — the ordering requirement of ADR 0073 decision 14,
// which is observable nowhere else (a record written afterwards still reads correct later).
// slotsHeld / children are the budget as it stood at that same instant. They are the reason
// this file can pin what docs/log/87 §87.11 listed as unpinnable: the difference between
// releasing the slot AT THE META WRITE and releasing it when the handler returns is only
// visible from inside the launch window, and on the managed path Send is inside it.
type spawnDelivery struct {
	prompt    string
	source    string
	slotsHeld int
	children  int
}

// spawnSlotsHeld reads the reservation counter under its own lock — publish has released it by
// the time Send runs, so this cannot deadlock, and an unlocked read would race the counter.
func spawnSlotsHeld(parent string) int {
	spawnInflight.mu.Lock()
	defer spawnInflight.mu.Unlock()
	return spawnInflight.n[parent]
}

// spawnFakeHandle is the ThreadHandle a managed launch delivers through: Send is the managed
// equivalent of deliverInitialPromptFn on the tui path.
type spawnFakeHandle struct {
	name   string
	parent string
	sends  chan spawnDelivery
}

// The channel is buffered by its maker: the handler calls Send inline, before it answers the
// create, so a test that only reads after the response would deadlock on an unbuffered one.
func (h *spawnFakeHandle) Send(in agents.TurnInput) error {
	h.sends <- spawnDelivery{
		prompt:    in.Prompt,
		source:    injectionSourceOf(h.name, in.Prompt),
		slotsHeld: spawnSlotsHeld(h.parent),
		children:  countChildren(h.parent),
	}
	return nil
}

func (h *spawnFakeHandle) Steer(agents.TurnInput) error               { return nil }
func (h *spawnFakeHandle) Interrupt() error                           { return nil }
func (h *spawnFakeHandle) UpdateSettings(agents.ThreadSettings) error { return nil }
func (h *spawnFakeHandle) Respond(agents.InteractionReply) error      { return nil }
func (h *spawnFakeHandle) Events() <-chan agents.Event                { return nil }

func (h *spawnFakeHandle) Snapshot() (agents.ThreadSnapshot, error) {
	return agents.ThreadSnapshot{}, nil
}

// spawnFakeDriver stands in for a kind's managed driver. Only Resume is on the create path
// (through mcpx.StartManagedSession); the embedded nil Driver makes anything else panic rather
// than answer with an invented value, the same discipline as deps_stub_test.go's stubs.
type spawnFakeDriver struct {
	agents.Driver
	sends chan spawnDelivery
}

func (d *spawnFakeDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	return &spawnFakeHandle{name: m.Name, parent: m.OriginSession, sends: d.sends}, nil
}

// useFakeManagedDriver swaps one kind's driver for the fake and puts the real one back. The
// registry is process-global, so restoring it is not optional even in a test binary.
func useFakeManagedDriver(t *testing.T, kind string) chan spawnDelivery {
	t.Helper()
	prev, had := managedDrivers[kind]
	sends := make(chan spawnDelivery, 4)
	managedDrivers[kind] = &spawnFakeDriver{sends: sends}
	t.Cleanup(func() {
		if had {
			managedDrivers[kind] = prev
			return
		}
		delete(managedDrivers, kind)
	})
	return sends
}

// managedSpawnBody is spawnBody for a driver launch: the kind has to be one the registry
// answers for, or the create refuses with driver_unsupported before any of this is reached.
func managedSpawnBody(extra map[string]any) map[string]any {
	body := spawnBody(map[string]any{"kind": session.KindCodex, "driver": session.DriverManaged})
	for k, v := range extra {
		body[k] = v
	}
	return body
}

// The same chain TestCreateSessionSpawnWiring pins, down the driver branch: provenance on the
// meta, the envelope on the instruction the runtime receives, the injection record already
// written when it goes out, and no slot left held.
func TestCreateSessionSpawnWiringManaged(t *testing.T) {
	env := spawnServer(t)
	sends := useFakeManagedDriver(t, session.KindCodex)
	repo := filepath.Join(env.home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	env.fixture(session.Meta{Name: "parent1", Kind: session.KindClaude,
		Origin: session.OriginUser, CreatedAt: "2026-09-09T10:00:00+09:00"})

	code, raw := env.create(managedSpawnBody(map[string]any{"dir": repo, "initial_prompt": "rebase onto develop"}))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	var created session.Session
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Driver != session.DriverManaged {
		t.Fatalf("driver = %q, want managed (this test would otherwise be the tui one again)", created.Driver)
	}
	if created.Origin != session.OriginSession || created.OriginSession != "parent1" {
		t.Fatalf("wire origin = %q/%q, want session/parent1", created.Origin, created.OriginSession)
	}
	// The meta is written by slot.publish on this branch too; without it the child exists with
	// no provenance and the parent's slot never comes back.
	m, ok := session.ReadMeta(created.Name)
	if !ok || m.Origin != session.OriginSession || m.OriginSession != "parent1" {
		t.Fatalf("meta origin = %q/%q (ok=%v)", m.Origin, m.OriginSession, ok)
	}
	d := <-sends
	if !strings.HasPrefix(d.prompt, "[agent-fleet:spawn from=parent1] ") {
		t.Fatalf("delivered task has no spawn envelope: %q", d.prompt)
	}
	if !strings.Contains(d.prompt, "rebase onto develop") {
		t.Fatalf("the task itself did not survive the envelope: %q", d.prompt)
	}
	// Managed delivery is synchronous, so a record written after it loses the badge every time
	// rather than only under a race — noteCreateOrigin has to run before h.Send.
	if d.source != TurnSourceSpawn {
		t.Fatalf("injection source at delivery = %q, want %q (recorded after delivery?)", d.source, TurnSourceSpawn)
	}
	// Inside the launch window: the meta exists and the reservation is already gone, so the
	// parent is charged for ONE child, not two. Releasing at the handler's return instead would
	// read 1 + 1 here and refuse a legitimate third create — invisible from outside, because by
	// then both numbers have settled (docs/log/87 §87.10, §87.11 item 3).
	if d.children != 1 || d.slotsHeld != 0 {
		t.Fatalf("inside the launch window: %d children + %d reservations, want 1 + 0 "+
			"(the slot must be handed over as the meta is written, not at the handler's return)",
			d.children, d.slotsHeld)
	}
	if got := spawnSlotsHeld("parent1"); got != 0 {
		t.Fatalf("inflight after a completed create = %d, want 0 (leaked slot)", got)
	}
	if n := countChildren("parent1"); n != 1 {
		t.Fatalf("children after create = %d, want 1", n)
	}
}

// A recreate of a managed spawned child must not cost the parent a second slot either. The
// handler has two calls to handOverSpawnLineage; TestRecreateSpawnedChildKeepsOneSlot covers
// the other one.
func TestRecreateSpawnedChildKeepsOneSlotManaged(t *testing.T) {
	env := spawnServer(t)
	useFakeManagedDriver(t, session.KindCodex)
	repo := filepath.Join(env.home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	env.fixture(session.Meta{Name: "parent1", Kind: session.KindClaude,
		Origin: session.OriginUser, CreatedAt: "2026-09-09T10:00:00+09:00"})

	code, raw := env.create(managedSpawnBody(map[string]any{"dir": repo}))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	var created session.Session
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if n := countChildren("parent1"); n != 1 {
		t.Fatalf("children after create = %d, want 1", n)
	}

	code, raw = roundtrip(t, env.srv, "POST", "/sessions/"+created.Name+"/recreate", nil)
	if code != http.StatusOK {
		t.Fatalf("recreate = %d %s", code, raw)
	}
	// Two metas, one child: the archived predecessor must have given its lineage up.
	if n := countChildren("parent1"); n != 1 {
		t.Fatalf("children after recreate = %d, want 1 (a recreated child is still one child)", n)
	}
	var recreated session.Session
	if err := json.Unmarshal(raw, &recreated); err != nil {
		t.Fatal(err)
	}
	if recreated.Driver != session.DriverManaged {
		t.Fatalf("successor driver = %q, want managed", recreated.Driver)
	}
	// And the successor is still the parent's to steer (ADR 0073 decision 4 reads this field).
	if m, ok := session.ReadMeta(recreated.Name); !ok || m.OriginSession != "parent1" {
		t.Fatalf("successor lineage = %q (ok=%v)", m.OriginSession, ok)
	}
}
