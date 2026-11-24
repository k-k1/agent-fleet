package pushsrv

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
	"github.com/k-k1/agent-fleet/control-plane/internal/webpush"
)

var b64 = base64.RawURLEncoding

type memStore struct {
	mu         sync.Mutex
	releaseErr error
	getErr     error
	gets       int
	joins      int
	joinErr    error
	subs       []store.PushSubscription
	off        map[string][]string
	offOrigins map[string][]string
	prefsErr   error
	inactive   map[string]bool
	includeTxt bool
	tenantSlug string
}

func (m *memStore) GetPushOffKinds(_ context.Context, mid string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.off[mid], nil
}
func (m *memStore) PutPushOffKinds(_ context.Context, mid string, k []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.off == nil {
		m.off = map[string][]string{}
	}
	m.off[mid] = k
	return nil
}
func (m *memStore) GetPushPrefs(_ context.Context, mid string) ([]string, []string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prefsErr != nil {
		return nil, nil, m.prefsErr
	}
	return m.off[mid], m.offOrigins[mid], nil
}
func (m *memStore) PutPushPrefs(_ context.Context, mid string, k []string, o *[]string) ([]string, []string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prefsErr != nil {
		return nil, nil, m.prefsErr
	}
	if m.off == nil {
		m.off = map[string][]string{}
	}
	if m.offOrigins == nil {
		m.offOrigins = map[string][]string{}
	}
	m.off[mid] = k
	if o != nil {
		m.offOrigins[mid] = *o
	}
	return m.off[mid], m.offOrigins[mid], nil
}
func (m *memStore) GetPushOffOrigins(_ context.Context, mid string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.offOrigins[mid], nil
}
func (m *memStore) PutPushOffOrigins(_ context.Context, mid string, o []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.offOrigins == nil {
		m.offOrigins = map[string][]string{}
	}
	m.offOrigins[mid] = o
	return nil
}
func (m *memStore) PushDeliveryContext(_ context.Context, mid string) (store.PushDelivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return store.PushDelivery{Active: !m.inactive[mid], IncludeText: m.includeTxt, TenantSlug: m.tenantSlug}, nil
}
func (m *memStore) GetTenantPushIncludeText(context.Context, string) (bool, error) { return false, nil }
func (m *memStore) PutTenantPushIncludeText(context.Context, string, string, bool, string) error {
	return nil
}

func (m *memStore) UpsertPushSubscription(_ context.Context, p store.PushSubscription) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p.ID = store.PushSubscriptionID(p.Endpoint)
	for i, s := range m.subs {
		if s.Endpoint == p.Endpoint {
			m.subs[i] = p
			return nil
		}
	}
	m.subs = append(m.subs, p)
	return nil
}
func (m *memStore) AddPushSubscription(ctx context.Context, p store.PushSubscription, max int) (bool, error) {
	m.mu.Lock()
	total, held := 0, false
	for _, s := range m.subs {
		if s.MembershipID == p.MembershipID {
			total++
			held = held || s.Endpoint == p.Endpoint
		}
	}
	m.mu.Unlock()
	if !held && total >= max {
		return false, nil
	}
	return true, m.UpsertPushSubscription(ctx, p)
}
func (m *memStore) GetPushSubscription(_ context.Context, mid, ep string) (store.PushSubscription, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	if m.getErr != nil {
		return store.PushSubscription{}, false, m.getErr
	}
	for _, s := range m.subs {
		if s.MembershipID == mid && s.Endpoint == ep {
			return s, true, nil
		}
	}
	return store.PushSubscription{}, false, nil
}

// GetPushSendTarget fails withText reads on joinErr (the setting table) and counts the reads
// without the setting in gets, as the subscription re-read they stand in for.
func (m *memStore) GetPushSendTarget(_ context.Context, mid, ep string, withText bool) (store.PushSubscription, store.PushDelivery, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if withText {
		m.joins++
		if m.joinErr != nil || m.getErr != nil {
			return store.PushSubscription{}, store.PushDelivery{}, false, errors.Join(m.joinErr, m.getErr)
		}
	} else {
		m.gets++
		if m.getErr != nil {
			return store.PushSubscription{}, store.PushDelivery{}, false, m.getErr
		}
	}
	for _, s := range m.subs {
		if s.MembershipID == mid && s.Endpoint == ep {
			return s, store.PushDelivery{Active: !m.inactive[mid], IncludeText: withText && m.includeTxt}, true, nil
		}
	}
	return store.PushSubscription{}, store.PushDelivery{}, false, nil
}
func (m *memStore) ListPushSubscriptions(_ context.Context, mid string) ([]store.PushSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.PushSubscription
	for _, s := range m.subs {
		if s.MembershipID == mid {
			out = append(out, s)
		}
	}
	return out, nil
}
func (m *memStore) DeletePushSubscription(_ context.Context, mid, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, s := range m.subs {
		if s.MembershipID == mid && s.ID == id {
			m.subs = append(m.subs[:i], m.subs[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}
func (m *memStore) DeletePushSubscriptionByEndpoint(_ context.Context, _, ep string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, s := range m.subs {
		if s.Endpoint == ep {
			m.subs = append(m.subs[:i], m.subs[i+1:]...)
			break
		}
	}
	return nil
}
func (m *memStore) ReleasePushEndpoint(ctx context.Context, ep, p256dh, auth string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.releaseErr != nil {
		return m.releaseErr
	}
	for i, s := range m.subs {
		if s.Endpoint == ep && s.P256dh == p256dh && s.Auth == auth {
			m.subs = append(m.subs[:i], m.subs[i+1:]...)
			break
		}
	}
	return nil
}
func (m *memStore) TouchPushSubscription(_ context.Context, _, ep, at string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, s := range m.subs {
		if s.Endpoint == ep {
			m.subs[i].LastOKAt = at
		}
	}
	return nil
}

// device is a browser: a subscription and the keys to read what the push service relays.
type device struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newDevice(t *testing.T) (device, string, string) {
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return device{priv, auth}, b64.EncodeToString(priv.PublicKey().Bytes()), b64.EncodeToString(auth)
}

// open is the user agent's half of RFC 8291.
func (d device) open(t *testing.T, body []byte) string {
	t.Helper()
	salt, asPubRaw := body[:16], body[21:21+int(body[20])]
	asPub, err := ecdh.P256().NewPublicKey(asPubRaw)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := d.priv.ECDH(asPub)
	prkKey, _ := hkdf.Extract(sha256.New, secret, d.auth)
	info := append(append([]byte("WebPush: info\x00"), d.priv.PublicKey().Bytes()...), asPubRaw...)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	pt, err := gcm.Open(nil, nonce, body[21+len(asPubRaw):], nil)
	if err != nil {
		t.Fatal(err)
	}
	return string(bytes.TrimSuffix(pt, []byte{2}))
}

type fixture struct {
	svc    *Service
	st     *memStore
	srv    *httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	status int
}

func newFixture(t *testing.T) *fixture { return newFixtureN(t, pushMaxInFlight, pushQueueLen, nil) }

// newFixtureN starts the service with workers send workers and a send queue of queueLen; setup
// runs before they start, for fields the workers read.
func newFixtureN(t *testing.T, workers, queueLen int, setup func(*fixture)) *fixture {
	f := &fixture{st: &memStore{}, status: http.StatusCreated}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, b)
		st := f.status
		f.mu.Unlock()
		w.WriteHeader(st)
	}))
	t.Cleanup(f.srv.Close)
	keys, _, _ := webpush.GenerateKeys()
	sender := webpush.NewSender(keys, "mailto:ops@example.invalid")
	sender.HTTP = f.srv.Client()
	sender.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	f.svc = &Service{store: f.st, sender: sender, pubKey: keys.PublicKey(),
		check: func(e string) error {
			return webpush.CheckEndpoint(strings.Replace(e, f.srv.URL, "https://fcm.googleapis.com", 1), webpush.DefaultEndpointHosts)
		},
		timeout: 5 * time.Second, slotWait: 2 * time.Second, quit: make(chan struct{})}
	if setup != nil {
		setup(f)
	}
	f.svc.start(workers, queueLen)
	t.Cleanup(func() { close(f.svc.quit) })
	return f
}

// deliver runs one notification's delivery and waits until every queued send ended.
func (f *fixture) deliver(n store.Notification) {
	var wg sync.WaitGroup
	f.svc.deliver(context.Background(), n, &wg)
	wg.Wait()
}

func (f *fixture) subscribe(t *testing.T, mid string) device {
	d, p256, auth := newDevice(t)
	if err := f.st.UpsertPushSubscription(context.Background(), store.PushSubscription{MembershipID: mid,
		Endpoint: f.srv.URL + "/p/" + p256[:8], P256dh: p256, Auth: auth}); err != nil {
		t.Fatal(err)
	}
	return d
}

func (f *fixture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func note(kind string) store.Notification {
	return store.Notification{EventID: "e", MembershipID: "m1", Kind: kind, TargetType: "session", TargetID: "s1",
		DisplayName: "My session", Payload: `{"question":"SECRET TEXT"}`}
}

func TestDeliverIsMinimal(t *testing.T) {
	f := newFixture(t)
	d := f.subscribe(t, "m1")
	f.deliver(note("question"))
	if f.count() != 1 {
		t.Fatalf("pushes: %d", f.count())
	}
	got := d.open(t, f.bodies[0])
	if got != `{"v":1,"kind":"question","session":"s1","name":"My session"}` {
		t.Fatalf("payload %s", got)
	}
	if strings.Contains(got, "SECRET") {
		t.Fatal("the question text reached the push service")
	}
}

// The payload names the receiving membership's tenant so the Console can open the session in that
// tenant rather than in whichever one it currently shows.
func TestPayloadCarriesTheTenantSlug(t *testing.T) {
	f := newFixture(t)
	f.st.tenantSlug = "acme"
	d := f.subscribe(t, "m1")
	f.deliver(note("question"))
	if f.count() != 1 {
		t.Fatalf("pushes: %d", f.count())
	}
	if got := d.open(t, f.bodies[0]); got != `{"v":1,"kind":"question","session":"s1","name":"My session","tenant":"acme"}` {
		t.Fatalf("payload %s", got)
	}
}

// A display name so long that even the base payload cannot be encrypted must not cost the push:
// the name goes, the session and the tenant stay.
func TestOverlongNameDropsTheNameNotThePush(t *testing.T) {
	f := newFixture(t)
	f.st.tenantSlug = "acme"
	d := f.subscribe(t, "m1")
	n := note("question")
	n.DisplayName = strings.Repeat("x", 3930) // main's base JSON is 3980 bytes; the tenant tips it past 3993
	f.deliver(n)
	if f.count() != 1 {
		t.Fatalf("the push was lost to a long name: %d", f.count())
	}
	if got := d.open(t, f.bodies[0]); got != `{"v":1,"kind":"question","session":"s1","tenant":"acme"}` {
		t.Fatalf("payload %s", got)
	}
}

// failingCtxStore fails the delivery-context read: the push still goes out, without a tenant.
type failingCtxStore struct{ *memStore }

func (failingCtxStore) PushDeliveryContext(context.Context, string) (store.PushDelivery, error) {
	return store.PushDelivery{}, errors.New("db down")
}

func TestPayloadWithoutTenantWhenTheLookupFails(t *testing.T) {
	f := newFixture(t)
	f.st.tenantSlug = "acme"
	f.svc.store = failingCtxStore{f.st}
	d := f.subscribe(t, "m1")
	f.deliver(note("question"))
	if f.count() != 1 {
		t.Fatalf("a failed tenant lookup must not lose the push: %d", f.count())
	}
	if got := d.open(t, f.bodies[0]); strings.Contains(got, "tenant") {
		t.Fatalf("payload %s", got)
	}
}

func TestOnNotificationFilters(t *testing.T) {
	f := newFixture(t)
	f.subscribe(t, "m1")
	for _, n := range []store.Notification{
		note("schedule-run"), // a kind that stays in the center
		func() store.Notification { n := note("question"); n.TargetType = "workspace"; return n }(),
		func() store.Notification { n := note("question"); n.TargetID = ""; return n }(),
	} {
		f.svc.onNotification(n)
	}
	f.svc.onNotification(store.Notification{MembershipID: "other", Kind: "question", TargetType: "session", TargetID: "s"})
	time.Sleep(150 * time.Millisecond)
	if f.count() != 0 {
		t.Fatalf("pushed %d messages that must not be pushed", f.count())
	}
	for _, k := range []string{"question", "plan-approval", "permission-request", "answer-ready"} {
		f.svc.onNotification(note(k))
	}
	deadline := time.Now().Add(3 * time.Second)
	for f.count() < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.count() != 4 {
		t.Fatalf("pushed %d of 4 kinds", f.count())
	}
}

func TestGoneSubscriptionIsDeletedButFailureIsKept(t *testing.T) {
	f := newFixture(t)
	f.subscribe(t, "m1")
	f.status = http.StatusInternalServerError
	f.deliver(note("question"))
	if rows, _ := f.st.ListPushSubscriptions(context.Background(), "m1"); len(rows) != 1 {
		t.Fatal("a 5xx dropped the subscription")
	}
	f.status = http.StatusGone
	f.deliver(note("question"))
	if rows, _ := f.st.ListPushSubscriptions(context.Background(), "m1"); len(rows) != 0 {
		t.Fatal("a 410 left the subscription")
	}
}

func TestEndpointOutsideAllowlistIsSkippedAndKept(t *testing.T) {
	f := newFixture(t)
	f.svc.check = func(string) error { return io.ErrUnexpectedEOF }
	f.subscribe(t, "m1")
	f.deliver(note("question"))
	if f.count() != 0 {
		t.Fatal("posted to an endpoint the allowlist refuses")
	}
	// A restart without the extra AF_WEB_PUSH_ENDPOINT_HOSTS entry must not cost the subscription.
	if rows, _ := f.st.ListPushSubscriptions(context.Background(), "m1"); len(rows) != 1 {
		t.Fatal("refused endpoint was deleted")
	}
}

func TestSlowDeviceDoesNotStarveTheOthers(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/slow") {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		f.mu.Lock()
		f.bodies = append(f.bodies, b)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	_, p1, a1 := newDevice(t)
	_ = f.st.UpsertPushSubscription(context.Background(), store.PushSubscription{MembershipID: "m1", Endpoint: f.srv.URL + "/slow", P256dh: p1, Auth: a1})
	f.subscribe(t, "m1")
	f.svc.timeout = 600 * time.Millisecond
	f.deliver(note("question"))
	close(release)
	if f.count() != 1 {
		t.Fatalf("the healthy device got %d pushes behind a hanging one", f.count())
	}
}

func TestSendErrorLogsNoEndpoint(t *testing.T) {
	f := newFixture(t)
	f.subscribe(t, "m1")
	f.srv.Close() // connection refused: *url.Error would print the full URL
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	f.deliver(note("question"))
	if buf.Len() == 0 {
		t.Fatal("no failure logged; the check below would pass vacuously")
	}
	if strings.Contains(buf.String(), "/p/") || strings.Contains(buf.String(), f.srv.URL) {
		t.Fatalf("log carries the endpoint: %s", buf.String())
	}
}

func TestDeviceCap(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < pushMaxPerMember; i++ {
		f.subscribe(t, "m1")
	}
	_, p256, auth := newDevice(t)
	post := func(ep string) int {
		w := httptest.NewRecorder()
		f.svc.Subscribe(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"endpoint":"`+ep+`","keys":{"p256dh":"`+p256+`","auth":"`+auth+`"}}`)), "m1")
		return w.Code
	}
	if c := post(f.srv.URL + "/new"); c != 409 {
		t.Fatalf("over the cap: %d", c)
	}
	rows, _ := f.st.ListPushSubscriptions(context.Background(), "m1")
	// A browser that switches to a member with no room is still released from the previous one,
	// but only when it presents that subscription's own keys.
	f.st.subs = append(f.st.subs, store.PushSubscription{MembershipID: "m0", Endpoint: f.srv.URL + "/new", P256dh: p256, Auth: auth})
	_, otherKey, otherAuth := newDevice(t)
	foreign := func() int {
		w := httptest.NewRecorder()
		f.svc.Subscribe(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"endpoint":"`+f.srv.URL+`/new","keys":{"p256dh":"`+otherKey+`","auth":"`+otherAuth+`"}}`)), "m1")
		return w.Code
	}
	if c := foreign(); c != 409 {
		t.Fatalf("foreign keys over the cap: %d", c)
	}
	if len(f.st.subs) != pushMaxPerMember+1 {
		t.Fatal("a request with foreign keys released the previous owner's subscription")
	}
	f.st.releaseErr = io.ErrUnexpectedEOF
	if c := post(f.srv.URL + "/new"); c != 500 {
		t.Fatalf("a failed release must not read as the plain cap error: %d", c)
	}
	f.st.releaseErr = nil
	if c := post(f.srv.URL + "/new"); c != 409 {
		t.Fatalf("over the cap: %d", c)
	}
	for _, s := range f.st.subs {
		if s.MembershipID == "m0" {
			t.Fatal("the previous owner still holds the endpoint after a switch at the cap")
		}
	}
	if c := post(rows[0].Endpoint); c != 201 { // re-registering a held endpoint is not a new device
		t.Fatalf("refresh at the cap: %d", c)
	}
}

func TestNonCanonicalKeysAreRefused(t *testing.T) {
	f := newFixture(t)
	_, p256, auth := newDevice(t)
	for name, keys := range map[string][2]string{
		"newline in p256dh": {p256[:10] + "\\n" + p256[10:], auth},
		"newline in auth":   {p256, auth[:5] + "\\r\\n" + auth[5:]},
		"padded auth":       {p256, auth + "=="},
	} {
		w := httptest.NewRecorder()
		f.svc.Subscribe(w, httptest.NewRequest("POST", "/", strings.NewReader(
			`{"endpoint":"`+f.srv.URL+`/k","keys":{"p256dh":"`+keys[0]+`","auth":"`+keys[1]+`"}}`)), "m1")
		if w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if len(f.st.subs) != 0 {
		t.Fatal("a non-canonical subscription was stored")
	}
}

func TestValidSubject(t *testing.T) {
	for s, want := range map[string]bool{"mailto:ops@example.invalid": true, "https://example.invalid/contact": true,
		"mailto:": false, "mailto:ops@@example.invalid": false, "mailto:@x": false, "https://": false, "http://example.invalid": false, "": false} {
		if validSubject(s) != want {
			t.Errorf("%q: want %v", s, want)
		}
	}
}

func TestAPI(t *testing.T) {
	f := newFixture(t)
	do := func(h func(http.ResponseWriter, *http.Request), method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(method, "/", strings.NewReader(body)))
		return w
	}
	_, p256, auth := newDevice(t)
	good := `{"endpoint":"` + f.srv.URL + `/x","keys":{"p256dh":"` + p256 + `","auth":"` + auth + `"}}`
	sub := func(mid, body string) *httptest.ResponseRecorder {
		return do(func(w http.ResponseWriter, r *http.Request) { f.svc.Subscribe(w, r, mid) }, "POST", body)
	}
	if w := sub("m1", good); w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, bad := range []string{`{`, `{"endpoint":"http://x/","keys":{}}`,
		`{"endpoint":"` + f.srv.URL + `/y","keys":{"p256dh":"AAAA","auth":"AAAA"}}`} {
		if w := sub("m1", bad); w.Code != 400 {
			t.Fatalf("%s: %d", bad, w.Code)
		}
	}
	w := do(func(w http.ResponseWriter, r *http.Request) { f.svc.List(w, r, "m1") }, "GET", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "endpoint") || strings.Contains(w.Body.String(), auth) {
		t.Fatalf("list leaks a secret: %d %s", w.Code, w.Body)
	}
	id := store.PushSubscriptionID(f.srv.URL + "/x")
	rm := func(mid string) int {
		return do(func(w http.ResponseWriter, r *http.Request) { f.svc.Remove(w, r, mid, id) }, "DELETE", "").Code
	}
	if rm("m2") != 404 {
		t.Fatal("another member removed it")
	}
	if rm("m1") != 204 {
		t.Fatal("owner could not remove it")
	}

	var off *Service
	if w := do(func(w http.ResponseWriter, r *http.Request) { off.List(w, r, "m1") }, "GET", ""); w.Code != 503 {
		t.Fatalf("nil service: %d", w.Code)
	}
}

// Time spent waiting in the send queue must not shorten the send that follows it (the two budgets
// are separate). One worker, so the third device waits behind two slow sends; its wait plus its own
// send (about 4.5 s) is longer than the slot wait (4 s) and than twice the send timeout (4 s), so
// sharing any of those deadlines between the wait and the send fails this test.
func TestQueueWaitDoesNotShortenTheSend(t *testing.T) {
	f := newFixtureN(t, 1, pushQueueLen, func(f *fixture) {
		f.svc.slotWait = 4 * time.Second
		f.svc.timeout = 2 * time.Second
	})
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	})
	for i := 0; i < 3; i++ {
		f.subscribe(t, "m1")
	}
	// Through the real entry point, and success is read off last_ok_at, so a deadline shared with
	// the wait brought back there fails this test.
	f.svc.onNotification(note("question"))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if rows, _ := f.st.ListPushSubscriptions(context.Background(), "m1"); len(rows) == 3 && rows[2].LastOKAt != "" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the last send was cut short by time spent waiting in the queue")
}

// hold makes every send hang until the returned func is called, and reports each arrival.
func (f *fixture) hold() (arrived chan string, release func()) {
	arrived = make(chan string, 64)
	gate := make(chan struct{})
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		arrived <- r.URL.Path
		<-gate
		f.mu.Lock()
		f.bodies = append(f.bodies, b)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	return arrived, release
}

// waitArrival takes the next arrival from hold, failing the test after a bound.
func waitArrival(t *testing.T, arrived chan string) string {
	t.Helper()
	select {
	case p := <-arrived:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no send reached the push service")
		return ""
	}
}

// A burst past the queue is dropped at once rather than parked in a goroutine per device, and the
// drop waits on nothing, log output included: with the log sink stalled, the reader still returns
// and the next notification is sent once the queue has room.
func TestFullSendQueueDrops(t *testing.T) {
	// Long budgets: this test is about room in the queue, not about staleness.
	f := newFixtureN(t, 1, 2, func(f *fixture) { f.svc.slotWait, f.svc.timeout = time.Minute, time.Minute })
	arrived, release := f.hold()
	// wg is waited on only on the path where every Add has been made; a failure path releases the
	// sends and leaves the rest to the fixture's cleanup.
	var wg sync.WaitGroup
	defer release()
	// Occupy the only worker first, so the queue's two places are all there is.
	f.subscribe(t, "m0")
	first := note("question")
	first.MembershipID = "m0"
	f.svc.deliver(context.Background(), first, &wg)
	waitArrival(t, arrived)
	for i := 0; i < 4; i++ {
		f.subscribe(t, "m1")
	}
	unstall := stallLog(t)
	returned := make(chan struct{})
	go func() {
		f.svc.deliver(context.Background(), note("question"), &wg)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		// Let the stuck reader finish before failing, so the test ends here instead of hanging.
		unstall()
		release()
		select {
		case <-returned:
		case <-time.After(5 * time.Second):
		}
		t.Fatal("a reader waited on log output while dropping")
	}
	if got := f.svc.droppedFull.Load(); got != 2 {
		t.Fatalf("%d drops counted, want 2", got)
	}
	release()
	wg.Wait()
	if f.count() != 3 {
		t.Fatalf("%d sent, want the one in flight and the 2 that fit", f.count())
	}
	// The queue has room again: the next notification goes out with the log still stalled.
	again := note("question")
	again.MembershipID = "m0"
	f.deliver(again)
	unstall()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	if f.count() != 4 {
		t.Fatalf("%d sent; the notification after the burst was lost", f.count())
	}
	f.svc.logDrops()
	if !strings.Contains(buf.String(), "send queue full; 2 messages dropped") {
		t.Fatalf("log: %q", buf.String())
	}
}

// blockedWriter is log output that never returns, like a stalled stderr pipe.
type blockedWriter struct{ gate chan struct{} }

func (b blockedWriter) Write(p []byte) (int, error) { <-b.gate; return len(p), nil }

// stallLog points the log at a sink that blocks every write until the returned func (or the
// test's end, on every failure path) opens it once and puts stderr back.
func stallLog(t *testing.T) (unstall func()) {
	bw := blockedWriter{make(chan struct{})}
	log.SetOutput(bw)
	var once sync.Once
	unstall = func() {
		once.Do(func() {
			close(bw.gate)
			log.SetOutput(os.Stderr)
		})
	}
	t.Cleanup(unstall)
	return unstall
}

// A full notification queue drops the notification without waiting on anything, log output
// included: it runs on the caller that inserted the notification.
func TestFullNotificationQueueDrops(t *testing.T) {
	p := &Service{notes: make(chan store.Notification)} // no reader takes from this one
	unstall := stallLog(t)
	returned := make(chan struct{})
	go func() {
		p.onNotification(note("question"))
		p.onNotification(note("question"))
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Error("onNotification blocked on a full queue")
	}
	unstall()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	p.logDrops()
	p.logDrops() // the count was taken; nothing more to say
	if strings.Count(buf.String(), "delivery queue full; 2 notifications dropped") != 1 || strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("log: %q", buf.String())
	}
}

// A send that waited longer than slotWait for a worker is stale for a phone and is dropped.
func TestStaleQueuedSendIsDropped(t *testing.T) {
	f := newFixtureN(t, 1, pushQueueLen, func(f *fixture) { f.svc.slotWait = 200 * time.Millisecond })
	arrived, release := f.hold()
	defer release()
	f.subscribe(t, "m1")
	f.subscribe(t, "m1")
	// A stalled log sink must not hold the worker that drops the stale message.
	stallLog(t)
	var wg sync.WaitGroup
	f.svc.deliver(context.Background(), note("question"), &wg)
	waitArrival(t, arrived)
	time.Sleep(400 * time.Millisecond)
	release()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker waited on log output while dropping a stale message")
	}
	if f.count() != 1 || f.svc.droppedStale.Load() != 1 {
		t.Fatalf("%d sent, %d counted stale; want 1 and 1", f.count(), f.svc.droppedStale.Load())
	}
}

// A device that moves to another member (or is removed) while its send is queued gets nothing,
// and a re-registration with new keys is sent with the new keys.
func TestQueuedSendRereadsTheDevice(t *testing.T) {
	f := newFixtureN(t, 1, pushQueueLen, nil)
	arrived, release := f.hold()
	defer release()
	f.subscribe(t, "m1") // occupies the worker
	_, pMoved, aMoved := newDevice(t)
	moved := store.PushSubscription{MembershipID: "m1", Endpoint: f.srv.URL + "/moved", P256dh: pMoved, Auth: aMoved}
	_ = f.st.UpsertPushSubscription(context.Background(), moved)
	_, pOld, aOld := newDevice(t)
	dNew, pNew, aNew := newDevice(t)
	rekeyed := store.PushSubscription{MembershipID: "m1", Endpoint: f.srv.URL + "/rekeyed", P256dh: pOld, Auth: aOld}
	_ = f.st.UpsertPushSubscription(context.Background(), rekeyed)
	var wg sync.WaitGroup
	f.svc.deliver(context.Background(), note("question"), &wg)
	waitArrival(t, arrived)
	moved.MembershipID = "m2"
	_ = f.st.UpsertPushSubscription(context.Background(), moved)
	rekeyed.P256dh, rekeyed.Auth = pNew, aNew
	_ = f.st.UpsertPushSubscription(context.Background(), rekeyed)
	release()
	wg.Wait()
	var paths []string
	for len(arrived) > 0 {
		paths = append(paths, <-arrived)
	}
	if len(paths) != 1 || paths[0] != "/rekeyed" {
		t.Fatalf("sends after the first: %v, want only /rekeyed", paths)
	}
	f.mu.Lock()
	last := f.bodies[len(f.bodies)-1]
	f.mu.Unlock()
	if !strings.Contains(dNew.open(t, last), `"kind":"question"`) {
		t.Fatal("the re-keyed device could not read its push")
	}
}

// A failed re-read of the row skips the send, as a failed list read does.
func TestFailedRereadSkipsTheSend(t *testing.T) {
	f := newFixture(t)
	f.subscribe(t, "m1")
	f.st.getErr = io.ErrUnexpectedEOF
	f.deliver(note("question"))
	if f.count() != 0 || f.st.gets != 2 {
		t.Fatalf("sent %d with %d re-reads; want none sent after the status and row reads failed", f.count(), f.st.gets)
	}
}

func TestBadRedirectLeavesNoToken(t *testing.T) {
	f := newFixture(t)
	f.subscribe(t, "m1")
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://x.invalid/\x7f%zz?token=SECRET-TOKEN")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	f.deliver(note("question"))
	if buf.Len() == 0 || strings.Contains(buf.String(), "SECRET-TOKEN") || !strings.Contains(buf.String(), "bad redirect") {
		t.Fatalf("log: %q", buf.String())
	}
}
