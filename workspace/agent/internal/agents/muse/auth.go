package muse

// auth.go is the connection card's Agent half (ADR 0095 decision 9): the credential status
// GET /connections reports, the device-code sign-in (start → poll), the API-key fallback, and
// the disconnect.
//
// Muse has NO status subcommand — no `whoami`, no `auth status` — and nothing on the MSP wire
// either (the 47 methods carry no auth verb at all). What it has is one credential file,
// `~/.config/muse/auth.json`, and this file reads it. That is strictly better than the exec
// probe kiro and codex pay: no subprocess, so no 30-second staleness cache, so a sign-in shows
// up on the very next /connections poll.
//
// Three measurements on 1.3.0-R3401.1 shape how it is read, and each one is a way a naive
// reader would be confidently wrong:
//
//  1. 🔴 **The presence of `api_key` does NOT mean the member is on metered billing.** The
//     device-code account login writes one too — a real subscription login leaves
//     `access_token`, `api_base_url`, `api_key`, `mechanism: "oauth"`,
//     `obtained_via: "device_code"`, `user_email` and `user_full_name` side by side. A card
//     keyed on `api_key` would tell every subscription member they were being billed per use,
//     which is the exact opposite of the truth this decision exists to protect. The
//     discriminator is `mechanism`: `muse auth set --api-key-stdin` writes `api_key` ALONE,
//     with no `mechanism` and no `obtained_via`.
//  2. 🔴 **`muse logout` does not remove the file.** It leaves `{"schema_version":1,
//     "providers":{}}` behind, mode 600. So connectedness is "`providers.meta` carries a
//     credential", never "the file exists" — the latter reads as signed-in forever after the
//     first logout.
//  3. 🔴 **`muse auth set` REPLACES the whole provider entry.** Measured over a
//     device-code-shaped file, it left `api_key` as the only key: the access token, the
//     mechanism, the e-mail — all gone. So an API key written over an account login does not
//     merely take priority, it destroys the sign-in and the member has to run the device flow
//     again. That is why HandleAPIKey refuses rather than asks (below).

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// mechanismAccount is the value muse stores for a device-code account login — the
// subscription path, and the only one AF offers by default.
const mechanismAccount = "oauth"

// mechanismAPIKey is what AF reports for a stored key. Muse itself writes no `mechanism` in
// that case (measurement 1 above), so this is AF's name for the shape, not muse's.
const mechanismAPIKey = "api_key"

// authPath is muse's credential file. Both this and DataHome are on the Agent's file
// deny-list, so nothing else in AF can read it (fs.go).
func authPath() string { return filepath.Join(ConfigHome(), "auth.json") }

// credential is what auth.json says about the Meta provider, minus every secret: this struct
// deliberately has no field for `api_key` or `access_token`, so no route can leak one by
// forgetting to filter it on the way out.
type credential struct {
	Present     bool
	Mechanism   string // mechanismAccount | mechanismAPIKey
	ObtainedVia string // "device_code" for the account login; empty for a stored key
	Email       string
	FullName    string
}

// authFile is the on-disk shape, left deliberately undecoded one level down: the provider
// entry is a bag of raw values, and only the four non-secret members are ever turned into Go
// strings. The two secrets are needed for their PRESENCE alone, which `hasValue` answers off
// the raw bytes — so no code path in AF ever holds muse's token or key as a value it could
// log, wrap in an error or serialise by accident.
type authFile struct {
	Providers map[string]map[string]json.RawMessage `json:"providers"`
}

// hasValue reports that a member is present and is not JSON null or the empty string.
func hasValue(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != `""`
}

// readCredential parses auth.json. A missing file, an unparseable one and an empty
// `providers` all mean the same thing to the card — not connected — and none of them is an
// error worth surfacing: the member's next action is the same either way.
func readCredential() credential {
	b, err := os.ReadFile(authPath())
	if err != nil {
		return credential{}
	}
	var f authFile
	if json.Unmarshal(b, &f) != nil {
		return credential{}
	}
	meta, ok := f.Providers["meta"]
	if !ok {
		return credential{}
	}
	str := func(k string) string {
		var s string
		if json.Unmarshal(meta[k], &s) != nil {
			return ""
		}
		return s
	}
	c := credential{Email: str("user_email"), FullName: str("user_full_name"), ObtainedVia: str("obtained_via")}
	switch {
	case str("mechanism") != "":
		// muse's own word for it, whatever it is; AF does not narrow it to the one value it
		// has seen, so a future mechanism reads as connected rather than as signed out.
		c.Present, c.Mechanism = true, str("mechanism")
	case hasValue(meta["api_key"]) || hasValue(meta["access_token"]):
		c.Present, c.Mechanism = true, mechanismAPIKey
	}
	return c
}

// envKeySet reports whether META_API_KEY is in the Agent's own environment, which the child
// inherits. Muse's own `login --help` says it "always takes priority over the account login",
// so a member who has one set is metered no matter what the card's pill says.
//
// AF reports this and does not act on it. Stripping it from the child would override a
// deliberate deployment choice silently, and injecting one is already rejected (decision 9);
// what the member needs is for the contradiction to be VISIBLE, because the failure mode is a
// bill, not an error.
func envKeySet() bool { return strings.TrimSpace(os.Getenv("META_API_KEY")) != "" }

// Status is the `muse` field of GET /connections.
//
// supported=false is the pre-install state: Muse Code is proprietary and not in the image, so
// on a fresh Workspace this is what the card sees and it offers the install rather than a
// sign-in it could not run.
func Status() map[string]any {
	if !Installed() {
		return map[string]any{"connected": false, "supported": false, "reason": "not_installed"}
	}
	c := readCredential()
	m := map[string]any{"supported": true, "connected": c.Present}
	if c.Email != "" {
		m["email"] = c.Email
	}
	if c.FullName != "" {
		m["name"] = c.FullName
	}
	if c.Present {
		m["mechanism"] = c.Mechanism
		// metered is the fact the member actually cares about, derived once here rather than
		// in the Console — an API key bills per use, an account login rides the subscription.
		m["metered"] = c.Mechanism != mechanismAccount
	}
	if envKeySet() {
		m["env_key"] = true
	}
	return m
}

// --- the device-code sign-in (decision 9) -----------------------------------------------
//
// `muse login` prints the verification URL with the code already embedded, prints the code
// again for the member to compare against the browser, and then self-polls Meta until the
// approval lands and writes auth.json. AF shows the URL and the code and polls its own
// reading of auth.json — the same start → poll shape as cursor and kiro, with no pasted code.
//
// 🔥 Unlike every other login in this tree it must run OFF a terminal. On a PTY muse stops at
// "Press Enter to open it in your browser:" and never begins polling; there is no browser here
// to open, and nothing would be waiting to press the key. agents.StartPipeFlow exists for
// this and its header carries the measurement.

// loginURLRe matches the device verification URL muse prints (measured:
// https://auth.meta.com/oauth/device/?code=XXXX-XXXX).
var loginURLRe = regexp.MustCompile(`https://\S*oauth/device\S*`)

// loginCodeRe matches the confirmation code, so the card can show it beside the URL for the
// member to compare with the browser. It appears twice — inside the URL and on its own
// line — and both are the same value.
var loginCodeRe = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4}\b`)

// The device-approval window is short; don't keep orphan login children around.
const loginFlowTTL = 15 * time.Minute

var loginFlows = agents.NewFlowStore(loginFlowTTL)

// HandleStart launches the device-code login, scrapes the URL and code, and returns them
// with a flow_id the client polls. The muse process is kept alive because it is the thing
// doing the Meta-side polling. POST /connections/muse/start.
func HandleStart(w http.ResponseWriter, r *http.Request) {
	if !Installed() {
		httpx.WriteErr(w, http.StatusConflict, "muse_unsupported", "Muse Code is not installed")
		return
	}
	if readCredential().Present {
		// A signed-in muse prints no login URL, so the scrape below would only time out.
		httpx.WriteErr(w, http.StatusConflict, "already_connected", "already connected; disconnect first to re-authenticate")
		return
	}
	loginFlows.Reap()
	cmd := exec.Command(Bin(), "login")
	// The pin is AF's, not the launcher's, on the login path too (decision 8).
	cmd.Env = append(os.Environ(), "MUSE_NO_AUTO_UPDATE=1")
	f, err := agents.StartPipeFlow(cmd)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "spawn_failed", err.Error())
		return
	}
	url := f.WaitFor(loginURLRe, 20*time.Second)
	if url == "" {
		detail := strings.TrimSpace(f.Clean())
		f.Close()
		httpx.WriteErr(w, http.StatusBadGateway, "no_url", "muse printed no login URL: "+detail)
		return
	}
	id := loginFlows.Put(f)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"flow_id":   id,
		"url":       url,
		"user_code": loginCodeRe.FindString(f.Clean()),
	})
}

type pollReq struct {
	FlowID string `json:"flow_id"`
}

// HandlePoll reports whether the browser approval has landed. POST /connections/muse/poll.
//
// The signal is auth.json, not the child's output: muse writes the credential before it says
// anything, and a reader of its final line would be guessing at wording that is not a
// contract. A child that has EXITED without one, though, is a finished failure — an expired
// code, a refused approval — and reporting that ends the poll instead of leaving the card
// spinning to its 15-minute deadline for something that can no longer happen.
func HandlePoll(w http.ResponseWriter, r *http.Request) {
	var req pollReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if readCredential().Present {
		if f := loginFlows.Take(req.FlowID); f != nil {
			f.Close()
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true})
		return
	}
	// An unknown or already-reaped flow id leaves nothing to wait on; report it as still
	// unconnected rather than as a failure, because the TTL reaper is one of the ways to get
	// here and it says nothing about the approval.
	f := loginFlows.Get(req.FlowID)
	if f == nil || !f.Ended() {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": false})
		return
	}
	detail := strings.TrimSpace(f.Clean())
	loginFlows.Take(req.FlowID)
	f.Close()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": false, "failed": true, "detail": detail})
}

// HandleDisconnect signs muse out with `muse logout`, which removes the stored credential
// whichever way it was obtained. DELETE /connections/muse.
func HandleDisconnect(w http.ResponseWriter, r *http.Request) {
	if !Installed() {
		httpx.WriteErr(w, http.StatusConflict, "muse_unsupported", "Muse Code is not installed")
		return
	}
	// logout reaches the network, so without a timeout this handler blocks indefinitely.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, Bin(), "logout").CombinedOutput()
	// The credential is the outcome that matters: a non-zero exit with the entry gone is a
	// disconnect, and a zero exit with it still there is not.
	if readCredential().Present {
		detail := strings.TrimSpace(string(out))
		if detail == "" && err != nil {
			detail = err.Error()
		}
		httpx.WriteErr(w, http.StatusBadGateway, "logout_failed", detail)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "muse"})
}

type keyReq struct {
	Key string `json:"key"`
}

// HandleAPIKey stores a pay-as-you-go API key by piping it to `muse auth set --api-key-stdin`,
// which is the only route that accepts a secret without putting it in argv (so it never lands
// in `ps` or in shell history). POST /connections/muse/api-key.
//
// 🔴 It REFUSES while an account login is stored, rather than warning. Measured, `auth set`
// replaces the provider entry outright: the member would lose the sign-in AND move from their
// flat-rate subscription onto metered billing, in one unconfirmed button press. Disconnecting
// first makes that a deliberate two-step, and the card says so.
func HandleAPIKey(w http.ResponseWriter, r *http.Request) {
	if !Installed() {
		httpx.WriteErr(w, http.StatusConflict, "muse_unsupported", "Muse Code is not installed")
		return
	}
	var req keyReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_key", "key is required")
		return
	}
	if c := readCredential(); c.Present && c.Mechanism == mechanismAccount {
		httpx.WriteErr(w, http.StatusConflict, "account_login_present",
			"an account sign-in is stored; an API key would replace it and move this workspace onto metered billing — disconnect first")
		return
	}
	cmd := exec.Command(Bin(), "auth", "set", "--api-key-stdin")
	cmd.Env = append(os.Environ(), "MUSE_NO_AUTO_UPDATE=1")
	cmd.Stdin = strings.NewReader(key)
	out, err := cmd.CombinedOutput()
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "auth_failed", "muse auth set failed: "+strings.TrimSpace(string(out)))
		return
	}
	// Verify from the file rather than trusting the exit code: `auth set` does not check the
	// key against Meta at all (measured — a syntactically plausible fake saves with rc=0), so
	// this confirms the write happened, and nothing more. The card says the rest.
	if c := readCredential(); !c.Present {
		httpx.WriteErr(w, http.StatusBadGateway, "auth_failed", "muse auth set wrote no credential")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true, "metered": true})
}
