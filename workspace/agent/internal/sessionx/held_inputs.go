package sessionx

// Operator and scheduled prompts held in a Managed session's queue (#1257). The queue keeps
// them across a stop (agents/heldpeers.go); this is where one that goes without running is
// accounted for: its instruction row is reported as not run (chatx), and its scheduled run is
// recorded as not executed on the Control Plane, which had recorded it as fired when the queue
// accepted it.

import (
	"encoding/json"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
)

// scheduleRefOf is the scheduled run a send belongs to: only a schedule source names one.
func scheduleRefOf(source, id, slot string) agents.ScheduleRef {
	if scheduleInjectionSource(source) == "" || id == "" {
		return agents.ScheduleRef{}
	}
	if _, err := time.Parse(time.RFC3339, slot); err != nil {
		slot = ""
	}
	return agents.ScheduleRef{ID: id, Slot: slot}
}

// scheduleDeliveryOf is the delivery a scheduled send asked for (#1560), completed with the
// run's identity, or nil: only a schedule source names one, and without it the run is handled
// exactly as before delivery targets existed.
func scheduleDeliveryOf(source, id, slot string, d *chatx.ScheduleDelivery) *chatx.ScheduleDelivery {
	ref := scheduleRefOf(source, id, slot)
	if d == nil || ref.ID == "" {
		return nil
	}
	c := *d
	c.ScheduleID, c.Slot = ref.ID, ref.Slot
	return &c
}

// InstallHeldDropHook routes held operator and scheduled prompts that are dropped before they
// ran to their reports. Called once at boot, before anything is queued and before SweepHeld,
// whose drops it must see.
func InstallHeldDropHook() { agents.OnHeldDropped = noteHeldDropped }

// noteHeldDropped runs under a driver's handle lock: the ledger write is local, and the
// Control Plane call goes on its own goroutine.
func noteHeldDropped(d agents.HeldDrop) {
	chatx.MarkInstrNotRun(d.Session, d.Instr, d.Reason)
	if d.Schedule.ID == "" {
		return
	}
	scheduleNotRuns.Add(1)
	go func() {
		defer scheduleNotRuns.Done()
		recordScheduleNotRunFn(d)
	}()
}

// scheduleNotRuns counts the in-flight Control Plane calls, so a test can wait for them.
var scheduleNotRuns sync.WaitGroup

// recordScheduleNotRunFn is recordScheduleNotRun behind a seam: tests must not reach a CP.
var recordScheduleNotRunFn = recordScheduleNotRun

// recordScheduleNotRun asks the Control Plane to mark the run as not executed. A failure is
// logged only: the run history then keeps "fired", which is what it said before #1257.
func recordScheduleNotRun(d agents.HeldDrop) {
	body, _ := json.Marshal(map[string]string{
		"session": d.Session, "slot": d.Schedule.Slot, "reason": d.Reason,
	})
	if _, err := mcpx.CPScheduleDo("POST", "/internal/schedules/"+url.PathEscape(d.Schedule.ID)+"/runs/not-executed", body); err != nil {
		log.Printf("held input: %s: record schedule %s run %s as not executed: %v",
			d.Session, d.Schedule.ID, d.Schedule.Slot, err)
	}
}
