package mcpreg

// The local relay for OAuth MCP servers (ADR 0119 decisions 6 and 7).
//
// A CLI reads its MCP config once, at start-up, so a bearer token written there dies inside a
// long session, and a CLI that meets a 401 starts its own OAuth flow whose loopback callback the
// member's browser cannot reach. Instead the config names this listener and a relay key; the
// relay holds the grant (AccessToken / RefreshAccessToken) and forwards each request to the
// registered URL. It is transport-level only: no MCP message is parsed, no body is logged.
//
// Rules that must not erode:
//
//   - 127.0.0.1 only, one path per server (/mcp/<server id>), nothing appended to the target.
//   - A request needs a loopback Host (DNS rebinding) AND the relay key (any other local
//     process or web page). Either failure is a bare 403.
//   - Whatever Authorization the CLI sent is dropped; the grant's token is set.
//   - Upstream redirects are never followed and never passed on (a CLI would follow Location with
//     the relay key header to another host): the CLI gets a 502 naming the problem.
//   - The upstream's WWW-Authenticate never reaches the CLI. A 401, a 403, a missing grant all
//     become statuses that cannot start the CLI's own OAuth flow (see relayGrantProblem).
//   - Streamed responses are flushed as they arrive; the connection is aborted, not closed
//     cleanly, when the upstream stream breaks, so the CLI reconnects instead of reading a
//     truncated stream as complete.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// RelayKeyHeader carries the relay key in a materialized config. It is a plain static header, which
// every kind's remote entry already supports.
const RelayKeyHeader = "X-Af-Relay-Key"

// relayMaxBody caps the request body the relay buffers. The body is held so a 401 can be
// retried with a fresh token: MCP requests are single JSON-RPC messages, and a stream-tee would
// have to give up the retry once the first attempt had consumed part of it.
var relayMaxBody int64 = 32 << 20 // a var so a test can lower it

const relayPathPrefix = "/mcp/"

// relayIDRe is what a server id looks like in a path. Registry ids are "mcp-" + hex; the pattern
// is wider only so a tenant row could not be locked out, and narrow enough that nothing in it can
// act as a separator in a header or a URL.
var relayIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// OnGrantChange is called (on its own goroutine) when the relay itself ends a grant, so the
// configs that name it can be rewritten. The registry code cannot import the layer that
// owns "when to materialize", so it is a seam. nil in tests.
var OnGrantChange func()

// relayConfig is what survives a restart. The key is generated once per workspace; the port is
// remembered because a running CLI keeps the config it read at start-up, so a port that moved
// at every boot would break every live session.
type relayConfig struct {
	Key  string `json:"key"`
	Port int    `json:"port,omitempty"`
}

func relayConfigPath() string {
	return filepath.Join(paths.AgentConfigDir(), "mcp-relay.json")
}

func loadRelayConfig() relayConfig {
	var c relayConfig
	if b, err := os.ReadFile(relayConfigPath()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	if len(c.Key) < 32 {
		c = relayConfig{}
	}
	return c
}

func saveRelayConfig(c relayConfig) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(relayConfigPath()), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(relayConfigPath(), b, 0o600)
}

var relay struct {
	mu   sync.RWMutex
	key  string
	port int
}

// relayEndpoint is the base URL and key a materialized config names, while the listener is up.
func relayEndpoint() (base, key string, ok bool) {
	relay.mu.RLock()
	defer relay.mu.RUnlock()
	if relay.port == 0 || relay.key == "" {
		return "", "", false
	}
	return "http://127.0.0.1:" + strconv.Itoa(relay.port) + relayPathPrefix, relay.key, true
}

// StartRelay opens the listener on 127.0.0.1, preferring the port used last time, and serves in
// the background. The returned function closes it. A caller must run it before the first
// materialization of the boot: until it is up no OAuth definition is written anywhere.
func StartRelay() (stop func(), err error) {
	cfg := loadRelayConfig()
	if cfg.Key == "" {
		var kb [32]byte
		if _, err := rand.Read(kb[:]); err != nil {
			return nil, err
		}
		cfg.Key = hex.EncodeToString(kb[:])
	}
	var ln net.Listener
	if cfg.Port != 0 {
		ln, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(cfg.Port))
		if err != nil {
			log.Printf("mcp relay: port %d is taken (%v); choosing another, the configs are rewritten", cfg.Port, err)
		}
	}
	if ln == nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return nil, err
		}
	}
	cfg.Port = ln.Addr().(*net.TCPAddr).Port
	if err := saveRelayConfig(cfg); err != nil {
		_ = ln.Close()
		return nil, err
	}
	srv := &http.Server{
		Handler:           relayHandler(cfg.Key),
		ReadHeaderTimeout: 15 * time.Second,
	}
	relay.mu.Lock()
	relay.key, relay.port = cfg.Key, cfg.Port
	relay.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("mcp relay: %v", err)
		}
	}()
	return func() {
		relay.mu.Lock()
		relay.key, relay.port = "", 0
		relay.mu.Unlock()
		_ = srv.Close()
	}, nil
}

// relayDef is how an OAuth definition is spelled in a CLI's config: the relay URL and the key
// header, nothing of the real server. ok is false when the definition may not be written now:
// no listener, a server id that is not path-safe, or no live grant.
func relayDef(d ServerDef) (ServerDef, bool) {
	if d.Auth != AuthOAuth || !relayIDRe.MatchString(d.ID) {
		return ServerDef{}, false
	}
	base, key, ok := relayEndpoint()
	if !ok {
		return ServerDef{}, false
	}
	if g := currentGrant(d.ID); g == nil || g.SignInAgain || (g.AccessToken == "" && g.RefreshToken == "") {
		return ServerDef{}, false
	}
	d.URL = base + d.ID
	d.Headers = map[string]string{RelayKeyHeader: key}
	d.Auth = ""
	return d, true
}

func relayHandler(key string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveRelay(w, r, key)
	})
}

func keysEqual(got, want string) bool {
	if want == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func relayHostOK(hostport string) bool {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	}
	return isLoopbackHost(strings.Trim(h, "[]"))
}

func relayFail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "Agent Fleet: "+msg+"\n")
}

const consoleHint = "open the Agent Fleet Console, Settings > MCP servers, and sign in to this server again"

// relayGrantProblem says what the CLI is told when the grant cannot serve the request. It is a
// 502, not a 401: a CLI answers a 401 by trying OAuth against the relay's own origin, and a 403
// reads as a scope problem. 502 means "the upstream failed", which is true, and carries the text.
func relayGrantProblem(w http.ResponseWriter, id string, err error) {
	switch {
	case errors.Is(err, ErrNotSignedIn), errors.Is(err, ErrSignInAgain):
		relayFail(w, http.StatusBadGateway, "this MCP server is not signed in (or its sign-in expired); "+consoleHint)
	default:
		// The authorization server could not be reached or refused for another reason.
		log.Printf("mcp relay: %s: the grant could not be renewed", id)
		relayFail(w, http.StatusBadGateway, "the sign-in for this MCP server could not be renewed right now; try again, and if it persists "+consoleHint)
	}
}

func serveRelay(w http.ResponseWriter, r *http.Request, key string) {
	// Both gates answer the same bare 403 and look at nothing else first: a page that is
	// rebinding a name to 127.0.0.1, or a process that guessed the port, learns nothing.
	if !relayHostOK(r.Host) || !keysEqual(r.Header.Get(RelayKeyHeader), key) {
		relayFail(w, http.StatusForbidden, "forbidden")
		return
	}
	switch r.Method {
	case http.MethodPost, http.MethodGet, http.MethodDelete:
	default:
		w.Header().Set("Allow", "POST, GET, DELETE")
		relayFail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, relayPathPrefix)
	if !strings.HasPrefix(r.URL.Path, relayPathPrefix) || !relayIDRe.MatchString(id) {
		relayFail(w, http.StatusNotFound, "no such MCP server")
		return
	}
	target, err := relayTarget(id)
	if err != nil {
		relayFail(w, http.StatusNotFound, err.Error())
		return
	}

	var body []byte
	if r.Body != nil && r.Method == http.MethodPost {
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, relayMaxBody))
		if err != nil {
			relayFail(w, http.StatusRequestEntityTooLarge, "the request is too large to relay")
			return
		}
	}

	ctx := r.Context()
	token, err := AccessToken(ctx, id)
	if err != nil {
		relayGrantProblem(w, id, err)
		return
	}
	resp, err := relayForward(ctx, r, target, body, token)
	if err != nil {
		relayUpstreamDown(w, id, err)
		return
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		// Once: the token the server just refused is named, so a caller that already renewed
		// it is not followed by a second renewal, and a second 401 ends the exchange.
		next, rerr := RefreshAccessToken(ctx, id, token)
		if rerr != nil {
			relayGrantProblem(w, id, rerr)
			return
		}
		if resp, err = relayForward(ctx, r, target, body, next); err != nil {
			relayUpstreamDown(w, id, err)
			return
		}
		if resp.StatusCode == http.StatusUnauthorized {
			_ = resp.Body.Close()
			relayFail(w, http.StatusBadGateway, "the MCP server refused the renewed sign-in; "+consoleHint)
			return
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		relayFail(w, http.StatusBadGateway, "the MCP server answered with a redirect, which the relay does not follow; check the registered URL")
		return
	}
	if resp.StatusCode == http.StatusForbidden {
		noteInsufficientScope(id, resp.Header.Values("Www-Authenticate"))
	}
	relayCopyResponse(w, r, resp)
}

// noteInsufficientScope records a 403 whose challenge says insufficient_scope, with the scope it
// asked for, so the Console can offer to sign in again with it. The challenge itself is dropped
// by the caller either way: passed on, it would be the very thing that sends a CLI into its own
// step-up flow against the relay. The CLI still sees the 403 and the server's body.
func noteInsufficientScope(id string, challenges []string) {
	for _, c := range parseBearerChallenges(challenges) {
		if strings.EqualFold(c.errCode, "insufficient_scope") && strings.TrimSpace(c.scope) != "" {
			if err := NoteInsufficientScope(id, c.scope); err != nil {
				log.Printf("mcp relay: %s: the scope request could not be recorded", id)
			}
			return
		}
	}
}

// relayTarget resolves the registered URL to forward to. The id is looked up in the user's own
// store: a path that names anything else (a tenant row, a builtin, a server with a static
// header) is not a relayed server.
func relayTarget(id string) (*url.URL, error) {
	s, err := secrets.Load()
	if err != nil {
		return nil, errors.New("the credential store could not be read")
	}
	for _, d := range s.MCP {
		if d.ID != id || d.Auth != AuthOAuth || d.Transport != TransportHTTP {
			continue
		}
		if !d.Enabled {
			return nil, errors.New("this MCP server is switched off")
		}
		u, perr := url.Parse(d.URL)
		if perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			return nil, errors.New("this MCP server's URL is not usable")
		}
		return u, nil
	}
	return nil, errors.New("no such MCP server")
}

// Hop-by-hop headers (RFC 9110 §7.6.1) plus what the relay owns itself.
var relayHopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Connection": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// relayRequestDrop are the request headers that must not reach the server: the CLI's own
// credentials, the relay key, and forwarding metadata that would tell the server about the relay.
var relayRequestDrop = map[string]bool{
	"Authorization": true, "Cookie": true, "Host": true, "Content-Length": true,
	"Origin": true, "Referer": true, "Forwarded": true,
}

func relayDropRequestHeader(name string) bool {
	c := http.CanonicalHeaderKey(name)
	return relayHopHeaders[c] || relayRequestDrop[c] || c == http.CanonicalHeaderKey(RelayKeyHeader) ||
		strings.HasPrefix(c, "X-Forwarded-")
}

// relayTransport is the process's default transport (the workspace's own egress, with its
// proxy and so its allowlist, exactly as the probe) with compression passed through untouched.
func relayTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DisableCompression = true
	return t
}

// relayHTTP is the relay's client. No Timeout: an SSE stream lives as long as the session, and
// the request's context ends it. A test swaps it.
var relayHTTP = &http.Client{
	Transport:     relayTransport(),
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func relayForward(ctx context.Context, in *http.Request, target *url.URL, body []byte, token string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, in.Method, target.String(), rd)
	if err != nil {
		return nil, err
	}
	// Names listed in Connection are hop-by-hop too.
	extra := map[string]bool{}
	for _, v := range in.Header.Values("Connection") {
		for _, f := range strings.Split(v, ",") {
			extra[http.CanonicalHeaderKey(strings.TrimSpace(f))] = true
		}
	}
	for k, vs := range in.Header {
		if relayDropRequestHeader(k) || extra[http.CanonicalHeaderKey(k)] {
			continue
		}
		req.Header[k] = append([]string(nil), vs...)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return relayHTTP.Do(req)
}

func relayUpstreamDown(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, context.Canceled) {
		return // the CLI went away
	}
	// url.Error's text carries the full URL; log the cause only.
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	log.Printf("mcp relay: %s: upstream: %v", id, err)
	relayFail(w, http.StatusBadGateway, "the MCP server could not be reached")
}

func relayCopyResponse(w http.ResponseWriter, r *http.Request, resp *http.Response) {
	extra := map[string]bool{}
	for _, v := range resp.Header.Values("Connection") {
		for _, f := range strings.Split(v, ",") {
			extra[http.CanonicalHeaderKey(strings.TrimSpace(f))] = true
		}
	}
	for k, vs := range resp.Header {
		c := http.CanonicalHeaderKey(k)
		// WWW-Authenticate is the one that matters (see the file header); Set-Cookie and Location
		// would hand the CLI state or a place to go that the relay did not vouch for.
		if relayHopHeaders[c] || extra[c] || c == "Www-Authenticate" || c == "Set-Cookie" || c == "Location" {
			continue
		}
		w.Header()[k] = append([]string(nil), vs...)
	}
	w.WriteHeader(resp.StatusCode)
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			if r.Context().Err() == nil {
				// The upstream stream broke after the headers went out: end the CLI's
				// connection abnormally so it reconnects, rather than a clean EOF it would
				// take for a complete response.
				panic(http.ErrAbortHandler)
			}
			return
		}
	}
}
