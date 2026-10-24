package egressagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/egressallow"
)

// ingestCP records every accepted event and can refuse requests.
type ingestCP struct {
	mu       sync.Mutex
	fail     int    // refuse this many requests, then accept
	onPost   func() // runs inside each request, before it is answered
	requests int
	maxBody  int
	got      []Event
}

func (c *ingestCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests++
	if c.onPost != nil {
		c.onPost()
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if c.fail > 0 {
		c.fail--
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var b struct{ Events []Event }
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	raw := make([]byte, 0, 1<<16)
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Body.Read(buf)
		raw = append(raw, buf[:n]...)
		if err != nil {
			break
		}
	}
	c.maxBody = max(c.maxBody, len(raw))
	if err := json.Unmarshal(raw, &b); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	c.got = append(c.got, b.Events...)
	w.WriteHeader(http.StatusNoContent)
}

// ids returns the distinct ids received, sorted, and how many events arrived in all.
func (c *ingestCP) ids() ([]string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	set := map[string]bool{}
	for _, e := range c.got {
		set[e.ID] = true
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, len(c.got)
}

func (c *ingestCP) set(fn func(*ingestCP)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(c)
}

type shipFixture struct {
	sh  *shipper
	cp  *ingestCP
	log string
}

func newShipFixture(t *testing.T) *shipFixture {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("log files are identified by an xattr; the agent runs on Linux only")
	}
	dir := t.TempDir()
	cp := &ingestCP{}
	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)
	cfg := &Config{
		IngestURL: srv.URL, Token: "tok", Client: srv.Client(),
		LogPath: filepath.Join(dir, "af-egress.log"), StatePath: filepath.Join(dir, "state.json"),
	}
	pol := &policySync{cfg: cfg}
	pol.cur.Store(&snapshot{match: egressallow.New([]string{".github.com"})})
	st, err := loadState(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	return &shipFixture{sh: &shipper{cfg: cfg, pol: pol, state: st}, cp: cp, log: cfg.LogPath}
}

// restart is a new shipper on the same files: what a crash and a restart leave.
func (f *shipFixture) restart(t *testing.T) {
	t.Helper()
	st, err := loadState(f.sh.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	f.sh = &shipper{cfg: f.sh.cfg, pol: f.sh.pol, state: st}
}

func line(sec int, src, host string) string {
	return fmt.Sprintf("af1 %d.000 %s 140.82.112.3 TCP_TUNNEL splice %s -\n", 1761300000+sec, src, host)
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestShipConvertsLines(t *testing.T) {
	f := newShipFixture(t)
	appendFile(t, f.log, line(1, "10.0.1.5", "api.github.com")+line(2, "10.0.1.6", "evil.example"))
	if err := f.sh.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.cp.got) != 2 {
		t.Fatalf("got %d events, want 2", len(f.cp.got))
	}
	a, b := f.cp.got[0], f.cp.got[1]
	if a.At != 1761300001000 || a.Source != "10.0.1.5" || a.Dest != "140.82.112.3" ||
		a.Host != "api.github.com" || !a.Allowed || a.Count != 1 {
		t.Fatalf("first event = %+v", a)
	}
	if b.Host != "evil.example" || b.Allowed {
		t.Fatalf("second event = %+v: a name outside the allowlist is not allowed", b)
	}
	if !strings.HasSuffix(a.ID, "@0") || b.ID != strings.TrimSuffix(a.ID, "0")+fmt.Sprint(len(line(1, "10.0.1.5", "api.github.com"))) {
		t.Fatalf("ids %q %q: want <file>@<offset of the line>", a.ID, b.ID)
	}
}

func TestShipCommitsOnlyAfter2xxAndResendsSameIDs(t *testing.T) {
	f := newShipFixture(t)
	appendFile(t, f.log, line(1, "10.0.1.5", "a.github.com")+line(2, "10.0.1.5", "b.github.com"))
	f.cp.set(func(c *ingestCP) { c.fail = 2 })
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := f.sh.pass(ctx); err != errShipFailed {
			t.Fatalf("pass %d: err = %v, want errShipFailed", i, err)
		}
		st, _ := loadState(f.sh.cfg.StatePath)
		for id, off := range st.Offsets {
			if off != 0 {
				t.Fatalf("offset for %s committed to %d while the CP refused", id, off)
			}
		}
		f.restart(t) // a crash between failures loses nothing either
	}
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}
	ids, n := f.cp.ids()
	if len(ids) != 2 || n != 2 {
		t.Fatalf("after recovery: %d ids, %d events; want 2 and 2", len(ids), n)
	}

	// Already shipped: a further pass (and a restart) sends nothing new.
	f.restart(t)
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if _, n := f.cp.ids(); n != 2 {
		t.Fatalf("committed lines were sent again (%d events)", n)
	}
}

func TestShipLostResponseResendsSameIDs(t *testing.T) {
	// The CP stored the batch but the agent died before committing: the resend carries the
	// same ids, which is what lets the CP store it once.
	f := newShipFixture(t)
	appendFile(t, f.log, line(1, "10.0.1.5", "a.github.com"))
	if err := f.sh.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.sh.cfg.StatePath); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	if err := f.sh.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	ids, n := f.cp.ids()
	if len(ids) != 1 || n != 2 {
		t.Fatalf("resend: %d distinct ids over %d events; want 1 over 2", len(ids), n)
	}
}

func TestShipWaitsForTheNewline(t *testing.T) {
	f := newShipFixture(t)
	full := line(1, "10.0.1.5", "a.github.com")
	appendFile(t, f.log, line(0, "10.0.1.5", "x.github.com")+full[:20])
	ctx := context.Background()
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if _, n := f.cp.ids(); n != 1 {
		t.Fatalf("a partial line was shipped (%d events)", n)
	}
	appendFile(t, f.log, full[20:])
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.cp.got) != 2 || f.cp.got[1].Host != "a.github.com" {
		t.Fatalf("completed line: %+v", f.cp.got)
	}
}

func TestShipFollowsRotation(t *testing.T) {
	f := newShipFixture(t)
	ctx := context.Background()
	appendFile(t, f.log, line(1, "10.0.1.5", "a.github.com"))
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}
	// Squid writes one more line, rotates (rename to .0), and writes a line to the old file
	// before it reopens: the agent has to finish .0 as well as read the new file.
	appendFile(t, f.log, line(2, "10.0.1.5", "b.github.com"))
	if err := os.Rename(f.log, f.log+".0"); err != nil {
		t.Fatal(err)
	}
	appendFile(t, f.log+".0", line(3, "10.0.1.5", "c.github.com"))
	appendFile(t, f.log, line(4, "10.0.1.5", "d.github.com"))
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}
	appendFile(t, f.log+".0", line(5, "10.0.1.5", "e.github.com"))
	// A second rotation while the agent is down: .0 -> .1, current -> .0.
	if err := os.Rename(f.log+".0", f.log+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.log, f.log+".0"); err != nil {
		t.Fatal(err)
	}
	appendFile(t, f.log+".0", line(6, "10.0.1.5", "f.github.com"))
	appendFile(t, f.log, line(7, "10.0.1.5", "g.github.com"))
	f.restart(t)
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}

	ids, n := f.cp.ids()
	if len(ids) != 7 || n != 7 {
		t.Fatalf("%d ids over %d events, want 7 and 7: %v", len(ids), n, ids)
	}
	hosts := map[string]bool{}
	for _, e := range f.cp.got {
		hosts[e.Host] = true
	}
	for _, h := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		if !hosts[h+".github.com"] {
			t.Fatalf("%s.github.com was never shipped", h)
		}
	}

	// The oldest file is deleted by the next rotation: its offset is forgotten.
	if err := os.Remove(f.log + ".1"); err != nil {
		t.Fatal(err)
	}
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.sh.state.Offsets) != 2 {
		t.Fatalf("state keeps %d files, want 2", len(f.sh.state.Offsets))
	}
}

func TestShipNewFileGetsNewIDs(t *testing.T) {
	// A replaced file at the same path (which may reuse the old inode) must not reuse the
	// old ids: the CP would drop its events as already stored.
	f := newShipFixture(t)
	ctx := context.Background()
	appendFile(t, f.log, line(1, "10.0.1.5", "a.github.com"))
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.log); err != nil {
		t.Fatal(err)
	}
	appendFile(t, f.log, line(9, "10.0.1.5", "z.github.com"))
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}
	ids, n := f.cp.ids()
	if len(ids) != 2 || n != 2 {
		t.Fatalf("%d ids over %d events: the new file reused an id (%v)", len(ids), n, ids)
	}
}

func TestShipBatchesUnderTheIngestLimit(t *testing.T) {
	f := newShipFixture(t)
	var b strings.Builder
	const lines = 6000
	for i := 0; i < lines; i++ {
		b.WriteString(line(i, "10.0.1.5", fmt.Sprintf("host-%04d-%s.github.com", i, strings.Repeat("x", 60))))
	}
	appendFile(t, f.log, b.String())
	if err := f.sh.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	ids, n := f.cp.ids()
	if len(ids) != lines || n != lines {
		t.Fatalf("%d ids over %d events, want %d", len(ids), n, lines)
	}
	if f.cp.requests < 2 || f.cp.maxBody > 1<<20 {
		t.Fatalf("%d requests, largest body %d bytes: want several, each under 1 MiB", f.cp.requests, f.cp.maxBody)
	}
}

func TestShipSkipsForeignAndOverlongLines(t *testing.T) {
	f := newShipFixture(t)
	appendFile(t, f.log, line(1, "10.0.1.5", "a.github.com")+
		"not our format\n"+
		"af1 1761300002.000 10.0.1.5 - TCP_TUNNEL splice "+strings.Repeat("y", maxLine)+" -\n"+
		line(3, "10.0.1.5", "c.github.com"))
	if err := f.sh.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.cp.got) != 2 || f.cp.got[1].Host != "c.github.com" {
		t.Fatalf("got %+v: want the two good lines", f.cp.got)
	}
}

func TestShipWaitsForAPolicy(t *testing.T) {
	f := newShipFixture(t)
	f.sh.pol.cur.Store(nil)
	appendFile(t, f.log, line(1, "10.0.1.5", "a.github.com"))
	if err := f.sh.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.cp.requests != 0 {
		t.Fatal("shipped without a policy: every verdict would read not allowed")
	}
}

func TestShipLineCompletedDuringPost(t *testing.T) {
	// The pass reads A and the start of B, hits EOF, and POSTs A. While that POST is in
	// flight Squid finishes B and writes C. B and C must arrive whole, with their own
	// offsets as ids.
	f := newShipFixture(t)
	a, b, c := line(1, "10.0.1.5", "a.github.com"), line(2, "10.0.1.5", "b.github.com"), line(3, "10.0.1.5", "c.github.com")
	appendFile(t, f.log, a+b[:15])
	once := sync.Once{}
	f.cp.onPost = func() { once.Do(func() { appendFile(t, f.log, b[15:]+c) }) }
	if err := f.sh.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.sh.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.cp.got) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(f.cp.got), f.cp.got)
	}
	prefix := strings.TrimSuffix(f.cp.got[0].ID, "@0")
	for i, want := range []struct {
		host string
		off  int
	}{{"a.github.com", 0}, {"b.github.com", len(a)}, {"c.github.com", len(a) + len(b)}} {
		if e := f.cp.got[i]; e.Host != want.host || e.ID != fmt.Sprintf("%s@%d", prefix, want.off) {
			t.Fatalf("event %d = %+v, want host %s at offset %d", i, e, want.host, want.off)
		}
	}
}

func TestShipSameFirstLineInTwoFilesGetsDistinctIDs(t *testing.T) {
	// Two connections that started in the same millisecond can be the first lines of the
	// rotated file and the new one. Both files' lines must all arrive, under distinct ids.
	f := newShipFixture(t)
	ctx := context.Background()
	first := line(1, "10.0.1.5", "a.github.com")
	appendFile(t, f.log, first+line(2, "10.0.1.5", "b.github.com"))
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.log, f.log+".0"); err != nil {
		t.Fatal(err)
	}
	appendFile(t, f.log, first+line(3, "10.0.1.5", "c.github.com"))
	if err := f.sh.pass(ctx); err != nil {
		t.Fatal(err)
	}
	ids, n := f.cp.ids()
	if len(ids) != 4 || n != 4 {
		t.Fatalf("%d ids over %d events, want 4 and 4: %v", len(ids), n, ids)
	}
}

func TestShipNothingBeforeTheGenerationIsDurable(t *testing.T) {
	f := newShipFixture(t)
	appendFile(t, f.log, line(1, "10.0.1.5", "a.github.com"))
	orig := syncFile
	t.Cleanup(func() { syncFile = orig })
	syncs := 0
	syncFile = func(*os.File) error { syncs++; return fmt.Errorf("disk gone") }
	if err := f.sh.pass(context.Background()); err == nil {
		t.Fatal("a failed sync must stop the pass")
	}
	if f.cp.requests != 0 {
		t.Fatal("events were sent under a generation that is not on disk")
	}
	syncFile = func(fh *os.File) error { syncs++; return orig(fh) }
	for i := 0; i < 2; i++ {
		if err := f.sh.pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, n := f.cp.ids(); n != 1 || syncs != 2 {
		t.Fatalf("%d events, %d syncs: want 1 event and one sync per generation per process", n, syncs)
	}
}
