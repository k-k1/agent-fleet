package main

// member_origin.go — the mark the CP puts on a member's Console request it relays to the Agent,
// so the Agent can tell it from a session (ADR 0113).
//
// AGENT_TOKEN cannot: every session's shell holds it, and X-AF-Relay is a hint any caller can set.
// A shared secret delivered to the Agent would not do either: sessions run as the Agent's uid and
// can read whatever it reads. So the mark is asymmetric. The CP signs with an Ed25519 key derived
// from the token-signing master, which never leaves the CP; the Agent is given only the public
// keys, in its start env, and a session that reads them can verify but not sign.
//
// The signature covers the method, the Agent-side path, the raw query, the body hash, the
// workspace, the membership, the identity, a 60 s expiry and a nonce the Agent remembers until
// the expiry. Only memberOriginRoutes (the relay rows marked sign, relays.go) are signed; every
// other relayed request has the header removed.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	memberOriginHeader = "X-AF-Member-Origin"
	// memberOriginTTL is how long a mark is good for. The relay sends it at once; the margin is
	// for a slow Agent and clock skew, and the Agent keeps each nonce this long.
	memberOriginTTL = 60 * time.Second
	// memberOriginMaxBody bounds what the CP buffers to hash. It covers the Agent's own save
	// limit (agentMemSaveMaxRequest, about 1.3 MiB); a larger body is refused here.
	memberOriginMaxBody = 2 << 20
	// memberOriginKeyVersionEnv selects the signing key. Raising it by one rotates: workspaces
	// were started with this version's public key and the next one's, so the running ones keep
	// verifying, and the old private key is never used again.
	memberOriginKeyVersionEnv = "AF_MEMBER_ORIGIN_KEY_VERSION"
)

// memberOriginRoutes are the relayed routes the CP signs, and memberOriginHashedRoutes the subset
// whose body is a multipart upload too large to buffer: the CP spools it to a file while hashing
// it, signs the hash, and relays the file; the Agent hashes what it receives and checks it against
// the mark. Both are derived from the relay rows' sign field (relays.go). A route that is not
// signed there never carries a mark, so adding a member route to the Agent without signing it
// leaves it refused rather than open.
var memberOriginRoutes, memberOriginHashedRoutes = signedRelayRoutes()

// memberOriginUploadMax bounds a hashed upload. The Agent's import limit is 64 MiB by default
// (AF_MEMORY_IMPORT_MAX) and the Agent clamps it to 128 MiB (memoryImportCeiling in
// workspace/agent/internal/memoryx); this leaves room for that plus the multipart framing, so a
// configured cap never meets a 413 from here. A larger body is refused here with 413 and nothing
// reaches the Agent. The two modules cannot share the constant: TestMemberOriginUploadMaxCoversAgentCeiling
// here and TestMemoryImportCeilingFitsControlPlane there pin the pair.
var memberOriginUploadMax int64 = 130 << 20 // a var so tests can lower it

// memberOriginConsoleHeader must accompany a hashed upload. The body is multipart/form-data,
// which a cross-origin page can send without a preflight, so unlike the JSON routes the content
// type does not keep other pages out; a custom header does (it forces a preflight the CP never
// answers). The Console's own fetch sets it.
const memberOriginConsoleHeader = "X-AF-Console"

func memberOriginHashedRoute(r *http.Request) bool {
	return memberOriginHashedRoutes[r.Method+" "+r.URL.Path]
}

func memberOriginRoute(r *http.Request) bool { return memberOriginRoutes[r.Method+" "+r.URL.Path] }

// memberOriginClaims is what is signed; the Agent's memberorigin.Claims is the same shape.
type memberOriginClaims struct {
	Method     string `json:"m"`
	Path       string `json:"p"`
	Query      string `json:"q"`
	BodySHA256 string `json:"b"`
	Workspace  string `json:"ws"`
	Membership string `json:"mb"`
	Subject    string `json:"sub"`
	IssuedAt   int64  `json:"iat"`
	Expires    int64  `json:"exp"`
	Nonce      string `json:"n"`
}

// memberOriginKeyVersion is the signing key's version; 0 turns signing off (an unparsable value
// does too, and says so once), and the Agent then refuses the member routes.
func memberOriginKeyVersion() int {
	raw := strings.TrimSpace(os.Getenv(memberOriginKeyVersionEnv))
	if raw == "" {
		return 1
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		log.Printf("%s=%q is not a key version; member-origin signing is off", memberOriginKeyVersionEnv, raw)
		return 0
	}
	return v
}

func memberOriginKID(v int) string { return "v" + strconv.Itoa(v) }

// memberOriginKey derives version v's key pair. A leaked private key of one version says nothing
// about the next: each is its own HMAC of the master.
func memberOriginKey(master32 []byte, v int) ed25519.PrivateKey {
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-member-origin-sign/" + memberOriginKID(v)))
	return ed25519.NewKeyFromSeed(mac.Sum(nil))
}

// memberOriginEnv is what a workspace is started with: the current and the next public key, its
// own workspace id and its membership id. Nothing secret.
func memberOriginEnv(master32 []byte, v int, wsID, membershipID string) []string {
	if v <= 0 || wsID == "" || membershipID == "" {
		return nil
	}
	pub := func(v int) string {
		return memberOriginKID(v) + ":" +
			base64.RawURLEncoding.EncodeToString(memberOriginKey(master32, v).Public().(ed25519.PublicKey))
	}
	return []string{
		"AF_MEMBER_ORIGIN_KEYS=" + pub(v) + "," + pub(v+1),
		"AF_WORKSPACE_ID=" + wsID,
		"AF_MEMBERSHIP_ID=" + membershipID,
	}
}

// signMemberOrigin encodes and signs claims into the header value.
func signMemberOrigin(priv ed25519.PrivateKey, kid string, c memberOriginClaims) string {
	payload, _ := json.Marshal(c)
	sig := ed25519.Sign(priv, append([]byte("af-member-origin/v1\n"), payload...))
	return kid + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// memberOriginMark reads the body of a relayed member route and returns it with the mark for it.
// The workspace must be the caller's own: the store it writes is that member's memory.
func (m *manager) memberOriginMark(r *http.Request, res *resolved, now time.Time) ([]byte, string, *apiError) {
	if res.ws.MembershipID == "" || res.ws.MembershipID != res.mv.MembershipID {
		return nil, "", &apiError{http.StatusForbidden, "forbidden", "only the workspace's own member can change its memory"}
	}
	if aerr := memberOriginRequestCheck(r); aerr != nil {
		return nil, "", aerr
	}
	return m.memberOriginMarkBuffered(r, res, now)
}

func (m *manager) memberOriginMarkBuffered(r *http.Request, res *resolved, now time.Time) ([]byte, string, *apiError) {
	body, err := io.ReadAll(io.LimitReader(r.Body, memberOriginMaxBody+1))
	if err != nil {
		return nil, "", &apiError{http.StatusBadRequest, "bad_request", "could not read the request body"}
	}
	if len(body) > memberOriginMaxBody {
		return nil, "", &apiError{http.StatusRequestEntityTooLarge, "too_large", "request body is too large"}
	}
	mark, aerr := m.memberOriginSign(res, r.Method, strings.TrimPrefix(r.URL.Path, "/api"), r.URL.RawQuery, body, now)
	if aerr != nil {
		return nil, "", aerr
	}
	return body, mark, nil
}

// memberOriginSign marks one request to res's Agent: method, Agent-side path, raw query and the
// exact body. "" with no error means signing is off, and the Agent then refuses the route. The
// relay above uses it for a member's Console request; a CP handler uses it directly for an Agent
// call it makes on the member's own request (the handoff push), after checking that the workspace
// is the caller's own.
func (m *manager) memberOriginSign(res *resolved, method, path, query string, body []byte, now time.Time) (string, *apiError) {
	sum := sha256.Sum256(body)
	return m.memberOriginSignHash(res, method, path, query, hex.EncodeToString(sum[:]), now)
}

// memberOriginSignHash is memberOriginSign for a body already hashed (hex SHA-256).
func (m *manager) memberOriginSignHash(res *resolved, method, path, query, bodySHA256 string, now time.Time) (string, *apiError) {
	v := memberOriginKeyVersion()
	if v <= 0 {
		return "", nil
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", &apiError{http.StatusInternalServerError, "internal", "could not sign the request"}
	}
	c := memberOriginClaims{
		Method: method, Path: path, Query: query,
		BodySHA256: bodySHA256, Workspace: res.ws.ID, Membership: res.ws.MembershipID,
		Subject: res.ident.ID, IssuedAt: now.Unix(), Expires: now.Add(memberOriginTTL).Unix(),
		Nonce: base64.RawURLEncoding.EncodeToString(nonce),
	}
	return signMemberOrigin(memberOriginKey(m.tokenSignMaster(), v), memberOriginKID(v), c), nil
}

// relayBody is the body and mark a relayed request goes out with: the member routes are buffered
// and signed, everything else streams through untouched and unmarked.
func (m *manager) relayBody(w http.ResponseWriter, r *http.Request, res *resolved) (io.Reader, string, *apiError) {
	if !memberOriginRoute(r) {
		return r.Body, "", nil
	}
	if memberOriginHashedRoute(r) {
		return m.hashedUploadMark(w, r, res)
	}
	body, mark, aerr := m.memberOriginMark(r, res, time.Now())
	if aerr != nil {
		return nil, "", aerr
	}
	return bytes.NewReader(body), mark, nil
}

// stripAgentRelayCredentials removes from a relayed request what the browser sent that the Agent
// has no use for and a workspace must not see: the Console's and a front gateway's login cookies
// and identity headers (preview's stripConsoleCredentials), every other cookie (no Agent route
// reads one), and any member-origin mark. A session that obtained the member's login cookie could
// otherwise have the CP sign whatever it sends. The caller sets the Agent bearer afterwards.
func stripAgentRelayCredentials(out, in *http.Request, identityHeader string) {
	stripConsoleCredentials(out, in, identityHeader)
	out.Header.Del("Cookie")
}

// memberOriginRequestCheck refuses, before anything is signed, a request a page other than the
// Console could have made with the member's cookie. A preview app on its own host is same-site, so
// SameSite=Lax still sends the cookie; a text/plain no-cors POST from it needs no preflight. So:
// the body must be JSON (a cross-origin page cannot send that without a preflight, and the CP
// answers none), Sec-Fetch-Site, when the browser sends it, must be same-origin, and Origin, when
// sent, must be the Console's own host. A caller with neither header is not a browser; it needs the
// member's login cookie, which the relays keep out of the workspace.
func memberOriginRequestCheck(r *http.Request) *apiError {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return &apiError{http.StatusUnsupportedMediaType, "unsupported_media_type", "member writes take application/json"}
	}
	return memberOriginOriginCheck(r)
}

// memberOriginOriginCheck is the browser-origin half of memberOriginRequestCheck.
func memberOriginOriginCheck(r *http.Request) *apiError {
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" {
		return &apiError{http.StatusForbidden, "forbidden", "member writes are accepted from the Console's own origin only"}
	}
	if r.Header.Get("Origin") != "" && !checkWSOrigin(r) {
		return &apiError{http.StatusForbidden, "forbidden", "member writes are accepted from the Console's own origin only"}
	}
	return nil
}

// An upload that makes no progress for memberOriginUploadIdle, or runs past memberOriginUploadTotal,
// is dropped so that it cannot hold one of the few spool slots indefinitely (the http.Server has no
// body read timeout). Vars so tests can shorten them.
var (
	memberOriginUploadIdle  = 60 * time.Second
	memberOriginUploadTotal = 30 * time.Minute
)

// progressReader pushes the connection's read deadline out before each read, so only a stall
// (not a slow but moving upload) trips it, and enforces the overall limit.
type progressReader struct {
	r        io.Reader
	rc       *http.ResponseController
	idle     time.Duration
	deadline time.Time
	expired  bool
}

func (p *progressReader) Read(b []byte) (int, error) {
	now := time.Now()
	if now.After(p.deadline) {
		p.expired = true
		return 0, os.ErrDeadlineExceeded
	}
	d := now.Add(p.idle)
	if d.After(p.deadline) {
		d = p.deadline
	}
	if err := p.rc.SetReadDeadline(d); err != nil {
		return 0, err
	}
	return p.r.Read(b)
}

// memberOriginNow is the clock the upload mark is taken from; a var so a test can watch it.
var memberOriginNow = time.Now

// Spooled uploads occupy the CP's temp volume (a size-limited emptyDir on Kubernetes, where
// exceeding it evicts the Pod), so they are bounded: a few at once overall and one per membership.
// A request over either bound is refused before anything is written.
const memberOriginUploadSlots = 4

var (
	uploadMu        sync.Mutex
	uploadsActive   int
	uploadsByMember = map[string]bool{}
)

func acquireUploadSlot(membership string) bool {
	uploadMu.Lock()
	defer uploadMu.Unlock()
	if uploadsActive >= memberOriginUploadSlots || uploadsByMember[membership] {
		return false
	}
	uploadsActive++
	uploadsByMember[membership] = true
	return true
}

func releaseUploadSlot(membership string) {
	uploadMu.Lock()
	defer uploadMu.Unlock()
	uploadsActive--
	delete(uploadsByMember, membership)
}

// spooledUpload is a hashed upload on disk; the first Close removes the file and frees the slot.
type spooledUpload struct {
	*os.File
	once       *sync.Once
	membership string
}

func (u spooledUpload) Close() error {
	err := u.File.Close()
	u.once.Do(func() {
		_ = os.Remove(u.File.Name())
		releaseUploadSlot(u.membership)
	})
	return err
}

// hashedUploadMark spools a member's multipart upload to a file while hashing it, and returns the
// file (rewound; the relay closes it) with the mark over the hash. The same ownership and
// same-origin checks as the JSON routes apply, with the custom header standing in for the JSON
// content type.
func (m *manager) hashedUploadMark(w http.ResponseWriter, r *http.Request, res *resolved) (io.Reader, string, *apiError) {
	if res.ws.MembershipID == "" || res.ws.MembershipID != res.mv.MembershipID {
		return nil, "", &apiError{http.StatusForbidden, "forbidden", "only the workspace's own member can change its memory"}
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "multipart/form-data" {
		return nil, "", &apiError{http.StatusUnsupportedMediaType, "unsupported_media_type", "this upload takes multipart/form-data"}
	}
	if r.Header.Get(memberOriginConsoleHeader) == "" {
		return nil, "", &apiError{http.StatusForbidden, "forbidden", "member writes are accepted from the Console only"}
	}
	if aerr := memberOriginOriginCheck(r); aerr != nil {
		return nil, "", aerr
	}
	if !acquireUploadSlot(res.ws.MembershipID) {
		return nil, "", &apiError{http.StatusTooManyRequests, "too_many_uploads", "another memory import is in progress; try again when it finishes"}
	}
	f, err := os.CreateTemp("", "af-member-upload-*")
	if err != nil {
		releaseUploadSlot(res.ws.MembershipID)
		return nil, "", &apiError{http.StatusInternalServerError, "internal", "could not buffer the upload"}
	}
	fail := func(aerr *apiError) (io.Reader, string, *apiError) {
		spooledUpload{f, new(sync.Once), res.ws.MembershipID}.Close()
		return nil, "", aerr
	}
	h := sha256.New()
	pr := &progressReader{r: io.LimitReader(r.Body, memberOriginUploadMax+1), rc: http.NewResponseController(w),
		idle: memberOriginUploadIdle, deadline: time.Now().Add(memberOriginUploadTotal)}
	n, err := io.Copy(io.MultiWriter(f, h), pr)
	_ = pr.rc.SetReadDeadline(time.Time{})
	if errors.Is(err, http.ErrNotSupported) {
		// No deadline could be set (a wrapper hides the connection): a stalled upload would hold
		// its slot for good, so it is not accepted at all.
		log.Printf("member upload refused: the response writer %T cannot set a read deadline", w)
		return fail(&apiError{http.StatusInternalServerError, "internal", "could not bound the upload"})
	}
	if err != nil {
		if pr.expired || errors.Is(err, os.ErrDeadlineExceeded) {
			return fail(&apiError{http.StatusRequestTimeout, "upload_timeout", "the upload stalled or took too long"})
		}
		return fail(&apiError{http.StatusBadRequest, "bad_request", "could not read the request body"})
	}
	if n > memberOriginUploadMax {
		return fail(&apiError{http.StatusRequestEntityTooLarge, "too_large", "request body is too large"})
	}
	// Taken after the body is read: the mark lives 60 s and a slow upload can take longer than that.
	mark, aerr := m.memberOriginSignHash(res, r.Method, strings.TrimPrefix(r.URL.Path, "/api"), r.URL.RawQuery, hex.EncodeToString(h.Sum(nil)), memberOriginNow())
	if aerr != nil {
		return fail(aerr)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fail(&apiError{http.StatusInternalServerError, "internal", "could not buffer the upload"})
	}
	return spooledUpload{f, new(sync.Once), res.ws.MembershipID}, mark, nil
}
