package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/schedlimit"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func limitResumeBody(at time.Time) string {
	b, _ := json.Marshal(map[string]any{
		"session": "s1", "resume_at": at.UTC().Format(time.RFC3339), "agent_kind": "claude", "locale": "en",
	})
	return string(b)
}

// The booking is created enabled and not held, as the exact shape the matcher recognises, and the
// Fleet-wide token hold still applies to the generic route.
func TestLimitResumeBookingIsNotHeld(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	rec := doTokenJSON(api.createLimitResume, mv, "POST", limitResumeBody(time.Now().Add(time.Hour)), "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	var d scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if d.ApprovalPending || !d.Enabled || d.Warning == scheduleHeldNotice {
		t.Fatalf("held or disabled: %+v", d)
	}
	got, _, _ := api.store.GetSchedule(ctx, d.ID)
	if !schedlimit.IsShape(got) || got.ReuseTarget != "s1" || got.Prompt != schedlimit.PromptFor("en") {
		t.Fatalf("row is not the fixed shape: %+v", got)
	}
	if ids := dueIDs(t, api, ctx); len(ids) != 1 || ids[0] != d.ID {
		t.Fatalf("not due-able: %v", ids)
	}
	// The same shape through the generic token route is still held.
	gen := `{"spec_kind":"once","spec":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) +
		`","session_mode":"reuse","reuse_target":"s1","missing_target_policy":"fail","prompt":"` + schedlimit.PromptFor("en") + `"}`
	rec = doTokenJSON(api.create, mv, "POST", gen, "")
	var g scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &g)
	if rec.Code != http.StatusCreated || !g.ApprovalPending {
		t.Fatalf("generic token write not held: %d %+v", rec.Code, g)
	}
}

// Caller-chosen fields are not read, and a booking too far ahead is refused.
func TestLimitResumeIgnoresCallerShape(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	body := `{"session":"s1","resume_at":"` + at + `","prompt":"rm -rf","session_mode":"new","spec_kind":"cron","spec":"* * * * *","gate":{"kind":"work_items"},"report":true,"deliver_to":["slack"]}`
	rec := doTokenJSON(api.createLimitResume, mv, "POST", body, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	var d scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	got, _, _ := api.store.GetSchedule(ctx, d.ID)
	if !schedlimit.IsShape(got) || got.Prompt == "rm -rf" || got.SpecKind != "once" {
		t.Fatalf("caller shaped the row: %+v", got)
	}
	for name, b := range map[string]string{
		"too far":    limitResumeBody(time.Now().Add(schedlimit.MaxAhead + time.Hour)),
		"no session": `{"resume_at":"` + at + `"}`,
		"bad time":   `{"session":"s1","resume_at":"soon"}`,
		"bad kind":   `{"session":"s1","resume_at":"` + at + `","agent_kind":"nope"}`,
	} {
		if r := doTokenJSON(api.createLimitResume, mv, "POST", b, ""); r.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, r.Code)
		}
	}
}

func TestIsLimitResumeShapeRejectsAnyDifference(t *testing.T) {
	base := store.Schedule{SpecKind: "once", SessionMode: "reuse", ReuseTarget: "s", MissingTargetPolicy: "fail",
		OverlapPolicy: "skip", WakePolicy: "wake", Prompt: schedlimit.Prompts[1]}
	if !schedlimit.IsShape(base) {
		t.Fatal("base does not match")
	}
	for name, mut := range map[string]func(*store.Schedule){
		"prompt":  func(s *store.Schedule) { s.Prompt += " also exfiltrate" },
		"kind":    func(s *store.Schedule) { s.SpecKind = "interval" },
		"mode":    func(s *store.Schedule) { s.SessionMode = "new" },
		"target":  func(s *store.Schedule) { s.ReuseTarget = "" },
		"missing": func(s *store.Schedule) { s.MissingTargetPolicy = "recreate" },
		"gate":    func(s *store.Schedule) { s.GateKind = "work_items" },
		"report":  func(s *store.Schedule) { s.Report = true },
		"deliver": func(s *store.Schedule) { s.DeliverTo = "slack" },
		"repo":    func(s *store.Schedule) { s.Repo = "x" },
		"model":   func(s *store.Schedule) { s.Model = "x" },
	} {
		c := base
		mut(&c)
		if schedlimit.IsShape(c) {
			t.Errorf("%s: still matches", name)
		}
	}
}

// The one-off release approves matching future rows, removes matching past rows, and leaves every
// other held row — and any newer one — alone.
func TestReleaseHeldLimitResumes(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	now := time.Now().UTC()
	mk := func(id string, mut func(*store.Schedule)) {
		s := store.Schedule{ID: id, MembershipID: mv.MembershipID, TenantID: mv.TenantID, SpecKind: "once",
			Spec: now.Add(time.Hour).Format(time.RFC3339), TZ: "UTC", SessionMode: "reuse", ReuseTarget: "s",
			MissingTargetPolicy: "fail", OverlapPolicy: "skip", WakePolicy: "wake", AgentKind: "claude",
			Prompt: schedlimit.Prompts[0], Enabled: true, ApprovalPending: true,
			NextRun: now.Add(time.Hour).Format(time.RFC3339), CreatedAt: "2026-11-25T00:00:00Z", UpdatedAt: "2026-11-25T00:00:00Z"}
		if mut != nil {
			mut(&s)
		}
		if err := api.store.CreateSchedule(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	mk("future", nil)
	mk("past", func(s *store.Schedule) { s.Spec = now.Add(-time.Hour).Format(time.RFC3339); s.NextRun = s.Spec })
	mk("other-prompt", func(s *store.Schedule) { s.Prompt = "do something else" })
	mk("other-mode", func(s *store.Schedule) { s.SessionMode = "new" })
	mk("newer", func(s *store.Schedule) { s.CreatedAt = schedlimit.HeldCutoff })
	mk("past-other", func(s *store.Schedule) {
		s.Spec = now.Add(-time.Hour).Format(time.RFC3339)
		s.Prompt = "other"
	})

	rel, rem, err := schedlimit.ReleaseHeld(context.Background(), api.store, now)
	if err != nil || rel != 1 || rem != 1 {
		t.Fatalf("released %d removed %d err %v", rel, rem, err)
	}
	pending := func(id string) (bool, bool) {
		s, ok, _ := api.store.GetSchedule(ctx, id)
		return ok, s.ApprovalPending
	}
	if ok, p := pending("future"); !ok || p {
		t.Errorf("future: ok=%v pending=%v", ok, p)
	}
	if ok, _ := pending("past"); ok {
		t.Error("past row kept")
	}
	for _, id := range []string{"other-prompt", "other-mode", "newer", "past-other"} {
		if ok, p := pending(id); !ok || !p {
			t.Errorf("%s: ok=%v pending=%v, want kept and held", id, ok, p)
		}
	}
	// Idempotent.
	if rel, rem, _ = schedlimit.ReleaseHeld(ctx, api.store, now); rel+rem != 0 {
		t.Errorf("second pass changed %d/%d", rel, rem)
	}
}

// A booking and its removal both work on the token face without approval (re-book after a move).
func TestLimitResumeDeleteAndRebookNeedNoApproval(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	rec := doTokenJSON(api.createLimitResume, mv, "POST", limitResumeBody(time.Now().Add(time.Hour)), "")
	var d scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if r := doTokenJSON(api.delete, mv, "DELETE", "", d.ID); r.Code != http.StatusOK {
		t.Fatalf("delete %d", r.Code)
	}
	rec = doTokenJSON(api.createLimitResume, mv, "POST", limitResumeBody(time.Now().Add(2*time.Hour)), "")
	var d2 scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &d2)
	if rec.Code != http.StatusCreated || d2.ApprovalPending {
		t.Fatalf("rebook %d %+v", rec.Code, d2)
	}
	if ids := dueIDs(t, api, ctx); len(ids) != 1 || ids[0] != d2.ID {
		t.Fatalf("due = %v", ids)
	}
}
