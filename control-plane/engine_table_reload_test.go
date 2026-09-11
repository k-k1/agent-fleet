package main

// Re-reading the engine table while the Control Plane runs (ADR 0074, the gap PR #520
// measured: a rung changed by a CloudFormation update reached a running CP only after a
// ~100-second blue/green of the CP itself).

import (
	"errors"
	"strings"
	"testing"
)

// errTableUnreachable stands for SSM being away, which must read as "keep what we have".
var errTableUnreachable = errors.New("dial tcp: i/o timeout")

// tableFixture is one role's row, with whatever ladder the case is about.
func tableFixture(classes string) string {
	return `{"engines":[{"key":"image","service":"af-image","url":"http://127.0.0.1:1",` +
		`"health":"/v1/models","provider":"sdcpp","api":"images","idleSec":900,` +
		`"startDeadlineSec":900,"classes":"` + classes + `"}]}`
}

const (
	ladderBefore = "l4|L4 24GB|22000|g6.xlarge|4-8|16000-32000|1.26"
	ladderAfter  = "l4|L4 24GB|22000|g6.xlarge|4-8|16000-32000|1.26;l40s|L40S 48GB|44000|g6e.xlarge|4-8|32000-64000|2.50"
)

func reloadFixture(t *testing.T, classes string) (*engineTableReloader, *engineRuntimeState, *fakePendingSSM) {
	t.Helper()
	e := newTestImageEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.ctrl = nil
	e.classes = parseEngineClasses(ladderBefore)
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	ssmc := &fakePendingSSM{value: tableFixture(classes)}
	return newEngineTableReloader(ssmc, "/af-ws/engines", reg, ""), e, ssmc
}

// 🔴 The defect itself: the ladder is what a CloudFormation update changes, and until this the
// running process kept the one it started with. "Models are the catalogue, the chassis is the
// table" (ADR 0072 decision 7) is the whole point of the split, and it cost a CP redeployment.
func TestEngineTableReloadCarriesANewLadder(t *testing.T) {
	r, e, ssmc := reloadFixture(t, ladderAfter)

	if got := len(e.classList()); got != 1 {
		t.Fatalf("the fixture starts with %d rung(s), want 1", got)
	}
	if !r.tick(t.Context()) {
		t.Fatal("a table with a new rung changed nothing")
	}
	got := e.classList()
	if len(got) != 2 || got[0].ID != "l4" || got[1].ID != "l40s" {
		t.Fatalf("classes = %+v, want the ladder the table now declares, in its order", got)
	}
	if got[1].VramMiB != 44000 || got[1].UsdPerHour != 2.50 {
		t.Errorf("the new rung's numbers did not come across: %+v", got[1])
	}

	// 🔴 The positive control for the poll itself: with the reload NOT run, the same registry
	// keeps the ladder it started with. Without this, a test that asserted the ladder after a
	// tick would pass on a fixture that simply started with two rungs.
	_, e2, _ := reloadFixture(t, ladderAfter)
	if got := len(e2.classList()); got != 1 {
		t.Errorf("the ladder changed with no tick: %+v", e2.classList())
	}

	// An unchanged table is a string compare and nothing else — the same ladder comes back as
	// the SAME slice, so nothing downstream sees a swap it has to be safe for.
	before := e.classList()
	if r.tick(t.Context()) {
		t.Error("an unchanged table reported a change")
	}
	after := e.classList()
	if len(after) != len(before) || &after[0] != &before[0] {
		t.Error("an unchanged table replaced the ladder anyway")
	}
	if ssmc.reads != 2 {
		t.Errorf("SSM was read %d times for two ticks", ssmc.reads)
	}

	// The same VALUE arriving again is not a change either, even though the text is re-read.
	ssmc.value = tableFixture(ladderAfter)
	if r.tick(t.Context()) {
		t.Error("re-reading the same text reported a change")
	}
}

// 🔴 A table nobody can parse is not a table that says "no engines". The registry keeps what
// it has — the alternative is an engine list that empties itself over a bad publish, taking
// every running session's engine with it.
func TestEngineTableReloadKeepsTheOldTableWhenTheNewOneIsBroken(t *testing.T) {
	r, e, ssmc := reloadFixture(t, ladderAfter)
	if !r.tick(t.Context()) {
		t.Fatal("the good table did not land")
	}
	ssmc.value = `{"engines":[{"key":"image",` // truncated mid-document
	if r.tick(t.Context()) {
		t.Error("a broken table reported a change")
	}
	if got := e.classList(); len(got) != 2 {
		t.Fatalf("a broken table cost the registry its ladder: %+v", got)
	}
	// Reported ONCE: the value is remembered even though it was refused, or a deployment with
	// a bad parameter logs the same line every ten seconds for ever.
	before := ssmc.reads
	if r.tick(t.Context()) {
		t.Error("the same broken table reported a change on the second look")
	}
	if ssmc.reads != before+1 {
		t.Errorf("reads = %d, want one per tick", ssmc.reads-before)
	}

	// And SSM being unreachable is not a change either — same reasoning, different failure.
	ssmc.err = errTableUnreachable
	if r.tick(t.Context()) {
		t.Error("an unreachable SSM reported a change")
	}
	if got := len(e.classList()); got != 2 {
		t.Errorf("an unreachable SSM cost the registry its ladder (%d rungs)", got)
	}
}

// What the reload deliberately does NOT do, and why each one is a log rather than an action.
func TestEngineTableReloadLeavesTheRestToARestart(t *testing.T) {
	// A field that is wired into objects this process is using right now: the ladder still
	// lands, and the rest is reported rather than half-applied.
	r, e, ssmc := reloadFixture(t, ladderAfter)
	ssmc.value = strings.Replace(tableFixture(ladderAfter), `"url":"http://127.0.0.1:1"`,
		`"url":"http://127.0.0.1:2"`, 1)
	if !r.tick(t.Context()) {
		t.Fatal("the ladder did not land alongside a change that cannot be taken live")
	}
	if e.def.URL != "http://127.0.0.1:1" {
		t.Errorf("the url was swapped under a running engine: %q", e.def.URL)
	}
	if len(e.classList()) != 2 {
		t.Error("the ladder was dropped because something else in the row changed")
	}

	// A role that appears in the table is not registered from inside a poll: it would come up
	// with no controller, no ECS client and none of the state the rest of the process assumes.
	r2, _, ssmc2 := reloadFixture(t, ladderBefore)
	ssmc2.value = strings.Replace(tableFixture(ladderBefore), `"engines":[`,
		`"engines":[{"key":"llm","service":"af-llm","url":"http://127.0.0.1:3","health":"/health","provider":"llamacpp","api":"chat"},`, 1)
	r2.tick(t.Context())
	if r2.reg.get("llm") != nil {
		t.Error("a role was registered from inside the poll")
	}
	if len(r2.reg.list()) != 1 {
		t.Errorf("the registry grew to %d engines", len(r2.reg.list()))
	}

	// A role that DISAPPEARS keeps running. Stopping one on the strength of one poll takes a
	// GPU away from whatever is using it; `mode=off` is the decision that does that.
	r3, e3, ssmc3 := reloadFixture(t, ladderBefore)
	ssmc3.value = `{"engines":[]}`
	r3.tick(t.Context())
	if r3.reg.get("image") != e3 {
		t.Error("an engine was unregistered because the table stopped naming it")
	}

	// 🔴 Adopting a ladder where there was none is also a restart: the capacity client and the
	// controller's start gate are attached only when a ladder exists at construction, so a
	// ladder taken live here would be one the panel shows and nothing enforces — which is the
	// "it lands quietly on the old card" failure ADR 0074 decision 4 exists for.
	r4, e4, _ := reloadFixture(t, ladderAfter)
	e4.classes = nil
	if r4.tick(t.Context()) {
		t.Error("a ladder was adopted live, with no start gate behind it")
	}
	if len(e4.classList()) != 0 {
		t.Errorf("classes = %+v, want none until a restart", e4.classList())
	}
}

// 🔴 The swap happens under the controller, the gateway and the admin panel — all of which
// read the ladder without knowing a poll exists. Run under -race, which is where "a pointer
// swap or an RWMutex" stops being a style question.
func TestEngineTableReloadIsSafeWhileTheLadderIsBeingRead(t *testing.T) {
	r, e, ssmc := reloadFixture(t, ladderAfter)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			// The three readers of the ladder, as they are called in the process.
			_ = len(e.classList())
			_, _ = e.selectedClass(t.Context())
			_ = e.classStartHeld(t.Context())
		}
	}()
	for i := 0; i < 50; i++ {
		if i%2 == 0 {
			ssmc.value = tableFixture(ladderAfter)
		} else {
			ssmc.value = tableFixture(ladderBefore)
		}
		r.tick(t.Context())
	}
	close(stop)
	<-done
	// And it ends on whichever ladder the table last held, not on a half-applied one.
	got := e.classList()
	if len(got) != 1 || got[0].ID != "l4" {
		t.Errorf("classes = %+v, want the last table's single rung", got)
	}
}
