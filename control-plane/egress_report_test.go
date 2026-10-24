package main

// Egress report delivery (#2603): the proxy keeps a batch until the CP answers 2xx, bounds
// what it holds while the CP is away, and the CP answers 5xx when it could not record.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// fakeIngest answers each POST per script: "drop" kills the connection (transport error),
// "500" answers a server error, anything else answers 204 and records the events.
type fakeIngest struct {
	mu      sync.Mutex
	script  []string
	got     map[string]int
	dropped int
}

func (f *fakeIngest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mode := "ok"
	if len(f.script) > 0 {
		mode, f.script = f.script[0], f.script[1:]
	}
	switch mode {
	case "drop":
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
		return
	case "500":
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	var in egressIngest
	_ = json.NewDecoder(r.Body).Decode(&in)
	for _, e := range in.Events {
		f.got[e.Host+"/"+strconv.FormatBool(e.Allowed)] += e.Count
	}
	f.dropped += in.Dropped
	w.WriteHeader(http.StatusNoContent)
}

func TestEgressBatcherKeepsBatchUntilCPAnswers2xx(t *testing.T) {
	f := &fakeIngest{script: []string{"drop", "500", "ok"}, got: map[string]int{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	b := newEgressBatcher(srv.URL, "tok")

	b.add("a.example", true)
	b.add("a.example", true)
	b.add("paste.ee", false)
	b.flush() // transport error
	b.add("a.example", true)
	b.flush() // 500
	b.add("new.example", false)
	if len(f.got) != 0 {
		t.Fatalf("nothing may be accepted before the CP recovers: %v", f.got)
	}
	b.flush() // 204
	b.flush() // nothing pending: no POST
	want := map[string]int{"a.example/true": 3, "paste.ee/false": 1, "new.example/false": 1}
	for k, n := range want {
		if f.got[k] != n {
			t.Fatalf("%s: want %d got %d (all: %v)", k, n, f.got[k], f.got)
		}
	}
	if len(f.got) != len(want) {
		t.Fatalf("unexpected buckets: %v", f.got)
	}
}

func TestEgressBatcherCapCountsDroppedAndReportsOnce(t *testing.T) {
	f := &fakeIngest{script: []string{"500"}, got: map[string]int{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	b := newEgressBatcher(srv.URL, "")

	for i := 0; i < maxEgressKeys+7; i++ {
		b.add("h"+strconv.Itoa(i)+".example", true)
	}
	b.add("h0.example", true) // an existing bucket still grows past the cap
	b.flush()                 // 500: everything is merged back, still capped
	b.add("late.example", true)
	b.mu.Lock()
	keys, dropped := len(b.counts), b.dropped
	b.mu.Unlock()
	if keys != maxEgressKeys || dropped != 8 {
		t.Fatalf("after failed flush: keys=%d (want %d) dropped=%d (want 8)", keys, maxEgressKeys, dropped)
	}
	b.flush()
	if f.dropped != 8 || f.got["h0.example/true"] != 2 || len(f.got) != maxEgressKeys {
		t.Fatalf("reported: dropped=%d h0=%d buckets=%d", f.dropped, f.got["h0.example/true"], len(f.got))
	}
	b.flush()
	if f.dropped != 8 {
		t.Fatalf("dropped must be reported once, got %d", f.dropped)
	}
}

// A failing POST must merge back with whatever add() collected while it was in flight:
// same-key counts add up, a new key past the cap becomes dropped, and the batch's own
// dropped is not lost. The handler is held on a barrier so the interleaving is fixed.
func TestEgressBatcherAddDuringFailingPost(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	got, dropped, calls := map[string]int{}, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			close(entered)
			<-release
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var in egressIngest
		_ = json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		for _, e := range in.Events {
			got[e.Host] += e.Count
		}
		dropped += in.Dropped
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	// Registered after srv.Close's defer so it runs first: a failing assertion below must
	// still free the held handler, or srv.Close would wait on it and hang the test.
	defer srv.Close()
	var relOnce sync.Once
	releaseAll := func() { relOnce.Do(func() { close(release) }) }
	defer releaseAll()
	b := newEgressBatcher(srv.URL, "")

	for i := 0; i < maxEgressKeys; i++ { // fill the cap
		b.add("h"+strconv.Itoa(i), true)
	}
	b.add("overflow-before", true) // dropped = 1 inside the in-flight batch
	done := make(chan struct{})
	go func() { b.flush(); close(done) }()
	<-entered
	// The window is empty now; fill it again to the cap, then go over it.
	b.add("h0", true)
	b.add("h0", true)
	for i := 0; i < maxEgressKeys; i++ {
		b.add("n"+strconv.Itoa(i), true)
	}
	b.add("overflow-after", true) // dropped = 1 more, outside the in-flight batch
	// A second flush must wait for the first to finish rather than POST concurrently.
	second := make(chan struct{})
	go func() { b.flush(); close(second) }()
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	inFlight := calls
	mu.Unlock()
	secondRan := false
	select {
	case <-second:
		secondRan = true
	default:
	}
	releaseAll()
	<-done
	<-second
	if secondRan || inFlight != 1 {
		t.Fatalf("second flush ran while the first POST was in flight (returned=%v, calls=%d)", secondRan, inFlight)
	}
	b.flush()
	b.flush()

	mu.Lock()
	defer mu.Unlock()
	total := 0
	for _, n := range got {
		total += n
	}
	// 5000 + 2 (h0 twice) + 5000 observed; 5000 old + 5000 new keys exceed the cap by
	// 5000 keys, which are folded into dropped on restore.
	if total+dropped != maxEgressKeys+1+2+maxEgressKeys+1 {
		t.Fatalf("every observation is either delivered or counted as dropped: delivered=%d dropped=%d", total, dropped)
	}
	if got["h0"] != 3 {
		t.Fatalf("same-key counts must add up across the restore: h0=%d", got["h0"])
	}
	if dropped < 2 {
		t.Fatalf("both overflow observations must be counted: dropped=%d", dropped)
	}
}

// After a long outage the pending batch is as large as the cap allows; it must still fit
// the CP's 1 MiB body limit, in pieces, or it would be refused for ever.
func TestEgressBatcherDeliversMaxSizedBacklogUnderBodyLimit(t *testing.T) {
	var mu sync.Mutex
	up := false
	got, dropped := map[string]int{}, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !up {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var in egressIngest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		for _, e := range in.Events {
			got[e.Host] += e.Count
		}
		dropped += in.Dropped
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	b := newEgressBatcher(srv.URL, "")

	pad := strings.Repeat("a", 240)
	for i := 0; i < maxEgressKeys+3; i++ {
		b.add(pad+strconv.Itoa(1000+i)+".example", true) // 248 characters each
	}
	b.add(strings.Repeat("x", 300), true) // not a hostname: dropped, never sent
	b.flush()                             // outage
	mu.Lock()
	up = true
	mu.Unlock()
	b.flush()
	b.flush()
	mu.Lock()
	defer mu.Unlock()
	if len(got) != maxEgressKeys || dropped != 4 {
		t.Fatalf("delivered %d buckets (want %d), dropped=%d (want 4)", len(got), maxEgressKeys, dropped)
	}
}

// failingEgressStore fails RecordEgress (and optionally the audit insert) on demand.
type failingEgressStore struct {
	egressStore
	recordErr, auditErr error
	recorded            int
}

func (s *failingEgressStore) RecordEgress(ctx context.Context, day, host string, allowed bool, n int) error {
	if s.recordErr != nil {
		return s.recordErr
	}
	s.recorded++
	return s.egressStore.RecordEgress(ctx, day, host, allowed, n)
}

func (s *failingEgressStore) InsertAudit(ctx context.Context, a store.AuditLog) error {
	if s.auditErr != nil {
		return s.auditErr
	}
	return s.egressStore.InsertAudit(ctx, a)
}

func ingestWith(t *testing.T, eg egressAPI, body string) int {
	t.Helper()
	r := httptest.NewRequest("POST", "/internal/egress", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	eg.ingest(w, r)
	return w.Code
}

func TestEgressIngestStoreFailureAnswers5xx(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	fs := &failingEgressStore{egressStore: st, recordErr: errors.New("db down")}
	eg := egressAPI{token: "tok", dedup: &egressAuditDedup{}, store: fs}
	body := `{"events":[{"host":"a.example","allowed":true,"count":2}]}`

	if code := ingestWith(t, eg, body); code != http.StatusInternalServerError {
		t.Fatalf("store failure: want 500 got %d", code)
	}

	// Recovered store: the same batch is accepted and counted.
	fs.recordErr = nil
	if code := ingestWith(t, eg, body); code != http.StatusNoContent {
		t.Fatalf("recovered: want 204 got %d", code)
	}
	rows, _ := st.ListEgress(ctx, "2000-01-01", 10)
	if len(rows) != 1 || rows[0].Allowed != 2 {
		t.Fatalf("rows: %+v", rows)
	}

	// A malformed row is skipped and a failing audit insert is best-effort: both stay 204.
	fs.auditErr = errors.New("audit down")
	mixed := `{"events":[{"host":"","allowed":true,"count":1},{"host":"b.example","allowed":true,"count":0},` +
		`{"host":"paste.ee","allowed":false,"count":1}],"dropped":3}`
	if code := ingestWith(t, eg, mixed); code != http.StatusNoContent {
		t.Fatalf("skipped row / audit failure: want 204 got %d", code)
	}
	if fs.recorded != 2 {
		t.Fatalf("only the valid rows are recorded: %d", fs.recorded)
	}
}

func TestEgressIngestAuditsDropped(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	eg := egressAPI{token: "tok", store: st}
	if code := ingestWith(t, eg, `{"events":[],"dropped":12}`); code != http.StatusNoContent {
		t.Fatalf("want 204 got %d", code)
	}
	al, _ := st.ListAuditByTenant(ctx, "", 100)
	for _, a := range al {
		if a.Action == "egress.dropped" && strings.HasPrefix(a.Detail, "12 ") {
			return
		}
	}
	t.Fatalf("egress.dropped audit row missing: %+v", al)
}

// '&' is a legal host character that json.Marshal escapes to \u0026, so budgeting by raw
// host length would build chunks over the CP's body limit.
func TestEgressBatcherChunksByEscapedSize(t *testing.T) {
	var evs []egressEvent
	for i := 0; i < maxEgressKeys; i++ {
		evs = append(evs, egressEvent{Host: strings.Repeat("&", 248) + strconv.Itoa(1000+i), Allowed: true, Count: 1})
	}
	for i, c := range splitEgressEvents(evs) {
		body, _ := json.Marshal(egressIngest{Events: c, Dropped: 1 << 30})
		if len(body) > 1<<20 {
			t.Fatalf("chunk %d marshals to %d bytes, over the CP limit", i, len(body))
		}
	}
}

// First chunk accepted, second refused: the accepted chunk (and the dropped count riding
// on it) must not be sent again, and everything else must arrive on the retry.
func TestEgressBatcherPartialChunkFailureRetriesOnlyTheRest(t *testing.T) {
	var mu sync.Mutex
	calls, dropped := 0, 0
	got := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var in egressIngest
		_ = json.NewDecoder(r.Body).Decode(&in)
		for _, e := range in.Events {
			got[e.Host] += e.Count
		}
		dropped += in.Dropped
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	b := newEgressBatcher(srv.URL, "")
	pad := strings.Repeat("a", 240)
	const n = 3000 // ~3 chunks
	for i := 0; i < n; i++ {
		b.add(pad+strconv.Itoa(1000+i), true)
	}
	b.mu.Lock()
	b.dropped = 5
	b.mu.Unlock()
	b.flush() // chunk 1 ok, chunk 2 fails
	b.flush()
	b.flush()
	mu.Lock()
	defer mu.Unlock()
	if len(got) != n || dropped != 5 {
		t.Fatalf("buckets=%d (want %d) dropped=%d (want 5)", len(got), n, dropped)
	}
	for h, c := range got {
		if c != 1 {
			t.Fatalf("%s delivered %d times", h[len(h)-4:], c)
		}
	}
}
