package branchpr

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// answer is a GraphQL body for one aliased repository.
func answer(alias, owner, defBranch string, nodes string) string {
	return `"` + alias + `":{"owner":{"login":"` + owner + `"},"defaultBranchRef":{"name":"` + defBranch +
		`"},"pullRequests":{"nodes":[` + nodes + `]}}`
}

func prNode(number int, state string, draft bool, headOwner, rollup string) string {
	r := "null"
	if rollup != "" {
		r = `{"state":"` + rollup + `"}`
	}
	d := "false"
	if draft {
		d = "true"
	}
	return `{"number":` + itoa(number) + `,"state":"` + state + `","isDraft":` + d +
		`,"url":"https://github.com/o/r/pull/` + itoa(number) + `","headRepositoryOwner":{"login":"` + headOwner +
		`"},"commits":{"nodes":[{"commit":{"statusCheckRollup":` + r + `}}]}}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestParsePicksTheRepositoryOwnBranchPR(t *testing.T) {
	keys := []Key{
		{Repo: "o/r", Branch: "feature/a"},
		{Repo: "o/r", Branch: "develop"},
		{Repo: "o/r", Branch: "fix/b"},
		{Repo: "o/gone", Branch: "x"},
		{Repo: "o/r", Branch: "none"},
	}
	body := `{"data":{` +
		// The newest PR is a fork's that reuses the branch name; the repository's own is next.
		answer("r0", "o", "develop", prNode(9, "OPEN", false, "someone", "FAILURE")+","+prNode(7, "OPEN", true, "O", "EXPECTED")) + "," +
		// A session on the default branch: the release PR whose head it is must not show.
		answer("r1", "o", "develop", prNode(8, "OPEN", false, "o", "SUCCESS")) + "," +
		answer("r2", "o", "develop", prNode(5, "MERGED", false, "o", "")) + "," +
		`"r3":null,` +
		answer("r4", "o", "develop", "") +
		`},"errors":[{"type":"NOT_FOUND","path":["r3"]}]}`
	got, failed, limited, err := parse([]byte(body), keys)
	if limited || err != nil || len(failed) != 0 {
		t.Fatalf("limited=%v err=%v failed=%v for a NOT_FOUND error", limited, err, failed)
	}
	if pr := got[keys[0]]; pr == nil || pr.Number != 7 || pr.State != "open" || !pr.Draft || pr.Checks != "pending" {
		t.Errorf("feature/a = %+v, want #7 open draft pending (the fork's #9 skipped)", pr)
	}
	if pr := got[keys[1]]; pr != nil {
		t.Errorf("develop = %+v, want none: it is the default branch", pr)
	}
	if pr := got[keys[2]]; pr == nil || pr.Number != 5 || pr.State != "merged" || pr.Checks != "" {
		t.Errorf("fix/b = %+v, want #5 merged with no checks", pr)
	}
	if _, ok := got[keys[3]]; ok {
		t.Errorf("unresolvable repository answered %+v, want none", got[keys[3]])
	}
	if _, ok := got[keys[4]]; ok {
		t.Errorf("branch with no PR answered %+v, want none", got[keys[4]])
	}
}

func TestParseReportsTheGraphQLRateLimit(t *testing.T) {
	if _, _, limited, _ := parse([]byte(`{"data":null,"errors":[{"type":"RATE_LIMITED"}]}`), []Key{{"o/r", "b"}}); !limited {
		t.Fatal("limited = false for RATE_LIMITED")
	}
}

func TestQueryPassesValuesAsVariables(t *testing.T) {
	q, vars := query([]Key{{Repo: "o/r", Branch: `x"){evil}`}})
	if strings.Contains(q, "evil") {
		t.Fatalf("branch name spliced into the query: %s", q)
	}
	if vars["o0"] != "o" || vars["n0"] != "r" || vars["b0"] != `x"){evil}` {
		t.Fatalf("vars = %v", vars)
	}
}

// server answers every call with one open PR per asked repository and counts the calls.
func server(t *testing.T, calls *atomic.Int32, status int, hdr map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		var in struct {
			Variables map[string]any `json:"variables"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		var parts []string
		if body := os.Getenv("BRANCHPR_TEST_BODY"); body != "" {
			_, _ = io.WriteString(w, body)
			return
		}
		for i := 0; ; i++ {
			if _, ok := in.Variables["o"+itoa(i)]; !ok {
				break
			}
			parts = append(parts, answer("r"+itoa(i), "o", "main", prNode(100+i, "OPEN", false, "o", "SUCCESS")))
		}
		_, _ = io.WriteString(w, `{"data":{`+strings.Join(parts, ",")+`}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestCache(url, token string) *Cache {
	c := New(func() (string, error) { return token, nil })
	c.Endpoint = url
	c.Async = func(f func()) { f() }
	return c
}

func TestLookupNeverWaitsAndBatchesOneCall(t *testing.T) {
	var calls atomic.Int32
	srv := server(t, &calls, http.StatusOK, nil)
	c := newTestCache(srv.URL, "tok")
	keys := []Key{{"o/r", "a"}, {"o/r", "b"}, {"o/r", "a"}}
	now := time.Now()
	// The first lookup has nothing to answer yet; it starts the refresh.
	if got := c.Lookup(keys, now); len(got) != 0 {
		t.Fatalf("first Lookup = %v, want empty", got)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("calls = %d, want one batched call", n)
	}
	got := c.Lookup(keys, now.Add(time.Second))
	if got[keys[0]] == nil || got[keys[1]] == nil || got[keys[0]].Checks != "success" {
		t.Fatalf("second Lookup = %v, want both answered", got)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("calls = %d after a fresh lookup, want still 1", n)
	}
	// An open PR is re-read once openTTL has passed: a merge shows within it.
	c.Lookup(keys, now.Add(openTTL+time.Second))
	if n := calls.Load(); n != 2 {
		t.Fatalf("calls = %d after openTTL, want 2", n)
	}
}

func TestLookupHoldsOffAfterTheRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := server(t, &calls, http.StatusForbidden, map[string]string{"Retry-After": "600"})
	c := newTestCache(srv.URL, "tok")
	keys := []Key{{"o/r", "a"}}
	c.Lookup(keys, time.Now())
	c.Lookup(keys, time.Now())
	if n := calls.Load(); n != 1 {
		t.Fatalf("calls = %d, want 1: the second lookup must wait out Retry-After", n)
	}
}

func TestLookupWithoutATokenAsksNothing(t *testing.T) {
	var calls atomic.Int32
	srv := server(t, &calls, http.StatusOK, nil)
	c := newTestCache(srv.URL, "")
	if got := c.Lookup([]Key{{"o/r", "a"}}, time.Now()); len(got) != 0 {
		t.Fatalf("Lookup = %v", got)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("calls = %d without a GitHub connection, want 0", n)
	}
}

func TestRateWait(t *testing.T) {
	now := time.Unix(1000, 0)
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "0")
	h.Set("X-RateLimit-Reset", "1300")
	if d := rateWait(h, now); d != 301*time.Second {
		t.Errorf("reset wait = %v", d)
	}
	if d := rateWait(http.Header{}, now); d != rateBackoff {
		t.Errorf("default wait = %v", d)
	}
}

func TestParseFailsAliasesWithAnErrorOtherThanNotFound(t *testing.T) {
	keys := []Key{{"o/r", "a"}, {"o/r", "b"}}
	body := `{"data":{` + answer("r0", "o", "main", prNode(3, "OPEN", false, "o", "SUCCESS")) +
		`,"r1":null},"errors":[{"type":"INTERNAL","path":["r1","pullRequests"]}]}`
	got, failed, _, err := parse([]byte(body), keys)
	if err != nil || got[keys[0]] == nil || !failed[keys[1]] || failed[keys[0]] {
		t.Fatalf("got=%v failed=%v err=%v, want r0 answered and r1 failed", got, failed, err)
	}
	for _, b := range []string{
		`{"data":null,"errors":[{"type":"INTERNAL","message":"service unavailable"}]}`,
		`{"data":{"r0":{"owner":{"login":"o"},"pullReq`, // cut off mid-answer
		`{"data":{},"errors":[{"type":"SERVICE_UNAVAILABLE"}]}`,
		`{"data":null}`,
	} {
		if _, _, _, err := parse([]byte(b), keys); err == nil {
			t.Errorf("parse(%s) err = nil, want a failure", b)
		}
	}
}

// seed answers keys once from the stub server and returns the cache.
func seeded(t *testing.T, keys []Key) (*Cache, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := server(t, &calls, http.StatusOK, nil)
	c := newTestCache(srv.URL, "tok")
	c.Lookup(keys, time.Now())
	if got := c.Lookup(keys, time.Now()); got[keys[0]] == nil {
		t.Fatalf("seed: %v", got)
	}
	return c, &calls
}

func TestATransientGraphQLFailureKeepsTheLastAnswer(t *testing.T) {
	keys := []Key{{"o/r", "a"}}
	for _, body := range []string{
		`{"data":null,"errors":[{"type":"INTERNAL","message":"service unavailable"}]}`,
		`{"data":{"r0":{"owner":`,
		`{"data":{"r0":null},"errors":[{"type":"INTERNAL","path":["r0"]}]}`,
	} {
		c, calls := seeded(t, keys)
		t.Setenv("BRANCHPR_TEST_BODY", body)
		later := time.Now().Add(openTTL + time.Second)
		c.Lookup(keys, later) // refresh: fails
		if got := c.Lookup(keys, later); got[keys[0]] == nil || got[keys[0]].Number != 100 {
			t.Errorf("%s: after the failed refresh Lookup = %v, want the PR kept", body, got)
		}
		n := calls.Load()
		c.Lookup(keys, later.Add(time.Second))
		if calls.Load() != n {
			t.Errorf("%s: asked again at once, want failBackoff", body)
		}
		os.Unsetenv("BRANCHPR_TEST_BODY")
	}
}

func TestDisconnectingGitHubDropsTheHeldAnswers(t *testing.T) {
	keys := []Key{{"o/r", "a"}}
	c, _ := seeded(t, keys)
	c.Token = func() (string, error) { return "", nil }
	later := time.Now().Add(openTTL + time.Second)
	c.Lookup(keys, later)
	if got := c.Lookup(keys, later); len(got) != 0 {
		t.Fatalf("Lookup after disconnecting = %v, want nothing", got)
	}
}

func TestAStoreReadErrorKeepsTheHeldAnswers(t *testing.T) {
	keys := []Key{{"o/r", "a"}}
	c, _ := seeded(t, keys)
	c.Token = func() (string, error) { return "", errors.New("locked") }
	later := time.Now().Add(openTTL + time.Second)
	c.Lookup(keys, later)
	if got := c.Lookup(keys, later); got[keys[0]] == nil {
		t.Fatalf("Lookup after an unreadable store = %v, want the PR kept", got)
	}
}

func TestLookupAsksOnceForAKeyManySessionsShare(t *testing.T) {
	var calls atomic.Int32
	var aliases atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in struct {
			Variables map[string]any `json:"variables"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		aliases.Add(int32(len(in.Variables) / 3))
		_, _ = io.WriteString(w, `{"data":{`+answer("r0", "o", "main", prNode(1, "OPEN", false, "o", ""))+`}}`)
	}))
	t.Cleanup(srv.Close)
	c := newTestCache(srv.URL, "tok")
	keys := make([]Key, batchSize+1)
	for i := range keys {
		keys[i] = Key{"o/r", "shared"}
	}
	c.Lookup(keys, time.Now())
	if calls.Load() != 1 || aliases.Load() != 1 {
		t.Fatalf("calls=%d aliases=%d, want one call with one alias", calls.Load(), aliases.Load())
	}
}

// A 401 for a token whose recorded expiry is still in the future (clock skew, or a renewal
// by another process) is renewed once and the batch asked again.
func TestA401RenewsTheTokenAndRetriesTheBatchOnce(t *testing.T) {
	var calls atomic.Int32
	var seen []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer old" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"r0":{"owner":{"login":"o"},"defaultBranchRef":{"name":"main"},"pullRequests":{"nodes":[]}}}}`))
	}))
	t.Cleanup(srv.Close)
	c := newTestCache(srv.URL, "old")
	renewals := 0
	c.Renew = func(rejected string) (string, error) {
		renewals++
		if rejected != "old" {
			t.Errorf("rejected = %q", rejected)
		}
		return "new", nil
	}
	c.Lookup([]Key{{"o/r", "a"}}, time.Now())
	if renewals != 1 || calls.Load() != 2 || strings.Join(seen, ",") != "Bearer old,Bearer new" {
		t.Fatalf("renewals=%d calls=%d seen=%v", renewals, calls.Load(), seen)
	}
}

func TestA401WithoutRenewAsksOnce(t *testing.T) {
	var calls atomic.Int32
	srv := server(t, &calls, http.StatusUnauthorized, nil)
	c := newTestCache(srv.URL, "tok")
	c.Lookup([]Key{{"o/r", "a"}}, time.Now())
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}
