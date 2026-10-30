package awsx

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// "Confirm before use" (ADR 0126 decisions 4 and 5). A Settings profile with the field set
// makes af-aws-exec ask the member in the Console before the profile's first use in a
// session. The run files a confirmation request and waits; only the Agent, answering the
// member's press through the CP's relay, writes a grant. A grant is keyed by the profile's
// Settings id, the session's incarnation and the identity it was asked for, so editing the
// profile, reusing its name or starting a new session of the same name asks again.
//
// What this does not stop is written in decision 6: an agent can call the approve route itself
// with AGENT_TOKEN, or run aws without af-aws-exec on a cached token. The gate catches
// accidents and unnoticed use, not an agent that sets out to get past it.

const (
	// NoticeKindAWSConfirm is the notification that carries a confirmation request's id.
	NoticeKindAWSConfirm = "aws-use-confirm"
	// confirmGrantTTL is how long an approval lasts within its session (decision F).
	confirmGrantTTL = 8 * time.Hour
	// confirmPendingTTL is how long an unanswered request stays up after its last waiter.
	confirmPendingTTL = 15 * time.Minute
	// confirmDenyHold is how long a denial answers new runs of the same request at once, so
	// an agent that retries does not put the card straight back.
	confirmDenyHold = time.Minute
)

// ErrConfirmRequired marks every gate refusal: af-aws-exec exits 4 for it.
var ErrConfirmRequired = errors.New("the member's confirmation is needed")

// confirmPoll is how often a waiting run reads its request and grants (file reads).
var confirmPoll = time.Second

// confirmTarget is one approval a run needs: the profile, by its Settings id, and the
// identity Settings gives it now.
type confirmTarget struct {
	ProfileID string `json:"profileId"`
	Profile   string `json:"profile"`
	Identity  string `json:"identity"`
}

// confirmRequest is one pending question to the member, one file per session incarnation and
// set of targets.
type confirmRequest struct {
	ID          string          `json:"id"`
	Session     string          `json:"session"`
	Incarnation string          `json:"incarnation"`
	Targets     []confirmTarget `json:"targets"`
	Commands    []string        `json:"commands,omitempty"`
	FirstAt     string          `json:"firstAt"`
	LastAt      string          `json:"lastAt"`
	// State is "pending", "approved" or "denied"; DecidedAt is when the member answered.
	State     string `json:"state"`
	DecidedAt string `json:"decidedAt,omitempty"`
}

// confirmGrant is one approval, written only by the Agent.
type confirmGrant struct {
	Incarnation string `json:"incarnation"`
	ProfileID   string `json:"profileId"`
	Identity    string `json:"identity"`
	Expires     string `json:"expires"`
	// Relayed is false for an approval that did not come through the CP's relay: an agent
	// calling the route itself (decision 6). The header can be forged, so true proves nothing.
	Relayed bool `json:"relayed"`
}

func confirmDir(sub string) string { return filepath.Join(paths.AgentStateDir(), "aws-confirm", sub) }

func hashParts(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// identityOf is the effective identity a grant covers: who the profile runs as and through
// what, as Settings defines it now (decision 5). Any change asks again.
func identityOf(p Profile, settings map[string]Profile) string {
	if p.Chained() {
		src := settings[p.SourceProfile]
		return hashParts("assume_role", p.ID, p.AccountID, p.RoleARN, p.Region, src.ID, p.ExternalID, p.SessionName,
			fmt.Sprint(p.DurationSeconds))
	}
	return hashParts("sso", p.ID, p.AccountID, p.RoleName, p.Region, strings.TrimRight(p.StartURL, "/"), p.SSORegion)
}

// confirmTargets lists the approvals a run of profile needs: the profile's own when it is
// gated, and, for a chain, its source's when the source is gated, since the chain uses the
// source's role to assume its own. Neither stands in for the other (decision 4).
func confirmTargets(settings map[string]Profile, profile string) []confirmTarget {
	p, ok := settings[profile]
	if !ok {
		return nil
	}
	var out []confirmTarget
	if p.ConfirmBeforeUse {
		out = append(out, confirmTarget{ProfileID: p.ID, Profile: p.Name, Identity: identityOf(p, settings)})
	}
	if p.Chained() {
		if src, ok := settings[p.SourceProfile]; ok && src.ConfirmBeforeUse {
			out = append(out, confirmTarget{ProfileID: src.ID, Profile: src.Name, Identity: identityOf(src, settings)})
		}
	}
	return out
}

// NeedsConfirmation reports whether a run of profile has to be confirmed.
func NeedsConfirmation(settings map[string]Profile, profile string) bool {
	return len(confirmTargets(settings, profile)) > 0
}

// incarnation names one life of a session: a new session of the same name is another.
func incarnation(m session.Meta) string { return hashParts(m.Name, m.Dir, m.CreatedAt) }

func grantPath(inc, profileID string) string {
	return filepath.Join(confirmDir("grants"), hashParts(inc, profileID)[:40]+".json")
}

func requestKey(inc string, ts []confirmTarget) string {
	parts := []string{inc}
	for _, t := range ts {
		parts = append(parts, t.ProfileID, t.Identity)
	}
	return hashParts(parts...)[:40]
}

func requestPath(key string) string { return filepath.Join(confirmDir("requests"), key+".json") }

// granted reports whether a live grant covers t in the session incarnation inc, and whether
// that grant came through the CP's relay.
func granted(inc string, t confirmTarget, now time.Time) (ok, relayed bool) {
	var g confirmGrant
	if !readJSON(grantPath(inc, t.ProfileID), &g) || g.Incarnation != inc || g.ProfileID != t.ProfileID || g.Identity != t.Identity {
		return false, false
	}
	exp, err := time.Parse(time.RFC3339, g.Expires)
	return err == nil && now.Before(exp), g.Relayed
}

// lockConfirm takes the requests' lock across processes.
func lockConfirm() (func(), error) {
	dir := confirmDir("requests")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// ConfirmOptions is what the gate needs from af-aws-exec.
type ConfirmOptions struct {
	Settings map[string]Profile
	Profile  string
	Session  string // AF_SESSION_NAME
	Wait     time.Duration
	Command  string // the command's first word, cut like a login waiter's
	Stderr   io.Writer
}

// ConfirmResult is what the gate let through.
type ConfirmResult struct {
	// Confirmed is true when the run needed confirmation and holds it.
	Confirmed bool
	// OutsideConsole is true when an approval it holds did not come through the CP's relay.
	OutsideConsole bool
}

// ConfirmGate returns without error when the run needs no confirmation or holds one, and
// otherwise asks the member and waits up to o.Wait. Every refusal wraps ErrConfirmRequired. It
// runs before any credential is fetched and before the command starts.
func ConfirmGate(o ConfirmOptions) (ConfirmResult, error) {
	targets := confirmTargets(o.Settings, o.Profile)
	if len(targets) == 0 {
		return ConfirmResult{}, nil
	}
	done := func(relayed bool) (ConfirmResult, error) {
		return ConfirmResult{Confirmed: true, OutsideConsole: !relayed}, nil
	}
	m, ok := session.Meta{}, false
	if session.ValidName(o.Session) {
		m, ok = session.ReadMeta(o.Session)
	}
	if !ok {
		return ConfirmResult{}, fmt.Errorf("%w: profile %q asks for confirmation before use, and that needs an Agent Fleet session; run this from one", ErrConfirmRequired, o.Profile)
	}
	inc := incarnation(m)
	deadline := time.Now().Add(o.Wait)
	announced := false
	for {
		now := time.Now()
		if ok, relayed := allGranted(inc, targets, now); ok {
			return done(relayed)
		}
		// Ask only for what is not granted yet: an approved source is not asked about again.
		ask := missingGrants(inc, targets, now)
		r, err := fileConfirm(m.Name, inc, ask, o.Command, now)
		if err != nil {
			return ConfirmResult{}, err
		}
		if r.State == "denied" {
			return ConfirmResult{}, fmt.Errorf("%w: use of profile %q was denied in the Console", ErrConfirmRequired, o.Profile)
		}
		if !announced && o.Stderr != nil {
			fmt.Fprintf(o.Stderr, "af-aws-exec: profile %q asks for confirmation before use; it is waiting in the Agent Fleet Console (up to %s)\n",
				o.Profile, o.Wait.Round(time.Second))
			announced = true
		}
		// Wait on this request until it is answered, dropped, or the time is up.
		for {
			if time.Now().After(deadline) {
				return ConfirmResult{}, fmt.Errorf("%w: profile %q is waiting for confirmation in the Agent Fleet Console; run the command again after it is allowed",
					ErrConfirmRequired, o.Profile)
			}
			time.Sleep(confirmPoll)
			if ok, relayed := allGranted(inc, targets, time.Now()); ok {
				return done(relayed)
			}
			var cur confirmRequest
			if !readJSON(requestPath(requestKey(inc, ask)), &cur) || cur.ID != r.ID {
				break // dropped (Settings changed) or replaced: file again
			}
			if cur.State == "denied" {
				return ConfirmResult{}, fmt.Errorf("%w: use of profile %q was denied in the Console", ErrConfirmRequired, o.Profile)
			}
		}
	}
}

// allGranted reports whether every target holds a live grant, and whether every one of them
// came through the CP's relay.
// missingGrants is the targets without a live grant.
func missingGrants(inc string, ts []confirmTarget, now time.Time) []confirmTarget {
	var out []confirmTarget
	for _, t := range ts {
		if ok, _ := granted(inc, t, now); !ok {
			out = append(out, t)
		}
	}
	return out
}

func allGranted(inc string, ts []confirmTarget, now time.Time) (ok, relayed bool) {
	relayed = true
	for _, t := range ts {
		g, rel := granted(inc, t, now)
		if !g {
			return false, false
		}
		relayed = relayed && rel
	}
	return true, relayed
}

// fileConfirm files the request for (inc, targets), or joins the pending one. A denial within
// confirmDenyHold is returned as it is; an older answer or an expired request is replaced by a
// new request with a new id and its own notification.
func fileConfirm(sessionName, inc string, targets []confirmTarget, command string, now time.Time) (confirmRequest, error) {
	unlock, err := lockConfirm()
	if err != nil {
		return confirmRequest{}, err
	}
	defer unlock()
	path := requestPath(requestKey(inc, targets))
	var r confirmRequest
	have := readJSON(path, &r)
	switch {
	case have && r.State == "denied" && within(r.DecidedAt, now, confirmDenyHold):
		return r, nil
	case have && r.State == "pending" && within(r.LastAt, now, confirmPendingTTL):
		r.LastAt = now.UTC().Format(time.RFC3339)
	default:
		r = confirmRequest{ID: newRequestID(), Session: sessionName, Incarnation: inc, Targets: targets,
			FirstAt: now.UTC().Format(time.RFC3339), LastAt: now.UTC().Format(time.RFC3339), State: "pending"}
		have = false
	}
	if c := cleanCommand(command); c != "" && !contains(r.Commands, c) {
		r.Commands = append(r.Commands, c)
		if len(r.Commands) > 5 {
			r.Commands = r.Commands[len(r.Commands)-5:]
		}
	}
	if err := writeJSONAtomic(path, r); err != nil {
		return confirmRequest{}, err
	}
	if !have {
		ev := notice.New(NoticeKindAWSConfirm, "", "", "")
		ev.TargetType = "session"
		if m, ok := session.ReadMeta(sessionName); ok {
			ev.SessionName, ev.SessionKind = m.Name, m.Kind
		}
		// The Console reads nothing but the id: the outbox is writable by every agent.
		ev.Payload["requestId"] = r.ID
		_ = notice.PutOnce(NoticeKindAWSConfirm+":"+r.ID, ev)
	}
	return r, nil
}

func within(stamp string, now time.Time, d time.Duration) bool {
	t, err := time.Parse(time.RFC3339, stamp)
	return err == nil && now.Sub(t) < d
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// cleanCommand keeps the first word of a command as plain characters; the Console shows it
// next to Settings' identity, so it must not be able to look like one.
func cleanCommand(s string) string {
	s = filepath.Base(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._-", r) {
			b.WriteRune(r)
		}
		if b.Len() >= 32 {
			break
		}
	}
	return b.String()
}

func newRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, b, 0o600)
}

// RoleNameOf is the role name of an IAM role ARN, "" when arn is not one.
func RoleNameOf(arn string) string { return roleNameOf(arn) }

// ForgetRoleCredentials removes the role credentials the CLI cached while af-aws-exec exported
// a withheld profile's (ADR 0126 decision 6): the SSO role's, under the key the CLI uses, and a
// chain's assumed role's, found by the role the cached credentials belong to. The command gets
// them in its environment; nothing is left for a plain aws to read.
func ForgetRoleCredentials(settings map[string]Profile, profile string) {
	p, ok := settings[profile]
	if !ok {
		return
	}
	sso := p
	if p.Chained() {
		sso = settings[p.SourceProfile]
		if acct, role, ok := roleARNParts(p.RoleARN); ok {
			forgetAssumedRole(acct, role)
		}
	}
	if sso.Name != "" {
		os.Remove(ssoRoleCachePath(sso.AccountID, sso.RoleName, "af-"+sso.Name))
	}
}

// forgetAssumedRole removes the CLI's cached AssumeRole credentials of role in account. Their
// key hashes the call's arguments, so the files are found by what they hold instead.
func forgetAssumedRole(account, role string) {
	dir := filepath.Join(paths.HomeDir(), ".aws", "cli", "cache")
	ents, _ := os.ReadDir(dir)
	prefix := ":sts::" + account + ":assumed-role/" + role + "/"
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var doc struct {
			AssumedRoleUser struct {
				Arn string `json:"Arn"`
			} `json:"AssumedRoleUser"`
		}
		path := filepath.Join(dir, e.Name())
		if readJSON(path, &doc) && strings.Contains(doc.AssumedRoleUser.Arn, prefix) {
			os.Remove(path)
		}
	}
}
