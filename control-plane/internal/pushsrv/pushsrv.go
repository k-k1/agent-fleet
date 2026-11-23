// Package pushsrv is the Control Plane's Web Push service (#4750, part 1: sender and
// subscriptions); the HTTP routes are mounted by package main.
package pushsrv

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
	"github.com/k-k1/agent-fleet/control-plane/internal/webpush"
)

// Web Push for the installed PWA (#4750, part 1: sender and subscriptions).
//
// Off unless AF_WEB_PUSH=true. Then AF_WEB_PUSH_VAPID_PRIVATE_KEY (unpadded base64url scalar)
// and AF_WEB_PUSH_SUBJECT (a mailto: or https: contact for the push services) are required. The
// key pair belongs to the deployment, so there is no relay. Changing the key invalidates every
// subscription: the push services answer 401/403 for a subscription made against another
// applicationServerKey, and the Console has to subscribe again.
//
// AF_WEB_PUSH_ENDPOINT_HOSTS adds hosts (CSV; a leading dot = any subdomain) to the push
// services a subscription endpoint may name. Egress allowlists need the same hosts open.

// pushKinds are the notification kinds that reach a phone: the ones the desktop notification and
// the chat bridge treat as "a session needs you or finished". Everything else (schedules,
// usage, image refresh, ...) stays in the notification center.
var pushKinds = map[string]bool{
	"question": true, "plan-approval": true, "permission-request": true, "answer-ready": true,
}

const (
	// pushSendTimeout bounds one device; the HTTP client's own 15 s sits inside it.
	pushSendTimeout = 20 * time.Second
	// pushMaxInFlight is the number of send workers, so concurrent sends CP-wide; pushMaxPerMember
	// the devices one member may register.
	pushMaxInFlight  = 32
	pushMaxPerMember = 20
	// pushSlotWait is how long a send may wait in the queue; a message queued longer is stale for a
	// phone and dropped.
	pushSlotWait = 30 * time.Second
	// pushQueueLen bounds the sends waiting for a worker, and pushNoteQueueLen the notifications
	// waiting for their reads, so a burst costs bounded memory and goroutines instead of one waiting
	// goroutine per notification and device. Past either bound a message is dropped and logged.
	// 1024 is about fifty members' worth of a full device list for one notification each.
	pushQueueLen     = 1024
	pushNoteQueueLen = 256
	// pushReaders run the per-notification reads (membership, preferences, devices).
	pushReaders = 4
	// pushDropLogEvery is how often the counts of dropped notifications and messages are logged.
	pushDropLogEvery = 10 * time.Second
	// A question that waits an hour is stale for a phone; the service drops it after that.
	pushTTL = time.Hour
)

type Service struct {
	store  store.PushSubscriptionStore
	sender *webpush.Sender
	pubKey string
	// check vets an endpoint against the allowed push services; a field so tests can point the
	// service at a local TLS server.
	check    func(string) error
	timeout  time.Duration
	slotWait time.Duration
	notes    chan store.Notification
	jobs     chan sendJob
	// quit stops the workers; nil (never) outside tests.
	quit chan struct{}
	// Drops are counted, and only the ticker logs the counts: a drop happens on the inserting caller
	// (droppedNotes), a reader (droppedFull) or a send worker (droppedStale), and none of them may
	// wait on a stalled log sink, which would stop later healthy deliveries behind it.
	droppedNotes, droppedFull, droppedStale atomic.Int64
	view                                    *viewing
}

// sendJob is one notification for one device, waiting for a send worker.
type sendJob struct {
	membershipID string
	sub          store.PushSubscription
	body         []byte
	queued       time.Time
	// done, when set, is called once the job is sent or dropped (tests wait on it).
	done func()
}

// start makes the queues and runs the readers and the send workers for the life of the process.
func (p *Service) start(workers, queueLen int) {
	p.notes = make(chan store.Notification, pushNoteQueueLen)
	p.jobs = make(chan sendJob, queueLen)
	for i := 0; i < pushReaders; i++ {
		go func() {
			for {
				select {
				case n := <-p.notes:
					p.deliver(context.Background(), n, nil)
				case <-p.quit:
					return
				}
			}
		}()
	}
	go func() {
		t := time.NewTicker(pushDropLogEvery)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				p.logDrops()
			case <-p.quit:
				return
			}
		}
	}()
	for i := 0; i < workers; i++ {
		go func() {
			for {
				select {
				case j := <-p.jobs:
					p.run(j)
				case <-p.quit:
					return
				}
			}
		}()
	}
}

// validSubject accepts mailto:<addr with @> or an https URL with a host: services that check the
// contact reject anything else, and only at the first push.
func validSubject(s string) bool {
	if rest, ok := strings.CutPrefix(s, "mailto:"); ok {
		a, err := mail.ParseAddress(rest)
		return err == nil && a.Address == rest
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != ""
}

func Enabled() bool { return envx.Or("AF_WEB_PUSH", "") == "true" }

// New returns nil when Web Push is off or misconfigured (logged). A nil service makes
// the routes answer 503 and the sender do nothing, so a typo never takes the Control Plane down.
func New(st *store.SQL) *Service {
	if !Enabled() {
		return nil
	}
	keys, err := webpush.ParsePrivateKey(envx.Or("AF_WEB_PUSH_VAPID_PRIVATE_KEY", ""))
	if err != nil {
		log.Printf("WARN: web push disabled: %v", err)
		return nil
	}
	subject := strings.TrimSpace(envx.Or("AF_WEB_PUSH_SUBJECT", ""))
	if !validSubject(subject) {
		log.Printf("WARN: web push disabled: AF_WEB_PUSH_SUBJECT must be a mailto: or https: contact")
		return nil
	}
	hosts := append([]string{}, webpush.DefaultEndpointHosts...)
	for _, h := range strings.Split(envx.Or("AF_WEB_PUSH_ENDPOINT_HOSTS", ""), ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	p := &Service{store: st, sender: webpush.NewSender(keys, subject), pubKey: keys.PublicKey(),
		check: func(e string) error { return webpush.CheckEndpoint(e, hosts) }, timeout: pushSendTimeout, slotWait: pushSlotWait, view: newViewing()}
	p.start(pushMaxInFlight, pushQueueLen)
	st.SetNotificationHook(p.onNotification)
	log.Printf("web push enabled")
	return p
}

// pushPayload is the encrypted body the service worker reads. It is deliberately minimal: the
// session's display name and the event kind. The push services relay the ciphertext, and a
// tenant may not want a question's text to pass through them; the Console renders the wording
// locally from kind.
type pushPayload struct {
	V       int    `json:"v"`
	Kind    string `json:"kind"`
	Session string `json:"session"`
	Name    string `json:"name,omitempty"`
	// Text is a question's text. Present only when the tenant allows it (tenant_push_setting).
	Text string `json:"text,omitempty"`
}

// maxPushText keeps the text short: a push is a prompt to open the Console, and the service
// limits a payload to about 4 KB anyway.
const maxPushText = 140

// questionText is the first question's text of a "question" notification payload, or "".
func questionText(payload string) string {
	var p struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil || len(p.Questions) == 0 {
		return ""
	}
	r := []rune(strings.TrimSpace(p.Questions[0].Question))
	if len(r) > maxPushText {
		r = append(r[:maxPushText-1], '…')
	}
	return string(r)
}

func (p *Service) logDrops() {
	if d := p.droppedNotes.Swap(0); d > 0 {
		log.Printf("web push: delivery queue full; %d notifications dropped", d)
	}
	if d := p.droppedFull.Swap(0); d > 0 {
		log.Printf("web push: send queue full; %d messages dropped", d)
	}
	if d := p.droppedStale.Swap(0); d > 0 {
		log.Printf("web push: %d messages waited over %s in the send queue; dropped", d, p.slotWait)
	}
}

// onNotification runs on the inserting caller, so it only queues the delivery and never blocks,
// not even on log output.
func (p *Service) onNotification(n store.Notification) {
	if !pushKinds[n.Kind] || n.TargetType != "session" || n.TargetID == "" {
		return
	}
	select {
	case p.notes <- n:
	default:
		p.droppedNotes.Add(1)
	}
}

// deliver does the reads for one notification and queues one send per device. It has its own
// budget per read, and run gives each queued send two more: the time it may wait in the queue
// (slotWait) and the send itself (timeout, started when a worker takes it), so none eats another's.
// done, when set, gets one Add per queued send and one Done when that send ends or is dropped.
//
// Before any send, in order: the member must still be active (a removed member gets nothing), the
// kind must not be one the member turned off, and the session must not be on screen in an active
// Console. For these checks every doubt resolves to "send": a lost question costs more than a
// redundant buzz. Which devices hold the member is not such a check: a device list or a device
// that cannot be read is not sent to, since the browser may belong to someone else by now.
func (p *Service) deliver(ctx context.Context, n store.Notification, done *sync.WaitGroup) {
	// Each read has its own deadline: one that stalls until its deadline must not leave the next
	// read (the subscription list) with an expired context.
	read := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, p.timeout) }
	if p.view.on(n.MembershipID, n.TargetID, time.Now()) {
		return
	}
	cctx, ccancel := read()
	active, includeText, err := p.store.PushDeliveryContext(cctx, n.MembershipID)
	ccancel()
	if err != nil {
		log.Printf("web push: delivery context: %v", err)
		active, includeText = true, false
	}
	if !active {
		return
	}
	octx, ocancel := read()
	off, err := p.store.GetPushOffKinds(octx, n.MembershipID)
	ocancel()
	if err != nil {
		log.Printf("web push: preferences: %v", err)
	} else if slices.Contains(off, n.Kind) {
		return
	}
	lctx, lcancel := read()
	subs, err := p.store.ListPushSubscriptions(lctx, n.MembershipID)
	lcancel()
	if err != nil || len(subs) == 0 {
		if err != nil {
			log.Printf("web push: list subscriptions: %v", err)
		}
		return
	}
	pl := pushPayload{V: 1, Kind: n.Kind, Session: n.TargetID, Name: n.DisplayName}
	body, _ := json.Marshal(pl)
	if includeText && n.Kind == "question" {
		// The text is optional, the question is not: JSON escaping can make 140 runes several
		// times longer, so a text that would push the payload past what can be encrypted is dropped.
		pl.Text = questionText(n.Payload)
		if withText, _ := json.Marshal(pl); len(withText) <= webpush.MaxPayload {
			body = withText
		}
	}
	for _, s := range subs {
		// An endpoint the allowlist refuses is skipped, not deleted: a missing AF_WEB_PUSH_ENDPOINT_HOSTS
		// entry after a restart must not cost the member their subscriptions.
		if p.check(s.Endpoint) != nil {
			continue
		}
		j := sendJob{membershipID: n.MembershipID, sub: s, body: body, queued: time.Now()}
		if done != nil {
			done.Add(1)
			j.done = done.Done
		}
		select {
		case p.jobs <- j:
		default:
			p.droppedFull.Add(1)
			if j.done != nil {
				j.done()
			}
		}
	}
}

// run is one worker's turn on a queued send.
func (p *Service) run(j sendJob) {
	if j.done != nil {
		defer j.done()
	}
	if waited := time.Since(j.queued); waited > p.slotWait {
		p.droppedStale.Add(1)
		return
	}
	// The row is read again just before the send: in the queue it may have moved to another member
	// (the same browser signed in as someone else) or been removed, and this member's message must
	// not reach that browser. A failed read skips the send, as a failed list read does. A move that
	// commits after this read can still get the one message being sent.
	rctx, rcancel := context.WithTimeout(context.Background(), p.timeout)
	cur, held, err := p.store.GetPushSubscription(rctx, j.membershipID, j.sub.Endpoint)
	rcancel()
	if err != nil {
		log.Printf("web push: re-read %s before the send: %v", j.sub.ID, err)
		return
	}
	if !held {
		return
	}
	sctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	p.send(sctx, j.membershipID, cur, j.body)
}

func (p *Service) send(ctx context.Context, membershipID string, s store.PushSubscription, body []byte) {
	err := p.sender.Send(ctx, webpush.Subscription{Endpoint: s.Endpoint, P256dh: s.P256dh, Auth: s.Auth}, body,
		webpush.Options{TTL: pushTTL, Urgency: "high"})
	switch {
	case err == nil:
		_ = p.store.TouchPushSubscription(ctx, membershipID, s.Endpoint, store.NowTS())
	case errors.Is(err, webpush.ErrGone):
		_ = p.store.DeletePushSubscriptionByEndpoint(ctx, membershipID, s.Endpoint)
	default:
		// Send's errors carry no URL (the endpoint is a bearer URL for the member's browser).
		log.Printf("web push: send to %s: %v", s.ID, err)
	}
}

func canonicalB64(s string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && base64.RawURLEncoding.EncodeToString(raw) == s
}

// --- API -----------------------------------------------------------------------------------
//
// Handlers take the caller's membership id from package main's auth wrapper. A nil *Service (Web
// Push off or misconfigured) answers 503 on every one of them.

type errBody struct {
	Error errDetail `json:"error"`
}
type errDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errBody{errDetail{code, msg}})
}

func (p *Service) off(w http.ResponseWriter) bool {
	if p != nil {
		return false
	}
	writeErr(w, http.StatusServiceUnavailable, "push_unavailable", "web push is not configured on this deployment")
	return true
}

func internalErr(w http.ResponseWriter, err error) {
	log.Printf("web push api: %v", err)
	writeErr(w, http.StatusInternalServerError, "internal", "internal error")
}

type configWire struct {
	PublicKey string `json:"publicKey"`
}

type subscriptionWire struct {
	ID        string `json:"id"`
	UserAgent string `json:"userAgent"`
	CreatedAt string `json:"createdAt"`
	LastOKAt  string `json:"lastOkAt"`
}

type listWire struct {
	Subscriptions []subscriptionWire `json:"subscriptions"`
}

type createdWire struct {
	ID string `json:"id"`
}

// Config answers the applicationServerKey the browser subscribes with.
func (p *Service) Config(w http.ResponseWriter) {
	if p.off(w) {
		return
	}
	writeJSON(w, http.StatusOK, configWire{PublicKey: p.pubKey})
}

// List answers the caller's devices. The endpoint and keys never leave the server.
func (p *Service) List(w http.ResponseWriter, r *http.Request, membershipID string) {
	if p.off(w) {
		return
	}
	rows, err := p.store.ListPushSubscriptions(r.Context(), membershipID)
	if err != nil {
		internalErr(w, err)
		return
	}
	out := listWire{Subscriptions: make([]subscriptionWire, 0, len(rows))}
	for _, s := range rows {
		out.Subscriptions = append(out.Subscriptions, subscriptionWire{ID: s.ID, UserAgent: s.UserAgent, CreatedAt: s.CreatedAt, LastOKAt: s.LastOKAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// Subscribe stores the PushSubscription JSON a browser's pushManager.subscribe() produced.
func (p *Service) Subscribe(w http.ResponseWriter, r *http.Request, membershipID string) {
	if p.off(w) {
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	if err := p.check(in.Endpoint); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_endpoint", err.Error())
		return
	}
	// Keys are compared as stored strings when a browser is released from its previous member, and
	// Go's decoder ignores CR/LF, so one key has several spellings. Only the canonical unpadded
	// base64url is stored, which makes the string comparison exact.
	if !canonicalB64(in.Keys.P256dh) || !canonicalB64(in.Keys.Auth) {
		writeErr(w, http.StatusBadRequest, "bad_keys", "subscription keys must be unpadded base64url")
		return
	}
	// Refuse keys Encrypt cannot use now rather than failing silently on the first push.
	if _, err := webpush.Encrypt(webpush.Subscription{Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth}, []byte("x")); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_keys", "invalid subscription keys")
		return
	}
	ua := r.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	added, err := p.store.AddPushSubscription(r.Context(), store.PushSubscription{MembershipID: membershipID,
		Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth, UserAgent: ua}, pushMaxPerMember)
	if err != nil {
		internalErr(w, err)
		return
	}
	if !added {
		// The browser presents the subscription's own keys, so it has left its previous member:
		// stop delivering that member's notifications to it even though this member has no room
		// (otherwise a switch at the cap would keep the old association).
		if err := p.store.ReleasePushEndpoint(r.Context(), in.Endpoint, in.Keys.P256dh, in.Keys.Auth); err != nil {
			// Not released: answering 409 would leave the old member's notifications flowing to
			// this browser, so say it is retryable instead.
			internalErr(w, err)
			return
		}
		writeErr(w, http.StatusConflict, "too_many_devices", "too many registered devices; remove one first")
		return
	}
	writeJSON(w, http.StatusCreated, createdWire{ID: store.PushSubscriptionID(in.Endpoint)})
}

// Remove deletes one of the caller's devices.
func (p *Service) Remove(w http.ResponseWriter, r *http.Request, membershipID, id string) {
	if p.off(w) {
		return
	}
	ok, err := p.store.DeletePushSubscription(r.Context(), membershipID, id)
	if err != nil {
		internalErr(w, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such subscription")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- preferences, on-screen reports, release --------------------------------------------------

// PrefKinds are the kinds a member may turn off for push.
func PrefKinds() []string {
	out := make([]string, 0, len(pushKinds))
	for k := range pushKinds {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

type prefsWire struct {
	Off []string `json:"off"`
}

// GetPrefs answers the kinds the caller turned off.
func (p *Service) GetPrefs(w http.ResponseWriter, r *http.Request, membershipID string) {
	if p.off(w) {
		return
	}
	off, err := p.store.GetPushOffKinds(r.Context(), membershipID)
	if err != nil {
		internalErr(w, err)
		return
	}
	if off == nil {
		off = []string{}
	}
	writeJSON(w, http.StatusOK, prefsWire{Off: off})
}

// PutPrefs replaces the caller's turned-off kinds. An unknown kind is refused so a typo cannot
// look like a saved opt-out.
func (p *Service) PutPrefs(w http.ResponseWriter, r *http.Request, membershipID string) {
	if p.off(w) {
		return
	}
	var in prefsWire
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil || in.Off == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", `body must be {"off":[...]}`)
		return
	}
	seen := map[string]bool{}
	var off []string
	for _, k := range in.Off {
		if !pushKinds[k] {
			writeErr(w, http.StatusBadRequest, "bad_kind", "unknown push kind: "+k)
			return
		}
		if !seen[k] {
			seen[k] = true
			off = append(off, k)
		}
	}
	slices.Sort(off)
	if err := p.store.PutPushOffKinds(r.Context(), membershipID, off); err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prefsWire{Off: append([]string{}, off...)})
}

// Viewing records that the caller's Console shows session on screen right now. A Console sends it
// while the tab is visible and focused, about every 20 s; an entry older than viewTTL is ignored,
// so a closed or backgrounded tab stops suppressing pushes by itself. An empty session clears the
// device's entry. device is any stable id the Console picks; the server only keeps it apart per
// device so that one device clearing does not clear another's.
func (p *Service) Viewing(w http.ResponseWriter, r *http.Request, membershipID string) {
	if p.off(w) {
		return
	}
	var in struct {
		Device  string `json:"device"`
		Session string `json:"session"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil || in.Device == "" || len(in.Device) > 100 || len(in.Session) > 200 {
		writeErr(w, http.StatusBadRequest, "bad_request", "device and session required")
		return
	}
	p.view.set(membershipID, in.Device, in.Session, time.Now())
	w.WriteHeader(http.StatusNoContent)
}

// Release drops the subscription named by endpoint+keys without a session: the Console calls it
// while signing out, and the proof of owning the browser is the subscription's own keys. A row
// that is not there is success (the goal, "no push to this browser", already holds).
func (p *Service) Release(w http.ResponseWriter, r *http.Request) {
	if p.off(w) {
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in); err != nil || in.Endpoint == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	if err := p.store.ReleasePushEndpoint(r.Context(), in.Endpoint, in.Keys.P256dh, in.Keys.Auth); err != nil {
		internalErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
