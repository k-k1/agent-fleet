package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

type fakeGCSObject struct {
	data []byte
	crc  uint32
	gen  int64
}

// fakeGCS speaks the part of the GCS JSON API the storage client uses against
// STORAGE_EMULATOR_HOST: object metadata, multipart and resumable uploads with
// ifGenerationMatch=0, and media download.
type fakeGCS struct {
	mu   sync.Mutex
	objs map[string]*fakeGCSObject // "bucket/name"
	gen  int64
	// Fault injection.
	truncate   bool   // store one byte fewer than was sent, and say so (a 2xx that lost data)
	dropReply  bool   // finalize the upload, answer 500 (the response is lost)
	failStatus int    // answer every request with this status
	uploads    int    // finalized uploads that created an object
	resumable  int    // resumable sessions opened
	finalizes  int    // upload requests that carried the object\'s total length
	crcSent    []bool // per upload: did the client send the object\'s CRC32C
	sessions   map[string]*fakeGCSSession
	requests   []string
}

type fakeGCSSession struct {
	bucket, name string
	cond0        bool
	crc          uint32
	sendCRC      bool
	buf          []byte
}

func newFakeGCS(t *testing.T) (*fakeGCS, *httptest.Server) {
	t.Helper()
	f := &fakeGCS{objs: map[string]*fakeGCSObject{}, sessions: map[string]*fakeGCSSession{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("STORAGE_EMULATOR_HOST", srv.Listener.Addr().String())
	return f, srv
}

func gcsCRC(b []byte) uint32 { return crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli)) }

func (f *fakeGCS) resource(bucket, name string, o *fakeGCSObject) map[string]any {
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], o.crc)
	return map[string]any{
		"kind": "storage#object", "bucket": bucket, "name": name,
		"size": strconv.Itoa(len(o.data)), "crc32c": base64.StdEncoding.EncodeToString(c[:]),
		"generation": strconv.FormatInt(o.gen, 10), "contentType": "application/gzip",
	}
}

func (f *fakeGCS) reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

func (f *fakeGCS) apiErr(w http.ResponseWriter, code int, msg string) {
	f.reply(w, code, map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

// create finalizes an object, honouring ifGenerationMatch=0.
func (f *fakeGCS) create(w http.ResponseWriter, bucket, name string, cond0 bool, data []byte, wantCRC uint32, sendCRC bool) {
	key := bucket + "/" + name
	if _, ok := f.objs[key]; ok && cond0 {
		f.apiErr(w, 412, "At least one of the pre-conditions you specified did not hold.")
		return
	}
	if sendCRC && gcsCRC(data) != wantCRC {
		f.apiErr(w, 400, "Provided CRC32C doesn't match calculated CRC32C")
		return
	}
	stored := data
	if f.truncate && len(stored) > 0 {
		stored = stored[:len(stored)-1]
	}
	f.gen++
	o := &fakeGCSObject{data: stored, crc: gcsCRC(stored), gen: f.gen}
	f.objs[key] = o
	f.uploads++
	if f.dropReply {
		f.apiErr(w, 500, "backend error")
		return
	}
	f.reply(w, 200, f.resource(bucket, name, o))
}

func (f *fakeGCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	if f.failStatus != 0 {
		f.apiErr(w, f.failStatus, "injected")
		return
	}
	q := r.URL.Query()
	p := r.URL.EscapedPath()
	switch {
	case strings.HasPrefix(p, "/upload/storage/v1/b/") && r.Method == http.MethodPost:
		bucket := strings.Split(strings.TrimPrefix(p, "/upload/storage/v1/b/"), "/")[0]
		cond0 := q.Get("ifGenerationMatch") == "0"
		if q.Get("uploadType") == "resumable" {
			var meta struct {
				Name   string `json:"name"`
				CRC32C string `json:"crc32c"`
			}
			json.NewDecoder(r.Body).Decode(&meta)
			id := fmt.Sprintf("s%d", len(f.sessions)+1)
			crc, sendCRC := decodeCRC(meta.CRC32C)
			f.crcSent = append(f.crcSent, sendCRC)
			f.sessions[id] = &fakeGCSSession{bucket: bucket, name: meta.Name, cond0: cond0, crc: crc, sendCRC: sendCRC}
			f.resumable++
			w.Header().Set("Location", "http://"+r.Host+"/resumable/"+id)
			w.WriteHeader(200)
			return
		}
		mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || !strings.HasPrefix(mt, "multipart/") {
			f.apiErr(w, 400, "want multipart")
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		part, _ := mr.NextPart()
		var meta struct {
			Name   string `json:"name"`
			CRC32C string `json:"crc32c"`
		}
		json.NewDecoder(part).Decode(&meta)
		part, _ = mr.NextPart()
		data, _ := io.ReadAll(part)
		wantCRC, sendCRC := decodeCRC(meta.CRC32C)
		f.crcSent = append(f.crcSent, sendCRC)
		f.finalizes++
		f.create(w, bucket, meta.Name, cond0, data, wantCRC, sendCRC)
	case strings.HasPrefix(p, "/resumable/"):
		s := f.sessions[strings.TrimPrefix(p, "/resumable/")]
		chunk, _ := io.ReadAll(r.Body)
		// Content-Range: bytes 0-8388607/* while more follows, bytes 0-N/total at the end.
		// Each chunk must start where the previous one ended: a resent or skipped range is
		// rejected rather than appended.
		cr := r.Header.Get("Content-Range")
		total := cr[strings.LastIndex(cr, "/")+1:]
		var start, end int
		if n, _ := fmt.Sscanf(cr, "bytes %d-%d/", &start, &end); n != 2 || start != len(s.buf) || end-start+1 != len(chunk) {
			f.apiErr(w, 400, "bad Content-Range "+cr+" at offset "+strconv.Itoa(len(s.buf)))
			return
		}
		s.buf = append(s.buf, chunk...)
		if total != "*" {
			f.finalizes++
			if n, _ := strconv.Atoi(total); n != len(s.buf) {
				f.apiErr(w, 400, "total length "+total+" does not match the bytes sent")
				return
			}
		}
		if total == "*" {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(s.buf)-1))
			// The client sends X-GUploader-No-308, so "resume incomplete" travels as a 200.
			w.Header().Set("X-Http-Status-Code-Override", "308")
			w.WriteHeader(200)
			return
		}
		f.create(w, s.bucket, s.name, s.cond0, s.buf, s.crc, s.sendCRC)
	case strings.HasPrefix(p, "/storage/v1/b/") && r.Method == http.MethodGet:
		rest := strings.TrimPrefix(p, "/storage/v1/b/")
		bucket, name, _ := strings.Cut(rest, "/o/")
		name, _ = url.PathUnescape(name)
		o, ok := f.objs[bucket+"/"+name]
		if !ok {
			f.apiErr(w, 404, "No such object: "+bucket+"/"+name)
			return
		}
		f.reply(w, 200, f.resource(bucket, name, o))
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		// Media download: /bucket/name.
		bucket, name, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
		name, _ = url.PathUnescape(name)
		o, ok := f.objs[bucket+"/"+name]
		if !ok {
			f.apiErr(w, 404, "No such object")
			return
		}
		var c [4]byte
		binary.BigEndian.PutUint32(c[:], o.crc)
		w.Header().Set("X-Goog-Hash", "crc32c="+base64.StdEncoding.EncodeToString(c[:]))
		w.Header().Set("X-Goog-Generation", strconv.FormatInt(o.gen, 10))
		w.Header().Set("Content-Length", strconv.Itoa(len(o.data)))
		w.Write(o.data)
	default:
		f.apiErr(w, 400, "unhandled "+r.Method+" "+r.URL.String())
	}
}

func decodeCRC(s string) (uint32, bool) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(b), true
}

func newTestGCSSink(t *testing.T, prefix string) *egressArchiveGCS {
	t.Helper()
	c, err := storage.NewClient(context.Background(), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return newEgressArchiveGCS(c, "bkt", prefix)
}

func TestEgressArchiveGCSSink(t *testing.T) {
	ctx := context.Background()
	f, _ := newFakeGCS(t)
	s := newTestGCSSink(t, "/pre/fix/")
	if ok, err := s.Exists(ctx, "egress/k"); ok || err != nil {
		t.Fatalf("missing: %v %v", ok, err)
	}
	if err := s.Put(ctx, "egress/k", strings.NewReader("data"), 4); err != nil {
		t.Fatal(err)
	}
	if o := f.objs["bkt/pre/fix/egress/k"]; o == nil || string(o.data) != "data" {
		t.Fatalf("objects = %v", f.objs)
	}
	if len(f.crcSent) != 1 || !f.crcSent[0] {
		t.Errorf("the multipart upload must carry the object's CRC32C: %v", f.crcSent)
	}
	if ok, err := s.Exists(ctx, "egress/k"); !ok || err != nil {
		t.Errorf("stored object: %v %v", ok, err)
	}
	rc, err := s.Get(ctx, "egress/k")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(rc); err != nil || string(b) != "data" {
		t.Errorf("Get = %q, %v", b, err)
	}
	rc.Close()
	if _, err := s.Get(ctx, "egress/none"); !errors.Is(err, storage.ErrObjectNotExist) {
		t.Errorf("Get of a missing object = %v", err)
	}
	// No prefix: the key is the object name.
	if err := newTestGCSSink(t, "").Put(ctx, "egress/root", strings.NewReader("x"), 1); err != nil || f.objs["bkt/egress/root"] == nil {
		t.Errorf("no prefix: %v %v", err, f.objs)
	}
}

// An existing key is never replaced: ifGenerationMatch=0 makes the service answer 412, and a
// Put with other content fails with the object untouched.
func TestEgressArchiveGCSSinkNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	f, _ := newFakeGCS(t)
	s := newTestGCSSink(t, "p")
	if err := s.Put(ctx, "k", strings.NewReader("first"), 5); err != nil {
		t.Fatal(err)
	}
	err := s.Put(ctx, "k", strings.NewReader("other"), 5)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrite = %v", err)
	}
	if string(f.objs["bkt/p/k"].data) != "first" || f.uploads != 1 {
		t.Errorf("object = %q after %d uploads", f.objs["bkt/p/k"].data, f.uploads)
	}
	// Identical bytes are refused as well: only the archiver's verify may call an existing
	// object good.
	if err := s.Put(ctx, "k", strings.NewReader("first"), 5); err == nil {
		t.Error("an existing key must fail even with identical content")
	}
	if f.uploads != 1 {
		t.Errorf("uploads = %d, want 1", f.uploads)
	}
}

// The upload landed and its response was lost: the client retries and sees 412. That is an
// error, not a confirmation; the next run finds the key with Exists and the archiver
// verifies the object in full.
func TestEgressArchiveGCSSinkLostResponse(t *testing.T) {
	f, _ := newFakeGCS(t)
	f.dropReply = true
	s := newTestGCSSink(t, "")
	if err := s.Put(context.Background(), "k", strings.NewReader("data"), 4); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Put = %v", err)
	}
	if f.uploads != 1 || string(f.objs["bkt/k"].data) != "data" {
		t.Errorf("uploads = %d, objects = %v", f.uploads, f.objs)
	}
	if ok, err := s.Exists(context.Background(), "k"); !ok || err != nil {
		t.Errorf("Exists after the lost response = %v, %v", ok, err)
	}
}

// Two different 16-byte bodies with the same length and CRC32C: an existing object that
// matches on both is still not the object this Put meant to write.
func TestEgressArchiveGCSSinkSameLengthAndCRCIsNotTheSameObject(t *testing.T) {
	a, _ := hex.DecodeString("e6cb34fca0141702e74dd811c186ec5f")
	b, _ := hex.DecodeString("20335ae4340d837fa06eac7ef3cc961f")
	if gcsCRC(a) != gcsCRC(b) || bytes.Equal(a, b) {
		t.Fatalf("fixture does not collide: %08x %08x", gcsCRC(a), gcsCRC(b))
	}
	f, _ := newFakeGCS(t)
	s := newTestGCSSink(t, "")
	if err := s.Put(context.Background(), "k", bytes.NewReader(a), 16); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(context.Background(), "k", bytes.NewReader(b), 16); err == nil {
		t.Error("a colliding body must not be reported as stored")
	}
	if !bytes.Equal(f.objs["bkt/k"].data, a) {
		t.Error("the existing object changed")
	}
}

// A 2xx whose stored length differs from what was sent is not a confirmation.
func TestEgressArchiveGCSSinkTruncatedObject(t *testing.T) {
	f, _ := newFakeGCS(t)
	f.truncate = true
	err := newTestGCSSink(t, "").Put(context.Background(), "k", strings.NewReader("data"), 4)
	if err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("truncated put = %v", err)
	}
}

// A body shorter than the size the caller promised is refused before anything is uploaded.
func TestEgressArchiveGCSSinkShortBody(t *testing.T) {
	f, _ := newFakeGCS(t)
	if err := newTestGCSSink(t, "").Put(context.Background(), "k", strings.NewReader("data"), 5); err == nil {
		t.Error("short body must fail")
	}
	if len(f.objs) != 0 || len(f.requests) != 0 {
		t.Errorf("requests = %v", f.requests)
	}
}

// Errors from the service are errors: Exists must not turn a 403 into "absent" (absent
// authorises a write), and a refused Put leaves no object.
func TestEgressArchiveGCSSinkServiceErrors(t *testing.T) {
	ctx := context.Background()
	f, _ := newFakeGCS(t)
	f.failStatus = 403
	s := newTestGCSSink(t, "")
	if ok, err := s.Exists(ctx, "k"); ok || err == nil {
		t.Errorf("Exists on 403 = %v, %v", ok, err)
	}
	if err := s.Put(ctx, "k", strings.NewReader("data"), 4); err == nil {
		t.Error("a refused Put must fail")
	}
	if _, err := s.Get(ctx, "k"); err == nil {
		t.Error("Get on 403 must fail")
	}
	if len(f.objs) != 0 {
		t.Errorf("objects = %v", f.objs)
	}
}

// Past one chunk the client uses a resumable upload; the object still appears whole, and a
// body that fails midway leaves nothing behind because the upload is cancelled, not closed.
func TestEgressArchiveGCSSinkResumableUpload(t *testing.T) {
	ctx := context.Background()
	f, _ := newFakeGCS(t)
	s := newTestGCSSink(t, "")
	s.chunk = 256 << 10
	data := make([]byte, 700<<10)
	for i := range data {
		data[i] = byte(i * 7)
	}
	if err := s.Put(ctx, "big", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if len(f.crcSent) != 1 || !f.crcSent[0] {
		t.Errorf("the resumable upload must carry the object's CRC32C: %v", f.crcSent)
	}
	if f.resumable != 1 || !bytes.Equal(f.objs["bkt/big"].data, data) {
		t.Errorf("resumable sessions = %d, stored %d of %d bytes", f.resumable, len(f.objs["bkt/big"].data), len(data))
	}
	rc, err := s.Get(ctx, "big")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(rc); err != nil || !bytes.Equal(b, data) {
		t.Errorf("Get: %d bytes, %v", len(b), err)
	}
	rc.Close()

	if err := s.Put(ctx, "broken", &breakingSeeker{data: data, okReads: 1}, int64(len(data))); err == nil {
		t.Error("a body that fails midway must fail the Put")
	}
	if f.objs["bkt/broken"] != nil {
		t.Error("a failed upload left an object behind")
	}
	// Cancelled before it was finished: no request ever announced the object's total length,
	// so the outcome does not hang on the service rejecting a partial body by its CRC32C.
	if f.finalizes != 1 {
		t.Errorf("finalizing requests = %d, want 1 (the good upload only)", f.finalizes)
	}
}

// breakingSeeker reads cleanly the first okReads passes (Seek starts a new one), then fails
// after its first chunk.
type breakingSeeker struct {
	data    []byte
	okReads int
	pos     int
	pass    int
}

func (b *breakingSeeker) Seek(off int64, whence int) (int64, error) {
	b.pos, b.pass = int(off), b.pass+1
	return off, nil
}

func (b *breakingSeeker) Read(p []byte) (int, error) {
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	if b.pass >= b.okReads && b.pos >= 300<<10 {
		return 0, errors.New("disk gone")
	}
	n := copy(p, b.data[b.pos:min(len(b.data), b.pos+64<<10)])
	b.pos += n
	return n, nil
}

// Reading back an object cut short is an error, not a clean EOF: the client checks the
// length and CRC32C the service reported.
func TestEgressArchiveGCSSinkTruncatedRead(t *testing.T) {
	ctx := context.Background()
	f, _ := newFakeGCS(t)
	s := newTestGCSSink(t, "")
	if err := s.Put(ctx, "k", strings.NewReader("some archive bytes"), 18); err != nil {
		t.Fatal(err)
	}
	o := f.objs["bkt/k"]
	o.data = o.data[:10] // damaged in storage; its recorded CRC32C still names the whole
	rc, err := s.Get(ctx, "k")
	if err == nil {
		_, err = io.ReadAll(rc)
		rc.Close()
	}
	if err == nil {
		t.Error("a truncated object must not read back cleanly")
	}
}

func TestNewEgressArchiveSinkGCS(t *testing.T) {
	newFakeGCS(t) // STORAGE_EMULATOR_HOST: the client needs no credentials
	if _, err := newEgressArchiveSink(context.Background(), "gs:///p"); err == nil {
		t.Error("gs:// without a bucket must be rejected")
	}
	s, err := newEgressArchiveSink(context.Background(), "gs://bkt/some/prefix/")
	if err != nil {
		t.Fatal(err)
	}
	if g, ok := s.(*egressArchiveGCS); !ok || g.prefix != "some/prefix" {
		t.Errorf("sink = %#v", s)
	}
}

// The purge over the GCS sink: every whole day is archived and verified through the sink
// before its rows go, and a second run finds the objects, verifies them and uploads nothing.
func TestEgressPurgeArchivesThenDeletesOverGCS(t *testing.T) {
	archiveDialects(t, func(t *testing.T, st *store.SQL) {
		ctx := context.Background()
		f, _ := newFakeGCS(t)
		seedArchive(t, st)
		sink := newTestGCSSink(t, "arch")
		archivePurger(st, sink).purge(ctx)

		d20 := f.objs["bkt/arch/egress/yyyy=2026/mm=09/dd=20/events-2026-09-20.jsonl.gz"]
		d21 := f.objs["bkt/arch/egress/yyyy=2026/mm=09/dd=21/events-2026-09-21.jsonl.gz"]
		if d20 == nil || d21 == nil || len(f.objs) != 3 {
			t.Fatalf("objects = %v", f.objs)
		}
		if ev := readArchive(t, d20.data)["event"]; len(ev) != 2 || ev[0]["id"] != "a" || ev[1]["id"] != "b" {
			t.Errorf("day 20 events = %v", ev)
		}
		if ids := remainingEventIDs(t, st); len(ids) != 2 || !ids["d"] || !ids["e"] {
			t.Errorf("events left = %v, want d and e", ids)
		}
		if f.uploads != 3 {
			t.Errorf("uploads = %d, want 3", f.uploads)
		}
	})
}
