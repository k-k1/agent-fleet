package main

// engine_remote.go — borrowing another Agent Fleet's engines (ADR 0079).
//
// A `remote` row is an `external` row with one difference: there is somebody on the other end who
// can START the engine. Everything in this file exists because of that one difference, and the
// three places it shows up are ADR 0079 decisions 5, 6 and 7.
//
// What the operator declares is WHERE and WITH WHAT; WHICH engines exist is read from the far
// deployment's own catalogue (decision 2). That is not a derivation dodged for convenience — an
// image role is `comfy` on one deployment and `sdcpp` on another, the Workspace composes a
// completely different request for each, and a guess produces a ComfyUI graph posted at an
// OpenAI-compatible endpoint.
//
// 🔴 The consequence is that rows appear on the first successful catalogue fetch rather than at
// boot, so this file is a POLL that adopts rows (engineRegistry.adopt) as well as a client. ADR
// 0077 P1 built the adopting half; its driver reads an SSM parameter and could not be reused.

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineRemotePollInterval is how often the far catalogue is re-read. The Agent's own catalogue
// cadence (ADR 0072), for the same reason: what changes here is an administrator's toggle on
// another deployment, which is minutes-scale, and each tick is one request to one URL.
const engineRemotePollInterval = 10 * time.Minute

// engineRemoteTokenRenewAhead is how long before expiry a far session token is replaced. The TTL
// the far side mints is 30 days (engine_token.go), so this is generous by three orders of
// magnitude and exists only so that a token never expires mid-request.
const engineRemoteTokenRenewAhead = 24 * time.Hour

// engineRemotes is the one far deployment this CP borrows from, and the per-role handles hanging
// off it. nil — the normal case — means nothing is borrowed, and every method here tolerates that
// so the call sites in engines.go do not each need a guard.
type engineRemotes struct {
	// base is the far fleet's URL with no path: https://af.example.com. The path under it is the
	// far side's to declare, and it states it in the token answer's `base_url` (decision 4).
	base string
	// token is the afei_… issuing token of a membership on the far deployment that is used for
	// nothing else (decision 3). All it can buy is a per-session engine token.
	token string
	// keys is AF_REMOTE_ENGINE_KEYS: which roles to borrow. Empty borrows every role the far
	// fleet offers, which is the answer for an operator who just wants what is there.
	keys []string

	mu    sync.Mutex
	byKey map[string]*engineRemote
}

// engineRemote is one borrowed role: its mirrored catalogue, the far side's own base path for it,
// and the session tokens minted against it.
type engineRemote struct {
	parent *engineRemotes
	key    string

	mu sync.Mutex
	// rows is the mirror — the far catalogue's models as this deployment's own catalogue shape.
	// Kept rather than re-fetched per read for the same reason engineCatalog caches: the gateway
	// consults the catalogue on every request.
	rows   []store.EngineModel
	loaded bool
	// warmModel is the id the FAR deployment last saw its engine answer with, as its catalogue
	// reports it. It is the only honest answer to "is this warm" available here: probing would be
	// the health call decision 5 refuses (ADR 0079 decision 10).
	warmModel string
	// basePath is the far side's own `/engine/<key>/v1` — READ from its token answer, never
	// composed here, because composing it would be this deployment asserting the far side's route
	// layout (decision 4).
	basePath string
	// negAlways is what the FAR administrator excludes from every image on this engine. It is a
	// deployment-wide setting over there rather than a model row, so it does not go in the mirror
	// — but it still has to reach the Workspace, and the local setting this would otherwise read is
	// one nobody over there can see. Writing it locally is refused for the same reason
	// (engine_admin.go's refuseBorrowedWrite).
	negAlways string
	// tokens are the far session tokens, per LOCAL session name (decision 8). The far usage rows
	// then carry the borrower's session, which is the only way an operator there can tell one
	// borrower's spending from another's.
	tokens map[string]engineRemoteToken
}

// engineRemoteToken is one minted far session token and when it stops being usable.
type engineRemoteToken struct {
	token   string
	expires time.Time
}

// newEngineRemotes reads the operator's declaration. nil when nothing is borrowed, which is what
// keeps every deployment that does not use this feature on exactly the path it had before.
//
// Read once, at startup, like AF_COMFY_URL: an environment variable cannot change under a running
// process, so pointing at a different fleet is a Control Plane restart.
func newEngineRemotes(mgr *manager) *engineRemotes {
	base := strings.TrimRight(strings.TrimSpace(envx.Or("AF_REMOTE_ENGINE_URL", "")), "/")
	token := strings.TrimSpace(envx.Or("AF_REMOTE_ENGINE_TOKEN", ""))
	if base == "" || token == "" {
		// Both or neither. A URL with no credential could only ever be refused 401, and saying so
		// at boot is better than a launch menu that is empty for a reason nothing names.
		if base != "" || token != "" {
			logRemoteMisconfigured(base, token)
		}
		return nil
	}
	var keys []string
	for _, k := range strings.Split(envx.Or("AF_REMOTE_ENGINE_KEYS", ""), ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	return &engineRemotes{base: base, token: token, keys: keys, byKey: map[string]*engineRemote{}}
}

// wants reports whether this role is one the operator asked to borrow.
func (r *engineRemotes) wants(key string) bool {
	if r == nil {
		return false
	}
	if len(r.keys) == 0 {
		return true
	}
	for _, k := range r.keys {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// forKey returns the handle for one borrowed role, creating it on first use. The handle outlives
// any single catalogue fetch: the row built from it holds it, and the poll refreshes it in place.
func (r *engineRemotes) forKey(key string) *engineRemote {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.byKey[key]; e != nil {
		return e
	}
	e := &engineRemote{parent: r, key: key, tokens: map[string]engineRemoteToken{}}
	r.byKey[key] = e
	return e
}

// catalogSource is what engineCatalog reads this role's rows through instead of the database
// (ADR 0079 decision 7).
//
// 🔴 It answers an EMPTY LIST and never a nil source, before the first fetch as much as after a
// failed one. engineCatalog.hasModels answers true when it has no source at all — deliberately,
// since the false direction stops a GPU — so a row whose mirror were merely absent would pass
// serve's no-models gate and then 404 every named model.
// It is also never nil ITSELF, for the same reason one level up: a nil handle here would leave the
// row reading the local database — or nothing — and `hasModels` would answer true for a catalogue
// that holds nothing. newEngineRegistry refuses such a row outright; this is the second lock, so
// that a future call site cannot reintroduce the failure by forgetting.
func (e *engineRemote) catalogSource() func(context.Context) ([]store.EngineModel, error) {
	if e == nil {
		return func(context.Context) ([]store.EngineModel, error) { return []store.EngineModel{}, nil }
	}
	return func(context.Context) ([]store.EngineModel, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.rows == nil {
			return []store.EngineModel{}, nil
		}
		return e.rows, nil
	}
}

// warm is what the far deployment last observed about its own engine (decision 10).
func (e *engineRemote) warm() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.warmModel != ""
}

// upstreamBase is the far gateway's own prefix for this role, as it stated it. Empty until the
// first token exchange has happened, and an empty one is what dial refuses to build a URL from
// rather than guessing `/engine/<key>/v1`.
func (e *engineRemote) upstreamBase() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.basePath
}

// logRemoteMisconfigured says which half of the declaration is missing. Both are needed and
// neither has a usable default, so a partial declaration is an operator error worth naming at boot
// rather than a 401 nobody connects to the cause.
func logRemoteMisconfigured(base, token string) {
	missing := "AF_REMOTE_ENGINE_TOKEN"
	if base == "" {
		missing = "AF_REMOTE_ENGINE_URL"
	}
	log.Printf("engines: borrowing is declared but %s is unset - no engine is borrowed (both are required; ADR 0079 decision 2)", missing)
}

// run starts the poll that both discovers borrowed rows and refreshes their mirrors. Nil-safe, so
// newEngineRegistry calls it unconditionally.
//
// The FIRST tick happens at once rather than after the interval: a CP that has just started should
// offer the borrowed models as soon as the far fleet can be reached, and ten minutes of an empty
// launch menu reads as a broken deployment.
func (r *engineRemotes) run(ctx context.Context, reg *engineRegistry) {
	if r == nil || reg == nil {
		return
	}
	go func() {
		t := time.NewTicker(engineRemotePollInterval)
		defer t.Stop()
		r.refreshAll(ctx, reg)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.refreshAll(ctx, reg)
			}
		}
	}()
}

// refreshAll reads the far catalogue once and brings the registry up to date with it: rows that
// are not served yet are adopted, and rows that are get a fresh mirror.
//
// A failed fetch keeps every previous answer. That is load-bearing rather than tidy: an empty
// catalogue means "this engine has nothing to serve", so a transient error would take a borrowed
// engine out of the launch menu and answer 503 to whoever was using it.
func (r *engineRemotes) refreshAll(ctx context.Context, reg *engineRegistry) {
	if r == nil || reg == nil {
		return
	}
	rows, err := r.fetchCatalog(ctx)
	if err != nil {
		log.Printf("engines: reading the borrowed catalogue from %s failed: %v", r.base, err)
		return
	}
	for _, row := range rows {
		if !r.wants(row.Key) {
			continue
		}
		r.forKey(row.Key).setNegativeAlways(row.NegativeAlways)
		r.applyCatalogRow(ctx, reg, row)
	}
}

// setNegativeAlways records the far administrator's exclusion list for this role.
func (e *engineRemote) setNegativeAlways(v string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.negAlways = strings.TrimSpace(v)
	e.mu.Unlock()
}

// negativeAlways is that list, or "" when the far side declares none.
func (e *engineRemote) negativeAlways() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.negAlways
}
