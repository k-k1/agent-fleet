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
// the exact text, and a test there reads this file to keep the two in step. Index 0/1 are the
// current ja/en wording; the rest are what older Agents booked, which only the release matcher accepts.
var Prompts = [...]string{
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

// HeldCutoff bounds the one-off release below: only rows created before it match. A row the token
// writes after this build is held for good, so the pass is safe to run on every start.
const HeldCutoff = "2026-11-26T00:30:00Z"

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

// ReleaseHeld is the one-off for rows #4631 held before this exemption existed: a
// matching row created before HeldCutoff is approved when its time is still ahead and
// deleted when it is past (a past once-schedule cannot be approved and would only linger as a
// "do not approve" card). Rows of any other shape, and anything newer, are left held.
func ReleaseHeld(ctx context.Context, st store.ScheduleStore, now time.Time) (released, removed int, err error) {
	rows, err := st.ListApprovalPendingSchedules(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, s := range rows {
		if !IsShape(s) || s.CreatedAt == "" || s.CreatedAt >= HeldCutoff {
			continue
		}
		at, perr := time.Parse(time.RFC3339, s.Spec)
		if perr != nil {
			continue
		}
		if !at.After(now) {
			if derr := st.DeleteSchedule(ctx, s.ID, s.MembershipID); derr != nil {
				return released, removed, derr
			}
			removed++
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
