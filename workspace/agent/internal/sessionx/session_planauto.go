package sessionx

// "Review automatically, up to N rounds" (#2810): the Agent-side loop behind the Console's
// "Review again in <reviewer>". When the planner re-presents a plan and the reviewer's latest
// reply said changes-requested, the revised plan goes to the same reviewer without a click.
//
// It runs here, not in the Console, so it survives a closed tab, and its whole state is the
// PlanAuto on the planner's PlanReviewers link (session.PlanAuto), so the round count survives
// an Agent restart and is bounded by it.
//
// The loop NEVER approves. Every way out (approve, the round limit, any failure) clears
// Enabled and leaves the last plan on an ordinary approval card for the member.
//
// Two events drive it:
//
//	reply  — a peer message from the linked reviewer to the planner (HandleSessionInput), whose
//	         `## Verdict` section is read by parsePlanVerdict. Only changes-requested arms the
//	         loop; approve ends it; anything that cannot be read unambiguously ends it too.
//	plan   — the planner raised its plan approval (the status hook, a separate process, kicks the
//	         daemon over /sessions/{name}/plan-presented). planAutoOnPlan claims one round with a
//	         compare-and-swap on the meta, so a duplicated kick sends nothing twice.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

const (
	planAutoDefaultMax = 3
	// planAutoMaxRounds caps what a member can ask for: each round is a full review.
	planAutoMaxRounds = 10
	// planAutoVerdictTTL: a changes-requested older than this is not acted on. Without it a
	// reply forgotten in a planner session that is revived days later would send a round.
	planAutoVerdictTTL = 12 * time.Hour
	planAutoMaxPrompt  = 4096

	verdictApprove = "approve"
	verdictChanges = "changes-requested"
)

// planAutoDefaultPrompt is used when the Console sent no (or an unusable) template.
const planAutoDefaultPrompt = "The plan was revised (revision {rev}). Review it again.\n\n" +
	"- Plan: {path} (read it; do not edit it)\n\n" +
	"Compare it with your earlier findings. Do not re-raise items the planner kept with a sound reason.\n" +
	"Answer in the same format as before (Verdict / Findings), sent to {session} with send_to_peer_session."

var (
	// The heading is localized by the Console (plan.review_verdict_heading): accept both shipped
	// languages. Anything else is "no verdict", which stops the loop.
	verdictHeadingRe = regexp.MustCompile(`(?i)^ {0,3}#{1,6}[ \t]*(?:verdict|判定)[ \t]*[:：]?[ \t]*#*[ \t]*$`)
	anyHeadingRe     = regexp.MustCompile(`^ {0,3}#{1,6}[ \t]`)
	verdictWordRe    = regexp.MustCompile(`(?i)(?:^|[^a-z-])(?:approve[ds]?|changes[ -]requested)(?:$|[^a-z-])`)
)

// parsePlanVerdict reads the verdict of a reviewer's reply from its `## Verdict` section and
// nothing else: "approve", "changes-requested", or "" when it cannot be told unambiguously
// (no section, two sections, the template line "approve | changes-requested" left as it was, a
// section that names the other verdict too). "" is never read as approve.
//
// Prose elsewhere in the message ("I would approve this once X") is ignored on purpose; fenced
// code is skipped so a quoted template cannot count as a second heading.
func parsePlanVerdict(body string) string {
	var section []string
	headings := 0
	inFence := false
	inSection := false
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			if inSection {
				section = append(section, "```") // a fence inside the section: not a plain verdict
			}
			continue
		}
		if inFence {
			if inSection {
				section = append(section, t)
			}
			continue
		}
		if verdictHeadingRe.MatchString(line) {
			headings++
			inSection = headings == 1
			continue
		}
		if anyHeadingRe.MatchString(line) {
			inSection = false
			continue
		}
		if inSection && t != "" {
			section = append(section, t)
		}
	}
	if headings != 1 || len(section) == 0 {
		return ""
	}
	first := strings.ToLower(strings.Trim(strings.TrimSpace(section[0]), "*_`"))
	first = strings.TrimRight(first, ".。 ")
	var v string
	switch first {
	case verdictApprove:
		v = verdictApprove
	case verdictChanges:
		v = verdictChanges
	default:
		return ""
	}
	for _, rest := range section[1:] {
		if rest == "```" || verdictWordRe.MatchString(rest) {
			return ""
		}
	}
	return v
}

// planFingerprint is a short id of the plan text (trailing whitespace ignored): equal for equal
// text, different for a revision. It rides in the follow-up so the peer duplicate drop (same
// body to the same target within 2 minutes) never swallows a real second revision.
func planFingerprint(plan string) string {
	lines := strings.Split(strings.ReplaceAll(plan, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(strings.Join(lines, "\n"))))
	return hex.EncodeToString(sum[:])[:12]
}

// planAutoPromptOf validates the Console's template: usable text that names the plan path.
func planAutoPromptOf(tmpl string) string {
	if tmpl == "" || len(tmpl) > planAutoMaxPrompt || !utf8.ValidString(tmpl) || !strings.Contains(tmpl, "{path}") {
		return planAutoDefaultPrompt
	}
	return tmpl
}

func renderPlanAutoPrompt(tmpl, planner, path, rev string) string {
	return strings.NewReplacer("{session}", planner, "{path}", path, "{rev}", rev).Replace(planAutoPromptOf(tmpl))
}

// planAutoStop ends the loop recorded in a, for `reason`.
func planAutoStop(a *session.PlanAuto, reason string) {
	a.Enabled = false
	a.Verdict, a.VerdictAt = "", ""
	a.Stop = reason
}

// stopPlanAuto ends the loop of one link and records why.
func stopPlanAuto(planner, path, reason string) {
	log.Printf("plan auto-review: %s %s: stopped (%s)", planner, path, reason)
	UpdateSessionMeta(planner, func(m *session.Meta) bool {
		for i := range m.PlanReviewers {
			if a := m.PlanReviewers[i].Auto; m.PlanReviewers[i].Path == path && a != nil && a.Enabled {
				cp := *a
				planAutoStop(&cp, reason)
				m.PlanReviewers[i].Auto = &cp
				return true
			}
		}
		return false
	})
}

// notePlanReviewerReply records the verdict of a peer message `from` sent to planner `dst`.
// It acts only for a reviewer linked to one of dst's plans with the loop on, so an ordinary
// peer message costs one read of the meta already in hand and no write.
func notePlanReviewerReply(dst session.Meta, from, body string) {
	watching := false
	for _, e := range dst.PlanReviewers {
		if e.Reviewer == from && e.Auto != nil && e.Auto.Enabled {
			watching = true
		}
	}
	if !watching {
		return
	}
	verdict := parsePlanVerdict(body)
	now := time.Now()
	UpdateSessionMeta(dst.Name, func(m *session.Meta) bool {
		var hit []int
		for i, e := range m.PlanReviewers {
			if e.Reviewer == from && e.Auto != nil && e.Auto.Enabled {
				hit = append(hit, i)
			}
		}
		if len(hit) == 0 {
			return false
		}
		for _, i := range hit {
			cp := *m.PlanReviewers[i].Auto
			switch {
			case len(hit) > 1:
				// One reviewer on two plans of the same planner: the reply cannot be matched to a plan.
				planAutoStop(&cp, "ambiguous_reviewer")
			case verdict == verdictApprove:
				planAutoStop(&cp, "approved")
			case verdict == verdictChanges:
				cp.Verdict, cp.VerdictAt = verdictChanges, now.Format(time.RFC3339)
			default:
				planAutoStop(&cp, "verdict_unclear")
			}
			m.PlanReviewers[i].Auto = &cp
		}
		return true
	})
}

// planAutoDeps are the loop's side effects, so a test can drive it without tmux.
type planAutoDeps struct {
	// prepare makes the reviewer able to take a message: resumed when stopped, composer up.
	prepare func(reviewer string) error
	// send delivers the follow-up as a `question` peer message.
	send func(planner, reviewer, message string) error
	// reject closes the planner's approval dialog (interrupt) so the reply lands on an idle
	// planner. errNoPlan = the member got there first.
	reject func(planner string) error
	// settle waits until the planner's dialog is really up (best effort).
	settle func(sid string)
	now    func() time.Time
}

var errPlanGone = &peerRejection{Code: "no_plan", Msg: "no pending plan approval"}

var defaultPlanAutoDeps = planAutoDeps{
	prepare: prepareReviewerSession,
	send:    sendPlanFollowUp,
	reject:  rejectPlannerPlan,
	settle:  settlePlanDialog,
	now:     time.Now,
}

// planAutoOnPlan runs one step of the loop for the planner `name`, which has just raised a
// plan approval. Any state that does not call for a round returns silently.
func planAutoOnPlan(d planAutoDeps, name string) {
	meta, ok := session.ReadMeta(name)
	if !ok || !planRespondSupported(meta.Kind) || len(meta.PlanReviewers) == 0 {
		return
	}
	sid := session.UUID(meta.Dir, meta.Name)
	path, _ := status.ReadPlanFile(sid)
	plan, _ := status.ReadPendingPlan(sid)
	if path == "" || strings.TrimSpace(plan) == "" {
		return
	}
	st, _ := status.Read(sid)
	inPlan := effectiveModal(sid, st.State) == "plan"
	fp := planFingerprint(plan)

	var claimed session.PlanReviewer
	var have bool
	UpdateSessionMeta(name, func(m *session.Meta) bool {
		for i := range m.PlanReviewers {
			e := m.PlanReviewers[i]
			a := e.Auto
			if e.Path != path || a == nil || !a.Enabled || a.Verdict != verdictChanges {
				continue
			}
			cp := *a
			at, err := time.Parse(time.RFC3339, a.VerdictAt)
			switch {
			case err != nil || d.now().Sub(at) > planAutoVerdictTTL:
				planAutoStop(&cp, "stale")
			case !inPlan:
				// A planner brought back after a stop is not in plan mode; its "plan" is not one
				// a reviewer's findings can be fed back into.
				planAutoStop(&cp, "not_plan_mode")
			case a.Plan == fp:
				return false // the plan a round was already sent for: a duplicated event
			case a.Round >= a.Max:
				planAutoStop(&cp, "max_rounds")
			default:
				cp.Round++ // persisted before the send: a crash costs a round, never adds one
				cp.Verdict, cp.VerdictAt, cp.Plan = "", "", fp
				m.PlanReviewers[i].Auto = &cp
				claimed, have = m.PlanReviewers[i], true
				return true
			}
			m.PlanReviewers[i].Auto = &cp
			return true
		}
		return false
	})
	if !have {
		return
	}
	fail := func(reason string, err error) {
		log.Printf("plan auto-review: %s %s: %v", name, path, err)
		stopPlanAuto(name, path, reason)
	}
	// Order: reviewer ready, send, THEN reject. A reviewer that will not come up or a refused
	// send leaves the plan on its card untouched; the reply can only arrive after the reject
	// because a reviewer takes far longer than the next line, and a reply that beat it would be
	// queued behind the dialog (peer_pending.go), not lost.
	if err := d.prepare(claimed.Reviewer); err != nil {
		fail("send_failed", err)
		return
	}
	msg := renderPlanAutoPrompt(claimed.Auto.Prompt, name, path, fp)
	if err := d.send(name, claimed.Reviewer, msg); err != nil {
		fail("send_failed", err)
		return
	}
	d.settle(sid)
	if cur, ok := status.ReadPendingPlan(sid); !ok || planFingerprint(cur) != fp {
		stopPlanAuto(name, path, "plan_changed") // the member answered the card meanwhile
		return
	}
	if err := d.reject(name); err != nil {
		if err == errPlanGone {
			stopPlanAuto(name, path, "plan_changed")
			return
		}
		fail("reject_failed", err)
	}
}

// prepareReviewerSession mirrors the Console's prepareReviewer: resume a stopped reviewer and
// wait for its composer.
func prepareReviewerSession(reviewer string) error {
	meta, ok := session.ReadMeta(reviewer)
	if !ok {
		return peerReject("peer_target_unknown", "reviewer %s not found", reviewer)
	}
	if SessionAlive(meta) {
		return nil
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/sessions/"+reviewer+"/start", nil)
	req.SetPathValue("name", reviewer)
	rec := &captureWriter{header: http.Header{}}
	HandleStartSession(rec, req)
	if rec.status < 200 || rec.status >= 300 {
		return peerReject("start_failed", "reviewer %s did not start (%d)", reviewer, rec.status)
	}
	for waited := time.Duration(0); waited < 30*time.Second; waited += 500 * time.Millisecond {
		if m, ok := session.ReadMeta(reviewer); ok && sessionInputReady(m, SessionAlive(m)) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return peerReject("not_ready", "reviewer %s did not become ready", reviewer)
}

func sendPlanFollowUp(planner, reviewer, message string) error {
	body, _ := json.Marshal(map[string]string{"prompt": message, "peer_from": planner, "peer_intent": "question"})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/sessions/"+reviewer+"/input", bytes.NewReader(body))
	req.SetPathValue("name", reviewer)
	rec := &captureWriter{header: http.Header{}}
	HandleSessionInput(rec, req)
	if rec.status >= 200 && rec.status < 300 {
		return nil
	}
	return peerReject("send_refused", "send to %s answered %d %s", reviewer, rec.status, strings.TrimSpace(rec.body.String()))
}

func rejectPlannerPlan(planner string) error {
	body, _ := json.Marshal(map[string]string{"decision": "reject"})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/sessions/"+planner+"/plan-respond", bytes.NewReader(body))
	req.SetPathValue("name", planner)
	rec := &captureWriter{header: http.Header{}}
	HandleSessionPlanRespond(rec, req)
	switch {
	case rec.status >= 200 && rec.status < 300:
		return nil
	case rec.status == http.StatusConflict && strings.Contains(rec.body.String(), "no_plan"):
		return errPlanGone
	}
	return peerReject("reject_refused", "plan-respond answered %d %s", rec.status, strings.TrimSpace(rec.body.String()))
}

// settlePlanDialog waits (up to 8 s) for claude's own permission prompt, which fires when the
// approval dialog is on screen: an Escape sent before it is typed into nothing.
func settlePlanDialog(sid string) {
	for i := 0; i < 32; i++ {
		if st, _ := status.Read(sid); st.State != "plan" {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// HandlePlanPresented (POST /sessions/{name}/plan-presented) is the status hook's kick: the
// planner raised a plan approval. Daemon-only (not in the Control Plane's proxy allowlist);
// it carries no data, because planAutoOnPlan reads everything it acts on from disk, so a kick
// from anywhere can at most ask for a step the state already allows.
func HandlePlanPresented(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	go planAutoOnPlan(defaultPlanAutoDeps, name)
	httpx.WriteJSON(w, http.StatusAccepted, struct {
		Queued string `json:"queued"`
	}{name})
}

// kickPlanAuto is called from the status hook process.
func kickPlanAuto(name string) {
	_, _ = mcpx.AgentPOST("/sessions/"+name+"/plan-presented", []byte("{}"))
}
