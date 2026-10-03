package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// #1257: a Managed session dropped a fired run's prompt before it ran. The run recorded as
// fired becomes a failure in the history, the member is notified, and no other run changes.
func TestScheduleRunNotExecuted(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	mid := realMembership(t, st, ctx) // the notification needs a real membership
	api := newScheduleAPI(&manager{store: st})
	mv := store.MembershipView{MembershipID: mid, TenantID: "default"}
	rec := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"*/5 * * * *","tz":"UTC","prompt":"x"}`, "")
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	for _, r := range []store.ScheduleRun{
		{FiredAt: "2026-10-03T09:00:01Z", Slot: "2026-10-03T09:00:00Z", Status: "fired", Session: "s1"},
		{FiredAt: "2026-10-03T09:05:01Z", Slot: "2026-10-03T09:05:00Z", Status: "fired", Session: "s1"},
		{FiredAt: "2026-10-03T09:10:01Z", Slot: "2026-10-03T09:10:00Z", Status: "fired", Session: "s1"},
		{FiredAt: "2026-10-03T09:05:02Z", Slot: "2026-10-03T09:05:00Z", Status: "fired", Session: "s2"},
	} {
		r.ID, r.ScheduleID, r.MembershipID = store.NewID(), dto.ID, mid
		if err := api.store.AppendScheduleRun(ctx, r, 50); err != nil {
			t.Fatal(err)
		}
	}

	r := doJSON(api.runNotExecuted, mv, "POST", `{"session":"s1","slot":"2026-10-03T09:05:00Z","reason":"archived"}`, dto.ID)
	if r.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", r.Code, r.Body.String())
	}
	runs, err := api.store.ListScheduleRuns(ctx, dto.ID, mid, 50)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, rn := range runs {
		got[rn.FiredAt+" "+rn.Session] = rn.Status
	}
	if s := got["2026-10-03T09:05:01Z s1"]; !strings.HasPrefix(s, "error:not executed") || !strings.Contains(s, "archived") {
		t.Fatalf("the dropped run = %q", s)
	}
	if got["2026-10-03T09:00:01Z s1"] != "fired" || got["2026-10-03T09:10:01Z s1"] != "fired" || got["2026-10-03T09:05:02Z s2"] != "fired" {
		t.Fatalf("other runs changed: %v", got)
	}
	notes, err := api.mgr.store.ListNotifications(ctx, mid, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Kind != "schedule-failed" || notes[0].TargetID != dto.ID {
		t.Fatalf("notifications = %+v", notes)
	}

	// A repeated report finds the run it marked and changes nothing: the session's next fired
	// run stays fired and no second notification is raised.
	r = doJSON(api.runNotExecuted, mv, "POST", `{"session":"s1","slot":"2026-10-03T09:05:00Z","reason":"archived"}`, dto.ID)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"changed":false`) {
		t.Fatalf("repeated report code=%d body=%s", r.Code, r.Body.String())
	}
	runs, _ = api.store.ListScheduleRuns(ctx, dto.ID, mid, 50)
	for _, rn := range runs {
		if rn.FiredAt == "2026-10-03T09:10:01Z" && rn.Status != "fired" {
			t.Fatalf("a repeated report marked the next run: %+v", rn)
		}
	}
	if notes, _ := api.mgr.store.ListNotifications(ctx, mid, "", 10); len(notes) != 1 {
		t.Fatalf("notifications after a repeat = %d, want 1", len(notes))
	}
	if r := doJSON(api.runNotExecuted, mv, "POST", `{"session":"s1","slot":"2026-10-03T09:15:00Z"}`, dto.ID); r.Code != http.StatusNotFound {
		t.Fatalf("unknown slot code=%d", r.Code)
	}
	if r := doJSON(api.runNotExecuted, mv, "POST", `{"session":"s1","slot":"yesterday"}`, dto.ID); r.Code != http.StatusBadRequest {
		t.Fatalf("bad slot code=%d", r.Code)
	}
	other := store.MembershipView{MembershipID: "m2", TenantID: "default"}
	if r := doJSON(api.runNotExecuted, other, "POST", `{"session":"s2","slot":"2026-10-03T09:05:00Z"}`, dto.ID); r.Code == http.StatusOK {
		t.Fatal("another membership marked the run")
	}
}

// Every send that fires a schedule into a session names the run.
func TestScheduleSendsNameTheRun(t *testing.T) {
	sch := store.Schedule{ID: "sch_x", Prompt: "p", OwnerConv: "conv-1"}
	slot := time.Date(2026, 10, 3, 9, 5, 0, 0, time.FixedZone("JST", 9*3600))
	for name, raw := range map[string][]byte{
		"reuse send":   reuseSendBody(sch, slot),
		"reuse create": buildReuseCreateBody(sch, slot, "t"),
		"new create":   buildInjectBody(sch, slot),
	} {
		var b map[string]any
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		if b["schedule_id"] != "sch_x" || b["schedule_slot"] != "2026-10-03T00:05:00Z" {
			t.Errorf("%s: schedule_id=%v schedule_slot=%v", name, b["schedule_id"], b["schedule_slot"])
		}
	}
}
