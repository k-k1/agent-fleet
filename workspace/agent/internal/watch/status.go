package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// The status-page source of ADR 0133 §9: wait for a hosted service to recover from an outage. It
// reads the public Statuspage API (/api/v2/summary.json, and /api/v2/incidents.json only for a
// watch that asks for resolved incidents) of GitHub and of the Atlassian cloud products. Hosted
// services only: Bitbucket Data Center and self-hosted Jira have no public status page.
//
// Ref: "<service>[:<component>[,<component>…]]" with service one of github, bitbucket, jira,
// confluence. Without components the whole page is meant. Subject:
//
//	status:<service>[/c/<component id>-<component id>…]
//
// The component IDs, not the names, are in the subject: they are header-token safe and short. The
// subject carries no time, so the same call made again is the same watch; the floor of
// incident.resolved lives in the cursor (Resolved.Seed).
//
// The three types follow the engine's level rule (a source reports what holds now):
//
//	status.recovered          every watched component is operational and no open incident names
//	                          one of them (whole page: no open incident at all)
//	status.degraded           a watched component is not operational
//	status.incident.resolved  an incident that names a watched component was resolved after the
//	                          watch was made; history is never told, because an old resolved
//	                          incident is not "news"
//
// One URL is read at most once per statusPageTTL whatever the number of watches on it, and callers
// that ask at the same time share one request (fetch). Incident titles and statuses and component names are third-party
// text and only ever appear in Data (rule 6.5).

// Event types of the status source.
const (
	EventStatusRecovered        = "status.recovered"
	EventStatusDegraded         = "status.degraded"
	EventStatusIncidentResolved = "status.incident.resolved"
)

// StatusKind is the status source's name.
const StatusKind = "status"

const (
	// statusPollAfter is the gap between two reads of a subject. A status page is public and
	// shared by every user of the service, so the source stays gentle: an outage is not a
	// minutes-race and the page itself is cached for a few minutes upstream.
	statusPollAfter = 3 * time.Minute
	// statusPageTTL is how long one answer is shared between subjects of the same page. It is
	// shorter than statusPollAfter, so each poll cycle sees a fresh page, and longer than the
	// spread of the subjects of one cycle, so they share it.
	statusPageTTL = 2 * time.Minute
	// statusFailTTL keeps a failed read for a short while, so six subjects of one dead page cost one
	// timeout and not six.
	statusFailTTL  = 30 * time.Second
	statusSeenKeep = 7 * 24 * time.Hour
	statusSeenMax  = 200
	// statusResFresh is how long after the last read of incidents.json a new subscriber still
	// trusts the cursor's floor and Seen.
	statusResFresh    = 10 * time.Minute
	statusMaxBody     = 4 << 20
	statusMaxComps    = 8
	statusDataMax     = 100
	statusMaxDataItem = 5 // what one notice shows safely under the renderer's 2 KiB cut
)

// StatusHosts maps each service to the host of its status page. Documented in the guide, because
// an egress allowlist must name them.
var StatusHosts = map[string]string{
	"github":     "www.githubstatus.com",
	"bitbucket":  "bitbucket.status.atlassian.com",
	"jira":       "jira-software.status.atlassian.com",
	"confluence": "confluence.status.atlassian.com",
}

var statusAliases = map[string]string{"jira-software": "jira", "githubstatus": "github"}

var (
	statusRefRE   = regexp.MustCompile(`^([A-Za-z-]{1,20})(?::(.*))?$`)
	statusSubjRE  = regexp.MustCompile(`^status:([a-z]{1,20})(?:/c/([A-Za-z0-9]{1,40}(?:-[A-Za-z0-9]{1,40})*))?$`)
	statusIDRE    = regexp.MustCompile(`^[A-Za-z0-9]{1,40}$`)
	statusStateRE = regexp.MustCompile(`^[a-z_]{1,40}$`)
)

const statusRefHelp = "give <service>[:<component>,…] with service github, bitbucket, jira or confluence, e.g. github:Actions,API Requests or bitbucket"

// statusRef is a parsed reference.
type statusRef struct {
	service string
	comps   []string // names as given, case folded on match
}

func parseStatusRef(ref string) (statusRef, error) {
	m := statusRefRE.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return statusRef{}, &ErrRefused{statusRefHelp}
	}
	svc := strings.ToLower(m[1])
	if a, ok := statusAliases[svc]; ok {
		svc = a
	}
	if _, ok := StatusHosts[svc]; !ok {
		return statusRef{}, &ErrRefused{fmt.Sprintf("unknown service %s; %s", quoteShort(m[1]), statusRefHelp)}
	}
	r := statusRef{service: svc}
	if strings.TrimSpace(m[2]) == "" {
		if strings.Contains(strings.TrimSpace(ref), ":") {
			return statusRef{}, &ErrRefused{"a component list was announced and left empty; " + statusRefHelp}
		}
		return r, nil
	}
	seen := map[string]bool{}
	for _, c := range strings.Split(m[2], ",") {
		c = strings.TrimSpace(c)
		if c == "" || len([]rune(c)) > 80 {
			return statusRef{}, &ErrRefused{"a component name must be 1 to 80 characters; " + statusRefHelp}
		}
		if k := strings.ToLower(c); !seen[k] {
			seen[k] = true
			r.comps = append(r.comps, c)
		}
	}
	if len(r.comps) > statusMaxComps {
		return statusRef{}, &ErrRefused{fmt.Sprintf("at most %d components", statusMaxComps)}
	}
	return r, nil
}

// ---- Statuspage wire types ----

type spComponent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
	Group     bool   `json:"group"`
}

type spIncident struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Status     string        `json:"status"`
	Shortlink  string        `json:"shortlink"`
	ResolvedAt string        `json:"resolved_at"`
	Components []spComponent `json:"components"`
}

type spSummary struct {
	Components []spComponent `json:"components"`
	Incidents  []spIncident  `json:"incidents"`
}

type spIncidents struct {
	Incidents []spIncident `json:"incidents"`
}

func (c spComponent) updated() int64 {
	if t, err := time.Parse(time.RFC3339, c.UpdatedAt); err == nil {
		return t.Unix()
	}
	return 0
}

// ---- the shared page reads ----

type pageEntry struct {
	at   time.Time
	body []byte
	date int64
	err  error
}

// pageRead is one answer of fetch. Fresh is true when this call (or a call that ran at the same
// time) went to the network, so Date is the provider's clock now and not a cache's age ago.
type pageRead struct {
	body  []byte
	date  int64
	fresh bool
}

// Status is the status-page source. Base maps a service to a base URL for tests; Now is
// replaceable by tests.
type Status struct {
	Client *http.Client
	Base   map[string]string
	Now    func() time.Time

	mu       sync.Mutex
	pages    map[string]pageEntry
	inflight map[string]chan struct{}
}

// NewStatus returns the status source.
func NewStatus() *Status {
	return &Status{Client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: sameOriginRedirect}}
}

func (s *Status) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Status) Kind() string { return StatusKind }
func (s *Status) Types() []string {
	return []string{EventStatusRecovered, EventStatusDegraded, EventStatusIncidentResolved}
}

// MinPollInterval bounds the reads of one subject whatever the engine's other hints say.
func (s *Status) MinPollInterval() time.Duration { return statusPollAfter }

func (s *Status) base(service string) string {
	if b, ok := s.Base[service]; ok {
		return strings.TrimRight(b, "/")
	}
	return "https://" + StatusHosts[service]
}

// fetch returns the body of one API path, shared per URL: a fresh answer, or a recent failure, is
// reused for statusPageTTL / statusFailTTL, and subjects that ask for one URL at the same time wait
// for one request. A slow page therefore holds up only the callers of that URL, and a caller whose
// context ends stops waiting. The read itself is detached from the first caller's context, so one
// cancelled caller cannot fail the others. A body is cached only after valid accepted it, so a
// truncated or empty 200 is neither believed nor remembered.
func (s *Status) fetch(ctx context.Context, service, path string, valid func([]byte) error) (pageRead, error) {
	url := s.base(service) + path
	for {
		// A cancelled caller starts no new traffic.
		if err := ctx.Err(); err != nil {
			return pageRead{}, err
		}
		s.mu.Lock()
		if e, ok := s.pages[url]; ok {
			age := s.now().Sub(e.at)
			if e.err == nil && age < statusPageTTL {
				s.mu.Unlock()
				return pageRead{body: e.body, date: e.date}, nil
			}
			if e.err != nil && age < statusFailTTL {
				s.mu.Unlock()
				return pageRead{}, e.err
			}
		}
		if ch, ok := s.inflight[url]; ok {
			s.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return pageRead{}, ctx.Err()
			}
		}
		ch := make(chan struct{})
		if s.inflight == nil {
			s.inflight = map[string]chan struct{}{}
		}
		s.inflight[url] = ch
		s.mu.Unlock()

		// The shared read runs on its own goroutine and its own deadline (the client's timeout), so
		// the caller that started it can leave on its context like any waiter.
		go func() {
			b, date, err := s.read(context.WithoutCancel(ctx), service, url, valid)
			s.mu.Lock()
			if s.pages == nil {
				s.pages = map[string]pageEntry{}
			}
			s.pages[url] = pageEntry{at: s.now(), body: b, date: date, err: err}
			delete(s.inflight, url)
			s.mu.Unlock()
			close(ch)
		}()
		select {
		case <-ch:
			s.mu.Lock()
			e := s.pages[url]
			s.mu.Unlock()
			if e.err != nil {
				return pageRead{}, e.err
			}
			return pageRead{body: e.body, date: e.date, fresh: true}, nil
		case <-ctx.Done():
			return pageRead{}, ctx.Err()
		}
	}
}

func (s *Status) read(ctx context.Context, service, url string, valid func([]byte) error) ([]byte, int64, error) {
	host := StatusHosts[service]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "agent-fleet")
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("status page %s unreachable", host)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, 0, fmt.Errorf("status page %s answered %d", host, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, statusMaxBody+1))
	if err != nil {
		return nil, 0, fmt.Errorf("status page %s: read failed", host)
	}
	if len(b) > statusMaxBody {
		return nil, 0, fmt.Errorf("status page %s: answer too large", host)
	}
	if err := valid(b); err != nil {
		return nil, 0, fmt.Errorf("status page %s: %v", host, err)
	}
	var d int64
	if t, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		d = t.Unix()
	}
	return b, d, nil
}

func validSummary(b []byte) error {
	var sum spSummary
	if err := json.Unmarshal(b, &sum); err != nil {
		return fmt.Errorf("unreadable answer")
	}
	if sum.Components == nil || sum.Incidents == nil {
		return fmt.Errorf("incomplete answer")
	}
	// A row that cannot be told apart or read is a damaged snapshot, not a component that left.
	for _, c := range sum.Components {
		if !statusIDRE.MatchString(c.ID) || !statusStateRE.MatchString(c.Status) {
			return fmt.Errorf("incomplete answer")
		}
	}
	return nil
}

func validIncidents(b []byte) error {
	var l spIncidents
	if err := json.Unmarshal(b, &l); err != nil {
		return fmt.Errorf("unreadable answer")
	}
	if l.Incidents == nil {
		return fmt.Errorf("incomplete answer")
	}
	return nil
}

func (s *Status) summary(ctx context.Context, service string) (spSummary, pageRead, error) {
	pr, err := s.fetch(ctx, service, "/api/v2/summary.json", validSummary)
	if err != nil {
		return spSummary{}, pageRead{}, err
	}
	var sum spSummary
	_ = json.Unmarshal(pr.body, &sum)
	return sum, pr, nil
}

func (s *Status) Resolve(ctx context.Context, ref, _ string) (Resolved, error) {
	r, err := parseStatusRef(ref)
	if err != nil {
		return Resolved{}, err
	}
	sum, pr, err := s.summary(ctx, r.service)
	if err != nil {
		return Resolved{}, err
	}
	// The floor for incident.resolved is the subscriber's own moment. The provider's Date is used
	// only for a read that really happened now: a cached answer's Date is up to statusPageTTL old
	// and would make an incident resolved just before the watch count as new. Without a Date the
	// Agent's clock stands in (a skewed clock moves the boundary by the skew, nothing else).
	floor := s.now().UnixMilli()
	if pr.fresh && pr.date > 0 {
		floor = pr.date * 1000
	}
	var ids []string
	for _, name := range r.comps {
		c, ok := findComponent(sum.Components, name)
		if !ok {
			// Fixed text on purpose: the page's own component names are third-party text and a
			// refusal reaches the model unquoted.
			return Resolved{}, &ErrRefused{fmt.Sprintf("the %s status page has no component named %s; use a component name exactly as the status page shows it", r.service, quoteShort(name))}
		}
		if c.Group {
			return Resolved{}, &ErrRefused{fmt.Sprintf("%s is a group of components; name a component inside it", quoteShort(name))}
		}
		if !statusIDRE.MatchString(c.ID) {
			return Resolved{}, &ErrRefused{"that component cannot be watched"}
		}
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	subj := "status:" + r.service
	if len(ids) > 0 {
		subj += "/c/" + strings.Join(slices.Compact(ids), "-")
	}
	if !TokenOK(subj) {
		return Resolved{}, &ErrRefused{"that reference is too long"}
	}
	seed, _ := json.Marshal(statusCursor{FloorMs: floor})
	return Resolved{Subject: SubjectID(subj), URL: SafeURL("https://" + StatusHosts[r.service]), Seed: seed}, nil
}

// SeedCursor keeps the floor of the first subscription of a subject: the cursor is shared, and a
// later subscriber must not move the boundary up past an incident the first one is waiting for.
// Incidents already told are in Seen, so a later subscriber does not get history either.
func (s *Status) SeedCursor(cursor, seed json.RawMessage) json.RawMessage {
	var c statusCursor
	if len(cursor) > 0 {
		_ = json.Unmarshal(cursor, &c)
	}
	// While incidents.json is being read for this subject, Seen covers everything told, so the
	// first floor stays. Otherwise nobody tracked resolutions since then and the new
	// subscriber's own moment is the boundary: an incident resolved while nobody watched is history.
	if c.FloorMs > 0 && c.ResAt > 0 && s.now().UnixMilli()-c.ResAt < statusResFresh.Milliseconds() {
		return cursor
	}
	var sd statusCursor
	_ = json.Unmarshal(seed, &sd)
	c.FloorMs, c.ResAt, c.Seen = sd.FloorMs, 0, nil
	out, _ := json.Marshal(c)
	return out
}

func findComponent(list []spComponent, name string) (spComponent, bool) {
	for _, c := range list {
		if strings.EqualFold(strings.TrimSpace(c.Name), name) {
			return c, true
		}
	}
	return spComponent{}, false
}

func wantsStatus(want []string, typ string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if MatchType(w, typ) {
			return true
		}
	}
	return false
}

// statusCursor is what a subject keeps between reads. The generations make the versions of the
// level events: a level that comes back after it went away is a new version, and a level that
// merely stays is the same one, whatever the page's timestamps do. Seen holds the resolved
// incidents already reported (pruned to the ones the provider still lists).
// statusSeen is a resolved incident already reported, kept for statusSeenKeep from its
// resolution so a listing that drops it for one read does not make it new again.
type statusSeen struct {
	ID string `json:"id"`
	Ms int64  `json:"ms"`
}

type statusCursor struct {
	FloorMs int64 `json:"floor_ms,omitempty"`
	// ResAt is when incidents.json was last read for this subject: Seen is only a trustworthy
	// record of what was told while someone keeps reading it.
	ResAt   int64        `json:"res_at,omitempty"`
	Seen    []statusSeen `json:"seen,omitempty"`
	RecGen  int          `json:"rec_gen,omitempty"`
	RecHold bool         `json:"rec_hold,omitempty"`
	DegGen  int          `json:"deg_gen,omitempty"`
	DegSig  string       `json:"deg_sig,omitempty"`
}

func (s *Status) Poll(ctx context.Context, subject SubjectID, raw json.RawMessage, want []string) ([]Event, json.RawMessage, Hint, error) {
	m := statusSubjRE.FindStringSubmatch(string(subject))
	if m == nil {
		return nil, raw, Hint{}, fmt.Errorf("not a status subject: %s", subject)
	}
	service := m[1]
	if _, ok := StatusHosts[service]; !ok {
		return nil, raw, Hint{}, fmt.Errorf("not a status subject: %s", subject)
	}
	var ids []string
	if m[2] != "" {
		ids = strings.Split(m[2], "-")
	}
	var cur statusCursor
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cur)
	}
	at := s.now().UTC()
	if cur.FloorMs == 0 {
		cur.FloorMs = at.UnixMilli()
	}
	wantRec, wantDeg, wantRes := wantsStatus(want, EventStatusRecovered), wantsStatus(want, EventStatusDegraded), wantsStatus(want, EventStatusIncidentResolved)
	var evs []Event
	var firstErr error
	answered := 0

	// The two endpoints are independent: a failure of one must not drop what the other settled.
	// A named component is checked against the summary whatever the types are, so a watch that
	// asks only for resolved incidents still ends when its component is gone.
	if wantRec || wantDeg || len(ids) > 0 {
		sum, _, err := s.summary(ctx, service)
		if err != nil {
			firstErr = err
		} else {
			watched, err := selectComponents(sum.Components, ids)
			var gone *ErrGone
			switch {
			case errors.As(err, &gone):
				return nil, raw, Hint{}, err
			case err != nil:
				firstErr = err
			case wantRec || wantDeg:
				// The summary only counts as an answer when it was asked for events; for a
				// resolved-only watch it is the existence check, and the events come from the list.
				answered++
				open := openIncidents(sum.Incidents)
				evs = append(evs, statusLevelEvents(service, subject, watched, open, len(ids) == 0, at, &cur, wantRec, wantDeg)...)
			}
		}
	}
	if wantRes {
		pr, err := s.fetch(ctx, service, "/api/v2/incidents.json", validIncidents)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			answered++
			var list spIncidents
			_ = json.Unmarshal(pr.body, &list)
			if ev := resolvedIncidentEvent(service, subject, list.Incidents, ids, at, &cur); ev != nil {
				evs = append(evs, *ev)
			}
		}
	}
	if answered == 0 && firstErr != nil {
		return nil, raw, Hint{}, firstErr
	}
	out, _ := json.Marshal(cur)
	return evs, out, Hint{After: statusPollAfter}, nil
}

// selectComponents is the watched set: the named components, or every leaf (non-group) component
// of the page. A named component that vanished from the page ends the watch's meaning: ErrGone.
func selectComponents(all []spComponent, ids []string) ([]spComponent, error) {
	var out []spComponent
	if len(ids) == 0 {
		for _, c := range all {
			if !c.Group {
				out = append(out, c)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("the status page lists no components")
		}
		return out, nil
	}
	for _, id := range ids {
		found := false
		for _, c := range all {
			if c.ID == id {
				out = append(out, c)
				found = true
				break
			}
		}
		if !found {
			return nil, &ErrGone{Reason: "a watched component is no longer listed on the status page"}
		}
	}
	return out, nil
}

func incidentNames(inc spIncident, watched []spComponent) bool {
	for _, ic := range inc.Components {
		for _, w := range watched {
			if ic.ID != "" && ic.ID == w.ID {
				return true
			}
		}
	}
	return false
}

// openIncidents are the unresolved incidents of a page, with the components each names. Whether
// one counts is decided by the watch: any of them for the whole page (an incident that names no
// component included), one that names a watched component otherwise.
func openIncidents(all []spIncident) []spIncident {
	var out []spIncident
	for _, inc := range all {
		if !isResolvedStatus(inc.Status) {
			out = append(out, inc)
		}
	}
	return out
}

func isResolvedStatus(s string) bool { return s == "resolved" || s == "postmortem" }

// statusLevelEvents builds the recovered / degraded conditions that hold now and advances the
// generations in cur. Recovered counts when no watched component is broken and no counted open
// incident exists; degraded when one is broken, identified by the sorted (id, status) pairs, so
// the order of the page and unrelated timestamp updates change nothing.
func statusLevelEvents(service string, subject SubjectID, watched []spComponent, open []spIncident, whole bool, at time.Time, cur *statusCursor, wantRec, wantDeg bool) []Event {
	var bad []spComponent
	var sig []string
	for _, c := range watched {
		if c.Status != "operational" {
			bad = append(bad, c)
			sig = append(sig, c.ID+"="+c.Status)
		}
	}
	sort.Strings(sig)
	var counted []spIncident
	for _, inc := range open {
		if whole || incidentNames(inc, watched) {
			counted = append(counted, inc)
		}
	}
	scope := "watched components"
	if whole {
		scope = "components"
	}
	data := func(withComps bool) []DataItem {
		var d []DataItem
		if withComps {
			for _, c := range bad {
				if len(d) >= statusMaxDataItem {
					break
				}
				d = append(d, DataItem{Text: CleanText(fmt.Sprintf("Component: %s — %s", c.Name, c.Status), statusDataMax)})
			}
		}
		for _, inc := range counted {
			if len(d) >= statusMaxDataItem {
				break
			}
			d = append(d, DataItem{Text: CleanText(fmt.Sprintf("Incident: %s (status: %s)", inc.Name, inc.Status), statusDataMax), URL: SafeURL(inc.Shortlink)})
		}
		return d
	}
	var evs []Event
	if wantDeg {
		joined := ""
		if len(sig) > 0 {
			sum := sha256.Sum256([]byte(strings.Join(sig, ";")))
			joined = hex.EncodeToString(sum[:8])
		}
		if joined != "" && joined != cur.DegSig {
			cur.DegGen++
		}
		cur.DegSig = joined
		if len(bad) > 0 {
			evs = append(evs, Event{
				Source: StatusKind, Subject: subject, Type: EventStatusDegraded, Version: fmt.Sprintf("d%d", cur.DegGen), At: at,
				Summary: fmt.Sprintf("%s status: %d of %d %s not operational.", service, len(bad), len(watched), scope),
				Data:    data(true),
			})
		}
	}
	if wantRec {
		holds := len(bad) == 0 && len(counted) == 0
		if holds && !cur.RecHold {
			cur.RecGen++
		}
		cur.RecHold = holds
		if holds {
			evs = append(evs, Event{
				Source: StatusKind, Subject: subject, Type: EventStatusRecovered, Version: fmt.Sprintf("r%d", cur.RecGen), At: at,
				Summary: fmt.Sprintf("%s status: all %d %s operational and no open incident.", service, len(watched), scope),
			})
		}
	}
	return evs
}

// resolvedIncidentEvent reports the incidents resolved after the floor that are not in Seen yet
// and name a watched component (any, for the whole page). The version is the newest one's id and
// the others ride along as data, because the engine keeps one pending event per type: two
// incidents that first show up in one read are one notice, not one lost.
func resolvedIncidentEvent(service string, subject SubjectID, list []spIncident, ids []string, at time.Time, cur *statusCursor) *Event {
	type cand struct {
		inc spIncident
		ms  int64
	}
	seen := map[string]bool{}
	for _, e := range cur.Seen {
		seen[e.ID] = true
	}
	var cands []cand
	for _, inc := range list {
		if !statusIDRE.MatchString(inc.ID) {
			continue // one broken entry must not hide the others
		}
		if !isResolvedStatus(inc.Status) || seen[inc.ID] {
			continue
		}
		t, err := time.Parse(time.RFC3339, inc.ResolvedAt)
		if err != nil || t.UnixMilli() <= cur.FloorMs {
			continue
		}
		if len(ids) > 0 {
			hit := false
			for _, ic := range inc.Components {
				if slices.Contains(ids, ic.ID) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		cands = append(cands, cand{inc, t.UnixMilli()})
	}
	cur.ResAt = at.UnixMilli()
	// Told incidents are remembered by their resolution time, whether or not the provider keeps
	// listing them.
	cutoff := at.Add(-statusSeenKeep).UnixMilli()
	var keep []statusSeen
	for _, e := range cur.Seen {
		if e.Ms >= cutoff {
			keep = append(keep, e)
		}
	}
	cur.Seen = keep
	if len(cands) == 0 {
		return nil
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].ms != cands[j].ms {
			return cands[i].ms > cands[j].ms
		}
		return cands[i].inc.ID > cands[j].inc.ID
	})
	// A notice carries a few details (the renderer cuts at 2 KiB). The ones that do not fit are
	// not marked as told: the next read reports them, so none is lost to the cut.
	total := len(cands)
	if len(cands) > statusMaxDataItem {
		cands = cands[:statusMaxDataItem]
	}
	var data []DataItem
	for _, c := range cands {
		data = append(data, DataItem{Text: CleanText(fmt.Sprintf("Incident: %s (status: %s)", c.inc.Name, c.inc.Status), statusDataMax), URL: SafeURL(c.inc.Shortlink)})
		cur.Seen = append(cur.Seen, statusSeen{ID: c.inc.ID, Ms: c.ms})
	}
	if len(cur.Seen) > statusSeenMax {
		cur.Seen = cur.Seen[len(cur.Seen)-statusSeenMax:]
	}
	summary := fmt.Sprintf("%s status: an incident was resolved.", service)
	if total > 1 {
		summary = fmt.Sprintf("%s status: %d incidents were resolved.", service, len(cands))
		if total > len(cands) {
			summary = fmt.Sprintf("%s status: %d incidents were resolved (%d more follow).", service, len(cands), total-len(cands))
		}
	}
	return &Event{Source: StatusKind, Subject: subject, Type: EventStatusIncidentResolved, Version: cands[0].inc.ID, At: at, Summary: summary, Data: data}
}
