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
	"strconv"
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
func (m *memSink) Get(_ context.Context, k string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(m.objs[k])), nil
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
	// The gzip footer (CRC, length) is only checked when the stream is read to its end.
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if err := zr.Close(); err != nil {
		t.Fatal(err)
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
	if rc, err := l.Get(ctx, "a/b/x.gz"); err != nil {
		t.Fatal(err)
	} else if b, _ := io.ReadAll(rc); string(b) != "hello" {
		t.Errorf("Get = %q", b)
	}
	// A root that does not exist yet is created, with every level beneath it.
	deep, _ := newEgressArchiveLocal("", filepath.Join(root, "new", "root"))
	if err := deep.Put(ctx, "egress/yyyy=2026/mm=09/dd=01/x", strings.NewReader("z"), 1); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "new/root/egress/yyyy=2026/mm=09/dd=01/x")); string(b) != "z" {
		t.Errorf("nested put = %q", b)
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

func (f *fakeArchiveS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := f.objs[aws.ToString(in.Key)]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: "no"}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
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
	if rc, err := s.Get(ctx, "egress/k"); err != nil {
		t.Fatal(err)
	} else if b, _ := io.ReadAll(rc); string(b) != "data" {
		t.Errorf("Get = %q", b)
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
		// The closed rows r1 and r2 go in one supplement, which is what authorises deleting them.
		supp := supplementsOf(sink.objs)
		if len(supp) != 1 {
			t.Fatalf("objects = %v", keysOf(sink.objs))
		}
		if s := readArchive(t, sink.objs[supp[0]]); len(s["address"]) != 2 || len(s["event"]) != 0 {
			t.Errorf("supplement = %v", s)
		}
		if len(sink.objs) != 3 {
			t.Errorf("a day without events must not be written: %v", keysOf(sink.objs))
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
		days := len(sink.objs)
		p.purge(ctx)
		if !bytes.Equal(sink.objs[key], full) {
			t.Error("the full day object was replaced")
		}
		// Only the address supplement is new.
		if len(sink.objs)-days != len(supplementsOf(sink.objs)) {
			t.Errorf("day objects were written again: %v", keysOf(sink.objs))
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

func supplementsOf(m map[string][]byte) []string {
	var out []string
	for k := range m {
		if strings.HasPrefix(k, "egress/addresses/") {
			out = append(out, k)
		}
	}
	return out
}

// hookSink runs fn inside every Put, as a writer racing the archive would.
type hookSink struct {
	*memSink
	fn func()
}

func (h hookSink) Put(ctx context.Context, k string, b io.ReadSeeker, n int64) error {
	if h.fn != nil {
		h.fn()
	}
	return h.memSink.Put(ctx, k, b, n)
}

// Address rows are not closed to writers by the floor. A row that arrives, or an open row that
// is closed, while the archive is being written must survive the purge and be archived by the
// next one, never deleted without being in an object.
func TestEgressPurgeKeepsAddressRowsThatChangedDuringTheArchive(t *testing.T) {
	archiveDialects(t, func(t *testing.T, st *store.SQL) {
		ctx := context.Background()
		seedArchive(t, st)
		day := func(d int) int64 { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC).UnixMilli() }
		injected := false
		sink := hookSink{memSink: newMemSink()}
		sink.fn = func() {
			if injected {
				return
			}
			injected = true
			// A late closed interval inside day 20, and the open row r3 closing at day 22.
			if err := st.UpsertWorkspaceAddresses(ctx, "m", []store.WorkspaceAddress{
				{WorkspaceID: "w9", Address: "10.0.1.9", Ref: "late", From: day(20) + 1000, Until: day(20) + 5000},
				{WorkspaceID: "w3", Address: "10.0.1.5", Ref: "r3", From: day(22), Until: day(22) + 1000},
			}); err != nil {
				t.Error(err)
			}
		}
		archivePurger(st, sink).purge(ctx)
		// Day objects were written before the injection or during it; the supplement was
		// taken after the day objects, so what matters is the state of the table now.
		left := map[string]bool{}
		_ = st.EachWorkspaceAddressOverlapping(ctx, 0, 1<<60, func(a store.WorkspaceAddress) error {
			left[a.Ref] = true
			return nil
		})
		// The supplement ran after the injection here, so "late" and the closed r3 are in it and
		// are deleted with it: whatever was deleted must be in a verified object.
		archived := map[string]bool{}
		for _, k := range supplementsOf(sink.objs) {
			for _, a := range readArchive(t, sink.objs[k])["address"] {
				archived[a["ref"].(string)] = true
			}
		}
		for _, r := range []string{"r1", "r2", "late", "r3"} {
			if !left[r] && !archived[r] {
				t.Errorf("row %s was deleted but is in no supplement", r)
			}
		}
		// A row written after the supplement was read is not deleted by it.
		sink2 := hookSink{memSink: newMemSink()}
		p := archivePurger(st, sink2)
		if err := st.UpsertWorkspaceAddresses(ctx, "m", []store.WorkspaceAddress{
			{WorkspaceID: "w8", Address: "10.0.1.8", Ref: "after", From: day(1), Until: day(2)}}); err != nil {
			t.Fatal(err)
		}
		rows, err := p.archive.archiveAddresses(ctx, day(25))
		if err != nil || len(rows) != 1 || rows[0].Ref != "after" {
			t.Fatalf("rows = %+v (%v)", rows, err)
		}
		if err := st.UpsertWorkspaceAddresses(ctx, "m", []store.WorkspaceAddress{
			{WorkspaceID: "w7", Address: "10.0.1.7", Ref: "later", From: day(1), Until: day(2)},
			{WorkspaceID: "w8", Address: "10.0.1.8", Ref: "after", From: day(1), Until: day(1) + 500}}); err != nil {
			t.Fatal(err)
		}
		if n, err := st.DeleteWorkspaceAddressVersions(ctx, rows); err != nil || n != 0 {
			t.Errorf("the changed version must not match: deleted %d (%v)", n, err)
		}
		if n, _ := st.PurgeWorkspaceAddresses(ctx, day(25), 10); n != 2 {
			t.Errorf("both rows must still be there, %d deleted", n)
		}
	})
}

func gz(t *testing.T, lines ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	zw.Write([]byte(strings.Join(lines, "\n") + "\n"))
	zw.Close()
	return b.Bytes()
}

const (
	vHdr = `{"type":"header","schema":1,"kind":"events-day","day":"2026-09-20","from":1,"to":2}`
	vEv  = `{"type":"event","id":"a","at":1,"source":"s","dest":"","host":"h","allowed":true,"count":1}`
	vMan = `{"type":"manifest","schema":1,"events":1,"addresses":0,"first_id":"a","last_id":"a","first_at":1,"last_at":1}`
)

func TestVerifyArchiveRejectsDamagedObjects(t *testing.T) {
	want := egressArchiveHeader{"header", 1, egressKindDay, "2026-09-20", 1, 2}
	if _, err := verifyArchive(bytes.NewReader(gz(t, vHdr, vEv, vMan)), want); err != nil {
		t.Fatalf("a whole object must verify: %v", err)
	}
	whole := gz(t, vHdr, vEv, vMan)
	corrupt := append([]byte(nil), whole...)
	corrupt[len(corrupt)-6] ^= 0xff // inside the CRC/length footer
	for name, data := range map[string][]byte{
		"empty":         {},
		"truncated":     whole[:len(whole)/2],
		"footer":        corrupt,
		"no manifest":   gz(t, vHdr, vEv),
		"wrong day":     gz(t, strings.Replace(vHdr, "09-20", "09-21", 1), vEv, vMan),
		"wrong schema":  gz(t, strings.Replace(vHdr, `"schema":1`, `"schema":2`, 1), vEv, vMan),
		"count":         gz(t, vHdr, vMan),
		"after the end": gz(t, vHdr, vEv, vMan, vEv),
	} {
		if _, err := verifyArchive(bytes.NewReader(data), want); err == nil {
			t.Errorf("%s: a damaged object verified", name)
		}
	}
}

// A key that exists is not proof of a whole object: a zero-byte or truncated one (left by an
// unconfirmed Put or an older bug) stops the purge at its day and is not trusted or overwritten.
func TestEgressPurgeDoesNotTrustADamagedExistingObject(t *testing.T) {
	archiveDialects(t, func(t *testing.T, st *store.SQL) {
		ctx := context.Background()
		seedArchive(t, st)
		sink := newMemSink()
		key := "egress/yyyy=2026/mm=09/dd=21/events-2026-09-21.jsonl.gz"
		sink.objs[key] = []byte{}
		archivePurger(st, sink).purge(ctx)
		if ids := remainingEventIDs(t, st); len(ids) != 3 || !ids["c"] {
			t.Errorf("day 21 and later must be kept, left = %v", ids)
		}
		if len(sink.objs[key]) != 0 {
			t.Error("the damaged object was overwritten without an operator")
		}
		// The operator removes it; the next run archives and deletes.
		delete(sink.objs, key)
		archivePurger(st, sink).purge(ctx)
		if ids := remainingEventIDs(t, st); len(ids) != 2 {
			t.Errorf("left = %v", ids)
		}
	})
}

// A Put the sink could not confirm leaves its key behind. The next run must verify it rather
// than treat the key as proof; a whole object passes, so an object that was in fact stored is
// then confirmed, and a truncated one stops the purge.
func TestEgressPurgeAfterAnUnconfirmedPut(t *testing.T) {
	archiveDialects(t, func(t *testing.T, st *store.SQL) {
		ctx := context.Background()
		seedArchive(t, st)
		key := "egress/yyyy=2026/mm=09/dd=20/events-2026-09-20.jsonl.gz"
		sink := newMemSink()
		unconfirmed := unconfirmedSink{sink, false}
		archivePurger(st, unconfirmed).purge(ctx)
		if ids := remainingEventIDs(t, st); len(ids) != 5 {
			t.Fatalf("an unconfirmed day must delete nothing, left = %v", ids)
		}
		if sink.objs[key] == nil {
			t.Fatal("setup: the unconfirmed put should have left the object")
		}
		// It was stored whole: verification confirms it and the purge proceeds.
		archivePurger(st, sink).purge(ctx)
		if ids := remainingEventIDs(t, st); len(ids) != 2 {
			t.Errorf("left = %v", ids)
		}
	})
	archiveDialects(t, func(t *testing.T, st *store.SQL) {
		ctx := context.Background()
		seedArchive(t, st)
		key := "egress/yyyy=2026/mm=09/dd=20/events-2026-09-20.jsonl.gz"
		sink := newMemSink()
		unconfirmed := unconfirmedSink{sink, true}
		archivePurger(st, unconfirmed).purge(ctx)
		archivePurger(st, sink).purge(ctx)
		if ids := remainingEventIDs(t, st); len(ids) != 5 {
			t.Errorf("a truncated leftover must stop the purge, left = %v", ids)
		}
		if len(sink.objs[key]) == 0 {
			t.Error("setup: truncated leftover missing")
		}
	})
}

// unconfirmedSink stores the object (whole, or cut in half) and then reports failure.
type unconfirmedSink struct {
	*memSink
	truncate bool
}

func (u unconfirmedSink) Put(ctx context.Context, k string, b io.ReadSeeker, n int64) error {
	_ = u.memSink.Put(ctx, k, b, n)
	if u.truncate {
		u.objs[k] = u.objs[k][:len(u.objs[k])/2]
	}
	return errors.New("not confirmed")
}

// craftDay writes a self-consistent day object holding only the given events, as an older bug
// or a day archived after part of it was deleted would leave.
func craftDay(t *testing.T, day string, events ...store.EgressEvent) []byte {
	t.Helper()
	d, _ := time.Parse("2006-01-02", day)
	from := d.UnixMilli()
	var b bytes.Buffer
	w := newArchiveWriter(&b, egressArchiveHeader{"header", egressArchiveSchema, egressKindDay, day, from, from + 24*3600*1000})
	for _, e := range events {
		w.event(e)
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A verified object can still lack events the database has. A well-formed partial day must stop
// the purge; a whole day with only some events left in the database (a delete cut off midway)
// must pass.
func TestEgressPurgeChecksAnExistingObjectAgainstTheDatabase(t *testing.T) {
	archiveDialects(t, func(t *testing.T, st *store.SQL) {
		ctx := context.Background()
		seedArchive(t, st)
		all, _ := st.ListAttributedEgressEvents(ctx, 0, 100)
		byID := map[string]store.EgressEvent{}
		for _, e := range all {
			byID[e.ID] = e.EgressEvent
		}
		key := "egress/yyyy=2026/mm=09/dd=20/events-2026-09-20.jsonl.gz"
		partial := newMemSink()
		partial.objs[key] = craftDay(t, "2026-09-20", byID["a"]) // b is missing
		archivePurger(st, partial).purge(ctx)
		if ids := remainingEventIDs(t, st); len(ids) != 5 {
			t.Fatalf("a partial day object must stop the purge, left = %v", ids)
		}
		// A different content for the same id is also a mismatch.
		wrong := byID["b"]
		wrong.Host = "other.example"
		partial.objs[key] = craftDay(t, "2026-09-20", byID["a"], wrong)
		archivePurger(st, partial).purge(ctx)
		if ids := remainingEventIDs(t, st); len(ids) != 5 {
			t.Fatalf("an object with other content must stop the purge, left = %v", ids)
		}
		// The whole day in the object, one event already deleted from the database.
		whole := newMemSink()
		whole.objs[key] = craftDay(t, "2026-09-20", byID["a"], byID["b"])
		if n, err := st.PurgeEgressEvents(ctx, byID["a"].At+1, 1); err != nil || n != 1 {
			t.Fatalf("partial delete %d %v", n, err)
		}
		archivePurger(st, whole).purge(ctx)
		if ids := remainingEventIDs(t, st); len(ids) != 2 || !ids["d"] {
			t.Errorf("a whole object with a partly deleted day must pass, left = %v", ids)
		}
	})
}

func TestVerifyArchiveChecksEventsAndRanges(t *testing.T) {
	want := egressArchiveHeader{"header", 1, egressKindDay, "2026-09-20", 1, 10}
	ev := func(id string, at int) string {
		return `{"type":"event","id":"` + id + `","at":` + strconv.Itoa(at) + `,"source":"s","dest":"","host":"h","allowed":true,"count":1}`
	}
	man := func(n int, first, last string, fa, la int) string {
		return `{"type":"manifest","schema":1,"events":` + strconv.Itoa(n) + `,"addresses":0,"first_id":"` + first + `","last_id":"` + last + `","first_at":` + strconv.Itoa(fa) + `,"last_at":` + strconv.Itoa(la) + `}`
	}
	hdr := `{"type":"header","schema":1,"kind":"events-day","day":"2026-09-20","from":1,"to":10}`
	if _, err := verifyArchive(bytes.NewReader(gz(t, hdr, ev("a", 2), ev("b", 2), ev("c", 5), man(3, "a", "c", 2, 5))), want); err != nil {
		t.Fatalf("whole object: %v", err)
	}
	for name, lines := range map[string][]string{
		"outside the window": {hdr, ev("a", 10), man(1, "a", "a", 10, 10)},
		"out of order":       {hdr, ev("b", 2), ev("a", 2), man(2, "a", "b", 2, 2)},
		"duplicate":          {hdr, ev("a", 2), ev("a", 2), man(2, "a", "a", 2, 2)},
		"id range":           {hdr, ev("a", 2), ev("b", 3), man(2, "a", "z", 2, 3)},
		"time range":         {hdr, ev("a", 2), ev("b", 3), man(2, "a", "b", 2, 9)},
	} {
		if _, err := verifyArchive(bytes.NewReader(gz(t, lines...)), want); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}

// The reader's rule: a stale open copy of a row in a day object must not keep attributing events
// after the end a supplement recorded for the same interval.
func TestNormalizeArchivedAddresses(t *testing.T) {
	rows := []store.WorkspaceAddress{
		{WorkspaceID: "w", Address: "10.0.0.1", Ref: "r", From: 100},             // day object: still open
		{WorkspaceID: "w", Address: "10.0.0.1", Ref: "r", From: 100, Until: 500}, // supplement: closed
		{WorkspaceID: "w", Address: "10.0.0.1", Ref: "r", From: 100, Until: 700}, // an older, later end
		{WorkspaceID: "x", Address: "10.0.0.1", Ref: "s", From: 500},
	}
	got := normalizeArchivedAddresses(rows)
	if len(got) != 2 || got[0].Until != 500 {
		t.Fatalf("normalized = %+v", got)
	}
	holders := func(at int64) (n int) {
		for _, r := range got {
			if r.From <= at && (r.Until == 0 || at < r.Until) {
				n++
			}
		}
		return
	}
	if holders(600) != 1 || got[0].Ref != "r" {
		t.Errorf("at 600 only s holds the address, got %d holders", holders(600))
	}
	// Without the rule the stale open copy matches too.
	raw := 0
	for _, r := range rows {
		if r.From <= 600 && (r.Until == 0 || 600 < r.Until) {
			raw++
		}
	}
	if raw != 3 {
		t.Errorf("setup: the raw union should misattribute (%d)", raw)
	}
}

// A failed fsync is retried on the next attempt, for the file's directory chain and for an
// object that already exists: being able to read a file proves nothing about the rename.
func TestEgressArchiveLocalSinkRetriesFsync(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "arch")
	l, _ := newEgressArchiveLocal("", root)
	synced := map[string]int{}
	failOn := ""
	orig := fsync
	t.Cleanup(func() { fsync = orig })
	fsync = func(f *os.File) error {
		synced[f.Name()]++
		if failOn != "" && f.Name() == failOn {
			return errors.New("fsync failed")
		}
		return f.Sync()
	}
	key := "egress/yyyy=2026/mm=09/dd=20/x"
	mm := filepath.Join(root, "egress/yyyy=2026/mm=09")

	// The parent's fsync fails on first creation: Put errors. The retry finds every directory
	// present and must still sync the chain.
	failOn = mm
	if err := l.Put(ctx, key, strings.NewReader("z"), 1); err == nil {
		t.Fatal("a failed directory fsync must fail the Put")
	}
	failOn = ""
	before := synced[mm]
	if err := l.Put(ctx, key, strings.NewReader("z"), 1); err != nil {
		t.Fatal(err)
	}
	if synced[mm] <= before {
		t.Error("the retry did not fsync the existing directory again")
	}

	// The rename landed but the final directory fsync failed: the next run finds the object
	// and reads it through Get, which must sync again and fail while the sync still fails.
	leaf := filepath.Join(root, "egress/yyyy=2026/mm=09/dd=20")
	key2 := "egress/yyyy=2026/mm=09/dd=20/y"
	n := 0
	fsync = func(f *os.File) error {
		synced[f.Name()]++
		if f.Name() == leaf {
			n++
			if n == 2 { // the chain sync before the write passes; the one after the rename fails
				return errors.New("fsync failed")
			}
		}
		return f.Sync()
	}
	if err := l.Put(ctx, key2, strings.NewReader("z"), 1); err == nil {
		t.Fatal("a failed fsync after the rename must fail the Put")
	}
	if ok, _ := l.Exists(ctx, key2); !ok {
		t.Fatal("setup: the renamed object should exist")
	}
	failOn = leaf
	fsync = func(f *os.File) error {
		if f.Name() == failOn {
			return errors.New("still failing")
		}
		return f.Sync()
	}
	if _, err := l.Get(ctx, key2); err == nil {
		t.Error("Get must not hand out an object whose directory cannot be synced")
	}
	failOn = ""
	rc, err := l.Get(ctx, key2)
	if err != nil {
		t.Fatalf("Get after the fsync works: %v", err)
	}
	rc.Close()
}

// The root's own ancestors are retried too: a directory created above the root whose parent's
// fsync failed is not skipped just because it exists on the next attempt.
func TestEgressArchiveLocalSinkRetriesFsyncAboveTheRoot(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "a", "b", "archive")
	l, _ := newEgressArchiveLocal("", root)
	synced := map[string]int{}
	failBase := true
	orig := fsync
	t.Cleanup(func() { fsync = orig })
	fsync = func(f *os.File) error {
		synced[f.Name()]++
		if failBase && f.Name() == base {
			return errors.New("fsync failed")
		}
		return f.Sync()
	}
	if err := l.Put(ctx, "egress/x", strings.NewReader("z"), 1); err == nil {
		t.Fatal("a failed fsync of the first created directory's parent must fail the Put")
	}
	failBase = false
	before := synced[base]
	if err := l.Put(ctx, "egress/x", strings.NewReader("z"), 1); err != nil {
		t.Fatal(err)
	}
	if synced[base] <= before {
		t.Error("the retry did not fsync the root's ancestor again")
	}
	failBase = true
	if _, err := l.Get(ctx, "egress/x"); err == nil {
		t.Error("Get must fail while an ancestor cannot be synced")
	}
}
