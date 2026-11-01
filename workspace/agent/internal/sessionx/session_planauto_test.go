package sessionx

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func TestParsePlanVerdict(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"approve":               {"## Verdict\napprove\n\n## Findings\n- none", verdictApprove},
		"changes":               {"## Verdict\nchanges-requested\n\n## Findings\n- [high] x", verdictChanges},
		"japanese heading":      {"## 判定\nchanges-requested\n\n## 指摘\n- x", verdictChanges},
		"bold and period":       {"## Verdict\n**Approve.**\n", verdictApprove},
		"crlf":                  {"## Verdict\r\nchanges-requested\r\n", verdictChanges},
		"colon heading":         {"### Verdict:\napprove", verdictApprove},
		"blank lines first":     {"## Verdict\n\n\napprove\n", verdictApprove},
		"prose before heading":  {"I do not approve of the old shape.\n\n## Verdict\nchanges-requested\n", verdictChanges},
		"approve only in prose": {"## Verdict\nchanges-requested\n\n## Findings\n- you could approve once X is fixed", verdictChanges},
		// Everything below must come back "" (the loop stops): never approve by accident.
		"template echoed":          {"## Verdict\napprove | changes-requested\n", ""},
		"no verdict section":       {"Looks good, approve.", ""},
		"approve only in findings": {"## Findings\n- approve\n- fine", ""},
		"two sections":             {"## Verdict\napprove\n\n## Verdict\nchanges-requested\n", ""},
		"other verdict named":      {"## Verdict\napprove\nor maybe changes-requested\n", ""},
		"hedged":                   {"## Verdict\nI would approve this\n", ""},
		"empty section":            {"## Verdict\n\n## Findings\napprove\n", ""},
		"negated":                  {"## Verdict\nnot approve\n", ""},
		"substring token":          {"## Verdict\napproved-with-notes\n", ""},
		"fence in section":         {"## Verdict\n```\napprove\n```\n", ""},
		"quoted heading":           {"> ## Verdict\n> approve\n", ""},
		"empty":                    {"", ""},
	} {
		if got := parsePlanVerdict(c.body); got != c.want {
			t.Errorf("%s: parsePlanVerdict = %q, want %q", name, got, c.want)
		}
	}
	// A heading inside a fenced block is not a second heading, so the real one still counts.
	if got := parsePlanVerdict("```\n## Verdict\napprove | changes-requested\n```\n\n## Verdict\nchanges-requested\n"); got != verdictChanges {
		t.Errorf("fenced template: got %q, want changes-requested", got)
	}
}

func TestPlanFingerprintFollowsTheTextNotTheWhitespace(t *testing.T) {
	if planFingerprint("# A\nx  \n") != planFingerprint("# A\r\nx\n\n") {
		t.Error("trailing whitespace changed the fingerprint")
	}
	if planFingerprint("# A\nx") == planFingerprint("# A\ny") {
		t.Error("a revision kept the fingerprint")
	}
}

func TestRenderPlanAutoPromptFallsBackAndFills(t *testing.T) {
	got := renderPlanAutoPrompt("R{rev} {path} -> {session}", "pl", "/p/a.md", "abc")
	if got != "Rabc /p/a.md -> pl" {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []string{"", "no path here", strings.Repeat("x{path}", 1000), "bad\xff{path}"} {
		if g := renderPlanAutoPrompt(bad, "pl", "/p/a.md", "abc"); !strings.Contains(g, "revision abc") || !strings.Contains(g, "/p/a.md") {
			t.Errorf("template %.20q did not fall back to the default: %q", bad, g)
		}
	}
}

func autoLink(path, reviewer string, a session.PlanAuto) session.PlanReviewer {
	return session.PlanReviewer{Path: path, Reviewer: reviewer, Auto: &a}
}

func readAuto(t *testing.T, planner, path string) session.PlanAuto {
	t.Helper()
	m, _ := session.ReadMeta(planner)
	for _, e := range m.PlanReviewers {
		if e.Path == path && e.Auto != nil {
			return *e.Auto
		}
	}
	t.Fatalf("no auto state for %s", path)
	return session.PlanAuto{}
}

func TestNotePlanReviewerReplyStatesTheLoop(t *testing.T) {
	const path = "/p/a.md"
	reply := func(body string, links ...session.PlanReviewer) session.PlanAuto {
		t.Setenv("HOME", t.TempDir())
		m := session.Meta{Name: "planner", Dir: t.TempDir(), Kind: session.KindClaude, PlanReviewers: links}
		session.WriteMeta(m)
		notePlanReviewerReply(m, "srev01", body)
		return readAuto(t, "planner", path)
	}
	on := session.PlanAuto{Enabled: true, Max: 3}

	if a := reply("## Verdict\nchanges-requested\n", autoLink(path, "srev01", on)); !a.Enabled || a.Verdict != verdictChanges || a.VerdictAt == "" {
		t.Errorf("changes-requested: %+v", a)
	}
	if a := reply("## Verdict\napprove\n", autoLink(path, "srev01", on)); a.Enabled || a.Stop != "approved" || a.Verdict != "" {
		t.Errorf("approve must end the loop without arming it: %+v", a)
	}
	for _, body := range []string{"thanks, will look", "## Verdict\napprove | changes-requested\n", "## Findings\napprove"} {
		if a := reply(body, autoLink(path, "srev01", on)); a.Enabled || a.Stop != "verdict_unclear" || a.Verdict != "" {
			t.Errorf("%q: an unreadable reply must stop the loop, never approve or arm it: %+v", body, a)
		}
	}
	// A loop that is off ignores replies entirely.
	if a := reply("## Verdict\nchanges-requested\n", autoLink(path, "srev01", session.PlanAuto{Max: 3})); a.Verdict != "" || a.Enabled {
		t.Errorf("a switched-off loop recorded a verdict: %+v", a)
	}
	// One reviewer linked to two plans of this planner: the reply cannot be matched.
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "planner", Dir: t.TempDir(), Kind: session.KindClaude,
		PlanReviewers: []session.PlanReviewer{autoLink(path, "srev01", on), autoLink("/p/b.md", "srev01", on)}}
	session.WriteMeta(m)
	notePlanReviewerReply(m, "srev01", "## Verdict\nchanges-requested\n")
	for _, p := range []string{path, "/p/b.md"} {
		if a := readAuto(t, "planner", p); a.Enabled || a.Stop != "ambiguous_reviewer" {
			t.Errorf("%s: %+v", p, a)
		}
	}
	// Another session's message changes nothing.
	m, _ = session.ReadMeta("planner")
	notePlanReviewerReply(m, "someone-else", "## Verdict\napprove\n")
}

type planAutoFake struct {
	prepared, sent, rejected []string
	prepareErr, sendErr      error
	rejectErr                error
	now                      time.Time
}

func (f *planAutoFake) deps() planAutoDeps {
	return planAutoDeps{
		prepare: func(r string) error { f.prepared = append(f.prepared, r); return f.prepareErr },
		send: func(p, r, msg string) error {
			if f.sendErr == nil {
				f.sent = append(f.sent, msg)
			}
			return f.sendErr
		},
		reject: func(p, expect string) error { f.rejected = append(f.rejected, p); return f.rejectErr },
		settle: func(string) bool { return true },
		now:    func() time.Time { return f.now },
	}
}

// planAutoEnv writes a claude planner that has a plan on its approval card, linked to srev01.
func planAutoEnv(t *testing.T, plan string, a session.PlanAuto) (planner string, sid, path string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	path = filepath.Join(t.TempDir(), "plan.md")
	m := session.Meta{Name: "planner", Dir: t.TempDir(), Kind: session.KindClaude,
		PlanReviewers: []session.PlanReviewer{autoLink(path, "srev01", a)}}
	session.WriteMeta(m)
	sid = session.UUID(m.Dir, m.Name)
	status.WritePendingPlan(sid, plan)
	status.WritePlanFile(sid, path)
	status.Persist(sid, "plan")
	return "planner", sid, path
}

func armed(round, max int, at time.Time) session.PlanAuto {
	return session.PlanAuto{Enabled: true, Max: max, Round: round, Verdict: verdictChanges, VerdictAt: at.Format(time.RFC3339)}
}

func TestPlanAutoSendsOneRoundAndRejectsAfterTheSend(t *testing.T) {
	now := time.Now()
	f := &planAutoFake{now: now}
	planner, sid, path := planAutoEnv(t, "# Plan v2\nbody", armed(0, 3, now))
	var order []string
	d := f.deps()
	d.send = func(p, r, msg string) error { order = append(order, "send"); f.sent = append(f.sent, msg); return nil }
	d.reject = func(p, expect string) error { order = append(order, "reject"); return nil }

	planAutoOnPlan(d, planner)
	if len(f.sent) != 1 || strings.Join(order, ",") != "send,reject" {
		t.Fatalf("sent=%d order=%v, want one send then the reject", len(f.sent), order)
	}
	if !strings.Contains(f.sent[0], path) || !strings.Contains(f.sent[0], planFingerprint("# Plan v2\nbody")) {
		t.Errorf("follow-up lacks the path or the plan fingerprint: %q", f.sent[0])
	}
	a := readAuto(t, planner, path)
	if !a.Enabled || a.Round != 1 || a.Verdict != "" || a.Plan != planFingerprint("# Plan v2\nbody") {
		t.Errorf("state after round 1 = %+v", a)
	}
	// The same kick again (a duplicated hook) is a no-op: the verdict was consumed.
	planAutoOnPlan(d, planner)
	if len(f.sent) != 1 {
		t.Fatalf("a duplicated event sent %d messages", len(f.sent))
	}
	// Even with the verdict armed again, the plan a round was sent for is not sent twice.
	UpdateSessionMeta(planner, func(m *session.Meta) bool {
		m.PlanReviewers[0].Auto.Verdict, m.PlanReviewers[0].Auto.VerdictAt = verdictChanges, now.Format(time.RFC3339)
		return true
	})
	planAutoOnPlan(d, planner)
	if len(f.sent) != 1 {
		t.Fatalf("the same plan went out twice: %d", len(f.sent))
	}
	_ = sid
}

func TestPlanAutoStopsAtTheRoundLimitAndLeavesTheCard(t *testing.T) {
	now := time.Now()
	f := &planAutoFake{now: now}
	planner, _, path := planAutoEnv(t, "# Plan v4", armed(3, 3, now))
	planAutoOnPlan(f.deps(), planner)
	if len(f.sent) != 0 || len(f.rejected) != 0 {
		t.Fatalf("round 4 went out: sent=%d rejected=%d", len(f.sent), len(f.rejected))
	}
	if a := readAuto(t, planner, path); a.Enabled || a.Stop != "max_rounds" || a.Round != 3 {
		t.Errorf("state = %+v", a)
	}
}

func TestPlanAutoWaitsWithoutAVerdict(t *testing.T) {
	now := time.Now()
	f := &planAutoFake{now: now}
	a := armed(0, 3, now)
	a.Verdict, a.VerdictAt = "", ""
	planner, _, path := planAutoEnv(t, "# Plan", a)
	planAutoOnPlan(f.deps(), planner)
	if len(f.sent) != 0 {
		t.Fatal("sent a round with no reviewer reply")
	}
	if got := readAuto(t, planner, path); !got.Enabled || got.Stop != "" {
		t.Errorf("waiting is not a stop: %+v", got)
	}
}

func TestPlanAutoStopsWhenThePlannerIsNotInPlanMode(t *testing.T) {
	now := time.Now()
	f := &planAutoFake{now: now}
	planner, sid, path := planAutoEnv(t, "# Plan", armed(0, 3, now))
	status.Persist(sid, "idle") // resumed after a stop: no approval dialog behind this plan
	status.RemovePendingPlan(sid)
	status.WritePendingPlan(sid, "# Plan") // a payload left over, the state says otherwise
	planAutoOnPlan(f.deps(), planner)
	if len(f.sent) != 0 {
		t.Fatal("sent a round to a planner that is not in plan mode")
	}
	if a := readAuto(t, planner, path); a.Enabled || a.Stop != "not_plan_mode" {
		t.Errorf("state = %+v", a)
	}
}

func TestPlanAutoIgnoresAStaleVerdict(t *testing.T) {
	now := time.Now()
	f := &planAutoFake{now: now}
	planner, _, path := planAutoEnv(t, "# Plan", armed(0, 3, now.Add(-planAutoVerdictTTL-time.Minute)))
	planAutoOnPlan(f.deps(), planner)
	if len(f.sent) != 0 {
		t.Fatal("acted on a verdict older than the TTL")
	}
	if a := readAuto(t, planner, path); a.Enabled || a.Stop != "stale" {
		t.Errorf("state = %+v", a)
	}
}

func TestPlanAutoFailuresStopTheLoopAndNeverApprove(t *testing.T) {
	now := time.Now()
	for name, c := range map[string]struct {
		prep, send, rej  error
		wantStop         string
		wantSent, wantRj int
	}{
		"reviewer will not come up": {errors.New("boom"), nil, nil, "send_failed", 0, 0},
		"send refused":              {nil, errors.New("429"), nil, "send_failed", 0, 0},
		"reject failed":             {nil, nil, errors.New("tmux"), "reject_failed", 1, 1},
		"member got there first":    {nil, nil, errPlanGone, "plan_changed", 1, 1},
	} {
		f := &planAutoFake{now: now, prepareErr: c.prep, sendErr: c.send, rejectErr: c.rej}
		planner, _, path := planAutoEnv(t, "# Plan", armed(0, 3, now))
		planAutoOnPlan(f.deps(), planner)
		if a := readAuto(t, planner, path); a.Enabled || a.Stop != c.wantStop {
			t.Errorf("%s: state = %+v, want stop %s", name, a, c.wantStop)
		}
		if len(f.sent) != c.wantSent || len(f.rejected) != c.wantRj {
			t.Errorf("%s: sent=%d rejected=%d", name, len(f.sent), len(f.rejected))
		}
	}
}

func TestPlanAutoLeavesAPlanTheMemberAnsweredMeanwhile(t *testing.T) {
	now := time.Now()
	f := &planAutoFake{now: now}
	planner, sid, path := planAutoEnv(t, "# Plan", armed(0, 3, now))
	d := f.deps()
	d.settle = func(string) bool { status.RemovePendingPlan(sid); return true } // approved while the reviewer woke up
	// The real Agent refuses a decision whose presentation is no longer on the card.
	d.reject = func(p, expect string) error {
		if planPresentation(sid) != expect {
			return errPlanGone
		}
		f.rejected = append(f.rejected, p)
		return nil
	}
	planAutoOnPlan(d, planner)
	if len(f.rejected) != 0 {
		t.Fatal("rejected a plan that was no longer on the card")
	}
	if a := readAuto(t, planner, path); a.Enabled || a.Stop != "plan_changed" {
		t.Errorf("state = %+v", a)
	}
}

// The count is what bounds the loop across a restart: it is on the meta, so a fresh process
// reading it continues from it.
func TestPlanAutoCountSurvivesARestart(t *testing.T) {
	now := time.Now()
	f := &planAutoFake{now: now}
	planner, _, path := planAutoEnv(t, "# Plan v1", armed(0, 2, now))
	planAutoOnPlan(f.deps(), planner)
	arm := func(plan string) {
		_, sid := func() (string, string) { m, _ := session.ReadMeta(planner); return "", session.UUID(m.Dir, m.Name) }()
		status.WritePendingPlan(sid, plan)
		UpdateSessionMeta(planner, func(m *session.Meta) bool {
			m.PlanReviewers[0].Auto.Verdict, m.PlanReviewers[0].Auto.VerdictAt = verdictChanges, now.Format(time.RFC3339)
			return true
		})
	}
	arm("# Plan v2")
	planAutoOnPlan(f.deps(), planner)
	arm("# Plan v3")
	planAutoOnPlan(f.deps(), planner)
	if len(f.sent) != 2 {
		t.Fatalf("sent %d rounds with max 2", len(f.sent))
	}
	if a := readAuto(t, planner, path); a.Enabled || a.Stop != "max_rounds" || a.Round != 2 {
		t.Errorf("state = %+v", a)
	}
}

func TestPlanReviewerAutoSettingRoute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.WriteMeta(session.Meta{Name: "planner", Dir: t.TempDir(), Kind: session.KindClaude})
	srv := planReviewerServer(t)
	postPlanReviewer(t, srv, "planner", `{"path":"/p/a.md","reviewer":"srev01"}`)

	if c := postPlanReviewer(t, srv, "planner", `{"path":"/p/a.md","auto":{"enabled":true,"max":4,"prompt":"R {path}"}}`); c != 200 {
		t.Fatalf("enable: %d", c)
	}
	a := readAuto(t, "planner", "/p/a.md")
	if !a.Enabled || a.Max != 4 || a.Round != 0 || a.Prompt != "R {path}" {
		t.Fatalf("after enable: %+v", a)
	}
	// Default N, and the bounds.
	postPlanReviewer(t, srv, "planner", `{"path":"/p/a.md","auto":{"enabled":true}}`)
	if a := readAuto(t, "planner", "/p/a.md"); a.Max != planAutoDefaultMax {
		t.Errorf("default max = %d", a.Max)
	}
	for body, want := range map[string]int{
		`{"path":"/p/a.md","auto":{"enabled":true,"max":11}}`:             400,
		`{"path":"/p/a.md","auto":{"enabled":true,"max":-1}}`:             400,
		`{"path":"/p/zz.md","auto":{"enabled":true}}`:                     409, // no link
		`{"path":"/p/a.md","reviewer":"other","auto":{"enabled":true}}`:   409, // replaced reviewer
		`{"path":"/p/a.md","reviewer":"srev01","auto":{"enabled":false}}`: 200,
	} {
		if got := postPlanReviewer(t, srv, "planner", body); got != want {
			t.Errorf("%s: %d, want %d", body, got, want)
		}
	}
	if a := readAuto(t, "planner", "/p/a.md"); a.Enabled || a.Stop != "" {
		t.Errorf("after the member switched it off: %+v", a)
	}
	// A new reviewer for the plan keeps the member's choice but not the old loop's progress.
	postPlanReviewer(t, srv, "planner", `{"path":"/p/a.md","auto":{"enabled":true,"max":5}}`)
	UpdateSessionMeta("planner", func(m *session.Meta) bool {
		m.PlanReviewers[0].Auto.Round, m.PlanReviewers[0].Auto.Verdict = 2, verdictChanges
		return true
	})
	postPlanReviewer(t, srv, "planner", `{"path":"/p/a.md","reviewer":"srev02"}`)
	a = readAuto(t, "planner", "/p/a.md")
	if !a.Enabled || a.Max != 5 || a.Round != 0 || a.Verdict != "" {
		t.Errorf("after a new reviewer: %+v", a)
	}
}

// The reply path through the real /input: a peer message from the linked reviewer records the
// verdict, a queued one included, and a message from anyone else does not.
func TestPeerReplyThroughInputArmsTheLoop(t *testing.T) {
	freshPeerRate(t)
	withTempHome(t)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	h := &queueingFakeHandle{queued: false, got: make(chan agents.TurnInput, 4)}
	prev, had := managedDrivers[session.KindCodex]
	managedDrivers[session.KindCodex] = &queueingFakeDriver{h: h}
	t.Cleanup(func() {
		if had {
			managedDrivers[session.KindCodex] = prev
			return
		}
		delete(managedDrivers, session.KindCodex)
	})
	const planner, reviewer, path = "pl_planner", "pl_reviewer", "/p/a.md"
	session.WriteMeta(session.Meta{Name: planner, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged,
		PlanReviewers: []session.PlanReviewer{autoLink(path, reviewer, session.PlanAuto{Enabled: true, Max: 3})}})
	session.WriteMeta(session.Meta{Name: reviewer, Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "pl_other", Dir: t.TempDir(), Kind: session.KindClaude})

	rec := postInput(t, planner, `{"prompt":"## Verdict\napprove\n","peer_from":"pl_other","peer_intent":"answer"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if a := readAuto(t, planner, path); !a.Enabled || a.Stop != "" {
		t.Fatalf("a stranger's message moved the loop: %+v", a)
	}
	rec = postInput(t, planner, `{"prompt":"## Verdict\nchanges-requested\n\n## Findings\n- x","peer_from":"`+reviewer+`","peer_intent":"answer"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if a := readAuto(t, planner, path); a.Verdict != verdictChanges {
		t.Fatalf("the reviewer's reply was not recorded: %+v", a)
	}
}

func TestPlanAutoWithoutADialogLeavesTheCard(t *testing.T) {
	now := time.Now()
	f := &planAutoFake{now: now}
	planner, _, path := planAutoEnv(t, "# Plan", armed(0, 3, now))
	d := f.deps()
	d.settle = func(string) bool { return false }
	planAutoOnPlan(d, planner)
	if len(f.rejected) != 0 {
		t.Fatal("sent Escape with no sign of the dialog")
	}
	if a := readAuto(t, planner, path); a.Enabled || a.Stop != "dialog_not_shown" || a.Inflight {
		t.Errorf("state = %+v", a)
	}
}

// The member switches the loop off (or changes the reviewer) while the reviewer wakes up or the
// dialog settles: the claimed round must not send or reject afterwards.
func TestPlanAutoClaimIsRecheckedBeforeEachSideEffect(t *testing.T) {
	now := time.Now()
	change := map[string]func(planner, path string){
		"switched off": func(planner, path string) {
			UpdateSessionMeta(planner, func(m *session.Meta) bool {
				a := *m.PlanReviewers[0].Auto
				planAutoStop(&a, "")
				a.Gen++
				m.PlanReviewers[0].Auto = &a
				return true
			})
		},
		"off and on again": func(planner, path string) {
			UpdateSessionMeta(planner, func(m *session.Meta) bool {
				a := *m.PlanReviewers[0].Auto
				a.Gen += 2 // two settings writes
				m.PlanReviewers[0].Auto = &a
				return true
			})
		},
		"reviewer changed": func(planner, path string) {
			UpdateSessionMeta(planner, func(m *session.Meta) bool {
				m.PlanReviewers = withPlanReviewer(m.PlanReviewers, path, "srev02")
				return true
			})
		},
		"approved verdict stopped it": func(planner, path string) { stopPlanAuto(planner, path, "approved") },
	}
	for name, mutate := range change {
		for phase := 0; phase < 2; phase++ { // 0: during prepare, 1: during settle
			f := &planAutoFake{now: now}
			planner, _, path := planAutoEnv(t, "# Plan", armed(0, 3, now))
			d := f.deps()
			if phase == 0 {
				d.prepare = func(string) error { mutate(planner, path); return nil }
			} else {
				d.settle = func(string) bool { mutate(planner, path); return true }
			}
			planAutoOnPlan(d, planner)
			if len(f.rejected) != 0 || (phase == 0 && len(f.sent) != 0) {
				t.Errorf("%s (phase %d): sent=%d rejected=%d after the setting changed", name, phase, len(f.sent), len(f.rejected))
			}
		}
	}
}

// A claim that could not be saved leaves the old verdict and round on disk; nothing may go out,
// or the next event claims the same round again.
func TestPlanAutoSendsNothingWhenTheClaimCannotBeSaved(t *testing.T) {
	now := time.Now()
	f := &planAutoFake{now: now}
	planner, _, path := planAutoEnv(t, "# Plan", armed(0, 3, now))
	dir := session.MetaDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip(err)
	}
	defer os.Chmod(dir, 0o700)
	if f2, err := os.CreateTemp(dir, "probe"); err == nil { // running as root: the injection cannot work
		f2.Close()
		os.Remove(f2.Name())
		t.Skip("directory permissions are not enforced here")
	}
	planAutoOnPlan(f.deps(), planner)
	if len(f.prepared)+len(f.sent)+len(f.rejected) != 0 {
		t.Fatalf("side effects after a failed claim write: %v %v %v", f.prepared, f.sent, f.rejected)
	}
	os.Chmod(dir, 0o700)
	if a := readAuto(t, planner, path); a.Round != 0 || a.Verdict != verdictChanges || a.Inflight {
		t.Errorf("state = %+v, want the unclaimed original", a)
	}
}

func TestPlanAutoRoundDiedInFlightIsEndedAtBoot(t *testing.T) {
	now := time.Now()
	a := armed(1, 3, now)
	a.Inflight, a.Verdict, a.VerdictAt = true, "", ""
	_, _, path := planAutoEnv(t, "# Plan", a)
	RecoverPlanAutoInflight()
	if got := readAuto(t, "planner", path); got.Enabled || got.Stop != "interrupted" || got.Inflight || got.Round != 1 {
		t.Errorf("after recovery: %+v", got)
	}
	// A healthy loop (no round in flight) is left alone.
	b := armed(1, 3, now)
	_, _, path = planAutoEnv(t, "# Plan", b)
	RecoverPlanAutoInflight()
	if got := readAuto(t, "planner", path); !got.Enabled || got.Stop != "" {
		t.Errorf("a healthy loop was touched: %+v", got)
	}
}

func TestPlanAutoClaimMarksTheRoundInFlightUntilTheRejectWent(t *testing.T) {
	now := time.Now()
	f := &planAutoFake{now: now}
	planner, _, path := planAutoEnv(t, "# Plan", armed(0, 3, now))
	d := f.deps()
	d.reject = func(p, expect string) error {
		if a := readAuto(t, planner, path); !a.Inflight {
			t.Error("round not marked in flight before the reject")
		}
		return nil
	}
	planAutoOnPlan(d, planner)
	if a := readAuto(t, planner, path); a.Inflight || !a.Enabled || a.Round != 1 {
		t.Errorf("after the round: %+v", a)
	}
}

// The decision endpoint itself refuses a presentation that is not the one claimed, and one whose
// dialog is not on screen, before any key is sent.
func TestPlanRespondWithExpectPlanChecksPresentationAndDialog(t *testing.T) {
	m, sid := planFileEnv(t, "# Plan\nbody")
	status.WritePlanFile(sid, "/p/plan.md")
	srv := planFileServer(t)
	mux := srv.Config.Handler.(*http.ServeMux)
	mux.HandleFunc("POST /sessions/{name}/plan-respond", HandleSessionPlanRespond)
	post := func(expect string) (int, string) {
		resp, err := http.Post(srv.URL+"/sessions/"+m.Name+"/plan-respond", "application/json",
			strings.NewReader(`{"decision":"reject","expect_plan":"`+expect+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	status.Persist(sid, "plan") // the hook has fired, the dialog's own prompt has not
	token := planPresentation(sid)
	if code, body := post(token); code != 409 || !strings.Contains(body, "dialog_not_shown") {
		t.Errorf("no dialog yet: %d %s", code, body)
	}
	if code, body := post("not|the|presentation"); code != 409 || !strings.Contains(body, "no_plan") {
		t.Errorf("wrong presentation: %d %s", code, body)
	}
	status.WritePendingPlan(sid, "# Plan\nbody") // the same text presented again is another presentation
	if token2 := planPresentation(sid); token2 == token {
		t.Skip("modtime resolution too coarse to tell two writes apart")
	} else if code, body := post(token); code != 409 || !strings.Contains(body, "no_plan") {
		t.Errorf("a re-presentation of the same text passed the guard: %d %s", code, body)
	}
}
