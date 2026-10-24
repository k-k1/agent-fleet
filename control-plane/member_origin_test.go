package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// memberOriginWireVector is the mark for a fixed seed and fixed claims. The Agent's
// memberorigin_test.go (a separate module) verifies the same string, so a change to the encoding
// on either side turns one of the two red.
const memberOriginWireVector = "v1.eyJtIjoiUE9TVCIsInAiOiIvYWdlbnRzL21lbW9yeS9lbnRyaWVzL3BpbiIsInEiOiJwcm9qZWN0PXAtMDEyMzQ1Njc4OWFiIiwiYiI6ImU1ZDdiMmFkYjhmYjlmOTE4NzYzODIyOWIwYTJjZjI0MWM1ZjE1NTg2N2NkMzFjODJjYmIyMGM2NDJlNjI1NTQiLCJ3cyI6IndzLXZlY3RvciIsIm1iIjoibWItdmVjdG9yIiwic3ViIjoiaWQtdmVjdG9yIiwiaWF0IjoxNzAwMDAwMDAwLCJleHAiOjE3MDAwMDAwNjAsIm4iOiJBQUVDQXdRRkJnY0lDUW9MREEwT0R3In0.4aQJdnXz7_t-YAUh_PRuMy9vivzGagEYFR5gZurPvVwR71TiyyTYUWIHTi9nob6libiNii-TJNl7kSt1MM-5DA"

func TestMemberOriginWireVector(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	sum := sha256.Sum256([]byte(`{"name":"n"}`))
	got := signMemberOrigin(ed25519.NewKeyFromSeed(seed), "v1", memberOriginClaims{
		Method: "POST", Path: "/agents/memory/entries/pin", Query: "project=p-0123456789ab",
		BodySHA256: hex.EncodeToString(sum[:]), Workspace: "ws-vector", Membership: "mb-vector", Subject: "id-vector",
		IssuedAt: 1700000000, Expires: 1700000060, Nonce: base64.RawURLEncoding.EncodeToString([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}),
	})
	if got != memberOriginWireVector {
		t.Fatalf("wire vector changed; update both modules' copies:\n%s", got)
	}
}

// The CP signs exactly these routes, and each is a relayed route of the real mux.
func TestMemberOriginRoutesAreTheMemberWrites(t *testing.T) {
	want := []string{
		"POST /api/agents/memory-review/proposals/approve",
		"POST /api/agents/memory-review/proposals/list",
		"POST /api/agents/memory-review/proposals/reject",
		"POST /api/agents/memory-review/proposals/reject-all",
		"POST /api/agents/memory-review/run",
		"POST /api/agents/memory-review/runs/revert",
		"POST /api/agents/memory/af-import",
		"POST /api/agents/memory/auto-sync",
		"POST /api/agents/memory/entries/member-save",
		"POST /api/agents/memory/entries/pin",
		"POST /api/agents/memory/entries/project-move",
		"POST /api/agents/memory/entries/purge",
		"POST /api/agents/memory/entries/revert",
		"POST /api/agents/memory/import",
		"POST /api/agents/memory/import/apply",
		"POST /api/agents/memory/snapshots",
		"PUT /api/env/ui-prefs",
	}
	var got []string
	for k := range memberOriginRoutes {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("signed routes = %v, want %v", got, want)
	}
	golden, err := os.ReadFile("testdata/routes.golden")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range want {
		if !strings.Contains("\n"+string(golden)+"\n", "\n"+r+"\n") {
			t.Errorf("%s is not registered in the CP's route table", r)
		}
	}
}

func decodeMark(t *testing.T, pub ed25519.PublicKey, mark string) memberOriginClaims {
	t.Helper()
	parts := strings.Split(mark, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		t.Fatalf("mark %q", mark)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if !ed25519.Verify(pub, append([]byte("af-member-origin/v1\n"), payload...), sig) {
		t.Fatalf("signature does not verify with the injected public key")
	}
	var c memberOriginClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// The relay signs a member write for the Agent's path, query and body, strips a mark the browser
// sent on any other route, and the Agent still receives the same body.
func TestRelaySignsOnlyMemberWrites(t *testing.T) {
	type seen struct{ path, query, mark, body string }
	var got []seen
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.URL.Path, r.URL.RawQuery, r.Header.Get(memberOriginHeader), string(b)})
		_, _ = w.Write([]byte(`{}`))
	}))
	defer cleanup()
	t.Setenv(memberOriginKeyVersionEnv, "")
	env := memberOriginEnv(proxy.mgr.tokenSignMaster(), memberOriginKeyVersion(), res.ws.ID, res.ws.MembershipID)
	if len(env) != 3 || !strings.HasPrefix(env[0], "AF_MEMBER_ORIGIN_KEYS=v1:") || !strings.Contains(env[0], ",v2:") ||
		env[1] != "AF_WORKSPACE_ID=ws1" || env[2] != "AF_MEMBERSHIP_ID="+res.ws.MembershipID {
		t.Fatalf("env = %v", env)
	}
	pub := memberOriginKey(proxy.mgr.tokenSignMaster(), 1).Public().(ed25519.PublicKey)
	if !strings.Contains(env[0], base64.RawURLEncoding.EncodeToString(pub)) {
		t.Fatalf("the injected key is not the signing key's public half")
	}

	post := func(p, body string) {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(body))
		req.Header.Set(memberOriginHeader, "v1.forged.byTheBrowser")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		proxy.rest(rec, req, res)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
	post("/api/agents/memory/entries/pin?project=app-0123456789ab", `{"name":"a","pinned":true}`)
	post("/api/agents/memory/entries/member-save?project=app-0123456789ab", `{"name":"b"}`)
	post("/api/agents/memory/restore?rev=abc", `{"rev":"abc"}`)
	post("/api/agents/memory/claude-import", `{}`)

	start := time.Now().Unix()
	for i, s := range got[:2] {
		c := decodeMark(t, pub, s.mark)
		sum := sha256.Sum256([]byte(s.body))
		if c.Method != "POST" || c.Path != s.path || c.Query != s.query || c.BodySHA256 != hex.EncodeToString(sum[:]) ||
			c.Workspace != "ws1" || c.Membership != res.ws.MembershipID || c.Subject != "user1" ||
			c.Expires-c.IssuedAt != 60 || c.IssuedAt < start-5 || len(c.Nonce) < 16 {
			t.Fatalf("claims %d = %+v for %+v", i, c, s)
		}
	}
	if got[0].body != `{"name":"a","pinned":true}` {
		t.Fatalf("body changed in transit: %q", got[0].body)
	}
	for _, s := range got[2:] {
		if s.mark != "" {
			t.Fatalf("%s went out with a mark %q", s.path, s.mark)
		}
	}
}

// A workspace that is not the caller's own is not signed for, and an oversized body is refused
// before anything reaches the Agent.
func TestRelayRefusesToSignForAnotherMember(t *testing.T) {
	calls := 0
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer cleanup()
	other := *res
	other.mv = store.MembershipView{MembershipID: "someone-else"}
	rec := httptest.NewRecorder()
	proxy.rest(rec, httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/revert", strings.NewReader(`{}`)), &other)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("another member: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	big := strings.Repeat("x", memberOriginMaxBody+1)
	bigReq := httptest.NewRequest(http.MethodPost, "/api/agents/memory/entries/member-save", strings.NewReader(big))
	bigReq.Header.Set("Content-Type", "application/json")
	proxy.rest(rec, bigReq, res)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized: %d", rec.Code)
	}
	if calls != 0 {
		t.Fatalf("the Agent was called %d times", calls)
	}
}

// Version 0 turns signing off: nothing is injected and nothing is signed, so the Agent refuses.
func TestMemberOriginOff(t *testing.T) {
	t.Setenv(memberOriginKeyVersionEnv, "0")
	if env := memberOriginEnv([]byte("k"), memberOriginKeyVersion(), "ws", "mb"); env != nil {
		t.Fatalf("env with signing off: %v", env)
	}
	t.Setenv(memberOriginKeyVersionEnv, "two")
	if memberOriginKeyVersion() != 0 {
		t.Fatal("an unparsable version must turn signing off")
	}
	t.Setenv(memberOriginKeyVersionEnv, "2")
	env := memberOriginEnv([]byte("k"), memberOriginKeyVersion(), "ws", "mb")
	if len(env) != 3 || !strings.HasPrefix(env[0], "AF_MEMBER_ORIGIN_KEYS=v2:") || !strings.Contains(env[0], ",v3:") {
		t.Fatalf("rotated env = %v", env)
	}
}

// browserCredentials puts on r what a browser behind the Console and a front gateway carries: the
// Console's session cookie, an app cookie, gateway identity headers, its own bearer and a forged mark.
func browserCredentials(r *http.Request, identityHeader string) {
	r.Header.Add("Cookie", "af_session=synthetic-member-login; theme=dark")
	r.Header.Set(identityHeader, "member@example.com")
	r.Header.Set("X-Forwarded-Email", "member@example.com")
	r.Header.Set("X-Auth-Request-Access-Token", "gateway-access-token")
	r.Header.Set("Authorization", "Bearer browser-token")
	r.Header.Set(memberOriginHeader, "v1.browser.supplied")
}

// leakedCredentials names what reached the Agent that it must not see.
func leakedCredentials(h http.Header, identityHeader string) []string {
	var out []string
	for _, k := range []string{"Cookie", identityHeader, "X-Forwarded-Email", "X-Auth-Request-Access-Token"} {
		if v := h.Get(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	if a := h.Get("Authorization"); a != "Bearer agent-token" {
		out = append(out, "Authorization="+a)
	}
	return out
}

// Neither the REST nor the SSE relay hands the Agent the member's login cookie or identity: with
// it, a session could have the CP sign anything as the member (ADR 0113). Signed and unsigned
// routes alike.
func TestAgentRelayStripsBrowserCredentials(t *testing.T) {
	const idh = "X-AF-Test-Email"
	var got []http.Header
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Clone())
		_, _ = w.Write([]byte(`{}`))
	}))
	defer cleanup()
	proxy.mgr.emailHeader = idh
	for _, p := range []string{"/api/agents/memory/entries/pin", "/api/agents/memory/restore", "/api/sessions"} {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(`{}`))
		browserCredentials(req, idh)
		req.Header.Set("Content-Type", "application/json")
		proxy.rest(httptest.NewRecorder(), req, res)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/s1/events", nil)
	browserCredentials(req, idh)
	proxy.stream(httptest.NewRecorder(), req, res)
	if len(got) != 4 {
		t.Fatalf("agent saw %d requests", len(got))
	}
	for i, h := range got {
		if l := leakedCredentials(h, idh); len(l) > 0 {
			t.Errorf("relay %d leaked %v", i, l)
		}
		if mark := h.Get(memberOriginHeader); (i == 0) != (mark != "") || mark == "v1.browser.supplied" {
			t.Errorf("relay %d mark %q", i, mark)
		}
	}
}

// The preview relay (path and host form; HTTP, SSE and WebSocket share it) drops a browser-sent
// mark with the other Console credentials.
func TestPreviewRelayDropsMemberOriginMark(t *testing.T) {
	const idh = "X-AF-Test-Email"
	var got []http.Header
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Clone())
	}))
	defer agent.Close()
	rt := stubRuntime{endpoint: agent.URL, token: "agent-token"}
	for _, o := range []previewRelayOptions{
		{port: 5173, path: "/app", publicHost: "cp.example", proto: "https", prefix: "/preview/5173", identityHeader: idh},
		{port: 5173, path: "/", publicHost: "p-5173.example", proto: "https", identityHeader: idh},
	} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		browserCredentials(req, idh)
		req.Header.Set("Accept", "text/event-stream")
		relayPreview(httptest.NewRecorder(), req, rt, o)
	}
	if len(got) != 2 {
		t.Fatalf("agent saw %d requests", len(got))
	}
	for i, h := range got {
		if v := h.Get(memberOriginHeader); v != "" {
			t.Errorf("preview %d forwarded a mark %q", i, v)
		}
		if strings.Contains(h.Get("Cookie"), "af_session") || h.Get(idh) != "" {
			t.Errorf("preview %d leaked credentials: %v", i, h)
		}
	}
}

// A same-site page that is not the Console (a preview app on its own host) can make the browser
// send the member's cookie with a text/plain no-cors POST. Nothing such a request asks for is
// signed or relayed; the Console's own JSON request, and a non-browser caller, are.
func TestMemberOriginRefusesCrossOriginRequests(t *testing.T) {
	calls := 0
	var marks []string
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		marks = append(marks, r.Header.Get(memberOriginHeader))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer cleanup()
	const body = `{"scope":"user","name":"injected","description":"d","type":"user","body":"attacker instructions"}`
	send := func(h map[string]string) int {
		req := httptest.NewRequest(http.MethodPost, "https://console.example/api/agents/memory/entries/member-save", strings.NewReader(body))
		for k, v := range h {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		proxy.rest(rec, req, res)
		return rec.Code
	}
	for name, c := range map[string]struct {
		h    map[string]string
		want int
	}{
		"text/plain no-cors from a preview host": {map[string]string{"Content-Type": "text/plain;charset=UTF-8",
			"Origin": "https://p-5173.preview.example", "Sec-Fetch-Site": "same-site"}, http.StatusUnsupportedMediaType},
		"text/plain with no browser headers": {map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		"no content type":                    {map[string]string{}, http.StatusUnsupportedMediaType},
		"JSON from a preview host":           {map[string]string{"Content-Type": "application/json", "Origin": "https://p-5173.preview.example"}, http.StatusForbidden},
		"JSON, same-site fetch metadata":     {map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		"JSON, cross-site fetch metadata":    {map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		"JSON from an opaque origin":         {map[string]string{"Content-Type": "application/json", "Origin": "null"}, http.StatusForbidden},
	} {
		if got := send(c.h); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	if calls != 0 {
		t.Fatalf("a refused request reached the Agent %d times", calls)
	}
	// The Console itself, and a caller that is not a browser.
	if got := send(map[string]string{"Content-Type": "application/json; charset=utf-8",
		"Origin": "https://console.example", "Sec-Fetch-Site": "same-origin"}); got != http.StatusOK {
		t.Fatalf("Console request: %d", got)
	}
	if got := send(map[string]string{"Content-Type": "application/json"}); got != http.StatusOK {
		t.Fatalf("non-browser request: %d", got)
	}
	if calls != 2 || marks[0] == "" || marks[1] == "" {
		t.Fatalf("accepted requests: calls=%d marks=%q", calls, marks)
	}
}

// The bundle import is a multipart upload signed by its body hash: the Agent gets the exact bytes
// (over the 2 MiB JSON cap included) with a mark over their SHA-256, and a page other than the
// Console is refused before anything is hashed.
func TestHashedUploadIsSignedByBodyHash(t *testing.T) {
	var gotBody []byte
	var gotMark string
	calls := 0
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotBody, _ = io.ReadAll(r.Body)
		gotMark = r.Header.Get(memberOriginHeader)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer cleanup()
	t.Setenv(memberOriginKeyVersionEnv, "")
	pub := memberOriginKey(proxy.mgr.tokenSignMaster(), 1).Public().(ed25519.PublicKey)

	big := "--B\r\nContent-Disposition: form-data; name=\"file\"; filename=\"b.tgz\"\r\n\r\n" +
		strings.Repeat("x", 3<<20) + "\r\n--B--\r\n" // over memberOriginMaxBody
	send := func(h map[string]string, body string) int {
		req := httptest.NewRequest(http.MethodPost, "https://console.example/api/agents/memory/import", strings.NewReader(body))
		for k, v := range h {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		proxy.rest(deadlineRecorder{rec}, req, res)
		return rec.Code
	}
	mp := "multipart/form-data; boundary=B"
	for name, c := range map[string]struct {
		h    map[string]string
		want int
	}{
		"no Console header": {map[string]string{"Content-Type": mp}, http.StatusForbidden},
		"text/plain":        {map[string]string{"Content-Type": "text/plain", memberOriginConsoleHeader: "1"}, http.StatusUnsupportedMediaType},
		"JSON":              {map[string]string{"Content-Type": "application/json", memberOriginConsoleHeader: "1"}, http.StatusUnsupportedMediaType},
		"cross-site":        {map[string]string{"Content-Type": mp, memberOriginConsoleHeader: "1", "Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		"foreign origin":    {map[string]string{"Content-Type": mp, memberOriginConsoleHeader: "1", "Origin": "https://p.preview.example"}, http.StatusForbidden},
	} {
		if got := send(c.h, big); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	if calls != 0 {
		t.Fatalf("a refused upload reached the Agent %d times", calls)
	}
	ok := map[string]string{"Content-Type": mp, memberOriginConsoleHeader: "1", "Origin": "https://console.example", "Sec-Fetch-Site": "same-origin"}
	if got := send(ok, big); got != http.StatusOK {
		t.Fatalf("Console upload: %d", got)
	}
	if string(gotBody) != big {
		t.Fatalf("the Agent received %d bytes, want %d", len(gotBody), len(big))
	}
	c := decodeMark(t, pub, gotMark)
	sum := sha256.Sum256([]byte(big))
	if c.Path != "/agents/memory/import" || c.BodySHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("claims = %+v", c)
	}
}

// deadlineRecorder is a ResponseRecorder that, like the real server's writer, accepts read
// deadlines (the hashed upload refuses to start without being able to set one).
type deadlineRecorder struct{ *httptest.ResponseRecorder }

func (deadlineRecorder) SetReadDeadline(time.Time) error { return nil }

// slowBody delivers its bytes, waits, then ends; doneAt is when it ended.
type slowBody struct {
	r      io.Reader
	wait   time.Duration
	doneAt time.Time
	fail   error
}

func (b *slowBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		if b.fail != nil {
			return n, b.fail
		}
		time.Sleep(b.wait)
		b.doneAt = time.Now()
	}
	return n, err
}

// The mark's lifetime starts when the upload has been read, not when it began (a slow upload may
// take longer than the mark lives); the spool is bounded, and nothing is left in the temp dir after
// a success, an oversize body, a refusal or a dropped connection.
func TestHashedUploadTimingLimitsAndCleanup(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	var gotMark string
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		gotMark = r.Header.Get(memberOriginHeader)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer cleanup()
	t.Setenv(memberOriginKeyVersionEnv, "")
	pub := memberOriginKey(proxy.mgr.tokenSignMaster(), 1).Public().(ed25519.PublicKey)
	old := memberOriginUploadMax
	memberOriginUploadMax = 1 << 10
	defer func() { memberOriginUploadMax = old }()

	send := func(body io.Reader) int {
		req := httptest.NewRequest(http.MethodPost, "https://console.example/api/agents/memory/import", body)
		req.Header.Set("Content-Type", "multipart/form-data; boundary=B")
		req.Header.Set(memberOriginConsoleHeader, "1")
		rec := httptest.NewRecorder()
		proxy.rest(deadlineRecorder{rec}, req, res)
		return rec.Code
	}
	empty := func(when string) {
		t.Helper()
		if left, _ := os.ReadDir(tmp); len(left) != 0 {
			t.Fatalf("%s: %d spool files left", when, len(left))
		}
		if uploadsActive != 0 || len(uploadsByMember) != 0 {
			t.Fatalf("%s: upload slots still held", when)
		}
	}

	sb := &slowBody{r: strings.NewReader("small"), wait: 1200 * time.Millisecond}
	if code := send(sb); code != http.StatusOK {
		t.Fatalf("slow upload: %d", code)
	}
	if c := decodeMark(t, pub, gotMark); c.IssuedAt < sb.doneAt.Unix() {
		t.Fatalf("iat %d is before the body was read (%d)", c.IssuedAt, sb.doneAt.Unix())
	}
	empty("after success")
	if code := send(strings.NewReader(strings.Repeat("x", 2<<10))); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d", code)
	}
	empty("after 413")
	if code := send(&slowBody{r: strings.NewReader("part"), fail: io.ErrUnexpectedEOF}); code != http.StatusBadRequest {
		t.Fatalf("dropped connection: %d", code)
	}
	empty("after a dropped connection")

	// One upload per membership, and a global bound.
	if !acquireUploadSlot("mb-a") || acquireUploadSlot("mb-a") {
		t.Fatal("a membership must hold one slot")
	}
	for i := 1; i < memberOriginUploadSlots; i++ {
		if !acquireUploadSlot("mb-" + strconv.Itoa(i)) {
			t.Fatalf("slot %d refused", i)
		}
	}
	if acquireUploadSlot("mb-extra") {
		t.Fatal("the global bound did not hold")
	}
	releaseUploadSlot("mb-a")
	for i := 1; i < memberOriginUploadSlots; i++ {
		releaseUploadSlot("mb-" + strconv.Itoa(i))
	}
	if !acquireUploadSlot(res.ws.MembershipID) {
		t.Fatal("slot not free")
	}
	if code := send(strings.NewReader("x")); code != http.StatusTooManyRequests {
		t.Fatalf("second upload of one member: %d", code)
	}
	releaseUploadSlot(res.ws.MembershipID)
	empty("after the slot test")
}

// A stalled upload is dropped (408) and gives its slot back; one that keeps moving is not.
func TestHashedUploadStallFreesTheSlot(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer cleanup()
	oldIdle := memberOriginUploadIdle
	memberOriginUploadIdle = 300 * time.Millisecond
	defer func() { memberOriginUploadIdle = oldIdle }()
	// The production wrappers, outermost first (main.go): they hide the connection unless they
	// Unwrap, and browsers send Accept-Encoding: gzip on a POST too.
	srv := httptest.NewServer(logRequests(gzipMiddleware(etagJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.rest(w, r, res)
	})))))
	defer srv.Close()

	post := func(body io.Reader) (int, error) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/agents/memory/import", body)
		req.Header.Set("Content-Type", "multipart/form-data; boundary=B")
		req.Header.Set(memberOriginConsoleHeader, "1")
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return resp.StatusCode, nil
	}
	// Sends a little and then nothing, without closing.
	pr, pw := io.Pipe()
	go func() { _, _ = pw.Write([]byte("--B\r\n")) }()
	code, err := post(pr)
	_ = pw.Close()
	if err != nil || code != http.StatusRequestTimeout {
		t.Fatalf("stalled upload: %d %v", code, err)
	}
	uploadMu.Lock()
	held := uploadsActive
	uploadMu.Unlock()
	if held != 0 {
		t.Fatalf("%d slots still held after the stall", held)
	}
	// Slow but moving: each gap is under the idle limit, the total is over it.
	pr2, pw2 := io.Pipe()
	go func() {
		for i := 0; i < 4; i++ {
			_, _ = pw2.Write([]byte("x"))
			time.Sleep(150 * time.Millisecond)
		}
		_ = pw2.Close()
	}()
	if code, err := post(pr2); err != nil || code != http.StatusOK {
		t.Fatalf("slow upload: %d %v", code, err)
	}
}

// Without a way to set a read deadline the upload is refused rather than left unbounded.
func TestHashedUploadRefusedWhenNoDeadlineCanBeSet(t *testing.T) {
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer cleanup()
	req := httptest.NewRequest(http.MethodPost, "https://console.example/api/agents/memory/import", strings.NewReader("x"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=B")
	req.Header.Set(memberOriginConsoleHeader, "1")
	rec := httptest.NewRecorder() // no SetReadDeadline
	proxy.rest(rec, req, res)
	if rec.Code != http.StatusInternalServerError || uploadsActive != 0 {
		t.Fatalf("code %d, slots %d", rec.Code, uploadsActive)
	}
}

// Every settings write is signed, a mark a browser sent on it is replaced, and the Agent receives
// the same body: the Agent asks for the mark only when the write arms the memory review.
func TestRelaySignsSettingsWrites(t *testing.T) {
	var gotMark, gotBody, gotMethod string
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotMark, gotBody, gotMethod = r.Header.Get(memberOriginHeader), string(b), r.Method
		_, _ = w.Write([]byte(`{}`))
	}))
	defer cleanup()
	t.Setenv(memberOriginKeyVersionEnv, "")
	pub := memberOriginKey(proxy.mgr.tokenSignMaster(), 1).Public().(ed25519.PublicKey)
	body := `{"agentMemory":true,"memoryReview":"propose"}`
	req := httptest.NewRequest(http.MethodPut, "/api/env/ui-prefs", strings.NewReader(body))
	req.Header.Set(memberOriginHeader, "v1.forged.byTheBrowser")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.rest(rec, req, res)
	if rec.Code != http.StatusOK || gotMethod != http.MethodPut || gotBody != body {
		t.Fatalf("%d method %q body %q", rec.Code, gotMethod, gotBody)
	}
	c := decodeMark(t, pub, gotMark)
	sum := sha256.Sum256([]byte(body))
	if c.Method != "PUT" || c.Path != "/env/ui-prefs" || c.BodySHA256 != hex.EncodeToString(sum[:]) || c.Subject != "user1" {
		t.Fatalf("claims = %+v", c)
	}
}

// The Agent clamps AF_MEMORY_IMPORT_MAX to 128 MiB (memoryImportCeiling in workspace/agent, a
// separate module) and adds 1 MiB of multipart framing; the spool limit must admit that body.
func TestMemberOriginUploadMaxCoversAgentCeiling(t *testing.T) {
	const agentCeiling, agentFraming = 128 << 20, 1 << 20
	if memberOriginUploadMax < agentCeiling+agentFraming {
		t.Fatalf("memberOriginUploadMax %d is below the Agent ceiling %d + framing %d", memberOriginUploadMax, agentCeiling, agentFraming)
	}
}
