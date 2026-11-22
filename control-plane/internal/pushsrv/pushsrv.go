// Package pushsrv is the Control Plane's Web Push service (#4750, part 1: sender and
// subscriptions); the HTTP routes are mounted by package main.
package pushsrv

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
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
	// pushMaxInFlight caps concurrent sends CP-wide, and pushMaxPerMember the devices one member
	// may register, so a burst of notifications cannot fan out without bound.
	pushMaxInFlight  = 32
	pushMaxPerMember = 20
	// A question that waits an hour is stale for a phone; the service drops it after that.
	pushTTL = time.Hour
)

type Service struct {
	store  store.PushSubscriptionStore
	sender *webpush.Sender
	pubKey string
	// check vets an endpoint against the allowed push services; a field so tests can point the
	// service at a local TLS server.
	check   func(string) error
	timeout time.Duration
	slots   chan struct{}
}

// validSubject accepts mailto:<addr with @> or an https URL with a host: services that check the
// contact reject anything else, and only at the first push.
func validSubject(s string) bool {
	if rest, ok := strings.CutPrefix(s, "mailto:"); ok {
		user, host, found := strings.Cut(rest, "@")
		return found && user != "" && host != ""
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
		check: func(e string) error { return webpush.CheckEndpoint(e, hosts) }, timeout: pushSendTimeout, slots: make(chan struct{}, pushMaxInFlight)}
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
}

// onNotification runs on the inserting caller, so it only starts the delivery.
func (p *Service) onNotification(n store.Notification) {
	if !pushKinds[n.Kind] || n.TargetType != "session" || n.TargetID == "" {
		return
	}
	go func() {
		// Each device gets its own deadline in deliver; this one only bounds the list read and
		// the wait for a free slot.
		ctx, cancel := context.WithTimeout(context.Background(), 2*p.timeout)
		defer cancel()
		p.deliver(ctx, n)
	}()
}

func (p *Service) deliver(ctx context.Context, n store.Notification) {
	subs, err := p.store.ListPushSubscriptions(ctx, n.MembershipID)
	if err != nil || len(subs) == 0 {
		if err != nil {
			log.Printf("web push: list subscriptions: %v", err)
		}
		return
	}
	body, _ := json.Marshal(pushPayload{V: 1, Kind: n.Kind, Session: n.TargetID, Name: n.DisplayName})
	// One goroutine per device with its own deadline: a device whose push service hangs must not
	// eat the budget of the ones behind it. p.slots bounds the sends in flight CP-wide.
	var wg sync.WaitGroup
	for _, s := range subs {
		// An endpoint the allowlist refuses is skipped, not deleted: a missing AF_WEB_PUSH_ENDPOINT_HOSTS
		// entry after a restart must not cost the member their subscriptions.
		if p.check(s.Endpoint) != nil {
			continue
		}
		wg.Add(1)
		go func(s store.PushSubscription) {
			defer wg.Done()
			select {
			case p.slots <- struct{}{}:
				defer func() { <-p.slots }()
			case <-ctx.Done():
				return
			}
			sctx, cancel := context.WithTimeout(ctx, p.timeout)
			defer cancel()
			p.send(sctx, n.MembershipID, s, body)
		}(s)
	}
	wg.Wait()
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
	// Refuse keys Encrypt cannot use now rather than failing silently on the first push.
	if _, err := webpush.Encrypt(webpush.Subscription{Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth}, []byte("x")); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_keys", "invalid subscription keys")
		return
	}
	if rows, err := p.store.ListPushSubscriptions(r.Context(), membershipID); err == nil && len(rows) >= pushMaxPerMember {
		held := false
		for _, s := range rows {
			held = held || s.Endpoint == in.Endpoint
		}
		if !held {
			writeErr(w, http.StatusConflict, "too_many_devices", "too many registered devices; remove one first")
			return
		}
	}
	ua := r.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	if err := p.store.UpsertPushSubscription(r.Context(), store.PushSubscription{MembershipID: membershipID,
		Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth, UserAgent: ua}); err != nil {
		internalErr(w, err)
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
