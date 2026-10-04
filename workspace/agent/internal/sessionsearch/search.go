package sessionsearch

import (
	"errors"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

const (
	defaultLimit      = 10
	maxLimit          = 50
	defaultPerSession = 3
	// candidatePool is how many FTS5 rows are re-ranked in Go (recency and the schedule weight
	// can reorder bm25's answer), already filtered and capped per session in SQL.
	candidatePool = 400
	snippetRunes  = 200

	maxWindow      = 10
	maxWindowBytes = 24 * 1024

	// scheduleWeight ranks down turns from scheduled runs, which repeat the same prompt and
	// would otherwise crowd out the one conversation where a person worked the problem.
	scheduleWeight = 0.5
	// recencyHalfLifeDays: a turn this old weighs half as much as one from today. A tie-breaker,
	// not a filter — an old answer that matches well still outranks a new one that barely does.
	recencyHalfLifeDays = 180.0
)

// Hit is one matching turn.
type Hit struct {
	Session  string  `json:"session"`
	Display  string  `json:"display"`
	Kind     string  `json:"kind"`
	Repo     string  `json:"repo,omitempty"`
	Archived bool    `json:"archived,omitempty"`
	Idx      int     `json:"idx"`
	Role     string  `json:"role"`
	TS       string  `json:"ts,omitempty"`
	Snippet  string  `json:"snippet"`
	Score    float64 `json:"score"`
}

// Result is GET /session-search's answer.
type Result struct {
	Hits []Hit `json:"hits"`
	// Indexing says a pass is running, so sessions changed since the last one may be missing.
	Indexing bool `json:"indexing"`
	// Indexed and Total are how many sessions the index holds and how many could be indexed;
	// Indexed < Total means some have never been read yet (the first pass after an upgrade).
	Indexed int `json:"indexed"`
	Total   int `json:"total"`
}

// Query is a search request after parsing.
type Query struct {
	Q          string
	Limit      int
	PerSession int
	Session    string
	Kind       string
	Repo       string
}

var nowFunc = time.Now

// Search answers q from the index as it stands.
func Search(q Query) (Result, error) {
	expr, err := MatchQuery(q.Q)
	if err != nil {
		return Result{}, err
	}
	s, err := openStore()
	if err != nil {
		return Result{}, err
	}
	// Only sessions that exist now, and pass the filters, are searched. Checking the metas at
	// answer time as well as in the pass means a session trashed since the last pass is never
	// found, whatever the index still holds.
	metas := map[string]session.Meta{}
	var names []string
	total := 0
	for _, m := range listMetas() {
		if !canTranscript(m) {
			continue
		}
		total++
		metas[m.Name] = m
		if (q.Session == "" || m.Name == q.Session) && (q.Kind == "" || m.Kind == q.Kind) && (q.Repo == "" || m.Repo == q.Repo) {
			names = append(names, m.Name)
		}
	}
	held, err := s.Names()
	if err != nil {
		return Result{}, err
	}
	indexed := 0
	for name := range held {
		if _, ok := metas[name]; ok {
			indexed++
		}
	}
	res := Result{Hits: []Hit{}, Indexed: indexed, Total: total}
	if len(names) == 0 {
		return res, nil
	}
	raw, err := s.match(expr, names, q.PerSession, candidatePool)
	if err != nil {
		return Result{}, err
	}
	terms := Terms(q.Q)
	now := nowFunc()
	var hits []Hit
	for _, h := range raw {
		m := metas[h.Session]
		hits = append(hits, Hit{
			Session: m.Name, Display: session.Display(m), Kind: m.Kind, Repo: m.Repo, Archived: m.Archived,
			Idx: h.Idx, Role: h.Role, TS: h.TS,
			Snippet: Snippet(h.Text, terms, snippetRunes),
			Score:   score(h.BM25, h.TS, session.OriginOf(m), now),
		})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	res.Hits = capHits(hits, q.Limit, q.PerSession)
	return res, nil
}

// score turns bm25 (negative, lower is better) into a positive score with the two adjustments.
func score(bm25 float64, ts, origin string, now time.Time) float64 {
	sc := -bm25
	if origin == session.OriginSchedule {
		sc *= scheduleWeight
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		if days := now.Sub(t).Hours() / 24; days > 0 {
			sc *= math.Pow(0.5, days/recencyHalfLifeDays)
		}
	}
	return sc
}

// capHits keeps at most perSession hits from any one session and limit overall, in order. Without
// the per-session cap one long conversation about the topic fills the whole answer and hides the
// other sessions that touched it.
func capHits(hits []Hit, limit, perSession int) []Hit {
	out := []Hit{}
	per := map[string]int{}
	for _, h := range hits {
		if per[h.Session] >= perSession {
			continue
		}
		per[h.Session]++
		out = append(out, h)
		if len(out) == limit {
			break
		}
	}
	return out
}

// Snippet is about n runes of text around the first place a term occurs, whitespace collapsed
// and width-folded the way the index folds it (so "ﾊﾞｸﾞ" finds "バグ"), with "…" where it was cut. With no term found (a match through diacritics or a prefix the
// plain comparison misses) it is the head of the text.
func Snippet(text string, terms []string, n int) string {
	rs := []rune(strings.Join(strings.Fields(fold(text)), " "))
	low := make([]rune, len(rs))
	for i, r := range rs {
		low[i] = unicode.ToLower(r)
	}
	at := -1
	for _, t := range terms {
		tr := []rune(fold(strings.TrimRight(t, "*")))
		for i := range tr {
			tr[i] = unicode.ToLower(tr[i])
		}
		if len(tr) == 0 {
			continue
		}
		if i := runeIndex(low, tr); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	start := 0
	if at > n/4 {
		start = at - n/4
	}
	end := min(start+n, len(rs))
	if end-start < n {
		start = max(0, end-n)
	}
	out := string(rs[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(rs) {
		out += "…"
	}
	return out
}

func runeIndex(hay, needle []rune) int {
outer:
	for i := 0; i+len(needle) <= len(hay); i++ {
		for j, r := range needle {
			if hay[i+j] != r {
				continue outer
			}
		}
		return i
	}
	return -1
}

// Window is GET /session-search/turns's answer: the indexed turns around one hit.
type Window struct {
	Session string `json:"session"`
	Display string `json:"display"`
	Turns   []Doc  `json:"turns"`
	// Clipped says turn text was cut to stay under the size cap.
	Clipped bool `json:"clipped,omitempty"`
}

// ReadWindow returns up to before/after indexed turns around transcript index idx of a session
// that still exists. Only indexed text comes back — the same conversation-only view the search
// matched, never tool output.
func ReadWindow(name string, idx, before, after int) (Window, error) {
	var m session.Meta
	found := false
	for _, x := range listMetas() {
		if x.Name == name && canTranscript(x) {
			m, found = x, true
			break
		}
	}
	if !found {
		return Window{}, errNoSession
	}
	s, err := openStore()
	if err != nil {
		return Window{}, err
	}
	docs, err := s.window(name, idx, clampInt(before, 0, maxWindow), clampInt(after, 0, maxWindow))
	if err != nil {
		return Window{}, err
	}
	w := Window{Session: name, Display: session.Display(m), Turns: []Doc{}}
	budget := maxWindowBytes
	for _, d := range docs {
		if budget <= 0 {
			w.Clipped = true
			break
		}
		if len(d.Text) > budget {
			d.Text, w.Clipped = clip(d.Text, budget)+"…", true
		}
		budget -= len(d.Text)
		w.Turns = append(w.Turns, d)
	}
	return w, nil
}

var errNoSession = errors.New("no such session")

func clampInt(v, lo, hi int) int { return max(lo, min(hi, v)) }

func intParam(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil {
		return v
	}
	return def
}

// allowFrom applies the user's switch to a call made on a session's behalf. `from` is the
// attribution the af MCP server fills from its own session binding, the same convention as the
// peer peek (sessionx/session_peek.go): the Console's own calls carry none and are never refused,
// because the switch governs what sessions may do, not what the member may.
func allowFrom(w http.ResponseWriter, r *http.Request) (string, bool) {
	from := r.URL.Query().Get("from")
	if from == "" {
		return "", true
	}
	if !session.ValidName(from) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_from", "invalid session name in from")
		return "", false
	}
	if !uiprefs.SessionSearch() {
		httpx.WriteErr(w, http.StatusForbidden, "session_search_disabled",
			"past-session search is turned off for sessions in Settings")
		return "", false
	}
	return from, true
}

// HandleSearch is GET /session-search?q=…[&limit][&per_session][&session][&kind][&repo][&refresh=1].
func HandleSearch(w http.ResponseWriter, r *http.Request) {
	from, ok := allowFrom(w, r)
	if !ok {
		return
	}
	qv := r.URL.Query()
	q := Query{
		Q:          qv.Get("q"),
		Limit:      clampInt(intParam(r, "limit", defaultLimit), 1, maxLimit),
		PerSession: clampInt(intParam(r, "per_session", defaultPerSession), 1, maxLimit),
		Session:    qv.Get("session"),
		Kind:       qv.Get("kind"),
		Repo:       qv.Get("repo"),
	}
	if q.Session != "" && !session.ValidName(q.Session) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	indexing := Kick(qv.Get("refresh") == "1")
	res, err := Search(q)
	if errors.Is(err, errEmptyQuery) {
		httpx.WriteErr(w, http.StatusBadRequest, "empty_query", "q has no searchable terms")
		return
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "search_failed", err.Error())
		return
	}
	res.Indexing = indexing || passRunning.Load()
	if from != "" {
		// Audited without the query text: what a session searched for can itself be sensitive.
		log.Printf("session-search: %s searched (%d hits)", from, len(res.Hits))
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// HandleWindow is GET /session-search/turns?session=…&idx=…[&before][&after].
func HandleWindow(w http.ResponseWriter, r *http.Request) {
	from, ok := allowFrom(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("session")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	win, err := ReadWindow(name, intParam(r, "idx", 0), intParam(r, "before", 2), intParam(r, "after", 2))
	if errors.Is(err, errNoSession) {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	if from != "" {
		log.Printf("session-search: %s read %s around %d", from, name, intParam(r, "idx", 0))
	}
	httpx.WriteJSON(w, http.StatusOK, win)
}
