package awsx

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudexec"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// What the AF route gateway (internal/routegw) needs from the profile machinery (ADR 0128
// decisions 7 and 11). The gateway never starts a command: it asks for the profile's
// credentials as a value, in its own memory, and re-asks for the approval on every call.

// Incarnation names one life of a session: a new session of the same name is another. It is
// what a route token and a confirmation grant are both bound to.
func Incarnation(m session.Meta) string { return incarnation(m) }

// ProfileByID finds the Settings profile with the given row id. A route names its connection by
// id (routes.AWSConnection), so a rename moves nothing and a deleted row finds nothing.
func ProfileByID(settings map[string]Profile, id string) (Profile, bool) {
	if id == "" {
		return Profile{}, false
	}
	for _, p := range settings {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// ProfileIdentity is the identity a grant covers (decision 5 of ADR 0126): who the profile runs
// as and through what, as Settings defines it now. A route token records it when it is issued and
// is refused once it differs, so editing the profile ends the token as it ends a grant.
func ProfileIdentity(settings map[string]Profile, p Profile) string { return identityOf(p, settings) }

// ApprovalHeld reports whether every approval a use of profile needs is granted now for the
// session's incarnation: true when the profile asks for none. It reads files and waits for
// nothing, so a gateway can ask it on every call. An approval is ADR 0126's own (per session and
// profile, at most 8 hours, ended with the session), never a second kind.
func ApprovalHeld(settings map[string]Profile, profile string, m session.Meta, now time.Time) bool {
	ts := confirmTargets(settings, profile)
	if len(ts) == 0 {
		return true
	}
	if liveIncarnation(m.Name) != incarnation(m) {
		return false
	}
	ok, _ := allGranted(incarnation(m), ts, now)
	return ok
}

// AskApproval files (or joins) the confirmation request for a use of profile, so the member sees
// the card, and returns at once with the gate's refusal. It is the gate af-aws-exec runs, given
// a wait too short to hold a model call open.
func AskApproval(settings map[string]Profile, profile, sessionName, what string) error {
	_, err := ConfirmGate(ConfirmOptions{Settings: settings, Profile: profile, Session: sessionName,
		Wait: 2 * time.Second, Command: what})
	return err
}

// RouteCredentials are one profile's short-lived credentials, as the gateway signs with them.
// They live in the gateway's memory only and are never written, logged or handed to a session.
type RouteCredentials struct {
	AccessKeyID, SecretAccessKey, SessionToken string
	// Expires is zero when the provider reported none.
	Expires time.Time
	Region  string
}

// ResolveCredentials runs the profile through exactly what af-aws-exec runs before it executes a
// command: the Settings checks, the SSO-only config, the role export and the STS identity check
// (PlanExec). It never logs in: a missing or expired SSO login is ErrLoginRequired, because a
// model call cannot wait for a device code. o.Argv and o.Login are set here.
func ResolveCredentials(awsBin string, environ []string, o ExecOptions) (RouteCredentials, error) {
	o.Argv = []string{"sh"}
	o.Login = "never"
	o.Quiet = true
	o.Stderr = nil
	o.ConsoleLogin = false
	_, _, env, err := PlanExec(awsBin, environ, o)
	if err != nil {
		return RouteCredentials{}, err
	}
	c := RouteCredentials{
		AccessKeyID:     cloudexec.EnvValue(env, "AWS_ACCESS_KEY_ID"),
		SecretAccessKey: cloudexec.EnvValue(env, "AWS_SECRET_ACCESS_KEY"),
		SessionToken:    cloudexec.EnvValue(env, "AWS_SESSION_TOKEN"),
		Region:          cloudexec.EnvValue(env, "AWS_REGION"),
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return RouteCredentials{}, errors.New("the profile resolved to no credentials")
	}
	if exp := cloudexec.EnvValue(env, "AWS_CREDENTIAL_EXPIRATION"); exp != "" {
		t, perr := time.Parse(time.RFC3339, strings.TrimSpace(exp))
		if perr != nil {
			return RouteCredentials{}, fmt.Errorf("the profile's credential expiry is not a time: %v", perr)
		}
		c.Expires = t
	}
	return c, nil
}
