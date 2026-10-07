package cloudlogin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	// RequestTTL is how long a request stays after the last run asked for it.
	RequestTTL = 15 * time.Minute
	// CancelHold is how long a cancel keeps new runs from filing again: without it an
	// agent that reruns at once would put the toast straight back. Short, because nothing
	// in the Console can lift it: a member who cancelled on the wrong device and wants to
	// log in from another one waits this long (ADR 0102).
	CancelHold = time.Minute
)

// Backend is what a cloud tells this package about its credential store. S is a state
// of that store for one request key; states are compared with ==, never interpreted here.
type Backend[S comparable] interface {
	// State reads the store's state for key now. It must never return a secret.
	State(key string) S
	// Landed reports whether cur, read at now, shows a login that may settle a request
	// recorded against recorded. A request whose state has landed is resolved, however
	// the login happened.
	Landed(cur, recorded S, now time.Time) bool
}

// Store is one backend's requests and attempts. The zero value is not usable: set Dir,
// NoticeKind, NoticeKey, LogPrefix and Backend.
type Store[S comparable] struct {
	// Dir is the directory under paths.AgentStateDir() that holds the request files.
	Dir string
	// NoticeKind is the outbox notification kind that carries a new request's id.
	NoticeKind string
	// NoticeKey prefixes the outbox key of that notification (NoticeKey + ":" + id).
	NoticeKey string
	// LogPrefix starts the Agent's log lines about attempts and cancels.
	LogPrefix string
	Backend   Backend[S]

	attempts registry
	gates    gateSet
}

// Waiter is one run waiting on a request. Both fields are text an agent wrote, so they
// are cut down before they are stored (see CleanWaiterText).
type Waiter struct {
	Session string `json:"session,omitempty"`
	Command string `json:"command,omitempty"`
	At      string `json:"at"`
}

// Request is one pending request, one file per request key. The JSON names are fixed by
// files already on disk (see the package doc).
type Request[S comparable] struct {
	ID       string   `json:"id"`
	Profile  string   `json:"profile"`
	Key      string   `json:"ssoSession"`
	FirstAt  string   `json:"firstAt"`
	LastAt   string   `json:"lastAt"`
	Snapshot S        `json:"cache"`
	Waiters  []Waiter `json:"waiters,omitempty"`
}

// cancelMarker is written by the Agent when a request is cancelled, before the request
// file goes. Waiters react only to the marker naming their own request.
type cancelMarker[S comparable] struct {
	RequestID string `json:"requestId"`
	Profile   string `json:"profile"`
	At        string `json:"at"`
	Snapshot  S      `json:"cache"`
}

func (s *Store[S]) dir() string { return filepath.Join(paths.AgentStateDir(), s.Dir) }

// fileKey turns a request key into a file name that cannot escape the directory,
// whatever the key holds.
func fileKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:12])
}

// RequestPath is the file that holds the request of key.
func (s *Store[S]) RequestPath(key string) string {
	return filepath.Join(s.dir(), fileKey(key)+".request.json")
}

func (s *Store[S]) markerPath(key string) string {
	return filepath.Join(s.dir(), fileKey(key)+".cancel.json")
}

// ErrLockInterrupted is FlockEx ending because cancel closed.
var ErrLockInterrupted = errors.New("interrupted while waiting for a lock")

// ErrLockTimeout is FlockEx ending because the deadline passed.
var ErrLockTimeout = errors.New("timed out waiting for a lock")

// FlockEx takes an exclusive flock on f like a blocking LOCK_EX, but gives up when cancel
// closes or a non-zero deadline passes: a wait for a lock somebody else holds must end
// with the run's own Ctrl-C and wait budget, not with the holder. nil cancel and a zero
// deadline wait for as long as the holder does.
func FlockEx(f *os.File, cancel <-chan struct{}, deadline time.Time) error {
	if cancel == nil && deadline.IsZero() {
		return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EINTR {
			return err
		}
		select {
		case <-cancel:
			return ErrLockInterrupted
		default:
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return ErrLockTimeout
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// lock takes the one lock every reader-writer of the directory shares.
func (s *Store[S]) lock() (func(), error) { return s.lockCancel(nil, time.Time{}) }

func (s *Store[S]) lockCancel(cancel <-chan struct{}, deadline time.Time) (func(), error) {
	if err := os.MkdirAll(s.dir(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.dir(), ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := FlockEx(f, cancel, deadline); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(b, v) == nil
}

func writeJSONFile(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Read returns the request filed for key, if there is one.
func (s *Store[S]) Read(key string) (Request[S], bool) {
	var r Request[S]
	ok := readJSON(s.RequestPath(key), &r) && r.ID != "" && r.Key == key
	return r, ok
}

// Held reports whether a cancel of key still holds new runs back: its marker is younger
// than CancelHold and not made moot by a state that changed since.
func (s *Store[S]) Held(key string, now time.Time) bool {
	var m cancelMarker[S]
	if !readJSON(s.markerPath(key), &m) {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, m.At)
	return err == nil && now.Sub(at) < CancelHold && s.Backend.State(key) == m.Snapshot
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var waiterTextRe = regexp.MustCompile(`[^A-Za-z0-9._@:/+-]`)

// CleanWaiterText keeps what a waiter field may show: a short run of plain characters.
// The Console prints it next to Settings' identity fields (an account, a role), so it
// must not be able to look like either.
func CleanWaiterText(s string) string {
	s = waiterTextRe.ReplaceAllString(strings.TrimSpace(s), "")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// WaiterWire is one waiter as the Console shows it.
type WaiterWire struct {
	Session string `json:"session,omitempty"`
	Command string `json:"command,omitempty"`
}

// RecentWaiters returns up to n distinct waiters of r, newest first, cleaned again here
// and not only when filed: any agent can write the file directly.
func RecentWaiters(ws []Waiter, n int) []WaiterWire {
	seen := map[WaiterWire]bool{}
	out := []WaiterWire{}
	for i := len(ws) - 1; i >= 0 && len(out) < n; i-- {
		ww := WaiterWire{Session: CleanWaiterText(ws[i].Session), Command: CleanWaiterText(ws[i].Command)}
		if ww == (WaiterWire{}) {
			continue
		}
		if !seen[ww] {
			seen[ww] = true
			out = append(out, ww)
		}
	}
	return out
}

// ErrHeld means a cancel in the Console still holds new requests back.
var ErrHeld = errors.New("the login request was cancelled in the Console")

// File files (or joins) the request of key for profile, recording snap: the state this
// run saw before its failing check. A new request also writes the notification that
// carries its id to the Console.
func (s *Store[S]) File(profile, key string, snap S, w Waiter) (Request[S], bool, error) {
	return s.FileCancel(profile, key, snap, w, nil, time.Time{})
}

// FileCancel is File whose wait for the directory lock ends with cancel or deadline
// (ErrLockInterrupted, ErrLockTimeout).
func (s *Store[S]) FileCancel(profile, key string, snap S, w Waiter, cancel <-chan struct{}, deadline time.Time) (Request[S], bool, error) {
	unlock, err := s.lockCancel(cancel, deadline)
	if err != nil {
		return Request[S]{}, false, err
	}
	defer unlock()
	now := time.Now().UTC()
	if s.Held(key, now) {
		return Request[S]{}, false, ErrHeld
	}
	w.Session, w.Command = CleanWaiterText(w.Session), CleanWaiterText(w.Command)
	w.At = now.Format(time.RFC3339Nano)
	r, exists := s.Read(key)
	created := !exists
	if created {
		r = Request[S]{ID: newID(), Profile: profile, Key: key, FirstAt: w.At}
	}
	r.LastAt = w.At
	// Joining: the record must describe a state this run found unusable.
	r.Snapshot = snap
	r.Waiters = append(r.Waiters, w)
	if len(r.Waiters) > 20 {
		r.Waiters = r.Waiters[len(r.Waiters)-20:]
	}
	if err := writeJSONFile(s.RequestPath(key), r); err != nil {
		return Request[S]{}, false, err
	}
	if created {
		ev := notice.New(s.NoticeKind, "", "", "")
		ev.TargetType = "workspace"
		if session.ValidName(w.Session) {
			if m, ok := session.ReadMeta(w.Session); ok {
				ev.TargetType, ev.SessionName, ev.SessionKind = "session", m.Name, m.Kind
			}
		}
		// The Console reads nothing but this id from the payload: the outbox is writable
		// by every agent, so anything else here would be text it could forge.
		ev.Payload["requestId"] = r.ID
		_ = notice.PutOnce(s.NoticeKey+":"+r.ID, ev)
	}
	return r, created, nil
}

// requestState is what a waiter reads every poll.
type requestState int

const (
	requestPending requestState = iota
	requestCancelled
	requestGone
)

// stateOf reports what became of the request id of key.
func (s *Store[S]) stateOf(key, id string) requestState {
	var m cancelMarker[S]
	if readJSON(s.markerPath(key), &m) && m.RequestID == id {
		return requestCancelled
	}
	if r, ok := s.Read(key); ok && r.ID == id {
		return requestPending
	}
	return requestGone
}

// Sweep drops what is settled and returns what is still pending, oldest first. A request
// is resolved once its key's state has landed (Backend.Landed) against the state it
// recorded; it expires RequestTTL after the last run asked, never while an attempt for it
// runs. Only the Agent removes request files.
func (s *Store[S]) Sweep(now time.Time) []Request[S] {
	unlock, err := s.lock()
	if err != nil {
		return nil
	}
	defer unlock()
	entries, _ := os.ReadDir(s.dir())
	var out []Request[S]
	for _, e := range entries {
		path := filepath.Join(s.dir(), e.Name())
		switch {
		case strings.HasSuffix(e.Name(), ".cancel.json"):
			var m cancelMarker[S]
			at, perr := time.Time{}, error(nil)
			if readJSON(path, &m) {
				at, perr = time.Parse(time.RFC3339Nano, m.At)
			}
			if perr != nil || at.IsZero() || now.Sub(at) >= CancelHold {
				_ = os.Remove(path)
			}
		case strings.HasSuffix(e.Name(), ".request.json"):
			var r Request[S]
			if !readJSON(path, &r) || r.ID == "" {
				_ = os.Remove(path)
				continue
			}
			if s.Backend.Landed(s.Backend.State(r.Key), r.Snapshot, now) {
				_ = os.Remove(path)
				continue
			}
			last, perr := time.Parse(time.RFC3339Nano, r.LastAt)
			if (perr != nil || now.Sub(last) >= RequestTTL) && !s.attempts.liveFor(r.Key, r.ID) {
				_ = os.Remove(path)
				continue
			}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstAt < out[j].FirstAt })
	return out
}

var idRe = regexp.MustCompile(`^[0-9a-f]{24}$`)

// Pending finds the pending request with id.
func (s *Store[S]) Pending(id string) (Request[S], bool) {
	if !idRe.MatchString(id) {
		return Request[S]{}, false
	}
	for _, r := range s.Sweep(time.Now()) {
		if r.ID == id {
			return r, true
		}
	}
	return Request[S]{}, false
}

// ErrNoRequest means no pending request has the id asked for.
var ErrNoRequest = errors.New("no pending login request with that id")

// Cancel ends the request id: it ends an attempt the request started, writes the cancel
// marker, then drops the request. A login the member started from Settings is not the
// request's to end: cancelling says "I do not want this request", and that login settles
// the request anyway.
func (s *Store[S]) Cancel(id string) (Request[S], error) {
	req, ok := s.Pending(id)
	if !ok {
		return Request[S]{}, ErrNoRequest
	}
	if cur := s.attempts.current(req.Key); cur != nil && cur.RequestID != "" {
		cur.End(PhaseCancelled, "")
	}
	unlock, err := s.lock()
	if err != nil {
		return req, err
	}
	defer unlock()
	m := cancelMarker[S]{RequestID: req.ID, Profile: req.Profile, At: time.Now().UTC().Format(time.RFC3339Nano), Snapshot: req.Snapshot}
	if err := writeJSONFile(s.markerPath(req.Key), m); err != nil {
		return req, err
	}
	if cur, ok := s.Read(req.Key); ok && cur.ID == req.ID {
		_ = os.Remove(s.RequestPath(req.Key))
	}
	return req, nil
}

// gateSet holds one mutex per request key; see Store.Gate.
type gateSet struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

// Gate is key's gate. Start holds it from making the attempt until the process has started,
// so a backend that rewrites the key's credential files (a logout taking a token off disk)
// can hold it to keep a login process from opening those files meanwhile: a process that
// opened one first and rewrites it in place would write into the deleted file. Never hold
// it across a network call.
func (s *Store[S]) Gate(key string) *sync.Mutex {
	s.gates.mu.Lock()
	defer s.gates.mu.Unlock()
	if s.gates.m == nil {
		s.gates.m = map[string]*sync.Mutex{}
	}
	g := s.gates.m[key]
	if g == nil {
		g = &sync.Mutex{}
		s.gates.m[key] = g
	}
	return g
}

// LockKey takes key's credential lock, across processes: shared by a wrapper run while it
// turns the cached login into credentials, exclusive by a logout while it deletes them. A run
// that read the login just before a logout would otherwise write fresh credentials back after
// the logout deleted them.
func (s *Store[S]) LockKey(key string, shared bool) (func(), error) {
	if err := os.MkdirAll(s.dir(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.LockKeyPath(key), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if shared {
		how = syscall.LOCK_SH
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// LockKeyPath is the file LockKey locks, for tests that need it unlockable.
func (s *Store[S]) LockKeyPath(key string) string {
	return filepath.Join(s.dir(), fileKey(key)+".cache.lock")
}

// registry holds the attempts of one Store. Attempts live in memory only: an Agent
// restart loses them, and the process dies with the Agent (ADR 0102 decision 3).
type registry struct {
	mu    sync.Mutex
	byID  map[string]*Attempt
	byKey map[string]*Attempt
}

func (g *registry) initLocked() {
	if g.byID == nil {
		g.byID, g.byKey = map[string]*Attempt{}, map[string]*Attempt{}
	}
}

func (g *registry) byIDOf(id string) *Attempt {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	return g.byID[id]
}

func (g *registry) current(key string) *Attempt {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	return g.byKey[key]
}

// liveFor reports whether an attempt for that request is still running: the request
// must not expire under it. A login started from a Settings row does not count; it would
// otherwise keep any request for the profile up for its whole run.
func (g *registry) liveFor(key, requestID string) bool {
	a := g.current(key)
	return a != nil && a.RequestID == requestID && a.Live()
}

// add makes a the current attempt of its key and returns the one it replaces.
func (g *registry) add(a *Attempt) *Attempt {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	prev := g.byKey[a.Key]
	g.pruneLocked(time.Now())
	g.byID[a.ID] = a
	g.byKey[a.Key] = a
	return prev
}

// pruneLocked forgets attempts that ended long ago.
func (g *registry) pruneLocked(now time.Time) {
	for id, a := range g.byID {
		a.mu.Lock()
		old := !a.ended.IsZero() && now.Sub(a.ended) > 30*time.Minute
		a.mu.Unlock()
		if old {
			delete(g.byID, id)
			if g.byKey[a.Key] == a {
				delete(g.byKey, a.Key)
			}
		}
	}
}
