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

func TestEgressBatcherConcurrentAddAndFlush(t *testing.T) {
	f := &fakeIngest{script: []string{"500", "500", "drop"}, got: map[string]int{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	b := newEgressBatcher(srv.URL, "")
	const workers, each = 8, 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				b.add("c.example", true)
			}
		}()
	}
	for i := 0; i < 6; i++ {
		b.flush()
	}
	wg.Wait()
	for i := 0; i < 4; i++ { // outlasts whatever failures the loop above did not consume
		b.flush()
	}
	if f.got["c.example/true"] != workers*each {
		t.Fatalf("want %d got %d", workers*each, f.got["c.example/true"])
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
