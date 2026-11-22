package engine

// remote.go — borrowing another Agent Fleet's engines (ADR 0079).
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
// boot, so this file is a POLL that adopts rows (RemoteHost.Adopt) as well as a client. ADR
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

// remotePollInterval is how often the far catalogue is re-read.
//
// Two minutes rather than the Agent's ten. The cadences were the same because the reasoning was
// "an administrator's toggle is minutes-scale", but they are not the same KIND of wait: the
// Agent's TTL is a cache a push already cuts short (decision 7's `catalog-changed`), while this
// poll is the only way a borrowing deployment ever hears about a change at all — the far side
// cannot call us. Measured 2026-09-15 on the dev deployment: a checkpoint enabled over there took
// about three minutes to reach a session here, and that was a lucky draw from a span whose worst
// case was this interval plus the Agent's TTL. Each tick is still one GET of one small JSON to
// one URL, so the cost of the change is 720 requests a day against a wait nobody can explain.
const remotePollInterval = 2 * time.Minute

// remoteTokenRenewAhead is how long before expiry a far session token is replaced. The TTL
// the far side mints is 30 days (internal/engine/token.go), so this is generous by three orders of
// magnitude and exists only so that a token never expires mid-request.
const remoteTokenRenewAhead = 24 * time.Hour

// Remotes is the one far deployment this CP borrows from, and the per-role handles hanging
// off it. nil — the normal case — means nothing is borrowed, and every method here tolerates that
// so the call sites in engines.go do not each need a guard.
type Remotes struct {
	// base is the far fleet's URL with no path: https://af.example.com. The path under it is the
	// far side's to declare, and it states it in the token answer's `base_url` (decision 4).
	base string
	// token is the afei_… issuing token of a membership on the far deployment that is used for
	// nothing else (decision 3). All it can buy is a per-session engine token.
	token string
	// keys is AF_REMOTE_ENGINE_KEYS: which roles to borrow. Empty borrows every role the far
	// fleet offers, which is the answer for an operator who just wants what is there.
	keys []string
	// client carries both far-side calls; main hands in the gateway's own upstream client.
	client HTTPDoer

	mu    sync.Mutex
	byKey map[string]*Remote

	// polling counts the goroutine run starts, so a test can wait for it to be gone before the
	// next test swaps the log output.
	polling sync.WaitGroup
}

// Remote is one borrowed role: its mirrored catalogue, the far side's own base path for it,
// and the session tokens minted against it.
type Remote struct {
	parent *Remotes
	key    string

	mu sync.Mutex
	// rows is the mirror — the far catalogue's models as this deployment's own catalogue shape.
	// Kept rather than re-fetched per read for the same reason engineCatalog caches: the gateway
	// consults the catalogue on every request.
	rows   []store.EngineModel
	loaded bool
	// sig is the fingerprint of that mirror, so the poll can tell "the far side answered again"
	// from "the far side answers something else now" — only the second is worth waking every
	// running workspace for (MirrorSignature).
	sig string
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
	tokens map[string]remoteToken
}

// remoteToken is one minted far session token and when it stops being usable.
type remoteToken struct {
	token   string
	expires time.Time
}

// NewRemotes reads the operator's declaration. nil when nothing is borrowed, which is what
// keeps every deployment that does not use this feature on exactly the path it had before.
//
// Read once, at startup, like AF_COMFY_URL: an environment variable cannot change under a running
// process, so pointing at a different fleet is a Control Plane restart.
func NewRemotes(client HTTPDoer) *Remotes {
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
	return NewRemotesFor(base, token, keys, client)
}

// NewRemotesFor builds the borrowing config from an already-read declaration.
func NewRemotesFor(base, token string, keys []string, client HTTPDoer) *Remotes {
	return &Remotes{base: base, token: token, keys: keys, client: client, byKey: map[string]*Remote{}}
}

// NewRemote is a handle with no parent deployment: a mirror that can be read and a token cache
// that can never be filled.
func NewRemote(key string) *Remote {
	return &Remote{key: key, tokens: map[string]remoteToken{}}
}

// Base is the far fleet's URL, with no path and no trailing slash.
func (r *Remotes) Base() string {
	if r == nil {
		return ""
	}
	return r.base
}

// WaitPolling blocks until every poll goroutine Run started has returned.
func (r *Remotes) WaitPolling() {
	if r != nil {
		r.polling.Wait()
	}
}

// Parent is the deployment this handle borrows from; nil for a handle built by NewRemote.
func (e *Remote) Parent() *Remotes {
	if e == nil {
		return nil
	}
	return e.parent
}

// SeedMirror installs rows as the loaded mirror without a fetch.
func (e *Remote) SeedMirror(rows []store.EngineModel) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.rows, e.loaded = rows, true
	e.mu.Unlock()
}

// Wants reports whether this role is one the operator asked to borrow.
func (r *Remotes) Wants(key string) bool {
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

// ForKey returns the handle for one borrowed role, creating it on first use. The handle outlives
// any single catalogue fetch: the row built from it holds it, and the poll refreshes it in place.
func (r *Remotes) ForKey(key string) *Remote {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.byKey[key]; e != nil {
		return e
	}
	e := &Remote{parent: r, key: key, tokens: map[string]remoteToken{}}
	r.byKey[key] = e
	return e
}

// CatalogSource is what engineCatalog reads this role's rows through instead of the database
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
func (e *Remote) CatalogSource() func(context.Context) ([]store.EngineModel, error) {
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

// Warm is what the far deployment last observed about its own engine (decision 10).
func (e *Remote) Warm() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.warmModel != ""
}

// UpstreamBase is the far gateway's own prefix for this role, as it stated it. Empty until the
// first token exchange has happened, and an empty one is what dial refuses to build a URL from
// rather than guessing `/engine/<key>/v1`.
func (e *Remote) UpstreamBase() string {
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

// Run starts the poll that both discovers borrowed rows and refreshes their mirrors. Nil-safe, so
// the registry builder calls it unconditionally.
//
// The FIRST tick happens at once rather than after the interval: a CP that has just started should
// offer the borrowed models as soon as the far fleet can be reached, and ten minutes of an empty
// launch menu reads as a broken deployment.
func (r *Remotes) Run(ctx context.Context, host RemoteHost) {
	if r == nil || host == nil {
		return
	}
	r.polling.Add(1)
	go func() {
		defer r.polling.Done()
		t := time.NewTicker(remotePollInterval)
		defer t.Stop()
		r.RefreshAll(ctx, host)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.RefreshAll(ctx, host)
			}
		}
	}()
}

// RefreshAll reads the far catalogue once and brings the registry up to date with it: rows that
// are not served yet are adopted, and rows that are get a fresh mirror.
//
// A failed fetch keeps every previous answer. That is load-bearing rather than tidy: an empty
// catalogue means "this engine has nothing to serve", so a transient error would take a borrowed
// engine out of the launch menu and answer 503 to whoever was using it.
func (r *Remotes) RefreshAll(ctx context.Context, host RemoteHost) {
	if r == nil || host == nil {
		return
	}
	rows, err := r.fetchCatalog(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down is not a failed fetch
		}
		log.Printf("engines: reading the borrowed catalogue from %s failed: %v", r.base, err)
		return
	}
	for _, row := range rows {
		if !r.Wants(row.Key) {
			continue
		}
		r.ForKey(row.Key).SetNegativeAlways(row.NegativeAlways)
		r.applyCatalogRow(ctx, host, row)
	}
}

// SetNegativeAlways records the far administrator's exclusion list for this role.
func (e *Remote) SetNegativeAlways(v string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.negAlways = strings.TrimSpace(v)
	e.mu.Unlock()
}

// NegativeAlways is that list, or "" when the far side declares none.
func (e *Remote) NegativeAlways() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.negAlways
}
