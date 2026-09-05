package opencode

// The opencode.ai workspace ID (`wrk_...`), plus what a usage limit tells us about the plan
// window.
//
// Why it is kept at all (docs/log/54 §54.7): the plan page
// `https://opencode.ai/workspace/{wrk}/go` assumes a browser session and a plain GET 302s to
// `/auth/authorize`. opencode.ai exposes no JSON API either (api/* is 404), and while the
// Console-side API is open to a Bearer token (/api/orgs and /api/user answer 401, so the
// route exists; /api/usage is 404), that token lives in opencode's own credential store with
// no way to read it out. The numbers therefore cannot be ingested. What is possible is
//
//	(1) holding the ID so a link to the plan page can be offered, and
//	(2) showing the window information a failure carries when a limit is hit (which window,
//	    when it comes back)
//
// and the ID is needed for both. It is filled in by hand and learned from failures.
//
// Measured:
//   - Out of credit: the message carries
//     `...Manage your billing here: https://opencode.ai/workspace/wrk_x/billing`
//     (the fixture in errors_test.go).
//   - Go plan limit: opencode itself reads responseBody as JSON, takes `metadata.workspace`
//     and `metadata.limitName`, derives the seconds until reset from the `retry-after` header
//     and points at `https://opencode.ai/workspace/{workspace}/go` (measured against the
//     binary). Whether a stored message carries all of that depends on the version, so this
//     reads them as "use whichever one can be found".

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// workspaceIDRe matches the ULID-shaped id (Crockford base32, measured at 26 characters).
var workspaceIDRe = regexp.MustCompile(`\bwrk_[0-9A-HJKMNP-TV-Za-hjkmnp-tv-z]{26}\b`)

// NormalizeWorkspaceID extracts the id out of whatever the user pasted. Pasting the plan
// page's URL as-is is the natural thing to do (and is what happened on a real machine), so
// only the `wrk_...` part is taken. Empty when nothing is found, which the caller treats as
// an input error.
func NormalizeWorkspaceID(s string) string { return workspaceIDRe.FindString(strings.TrimSpace(s)) }

// ValidWorkspaceID reports whether s CONTAINS an opencode workspace id (a URL is fine).
func ValidWorkspaceID(s string) bool { return NormalizeWorkspaceID(s) != "" }

type workspaceState struct {
	ID string `json:"id"`
	// Source is "manual" (entered by the user) or "learned" (picked out of a failure). The
	// distinction exists so that learning never silently overwrites a hand-entered id.
	Source string `json:"source"`
	At     string `json:"at"`
}

var (
	wsIDMu    sync.Mutex
	wsIDCache *workspaceState
)

func workspaceIDPath() string {
	return filepath.Join(paths.AgentDataDir(), "opencode-workspace.json")
}

// WorkspaceID returns the stored id ("" when unknown) and where it came from.
func WorkspaceID() (string, string) {
	wsIDMu.Lock()
	defer wsIDMu.Unlock()
	st := loadWorkspaceIDLocked()
	return st.ID, st.Source
}

func loadWorkspaceIDLocked() workspaceState {
	if wsIDCache != nil {
		return *wsIDCache
	}
	var st workspaceState
	if b, err := os.ReadFile(workspaceIDPath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	// A file written before normalisation existed holds the whole URL; repair it on every read.
	if id := NormalizeWorkspaceID(st.ID); id != st.ID {
		st.ID = id
	}
	if st.ID == "" {
		st = workspaceState{}
	}
	wsIDCache = &st
	return st
}

func saveWorkspaceIDLocked(st workspaceState) error {
	if err := os.MkdirAll(filepath.Dir(workspaceIDPath()), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.WriteFile(workspaceIDPath(), b, 0o600); err != nil {
		return err
	}
	wsIDCache = &st
	return nil
}

// SetWorkspaceID records a manually entered id; an empty value clears it.
func SetWorkspaceID(id string) error {
	id = strings.TrimSpace(id)
	wsIDMu.Lock()
	defer wsIDMu.Unlock()
	if id == "" {
		wsIDCache = &workspaceState{}
		return os.Remove(workspaceIDPath())
	}
	return saveWorkspaceIDLocked(workspaceState{ID: NormalizeWorkspaceID(id), Source: "manual", At: nowRFC3339()})
}

// learnWorkspaceID records an id seen in a failure. It never overwrites a hand-entered one:
// the workspace the user chose is closer to their intent than an id that happened to show up
// in an error.
func learnWorkspaceID(id string) {
	if !ValidWorkspaceID(id) {
		return
	}
	wsIDMu.Lock()
	defer wsIDMu.Unlock()
	id = NormalizeWorkspaceID(id)
	st := loadWorkspaceIDLocked()
	if st.Source == "manual" || st.ID == id {
		return
	}
	_ = saveWorkspaceIDLocked(workspaceState{ID: id, Source: "learned", At: nowRFC3339()})
}

var nowRFC3339 = func() string { return time.Now().Format(time.RFC3339) }

// --- window information from a usage limit --------------------------------------

// LimitInfo is what a usage-limit failure tells us about the plan window.
type LimitInfo struct {
	Name    string `json:"name,omitempty"`     // "rolling" / "weekly" / "monthly" and the like
	ResetAt string `json:"reset_at,omitempty"` // RFC3339, derived from retry-after
}

// limitPayload is the shape opencode itself reads (responseBody parsed as JSON).
type limitPayload struct {
	Metadata struct {
		Workspace string `json:"workspace"`
		LimitName string `json:"limitName"`
	} `json:"metadata"`
}

// lastLimit is the most recent usage-limit observation, for the Console card.
var (
	limitMu   sync.Mutex
	lastLimit LimitInfo
)

// LastLimit returns the most recent usage-limit observation (zero value = never observed).
func LastLimit() LimitInfo {
	limitMu.Lock()
	defer limitMu.Unlock()
	return lastLimit
}

// scanFailure harvests what a failed turn can tell us: the workspace id (which appears both
// in the prose and in the metadata) and, for a usage-limit failure, which window it was and
// when it resets. Anything that could not be found is returned empty.
func scanFailure(e messageError) LimitInfo {
	learnWorkspaceID(string(workspaceIDRe.Find([]byte(e.Data.Message))))
	learnWorkspaceID(e.Data.Metadata.Workspace)

	info := LimitInfo{Name: strings.TrimSpace(e.Data.Metadata.LimitName)}
	// opencode itself re-reads responseBody (the provider's raw response string) as JSON.
	if body := e.Data.ResponseBody; body != "" {
		learnWorkspaceID(string(workspaceIDRe.Find([]byte(body))))
		var p limitPayload
		if json.Unmarshal([]byte(body), &p) == nil {
			learnWorkspaceID(p.Metadata.Workspace)
			if info.Name == "" {
				info.Name = strings.TrimSpace(p.Metadata.LimitName)
			}
		}
	}
	info.ResetAt = resetAt(headerValue(e.Data.ResponseHeaders, "retry-after"))
	if info.Name != "" || info.ResetAt != "" {
		limitMu.Lock()
		lastLimit = info
		limitMu.Unlock()
	}
	return info
}

// headerValue looks a header up case-insensitively: how response headers are normalised
// varies between versions.
func headerValue(h map[string]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// resetAt turns a retry-after value (seconds, or an HTTP date) into an absolute time.
func resetAt(retryAfter string) string {
	v := strings.TrimSpace(retryAfter)
	if v == "" {
		return ""
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
		return time.Now().Add(time.Duration(secs * float64(time.Second))).Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC1123, v); err == nil {
		return t.Format(time.RFC3339)
	}
	return ""
}

// WorkspaceURL builds the deep link for the Go plan page. Empty for an empty id.
func WorkspaceURL(id, page string) string {
	norm := NormalizeWorkspaceID(id)
	if norm == "" {
		return ""
	}
	if page == "" {
		page = "go"
	}
	return "https://opencode.ai/workspace/" + norm + "/" + page
}

// --- HTTP ---------------------------------------------------------------------

type workspaceReq struct {
	ID string `json:"id"` // "" = unregister
}

// HandlePutWorkspace records the workspace id the user pasted from their browser
// (PUT /connections/opencode/workspace). ID is not a secret — it is the path segment
// of the billing page URL — so it lives in the Agent's own data dir, not the sealed
// store.
func HandlePutWorkspace(w http.ResponseWriter, r *http.Request) {
	var req workspaceReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if id != "" && !ValidWorkspaceID(id) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_workspace_id",
			"workspace ID は wrk_ で始まる 30 文字です（利用枠ページの URL から取れます）")
		return
	}
	if err := SetWorkspaceID(id); err != nil && !os.IsNotExist(err) {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	id = NormalizeWorkspaceID(id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"workspace_id": id, "workspace_url": WorkspaceURL(id, "go")})
}
