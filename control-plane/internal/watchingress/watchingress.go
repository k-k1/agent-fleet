// Package watchingress is the Control Plane side of the watch engine's webhook ingress.
package watchingress

// Watch webhook ingress — ADR 0133 §4, step 4 (issue #3630).
//
//	GitHub / Bitbucket ──▶ POST /webhooks/github          (the deployment's GitHub App)
//	                       POST /webhooks/hooks/{id}      (a tenant's hook, own secret)
//	                          │ verify signature, then parse: subjects only
//	                          ▼
//	               watch_subject (who asked) ──▶ running workspace's Agent  POST /watch/poke
//	member's Agent ──(AF_WATCH_TOKEN)──▶ PUT /internal/watch-subjects, /internal/watch-hooks
//
// A delivery is never forwarded. The Agent receives (source, subject) and polls with the
// member's own token, so the data a session sees is what that member can read. The Control
// Plane holds no member credential and no payload.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
	"github.com/k-k1/agent-fleet/control-plane/internal/webhookpoke"
)

// Bounds. A workspace holds at most the engine's own per-workspace limit of watches (ADR 0133
// rule 6.2), so the registration cap is the same number.
const (
	MaxSubjects       = 200
	MaxHooks          = 20
	SubjectTTL        = time.Hour
	bridgeBodyMax     = 64 << 10
	PokeMinGap        = 5 * time.Second // per (workspace, subject): a replay costs one poll
	pokeConcurrency   = 16
	pokeTimeout       = 5 * time.Second
	targetsPerSubject = 50
)

func SignKey(master32 []byte) []byte {
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-watch-token-sign/v1"))
	return mac.Sum(nil)
}

// MintToken returns the deterministic bridge token for a membership: "afw_" +
// b64url(membershipID) + "." + tag. Its own credential: a leak registers subjects and creates
// hooks for that member and nothing else.
func MintToken(signKey []byte, membershipID string) string {
	return "afw_" + base64.RawURLEncoding.EncodeToString([]byte(membershipID)) + "." + tokenTag(signKey, membershipID)
}

func tokenTag(signKey []byte, membershipID string) string {
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(membershipID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

func VerifyToken(signKey []byte, token string) (string, bool) {
	body, ok := strings.CutPrefix(strings.TrimSpace(token), "afw_")
	if !ok {
		return "", false
	}
	dot := strings.LastIndexByte(body, '.')
	if dot < 0 {
		return "", false
	}
	idRaw, err := base64.RawURLEncoding.DecodeString(body[:dot])
	if err != nil || len(idRaw) == 0 {
		return "", false
	}
	mid := string(idRaw)
	if !hmac.Equal([]byte(body[dot+1:]), []byte(tokenTag(signKey, mid))) {
		return "", false
	}
	return mid, true
}

// HookSecret derives the secret of a hook from the signing root and the hook's id, so no
// secret is stored. It is deliberately not under a bridge epoch: an epoch advance would silently
// break every hook already configured at the provider. Deleting the hook retires its secret.
func HookSecret(root []byte, hookID string) []byte {
	k := hmac.New(sha256.New, root)
	k.Write([]byte("af-watch-hook-secret/v1"))
	mac := hmac.New(sha256.New, k.Sum(nil))
	mac.Write([]byte(hookID))
	out := make([]byte, hex.EncodedLen(sha256.Size))
	hex.Encode(out, mac.Sum(nil))
	return out
}

// tokenBucket is a small mutex-guarded rate limiter.
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(rate, burst float64) *tokenBucket {
	return &tokenBucket{rate: rate, burst: burst, tokens: burst}
}

func (b *tokenBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.last.IsZero() {
		b.tokens += now.Sub(b.last).Seconds() * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Store is what the ingress reads and writes.
type Store interface {
	store.WatchIngressStore
	GetWorkspaceByMembership(ctx context.Context, membershipID string) (store.Workspace, bool, error)
	GetMembershipByID(ctx context.Context, membershipID string) (store.MembershipView, bool, error)
}

// Deps is what the ingress needs from the Control Plane.
type Deps interface {
	Store() Store
	// VerifyBridge checks a bridge token under its membership's current epoch.
	VerifyBridge(ctx context.Context, signKey []byte, token string, verify func(signKey []byte, token string) (string, bool)) (string, bool)
	// SigningRoot is the root the token and hook-secret keys derive from.
	SigningRoot() []byte
	PublicBaseURL() string
	// AgentEndpoint is the workspace Agent's base URL and bearer token.
	AgentEndpoint(ws store.Workspace) (endpoint, token string)
	// AgentClient sends the poke.
	AgentClient() *http.Client
}

// Ingress is the state of the ingress.
type Ingress struct {
	d         Deps
	appSecret []byte
	// Now is replaceable by tests.
	Now func() time.Time

	// pre limits every request before any lookup or signature work, so an anonymous flood
	// costs the process a counter and not a database read.
	pre *tokenBucket

	mu     sync.Mutex
	perKey map[string]*tokenBucket // verified deliveries, per hook id or "app"
	pokeAt map[string]time.Time    // "membership|subject" -> last poke
	slots  chan struct{}

	// Poke is replaceable by tests.
	Poke func(ctx context.Context, ws store.Workspace, source, subject string) error
}

// New builds the ingress. An empty appSecret leaves /webhooks/github off.
func New(d Deps, appSecret string) *Ingress {
	wi := &Ingress{
		d: d, appSecret: []byte(appSecret), Now: time.Now,
		pre:    newTokenBucket(50, 100),
		perKey: map[string]*tokenBucket{}, pokeAt: map[string]time.Time{},
		slots: make(chan struct{}, pokeConcurrency),
	}
	wi.Poke = wi.pokeAgent
	return wi
}

// Register adds the ingress routes to mux. The caller exempts "/webhooks/" from the session gate.
func (wi *Ingress) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/github", wi.handleApp)
	mux.HandleFunc("POST /webhooks/hooks/{id}", wi.handleHook)
	mux.HandleFunc("PUT /internal/watch-subjects", wi.withToken(wi.putSubjects))
	mux.HandleFunc("GET /internal/watch-hooks", wi.withToken(wi.listHooks))
	mux.HandleFunc("POST /internal/watch-hooks", wi.withToken(wi.createHook))
	mux.HandleFunc("DELETE /internal/watch-hooks/{id}", wi.withToken(wi.deleteHook))
}

// --- public endpoints ----------------------------------------------------------------

func (wi *Ingress) handleApp(w http.ResponseWriter, r *http.Request) {
	if len(wi.appSecret) == 0 {
		http.NotFound(w, r)
		return
	}
	wi.deliver(w, r, "github", "app", wi.appSecret, "", "")
}

func (wi *Ingress) handleHook(w http.ResponseWriter, r *http.Request) {
	if !wi.pre.allow(wi.Now()) {
		tooMany(w)
		return
	}
	id := r.PathValue("id")
	h, ok, err := wi.d.Store().GetWatchHook(r.Context(), id)
	if err != nil {
		log.Printf("watch ingress: hook lookup: %v", err)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	// An unknown hook answers like a bad signature, so the status does not tell a guesser which
	// ids exist. A hook whose owner left the tenant is as good as unknown.
	if ok {
		if _, active, err := wi.d.Store().GetMembershipByID(r.Context(), h.MembershipID); err != nil || !active {
			ok = false
		}
	}
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	wi.deliver(w, r, h.Source, h.ID, HookSecret(wi.d.SigningRoot(), h.ID), h.TenantID, h.Repo)
}

func tooMany(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	http.Error(w, "slow down", http.StatusTooManyRequests)
}

// deliver is the one path of a delivery: size limit, signature, then parse, then forward.
// tenantID "" is the deployment's App, whose pokes may only reach public repositories.
func (wi *Ingress) deliver(w http.ResponseWriter, r *http.Request, source, key string, secret []byte, tenantID, hookRepo string) {
	if key == "app" && !wi.pre.allow(wi.Now()) {
		tooMany(w)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, webhookpoke.MaxBody))
	if err != nil {
		var big *http.MaxBytesError
		if errors.As(err, &big) {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// The signature is checked before the body is parsed, or even looked at.
	sigHeader := "X-Hub-Signature-256"
	if source == "bitbucket" {
		sigHeader = "X-Hub-Signature"
	}
	if webhookpoke.VerifyHMAC256(r.Header.Get(sigHeader), body, secret) != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !wi.allowVerified(key) {
		tooMany(w)
		return
	}
	var parsed webhookpoke.Parsed
	switch source {
	case "github":
		parsed, err = webhookpoke.ParseGitHub(r.Header.Get("X-GitHub-Event"), body)
	case "bitbucket":
		parsed, err = webhookpoke.ParseBitbucket(r.Header.Get("X-Event-Key"), body)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// The deployment's App serves every tenant, so what it forwards must already be public.
	// A private repository's activity reaches only a tenant's own hook.
	if tenantID == "" && parsed.Private {
		parsed.Subjects = nil
	}
	// A hook made for one repository speaks only for it: whoever holds the secret cannot name
	// another repository's subjects.
	if hookRepo != "" && !strings.EqualFold(parsed.Repo, hookRepo) {
		parsed.Subjects = nil
	}
	wi.forward(source, parsed.Subjects, tenantID)
	w.WriteHeader(http.StatusAccepted)
}

func (wi *Ingress) allowVerified(key string) bool {
	wi.mu.Lock()
	b, ok := wi.perKey[key]
	if !ok {
		if len(wi.perKey) > 10000 {
			wi.perKey = map[string]*tokenBucket{} // hook ids are bounded by rows, but never grow without limit
		}
		b = newTokenBucket(10, 30)
		wi.perKey[key] = b
	}
	wi.mu.Unlock()
	return b.allow(wi.Now())
}

// forward pokes the running workspaces that registered each subject. It returns at once: the
// pokes run in the background so a slow Agent cannot hold the provider's delivery open.
func (wi *Ingress) forward(source string, subjects []string, tenantID string) {
	if len(subjects) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	now := wi.Now()
	var jobs []func()
	for _, sub := range subjects {
		targets, err := wi.d.Store().WatchTargets(ctx, sub, tenantID, store.NowTS(), targetsPerSubject)
		if err != nil {
			log.Printf("watch ingress: targets: %v", err)
			continue
		}
		for _, t := range targets {
			if !wi.pokeDue(t.MembershipID+"|"+sub, now) {
				continue
			}
			sub, t := sub, t
			jobs = append(jobs, func() {
				ws, ok, err := wi.d.Store().GetWorkspaceByMembership(ctx, t.MembershipID)
				if err != nil || !ok || ws.ID != t.WorkspaceID || ws.State != "running" {
					return // stopped between the read and now: dropped, never woken
				}
				if err := wi.Poke(ctx, ws, source, sub); err != nil {
					log.Printf("watch ingress: poke ws %s: %v", ws.ID, err)
				}
			})
		}
	}
	go func() {
		defer cancel()
		var wg sync.WaitGroup
		for _, j := range jobs {
			select {
			case wi.slots <- struct{}{}:
			default:
				continue // saturated: the engine's own long poll covers a dropped poke
			}
			wg.Add(1)
			go func(j func()) {
				defer wg.Done()
				defer func() { <-wi.slots }()
				j()
			}(j)
		}
		wg.Wait()
	}()
}

// pokeDue admits one poke per (workspace, subject) per PokeMinGap.
func (wi *Ingress) pokeDue(key string, now time.Time) bool {
	wi.mu.Lock()
	defer wi.mu.Unlock()
	if at, ok := wi.pokeAt[key]; ok && now.Sub(at) < PokeMinGap {
		return false
	}
	if len(wi.pokeAt) > 10000 {
		for k, at := range wi.pokeAt {
			if now.Sub(at) >= PokeMinGap {
				delete(wi.pokeAt, k)
			}
		}
	}
	wi.pokeAt[key] = now
	return true
}

// pokeAgent sends (source, subject) to the Agent. Nothing else is in the body.
func (wi *Ingress) pokeAgent(ctx context.Context, ws store.Workspace, source, subject string) error {
	endpoint, token := wi.d.AgentEndpoint(ws)
	if endpoint == "" {
		return errors.New("no endpoint")
	}
	body, _ := json.Marshal(pokeBody{Source: source, Subject: subject})
	ctx, cancel := context.WithTimeout(ctx, pokeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/watch/poke", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := wi.d.AgentClient().Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		return errors.New("agent answered " + resp.Status)
	}
	return nil
}

// --- Agent-facing bridge ---------------------------------------------------------------

func (wi *Ingress) withToken(h func(http.ResponseWriter, *http.Request, store.MembershipView)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mid, ok := wi.d.VerifyBridge(r.Context(), SignKey(wi.d.SigningRoot()), tok, VerifyToken)
		if !ok {
			writeErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "invalid watch token"})
			return
		}
		mv, ok, err := wi.d.Store().GetMembershipByID(r.Context(), mid)
		if err != nil {
			writeErr(w, internalErr(err))
			return
		}
		if !ok {
			writeErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "membership not active"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, bridgeBodyMax)
		h(w, r, mv)
	}
}

// putSubjects (PUT /internal/watch-subjects) replaces the caller's whole set. The Agent sends
// the set again before the TTL runs out, so a registration it stopped renewing disappears.
func (wi *Ingress) putSubjects(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	var in struct {
		Subjects []string `json:"subjects"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || in.Subjects == nil {
		writeErr(w, &apiError{http.StatusBadRequest, "invalid", `body must be {"subjects":[...]}`})
		return
	}
	seen := map[string]bool{}
	var rows []store.WatchSubjectRow
	for _, s := range in.Subjects {
		if !webhookpoke.ValidSubject(s) {
			writeErr(w, &apiError{http.StatusBadRequest, "invalid_subject", "a subject is not a github or bitbucket pull request reference or a github workflow run key"})
			return
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		src, _, _ := strings.Cut(s, ":")
		rows = append(rows, store.WatchSubjectRow{Source: src, Subject: s})
	}
	now := wi.Now().UTC()
	exp := now.Add(SubjectTTL).Format(time.RFC3339)
	err := wi.d.Store().ReplaceWatchSubjects(r.Context(), mv.MembershipID, mv.TenantID, rows, exp, MaxSubjects)
	if errors.Is(err, store.ErrWatchLimit) {
		writeErr(w, &apiError{http.StatusBadRequest, "too_many", "at most 200 subjects per workspace"})
		return
	}
	if err != nil {
		writeErr(w, internalErr(err))
		return
	}
	_ = wi.d.Store().SweepWatchSubjects(r.Context(), now.Format(time.RFC3339))
	writeJSON(w, http.StatusOK, registeredResp{len(rows), int(SubjectTTL / time.Second)})
}

type hookWire struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Repo      string `json:"repo,omitempty"`
	URL       string `json:"url"`
	CreatedAt string `json:"created_at"`
	// Secret is returned by create only.
	Secret string `json:"secret,omitempty"`
}

func (wi *Ingress) wire(h store.WatchHook) hookWire {
	return hookWire{ID: h.ID, Source: h.Source, Repo: h.Repo, CreatedAt: h.CreatedAt,
		URL: strings.TrimRight(wi.d.PublicBaseURL(), "/") + "/webhooks/hooks/" + h.ID}
}

func (wi *Ingress) listHooks(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	hs, err := wi.d.Store().ListWatchHooks(r.Context(), mv.MembershipID)
	if err != nil {
		writeErr(w, internalErr(err))
		return
	}
	out := []hookWire{}
	for _, h := range hs {
		out = append(out, wi.wire(h))
	}
	writeJSON(w, http.StatusOK, hooksResp{out})
}

// createHook (POST /internal/watch-hooks) makes an endpoint owned by the caller's tenant and
// returns its URL and secret once. The caller (the member, with their own provider token)
// installs it at the provider: this Control Plane never holds that token.
func (wi *Ingress) createHook(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	var in struct {
		Source string `json:"source"`
		Repo   string `json:"repo"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || (in.Source != "github" && in.Source != "bitbucket") {
		writeErr(w, &apiError{http.StatusBadRequest, "invalid", `body must be {"source":"github"|"bitbucket","repo":"owner/name"}`})
		return
	}
	if in.Repo != "" && !regexp.MustCompile(`^[A-Za-z0-9._~-]{1,100}/[A-Za-z0-9._~-]{1,100}$`).MatchString(in.Repo) {
		writeErr(w, &apiError{http.StatusBadRequest, "invalid", "repo must be owner/name"})
		return
	}
	if wi.d.PublicBaseURL() == "" {
		writeErr(w, &apiError{http.StatusConflict, "no_public_url", "this deployment has no public URL, so a provider could not reach a hook"})
		return
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		writeErr(w, internalErr(err))
		return
	}
	h := store.WatchHook{ID: hex.EncodeToString(raw), TenantID: mv.TenantID, MembershipID: mv.MembershipID,
		Source: in.Source, Repo: strings.ToLower(in.Repo), CreatedAt: store.NowTS()}
	switch err := wi.d.Store().CreateWatchHook(r.Context(), h, MaxHooks); {
	case errors.Is(err, store.ErrWatchLimit):
		writeErr(w, &apiError{http.StatusConflict, "too_many", "at most 20 hooks per member; delete one first"})
		return
	case err != nil:
		writeErr(w, internalErr(err))
		return
	}
	out := wi.wire(h)
	out.Secret = string(HookSecret(wi.d.SigningRoot(), h.ID))
	writeJSON(w, http.StatusCreated, out)
}

func (wi *Ingress) deleteHook(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	ok, err := wi.d.Store().DeleteWatchHook(r.Context(), mv.MembershipID, r.PathValue("id"))
	if err != nil {
		writeErr(w, internalErr(err))
		return
	}
	if !ok {
		writeErr(w, &apiError{http.StatusNotFound, "not_found", "no such hook"})
		return
	}
	writeJSON(w, http.StatusOK, deletedResp{r.PathValue("id")})
}

type apiError struct {
	status  int
	code    string
	message string
}

// errBody is the error envelope every Control Plane API answers with.
type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeErr(w http.ResponseWriter, e *apiError) {
	var b errBody
	b.Error.Code, b.Error.Message = e.code, e.message
	writeJSON(w, e.status, b)
}

// internalErr logs the cause and gives the caller a fixed message.
func internalErr(err error) *apiError {
	log.Printf("watch ingress: %v", err)
	return &apiError{http.StatusInternalServerError, "internal", "internal error"}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type registeredResp struct {
	Registered int `json:"registered"`
	TTLSeconds int `json:"ttl_seconds"`
}

type hooksResp struct {
	Hooks []hookWire `json:"hooks"`
}

type deletedResp struct {
	Deleted string `json:"deleted"`
}

// pokeBody is POST /watch/poke's request: a source and a subject, nothing else.
type pokeBody struct {
	Source  string `json:"source"`
	Subject string `json:"subject"`
}
