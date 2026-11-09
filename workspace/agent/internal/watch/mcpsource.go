package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// Member-added watch sources (ADR 0133 §8). An MCP server the member registered with
// Targets.Watch is a source: the engine, never a session, calls its watch_resolve and
// watch_poll tools. Everything the server returns is untrusted text:
//
//   - its event types must be ext.<source>.* and its subjects ext:<source>:…, and a subject must
//     be one the engine asked about, so it cannot pose as a built-in event, speak of another
//     source's subject or inject into a subscription it was not asked to read;
//   - the engine composes Summary itself; whatever the server says goes into Data;
//   - one call lasts 10 s, carries 20 subjects and returns 20 events, a subject is read at most
//     once a minute, and five failed calls in a row pause the source.

// Budget of ADR 0133 §8, fixed.
const (
	mcpCallTimeout   = 10 * time.Second
	mcpMaxSubjects   = 20
	mcpMaxEvents     = 20
	mcpMinPoll       = time.Minute
	mcpMaxFailures   = 5
	mcpMaxRefLen     = 200
	mcpMaxReply      = 256 << 10 // bytes of one tool result
	mcpMaxCursor     = 4 << 10
	mcpMaxVersionLen = 200
	mcpMaxDataItems  = 10
	mcpDataTextMax   = 300
	mcpDefsTTL       = 5 * time.Second
)

// MCPConn is the part of mcpc.Manager the sources use.
type MCPConn interface {
	Sync(ctx context.Context, defs []mcpreg.ServerDef) map[string]error
	CallServer(ctx context.Context, serverName, tool string, args json.RawMessage) (text string, isError bool, err error)
}

// MCPSources finds the member's MCP watch sources for the engine (Engine.Dynamic).
type MCPSources struct {
	// Allowed is the tenant switch. It is checked at subscribe time and at every read, so
	// switching it off stops both, and an unset hook is off.
	Allowed func() bool
	// Defs lists the member's own watch-source definitions (mcpreg.WatchDefs).
	Defs func() ([]mcpreg.ServerDef, error)
	// KindOf is the agent kind of a session, "" when unknown.
	KindOf func(session string) string
	Conn   MCPConn

	now func() time.Time

	connMu  sync.Mutex // serialises Sync, which can wait on a handshake; mu never does
	mu      sync.Mutex
	defs    []mcpreg.ServerDef
	defsAt  time.Time
	defsErr error
	state   map[string]*mcpState // by definition ID
}

// mcpState is the failure bookkeeping of one source.
type mcpState struct {
	fails      int
	violations int
	pausedAt   int64 // the definition's UpdatedAt when it was paused; 0 = running
	paused     bool
	lastErr    string
}

var slugRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// MCPSlug is the <source> of a definition's events: its name lower-cased, "-" as "_". ok is false
// for a name that cannot be written into an event type (it starts with a digit, or is long).
func MCPSlug(name string) (string, bool) {
	s := strings.ReplaceAll(strings.ToLower(name), "-", "_")
	return s, slugRE.MatchString(s)
}

func (m *MCPSources) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// Lookup is Engine.Dynamic. A kind "ext.<slug>" always answers, even when the definition is
// gone or the switch is off, so that watches on it show why they are silent instead of being
// skipped without a trace.
func (m *MCPSources) Lookup(kind string) (Source, bool) {
	slug, ok := strings.CutPrefix(kind, "ext.")
	if !ok || !slugRE.MatchString(slug) {
		return nil, false
	}
	return &mcpSource{p: m, slug: slug}, true
}

// defsFor returns the unambiguous definitions by slug. fresh skips the short cache.
func (m *MCPSources) defsFor(fresh bool) (map[string]mcpreg.ServerDef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fresh || m.clock().Sub(m.defsAt) >= mcpDefsTTL {
		m.defs, m.defsErr = m.Defs()
		m.defsAt = m.clock()
	}
	if m.defsErr != nil {
		return nil, m.defsErr
	}
	bySlug := map[string]mcpreg.ServerDef{}
	clash := map[string]bool{}
	for _, d := range m.defs {
		if d.Origin != mcpreg.OriginUser && d.Origin != "" {
			continue // only the member's own server is a source
		}
		s, ok := MCPSlug(d.Name)
		if !ok || !d.Targets.Watch {
			continue
		}
		if _, dup := bySlug[s]; dup {
			clash[s] = true
		}
		bySlug[s] = d
	}
	for s := range clash {
		delete(bySlug, s) // two names that spell the same source: neither is usable
	}
	return bySlug, nil
}

func (m *MCPSources) stateLocked(id string) *mcpState {
	if m.state == nil {
		m.state = map[string]*mcpState{}
	}
	st := m.state[id]
	if st == nil {
		st = &mcpState{}
		m.state[id] = st
	}
	return st
}

// gate says why d cannot be called now: the switch is off, or the source is paused. A paused
// source is lifted when the member edits or toggles the definition (UpdatedAt moves).
func (m *MCPSources) gate(d mcpreg.ServerDef) error {
	if m.Allowed == nil || !m.Allowed() {
		// Close whatever is running: a server of a tenant that switched the feature off keeps no
		// child process of ours.
		m.Conn.Sync(context.Background(), nil)
		return errors.New("watch sources are switched off for this tenant")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.stateLocked(d.ID)
	if st.paused && st.pausedAt != d.UpdatedAt {
		*st = mcpState{}
	}
	if st.paused {
		return fmt.Errorf("source paused after %d failed calls (last: %s); edit or re-enable the MCP server to resume", mcpMaxFailures, st.lastErr)
	}
	return nil
}

// record counts one call: ok clears the run of failures, a failure extends it and pauses the
// source at the fifth.
func (m *MCPSources) record(d mcpreg.ServerDef, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.stateLocked(d.ID)
	if err == nil {
		st.fails = 0
		return
	}
	st.fails++
	st.lastErr = CleanText(err.Error(), 120)
	if st.fails >= mcpMaxFailures && !st.paused {
		st.paused, st.pausedAt = true, d.UpdatedAt
		log.Printf("watch: source %s paused after %d failed calls", d.Name, st.fails)
	}
}

// connect makes sure Conn serves exactly the allowed definitions, so a removed or switched-off
// source has its child process closed.
func (m *MCPSources) connect(ctx context.Context, d mcpreg.ServerDef) error {
	all, err := m.Defs()
	if err != nil {
		return err
	}
	var want []mcpreg.ServerDef
	for _, x := range all {
		if x.Targets.Watch && (x.Origin == mcpreg.OriginUser || x.Origin == "") {
			want = append(want, x)
		}
	}
	m.connMu.Lock()
	defer m.connMu.Unlock()
	if errs := m.Conn.Sync(ctx, want); errs[d.Name] != nil {
		return fmt.Errorf("cannot start the server: %w", errs[d.Name])
	}
	return nil
}

// call runs one tool with the 10 s budget and returns the JSON object it answered.
func (m *MCPSources) call(ctx context.Context, d mcpreg.ServerDef, tool string, args any) (json.RawMessage, error) {
	if err := m.connect(ctx, d); err != nil {
		return nil, err
	}
	b, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, mcpCallTimeout)
	defer cancel()
	text, isErr, err := m.Conn.CallServer(cctx, d.Name, tool, b)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%s: %w", tool, err)
	case isErr:
		return nil, fmt.Errorf("%s reported an error", tool)
	case len(text) > mcpMaxReply:
		return nil, fmt.Errorf("%s answered more than %d bytes", tool, mcpMaxReply)
	}
	return json.RawMessage(text), nil
}

// mcpSource is the Source view of one definition.
type mcpSource struct {
	p    *MCPSources
	slug string
}

var (
	_ Source        = (*mcpSource)(nil)
	_ BatchPoller   = (*mcpSource)(nil)
	_ MinIntervaler = (*mcpSource)(nil)
	_ TypeChecker   = (*mcpSource)(nil)
)

func (s *mcpSource) Kind() string          { return "ext." + s.slug }
func (s *mcpSource) typePrefix() string    { return "ext." + s.slug + "." }
func (s *mcpSource) subjectPrefix() string { return "ext:" + s.slug + ":" }
func (s *mcpSource) Types() []string       { return []string{s.typePrefix() + "*"} }
func (s *mcpSource) MaxBatch() int         { return mcpMaxSubjects }
func (s *mcpSource) MinPollInterval() time.Duration {
	return mcpMinPoll
}

// CheckTypes accepts patterns under ext.<source>. only; the server names its own types.
func (s *mcpSource) CheckTypes(patterns []string) error {
	if len(patterns) == 0 {
		return &ErrRefused{"types is required, e.g. [\"" + s.typePrefix() + "*\"]"}
	}
	if len(patterns) > 20 {
		return &ErrRefused{"at most 20 types"}
	}
	for _, p := range patterns {
		if !typeRE.MatchString(p) || !strings.HasPrefix(p, s.typePrefix()) || len(p) > 100 {
			return &ErrRefused{"bad type " + quoteShort(p) + ": this source's types start with " + s.typePrefix()}
		}
	}
	return nil
}

func (s *mcpSource) def() (mcpreg.ServerDef, error) {
	by, err := s.p.defsFor(false)
	if err != nil {
		return mcpreg.ServerDef{}, err
	}
	d, ok := by[s.slug]
	if !ok {
		return mcpreg.ServerDef{}, errors.New("this MCP server is no longer a watch source (removed, disabled or not marked as one)")
	}
	return d, nil
}

// Resolve asks the server about ref. What the server may be sent is one line of printable ASCII,
// and when the subscribing session could not reach the server itself, only header tokens: watch
// must not carry a session's text to a server that session cannot talk to.
func (s *mcpSource) Resolve(ctx context.Context, ref, _ string) (Resolved, error) {
	d, err := s.def()
	if err != nil {
		return Resolved{}, &ErrRefused{err.Error()}
	}
	if err := s.p.gate(d); err != nil {
		return Resolved{}, &ErrRefused{err.Error()}
	}
	who := SubscriberFrom(ctx)
	kind := ""
	if who != "" && s.p.KindOf != nil {
		kind = s.p.KindOf(who)
	}
	if kind == "" {
		return Resolved{}, &ErrRefused{"cannot tell which session is subscribing"}
	}
	if !mcpreg.KindAllowed(d, kind) {
		return Resolved{}, &ErrRefused{"this MCP server is not offered to " + kind + " sessions"}
	}
	if err := checkMCPRef(ref, d.Enabled && d.Targets.Session); err != nil {
		return Resolved{}, err
	}
	raw, err := s.p.call(ctx, d, "watch_resolve", map[string]string{"ref": ref})
	s.p.record(d, err)
	if err != nil {
		return Resolved{}, err
	}
	var out struct {
		Subject string `json:"subject"`
		Refused string `json:"refused"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		s.p.record(d, err)
		return Resolved{}, errors.New("watch_resolve did not answer with JSON")
	}
	if out.Refused != "" {
		// The server's own words are data for the member who typed the ref, not a prompt.
		return Resolved{}, &ErrRefused{"the server refused: " + quoteShort(CleanText(out.Refused, 200))}
	}
	if !strings.HasPrefix(out.Subject, s.subjectPrefix()) || len(out.Subject) == len(s.subjectPrefix()) || !TokenOK(out.Subject) {
		return Resolved{}, &ErrRefused{"the server returned a subject outside " + s.subjectPrefix() + "…"}
	}
	// No URL: it would reach the notice header outside Data.
	return Resolved{Subject: SubjectID(out.Subject)}, nil
}

func checkMCPRef(ref string, reachable bool) error {
	if ref == "" || len(ref) > mcpMaxRefLen {
		return &ErrRefused{fmt.Sprintf("ref must be 1 to %d characters", mcpMaxRefLen)}
	}
	for _, r := range ref {
		if r < 0x20 || r > 0x7e {
			return &ErrRefused{"ref must be one line of printable ASCII"}
		}
	}
	if !reachable && !TokenOK(ref) {
		return &ErrRefused{"this server is not attached to your session, so ref may only use letters, digits and .:/#_-"}
	}
	return nil
}

// Poll is unused: the engine calls PollBatch.
func (s *mcpSource) Poll(context.Context, SubjectID, json.RawMessage, []string) ([]Event, json.RawMessage, Hint, error) {
	return nil, nil, Hint{}, errors.New("an MCP source is read in batches")
}

type mcpWireEvent struct {
	Subject string          `json:"subject"`
	Type    string          `json:"type"`
	Version string          `json:"version"`
	Summary string          `json:"summary"`
	Data    json.RawMessage `json:"data"`
}

type mcpWireHint struct {
	AfterSeconds float64 `json:"after_seconds"`
	Settled      bool    `json:"settled"`
}

type mcpWireReply struct {
	Events  []mcpWireEvent             `json:"events"`
	Cursors map[string]json.RawMessage `json:"cursors"`
	Hints   map[string]mcpWireHint     `json:"hints"`
}

// PollBatch reads up to 20 subjects with one watch_poll call.
func (s *mcpSource) PollBatch(ctx context.Context, items []PollItem) []PollResult {
	failAll := func(err error) []PollResult {
		out := make([]PollResult, len(items))
		for i, it := range items {
			out[i] = PollResult{Subject: it.Subject, Cursor: it.Cursor, Err: err}
		}
		return out
	}
	d, err := s.def()
	if err != nil {
		return failAll(err)
	}
	if err := s.p.gate(d); err != nil {
		return failAll(err)
	}
	if len(items) > mcpMaxSubjects {
		items = items[:mcpMaxSubjects]
	}
	asked := make(map[string]PollItem, len(items))
	subjects := make([]string, 0, len(items))
	cursors := map[string]json.RawMessage{}
	for _, it := range items {
		id := string(it.Subject)
		// Only subjects of this source are ever sent: another source's subject, however it got
		// here, is not this server's to read.
		if !strings.HasPrefix(id, s.subjectPrefix()) {
			continue
		}
		asked[id] = it
		subjects = append(subjects, id)
		if len(it.Cursor) > 0 {
			cursors[id] = it.Cursor
		}
	}
	raw, err := s.p.call(ctx, d, "watch_poll", map[string]any{"subjects": subjects, "cursors": cursors})
	s.p.record(d, err)
	if err != nil {
		return failAll(err)
	}
	var reply mcpWireReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		err = errors.New("watch_poll did not answer with the expected JSON")
		s.p.record(d, err)
		return failAll(err)
	}
	now := s.p.clock().UTC()
	byID := map[string]*PollResult{}
	out := make([]PollResult, len(items)) // never grown: byID points into it
	for i, it := range items {
		out[i] = PollResult{Subject: it.Subject, Cursor: it.Cursor, Hint: Hint{After: mcpMinPoll}}
		if _, ok := asked[string(it.Subject)]; !ok {
			out[i].Err = errors.New("not a subject of this source")
		}
		byID[string(it.Subject)] = &out[i]
	}
	truncated := map[string]bool{}
	violations, kept := 0, 0
	for _, w := range reply.Events {
		r, ok := byID[w.Subject]
		if _, wasAsked := asked[w.Subject]; !ok || !wasAsked {
			violations++ // a subject nobody asked the server about
			continue
		}
		if kept >= mcpMaxEvents {
			truncated[w.Subject] = true
			continue
		}
		ev, ok := s.compose(w, now)
		if !ok {
			violations++
			truncated[w.Subject] = true // not understood: do not move past it
			continue
		}
		r.Events = append(r.Events, ev)
		kept++
	}
	for id, c := range reply.Cursors {
		r, ok := byID[id]
		if _, wasAsked := asked[id]; !ok || !wasAsked {
			violations++
			continue
		}
		// A new cursor only for a subject whose every event is in hand.
		if truncated[id] || len(c) == 0 || len(c) > mcpMaxCursor || string(c) == "null" {
			continue
		}
		r.Cursor = c
	}
	for id, h := range reply.Hints {
		if r, ok := byID[id]; ok {
			if _, wasAsked := asked[id]; wasAsked {
				r.Hint = Hint{After: clampAfter(h.AfterSeconds), Settled: h.Settled}
			}
		}
	}
	if violations > 0 {
		s.p.mu.Lock()
		s.p.stateLocked(d.ID).violations += violations
		s.p.mu.Unlock()
		s.p.record(d, fmt.Errorf("%d malformed or out-of-namespace entries in a reply", violations))
	}
	return out
}

func clampAfter(sec float64) time.Duration {
	d := time.Duration(sec * float64(time.Second))
	return min(max(d, mcpMinPoll), 24*time.Hour)
}

// compose turns one wire event into an engine event. Summary is the engine's own wording from
// source, type and subject; the server's summary and data become Data.
func (s *mcpSource) compose(w mcpWireEvent, at time.Time) (Event, bool) {
	if !strings.HasPrefix(w.Type, s.typePrefix()) || len(w.Type) == len(s.typePrefix()) || len(w.Type) > 100 ||
		!typeRE.MatchString(w.Type) || !TokenOK(w.Type) {
		return Event{}, false
	}
	if w.Version == "" || len(w.Version) > mcpMaxVersionLen || strings.ContainsFunc(w.Version, func(r rune) bool { return r < 0x20 || r > 0x7e }) {
		return Event{}, false
	}
	ev := Event{
		Source: s.Kind(), Subject: SubjectID(w.Subject), Type: w.Type, Version: w.Version, At: at,
		Summary: fmt.Sprintf("%s reported %s for %s", s.Kind(), w.Type, w.Subject),
	}
	if t := CleanText(w.Summary, mcpDataTextMax); t != "" {
		ev.Data = append(ev.Data, DataItem{Text: t})
	}
	ev.Data = append(ev.Data, parseMCPData(w.Data)...)
	if len(ev.Data) > mcpMaxDataItems {
		ev.Data = ev.Data[:mcpMaxDataItems]
	}
	return ev, true
}

// parseMCPData reads the server's data: a list of strings or {text, url} objects. Anything else
// becomes one compact text item.
func parseMCPData(raw json.RawMessage) []DataItem {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return []DataItem{{Text: CleanText(string(raw), mcpDataTextMax)}}
	}
	var out []DataItem
	for _, e := range list {
		var str string
		var obj struct {
			Text string `json:"text"`
			URL  string `json:"url"`
		}
		switch {
		case json.Unmarshal(e, &str) == nil:
			out = append(out, DataItem{Text: CleanText(str, mcpDataTextMax)})
		case json.Unmarshal(e, &obj) == nil && obj.Text != "":
			out = append(out, DataItem{Text: CleanText(obj.Text, mcpDataTextMax), URL: SafeURL(obj.URL)})
		default:
			out = append(out, DataItem{Text: CleanText(string(e), mcpDataTextMax)})
		}
		if len(out) >= mcpMaxDataItems {
			break
		}
	}
	return out
}
