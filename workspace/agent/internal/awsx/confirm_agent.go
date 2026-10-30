package awsx

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// The Agent's half of "confirm before use" (ADR 0126 decision 5): it lists the pending
// requests joined with Settings, and writes a grant or a denial when the member answers.
// Everything shown comes from Settings: the request names only profile ids and identity
// hashes, and a request whose identity no longer matches Settings is dropped, so the card
// and what a grant covers cannot drift apart.

// confirmLockWait bounds how long a route waits for the requests' lock.
const confirmLockWait = 5 * time.Second

// confirmSettings is the Settings list the routes check requests against: the last poll's (a
// read must not pull from the CP). A variable so tests can give their own.
var confirmSettings = loginSettings

// ConfirmTargetWire is one profile a confirmation request asks about, as Settings defines it.
type ConfirmTargetWire struct {
	Profile   string `json:"profile"`
	Label     string `json:"label"`
	AccountID string `json:"accountId"`
	RoleName  string `json:"roleName"`
	Region    string `json:"region,omitempty"`
	// Source is set for a chain: the profile it assumes its role through.
	Source string `json:"source,omitempty"`
}

// ConfirmRequestWire is one pending confirmation request as the Console shows it.
type ConfirmRequestWire struct {
	ID       string              `json:"id"`
	Session  string              `json:"session"`
	Targets  []ConfirmTargetWire `json:"targets"`
	Commands []string            `json:"commands,omitempty"`
	FirstAt  string              `json:"firstAt"`
	LastAt   string              `json:"lastAt"`
	// ExpiresInSec is how long the request stays up unanswered.
	ExpiresInSec int64 `json:"expiresInSec"`
}

type confirmListWire struct {
	Requests []ConfirmRequestWire `json:"requests"`
}

// settingsByID indexes Settings by profile id.
func settingsByID(settings map[string]Profile) map[string]Profile {
	out := map[string]Profile{}
	for _, p := range settings {
		if p.ID != "" {
			out[p.ID] = p
		}
	}
	return out
}

// currentTargets returns r's targets as Settings shows them now, or false when any of them is
// gone, no longer gated, or has another identity than the one the request was filed for.
func currentTargets(r confirmRequest, settings map[string]Profile) ([]ConfirmTargetWire, bool) {
	byID := settingsByID(settings)
	var out []ConfirmTargetWire
	for _, t := range r.Targets {
		p, ok := byID[t.ProfileID]
		if !ok || !p.ConfirmBeforeUse || identityOf(p, settings) != t.Identity {
			return nil, false
		}
		w := ConfirmTargetWire{Profile: p.Name, Label: p.Label, AccountID: p.AccountID, RoleName: p.RoleName, Region: p.Region}
		if p.Chained() {
			w.RoleName, w.Source = roleNameOf(p.RoleARN), p.SourceProfile
		}
		out = append(out, w)
	}
	return out, len(out) > 0
}

// confirmRequests reads every request file, dropping those that ended: answered ones past their
// hold, unanswered ones past their time, and those Settings no longer matches. The caller holds
// the lock.
func confirmRequests(settings map[string]Profile, now time.Time) []confirmRequest {
	dir := confirmDir("requests")
	ents, _ := os.ReadDir(dir)
	var out []confirmRequest
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		var r confirmRequest
		if !readJSON(path, &r) {
			os.Remove(path)
			continue
		}
		_, matches := currentTargets(r, settings)
		// A request of a session that ended, or that is another of the same name now, can no
		// longer be answered: its run is gone or will end itself.
		matches = matches && liveIncarnation(r.Session) == r.Incarnation
		switch {
		case r.State == "pending" && within(r.LastAt, now, confirmPendingTTL) && matches:
			out = append(out, r)
		case r.State == "denied" && within(r.DecidedAt, now, confirmDenyHold):
		default:
			os.Remove(path)
		}
	}
	sweepGrants(now)
	return out
}

// sweepGrants removes expired grants, those of sessions that ended, and old denial markers.
func sweepGrants(now time.Time) {
	dir := confirmDir("grants")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		var g confirmGrant
		path := filepath.Join(dir, e.Name())
		if !readJSON(path, &g) {
			os.Remove(path)
			continue
		}
		if exp, err := time.Parse(time.RFC3339, g.Expires); err != nil || !now.Before(exp) || liveIncarnation(g.Session) != g.Incarnation {
			os.Remove(path)
		}
	}
	dir = confirmDir("denied")
	ents, _ = os.ReadDir(dir)
	for _, e := range ents {
		var d denyMarker
		path := filepath.Join(dir, e.Name())
		if !readJSON(path, &d) || !within(d.DecidedAt, now, confirmDenyHold) {
			os.Remove(path)
		}
	}
}

// HandleConfirmList is GET /aws-confirm: the pending requests the Console may show.
func HandleConfirmList(w http.ResponseWriter, r *http.Request) {
	settings := confirmSettings()
	out := []ConfirmRequestWire{}
	unlock, err := lockConfirm(time.Now().Add(confirmLockWait))
	if err != nil {
		httpx.WriteErr(w, http.StatusServiceUnavailable, "busy", err.Error())
		return
	}
	now := time.Now()
	reqs := confirmRequests(settings, now)
	unlock()
	for _, req := range reqs {
		ts, _ := currentTargets(req, settings)
		last, _ := time.Parse(time.RFC3339, req.LastAt)
		left := int64(last.Add(confirmPendingTTL).Sub(now).Seconds())
		out = append(out, ConfirmRequestWire{ID: req.ID, Session: req.Session, Targets: ts, Commands: req.Commands,
			FirstAt: req.FirstAt, LastAt: req.LastAt, ExpiresInSec: max(left, 0)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstAt < out[j].FirstAt })
	httpx.WriteJSON(w, http.StatusOK, confirmListWire{Requests: out})
}

// HandleConfirmApprove is POST /aws-confirm/{id}/approve: a grant per target, for this session
// incarnation and identity, for confirmGrantTTL.
func HandleConfirmApprove(w http.ResponseWriter, r *http.Request) { answerConfirm(w, r, true) }

// HandleConfirmDeny is POST /aws-confirm/{id}/deny: the waiting runs end with exit 4, and new
// ones of the same request do for confirmDenyHold.
func HandleConfirmDeny(w http.ResponseWriter, r *http.Request) { answerConfirm(w, r, false) }

func answerConfirm(w http.ResponseWriter, r *http.Request, approve bool) {
	id := r.PathValue("id")
	settings := confirmSettings()
	unlock, err := lockConfirm(time.Now().Add(confirmLockWait))
	if err != nil {
		httpx.WriteErr(w, http.StatusServiceUnavailable, "busy", err.Error())
		return
	}
	defer unlock()
	now := time.Now()
	var req confirmRequest
	found := false
	for _, c := range confirmRequests(settings, now) {
		if c.ID == id && c.State == "pending" {
			req, found = c, true
		}
	}
	if !found {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no pending confirmation request with that id")
		return
	}
	path := requestPath(requestKey(req.Incarnation, req.Targets))
	req.DecidedAt = now.UTC().Format(time.RFC3339)
	if approve {
		for _, t := range req.Targets {
			g := confirmGrant{Session: req.Session, Incarnation: req.Incarnation, ProfileID: t.ProfileID, Identity: t.Identity,
				Expires: now.Add(confirmGrantTTL).UTC().Format(time.RFC3339), Relayed: cloudlogin.RelayedByCP(r)}
			if err := writeJSONAtomic(grantPath(req.Incarnation, t.ProfileID), g); err != nil {
				httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
				return
			}
		}
		// The grants answer the waiters; the request is done.
		os.Remove(path)
	} else {
		// One marker per profile: the hold applies whatever set of profiles a later run asks
		// about. The request is kept so its waiters see the answer too.
		for _, t := range req.Targets {
			if err := writeJSONAtomic(denyPath(req.Incarnation, t.ProfileID), denyMarker{DecidedAt: req.DecidedAt}); err != nil {
				httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
				return
			}
		}
		req.State = "denied"
		if err := writeJSONAtomic(path, req); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
	}
	names := make([]string, 0, len(req.Targets))
	for _, t := range req.Targets {
		names = append(names, t.Profile)
	}
	// The Agent's own record: an agent can call this route with AGENT_TOKEN, and only what the
	// CP relayed is in its audit (decision 6).
	log.Printf("aws-confirm: approve=%t profiles=%s session=%s relayed=%t", approve, strings.Join(names, ","), req.Session,
		cloudlogin.RelayedByCP(r))
	httpx.WriteJSON(w, http.StatusOK, confirmAnswerWire{OK: true})
}

// confirmAnswerWire is the answer to an approve or a deny.
type confirmAnswerWire struct {
	OK bool `json:"ok"`
}
