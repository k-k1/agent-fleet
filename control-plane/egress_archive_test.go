package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// memSink is an in-memory sink; failPut makes every Put fail the way an unreachable bucket would.
type memSink struct {
	objs    map[string][]byte
	failPut error
	puts    int
}

func newMemSink() *memSink { return &memSink{objs: map[string][]byte{}} }

func (m *memSink) Exists(_ context.Context, k string) (bool, error) {
	_, ok := m.objs[k]
	return ok, nil
}
func (m *memSink) Put(_ context.Context, k string, b io.ReadSeeker, size int64) error {
	m.puts++
	if m.failPut != nil {
		return m.failPut
	}
	data, err := io.ReadAll(b)
	if err != nil || int64(len(data)) != size {
		return errors.New("short body")
	}
	m.objs[k] = data
	return nil
}

// readArchive decodes an object into its lines by type.
func readArchive(t *testing.T, data []byte) map[string][]map[string]any {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]map[string]any{}
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		out[m["type"].(string)] = append(out[m["type"].(string)], m)
	}
	return out
}

func TestEgressArchiveLocalSink(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	l, err := newEgressArchiveLocal("", root)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := l.Exists(ctx, "a/b/x.gz"); ok || err != nil {
		t.Fatalf("missing object: %v %v", ok, err)
	}
	if err := l.Put(ctx, "a/b/x.gz", strings.NewReader("hello"), 5); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a/b/x.gz")); string(b) != "hello" {
		t.Errorf("stored %q", b)
	}
	if ok, _ := l.Exists(ctx, "a/b/x.gz"); !ok {
		t.Error("stored object not found")
	}
	// A failed Put leaves neither the key nor a temp file, and keeps the old object.
	if err := l.Put(ctx, "a/b/x.gz", strings.NewReader("hel"), 5); err == nil {
		t.Fatal("a body shorter than size must fail")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a/b/x.gz")); string(b) != "hello" {
		t.Errorf("failed Put changed the object: %q", b)
	}
	if err := l.Put(ctx, "a/c/y.gz", strings.NewReader("hel"), 5); err == nil {
		t.Fatal("short body")
	}
	ents, _ := os.ReadDir(filepath.Join(root, "a/c"))
	if len(ents) != 0 {
		t.Errorf("temp file left behind: %v", ents)
	}
	if err := l.Put(ctx, "../escape", strings.NewReader("x"), 1); err == nil {
		t.Error("key leaving the root must be refused")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := l.Put(cctx, "z", strings.NewReader("x"), 1); err == nil {
		t.Error("a cancelled context must fail the Put")
	}
	if _, err := newEgressArchiveLocal("", "relative/dir"); err == nil {
		t.Error("relative path must be refused")
	}
}

type fakeArchiveS3 struct {
	objs    map[string][]byte
	putErr  error
	lastPut *s3.PutObjectInput
	// shrink makes the bucket report one byte less than was sent.
	shrink bool
}

func (f *fakeArchiveS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.lastPut = in
	if f.putErr != nil {
		return nil, f.putErr
	}
	b, _ := io.ReadAll(in.Body)
	if f.objs == nil {
		f.objs = map[string][]byte{}
	}
	f.objs[aws.ToString(in.Key)] = b
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeArchiveS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	b, ok := f.objs[aws.ToString(in.Key)]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NotFound", Message: "no"}
	}
	n := int64(len(b))
	if f.shrink {
		n--
	}
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(n)}, nil
}

func TestEgressArchiveS3Sink(t *testing.T) {
	ctx := context.Background()
	api := &fakeArchiveS3{}
	s := newEgressArchiveS3(api, "bkt", "/pre/fix/")
	if ok, err := s.Exists(ctx, "k"); ok || err != nil {
		t.Fatalf("missing: %v %v", ok, err)
	}
	if err := s.Put(ctx, "egress/k", strings.NewReader("data"), 4); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(api.lastPut.Bucket) != "bkt" || aws.ToString(api.lastPut.Key) != "pre/fix/egress/k" {
		t.Errorf("bucket/key = %s/%s", aws.ToString(api.lastPut.Bucket), aws.ToString(api.lastPut.Key))
	}
	if ok, _ := s.Exists(ctx, "egress/k"); !ok {
		t.Error("stored object not found")
	}
	api.putErr = &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}
	if err := s.Put(ctx, "egress/j", strings.NewReader("data"), 4); err == nil {
		t.Error("a refused PutObject must fail")
	}
	// A 2xx whose stored length differs is not a confirmation.
	api.putErr, api.shrink = nil, true
	if err := s.Put(ctx, "egress/m", strings.NewReader("data"), 4); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("unconfirmed put = %v", err)
	}
	// An Exists that cannot ask is an error, not "absent": absent would authorise a write.
	api2 := &fakeArchiveS3{}
	if _, err := newEgressArchiveS3(errHead{api2}, "b", "").Exists(ctx, "k"); err == nil {
		t.Error("Exists must report a failed HeadObject")
	}
}

type errHead struct{ *fakeArchiveS3 }

func (errHead) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return nil, &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}
}

func TestNewEgressArchiveSinkRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"gs://b/p", "s3:///p", "file://host/x", "file:///", "/plain/path", "ftp://x"} {
		if u == "file:///" {
			continue // the filesystem root is absolute, so it parses; operators choose their directory
		}
		if _, err := newEgressArchiveSink(context.Background(), u); err == nil {
			t.Errorf("%q must be rejected", u)
		}
	}
	if s, err := newEgressArchiveSink(context.Background(), "file://"+t.TempDir()); err != nil || s == nil {
		t.Errorf("file url: %v", err)
	}
}

// failingArchiveSink wraps memSink and fails Put for a single day's key.
type failDay struct {
	*memSink
	key string
}

func (f failDay) Put(ctx context.Context, k string, b io.ReadSeeker, n int64) error {
	if k == f.key {
		return errors.New("bucket unreachable")
	}
	return f.memSink.Put(ctx, k, b, n)
}

// archiveDialects runs f on SQLite and, when AF_TEST_DATABASE_URL is set, Postgres.
func archiveDialects(t *testing.T, f func(t *testing.T, st *store.SQL)) {
	t.Run("sqlite", func(t *testing.T) { f(t, openLimitStore(t)) })
	t.Run("postgres", func(t *testing.T) {
		url, ok := pgtest.Schema(t)
		if !ok {
			t.Skip("set AF_TEST_DATABASE_URL to run the Postgres half")
		}
		st, err := store.OpenPostgres(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		f(t, st)
	})
}

var archiveNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func archivePurger(st *store.SQL, sink egressArchiveSink) *egressPurger {
	p := &egressPurger{st: st, leases: st, retention: 30 * 24 * time.Hour, interval: time.Minute,
		batch: 2, pause: 0, holder: "h", now: func() time.Time { return archiveNow }}
	if sink != nil {
		p.archive = newEgressArchiver(st, sink)
		p.archive.batch = 2
	}
	return p
}

// seedArchive stores events on 2026-09-20 (two), 2026-09-21 (one), and on the day the cutoff
// crosses, 2026-09-25 (cutoff 2026-09-25T12:00; one before it, one after), plus address rows.
func seedArchive(t *testing.T, st *store.SQL) {
	t.Helper()
	ctx := context.Background()
	ms := func(s string) int64 {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v.UnixMilli()
	}
	for _, e := range []store.EgressEvent{
		{ID: "b", At: ms("2026-09-20T10:00:00Z"), Source: "10.0.1.5", Host: "a.example", Allowed: true, Count: 1},
		{ID: "a", At: ms("2026-09-20T10:00:00Z"), Source: "10.0.1.5", Host: "b.example", Count: 3},
		{ID: "c", At: ms("2026-09-21T23:59:59Z"), Source: "10.0.1.6", Host: "a.example", Allowed: true, Count: 1},
		{ID: "d", At: ms("2026-09-25T06:00:00Z"), Source: "10.0.1.5", Host: "a.example", Allowed: true, Count: 1},
		{ID: "e", At: ms("2026-09-26T06:00:00Z"), Source: "10.0.1.5", Host: "a.example", Allowed: true, Count: 1},
	} {
		if _, err := st.RecordEgressEvent(ctx, "2026-09-01", e, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertWorkspaceAddresses(ctx, "m", []store.WorkspaceAddress{
		{WorkspaceID: "w1", Address: "10.0.1.5", Ref: "r1", From: ms("2026-09-10T00:00:00Z"), Until: ms("2026-09-22T00:00:00Z")},
		{WorkspaceID: "w2", Address: "10.0.1.6", Ref: "r2", From: ms("2026-09-21T00:00:00Z"), Until: ms("2026-09-23T00:00:00Z")},
		{WorkspaceID: "w3", Address: "10.0.1.5", Ref: "r3", From: ms("2026-09-22T00:00:00Z")}, // open
	}); err != nil {
		t.Fatal(err)
	}
}

func remainingEventIDs(t *testing.T, st *store.SQL) map[string]bool {
	t.Helper()
	got, err := st.ListAttributedEgressEvents(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, e := range got {
		ids[e.ID] = true
	}
	return ids
}

// Archive succeeds: every whole day before the cutoff is stored with its events and the
// address rows that attribute them, and only then is the data deleted. The day the cutoff
// crosses is kept whole until it has aged out.
func TestEgressPurgeArchivesThenDeletes(t *testing.T) {
	archiveDialects(t, func(t *testing.T, st *store.SQL) {
		ctx := context.Background()
		seedArchive(t, st)
		sink := newMemSink()
		archivePurger(st, sink).purge(ctx)

		d20 := sink.objs["egress/yyyy=2026/mm=09/dd=20/events-2026-09-20.jsonl.gz"]
		d21 := sink.objs["egress/yyyy=2026/mm=09/dd=21/events-2026-09-21.jsonl.gz"]
		if d20 == nil || d21 == nil {
			t.Fatalf("keys = %v", keysOf(sink.objs))
		}
		a := readArchive(t, d20)
		if len(a["header"]) != 1 || a["header"][0]["day"] != "2026-09-20" || a["header"][0]["schema"] != float64(1) {
			t.Errorf("header = %v", a["header"])
		}
		if ev := a["event"]; len(ev) != 2 || ev[0]["id"] != "a" || ev[1]["id"] != "b" {
			t.Errorf("day 20 events (want a, b in id order) = %v", ev)
		}
		if len(a["address"]) != 1 || a["address"][0]["ref"] != "r1" {
			t.Errorf("day 20 holds only r1: %v", a["address"])
		}
		m := a["manifest"]
		if len(m) != 1 || m[0]["events"] != float64(2) || m[0]["addresses"] != float64(1) || m[0]["first_id"] != "a" || m[0]["last_id"] != "b" {
			t.Errorf("manifest = %v", m)
		}
		b := readArchive(t, d21)
		if len(b["event"]) != 1 || len(b["address"]) != 2 {
			t.Errorf("day 21: %d events, %d addresses (r1 and r2 meet it)", len(b["event"]), len(b["address"]))
		}
		// Day 22 has no events, but r2 (ending at midnight of the 23rd) ends in it: archived, then
		// deleted. r1 ends at midnight of the 22nd, so its last day is the 21st.
		if sink.objs["egress/yyyy=2026/mm=09/dd=22/events-2026-09-22.jsonl.gz"] == nil {
			t.Errorf("address-only days missing: %v", keysOf(sink.objs))
		}
		if sink.objs["egress/yyyy=2026/mm=09/dd=24/events-2026-09-24.jsonl.gz"] != nil {
			t.Error("a day with no event and no ending address must not be written")
		}
		if sink.objs["egress/yyyy=2026/mm=09/dd=23/events-2026-09-23.jsonl.gz"] != nil {
			t.Error("day 23 holds nothing that is deleted and must not be written")
		}
		if len(sink.objs) != 3 {
			t.Errorf("objects = %v", keysOf(sink.objs))
		}
		if ids := remainingEventIDs(t, st); len(ids) != 2 || !ids["d"] || !ids["e"] {
			t.Errorf("events left = %v, want d and e (the crossing day stays whole)", ids)
		}
		if refs, _ := st.OpenWorkspaceAddressRefs(ctx, "m"); len(refs) != 1 {
			t.Errorf("open row must stay: %v", refs)
		}
		if n, _ := st.PurgeWorkspaceAddresses(ctx, archiveNow.UnixMilli(), 10); n != 0 {
			t.Errorf("closed rows must be deleted, %d left", n)
		}
	})
}

// Archive fails for a day: that day and everything after it is kept, earlier days are
// archived and deleted, and the next run (the sink back) completes it.
func TestEgressPurgeKeepsWhatWasNotArchived(t *testing.T) {
	archiveDialects(t, func(t *testing.T, st *store.SQL) {
		ctx := context.Background()
		seedArchive(t, st)
		mem := newMemSink()
		bad := failDay{mem, "egress/yyyy=2026/mm=09/dd=21/events-2026-09-21.jsonl.gz"}
		archivePurger(st, bad).purge(ctx)
		if ids := remainingEventIDs(t, st); len(ids) != 3 || ids["a"] || ids["b"] || !ids["c"] {
			t.Errorf("only day 20 may be gone, left = %v", ids)
		}
		// r1 and r2 end after the failed day began: both must still be there, as the
		// attribution of event c.
		got, _ := st.ListAttributedEgressEvents(ctx, 0, 100)
		for _, e := range got {
			if e.ID == "c" && len(e.Holders) != 1 {
				t.Errorf("event c lost its holder: %+v", e)
			}
		}
		// Whole sink down: nothing is deleted at all.
		st2 := newMemSink()
		st2.failPut = errors.New("down")
		archivePurger(st, st2).purge(ctx)
		if ids := remainingEventIDs(t, st); len(ids) != 3 {
			t.Errorf("left = %v", ids)
		}
		// Recovered: the retry archives the rest and deletes.
		archivePurger(st, mem).purge(ctx)
		if ids := remainingEventIDs(t, st); len(ids) != 2 || !ids["d"] || !ids["e"] {
			t.Errorf("after recovery left = %v", ids)
		}
		if n, _ := st.PurgeWorkspaceAddresses(ctx, archiveNow.UnixMilli(), 10); n != 0 {
			t.Errorf("closed rows must be gone after recovery, %d left", n)
		}
	})
}

// A purge cut off after the object was written, with part of the day deleted, must not
// replace the full object with the remainder when it runs again.
func TestEgressArchiveIsNotRewrittenAfterAPartialDelete(t *testing.T) {
	archiveDialects(t, func(t *testing.T, st *store.SQL) {
		ctx := context.Background()
		seedArchive(t, st)
		sink := newMemSink()
		p := archivePurger(st, sink)
		before := egressDayStart(archiveNow.Add(-p.retention).UnixMilli())
		if _, err := p.archive.archive(ctx, before); err != nil {
			t.Fatal(err)
		}
		key := "egress/yyyy=2026/mm=09/dd=20/events-2026-09-20.jsonl.gz"
		full := append([]byte(nil), sink.objs[key]...)
		// Crash after one batch of the delete: event "a" is gone, "b" remains.
		if n, err := st.PurgeEgressEvents(ctx, before, 1); err != nil || n != 1 {
			t.Fatalf("partial delete %d %v", n, err)
		}
		puts := sink.puts
		p.purge(ctx)
		if !bytes.Equal(sink.objs[key], full) {
			t.Error("the full day object was replaced")
		}
		if sink.puts != puts {
			t.Errorf("re-ran %d puts for days that already exist", sink.puts-puts)
		}
		if ids := remainingEventIDs(t, st); len(ids) != 2 {
			t.Errorf("left = %v", ids)
		}
	})
}

// With the archive off the purge keeps the exact cutoff and writes nothing.
func TestEgressPurgeWithoutArchiveIsUnchanged(t *testing.T) {
	archiveDialects(t, func(t *testing.T, st *store.SQL) {
		seedArchive(t, st)
		archivePurger(st, nil).purge(context.Background())
		if ids := remainingEventIDs(t, st); len(ids) != 1 || !ids["e"] {
			t.Errorf("left = %v, want only e (exact cutoff 2026-09-25T12:00)", ids)
		}
	})
}

// The floor reaches the archived day's end before the archive reads it, so a late event for
// a day being archived is refused instead of being deleted unarchived.
func TestEgressArchiveRaisesTheFloorFirst(t *testing.T) {
	archiveDialects(t, func(t *testing.T, st *store.SQL) {
		ctx := context.Background()
		seedArchive(t, st)
		sink := newMemSink()
		sink.failPut = errors.New("down")
		archivePurger(st, sink).purge(ctx)
		late := store.EgressEvent{ID: "late", At: time.Date(2026, 9, 20, 11, 0, 0, 0, time.UTC).UnixMilli(), Source: "10.0.1.5", Host: "a.example", Count: 1}
		if _, err := st.RecordEgressEvent(ctx, "2026-09-20", late, 1); !errors.Is(err, store.ErrEgressEventExpired) {
			t.Errorf("late event = %v, want ErrEgressEventExpired", err)
		}
	})
}

func keysOf(m map[string][]byte) []string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	return k
}
