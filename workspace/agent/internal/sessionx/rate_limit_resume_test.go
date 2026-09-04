package sessionx

// The usage-limit episode state machine (docs/log/47 §4-4). Pane classification is covered by
// internal/tmuxx's golden corpus and the reset instant by internal/agents/claude, so what is
// checked here is the wiring: how many keys are sent, when a resume is booked, what the
// setting governs, and when an episode is retired. There is no tmux and no CP, so the side
// effects are replaced.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// rateLimitFixture isolates the state store (which lives under HOME) and replaces the two
// side effects, returning counters for what the code tried to do.
type rateLimitFixture struct {
	dismissed   int
	scheduled   int
	deleted     []string
	dismissOK   bool
	scheduleAt  time.Time
	scheduleErr error
	resetAt     time.Time
	resetOK     bool
	resetSource string // evidence for the instant (banner / banner+capture / capture)
}

func newRateLimitFixture(t *testing.T) *rateLimitFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	f := &rateLimitFixture{dismissOK: true, resetOK: true, resetSource: "banner"}
	origDismiss, origPut := dismissRateLimitModal, putRateLimitSchedule
	origDrop, origReset := dropRateLimitSchedule, rateLimitResetAt
	dismissRateLimitModal = func(string) bool { f.dismissed++; return f.dismissOK }
	putRateLimitSchedule = func(_ session.Meta, at time.Time) (string, error) {
		f.scheduled++
		f.scheduleAt = at
		if f.scheduleErr != nil {
			return "", f.scheduleErr
		}
		return "sch_test", nil
	}
	dropRateLimitSchedule = func(id string) { f.deleted = append(f.deleted, id) }
	rateLimitResetAt = func(string, time.Time) (time.Time, string, bool) {
		return f.resetAt, f.resetSource, f.resetOK
	}
	t.Cleanup(func() {
		dismissRateLimitModal, putRateLimitSchedule = origDismiss, origPut
		dropRateLimitSchedule, rateLimitResetAt = origDrop, origReset
	})
	return f
}

func setRateLimitPref(t *testing.T, on bool) {
	t.Helper()
	p := uiprefs.Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"rateLimitAutoResume": on})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func rlMeta() session.Meta {
	return session.Meta{Name: "rl1", Dir: "/tmp/rl1", Kind: session.KindClaude}
}

func stateOf(t *testing.T, name string) rateLimitState {
	t.Helper()
	st, _ := RateLimitStates.Read(name)
	return st
}

// TestRateLimitRecoverBooksThenDismisses: one detection gets all the way from booking to
// dismissal, and neither is repeated within the same episode. The point is that the booking
// happens first: once the dismissal clears the menu, this detection path never opens again.
func TestRateLimitRecoverBooksThenDismisses(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(3 * time.Hour)
	m := rlMeta()

	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)
	st := stateOf(t, m.Name)
	if f.scheduled != 1 || st.ScheduleID != "sch_test" {
		t.Fatalf("bookings = %d / id=%q, want 1 / sch_test", f.scheduled, st.ScheduleID)
	}
	if !f.scheduleAt.Equal(f.resetAt) {
		t.Errorf("booked instant = %v, want %v", f.scheduleAt, f.resetAt)
	}
	if f.dismissed != 1 || !st.Dismissed {
		t.Fatalf("dismissals = %d / dismissed=%v, want 1 / true", f.dismissed, st.Dismissed)
	}
	// Another sweep while the menu is still visible (the dismissal has not landed yet) must
	// not fire either action again.
	rateLimitRecover(m, stateOf(t, m.Name), now.Add(rateLimitWatchInterval), true, claude.LimitWindow)
	if f.scheduled != 1 || f.dismissed != 1 {
		t.Errorf("second tick: bookings=%d dismissals=%d - repeating within one episode", f.scheduled, f.dismissed)
	}
}

// TestRateLimitRecoverDoesNotDependOnSessionOrigin: the usage-limit watch does not condition
// on a session's origin or on it being tied to an operator conversation. A standalone session
// launched straight from the Console (origin=user, empty originConv) gets the same booking and
// dismissal as one started by an assistant.
func TestRateLimitRecoverDoesNotDependOnSessionOrigin(t *testing.T) {
	for _, tc := range []struct {
		name       string
		origin     string
		originConv string
	}{
		{"launched straight from the Console", session.OriginUser, ""},
		{"started by an assistant", session.OriginOperator, "a1b2c3d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRateLimitFixture(t)
			now := time.Now()
			f.resetAt = now.Add(time.Hour)
			m := rlMeta()
			m.Name = "rl-" + tc.origin
			m.Origin = tc.origin
			m.OriginConv = tc.originConv

			rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)
			if f.scheduled != 1 || f.dismissed != 1 {
				t.Fatalf("origin=%q originConv=%q: bookings=%d dismissals=%d, want 1 / 1",
					m.Origin, m.OriginConv, f.scheduled, f.dismissed)
			}
		})
	}
}

// TestRateLimitNotificationsAreOnceAndDeliveryBased: "limit reached" is notified only on an
// episode's first detection, and "resumed" only after the internal prompt's delivery is
// confirmed - not at the once-schedule's instant. It also pins that a watcher re-sweep or a CP
// redelivery cannot multiply unread notices.
func TestRateLimitNotificationsAreOnceAndDeliveryBased(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(time.Hour)
	m := rlMeta()
	m.Title = "API 整理"
	session.WriteMeta(m)

	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)
	got := notice.List()
	if len(got) != 1 || got[0].Kind != rateLimitNoticeReached || got[0].DisplayName != m.Title {
		t.Fatalf("first notice = %+v, want 1 reached", got)
	}
	// The watcher seeing the same menu again must not add another reached notice.
	rateLimitRecover(m, stateOf(t, m.Name), now.Add(rateLimitWatchInterval), true, claude.LimitWindow)
	if got = notice.List(); len(got) != 1 {
		t.Fatalf("notices after re-sweep = %+v, want still 1", got)
	}

	// A mere booking, a different prompt, or a manual firing must not claim "resumed".
	notifyRateLimitResumeDelivered(m.Name, rateLimitResumePromptFor("en"), TurnSourceScheduleManual, now)
	notifyRateLimitResumeDelivered(m.Name, "unrelated scheduled prompt", TurnSourceSchedule, now)
	if got = notice.List(); len(got) != 1 {
		t.Fatalf("a resume notice appeared without delivery: %+v", got)
	}

	deliveredAt := f.resetAt.Add(time.Minute)
	notifyRateLimitResumeDelivered(m.Name, rateLimitResumePromptFor("en"), TurnSourceSchedule, deliveredAt)
	notifyRateLimitResumeDelivered(m.Name, rateLimitResumePromptFor("en"), TurnSourceSchedule, deliveredAt.Add(time.Second))
	got = notice.List()
	if len(got) != 2 || got[1].Kind != rateLimitNoticeResumed {
		t.Fatalf("notices after delivery = %+v, want reached + resumed", got)
	}
	if got[1].Payload["resumeAt"] != stateOf(t, m.Name).ResumeAt {
		t.Errorf("resumeAt payload = %v, want %q", got[1].Payload["resumeAt"], stateOf(t, m.Name).ResumeAt)
	}
}

// TestRateLimitRecoverWithoutMenu: a limit without a menu (a per-model limit prints a one-line
// error and returns to the ordinary input box; measured 2026-08-05 s6no6jv) still opens an
// episode and notifies that the limit was reached. It also pins that no key is sent in this
// shape: with no menu up, an Enter press is simply the user's prompt being submitted.
func TestRateLimitRecoverWithoutMenu(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(time.Hour)
	m := rlMeta()
	m.Title = "読者レポート生成"
	session.WriteMeta(m)

	rateLimitRecover(m, stateOf(t, m.Name), now, false, claude.LimitWindow)
	if f.dismissed != 0 {
		t.Errorf("dismissals = %d, want 0 (Enter sent although no menu is up)", f.dismissed)
	}
	got := notice.List()
	if len(got) != 1 || got[0].Kind != rateLimitNoticeReached {
		t.Fatalf("notices = %+v, want 1 reached", got)
	}
	if st := stateOf(t, m.Name); st.At == "" || st.Menu {
		t.Errorf("state = %+v, want At set / Menu=false", st)
	}
	// However often the watcher sees the same limit, neither notices nor keys accumulate.
	rateLimitRecover(m, stateOf(t, m.Name), now.Add(rateLimitWatchInterval), false, claude.LimitWindow)
	if len(notice.List()) != 1 || f.dismissed != 0 {
		t.Errorf("re-sweep: notices=%d dismissals=%d - repeating within one episode", len(notice.List()), f.dismissed)
	}
}

// TestRateLimitWithoutMenuNeedsBannerTime: for a limit without a menu, the reset instant is
// trusted only when it came from the banner. The statusline-capture fallback (source=capture)
// answers for the account's 5-hour/weekly windows, and a per-model limit is a different window
// - resuming there just hits the same limit. The form that comes with a menu (an account
// window) keeps using the capture as before.
func TestRateLimitWithoutMenuNeedsBannerTime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		onMenu  bool
		source  string
		wantSch int
	}{
		{"no menu, capture only", false, "capture", 0},
		{"no menu, banner", false, "banner", 1},
		{"no menu, banner and capture", false, "banner+capture", 1},
		{"menu, capture only", true, "capture", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRateLimitFixture(t)
			now := time.Now()
			f.resetAt, f.resetSource = now.Add(time.Hour), tc.source
			m := rlMeta()

			rateLimitRecover(m, stateOf(t, m.Name), now, tc.onMenu, claude.LimitWindow)
			if f.scheduled != tc.wantSch {
				t.Errorf("bookings = %d, want %d (source=%s onMenu=%v)",
					f.scheduled, tc.wantSch, tc.source, tc.onMenu)
			}
			if st := stateOf(t, m.Name); tc.wantSch == 0 && st.ResumeAt != "" {
				t.Errorf("ResumeAt = %q, want empty (the instant cannot be trusted)", st.ResumeAt)
			}
		})
	}
}

// TestRateLimitDismissRetriesAreBounded: when the dismissal does not take, it is retried only
// a few times with a gap, then given up (neither a moved selection nor a changed TUI shape is
// fixed by hammering the key).
func TestRateLimitDismissRetriesAreBounded(t *testing.T) {
	f := newRateLimitFixture(t)
	f.dismissOK = false
	now := time.Now()
	f.resetAt = now.Add(time.Hour)
	m := rlMeta()

	for i := 0; i < maxRateLimitDismissTries+3; i++ {
		rateLimitRecover(m, stateOf(t, m.Name), now.Add(time.Duration(i)*rateLimitWatchInterval), true, claude.LimitWindow)
	}
	if f.dismissed != maxRateLimitDismissTries {
		t.Fatalf("dismissal attempts = %d, want capped at %d", f.dismissed, maxRateLimitDismissTries)
	}
	if st := stateOf(t, m.Name); st.Dismissed {
		t.Error("dismissed = true although the menu was never dismissed")
	}
	// The booking stays at one: a failed dismissal must not rebook the resume.
	if f.scheduled != 1 {
		t.Errorf("bookings = %d, want 1", f.scheduled)
	}
}

// TestRateLimitDismissRunsWithSettingOff: the setting governs only the resume booking; the
// menu is dismissed even when it is off, because until someone touches it the session accepts
// nothing.
func TestRateLimitDismissRunsWithSettingOff(t *testing.T) {
	f := newRateLimitFixture(t)
	setRateLimitPref(t, false)
	now := time.Now()
	f.resetAt = now.Add(time.Hour)
	m := rlMeta()

	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)
	if f.dismissed != 1 {
		t.Errorf("dismissals = %d, want 1 (dismissal happens even with the setting off)", f.dismissed)
	}
	if f.scheduled != 0 {
		t.Errorf("bookings = %d, want 0 (setting off)", f.scheduled)
	}
	if st := stateOf(t, m.Name); st.ResumeAt != "" {
		t.Errorf("ResumeAt = %q, want empty", st.ResumeAt)
	}
}

// TestRateLimitPastResetResumesSoon: a menu left sitting until the reset instant has already
// passed is booked for the soonest slot (now + lead), not for an instant in the past.
func TestRateLimitPastResetResumesSoon(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(-16 * time.Hour)
	m := rlMeta()

	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)
	if f.scheduled != 1 {
		t.Fatalf("bookings = %d, want 1", f.scheduled)
	}
	if want := now.Add(rateLimitResumeLead); !f.scheduleAt.Equal(want) {
		t.Errorf("booked instant = %v, want %v (a reset already past runs as soon as possible)", f.scheduleAt, want)
	}
}

// TestRateLimitNoResetTimeNoSchedule: with no instant to be had, nothing is booked - waking on
// a guess just hits the limit again and brings the same menu back. The dismissal still runs.
func TestRateLimitNoResetTimeNoSchedule(t *testing.T) {
	f := newRateLimitFixture(t)
	f.resetOK = false
	m := rlMeta()

	rateLimitRecover(m, stateOf(t, m.Name), time.Now(), true, claude.LimitWindow)
	if f.scheduled != 0 {
		t.Errorf("bookings = %d, want 0", f.scheduled)
	}
	if f.dismissed != 1 {
		t.Errorf("dismissals = %d, want 1", f.dismissed)
	}
}

// TestRateLimitFollowUpRetriesRegistration: once the menu is gone the detection path no longer
// opens, so an episode whose registration failed is retried by a later tick from the state
// file - a bounded number of times.
func TestRateLimitFollowUpRetriesRegistration(t *testing.T) {
	f := newRateLimitFixture(t)
	f.scheduleErr = errTest{}
	now := time.Now()
	f.resetAt = now.Add(2 * time.Hour)
	m := rlMeta()

	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)
	if f.scheduled != 1 || stateOf(t, m.Name).ScheduleID != "" {
		t.Fatalf("premise broken: bookings=%d id=%q", f.scheduled, stateOf(t, m.Name).ScheduleID)
	}
	f.scheduleErr = nil
	rateLimitFollowUp(m, stateOf(t, m.Name), now.Add(rateLimitWatchInterval), true)
	if f.scheduled != 2 || stateOf(t, m.Name).ScheduleID != "sch_test" {
		t.Fatalf("not retried: bookings=%d id=%q", f.scheduled, stateOf(t, m.Name).ScheduleID)
	}
	// Repeated failures must not lead to unbounded attempts.
	f.scheduleErr = errTest{}
	RateLimitStates.Write(m.Name, rateLimitState{At: now.Format(time.RFC3339), ScheduleTries: maxRateLimitScheduleTries})
	rateLimitFollowUp(m, stateOf(t, m.Name), now.Add(2*rateLimitWatchInterval), true)
	if f.scheduled != 2 {
		t.Errorf("registration attempted past the try limit (bookings = %d)", f.scheduled)
	}
}

// TestRateLimitEpisodeRetired: an episode past its booked instant is retired and its spent
// once-schedule deleted. Leaving them behind piles dead rows into the Console's list, and the
// state file would suppress detection of the next episode.
func TestRateLimitEpisodeRetired(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(time.Hour)
	m := rlMeta()
	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)

	after := now.Add(time.Hour + rateLimitCleanupGrace + time.Minute)
	rateLimitFollowUp(m, stateOf(t, m.Name), after, true)
	if len(f.deleted) != 1 || f.deleted[0] != "sch_test" {
		t.Errorf("deleted spent schedules = %v, want [sch_test]", f.deleted)
	}
	if _, ok := RateLimitStates.Read(m.Name); ok {
		t.Error("the episode's state file is still there - the next limit would not get booked")
	}
	// The next limit is treated as a new episode.
	f.resetAt = after.Add(2 * time.Hour)
	rateLimitRecover(m, stateOf(t, m.Name), after, true, claude.LimitWindow)
	if f.scheduled != 2 {
		t.Errorf("bookings = %d, want 2 (a new episode)", f.scheduled)
	}
}

// TestRateLimitSpendLimitNeverSchedules: a spend/balance limit (docs/log/47 §4-10) is not put
// through the machinery for limits that waiting clears. Booking would wake the session for a
// reset that never comes, and notifying would make the user wait - both only delay the billing
// move (raise the limit, or add credits) that is the actual fix.
func TestRateLimitSpendLimitNeverSchedules(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(time.Hour) // resolvable, but unused: this is not a question of timing
	m := rlMeta()

	rateLimitRecover(m, stateOf(t, m.Name), now, false, claude.LimitSpend)
	if f.scheduled != 0 {
		t.Errorf("bookings = %d, want 0 (waking the session for a reset that never comes)", f.scheduled)
	}
	if f.dismissed != 0 {
		t.Errorf("dismissals = %d, want 0 (no menu is up)", f.dismissed)
	}
	if got := notice.List(); len(got) != 0 {
		t.Errorf("notices = %+v, want 0 (the \"usage limit reached\" wording assumes waiting clears it)", got)
	}
	st := stateOf(t, m.Name)
	if !st.Spend || st.At == "" || st.ResumeAt != "" {
		t.Errorf("state = %+v, want Spend=true / At set / ResumeAt empty", st)
	}
}

// TestRateLimitSpendEpisodeEndsWithTheTranscript: a spend episode is never retired on time.
// Dropping it at the 12-hour TTL would remove the fact from the screen (the chip falls back to
// "waiting for input") while the limit is still in force until someone raises it. It may be
// retired only once a live session's transcript is no longer at the limit.
func TestRateLimitSpendEpisodeEndsWithTheTranscript(t *testing.T) {
	newRateLimitFixture(t)
	now := time.Now()
	m := rlMeta()
	rateLimitRecover(m, stateOf(t, m.Name), now, false, claude.LimitSpend)

	// Far past the TTL, still not retired.
	late := now.Add(rateLimitEpisodeTTL + 6*time.Hour)
	if episodeStale(stateOf(t, m.Name), late) {
		t.Error("spend episode retired on time - the chip disappears before the limit is raised")
	}
	// A merely stopped session keeps its episode: it may come back still at the same limit.
	rateLimitFollowUp(m, stateOf(t, m.Name), late, false)
	if _, ok := RateLimitStates.Read(m.Name); !ok {
		t.Error("episode deleted for a stopped session")
	}
	// Reaching here on a live session means the transcript tail is no longer at the limit,
	// i.e. the limit was raised.
	rateLimitFollowUp(m, stateOf(t, m.Name), late, true)
	if _, ok := RateLimitStates.Read(m.Name); ok {
		t.Error("episode still present after the limit cleared - it suppresses detection of the next one")
	}
}

// TestRateLimitResumeNoteOnFailedReport: a turn stopped by the limit is reported as turn-failed
// (i.e. resending changes nothing), so without a note the conversation stalls on "let's discuss
// what to do" and the later resume looks to the user like it happened on its own. The report
// body must carry the fact that a resume is already booked.
func TestRateLimitResumeNoteOnFailedReport(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(90 * time.Minute)
	m := rlMeta()
	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)

	body := reportBodyForTest("表示名", m.Name, chatx.ReportKindAnswerReady, chatx.ReportReasonTurnFailed)
	if !strings.Contains(body, "利用上限による停止です") ||
		!strings.Contains(body, f.resetAt.Local().Format("1月2日 15:04")) {
		t.Errorf("the report body does not mention the booked auto-resume:\n%s", body)
	}
	// Not added for a session with no booking (an ordinary failure).
	if other := reportBodyForTest("表示名", "rl-none", chatx.ReportKindAnswerReady, chatx.ReportReasonTurnFailed); strings.Contains(other, "利用上限による停止です") {
		t.Errorf("failure report for a session with no booking carries the limit note:\n%s", other)
	}
	// Not added to a completion report.
	if done := reportBodyForTest("表示名", m.Name, chatx.ReportKindAnswerReady, ""); strings.Contains(done, "利用上限による停止です") {
		t.Errorf("completion report carries the limit note:\n%s", done)
	}
}

// TestDismissRateLimitModalLive drives the real key path against a real tmux pane: a program
// draws the menu frame and waits for one line of input, switching to the waiting frame once
// Enter arrives. Whether the press-then-reread round trip really works can only be checked
// here; the pure classification is covered by the golden corpus.
func TestDismissRateLimitModalLive(t *testing.T) {
	isolateAgentState(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	name := "ratelimit-live"
	tn := session.TmuxName(name)
	// On Enter, clear the screen and swap in the waiting frame - the same "the confirmation
	// footer disappears" change claude makes when it closes the menu.
	script := `cat ../tmuxx/testdata/footers/modal_rate_limit.txt; read x; ` +
		`printf '\033[2J\033[H'; cat ../tmuxx/testdata/footers/idle_bypass_hint.txt; sleep 60`
	if out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "-x", "200", "-y", "50", "sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v\n%s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !tmuxx.AtRateLimitModal(name) {
		time.Sleep(50 * time.Millisecond)
	}
	if !tmuxx.AtRateLimitModal(name) {
		t.Fatal("the menu frame was never drawn (premise broken)")
	}
	if !tmuxx.DismissRateLimitModal(name) {
		t.Fatal("DismissRateLimitModal = false - either Enter never arrived, or the menu could not be confirmed gone")
	}
	if tmuxx.AtRateLimitModal(name) {
		t.Error("the menu is still detected after the dismissal")
	}
}

type errTest struct{}

func (errTest) Error() string { return "CP unreachable (test)" }
