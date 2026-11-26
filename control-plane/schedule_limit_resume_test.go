package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// legacyHeld is a row as an earlier Agent's generic POST left it: Agent-written label, fixed shape,
// held, never fired.
func legacyHeld(mv store.MembershipView, id string, spec time.Time, mut func(*store.Schedule)) store.Schedule {
	s := store.Schedule{ID: id, MembershipID: mv.MembershipID, TenantID: mv.TenantID, SpecKind: "once",
		Spec: spec.UTC().Format(time.RFC3339), TZ: "UTC", SessionMode: "reuse", ReuseTarget: "s",
		SpecLabel:           schedlimit.Label("ja", "s"),
		MissingTargetPolicy: "fail", OverlapPolicy: "skip", WakePolicy: "wake", AgentKind: "claude",
		Prompt: schedlimit.Prompts[2], Enabled: true, ApprovalPending: true,
		NextRun: spec.UTC().Format(time.RFC3339), CreatedAt: time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339), UpdatedAt: "x"}
	if mut != nil {
		mut(&s)
	}
	return s
}

// The one-off release approves legacy rows still ahead, removes legacy rows already past, and
// leaves every other held row alone — a renamed row, another prompt or shape, a time beyond the
// window, a row that has run — and then never runs again.
func TestReleaseHeldLimitResumes(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	now := time.Now().UTC()
	mk := func(id string, spec time.Time, mut func(*store.Schedule)) {
		if err := api.store.CreateSchedule(ctx, legacyHeld(mv, id, spec, mut)); err != nil {
			t.Fatal(err)
		}
	}
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	mk("future", future, nil)
	mk("future-en", future, func(s *store.Schedule) { s.SpecLabel = schedlimit.Label("en", "s"); s.Prompt = schedlimit.Prompts[3] })
	mk("past", past, nil)
	mk("edge", now.Add(-3*time.Hour).Add(schedlimit.MaxAhead), nil) // exactly the window after creation
	mk("renamed", future, func(s *store.Schedule) { s.SpecLabel = "my nightly nudge" })
	mk("past-renamed", past, func(s *store.Schedule) { s.SpecLabel = "my nightly nudge" })
	mk("other-prompt", future, func(s *store.Schedule) { s.Prompt = "do something else" })
	mk("other-mode", future, func(s *store.Schedule) { s.SessionMode = "new" })
	mk("beyond-window", now.Add(schedlimit.MaxAhead+48*time.Hour), nil)
	mk("before-creation", now.Add(-5*time.Hour), nil)
	mk("operator", past, func(s *store.Schedule) { s.DeliverTo = "operator" })
	mk("noticed", future, func(s *store.Schedule) { s.Prompt = schedlimit.Prompts[0] }) // the new wording is never an old Agent's
	mk("noticed-past", past, func(s *store.Schedule) { s.Prompt = schedlimit.Prompts[1]; s.SpecLabel = schedlimit.Label("en", "s") })
	mk("after-claim", future, func(s *store.Schedule) { s.CreatedAt = now.Add(time.Second).Format(time.RFC3339) })
	mk("shown", future, func(s *store.Schedule) { s.ShowInSessions = true })
	mk("ran", past, func(s *store.Schedule) { s.LastRun = "2026-11-01T00:00:00Z"; s.LastStatus = "ok" })
	mk("adopted", future, func(s *store.Schedule) { s.ReuseSession = "other-session" })

	rel, rem, err := schedlimit.ReleaseHeld(ctx, api.store, now)
	if err != nil || rel != 3 || rem != 1 {
		t.Fatalf("released %d removed %d err %v", rel, rem, err)
	}
	state := func(id string) (bool, bool) {
		s, ok, _ := api.store.GetSchedule(ctx, id)
		return ok, s.ApprovalPending
	}
	for _, id := range []string{"future", "future-en", "edge"} {
		if ok, p := state(id); !ok || p {
			t.Errorf("%s: ok=%v pending=%v, want released", id, ok, p)
		}
	}
	if ok, _ := state("past"); ok {
		t.Error("past row kept")
	}
	for _, id := range []string{"renamed", "past-renamed", "other-prompt", "other-mode", "beyond-window", "noticed", "noticed-past", "after-claim", "before-creation", "operator", "shown", "ran", "adopted"} {
		if ok, p := state(id); !ok || !p {
			t.Errorf("%s: ok=%v pending=%v, want kept and held", id, ok, p)
		}
	}
	// Once only: a held row of the same shape written afterwards is not swept in.
	mk("later", future, nil)
	if rel, rem, _ = schedlimit.ReleaseHeld(ctx, api.store, now); rel+rem != 0 {
		t.Errorf("second pass changed %d/%d", rel, rem)
	}
	if ok, p := state("later"); !ok || !p {
		t.Errorf("later: ok=%v pending=%v, want held", ok, p)
	}
}

// A delete or approval that lost the race to an edit made after the read leaves the row alone.
func TestHeldDeleteIsConditionalOnTheVersionRead(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	if err := api.store.CreateSchedule(ctx, legacyHeld(mv, "r", time.Now().Add(-time.Hour), nil)); err != nil {
		t.Fatal(err)
	}
	seen, _, _ := api.store.GetSchedule(ctx, "r")
	edited := seen
	edited.Prompt = "edited by the member"
	if err := api.store.UpdateSchedule(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if ok, err := api.store.DeleteHeldSchedule(ctx, "r", mv.MembershipID, seen.Generation); err != nil || ok {
		t.Fatalf("stale delete: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := api.store.GetSchedule(ctx, "r"); !ok {
		t.Fatal("the edited row was deleted")
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

// Each new booking opens with the notice line, the locale picks the wording, and the pick is not
// reversed: a pair of identical or swapped prompts would still be "a prompt that starts with it".
func TestLimitResumePromptsCarryTheNotice(t *testing.T) {
	const notice = "[agent-fleet:notice kind=limit-resume reply=none] "
	en, ja := schedlimit.PromptFor("en"), schedlimit.PromptFor("ja")
	if !strings.HasPrefix(en, notice+"The usage limit has reset.") || !strings.HasPrefix(ja, notice+"利用上限がリセットされました。") {
		t.Fatalf("en=%q ja=%q", en, ja)
	}
	if schedlimit.PromptFor("") != ja || schedlimit.PromptFor("fr") != ja {
		t.Fatal("an unknown locale is not Japanese")
	}
	api, ctx, mv := newSchedAPITest(t)
	for loc, want := range map[string]string{"en": en, "ja": ja} {
		b, _ := json.Marshal(map[string]any{"session": "s-" + loc, "resume_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "locale": loc})
		rec := doTokenJSON(api.createLimitResume, mv, "POST", string(b), "")
		var d scheduleDTO
		_ = json.Unmarshal(rec.Body.Bytes(), &d)
		got, _, _ := api.store.GetSchedule(ctx, d.ID)
		if got.Prompt != want {
			t.Errorf("%s: stored prompt %q", loc, got.Prompt)
		}
	}
}

// The token face cannot fire or re-arm a booking, and an edit that changes what it does puts it
// back on hold, whether the row came from the new route or from the release of an old one.
func TestLimitResumeRowsAreFixedOnTheTokenFace(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	rec := doTokenJSON(api.createLimitResume, mv, "POST", limitResumeBody(time.Now().Add(time.Hour)), "")
	var d scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if err := api.store.CreateSchedule(ctx, legacyHeld(mv, "legacy", time.Now().Add(time.Hour), func(s *store.Schedule) { s.ApprovalPending = false })); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{d.ID, "legacy"} {
		if r := doTokenJSON(api.runNow, mv, "POST", "", id); r.Code != http.StatusConflict {
			t.Errorf("%s run-now on the token face: %d", id, r.Code)
		}
		if r := doTokenJSON(api.resume, mv, "POST", "", id); r.Code != http.StatusConflict {
			t.Errorf("%s resume on the token face: %d", id, r.Code)
		}
		for name, patch := range map[string]string{
			"time":   `{"spec":"` + time.Now().Add(2*time.Hour).UTC().Format(time.RFC3339) + `"}`,
			"target": `{"reuse_target":"other"}`,
			"prompt": `{"prompt":"something else"}`,
		} {
			r := doTokenJSON(api.update, mv, "PATCH", patch, id)
			if r.Code != http.StatusOK {
				t.Fatalf("%s %s: %d %s", id, name, r.Code, r.Body.String())
			}
			got, _, _ := api.store.GetSchedule(ctx, id)
			if !got.ApprovalPending {
				t.Errorf("%s: a token edit of the %s did not put it back on hold", id, name)
			}
			got.ApprovalPending = false // un-hold for the next edit
			got.Prompt, got.ReuseTarget = schedlimit.Prompts[1], "s"
			_ = api.store.UpdateSchedule(ctx, got)
		}
	}
	// The Console face (the member) can still run it.
	if r := doJSON(api.runNow, mv, "POST", "", d.ID); r.Code == http.StatusConflict {
		t.Errorf("the member's run-now was refused: %s", r.Body.String())
	}
}

// Through the real mux: a valid token books, a wrong one is refused, and the booking is not held.
func TestLimitResumeRouteThroughTheMux(t *testing.T) {
	e := newInboxEnv(t)
	tok := mintScheduleToken(scheduleSignKey(e.mgr.tokenSigningRoot()), e.members["a"])
	call := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/internal/schedules/limit-resume", strings.NewReader(limitResumeBody(time.Now().Add(time.Hour))))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, req)
		return rec
	}
	for name, auth := range map[string]string{"none": "", "garbage": "afs_nope.nope", "other credential": "x." + tok} {
		if rec := call(auth); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, rec.Code)
		}
	}
	rec := call(tok)
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid token: %d %s", rec.Code, rec.Body.String())
	}
	if d := decode[scheduleDTO](t, rec); d.ApprovalPending || !d.Enabled {
		t.Fatalf("held or disabled: %+v", d)
	}
}

// Two CPs starting on one database: only the first claim runs the pass.
func TestReleaseHeldClaimsOnce(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	if err := api.store.CreateSchedule(ctx, legacyHeld(mv, "a", time.Now().Add(time.Hour), nil)); err != nil {
		t.Fatal(err)
	}
	if ok, err := api.store.ClaimOneOff(ctx, "limit-resume-release-5212", store.NowTS()); err != nil || !ok {
		t.Fatalf("first claim ok=%v err=%v", ok, err)
	}
	// Another CP found the claim: it runs nothing, even though a candidate is there.
	if rel, rem, err := schedlimit.ReleaseHeld(ctx, api.store, time.Now()); err != nil || rel+rem != 0 {
		t.Fatalf("second runner released %d removed %d err %v", rel, rem, err)
	}
	if s, _, _ := api.store.GetSchedule(ctx, "a"); !s.ApprovalPending {
		t.Fatal("a claimed pass was run again")
	}
}

// A booking whose reply was lost is sent again: the CP answers with the live row instead of a
// second one; after an explicit delete the same instant books afresh.
func TestLimitResumeRepeatedBookingReturnsTheLiveRow(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	at := time.Now().Add(time.Hour)
	var first, again, fresh scheduleDTO
	r1 := doTokenJSON(api.createLimitResume, mv, "POST", limitResumeBody(at), "")
	_ = json.Unmarshal(r1.Body.Bytes(), &first)
	r2 := doTokenJSON(api.createLimitResume, mv, "POST", limitResumeBody(at), "")
	_ = json.Unmarshal(r2.Body.Bytes(), &again)
	if r1.Code != http.StatusCreated || r2.Code != http.StatusOK || again.ID != first.ID {
		t.Fatalf("first %d %s, repeat %d %s", r1.Code, first.ID, r2.Code, again.ID)
	}
	if rows, _ := api.store.ListSchedules(ctx, mv.MembershipID); len(rows) != 1 {
		t.Fatalf("%d rows after a repeated booking", len(rows))
	}
	if r := doTokenJSON(api.delete, mv, "DELETE", "", first.ID); r.Code != http.StatusOK {
		t.Fatalf("delete %d", r.Code)
	}
	r3 := doTokenJSON(api.createLimitResume, mv, "POST", limitResumeBody(at), "")
	_ = json.Unmarshal(r3.Body.Bytes(), &fresh)
	if r3.Code != http.StatusCreated || fresh.ID == first.ID {
		t.Fatalf("re-book after delete: %d %s", r3.Code, fresh.ID)
	}
}

// Only the Agent's own booking is fixed on the token face; a member's approved row that shares the
// prompt and policies under a label of its own is an ordinary schedule, as on main.
func TestMemberApprovedLookalikeStaysRunnableOnTheTokenFace(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	row := legacyHeld(mv, "mine", time.Now().Add(time.Hour), func(s *store.Schedule) {
		s.ApprovalPending = false
		s.SpecLabel = "my own nudge"
	})
	if err := api.store.CreateSchedule(ctx, row); err != nil {
		t.Fatal(err)
	}
	if r := doTokenJSON(api.runNow, mv, "POST", "", "mine"); r.Code == http.StatusConflict && strings.Contains(r.Body.String(), "limit_resume_fixed") {
		t.Fatalf("an ordinary approved row was refused: %s", r.Body.String())
	}
}

// A row that went through the release pass is as fixed on the token face as a new booking.
func TestReleasedRowIsFixedOnTheTokenFace(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	if err := api.store.CreateSchedule(ctx, legacyHeld(mv, "old", time.Now().Add(time.Hour), nil)); err != nil {
		t.Fatal(err)
	}
	if rel, _, err := schedlimit.ReleaseHeld(ctx, api.store, time.Now()); err != nil || rel != 1 {
		t.Fatalf("release %d %v", rel, err)
	}
	if r := doTokenJSON(api.runNow, mv, "POST", "", "old"); r.Code != http.StatusConflict {
		t.Errorf("run-now on a released row: %d", r.Code)
	}
}

// A resend whose time drifted by seconds (the Agent's "now + 2 minutes") still finds the live row;
// a booking for the same session hours later is a different one.
func TestLimitResumeResendWithDriftedTimeReturnsTheLiveRow(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	at := time.Now().Add(2 * time.Minute)
	var a, b, c scheduleDTO
	_ = json.Unmarshal(doTokenJSON(api.createLimitResume, mv, "POST", limitResumeBody(at), "").Body.Bytes(), &a)
	_ = json.Unmarshal(doTokenJSON(api.createLimitResume, mv, "POST", limitResumeBody(at.Add(40*time.Second)), "").Body.Bytes(), &b)
	_ = json.Unmarshal(doTokenJSON(api.createLimitResume, mv, "POST", limitResumeBody(at.Add(3*time.Hour)), "").Body.Bytes(), &c)
	if a.ID == "" || b.ID != a.ID || c.ID == a.ID || c.ID == "" {
		t.Fatalf("ids %q %q %q", a.ID, b.ID, c.ID)
	}
	if rows, _ := api.store.ListSchedules(ctx, mv.MembershipID); len(rows) != 2 {
		t.Fatalf("%d rows", len(rows))
	}
}
