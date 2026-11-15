// Package routepolicy is the Agent's copy of the credential policy (ADR 0138 D12): the effective
// policy of this Workspace's member, as the Control Plane computes it, held on disk and kept
// fresh by a pull. It decides nothing about a create by itself; the enforcement points ask it.
//
// The rules that make it safe to judge by a copy:
//   - Only a strictly higher revision replaces the held policy. The CP gives every policy write
//     a revision greater than any before it, so a restart, an out-of-order answer, a replayed
//     one or a stale cache can never bring back a weaker rule.
//   - Freshness comes only from the CP answering now: a live pull, or the start environment the
//     CP issued, at the time it says it issued it. An old start environment read again by a
//     restarted Agent confirms nothing.
//   - When the held policy restricts anything, or the deployment has a ceiling, and nothing has
//     confirmed it for FreshnessLimit, new work is refused ("the policy cannot be confirmed")
//     until something does. A member nobody restricts is never blocked by an unreachable CP.
//   - The copy on disk is written file-synced then renamed then directory-synced, behind a floor
//     record written first: the revision being taken and whether it restricts. The floor is the
//     anti-replay mark that survives a lost copy (Open says how each case is held): a restart
//     never holds a copy older than the floor, nor a copy no valid floor vouches for.
//   - The floor is written twice, beside the copy and as a mirror in the state directory, which
//     is another volume on the runtimes where the config directory is a network mount. When the
//     config directory cannot be written, the newer rule is enforced in memory and the mirror
//     records it, so a restart holds the newer of the two floors and never the older copy. Each
//     floor write is numbered, because within one revision the state moves both ways (a start
//     marker restricts, a live answer clears it) and only the order of the writes tells which
//     of two floors at that revision is the newer.
//   - When at a start neither directory can be written, nothing on disk can be trusted to show
//     the last rule in force, so new work waits for the CP's current answer. Only when a pull
//     shows the CP cannot be reached (no connection, a 5xx, no bridge) does the Agent judge by
//     what is on disk, as it would during any outage; a replayed, malformed or refused answer
//     keeps it waiting.
package routepolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Policy is the effective policy as the CP sends it (control-plane routepolicysrv.MemberPolicy).
// With Restricted false every route is allowed; with it true only the routes in Allowed are,
// and an id the CP did not list (one it does not know) is refused, except a per-connection
// route on an approved self-hosted endpoint (endpoints.go): Endpoints are the approved hosts and
// EndpointRoutes the routes allowed on one.
type Policy struct {
	Revision          int64    `json:"revision"`
	Restricted        bool     `json:"restricted"`
	CeilingRestricted bool     `json:"ceilingRestricted"`
	Allowed           []string `json:"allowed"`
	Endpoints         []string `json:"endpoints,omitempty"`
	EndpointRoutes    []string `json:"endpointRoutes,omitempty"`
}

// Allows reports whether the policy allows the route id. A per-connection route is allowed on
// the host its connection points at now (RegisterEndpoint) when that host is approved.
func (p Policy) Allows(route string) bool {
	return !p.Restricted || slices.Contains(p.Allowed, route) || p.onApprovedEndpoint(route)
}

func (p Policy) restricting() bool { return p.Restricted || p.CeilingRestricted }

// FreshnessLimit is how long a restricting policy is trusted without a confirmation: the route
// gateway's rule for its Settings pull (ADR 0128 note of 2026-11-05).
const FreshnessLimit = 15 * time.Minute

// ErrUnrecorded is the refusal of new work after a start at which no floor could be written,
// until the CP's current answer or a pull that shows it cannot be reached. The first pull is
// made at once in that case.
var ErrUnrecorded = errors.New("the credential policy cannot be recorded on disk and the Control Plane has not answered since the Agent started, so new work waits for its answer")

// ErrUnconfirmed is the refusal of new work while a restricting policy cannot be confirmed.
var ErrUnconfirmed = errors.New("the credential policy cannot be confirmed: the Control Plane has not answered for over 15 minutes, so new work is refused until it does")

// held is what is kept on disk: the policy and when the CP last confirmed it.
type held struct {
	Policy Policy `json:"policy"`
	Held   bool   `json:"held"`
	// LastOK is when the CP last confirmed this revision or a higher one (Unix seconds). It is
	// on disk so a restart during an outage does not restart the clock.
	LastOK int64 `json:"lastOk"`
	// Placeholder marks a policy that stands in for one the Agent cannot read (a lost copy under
	// a restricting or untrusted floor, a start the CP could not compute under a restricting
	// ceiling): it allows nothing, and an answer at its revision or above replaces it.
	Placeholder bool `json:"placeholder,omitempty"`
	// NeedsLive: the placeholder's revision may be lower than the last one taken (its floor
	// could not be read), so only a live pull, which answers the CP's current revision, replaces
	// it; a start environment cannot.
	NeedsLive bool `json:"needsLive,omitempty"`
	// SeenStart is the id of the last unknown start marker taken (applied or found already
	// covered). The marker stays in the container's environment for its whole life, so a restart
	// reads it again: by its id it is taken once, and a later live answer is never undone by it,
	// whatever the clocks say.
	SeenStart string `json:"seenStart,omitempty"`
}

// floor is the record written before every copy: the revision held and whether it restricts. It
// is the anti-replay high-water mark that survives a lost copy, and for an unrestricted policy it
// is the whole of it (an unrestricted policy needs no allowed list).
type floor struct {
	Revision    int64 `json:"revision"`
	Restricting bool  `json:"restricting"`
	// SeenStart mirrors held.SeenStart, so the receipt of an unknown start marker survives a lost
	// copy: without it, an unrestricted tenant whose copy was lost would be re-blocked by a
	// marker it had already taken.
	SeenStart string `json:"seenStart,omitempty"`
	// Placeholder and NeedsLive mirror held's: a restricting floor may stand for a real policy
	// with an allowed list or for a placeholder that allows nothing, and only a copy in the same
	// state is the one the floor vouches for.
	Placeholder bool `json:"placeholder,omitempty"`
	NeedsLive   bool `json:"needsLive,omitempty"`
	// Seq numbers the floor writes of this store, both files alike, so Open can tell which of two
	// floors at one revision is the newer: within a revision the state moves both ways (a start
	// marker restricts, a live answer clears it). 0 on a floor written before it existed.
	Seq int64 `json:"seq,omitempty"`
}

// floorState is what Open found in the floor file.
type floorState int

const (
	floorMissing floorState = iota
	floorOK
	floorUntrusted // present but unreadable, malformed or incomplete
)

// floorOf is the floor that records h, without its sequence number.
func floorOf(h held) floor {
	return floor{Revision: h.Policy.Revision, Restricting: h.Policy.restricting(), SeenStart: h.SeenStart,
		Placeholder: h.Placeholder, NeedsLive: h.NeedsLive}
}

// vouchesFor reports whether the floor, at the copy's own revision, records the copy's state.
// A restricting floor from before Placeholder was recorded (no sequence number) cannot say
// whether it stood for a placeholder, so it vouches for no restricting copy: blessing a
// restricted copy's allowed list in place of a placeholder would widen what is allowed.
func (f floor) vouchesFor(c held) bool {
	if f.Revision != c.Policy.Revision || f.Restricting != c.Policy.restricting() {
		return false
	}
	if f.Restricting && f.Seq == 0 {
		return false
	}
	return f.Placeholder == c.Placeholder && f.NeedsLive == c.NeedsLive
}

// newer reports whether floor a was written after b: a higher revision, or the same one written
// later. Only between floors no sequence number tells apart (both from before it) does the
// restricting one count as the newer, the safe direction.
func (a floor) newer(b floor) bool {
	switch {
	case a.Revision != b.Revision:
		return a.Revision > b.Revision
	case a.Seq != b.Seq:
		return a.Seq > b.Seq
	}
	return a.Restricting && !b.Restricting
}

// same reports whether a and b record the same state, whatever their sequence numbers.
func (a floor) same(b floor) bool { a.Seq, b.Seq = 0, 0; return a == b }

// readFloor reads the floor record at file (floorPath of the copy, or the mirror).
func readFloor(file string) (floor, floorState) {
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return floor{}, floorMissing
	}
	if err != nil {
		return floor{}, floorUntrusted
	}
	var wire struct {
		Revision    *int64 `json:"revision"`
		Restricting *bool  `json:"restricting"`
		SeenStart   string `json:"seenStart"`
		Placeholder bool   `json:"placeholder"`
		NeedsLive   bool   `json:"needsLive"`
		Seq         int64  `json:"seq"`
	}
	if json.Unmarshal(b, &wire) != nil || wire.Revision == nil || wire.Restricting == nil || *wire.Revision < 0 || wire.Seq < 0 {
		return floor{}, floorUntrusted
	}
	if (wire.Placeholder || wire.NeedsLive) && !*wire.Restricting {
		return floor{}, floorUntrusted // a placeholder always restricts
	}
	return floor{Revision: *wire.Revision, Restricting: *wire.Restricting, SeenStart: wire.SeenStart, Placeholder: wire.Placeholder,
		NeedsLive: wire.NeedsLive, Seq: wire.Seq}, floorOK
}

// placeholder allows nothing and restricts, unconfirmed.
func placeholder(rev int64, needsLive bool) held {
	return held{Policy: Policy{Revision: rev, Restricted: true, CeilingRestricted: true, Allowed: []string{}}, Held: true,
		Placeholder: true, NeedsLive: needsLive}
}

// State is a snapshot for the Agent's own API and the enforcement points.
type State struct {
	Policy Policy
	// Held is false until a policy was ever received: there is then nothing to enforce.
	Held   bool
	LastOK time.Time
	// Placeholder: the held policy stands in for one that could not be read; it allows nothing.
	Placeholder bool
	// Holding: the Agent started unable to record a floor anywhere, and neither the CP's current
	// answer nor an outage has been seen since; new work waits for one of them.
	Holding bool
}

// Restricting reports whether the freshness rule applies: the held policy restricts anything or
// the deployment has a ceiling.
func (s State) Restricting() bool { return s.Held && s.Policy.restricting() }

// Confirmed reports whether new work may be judged by the held policy at now.
func (s State) Confirmed(now time.Time) bool {
	return !s.Holding && (!s.Restricting() || now.Sub(s.LastOK) <= FreshnessLimit)
}

// Store holds the policy. Use Open.
type Store struct {
	mu   sync.Mutex
	path string
	h    held
	// floorOK is true while the floor on disk is known to match h: the next write may then skip
	// it. Any doubt (a failed floor write, a floor Open could not vouch for) clears it, and the
	// next take writes the floor again before reporting anything durable.
	floorOK bool
	// mirror is the second floor's path ("" for none); mirrorOK is floorOK for it.
	mirror   string
	mirrorOK bool
	// seq is the highest floor sequence number written or read; the next floor write takes the
	// one above it, whether or not an earlier write landed.
	seq int64
	// holding: see State.Holding. Set by Open, cleared by releaseLocked.
	holding bool
	now     func() time.Time
}

// Path is where the Agent keeps its copy, in the durable config directory.
func Path() string { return filepath.Join(paths.AgentConfigDir(), "route-policy.json") }

// MirrorPath is where the Agent keeps the second floor, in the state directory: local disk on
// every runtime, so it is written when a network-mounted config directory is not.
func MirrorPath() string { return filepath.Join(paths.AgentStateDir(), "route-policy.floor") }

func floorPath(path string) string { return path + ".floor" }

// Open loads the copy at path behind its floor and the floor mirror (mirror; "" for none). The
// floor it judges by is the one beside the copy, replaced by the mirror when the mirror was
// written after it (floor.newer), or when neither the floor nor the copy is left; a mirror that
// is missing or malformed counts for nothing (an Agent from before it, a lost home volume).
//   - No floor and no copy: nothing held (a first boot).
//   - A valid copy above a valid floor, or at its revision and agreeing with it on whether it
//     restricts: the copy.
//   - A valid floor above the copy, or over a copy that is missing or unreadable: the floor's own
//     policy when it does not restrict (an unrestricted policy needs nothing more), a placeholder
//     at its revision when it does.
//   - A floor that is missing beside a copy, or unreadable, malformed or incomplete: nothing can
//     vouch for the copy, so a placeholder that only a live pull replaces.
//
// When neither the copy's directory nor the mirror's can be written now, the Store starts holding
// (State.Holding).
func Open(path, mirror string) *Store {
	s := &Store{path: path, mirror: mirror, now: time.Now}
	f, fs := readFloor(floorPath(path))
	m, ms := floor{}, floorMissing
	if mirror != "" {
		m, ms = readFloor(mirror)
	}
	c, copyErr := readCopy(path)
	copyMissing := errors.Is(copyErr, os.ErrNotExist)
	raised := false
	switch {
	case ms == floorUntrusted:
		log.Printf("route policy: ignoring the floor mirror at %s: unreadable or incomplete", mirror)
	case ms != floorOK:
	case fs == floorOK && m.newer(f), fs == floorMissing && copyMissing:
		// A mirror stands in for a missing floor only when the copy is gone too (the config
		// directory lost as a whole). Beside a surviving copy it could be the stale one of the
		// two, and at a tie a stale unrestricted floor would bless a placeholder's revision as
		// unrestricted; that case stays below, where nothing vouches for the copy.
		f, fs, raised = m, floorOK, true
	}
	s.seq = max(f.Seq, m.Seq)
	switch {
	case fs == floorMissing && copyMissing:
	case fs == floorOK && copyErr == nil && (c.Policy.Revision > f.Revision || f.vouchesFor(c)):
		// At a tie the two must agree, placeholder or not: the floor is written first, so a floor
		// that disagrees with a copy at its own revision is the newer of the two (a copy that
		// failed to land), and the copy's permissions are not the ones in force.
		s.h = c
		if f.Revision == c.Policy.Revision {
			// The floor is written first: where the two disagree on the receipt, the floor's
			// is the newer one (a copy that failed to land after recording a marker).
			s.h.SeenStart = f.SeenStart
		}
		s.floorOK = f.same(floorOf(s.h))
	case fs == floorOK && !f.Restricting:
		s.h = held{Policy: Policy{Revision: f.Revision, Allowed: []string{}}, Held: true, SeenStart: f.SeenStart}
		s.floorOK = f.same(floorOf(s.h))
	case fs == floorOK:
		log.Printf("route policy: the copy at %s cannot be trusted (%v) behind a restricting floor at revision %d; allowing nothing until the Control Plane answers",
			path, copyErrOr(copyErr, "older than the floor"), f.Revision)
		s.h = placeholder(f.Revision, f.NeedsLive)
		s.h.SeenStart = f.SeenStart
		s.floorOK = f.same(floorOf(s.h))
	default:
		rev := int64(0)
		if copyErr == nil {
			rev = c.Policy.Revision
		}
		if ms == floorOK {
			rev = max(rev, m.Revision)
		}
		log.Printf("route policy: the floor at %s cannot vouch for the copy; allowing nothing until the Control Plane answers", floorPath(path))
		s.h = placeholder(rev, true)
		if copyErr == nil {
			s.h.SeenStart = c.SeenStart
		}
	}
	if raised {
		s.floorOK = false // the floor beside the copy is behind the mirror: rewritten at the next take
	}
	s.mirrorOK = s.h.Held && ms == floorOK && m.same(floorOf(s.h))
	if !writable(filepath.Dir(path)) && (mirror == "" || !writable(filepath.Dir(mirror))) {
		log.Printf("route policy: no floor can be recorded (%s unwritable, mirror %q too); new work waits for the Control Plane",
			filepath.Dir(path), mirror)
		s.holding = true
	}
	return s
}

// writable reports whether a file can be created, written and synced in dir now: the test for a
// read-only mount and for a full disk alike.
func writable(dir string) bool {
	if os.MkdirAll(dir, 0o700) != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".route-policy-probe.*")
	if err != nil {
		return false
	}
	defer os.Remove(f.Name())
	_, err = f.Write([]byte("probe\n"))
	if err == nil {
		err = syncFile(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err == nil
}

func copyErrOr(err error, alt string) string {
	if err != nil {
		return err.Error()
	}
	return alt
}

// readCopy reads and validates the stored copy.
func readCopy(path string) (held, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return held{}, err
	}
	var wire struct {
		Policy      json.RawMessage `json:"policy"`
		Held        *bool           `json:"held"`
		LastOK      *int64          `json:"lastOk"`
		Placeholder bool            `json:"placeholder"`
		NeedsLive   bool            `json:"needsLive"`
		SeenStart   string          `json:"seenStart"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return held{}, err
	}
	if wire.Held == nil || wire.LastOK == nil || !*wire.Held {
		return held{}, errors.New("the copy holds no policy")
	}
	p, err := parse(wire.Policy)
	if err != nil {
		return held{}, err
	}
	return held{Policy: p, Held: true, LastOK: *wire.LastOK, Placeholder: wire.Placeholder, NeedsLive: wire.NeedsLive,
		SeenStart: wire.SeenStart}, nil
}

// State returns the current snapshot.
func (s *Store) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return State{Policy: s.h.Policy, Held: s.h.Held, LastOK: time.Unix(s.h.LastOK, 0), Placeholder: s.h.Placeholder,
		Holding: s.holding}
}

// releaseLocked ends the hold of a start that could record no floor (State.Holding), saying why.
// It is called for a live answer at or above the held revision (take), and for a pull that shows
// the CP cannot be reached or distributes no policy (Sync): what is on disk then decides, as in
// any outage. A replayed, malformed or refused answer does not release it. s.mu must be held.
func (s *Store) releaseLocked(why string) {
	if s.holding {
		s.holding = false
		log.Printf("route policy: %s; judging new work by %s", why, State{Policy: s.h.Policy, Held: s.h.Held,
			Placeholder: s.h.Placeholder}.Describe())
	}
}

func (s *Store) release(why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseLocked(why)
}

// Accept takes a live answer from the CP (a pull). A strictly higher revision, the first answer
// ever, or any answer at or above a placeholder's revision replaces the held policy; an answer at
// the held revision or above confirms it; a lower one is a replay or an out-of-order answer and
// changes nothing, its freshness included. It reports whether the held policy changed.
func (s *Store) Accept(p Policy) (changed bool, err error) {
	changed, err = s.take(p, s.now().Unix(), true)
	if changed {
		s.notify()
	}
	return changed, err
}

// Seed takes the start environment's policy, issued by the CP at issuedAt. It replaces like
// Accept (except a placeholder only a live pull may replace), but confirms only up to issuedAt:
// the environment is read again by every restart of the Agent in the same container, and an old
// one must not stand in for the CP answering now.
func (s *Store) Seed(p Policy, issuedAt int64) (changed bool, err error) {
	changed, err = s.take(p, issuedAt, false)
	if changed {
		s.notify()
	}
	return changed, err
}

// SeedUnknown takes the CP's word, in the start marker with the given id, that it could not
// compute the policy at this start while a ceiling restricts the deployment. A marker is taken
// once, by its id: the first time, it supersedes a held policy that knows of no ceiling with a
// placeholder at that policy's revision (the ceiling is newer than that policy); a policy that
// already knows the ceiling, or a placeholder, stays. Read again by a restart of the Agent in the
// same container, it changes nothing, so a live answer taken since is never undone by it.
func (s *Store) SeedUnknown(id string) (changed bool, err error) {
	changed, err = s.seedUnknown(id)
	if changed {
		s.notify()
	}
	return changed, err
}

func (s *Store) seedUnknown(id string) (changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.h
	if cur.Held && cur.SeenStart == id {
		return false, nil
	}
	next := cur
	if !cur.Held || !cur.Placeholder && !cur.Policy.CeilingRestricted {
		next, changed = placeholder(cur.Policy.Revision, false), true
	}
	next.SeenStart = id
	return changed, s.persistLocked(next)
}

func (s *Store) take(p Policy, confirmedAt int64, live bool) (bool, error) {
	if p.Allowed == nil {
		p.Allowed = []string{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.h
	var replace bool
	switch {
	case !cur.Held:
		replace = true
	case cur.Placeholder:
		replace = p.Revision >= cur.Policy.Revision && (live || !cur.NeedsLive)
		if !replace {
			return false, nil // a placeholder is never confirmed, only replaced
		}
	default:
		replace = p.Revision > cur.Policy.Revision
	}
	if live && (replace || p.Revision >= cur.Policy.Revision) {
		// The CP's current answer: the held policy is now what it says, recorded or not.
		s.releaseLocked("the Control Plane answered")
	}
	if !replace && (p.Revision < cur.Policy.Revision || confirmedAt <= cur.LastOK && s.floorOK && (s.mirrorOK || s.mirror == "")) {
		return false, nil // a replay, or nothing new to record
	}
	next := cur
	switch {
	case replace:
		// A new revision is confirmed by what brought it, never by a confirmation of an older one.
		next = held{Policy: p, Held: true, LastOK: confirmedAt, SeenStart: cur.SeenStart}
	case confirmedAt > next.LastOK:
		next.LastOK = confirmedAt
	}
	return replace, s.persistLocked(next)
}

// persistLocked holds next and records it: the floor and its mirror first whenever they may not
// match (so a copy that then fails to land is caught at the next start), then the copy. next is
// held in memory whatever the disk does: it is the rule to enforce now. s.mu must be held.
//
// The copy is not written when the floor beside it failed: at a tie a floor that disagrees with
// the copy is taken as the newer of the two, so a copy landing over an older floor at its own
// revision would be read back as the older, looser state.
func (s *Store) persistLocked(next held) error {
	want := floorOf(next)
	same := s.h.Held && floorOf(s.h) == want
	s.h = next
	if !s.floorOK || !same || s.mirror != "" && !s.mirrorOK {
		s.seq++
		want.Seq = s.seq
	}
	var errs []error
	if !s.floorOK || !same {
		s.floorOK = false
		if err := writeJSON(floorPath(s.path), want); err != nil {
			errs = append(errs, fmt.Errorf("recording the policy floor: %w", err))
		} else {
			s.floorOK = true
		}
	}
	if s.mirror != "" && (!s.mirrorOK || !same) {
		s.mirrorOK = false
		if err := writeJSON(s.mirror, want); err != nil {
			errs = append(errs, fmt.Errorf("recording the policy floor mirror: %w", err))
		} else {
			s.mirrorOK = true
		}
	}
	if !s.floorOK {
		return errors.Join(errs...)
	}
	if err := writeJSON(s.path, next); err != nil {
		errs = append(errs, fmt.Errorf("recording the policy: %w", err))
	}
	return errors.Join(errs...)
}

// Durability seams: a test injects failures through these.
var (
	syncFile = func(f *os.File) error { return f.Sync() }
	syncDir  = func(dir string) error {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	}
	rename = os.Rename
)

// writeJSON replaces path with v: a temporary file written and synced, renamed, then the directory
// synced, so after a crash the path holds either the old bytes or the new ones.
func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if werr == nil {
		werr = syncFile(tmp)
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return werr
	}
	return syncDir(dir)
}

// Gate is the enforcement points' question for new work: nil when it may be judged by the held
// policy now, ErrUnrecorded while a start that could record no floor waits for the CP,
// ErrUnconfirmed when a restricting policy has gone unconfirmed past FreshnessLimit.
func (s *Store) Gate() error {
	switch st := s.State(); {
	case st.Holding:
		return ErrUnrecorded
	case !st.Confirmed(s.now()):
		return ErrUnconfirmed
	}
	return nil
}

// Allows is the decision for a route id under the held policy (every route when nothing is held).
func (s *Store) Allows(route string) bool {
	st := s.State()
	return !st.Held || st.Policy.Allows(route)
}

// Describe is a one-line summary for the log.
func (st State) Describe() string {
	switch {
	case !st.Held:
		return "none held"
	case st.Placeholder:
		return fmt.Sprintf("a placeholder at revision %d that allows nothing", st.Policy.Revision)
	}
	return fmt.Sprintf("revision %d, restricted=%t, ceiling=%t, %d allowed", st.Policy.Revision, st.Policy.Restricted,
		st.Policy.CeilingRestricted, len(st.Policy.Allowed))
}
