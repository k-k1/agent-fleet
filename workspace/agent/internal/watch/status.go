package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
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
//	status:<service>/after/<floor unix>[/c/<component id>-<component id>…]
//
// The component IDs, not the names, are in the subject: they are header-token safe and short. The
// floor is the page's own clock when the watch was made; it only matters for incident.resolved.
//
// The three types follow the engine's level rule (a source reports what holds now):
//
//	status.recovered          every watched component is operational and no open incident names
//	                          one of them (whole page: no open incident at all)
//	status.degraded           a watched component is not operational
//	status.incident.resolved  an incident that names a watched component was resolved after the
//	                          watch was made; history is never told, because the summary lists
//	                          open incidents only and an old resolved one is not "news"
//
// One page is read at most once per statusPageTTL whatever the number of watches on it: reads go
// through pageCache, keyed by URL. Incident titles and statuses and component names are third-party
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
	statusPageTTL     = 2 * time.Minute
	statusMaxBody     = 4 << 20
	statusMaxComps    = 8
	statusDataMax     = 200
	statusMaxDataItem = 10
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
	statusRefRE  = regexp.MustCompile(`^([A-Za-z-]{1,20})(?::(.*))?$`)
	statusSubjRE = regexp.MustCompile(`^status:([a-z]{1,20})/after/([0-9]{1,12})(?:/c/([A-Za-z0-9]{1,40}(?:-[A-Za-z0-9]{1,40})*))?$`)
	statusIDRE   = regexp.MustCompile(`^[A-Za-z0-9]{1,40}$`)
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
}

// Status is the status-page source. Base maps a service to a base URL for tests; Now is
// replaceable by tests.
type Status struct {
	Client *http.Client
	Base   map[string]string
	Now    func() time.Time

	mu    sync.Mutex
	pages map[string]pageEntry
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

// fetch returns the body of one API path, shared for statusPageTTL. The lock is held across the
// read on purpose: subjects of one page that are due together then make one request, not one each.
// A failed read is not cached.
func (s *Status) fetch(ctx context.Context, service, path string) (body []byte, date int64, status int, err error) {
	url := s.base(service) + path
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.pages[url]; ok && s.now().Sub(e.at) < statusPageTTL {
		return e.body, e.date, http.StatusOK, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "agent-fleet")
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("status page %s unreachable: %w", StatusHosts[service], err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, 0, resp.StatusCode, nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, statusMaxBody))
	if err != nil {
		return nil, 0, 0, err
	}
	var d int64
	if t, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		d = t.Unix()
	}
	if s.pages == nil {
		s.pages = map[string]pageEntry{}
	}
	s.pages[url] = pageEntry{at: s.now(), body: b, date: d}
	return b, d, http.StatusOK, nil
}

func (s *Status) summary(ctx context.Context, service string) (spSummary, int64, error) {
	b, date, status, err := s.fetch(ctx, service, "/api/v2/summary.json")
	if err != nil {
		return spSummary{}, 0, err
	}
	if status != http.StatusOK {
		return spSummary{}, 0, fmt.Errorf("status page %s answered %d", StatusHosts[service], status)
	}
	var sum spSummary
	if err := json.Unmarshal(b, &sum); err != nil {
		return spSummary{}, 0, fmt.Errorf("status page %s: unreadable answer", StatusHosts[service])
	}
	return sum, date, nil
}

func (s *Status) Resolve(ctx context.Context, ref, _ string) (Resolved, error) {
	r, err := parseStatusRef(ref)
	if err != nil {
		return Resolved{}, err
	}
	sum, date, err := s.summary(ctx, r.service)
	if err != nil {
		return Resolved{}, err
	}
	if date == 0 {
		date = s.now().Unix()
	}
	var ids []string
	for _, name := range r.comps {
		c, ok := findComponent(sum.Components, name)
		if !ok {
			return Resolved{}, &ErrRefused{fmt.Sprintf("%s has no component %s; it lists %s", r.service, quoteShort(name), componentNames(sum.Components))}
		}
		if c.Group {
			return Resolved{}, &ErrRefused{fmt.Sprintf("%s is a group of components; name the components inside it", quoteShort(c.Name))}
		}
		if !statusIDRE.MatchString(c.ID) {
			return Resolved{}, &ErrRefused{"that component cannot be watched"}
		}
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	subj := fmt.Sprintf("status:%s/after/%d", r.service, date)
	if len(ids) > 0 {
		subj += "/c/" + strings.Join(ids, "-")
	}
	if !TokenOK(subj) {
		return Resolved{}, &ErrRefused{"that reference is too long"}
	}
	return Resolved{Subject: SubjectID(subj), URL: SafeURL("https://" + StatusHosts[r.service])}, nil
}

func findComponent(list []spComponent, name string) (spComponent, bool) {
	for _, c := range list {
		if strings.EqualFold(strings.TrimSpace(c.Name), name) {
			return c, true
		}
	}
	return spComponent{}, false
}

func componentNames(list []spComponent) string {
	var names []string
	for _, c := range list {
		names = append(names, CleanText(c.Name, 40))
	}
	if len(names) > 30 {
		names = append(names[:30], "…")
	}
	return strings.Join(names, ", ")
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

// statusCursor remembers the newest resolved incident already told, so an edit of an older one
// (or a re-ordered list) cannot make it the best candidate again.
type statusCursor struct {
	MarkAt int64  `json:"mark_at,omitempty"`
	MarkID string `json:"mark_id,omitempty"`
}

func (s *Status) Poll(ctx context.Context, subject SubjectID, raw json.RawMessage, want []string) ([]Event, json.RawMessage, Hint, error) {
	m := statusSubjRE.FindStringSubmatch(string(subject))
	if m == nil {
		return nil, raw, Hint{}, fmt.Errorf("not a status subject: %s", subject)
	}
	service, floor := m[1], atoi64(m[2])
	if _, ok := StatusHosts[service]; !ok {
		return nil, raw, Hint{}, fmt.Errorf("not a status subject: %s", subject)
	}
	var ids []string
	if m[3] != "" {
		ids = strings.Split(m[3], "-")
	}
	var cur statusCursor
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cur)
	}
	hint := Hint{After: statusPollAfter}
	var evs []Event
	at := s.now().UTC()

	if wantsStatus(want, EventStatusRecovered) || wantsStatus(want, EventStatusDegraded) {
		sum, _, err := s.summary(ctx, service)
		if err != nil {
			return nil, raw, Hint{}, err
		}
		watched, err := selectComponents(sum.Components, ids)
		if err != nil {
			return nil, raw, Hint{}, err
		}
		open := matchingIncidents(sum.Incidents, watched, len(ids) == 0)
		evs = append(evs, statusLevelEvents(service, subject, watched, open, len(ids) == 0, at)...)
	}
	if wantsStatus(want, EventStatusIncidentResolved) {
		b, _, status, err := s.fetch(ctx, service, "/api/v2/incidents.json")
		if err != nil {
			return nil, raw, Hint{}, err
		}
		if status != http.StatusOK {
			return nil, raw, Hint{}, fmt.Errorf("status page %s answered %d", StatusHosts[service], status)
		}
		var list spIncidents
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, raw, Hint{}, fmt.Errorf("status page %s: unreadable answer", StatusHosts[service])
		}
		var ev *Event
		ev, cur = resolvedIncidentEvent(service, subject, list.Incidents, ids, floor, cur, at)
		if ev != nil {
			evs = append(evs, *ev)
		}
	}
	out, _ := json.Marshal(cur)
	return evs, out, hint, nil
}

// selectComponents is the watched set: the named components, or every non-group component of the
// page. A named component that vanished from the page ends the watch's meaning, so it is an error
// the engine keeps retrying and then reports, not a silent "recovered".
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

// matchingIncidents are the open incidents that count for the watch: any open incident for a
// whole-page watch, one that names a watched component otherwise.
func matchingIncidents(all []spIncident, watched []spComponent, whole bool) []spIncident {
	var out []spIncident
	for _, inc := range all {
		if isResolvedStatus(inc.Status) {
			continue
		}
		if whole || incidentNames(inc, watched) {
			out = append(out, inc)
		}
	}
	return out
}

func isResolvedStatus(s string) bool { return s == "resolved" || s == "postmortem" }

// statusLevelEvents builds the recovered / degraded conditions that hold now. The version of a
// degraded state carries the newest update time of the components behind it and a hash of their
// statuses, so a change of state is news and the same state after a recovery in between is too
// (the update time moved). Recovered is versioned by the newest update time alone.
func statusLevelEvents(service string, subject SubjectID, watched []spComponent, open []spIncident, whole bool, at time.Time) []Event {
	var bad []spComponent
	var newest, newestBad int64
	h := sha256.New()
	for _, c := range watched {
		if u := c.updated(); u > newest {
			newest = u
		}
		if c.Status != "operational" {
			bad = append(bad, c)
			if u := c.updated(); u > newestBad {
				newestBad = u
			}
			fmt.Fprintf(h, "%s=%s;", c.ID, c.Status)
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
		for _, inc := range open {
			if len(d) >= statusMaxDataItem {
				break
			}
			d = append(d, DataItem{Text: CleanText(fmt.Sprintf("Incident: %s (status: %s)", inc.Name, inc.Status), statusDataMax), URL: SafeURL(inc.Shortlink)})
		}
		return d
	}
	if len(bad) > 0 {
		v := fmt.Sprintf("%d-%s", newestBad, hex.EncodeToString(h.Sum(nil))[:8])
		return []Event{{
			Source: StatusKind, Subject: subject, Type: EventStatusDegraded, Version: v, At: at,
			Summary: fmt.Sprintf("%s status: %d of %d %s not operational.", service, len(bad), len(watched), scope),
			Data:    data(true),
		}}
	}
	if len(open) == 0 {
		return []Event{{
			Source: StatusKind, Subject: subject, Type: EventStatusRecovered, Version: fmt.Sprintf("%d", newest), At: at,
			Summary: fmt.Sprintf("%s status: all %d %s operational and no open incident.", service, len(watched), scope),
		}}
	}
	return nil
}

// resolvedIncidentEvent picks the newest incident resolved after the floor and after the cursor's
// mark that names a watched component (any incident, for a whole-page watch), and moves the mark.
func resolvedIncidentEvent(service string, subject SubjectID, list []spIncident, ids []string, floor int64, cur statusCursor, at time.Time) (*Event, statusCursor) {
	var best *spIncident
	var bestAt int64
	for i := range list {
		inc := &list[i]
		if !isResolvedStatus(inc.Status) {
			continue
		}
		t, err := time.Parse(time.RFC3339, inc.ResolvedAt)
		if err != nil {
			continue
		}
		u := t.Unix()
		if u <= floor || u < cur.MarkAt || u == cur.MarkAt && inc.ID <= cur.MarkID {
			continue
		}
		if len(ids) > 0 {
			hit := false
			for _, ic := range inc.Components {
				if slicesContains(ids, ic.ID) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		if best == nil || u > bestAt || u == bestAt && inc.ID > best.ID {
			best, bestAt = inc, u
		}
	}
	if best == nil || !statusIDRE.MatchString(best.ID) {
		return nil, cur
	}
	cur.MarkAt, cur.MarkID = bestAt, best.ID
	return &Event{
		Source: StatusKind, Subject: subject, Type: EventStatusIncidentResolved, Version: best.ID, At: at,
		Summary: fmt.Sprintf("%s status: an incident was resolved.", service),
		Data:    []DataItem{{Text: CleanText(fmt.Sprintf("Incident: %s (status: %s)", best.Name, best.Status), statusDataMax), URL: SafeURL(best.Shortlink)}},
	}, cur
}

func slicesContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
