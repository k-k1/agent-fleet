// Package pushsrv is the Control Plane's Web Push service (#4750, part 1: sender and
// subscriptions); the HTTP routes are mounted by package main.
package pushsrv

import (
	"context"
	"crypto/rand"
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
// usage, image refresh, ...) stays in the notification center. turn-failed stands in for the
// answer-ready a failed auto-run turn used to raise: leaving it out silences a failure on the
// phone while a success still pushes.
var pushKinds = map[string]bool{
	"question": true, "plan-approval": true, "permission-request": true, "answer-ready": true,
	"turn-failed": true,
}

// pushOrigins are the origin switches a member may turn off, each with the (root, child) pair it
// matches: the root session of an own / auto-run / scheduled chain, and the sessions that chain's
// root started. Sessions are told apart by the Agent (workspace notice.OriginOf).
var pushOrigins = map[string]struct {
	root  string
	child bool
}{
	"own": {"own", false}, "own-child": {"own", true},
	"autorun": {"autorun", false}, "autorun-child": {"autorun", true},
	"schedule": {"schedule", false}, "schedule-child": {"schedule", true},
}

// originKey is the switch name a notification falls under; an unknown or empty root is own.
func originKey(root string, child bool) string {
	switch root {
	case "autorun", "schedule":
	default:
		root = "own"
	}
	if child {
		return root + "-child"
	}
	return root
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
	// pushJoinBudget bounds sendTarget's read with the text setting, well below pushSlotWait, and
	// pushJoinOffFor is how long sendTarget leaves the setting out after that read failed.
	// pushStatusBudget bounds its read of the member's status without the setting: past it the
	// device row is read alone, so a stall on the membership table costs each send that much, not
	// the whole send budget.
	pushJoinBudget   = 2 * time.Second
	pushJoinOffFor   = 30 * time.Second
	pushStatusBudget = 2 * time.Second
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
	// joinFailed counts send-time reads with the text setting that failed; sendTarget then read
	// the member's status again without it.
	joinFailed atomic.Int64
	// statusFailed counts send-time reads of the member's status that failed; sendTarget then read
	// the device row alone, and a row this member still held was sent to without the text.
	statusFailed atomic.Int64
	// joinOffUntil (unix nanoseconds) is when sendTarget tries the joined read again after a failure.
	joinOffUntil atomic.Int64
	// joinBudget, joinOffFor and statusBudget override pushJoinBudget, pushJoinOffFor and
	// pushStatusBudget when set (tests).
	joinBudget, joinOffFor, statusBudget time.Duration
	// dropLogEvery is the ticker's period; 0 runs no ticker (tests call logDrops themselves).
	dropLogEvery time.Duration
	view         viewing
}

// sendJob is one notification for one device, waiting for a send worker.
type sendJob struct {
	membershipID string
	sub          store.PushSubscription
	body         []byte
	// plain is body without the question's text; the same slice when body carries none. run sends
	// it instead when the tenant setting has gone off by the time of the send.
	plain  []byte
	queued time.Time
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
	if p.dropLogEvery > 0 {
		go func() {
			t := time.NewTicker(p.dropLogEvery)
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
	}
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
		check: func(e string) error { return webpush.CheckEndpoint(e, hosts) }, timeout: pushSendTimeout, slotWait: pushSlotWait, dropLogEvery: pushDropLogEvery}
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
	// Tenant is the slug of the tenant the session belongs to (the receiving membership's). A
	// browser registered under one tenant may have its Console on another; without it a tap would
	// open a same-named session of the wrong tenant. Absent when the lookup failed.
	Tenant string `json:"tenant,omitempty"`
	// Origin is the originKey of the notification (one of the six switch names). The service worker
	// turns it into a title prefix; it is always present, and own has no prefix there.
	Origin string `json:"origin"`
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
	if d := p.joinFailed.Swap(0); d > 0 {
		log.Printf("web push: %d send-time reads with the text setting failed", d)
	}
	if d := p.statusFailed.Swap(0); d > 0 {
		log.Printf("web push: %d send-time reads of the member's status failed; the device row alone decided those sends, without the text", d)
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
// Console. run checks the membership and the text setting again after the wait for a worker. For
// these checks every doubt resolves to "send": a lost question costs more than a redundant buzz. Which devices hold the member is not such a check: a device list or a device
// that cannot be read is not sent to, since the browser may belong to someone else by now.
func (p *Service) deliver(ctx context.Context, n store.Notification, done *sync.WaitGroup) {
	// Each read has its own deadline: one that stalls until its deadline must not leave the next
	// read (the subscription list) with an expired context.
	read := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, p.timeout) }
	if p.view.on(n.MembershipID, n.TargetID, time.Now()) {
		return
	}
	cctx, ccancel := read()
	dc, err := p.store.PushDeliveryContext(cctx, n.MembershipID)
	ccancel()
	if err != nil {
		// Unknown tenant: the payload then carries none, and the Console opens the session where
		// it already is, as it did before the tenant was in the payload.
		log.Printf("web push: delivery context: %v", err)
		dc = store.PushDelivery{Active: true}
	}
	if !dc.Active {
		return
	}
	includeText := dc.IncludeText
	octx, ocancel := read()
	off, offOrigins, err := p.store.GetPushPrefs(octx, n.MembershipID)
	ocancel()
	// A notification is pushed only when its kind and its origin are both on. Both come from one
	// read, so a save between two reads cannot let through what no saved state allows. A failed
	// read pushes: a stall must not silence a member.
	if err != nil {
		log.Printf("web push: preferences: %v", err)
	} else if slices.Contains(off, n.Kind) || slices.Contains(offOrigins, originKey(n.OriginRoot, n.OriginChild)) {
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
	pl := pushPayload{V: 1, Kind: n.Kind, Session: n.TargetID, Name: n.DisplayName, Tenant: dc.TenantSlug, Origin: originKey(n.OriginRoot, n.OriginChild)}
	body, _ := json.Marshal(pl)
	if len(body) > webpush.MaxPayload {
		// The display name is stored as the Agent sent it. A name that makes even the base payload
		// unencryptable is dropped, not the push: the session and the tenant are what a tap needs.
		pl.Name = ""
		body, _ = json.Marshal(pl)
	}
	plain := body
	if includeText && n.Kind == "question" {
		// The text is optional, the question is not: JSON escaping can make 140 runes several
		// times longer, so a text that would push the payload past what can be encrypted is dropped.
		if pl.Text = questionText(n.Payload); pl.Text != "" {
			if withText, _ := json.Marshal(pl); len(withText) <= webpush.MaxPayload {
				body = withText
			}
		}
	}
	for _, s := range subs {
		// An endpoint the allowlist refuses is skipped, not deleted: a missing AF_WEB_PUSH_ENDPOINT_HOSTS
		// entry after a restart must not cost the member their subscriptions.
		if p.check(s.Endpoint) != nil {
			continue
		}
		j := sendJob{membershipID: n.MembershipID, sub: s, body: body, plain: plain, queued: time.Now()}
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
	// The row is read again just before the send: in the queue the device may have moved to another
	// member (the same browser signed in as someone else) or been removed, the member may have been
	// removed, or text turned off, and none of that may still get what deliver built before it. A
	// failed read of the row skips the send, as a failed list read does; a failed read of only the
	// member's status does not (see sendTarget). A change that commits after this read can still
	// reach the one message being sent.
	cur, dc, held, err := p.sendTarget(j)
	if err != nil {
		log.Printf("web push: re-read %s before the send: %v", j.sub.ID, err)
		return
	}
	if !held || !dc.Active {
		return
	}
	body := j.body
	if !dc.IncludeText {
		body = j.plain
	}
	sctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	p.send(sctx, j.membershipID, cur, body)
}

// sendTarget reads the device's row with the member's status and the tenant's text setting in one
// statement, so all three are checked at one point in time. The setting is the part a send can do
// without, so that statement runs on a short budget of its own, and when it fails the row and the
// member's status are read again without the setting, and the send goes out without the text. A
// failure also leaves the setting out for joinOffFor, so a stall on its table costs one short wait
// per worker rather than one per queued send, which would make the sends behind them stale.
//
// The member's status is read on every send, also on a short budget, and never skipped because
// another send's read failed. Only when this send's own status read fails is the device row read
// alone, and a row this member still holds is sent to without the text, as main's worker did; a
// lock on the membership table (DDL's ACCESS EXCLUSIVE; a row lock does not block these reads)
// must not silence every push. So a member the read answered as inactive is never sent to, but
// while their status cannot be read, each queued send to a device they still hold (a deactivated
// membership keeps its rows) can go out without the text.
//
// All of these reads share one budget of timeout, the one main's single re-read had, so a stall on
// every table holds the worker no longer before its send than it did there. The cost is that a row
// read which needed nearly all of it on main can now run out after the short reads took their part.
func (p *Service) sendTarget(j sendJob) (store.PushSubscription, store.PushDelivery, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	if time.Now().UnixNano() >= p.joinOffUntil.Load() {
		jctx, jcancel := context.WithTimeout(ctx, orDefault(p.joinBudget, pushJoinBudget))
		cur, dc, held, err := p.store.GetPushSendTarget(jctx, j.membershipID, j.sub.Endpoint, true)
		jcancel()
		if err == nil {
			return cur, dc, held, nil
		}
		p.joinFailed.Add(1)
		p.joinOffUntil.Store(time.Now().Add(orDefault(p.joinOffFor, pushJoinOffFor)).UnixNano())
	}
	sctx, scancel := context.WithTimeout(ctx, orDefault(p.statusBudget, pushStatusBudget))
	cur, dc, held, err := p.store.GetPushSendTarget(sctx, j.membershipID, j.sub.Endpoint, false)
	scancel()
	if err == nil {
		return cur, dc, held, nil
	}
	p.statusFailed.Add(1)
	cur, held, rerr := p.store.GetPushSubscription(ctx, j.membershipID, j.sub.Endpoint)
	if rerr != nil {
		return store.PushSubscription{}, store.PushDelivery{}, false, errors.Join(err, rerr)
	}
	// The status is unknown here, not inactive; IncludeText stays false.
	return cur, store.PushDelivery{Active: true}, held, nil
}

// orDefault is d, or def when d is zero (a Service literal leaves the tuning fields zero).
func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
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
	// ReleaseToken binds a later release to this registration. The Console keeps it beside the
	// device id and sends it back with the release.
	ReleaseToken string `json:"releaseToken"`
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
	var tok [16]byte
	if _, err := rand.Read(tok[:]); err != nil {
		internalErr(w, err)
		return
	}
	fresh := base64.RawURLEncoding.EncodeToString(tok[:])
	added, token, err := p.store.AddPushSubscription(r.Context(), store.PushSubscription{MembershipID: membershipID,
		Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth, UserAgent: ua, ReleaseToken: fresh}, pushMaxPerMember)
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
	writeJSON(w, http.StatusCreated, createdWire{ID: store.PushSubscriptionID(in.Endpoint), ReleaseToken: token})
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

// PrefOrigins are the origins a member may turn off for push.
func PrefOrigins() []string {
	out := make([]string, 0, len(pushOrigins))
	for k := range pushOrigins {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

type prefsWire struct {
	Off        []string `json:"off"`
	OffOrigins []string `json:"offOrigins"`
}

// GetPrefs answers the kinds the caller turned off.
func (p *Service) GetPrefs(w http.ResponseWriter, r *http.Request, membershipID string) {
	if p.off(w) {
		return
	}
	off, offOrigins, err := p.store.GetPushPrefs(r.Context(), membershipID)
	if err != nil {
		internalErr(w, err)
		return
	}
	if off == nil {
		off = []string{}
	}
	if offOrigins == nil {
		offOrigins = []string{}
	}
	writeJSON(w, http.StatusOK, prefsWire{Off: off, OffOrigins: offOrigins})
}

// PutPrefs replaces the caller's turned-off kinds. An unknown kind is refused so a typo cannot
// look like a saved opt-out.
func (p *Service) PutPrefs(w http.ResponseWriter, r *http.Request, membershipID string) {
	if p.off(w) {
		return
	}
	// OffOrigins absent (null) leaves the saved origins alone, so a client that only knows kinds
	// cannot clear them by saving.
	var in struct {
		Off        []string  `json:"off"`
		OffOrigins *[]string `json:"offOrigins"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil || in.Off == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", `body must be {"off":[...],"offOrigins":[...]}`)
		return
	}
	var origins []string
	if in.OffOrigins != nil {
		seenO := map[string]bool{}
		for _, o := range *in.OffOrigins {
			if _, ok := pushOrigins[o]; !ok {
				writeErr(w, http.StatusBadRequest, "bad_origin", "unknown push origin: "+o)
				return
			}
			if !seenO[o] {
				seenO[o] = true
				origins = append(origins, o)
			}
		}
		slices.Sort(origins)
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
	var originsArg *[]string
	if in.OffOrigins != nil {
		originsArg = &origins
	}
	savedKinds, savedOrigins, err := p.store.PutPushPrefs(r.Context(), membershipID, off, originsArg)
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prefsWire{Off: append([]string{}, savedKinds...), OffOrigins: append([]string{}, savedOrigins...)})
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
		// Seq is optional: the device's own counter, rising with each report. See viewing.set.
		Seq int64 `json:"seq"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil || in.Device == "" || len(in.Device) > 100 || len(in.Session) > 200 || in.Seq < 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "device and session required")
		return
	}
	p.view.set(membershipID, in.Device, in.Session, in.Seq, time.Now())
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
		// ReleaseToken is the token the registration answered with. Present: the release only
		// applies to that registration. Absent (an older Console): keys alone, as before.
		ReleaseToken string `json:"releaseToken"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in); err != nil || in.Endpoint == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	release := func() error {
		if in.ReleaseToken != "" {
			return p.store.ReleasePushEndpointIfToken(r.Context(), in.Endpoint, in.Keys.P256dh, in.Keys.Auth, in.ReleaseToken)
		}
		return p.store.ReleasePushEndpoint(r.Context(), in.Endpoint, in.Keys.P256dh, in.Keys.Auth)
	}
	if err := release(); err != nil {
		internalErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
