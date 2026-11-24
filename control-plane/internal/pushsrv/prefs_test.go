package pushsrv

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestOffKindIsNotPushedOthersAre(t *testing.T) {
	f := newFixture(t)
	f.subscribe(t, "m1")
	_ = f.st.PutPushOffKinds(context.Background(), "m1", []string{"answer-ready"})
	f.deliver(note("answer-ready"))
	if f.count() != 0 {
		t.Fatal("a kind the member turned off was pushed")
	}
	f.deliver(note("question"))
	if f.count() != 1 {
		t.Fatalf("a kind left on was not pushed: %d", f.count())
	}
}

func TestRemovedMemberGetsNothing(t *testing.T) {
	f := newFixture(t)
	f.subscribe(t, "m1")
	f.st.inactive = map[string]bool{"m1": true}
	f.deliver(note("question"))
	if f.count() != 0 {
		t.Fatal("an inactive member was pushed")
	}
}

func TestOnScreenSessionIsNotPushed(t *testing.T) {
	f := newFixture(t)
	f.subscribe(t, "m1")
	now := time.Now()
	f.svc.view.set("m1", "desk", "s1", now)
	f.deliver(note("question"))
	if f.count() != 0 {
		t.Fatal("pushed for a session on screen")
	}
	other := note("question")
	other.TargetID = "s2"
	f.deliver(other)
	if f.count() != 1 {
		t.Fatal("a different session was suppressed")
	}
	// Another member's report does not suppress this one.
	f.svc.view.set("m2", "desk", "s3", now)
	third := note("question")
	third.TargetID = "s3"
	f.deliver(third)
	if f.count() != 2 {
		t.Fatal("another member's view suppressed the push")
	}
	// Clearing, and expiry, both end the suppression.
	f.svc.view.set("m1", "desk", "", now)
	f.deliver(note("question"))
	if f.count() != 3 {
		t.Fatal("still suppressed after the device cleared")
	}
	f.svc.view.set("m1", "desk", "s1", now.Add(-2*viewTTL))
	f.deliver(note("question"))
	if f.count() != 4 {
		t.Fatal("a stale report still suppressed the push")
	}
}

func TestQuestionTextOnlyWhenTenantAllows(t *testing.T) {
	f := newFixture(t)
	d := f.subscribe(t, "m1")
	n := note("question")
	n.Payload = `{"questions":[{"question":"Deploy to prod?"}]}`
	f.deliver(n)
	if got := d.open(t, f.bodies[0]); strings.Contains(got, "Deploy") {
		t.Fatalf("text sent with the setting off: %s", got)
	}
	f.st.includeTxt = true
	f.deliver(n)
	if got := d.open(t, f.bodies[1]); !strings.Contains(got, `"text":"Deploy to prod?"`) {
		t.Fatalf("text missing with the setting on: %s", got)
	}
	// Only a question carries text; an answer's body never does.
	a := note("answer-ready")
	a.Payload = `{"body":"SECRET","questions":[{"question":"x"}]}`
	f.deliver(a)
	if got := d.open(t, f.bodies[2]); strings.Contains(got, "SECRET") || strings.Contains(got, `"text"`) {
		t.Fatalf("answer-ready carried text: %s", got)
	}
}

func TestQuestionTextIsTruncated(t *testing.T) {
	long := strings.Repeat("あ", 500)
	got := questionText(`{"questions":[{"question":"` + long + `"}]}`)
	if r := []rune(got); len(r) != maxPushText || r[len(r)-1] != '…' {
		t.Fatalf("len %d", len(r))
	}
	if questionText(`not json`) != "" || questionText(`{"questions":[]}`) != "" {
		t.Fatal("garbage payload produced text")
	}
}

func TestPrefsAPI(t *testing.T) {
	f := newFixture(t)
	put := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		f.svc.PutPrefs(w, httptest.NewRequest("PUT", "/", strings.NewReader(body)), "m1")
		return w
	}
	if w := put(`{"off":["answer-ready","answer-ready"]}`); w.Code != 200 || !strings.Contains(w.Body.String(), `["answer-ready"]`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, bad := range []string{`{"off":["nope"]}`, `{}`, `{`} {
		if put(bad).Code != 400 {
			t.Fatalf("accepted %s", bad)
		}
	}
	if off, _ := f.st.GetPushOffKinds(context.Background(), "m1"); len(off) != 1 {
		t.Fatalf("a refused save changed the stored prefs: %v", off)
	}
	w := httptest.NewRecorder()
	f.svc.GetPrefs(w, httptest.NewRequest("GET", "/", nil), "m2")
	if !strings.Contains(w.Body.String(), `"off":[]`) {
		t.Fatalf("default is not an empty list: %s", w.Body)
	}
}

func TestViewingAPI(t *testing.T) {
	f := newFixture(t)
	post := func(body string) int {
		w := httptest.NewRecorder()
		f.svc.Viewing(w, httptest.NewRequest("POST", "/", strings.NewReader(body)), "m1")
		return w.Code
	}
	if post(`{"device":"d1","session":"s1"}`) != http.StatusNoContent || !f.svc.view.on("m1", "s1", time.Now()) {
		t.Fatal("report not recorded")
	}
	if post(`{"session":"s1"}`) != 400 {
		t.Fatal("accepted a report with no device")
	}
}

func TestReleaseNeedsTheSubscriptionsKeys(t *testing.T) {
	f := newFixture(t)
	_, p256, auth := newDevice(t)
	ep := f.srv.URL + "/r"
	_ = f.st.UpsertPushSubscription(context.Background(), store.PushSubscription{MembershipID: "m1", Endpoint: ep, P256dh: p256, Auth: auth})
	rel := func(a string) int {
		w := httptest.NewRecorder()
		f.svc.Release(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"endpoint":"`+ep+`","keys":{"p256dh":"`+p256+`","auth":"`+a+`"}}`)))
		return w.Code
	}
	if rel("wrong") != 204 {
		t.Fatal("a wrong-key release must still answer 204")
	}
	if subs, _ := f.st.ListPushSubscriptions(context.Background(), "m1"); len(subs) != 1 {
		t.Fatal("released with the wrong keys")
	}
	if rel(auth) != 204 {
		t.Fatal("release failed")
	}
	if subs, _ := f.st.ListPushSubscriptions(context.Background(), "m1"); len(subs) != 0 {
		t.Fatal("still subscribed after release")
	}
	f.deliver(note("question"))
	if f.count() != 0 {
		t.Fatal("pushed after release")
	}
}

func TestOneDeviceClearingKeepsTheOthersSuppression(t *testing.T) {
	f := newFixture(t)
	f.subscribe(t, "m1")
	now := time.Now()
	f.svc.view.set("m1", "phone", "s1", now)
	f.svc.view.set("m1", "desk", "s1", now)
	f.svc.view.set("m1", "phone", "", now)
	f.deliver(note("question"))
	if f.count() != 0 {
		t.Fatal("clearing one device lifted the other device's suppression")
	}
}

func TestViewingIsBounded(t *testing.T) {
	var v viewing
	now := time.Now()
	for i := 0; i < 1000; i++ {
		v.set("m1", string(rune('a'+i%26))+string(rune('A'+i/26%26))+string(rune('0'+i/676)), "s", now)
	}
	if n := len(v.byMember["m1"]); n > maxViewDevices {
		t.Fatalf("%d devices on record for one member", n)
	}
	// A member who stopped reporting is reclaimed by someone else's report after the TTL.
	v.set("gone", "d", "s", now)
	v.set("other", "d", "s", now.Add(viewTTL+sweepEvery+time.Second))
	if _, ok := v.byMember["gone"]; ok {
		t.Fatal("an abandoned member's expired entry was never reclaimed")
	}
}

func TestOversizedTextIsDroppedNotTheQuestion(t *testing.T) {
	f := newFixture(t)
	d := f.subscribe(t, "m1")
	f.st.includeTxt = true
	n := note("question")
	n.DisplayName = strings.Repeat("a", 3600)
	n.Payload = `{"questions":[{"question":"` + strings.Repeat("<", 140) + `"}]}`
	f.deliver(n)
	if f.count() != 1 {
		t.Fatal("the push was lost to an over-long text")
	}
	if got := d.open(t, f.bodies[0]); strings.Contains(got, `"text"`) {
		t.Fatal("text kept past the payload limit")
	}
}

// ctxStore makes the named reads honour their context the way a database does: a stalled read
// blocks until its deadline, and every read fails once the context it was given is done.
type ctxStore struct {
	*memStore
	stall string // "context" or "prefs": the read that hangs until its deadline
}

func (c ctxStore) PushDeliveryContext(ctx context.Context, mid string) (store.PushDelivery, error) {
	if c.stall == "context" {
		<-ctx.Done()
		return store.PushDelivery{}, ctx.Err()
	}
	return c.memStore.PushDeliveryContext(ctx, mid)
}
func (c ctxStore) GetPushOffKinds(ctx context.Context, mid string) ([]string, error) {
	if c.stall == "prefs" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.memStore.GetPushOffKinds(ctx, mid)
}
func (c ctxStore) ListPushSubscriptions(ctx context.Context, mid string) ([]store.PushSubscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.memStore.ListPushSubscriptions(ctx, mid)
}

func TestStalledReadStillDeliversTheQuestion(t *testing.T) {
	for _, stall := range []string{"context", "prefs"} {
		t.Run(stall, func(t *testing.T) {
			f := newFixture(t)
			d := f.subscribe(t, "m1")
			f.st.includeTxt = true
			f.svc.store = ctxStore{f.st, stall}
			f.svc.timeout = 300 * time.Millisecond
			n := note("question")
			n.Payload = `{"questions":[{"question":"Deploy to prod?"}]}`
			f.deliver(n)
			if f.count() != 1 {
				t.Fatalf("a stalled %s read cost the member the notification", stall)
			}
			got := d.open(t, f.bodies[0])
			// A settings read that could not be answered must not be read as "text allowed".
			if stall == "context" && strings.Contains(got, `"text"`) {
				t.Fatalf("text sent although the tenant setting could not be read: %s", got)
			}
			if stall == "prefs" && !strings.Contains(got, `"text":"Deploy to prod?"`) {
				t.Fatalf("text lost when only the opt-out read stalled: %s", got)
			}
		})
	}
}

// A send that waited in the queue is checked against the state at the time of the send, not the
// one deliver read: the first device is sent at once and holds the only worker, and the second
// waits while the state changes.
func TestQueuedSendRereadsTheDeliveryContext(t *testing.T) {
	cases := []struct {
		name   string
		change func(*memStore)
		// sent is whether the waiting device gets a push; text, whether it carries the question.
		sent, text bool
	}{
		{"member removed", func(m *memStore) { m.inactive = map[string]bool{"m1": true} }, false, false},
		{"text turned off", func(m *memStore) { m.includeTxt = false }, true, false},
		{"nothing changed", func(*memStore) {}, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixtureN(t, 1, pushQueueLen, nil)
			arrived, release := f.hold()
			defer release()
			f.st.includeTxt = true
			d1 := f.subscribe(t, "m1")
			d2 := f.subscribe(t, "m1")
			n := note("question")
			n.Payload = `{"questions":[{"question":"Deploy to prod?"}]}`
			var wg sync.WaitGroup
			f.svc.deliver(context.Background(), n, &wg)
			waitArrival(t, arrived)
			f.st.mu.Lock()
			c.change(f.st)
			f.st.mu.Unlock()
			release()
			wg.Wait()
			want := 1
			if c.sent {
				want = 2
			}
			if f.count() != want {
				t.Fatalf("%d pushes, want %d", f.count(), want)
			}
			if got := d1.open(t, f.bodies[0]); !strings.Contains(got, `"text":"Deploy to prod?"`) {
				t.Fatalf("the send before the change lost its text: %s", got)
			}
			if !c.sent {
				return
			}
			got := d2.open(t, f.bodies[1])
			if strings.Contains(got, `"text"`) != c.text {
				t.Fatalf("waiting send: %s (text wanted: %v)", got, c.text)
			}
			if !strings.Contains(got, `"kind":"question","session":"s1"`) {
				t.Fatalf("waiting send lost the question: %s", got)
			}
		})
	}
}

// A Service built as a literal, without New, takes a report and a delivery without panicking.
func TestServiceLiteralHasUsableViewing(t *testing.T) {
	p := &Service{store: &memStore{}, timeout: time.Second, jobs: make(chan sendJob, 1)}
	if p.view.on("m1", "s1", time.Now()) {
		t.Fatal("an empty map reports a session on screen")
	}
	w := httptest.NewRecorder()
	p.Viewing(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"device":"d1","session":"s1"}`)), "m1")
	if w.Code != http.StatusNoContent || !p.view.on("m1", "s1", time.Now()) {
		t.Fatalf("report not recorded: %d", w.Code)
	}
	other := note("question")
	other.TargetID = "s2"
	p.deliver(context.Background(), other, nil)
}

// When the send-time joined read fails, the device is still sent to, without the text, after its
// row alone was read; once the read is skipped for joinOffFor, it is tried again.
func TestFailedJoinedReadSendsWithoutText(t *testing.T) {
	f := newFixtureN(t, 1, pushQueueLen, func(f *fixture) { f.svc.joinOffFor = 200 * time.Millisecond })
	f.st.includeTxt = true
	d := f.subscribe(t, "m1")
	n := note("question")
	n.Payload = `{"questions":[{"question":"Deploy to prod?"}]}`
	f.st.joinErr = io.ErrUnexpectedEOF
	f.deliver(n)
	if f.count() != 1 || f.svc.joinFailed.Load() != 1 {
		t.Fatalf("%d sent, %d failed joins; want 1 and 1", f.count(), f.svc.joinFailed.Load())
	}
	if got := d.open(t, f.bodies[0]); strings.Contains(got, `"text"`) || !strings.Contains(got, `"kind":"question"`) {
		t.Fatalf("fallback send: %s", got)
	}
	f.st.mu.Lock()
	f.st.joinErr = nil
	f.st.mu.Unlock()
	time.Sleep(300 * time.Millisecond)
	f.deliver(n)
	if got := d.open(t, f.bodies[1]); !strings.Contains(got, `"text":"Deploy to prod?"`) {
		t.Fatalf("the joined read was not tried again after joinOffFor: %s", got)
	}
}

// stallJoin makes the send-time read with the setting hang until its deadline, as a lock on the
// setting table would, while the read without it still answers at once.
type stallJoin struct{ *memStore }

func (s stallJoin) GetPushSendTarget(ctx context.Context, mid, ep string, withText bool) (store.PushSubscription, store.PushDelivery, bool, error) {
	if !withText {
		return s.memStore.GetPushSendTarget(ctx, mid, ep, false)
	}
	<-ctx.Done()
	return store.PushSubscription{}, store.PushDelivery{}, false, ctx.Err()
}

// A joined read that stalls costs one short wait, not one per queued send: with more sends queued
// than budgets fit in the queue wait, every one is still sent, without the text.
func TestStalledJoinedReadDoesNotStaleTheQueue(t *testing.T) {
	f := newFixtureN(t, 1, pushQueueLen, func(f *fixture) {
		f.svc.slotWait, f.svc.timeout, f.svc.joinBudget = time.Second, 5*time.Second, 100*time.Millisecond
	})
	f.st.includeTxt = true
	var devs []device
	for i := 0; i < 15; i++ {
		devs = append(devs, f.subscribe(t, "m1"))
	}
	f.svc.store = stallJoin{f.st}
	n := note("question")
	n.Payload = `{"questions":[{"question":"Deploy to prod?"}]}`
	f.deliver(n)
	if f.count() != len(devs) || f.svc.droppedStale.Load() != 0 {
		t.Fatalf("%d sent, %d stale; want %d and 0", f.count(), f.svc.droppedStale.Load(), len(devs))
	}
	for i, d := range devs {
		if got := d.open(t, f.bodies[i]); strings.Contains(got, `"text"`) {
			t.Fatalf("send %d kept the text: %s", i, got)
		}
	}
	if got := f.svc.joinFailed.Load(); got != 1 {
		t.Fatalf("joined reads that waited out their budget: %d, want 1", got)
	}
}

// After one failed read with the setting, the reads that leave it out still check the member: a
// member removed while their send waited is not sent to, even inside joinOffFor.
func TestFailedJoinedReadStillChecksTheMember(t *testing.T) {
	f := newFixtureN(t, 1, pushQueueLen, nil)
	arrived, release := f.hold()
	defer release()
	f.subscribe(t, "m1")
	f.subscribe(t, "m2")
	f.st.mu.Lock()
	f.st.joinErr = io.ErrUnexpectedEOF
	f.st.mu.Unlock()
	var wg sync.WaitGroup
	f.svc.deliver(context.Background(), note("question"), &wg) // m1: its read with the setting fails
	waitArrival(t, arrived)
	other := note("question")
	other.MembershipID = "m2"
	f.svc.deliver(context.Background(), other, &wg) // m2: queued while still active
	f.st.mu.Lock()
	f.st.joinErr = nil // the database is back
	f.st.inactive = map[string]bool{"m2": true}
	f.st.mu.Unlock()
	release()
	wg.Wait()
	if f.count() != 1 || f.svc.joinFailed.Load() != 1 {
		t.Fatalf("%d sent, %d failed joins; want 1 (m1 only) and 1", f.count(), f.svc.joinFailed.Load())
	}
}

// stallMember makes every send-time read that joins the status of a member in stalled hang until
// its deadline, as a lock on that member's membership row would; the device row alone still
// answers at once.
type stallMember struct {
	*memStore
	stalled map[string]bool
}

func (s stallMember) GetPushSendTarget(ctx context.Context, mid, ep string, withText bool) (store.PushSubscription, store.PushDelivery, bool, error) {
	if !s.stalled[mid] {
		return s.memStore.GetPushSendTarget(ctx, mid, ep, withText)
	}
	<-ctx.Done()
	return store.PushSubscription{}, store.PushDelivery{}, false, ctx.Err()
}

// When only the member's status cannot be read, a device the member still holds is sent to on its
// row alone, without the text, and one released meanwhile is not. The next member's send reads
// that member's status itself: one confirmed inactive is not sent to.
func TestStalledMemberReadSendsOnTheRow(t *testing.T) {
	f := newFixtureN(t, 1, pushQueueLen, func(f *fixture) {
		f.svc.joinBudget, f.svc.statusBudget = 100*time.Millisecond, 100*time.Millisecond
	})
	arrived, release := f.hold()
	defer release()
	f.st.includeTxt = true
	d := f.subscribe(t, "m1")
	f.subscribe(t, "m1")
	f.subscribe(t, "m2")
	released := f.st.subs[1]
	f.svc.store = stallMember{f.st, map[string]bool{"m1": true}}
	n := note("question")
	n.Payload = `{"questions":[{"question":"Deploy to prod?"}]}`
	var wg sync.WaitGroup
	f.svc.deliver(context.Background(), n, &wg) // m1's two devices; their status reads stall
	waitArrival(t, arrived)
	other := n
	other.MembershipID = "m2"
	f.svc.deliver(context.Background(), other, &wg) // m2: queued while still active
	if err := f.st.ReleasePushEndpoint(context.Background(), released.Endpoint, released.P256dh, released.Auth); err != nil {
		t.Fatal(err)
	}
	f.st.mu.Lock()
	f.st.inactive = map[string]bool{"m2": true}
	f.st.mu.Unlock()
	release()
	wg.Wait()
	if f.count() != 1 {
		t.Fatalf("%d sent; want 1 (m1's held device only)", f.count())
	}
	if got := d.open(t, f.bodies[0]); strings.Contains(got, `"text"`) || !strings.Contains(got, `"kind":"question"`) {
		t.Fatalf("send on the row alone: %s", got)
	}
	if got := f.svc.statusFailed.Load(); got != 2 {
		t.Fatalf("failed status reads: %d, want 2 (m1's devices only)", got)
	}
}
