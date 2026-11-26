// Package schedlimit holds what the CP knows about the Agent's usage-limit auto-resume booking
// (ADR 0021, note 2026-11-26): the fixed prompts, the exact shape of such a row, and the one-off
// release of rows #4631 held before the exemption existed.
//
// Why a booking is exempt from the approval hold: the schedule token is shared by every process of
// the workspace, so the CP cannot tell the Agent from a session or the Fleet Operator, and holds
// every token write (#4631). Held, the resume of a limited session never fires on its own, and a
// queue run that reaches the limit stalls with its slot. The exemption cannot be a flag the caller
// sets: the CP builds the whole row itself from a session name, an instant and an agent kind, plus a
// locale that picks one of two fixed prompts. A token holder can at most book a once-nudge of the
// fixed text into a session of its own membership, no later than the longest limit window.
package schedlimit

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// MaxAhead is the furthest a booking may lie ahead: the weekly window plus a day of slack.
const MaxAhead = 8 * 24 * time.Hour

// Prompts are the fixed nudges. They are copies of the Agent's wording
// (workspace/agent/internal/sessionx/rate_limit_resume.go): the Agent recognises its own resume by
// the exact text, and a test there reads this file to keep the two in step. Index 0 and 1 are the
// current ja and en wording, which opens with the notice line so the session can tell the nudge
// from its user typing (the line carries no instruction). The rest are what earlier builds
// booked, with no notice line; only the release matcher accepts them.
var Prompts = [...]string{
	"[agent-fleet:notice kind=limit-resume reply=none] " +
		"利用上限がリセットされました。上限で中断した作業を、止まったところから続けてください。" +
		"直前の依頼が着手前に上限で断られ、まだ何も実行していない場合は、その依頼を最初から実行してください" +
		"（途中まで実行していた場合は、その続きから再開してください）。" +
		"これは自動再開なので新しい指示はありません。" +
		"それ以外でどこで止まったか分からない場合は、新しい作業を始めずにその旨を伝えてください。",
	"[agent-fleet:notice kind=limit-resume reply=none] " +
		"The usage limit has reset. Continue the work that was cut off, from where it stopped. " +
		"If the limit refused the latest request before you started it and you have done none of it yet, " +
		"carry it out from the beginning; if you had done part of it, continue from there instead. " +
		"This is an automatic resume — there is no new instruction. " +
		"Otherwise, if you cannot tell where it stopped, say so instead of starting something new.",
	"利用上限がリセットされました。上限で中断した作業を、止まったところから続けてください。" +
		"直前の依頼が着手前に上限で断られ、まだ何も実行していない場合は、その依頼を最初から実行してください" +
		"（途中まで実行していた場合は、その続きから再開してください）。" +
		"これは自動再開なので新しい指示はありません。" +
		"それ以外でどこで止まったか分からない場合は、新しい作業を始めずにその旨を伝えてください。",
	"The usage limit has reset. Continue the work that was cut off, from where it stopped. " +
		"If the limit refused the latest request before you started it and you have done none of it yet, " +
		"carry it out from the beginning; if you had done part of it, continue from there instead. " +
		"This is an automatic resume — there is no new instruction. " +
		"Otherwise, if you cannot tell where it stopped, say so instead of starting something new.",
	"The usage limit has reset. Continue the work that was cut off, from where it stopped. " +
		"This is an automatic resume — there is no new instruction. " +
		"If you cannot tell where it stopped, say so instead of starting something new.",
	"利用上限がリセットされました。上限で中断した作業を、止まったところから続けてください。" +
		"これは自動再開なので新しい指示はありません。" +
		"どこで止まったか分からない場合は、新しい作業を始めずにその旨を伝えてください。",
}

// currentPrompts is how many leading entries of Prompts a new booking may use.
const currentPrompts = 2

// PromptFor is the fixed prompt for a locale; anything but "en" is Japanese, as the Agent's is.
func PromptFor(locale string) string {
	if locale == "en" {
		return Prompts[1]
	}
	return Prompts[0]
}

// Label is the row's display label.
func Label(locale, session string) string {
	if locale == "en" {
		return "auto-resume after usage limit (" + session + ")"
	}
	return "利用上限リセット後の自動再開（" + session + "）"
}

// IsShape reports whether a stored schedule is exactly a usage-limit resume: the shape the CP's
// builder makes, with any of the fixed prompts. Nothing else is ever released.
func IsShape(s store.Schedule) bool {
	prompt := strings.TrimSpace(s.Prompt)
	known := false
	for _, p := range Prompts {
		if prompt == strings.TrimSpace(p) {
			known = true
		}
	}
	return known && s.SpecKind == "once" && s.SessionMode == "reuse" && s.ReuseTarget != "" &&
		s.MissingTargetPolicy == "fail" && s.OverlapPolicy == "skip" && s.WakePolicy == "wake" &&
		!s.Report && !s.StopAfterRun && !s.Silent && s.GateKind == "" && s.GateSpec == "" &&
		s.OwnerConv == "" && s.Rotation == "" && s.Repo == "" && s.Worktree == "" && !s.NewBranch &&
		s.Model == "" && s.Route == "" && s.RouteConnection == "" && (s.DeliverTo == "" || s.DeliverTo == "operator")
}

// IsBooking is IsShape plus the Agent's own label for that very session. It is what the token face
// treats as a usage-limit resume (no run-now, no resume): a member's own approved row that merely
// shares the fixed prompt and policies, under a label of its own, is an ordinary schedule.
func IsBooking(s store.Schedule) bool {
	return IsShape(s) && (s.SpecLabel == Label("ja", s.ReuseTarget) || s.SpecLabel == Label("en", s.ReuseTarget))
}

// oneOffName is the marker under which the release pass is recorded as done.
const oneOffName = "limit-resume-release-5212"

// legacyBooking reports whether a held row is what an earlier Agent's generic POST left behind:
// the fixed shape, an Agent-written label for that very session, never shown in sessions, never
// fired or adopted a session, and a time from its creation up to the window after it. A row a
// member renamed, or anything else that cannot be shown to have that origin, stays held.
func legacyBooking(s store.Schedule) bool {
	// DeliverTo is empty: an earlier Agent never sent it, so an explicit "operator" is someone else's row.
	if !IsBooking(s) || s.DeliverTo != "" || s.ShowInSessions || s.ReuseSession != "" || s.LastRun != "" || s.LastStatus != "" {
		return false
	}
	created, cerr := time.Parse(time.RFC3339, s.CreatedAt)
	at, aerr := time.Parse(time.RFC3339, s.Spec)
	// An Agent books a moment ahead of the creation, never before it.
	return cerr == nil && aerr == nil && !at.Before(created) && !at.After(created.Add(MaxAhead))
}

// ReleaseHeld is the one-off for rows #4631 held before the exemption existed, run once per
// database (the claim row records it, so a held row written afterwards is never swept in). A row
// matching legacyBooking is approved when its time is still ahead and deleted when it is past (a
// past once-schedule cannot be approved and would only linger as a "do not approve" card). Both
// writes are conditional on the version read, so an edit or approval made meanwhile — an older CP
// may still be serving during a rolling update — leaves the row alone. Everything else stays held.
func ReleaseHeld(ctx context.Context, st store.ScheduleStore, now time.Time) (released, removed int, err error) {
	// Claim first, atomically: of several CPs starting on one database exactly one runs the pass, and
	// it reads its candidates only after the claim, so a row written by the generic route once the
	// pass has begun elsewhere is never a candidate. A pass that fails half-way is not retried: the
	// rows it did not reach stay held, which is where they started.
	if claimed, cerr := st.ClaimOneOff(ctx, oneOffName, store.NowTS()); cerr != nil || !claimed {
		return 0, 0, cerr
	}
	rows, err := st.ListApprovalPendingSchedules(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, s := range rows {
		if !legacyBooking(s) {
			continue
		}
		at, _ := time.Parse(time.RFC3339, s.Spec)
		if !at.After(now) {
			ok, derr := st.DeleteHeldSchedule(ctx, s.ID, s.MembershipID, s.Generation)
			if derr != nil {
				return released, removed, derr
			}
			if ok {
				removed++
			}
			continue
		}
		next := ""
		if s.Enabled {
			next = at.UTC().Format(time.RFC3339)
		}
		ok, aerr := st.ApproveSchedule(ctx, s.ID, s.MembershipID, s.Generation, next, store.NowTS())
		if aerr != nil {
			return released, removed, aerr
		}
		if ok {
			released++
		}
	}
	if released+removed > 0 {
		log.Printf("schedules: released %d and removed %d held usage-limit resume(s) (#5212)", released, removed)
	}
	return released, removed, nil
}
