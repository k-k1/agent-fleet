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
	subs       []store.PushSubscription
	off        map[string][]string
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
func (m *memStore) PushDeliveryContext(_ context.Context, mid string) (store.PushDelivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return store.PushDelivery{Active: !m.inactive[mid], IncludeText: m.includeTxt, TenantSlug: m.tenantSlug}, nil
}
func (m *memStore) GetTenantPushIncludeText(context.Context, string) (bool, error) { return false, nil }
func (m *memStore) PutTenantPushIncludeText(context.Context, string, bool, string) error {
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

func newFixture(t *testing.T) *fixture {
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
		timeout: 5 * time.Second, slotWait: 2 * time.Second, slots: make(chan struct{}, pushMaxInFlight), view: newViewing()}
	return f
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
	f.svc.deliver(context.Background(), note("question"))
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
	f.svc.deliver(context.Background(), note("question"))
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
	f.svc.deliver(context.Background(), n)
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
	f.svc.deliver(context.Background(), note("question"))
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
	f.svc.deliver(context.Background(), note("question"))
	if rows, _ := f.st.ListPushSubscriptions(context.Background(), "m1"); len(rows) != 1 {
		t.Fatal("a 5xx dropped the subscription")
	}
	f.status = http.StatusGone
	f.svc.deliver(context.Background(), note("question"))
	if rows, _ := f.st.ListPushSubscriptions(context.Background(), "m1"); len(rows) != 0 {
		t.Fatal("a 410 left the subscription")
	}
}

func TestEndpointOutsideAllowlistIsSkippedAndKept(t *testing.T) {
	f := newFixture(t)
	f.svc.check = func(string) error { return io.ErrUnexpectedEOF }
	f.subscribe(t, "m1")
	f.svc.deliver(context.Background(), note("question"))
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
	f.svc.deliver(context.Background(), note("question"))
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
	f.svc.deliver(context.Background(), note("question"))
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

// A wait for a slot must not shorten the send that follows it (the two budgets are separate).
func TestSlotWaitDoesNotShortenTheSend(t *testing.T) {
	f := newFixture(t)
	f.subscribe(t, "m1")
	f.svc.slotWait = 3 * time.Second
	f.svc.timeout = 3 * time.Second
	for i := 0; i < pushMaxInFlight; i++ {
		f.svc.slots <- struct{}{}
	}
	go func() {
		time.Sleep(1500 * time.Millisecond)
		<-f.svc.slots
	}()
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2500 * time.Millisecond) // 1.5 s wait + 2.5 s send > the 3 s a shared deadline would give
		f.mu.Lock()
		f.bodies = append(f.bodies, nil)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	// Through the real entry point, and success is read off last_ok_at, so a shared deadline
	// brought back there fails this test.
	f.svc.onNotification(note("question"))
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if rows, _ := f.st.ListPushSubscriptions(context.Background(), "m1"); len(rows) == 1 && rows[0].LastOKAt != "" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the send was cut short by time spent waiting for a slot")
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
	f.svc.deliver(context.Background(), note("question"))
	if buf.Len() == 0 || strings.Contains(buf.String(), "SECRET-TOKEN") || !strings.Contains(buf.String(), "bad redirect") {
		t.Fatalf("log: %q", buf.String())
	}
}
