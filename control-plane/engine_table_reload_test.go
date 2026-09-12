package main

// Re-reading the engine table while the Control Plane runs (ADR 0074, the gap PR #520
// measured: a rung changed by a CloudFormation update reached a running CP only after a
// ~100-second blue/green of the CP itself).

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// errTableUnreachable stands for SSM being away, which must read as "keep what we have".
var errTableUnreachable = errors.New("dial tcp: i/o timeout")

// tableFixture is one role's row, with whatever ladder the case is about.
func tableFixture(classes string) string { return tableRow(classes, templateBefore) }

// tableRow is the same row with the launch template spelled out, for the cases about a template
// being REPLACED — the successor of the capacity provider rename #536 measured.
func tableRow(classes, template string) string {
	return `{"engines":[{"key":"image","service":"af-image","url":"http://127.0.0.1:1",` +
		`"health":"/v1/models","provider":"sdcpp","api":"images","idleSec":900,` +
		`"startDeadlineSec":900,"launchTemplate":"` + template + `","classes":"` + classes + `"}]}`
}

const (
	ladderBefore = "l4|L4 24GB|22000|g6.xlarge|4-8|16000-32000|1.26"
	ladderAfter  = "l4|L4 24GB|22000|g6.xlarge|4-8|16000-32000|1.26;l40s|L40S 48GB|44000|g6e.xlarge|4-8|32000-64000|2.50"

	templateBefore = "lt-0aaa"
	templateAfter  = "lt-0bbb"
)

func reloadFixture(t *testing.T, classes string) (*engineTableReloader, *engineRuntimeState, *fakePendingSSM) {
	t.Helper()
	e := newTestImageEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.ctrl = nil
	e.classes = parseEngineClasses(ladderBefore)
	e.def.LaunchTemplate = templateBefore
	e.ecs.roleAttr = engineBoxRole(e.def)
	e.cluster = "cluster"
	e.offers = newEngineOfferRun(engineOfferBudgetDefault)
	e.fleet = newEngineFleet(&fakeFleet{}, "image", "cluster", templateBefore, []string{"subnet-a"})
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

// --- the launch template's ID (the successor of #536 step 4) --------------------------

// 🔴 The defect this inherits: replacing the resource a role buys from gives it a NEW NAME, the
// table carries it, and until this the running CP only logged "restart the Control Plane". What
// followed was measured on smkvsuu when that resource was a capacity provider: the rung apply
// went to a name that no longer existed (400), `box` matched nothing so the panel said the engine
// was up on a card it was not on, and it took 217 seconds of force-new-deployment to clear.
// A launch template is replaced the same way, and the next purchase would go to a template that
// no longer exists.
func TestEngineTableReloadTakesAReplacedLaunchTemplateLive(t *testing.T) {
	var buf bytes.Buffer
	defer captureLog(&buf)()
	r, e, ssmc := reloadFixture(t, ladderBefore)
	ssmc.value = tableRow(ladderBefore, templateAfter)

	if got := e.fleet.launchTemplate(); got != templateBefore {
		t.Fatalf("the fixture starts on %q", got)
	}
	if !r.tick(t.Context()) {
		t.Fatal("a table with a replaced launch template changed nothing")
	}
	if got := e.fleet.launchTemplate(); got != templateAfter {
		t.Fatalf("launch template = %q, want the one the table now declares", got)
	}
	// 🔴 The whole point: this must not be reported as needing a restart any more.
	if strings.Contains(buf.String(), "restart the Control Plane") {
		t.Errorf("a replaced template still asks for a restart:\n%s", buf.String())
	}
	// The ladder was not touched, and nothing else in the row moved.
	if got := e.classList(); len(got) != 1 || got[0].ID != "l4" {
		t.Errorf("the ladder moved with the template: %+v", got)
	}
	// Idempotent: the same table again is a string compare and reports nothing.
	if r.tick(t.Context()) {
		t.Error("re-reading the same template reported a change")
	}
	// 🔴 The positive control for the tick itself — with no reload, the same fixture keeps the
	// old id, so the assertion above is about the reload and not about the fixture.
	_, e2, _ := reloadFixture(t, ladderBefore)
	if got := e2.fleet.launchTemplate(); got != templateBefore {
		t.Errorf("the template changed with no tick: %q", got)
	}
}

// Where the id is actually spent: the purchase. The destination is pinned by what the fake
// records rather than asserted about a string.
func TestEngineTableReloadPointsTheNextPurchaseAtTheNewTemplate(t *testing.T) {
	st := testSettingsStore(t)
	r, e, ssmc := reloadFixture(t, ladderBefore)
	f := &fakeFleet{}
	e.fleet = newEngineFleet(f, "image", "cluster", templateBefore, []string{"subnet-a"})
	e.settings, e.audit = st, st
	ssmc.value = tableRow(ladderBefore, templateAfter)

	if !r.tick(t.Context()) {
		t.Fatal("the replacement did not land")
	}
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("the start was held after the replacement: %s", why)
	}
	if err := e.startOnOffer(t.Context()); err == nil {
		t.Fatal("the start did not report that it is waiting for the box")
	}
	if len(f.creates) != 1 {
		t.Fatalf("%d purchase(s)", len(f.creates))
	}
	got := aws.ToString(f.creates[0].LaunchTemplateConfigs[0].LaunchTemplateSpecification.LaunchTemplateId)
	if got != templateAfter {
		t.Errorf("the box was bought from %q, want %q", got, templateAfter)
	}
}

// A row that GAINS or LOSES its launch template is a different question: the fleet is attached at
// construction, so a template appearing here would be a purchase path nothing holds — the same
// shape as adopting a ladder. It asks for a restart rather than pretending.
func TestEngineTableReloadAsksForARestartWhenTheTemplateAppears(t *testing.T) {
	var buf bytes.Buffer
	defer captureLog(&buf)()
	r, _, ssmc := reloadFixture(t, ladderBefore)
	ssmc.value = strings.Replace(tableFixture(ladderBefore), `"launchTemplate":"`+templateBefore+`"`, `"launchTemplate":""`, 1)
	r.tick(t.Context())
	if !strings.Contains(buf.String(), "restart the Control Plane") || !strings.Contains(buf.String(), "launch template") {
		t.Errorf("dropping the launch template did not ask for a restart:\n%s", buf.String())
	}
}

// The rest of a row is unchanged: still a log, still a restart. Asserted beside the rename so
// that "the restart line is gone" is about the capacity provider and not about the line having
// stopped being written at all.
func TestEngineTableReloadStillAsksForARestartForTheOtherFields(t *testing.T) {
	for _, c := range []struct{ what, from, to, want string }{
		{"service", `"service":"af-image"`, `"service":"af-image-2"`, "service"},
		{"url", `"url":"http://127.0.0.1:1"`, `"url":"http://127.0.0.1:2"`, "url"},
		{"health", `"health":"/v1/models"`, `"health":"/healthz"`, "health"},
		{"provider", `"provider":"sdcpp"`, `"provider":"comfy"`, "provider"},
		{"idle", `"idleSec":900`, `"idleSec":600`, "idle"},
		{"start deadline", `"startDeadlineSec":900`, `"startDeadlineSec":600`, "start deadline"},
	} {
		t.Run(c.what, func(t *testing.T) {
			var buf bytes.Buffer
			defer captureLog(&buf)()
			r, _, ssmc := reloadFixture(t, ladderBefore)
			next := strings.Replace(tableFixture(ladderBefore), c.from, c.to, 1)
			if next == tableFixture(ladderBefore) {
				t.Fatalf("the fixture does not contain %s — this case changes nothing", c.from)
			}
			ssmc.value = next
			r.tick(t.Context())
			if !strings.Contains(buf.String(), "restart the Control Plane") ||
				!strings.Contains(buf.String(), c.want) {
				t.Errorf("changing %s did not ask for a restart:\n%s", c.what, buf.String())
			}
		})
	}
}

// 🔴 The swap happens under the controller, the gateway and the admin panel — all of which
// read the ladder without knowing a poll exists. Run under -race, which is where "a pointer
// swap or an RWMutex" stops being a style question.
func TestEngineTableReloadIsSafeWhileTheLadderIsBeingRead(t *testing.T) {
	r, e, ssmc := reloadFixture(t, ladderAfter)
	e.ecs.api = &engineTestECS{desired: 1, running: 1, instance: "engine-image", instanceType: "g6.xlarge"}
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
			// And the launch template, which moves under them for the same reason the ladder
			// does — it is the destination of the next purchase.
			_ = e.fleet.launchTemplate()
			_, _ = e.ecs.box(t.Context())
		}
	}()
	for i := 0; i < 50; i++ {
		if i%2 == 0 {
			ssmc.value = tableRow(ladderAfter, templateAfter)
		} else {
			ssmc.value = tableRow(ladderBefore, templateBefore)
		}
		r.tick(t.Context())
	}
	close(stop)
	<-done
	// And it ends on whichever ladder and template the table last held, not on a half-applied one.
	got := e.classList()
	if len(got) != 1 || got[0].ID != "l4" {
		t.Errorf("classes = %+v, want the last table's single rung", got)
	}
	if tpl := e.fleet.launchTemplate(); tpl != templateBefore {
		t.Errorf("launch template = %q, want the last table's", tpl)
	}
}
