package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// #1560: a schedule names its delivery targets and may enable the silent sentinel. Both are
// stored, read back, patched, and refused when malformed.
func TestScheduleDeliveryFieldsRoundTrip(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	api := newScheduleAPI(&manager{store: st})
	mv := store.MembershipView{MembershipID: "m1", TenantID: "default"}

	// Omitted = today's report: the operator conversation, no sentinel.
	rec := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x","report":true}`, "")
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != http.StatusCreated || !reflect.DeepEqual(dto.DeliverTo, []string{"operator"}) || dto.Silent {
		t.Fatalf("default: code=%d deliver_to=%v silent=%v", rec.Code, dto.DeliverTo, dto.Silent)
	}

	// Duplicates fold, the order is fixed, and the choice survives a read from the store.
	rec = doJSON(api.create, mv, "POST",
		`{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x","report":true,"silent":true,"deliver_to":["slack","notifications","slack"]}`, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	got, _, err := st.GetSchedule(ctx, dto.ID)
	if err != nil || got.DeliverTo != "notifications,slack" || !got.Silent {
		t.Fatalf("stored deliver_to=%q silent=%v err=%v", got.DeliverTo, got.Silent, err)
	}

	// A patch replaces the list; omitting it leaves it alone.
	rec = doJSON(api.update, mv, "PATCH", `{"deliver_to":["discord","operator"],"silent":false}`, dto.ID)
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != http.StatusOK || !reflect.DeepEqual(dto.DeliverTo, []string{"operator", "discord"}) || dto.Silent {
		t.Fatalf("patch: code=%d deliver_to=%v silent=%v", rec.Code, dto.DeliverTo, dto.Silent)
	}
	rec = doJSON(api.update, mv, "PATCH", `{"spec_label":"morning"}`, dto.ID)
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if !reflect.DeepEqual(dto.DeliverTo, []string{"operator", "discord"}) {
		t.Fatalf("an unrelated patch changed deliver_to: %v", dto.DeliverTo)
	}

	for name, body := range map[string]string{
		"unknown target":    `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x","deliver_to":["webhook:https://example.com"]}`,
		"assistant silent":  `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x","session_mode":"assistant","silent":true}`,
		"assistant targets": `{"spec_kind":"cron","spec":"0 9 * * *","prompt":"x","session_mode":"assistant","deliver_to":["slack"]}`,
	} {
		if rec := doJSON(api.create, mv, "POST", body, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code=%d, want 400", name, rec.Code)
		}
	}
	if rec := doJSON(api.update, mv, "PATCH", `{"deliver_to":["email"]}`, dto.ID); rec.Code != http.StatusBadRequest {
		t.Errorf("patching an unknown target: code=%d, want 400", rec.Code)
	}
}

// #1560: a schedule that asks for nothing beyond today's report sends the Agent exactly the body
// it sent before, so an Agent that never heard of delivery targets behaves as it always has.
func TestScheduleDeliveryBodies(t *testing.T) {
	slot := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	base := store.Schedule{ID: "sch_1", OwnerConv: "conv1", TZ: "UTC", Prompt: "check", Report: true, SpecLabel: "nightly"}
	decode := func(b []byte) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	bodies := map[string]func(store.Schedule) map[string]any{
		"create":       func(s store.Schedule) map[string]any { return decode(buildInjectBody(s, slot)) },
		"reuse create": func(s store.Schedule) map[string]any { return decode(buildReuseCreateBody(s, slot, "t")) },
	}
	for name, body := range bodies {
		m := body(base)
		if _, ok := m["schedule_delivery"]; ok || m["report_to"] != "conv1" || m["initial_prompt"] != "check" {
			t.Errorf("%s legacy: %v", name, m)
		}
		// The operator is not a target: no report_to, the targets ride schedule_delivery.
		s := base
		s.DeliverTo = "notifications,discord"
		m = body(s)
		d, _ := m["schedule_delivery"].(map[string]any)
		if m["report_to"] != "" || d == nil || !reflect.DeepEqual(d["targets"], []any{"notifications", "discord"}) || d["silent"] != false {
			t.Errorf("%s targets: report_to=%v delivery=%v", name, m["report_to"], d)
		}
		// Silent with reporting off: no targets, but the row is still needed for the sentinel,
		// and the note tells the agent about it.
		s = base
		s.Report, s.Silent = false, true
		m = body(s)
		d, _ = m["schedule_delivery"].(map[string]any)
		if m["report_to"] != "" || d == nil || d["silent"] != true || len(d["targets"].([]any)) != 0 {
			t.Errorf("%s silent: report_to=%v delivery=%v", name, m["report_to"], d)
		}
		p, _ := m["initial_prompt"].(string)
		if !strings.HasPrefix(p, scheduleSilentNote) || !strings.HasSuffix(p, "\n\ncheck") || !strings.Contains(p, scheduleSilentSentinel) {
			t.Errorf("%s silent prompt = %q", name, p)
		}
	}
	// The reuse /input body follows the same rules.
	s := base
	s.Silent = true
	m := decode(reuseSendBody(s, slot))
	if d, _ := m["schedule_delivery"].(map[string]any); m["report_to"] != "conv1" || d == nil || d["silent"] != true {
		t.Errorf("reuse send: %v", m)
	}
	if p, _ := m["prompt"].(string); !strings.HasPrefix(p, scheduleSilentNote) {
		t.Errorf("reuse send prompt = %q", p)
	}
	if m := decode(reuseSendBody(base, slot)); m["schedule_delivery"] != nil || m["prompt"] != "check" {
		t.Errorf("reuse send legacy: %v", m)
	}
}

// #1560: the Agent reports that a run answered with the sentinel. Only a plainly fired run
// becomes fired_silent; a failure is never overwritten, and nothing is notified.
func TestScheduleRunSilent(t *testing.T) {
	st, ctx := newSchedTestStore(t)
	mid := realMembership(t, st, ctx)
	api := newScheduleAPI(&manager{store: st})
	mv := store.MembershipView{MembershipID: mid, TenantID: "default"}
	rec := doJSON(api.create, mv, "POST", `{"spec_kind":"cron","spec":"*/5 * * * *","prompt":"x","silent":true}`, "")
	var dto scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	for _, r := range []store.ScheduleRun{
		{FiredAt: "2026-10-03T09:00:01Z", Slot: "2026-10-03T09:00:00Z", Status: "fired", Session: "s1"},
		{FiredAt: "2026-10-03T09:05:01Z", Slot: "2026-10-03T09:05:00Z", Status: "fired", Session: "s1"},
		{FiredAt: "2026-10-03T09:10:01Z", Slot: "2026-10-03T09:10:00Z", Status: "error:not executed", Session: "s1"},
	} {
		r.ID, r.ScheduleID, r.MembershipID = store.NewID(), dto.ID, mid
		if err := st.AppendScheduleRun(ctx, r, 50); err != nil {
			t.Fatal(err)
		}
	}
	r := doJSON(api.runSilent, mv, "POST", `{"session":"s1","slot":"2026-10-03T09:05:00Z"}`, dto.ID)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"changed":true`) {
		t.Fatalf("code=%d body=%s", r.Code, r.Body.String())
	}
	r = doJSON(api.runSilent, mv, "POST", `{"session":"s1","slot":"2026-10-03T09:10:00Z"}`, dto.ID)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"changed":false`) {
		t.Fatalf("failed run: code=%d body=%s", r.Code, r.Body.String())
	}
	runs, _ := st.ListScheduleRuns(ctx, dto.ID, mid, 50)
	got := map[string]string{}
	for _, rn := range runs {
		got[rn.Slot] = rn.Status
	}
	want := map[string]string{
		"2026-10-03T09:00:00Z": "fired",
		"2026-10-03T09:05:00Z": store.ScheduleStatusFiredSilent,
		"2026-10-03T09:10:00Z": "error:not executed",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	if notes, _ := st.ListNotifications(ctx, mid, "", 10); len(notes) != 0 {
		t.Fatalf("a silent run notified: %+v", notes)
	}
	if r := doJSON(api.runSilent, mv, "POST", `{"session":"s2","slot":"2026-10-03T09:05:00Z"}`, dto.ID); r.Code != http.StatusNotFound {
		t.Fatalf("unknown session code=%d", r.Code)
	}
	other := store.MembershipView{MembershipID: "someone-else", TenantID: "default"}
	if r := doJSON(api.runSilent, other, "POST", `{"session":"s1","slot":"2026-10-03T09:00:00Z"}`, dto.ID); r.Code != http.StatusNotFound {
		t.Fatalf("another member's schedule code=%d", r.Code)
	}
}
