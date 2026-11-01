package opencode

// The client layer for the shared `opencode serve` daemon: authentication, the health probe and
// the route table. serve.go and driver.go call these helpers instead of spelling a path, so a
// route lives in exactly one place. Only opencode 2 is supported (ADR 0127).
//
// Measured (2.0.28, with OPENCODE_SERVER_PASSWORD set; user `opencode`):
//   - It answers 401 to every request without Basic auth, the SSE stream included.
//   - Paths it does not serve answer 200 text/html (the web UI), an opencode 1 path included.
//     A status-code-only health check therefore accepts any server, so the probe passes only
//     on a JSON /api/info that names a 2.x version (healthyV2).
//   - v2 has no question endpoints (they became forms) and its PATCH /api/experimental/config
//     takes `shell` only; a provider block is refused with 400. A change that has to reach
//     a running v2 daemon therefore needs a restart (ErrLiveConfigUnsupported).
//   - v2's session list filters on `directory`; `location[directory]` is accepted and
//     silently ignored (returns every project's sessions).
//
// Reading a session's history, queue and outcome on v2 is v2state.go; it needs no route here
// beyond the paths it builds from the same address and authenticated client.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// serveUser is the Basic auth user opencode expects (OPENCODE_SERVER_USERNAME default).
const serveUser = "opencode"

// ErrLiveConfigUnsupported: the running daemon cannot take this configuration change over
// HTTP, so only a new process applies it.
var ErrLiveConfigUnsupported = errors.New("opencode serve cannot take this change live")

// --- authentication -----------------------------------------------------------

var (
	pwMu     sync.Mutex
	pw       string
	pwLoaded bool
)

// servePasswordFile holds the password of the daemon this Agent started, so an Agent that
// restarts can still adopt it (it lives in the config root: it is a credential).
func servePasswordFile() string {
	return filepath.Join(paths.AgentConfigDir(), "opencode-serve-password")
}

// servePassword returns the password the current daemon expects, or "" when none is known
// (a daemon an older Agent started has none).
func servePassword() string {
	pwMu.Lock()
	defer pwMu.Unlock()
	if !pwLoaded {
		pwLoaded = true
		if b, err := os.ReadFile(servePasswordFile()); err == nil {
			pw = strings.TrimSpace(string(b))
		}
	}
	return pw
}

// genServePassword makes a candidate password: 256 random bits, never logged.
func genServePassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// useServePassword switches the password requests carry, without touching the file.
func useServePassword(p string) {
	pwMu.Lock()
	pw, pwLoaded = p, true
	pwMu.Unlock()
}

// persistServePassword writes the file atomically (tmp + rename): a half-written file would
// lock a restarted Agent out of the daemon exactly like a lost one.
func persistServePassword(p string) error {
	f := servePasswordFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, []byte(p+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

// newServePassword generates, adopts and persists a password in one step. ensure does not
// use it: it must not forget the old password before the new daemon has proved itself.
func newServePassword() (string, error) {
	p, err := genServePassword()
	if err != nil {
		return "", err
	}
	if err := persistServePassword(p); err != nil {
		return "", err
	}
	useServePassword(p)
	return p, nil
}

// serveRejectsAuth reports that something on addr answers 401: a daemon is there and holds
// a password this Agent does not have. Starting another one would only collide on the port.
func serveRejectsAuth(addr string) bool {
	res, err := serveClient.Get(addr + "/api/info")
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusUnauthorized
}

// authTransport adds Basic auth to every request that goes to the serve daemon. Wrapping
// the transport (rather than each call site) is what keeps the SSE stream, the turn calls
// and the OAuth calls from each needing to remember it.
type authTransport struct{ base http.RoundTripper }

func (t authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if p := servePassword(); p != "" && req.Header.Get("Authorization") == "" && sameServeHost(req.URL) {
		req = req.Clone(req.Context())
		req.SetBasicAuth(serveUser, p)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// sameServeHost keeps the password from leaving for any host but the daemon's own.
func sameServeHost(u *url.URL) bool {
	want, err := url.Parse(serveAddr())
	return err == nil && want.Host == u.Host
}

// --- health ---------------------------------------------------------------------

// serveSeen is set once a probe has found an opencode 2 daemon answering. Before that (the Agent
// has just restarted and nothing has started a daemon yet) the store decides where history is
// read from (v2Served).
var serveSeen atomic.Bool

// healthyV2 accepts only a JSON /api/info that carries a version of major 2 or later.
func healthyV2(addr string) bool {
	body, ok := fetchJSON(addr + "/api/info")
	if !ok {
		return false
	}
	var info struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(body, &info) != nil || info.Version == "" {
		return false
	}
	major, _, _ := strings.Cut(info.Version, ".")
	n, err := strconv.Atoi(major)
	return err == nil && n >= 2
}

// fetchJSON is a 200 whose Content-Type is JSON; anything else (an HTML page, 401) is not.
func fetchJSON(u string) ([]byte, bool) {
	res, err := serveClient.Get(u)
	if err != nil {
		return nil, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	return b, err == nil
}

// probeServe reports whether an opencode 2 daemon answers at addr.
func probeServe(addr string) bool {
	if healthyV2(addr) {
		serveSeen.Store(true)
		return true
	}
	return false
}

// --- route table ----------------------------------------------------------------

func eventURL(addr string) string { return addr + "/api/event" }

func sessionURL(addr, ses string) string { return addr + "/api/session/" + url.PathEscape(ses) }

func abortURL(addr, ses string) string {
	return addr + "/api/session/" + url.PathEscape(ses) + "/interrupt"
}

func forkURL(addr, ses string) string { return addr + "/api/session/" + url.PathEscape(ses) + "/fork" }

// forkBody is the fork request; at is the message the copy stops BEFORE.
func forkBody(at string) (string, error) {
	if at == "" {
		return "{}", nil
	}
	b, err := json.Marshal(map[string]string{"before": at})
	return string(b), err
}

// createSessionRequest returns the URL and body that create a session in dir.
func createSessionRequest(addr, dir, title string) (string, []byte) {
	// The allow-all ruleset is the --auto of the TUI: the container is the sandbox and
	// nobody can answer a prompt from chat.
	// metadata marks the session as af's Managed one: the env-gate plugin lets a session
	// with no af tag wait for a Terminal pane's environment, and this one has none to wait for.
	b, _ := json.Marshal(map[string]any{
		"title": title, "location": map[string]string{"directory": dir},
		"permissions": []map[string]string{{"action": "*", "resource": "*", "effect": "allow"}},
		"metadata":    map[string]string{metaManaged: "1"},
	})
	return addr + "/api/session", b
}

// sessionID extracts the id from a create/fork response, which wraps it in {"data":{…}}.
func sessionID(body io.Reader) string {
	var s struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(body).Decode(&s) != nil {
		return ""
	}
	return s.Data.ID
}

// activeSessions lists the sessions the daemon is running, id → status type. An idle
// session is not listed (measured). The list is global, not per project.
func activeSessions(addr string) (map[string]string, error) {
	res, err := serveClient.Get(addr + "/api/session/active")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("active sessions: HTTP %d", res.StatusCode)
	}
	type st struct {
		Type string `json:"type"`
	}
	var w struct {
		Data map[string]st `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&w); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(w.Data))
	for id, s := range w.Data {
		out[id] = s.Type
	}
	return out, nil
}

func permissionReplyRequest(addr, ses, id string) (string, string) {
	return addr + "/api/session/" + url.PathEscape(ses) + "/permission/" + url.PathEscape(id) + "/reply", `{"decision":"always"}`
}

// --- turns ----------------------------------------------------------------------

// newTurnRequest builds the request that submits one input: POST /api/session/{id}/prompt, which
// takes text and files only (agent and model are session state, see sendTurn).
func newTurnRequest(addr, ses string, in agents.TurnInput) (*http.Request, error) {
	body := map[string]any{"text": strings.TrimSpace(in.Prompt)}
	var files []map[string]string
	for _, p := range in.Attachments {
		if strings.TrimSpace(p) != "" {
			files = append(files, map[string]string{"uri": "file://" + p, "name": filepath.Base(p)})
		}
	}
	if len(files) > 0 {
		body["files"] = files
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return http.NewRequest("POST", addr+"/api/session/"+url.PathEscape(ses)+"/prompt", bytes.NewReader(buf))
}

// v2 turn pacing. /prompt only queues the input (its answer is the stored user message), so the
// end of the turn is read off /api/session/active: the session has to be seen running, then
// gone. How it ended is explicit (measured 2.0.28, testdata/v2/): the execution leaves an idle
// message behind with outcome succeeded | failed | interrupted and, on a failure, the error. The
// session.execution.* events on the stream only wake the loop earlier than its tick.
var (
	v2PollEvery   = 500 * time.Millisecond
	v2StartWithin = 10 * time.Second
	v2PollMisses  = 3
)

// sendTurn submits the input and returns once the turn has ended, as a body runTurn can read an
// error or an abort out of. A transport failure (the daemon went away) is an error.
func sendTurn(addr, ses string, req *http.Request, st agents.ThreadSettings) (*http.Response, error) {
	// Armed before the prompt goes out, so the start event cannot slip between the two.
	events, stop := watchExec(ses)
	defer stop()
	if err := applyV2Settings(addr, ses, st); err != nil {
		return nil, err
	}
	res, err := turnClient.Do(req)
	if err != nil || res.StatusCode >= 300 {
		return res, err
	}
	promptBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	var userMsg struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(promptBody, &userMsg)
	gen := Serve().Generation()
	seen, misses, outMisses := false, 0, 0 // failed active reads / failed outcome reads, in a row
	lastLive := time.Now()                 // the last moment the turn was known to be alive or not yet due
	for {
		m, err := activeSessions(addr)
		// A new generation means the daemon this turn ran on is gone, whatever the new one
		// reports. Checked after the read so
		// that an empty list from the replacement is never taken for "finished".
		if Serve().Generation() != gen {
			return nil, errors.New("opencode serve was replaced during the turn")
		}
		running := err == nil && m[ses] != ""
		if running {
			seen, misses, lastLive = true, 0, time.Now()
		} else if err != nil {
			if misses++; misses >= v2PollMisses {
				return nil, err
			}
		}
		// The record of THIS input's execution decides, whether or not the session is running:
		// a queued input behind it may already have started the next execution, and only
		// the first idle message after our input is ours. Only an explicit outcome ends the
		// turn; a record that cannot be read or does not appear is unknown, never success.
		if err == nil && (!running || seen) {
			kind, e, found, oerr := v2OutcomeFor(addr, ses, userMsg.Data.ID)
			switch {
			case oerr != nil:
				if outMisses++; outMisses >= v2PollMisses {
					return nil, fmt.Errorf("opencode v2 turn outcome unreadable: %w", oerr)
				}
			case found:
				return v2OutcomeResponse(kind, e), nil
			case !running && time.Since(lastLive) > v2StartWithin:
				return nil, errors.New("opencode v2 turn state unknown: no outcome was recorded")
			default:
				outMisses = 0 // read fine, just not recorded yet
			}
		}
		select {
		case <-events: // look at the daemon now instead of at the next tick
		case <-time.After(v2PollEvery):
		}
	}
}

func v2OutcomeResponse(kind string, e *v2Err) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(v2OutcomeBody(kind, e)))}
}

// applyV2Settings pushes the agent and the model, which opencode 2 keeps on the session rather
// than on each prompt.
func applyV2Settings(addr, ses string, st agents.ThreadSettings) error {
	post := func(path string, body any) error {
		buf, _ := json.Marshal(body)
		res, err := serveClient.Post(addr+"/api/session/"+url.PathEscape(ses)+path, "application/json", bytes.NewReader(buf))
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode >= 300 {
			return fmt.Errorf("opencode session%s: HTTP %d", path, res.StatusCode)
		}
		return nil
	}
	if ag := agentForMode(st.Mode); ag != "" {
		if err := post("/agent", map[string]string{"agent": ag}); err != nil {
			return err
		}
	}
	if prov, model, ok := splitModel(st.Model); ok {
		ref := map[string]string{"providerID": prov, "id": model}
		if st.Effort != "" {
			ref["variant"] = st.Effort
		}
		return post("/model", map[string]any{"model": ref})
	}
	return nil
}
