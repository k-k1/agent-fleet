package pushsrv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
	v := newViewing()
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
