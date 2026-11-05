// Package routegw is the AF route gateway (ADR 0128 decision 7, option A2): an HTTP endpoint on
// the Agent's loopback that holds a route's credential and forwards a session's model calls with
// it, so the credential never reaches the session. This file is the token contract; gateway.go is
// the check every call runs; bedrock.go is the first upstream.
//
// A route token is not the engine token. It is bound to a connection (the Settings profile), a
// route and one incarnation of one session; it ends when the session does, when the selection
// changes, or when the connection is changed, unmarked, deleted or loses its approval. It is held
// only as a hash, in this process's memory: an Agent restart ends every token, which is the safe
// direction (the session fails visibly and is relaunched).
package routegw

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// MaxTTL is the longest a token lives: ADR 0126's approval ceiling. A token never outlives the
// approval it depends on, and nothing here is long-lived or workspace-scoped.
const MaxTTL = 8 * time.Hour

// tokenPrefix makes a leaked token recognisable by a secret scanner and refuses a stray string
// before it reaches the map.
const tokenPrefix = "afrt_"

// Binding is what a token is bound to. Every field is checked again on every call (gateway.go);
// the token itself carries none of it.
type Binding struct {
	Session string
	// Incarnation is awsx.Incarnation of the session when the token was issued: a new session of
	// the same name is another incarnation and cannot use it.
	Incarnation string
	Route       string
	// Connection is the selection's connection reference as stored in the session.
	Connection string
	ProfileID  string
	Region     string
	// Identity is awsx.ProfileIdentity when the token was issued.
	Identity string
}

type entry struct {
	b       Binding
	expires time.Time
}

// Registry holds the live tokens.
type Registry struct {
	mu    sync.Mutex
	byKey map[[32]byte]entry
	now   func() time.Time
}

// NewRegistry returns an empty registry; now is the clock (nil = time.Now).
func NewRegistry(now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{byKey: map[[32]byte]entry{}, now: now}
}

// Mint issues a token for b and ends every earlier token of the same session: a relaunch, which
// is when a selection can have changed, leaves exactly one live token. ttl is clamped to MaxTTL.
func (r *Registry) Mint(b Binding, ttl time.Duration) (token string, expires time.Time, err error) {
	if b.Session == "" || b.Incarnation == "" || b.Route == "" || b.ProfileID == "" {
		return "", time.Time{}, errors.New("a route token needs a session, its incarnation, a route and a connection")
	}
	if ttl <= 0 || ttl > MaxTTL {
		ttl = MaxTTL
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", time.Time{}, err
	}
	token = tokenPrefix + hex.EncodeToString(raw[:])
	expires = r.now().Add(ttl)
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, e := range r.byKey {
		if e.b.Session == b.Session {
			delete(r.byKey, k)
		}
	}
	r.byKey[sha256.Sum256([]byte(token))] = entry{b: b, expires: expires}
	return token, expires, nil
}

// Lookup returns the binding of a live token. An expired token is removed and unknown.
func (r *Registry) Lookup(token string) (Binding, bool) {
	if len(token) < len(tokenPrefix) || token[:len(tokenPrefix)] != tokenPrefix {
		return Binding{}, false
	}
	k := sha256.Sum256([]byte(token))
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byKey[k]
	if !ok {
		return Binding{}, false
	}
	if !r.now().Before(e.expires) {
		delete(r.byKey, k)
		return Binding{}, false
	}
	return e.b, true
}

// Revoke ends one token.
func (r *Registry) Revoke(token string) {
	r.mu.Lock()
	delete(r.byKey, sha256.Sum256([]byte(token)))
	r.mu.Unlock()
}

// RevokeIf ends every token whose binding the predicate accepts, and says how many it ended.
func (r *Registry) RevokeIf(pred func(Binding) bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for k, e := range r.byKey {
		if pred(e.b) {
			delete(r.byKey, k)
			n++
		}
	}
	return n
}

// RevokeSession ends every token of a session.
func (r *Registry) RevokeSession(name string) int {
	return r.RevokeIf(func(b Binding) bool { return b.Session == name })
}

// Len is the number of tokens held, expired ones included until they are swept.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byKey)
}

// Sweep drops expired tokens and those keep reports false for, and says how many it dropped. keep
// is how a periodic sweep ends the tokens of sessions that are gone; the per-call check already
// refuses them, so the sweep only frees memory.
func (r *Registry) Sweep(keep func(Binding) bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	now, n := r.now(), 0
	for k, e := range r.byKey {
		if !now.Before(e.expires) || (keep != nil && !keep(e.b)) {
			delete(r.byKey, k)
			n++
		}
	}
	return n
}
