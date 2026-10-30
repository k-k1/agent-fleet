package awsx

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// A token key is bound to its owner and its portal (ADR 0125 decision 10). Neither of the
// CLI's caches is checked against the config: botocore takes whatever token sits under
// sha1(sso-session name), and it serves cached role credentials under sha1(account, role,
// session) without reading the token at all (CachedCredentialFetcher). A key that changes
// hands (a sign-in renamed, a portal changed, a name freed and taken by another owner) would be
// used as it is. So the Agent records, per key, which Settings owner and portal its token was
// written for, and which roles were taken through it, and accepts a key only while all of that
// still holds. A key that fails is retired: the token moves aside and the role credentials of
// every role it ever served are deleted, so no reader finds them, the SSM pane and a plain aws
// included.
//
// Only keys Agent Fleet uses are touched: those of usable Settings profiles (UsableNow). A key
// the member's own files take, or that a collision holds back, is theirs, and its cache is left
// alone.

// keyBinding is what one sso-session key's token was written for.
type keyBinding struct {
	Session   string `json:"session"`
	Owner     string `json:"owner"`
	StartURL  string `json:"startUrl"`
	SSORegion string `json:"ssoRegion"`
	// Roles are the account and role pairs taken through the key since it was bound: their
	// role credentials are what a retire must delete, also of members no longer in Settings.
	Roles [][2]string `json:"roles,omitempty"`
}

// sameTarget compares what two bindings are for, not the roles they recorded.
func (b keyBinding) sameTarget(o keyBinding) bool {
	return b.Session == o.Session && b.Owner == o.Owner && b.StartURL == o.StartURL && b.SSORegion == o.SSORegion
}

func bindingPath(session string) string {
	sum := sha1.Sum([]byte(session))
	return filepath.Join(paths.AgentStateDir(), "aws-sso-bindings", hex.EncodeToString(sum[:])+".json")
}

// keyOwner is who and which portal Settings gives session now, from its first usable SSO
// member, with every member's role; ok is false when no usable Settings SSO profile logs in
// through session (the key is then not Agent Fleet's to judge).
func keyOwner(settings map[string]Profile, usable []string, session string) (keyBinding, bool) {
	var ms []Profile
	for _, m := range sessionMembers(settings, session) {
		if slices.Contains(usable, m.Name) {
			ms = append(ms, m)
		}
	}
	if len(ms) == 0 {
		return keyBinding{}, false
	}
	p := ms[0]
	b := keyBinding{Session: session, Owner: p.TokenOwner(), StartURL: strings.TrimRight(p.StartURL, "/"), SSORegion: p.SSORegion}
	for _, m := range ms {
		b.Roles = addRole(b.Roles, m.AccountID, m.RoleName)
	}
	return b, true
}

// settingsOwner is keyOwner over every Settings profile, usable or not: who Settings gives the
// key to, for a login attempt and a request, which may be for a profile the member's own files
// define the way Settings does.
func settingsOwner(settings map[string]Profile, session string) (keyBinding, bool) {
	all := make([]string, 0, len(settings))
	for n := range settings {
		all = append(all, n)
	}
	return keyOwner(settings, all, session)
}

func addRole(roles [][2]string, account, role string) [][2]string {
	if account == "" || role == "" {
		return roles
	}
	r := [2]string{account, role}
	if slices.Contains(roles, r) {
		return roles
	}
	return append(roles, r)
}

// tokenPortal is the start URL and region the cached token of session says it is for.
func tokenPortal(session string) (url, region string, present bool) {
	return tokenPortalAt(ssoCachePath(session))
}

// tokenPortalAt is tokenPortal for the token file at path.
func tokenPortalAt(path string) (url, region string, present bool) {
	var doc struct {
		StartURL string `json:"startUrl"`
		Region   string `json:"region"`
	}
	if _, err := os.Stat(path); err != nil {
		return "", "", false
	}
	readJSON(path, &doc)
	return strings.TrimRight(doc.StartURL, "/"), doc.Region, true
}

// verdict is what vetting a key found.
type verdict int

const (
	keyNotOurs  verdict = iota // no usable Settings profile renders it: never touched
	keyNoToken                 // nothing cached, and no binding that names another owner
	keyUsable                  // bound to the current owner and portal
	keyAdopt                   // unbound, and the token is for the current owner's portal
	keyHandOver                // bound to a profile that is now a member of the sign-in owning the key
	keyUnusable                // another owner, another portal, or a token for another portal
)

// judgeKey decides session's key against Settings, reading only.
func judgeKey(settings map[string]Profile, usable []string, session string) (verdict, keyBinding, keyBinding) {
	want, ours := keyOwner(settings, usable, session)
	if !ours {
		return keyNotOurs, want, keyBinding{}
	}
	var got keyBinding
	bound := readJSON(bindingPath(session), &got)
	url, region, present := tokenPortal(session)
	if bound && (got.StartURL != want.StartURL || got.SSORegion != want.SSORegion || got.Owner != want.Owner) && !handOver(settings, got, want) {
		// A binding that names another owner or portal invalidates the role credentials taken
		// through it even with no token: botocore serves them without reading the token.
		return keyUnusable, want, got
	}
	if !present {
		return keyNoToken, want, got
	}
	if url != want.StartURL || region != want.SSORegion {
		return keyUnusable, want, got
	}
	switch {
	case !bound:
		return keyAdopt, want, got
	case got.Owner == want.Owner:
		return keyUsable, want, got
	}
	return keyHandOver, want, got
}

// handOver is the one legitimate change of owner: profile P becomes a member of sign-in S,
// which took P's name, so S renders the key P's token sits under (decision D).
func handOver(settings map[string]Profile, got, want keyBinding) bool {
	pid, isProfile := strings.CutPrefix(got.Owner, "profile:")
	if !isProfile || !strings.HasPrefix(want.Owner, "sign-in:") || got.StartURL != want.StartURL || got.SSORegion != want.SSORegion {
		return false
	}
	for _, m := range sessionMembers(settings, want.Session) {
		if m.ID == pid && m.TokenOwner() == want.Owner {
			return true
		}
	}
	return false
}

// usableVerdict reports whether v lets the key's token be used.
func usableVerdict(v verdict) bool {
	return v == keyNotOurs || v == keyNoToken || v == keyUsable || v == keyAdopt || v == keyHandOver
}

// vetKeyHook lets a test watch the key's lock being held.
var vetKeyHook = func() {}

// ErrKeyRetireFailed reports a key that had to be retired but could not be: its token or role
// credentials may still be readable, so nothing may use it.
var ErrKeyRetireFailed = errors.New("could not retire a cached AWS login that no longer matches Settings")

// vetKey accepts or retires session's key under its exclusive lock. It returns nil when the key
// may be used or was retired cleanly (the run then takes the normal login-required path), and
// an error when the lock, the binding or the retire failed: the caller must not use the key.
// Adopting an unbound token and the hand-over write the binding; a usable key records its
// members' roles. A key no usable Settings profile renders is left alone.
func vetKey(settings map[string]Profile, usable []string, session string) error {
	unlock, err := logins.LockKey(session, false)
	if err != nil {
		return fmt.Errorf("lock the cached login of %s: %w", session, err)
	}
	defer unlock()
	vetKeyHook()
	return vetLocked(settings, usable, session)
}

// vetLocked is vetKey for a caller that holds the key's exclusive lock.
func vetLocked(settings map[string]Profile, usable []string, session string) error {
	v, want, got := judgeKey(settings, usable, session)
	switch v {
	case keyNotOurs:
		return nil
	case keyNoToken:
		if got.Session != "" {
			return nil // bound to the current owner and portal: its role credentials are theirs
		}
		// Role credentials with neither a token nor a binding carry no evidence of the portal
		// they were taken from, and botocore serves them without reading a token: they go, and
		// the next run logs in.
		for _, r := range want.Roles {
			if err := os.Remove(ssoRoleCachePath(r[0], r[1], session)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("%w: %v", ErrKeyRetireFailed, err)
			}
		}
		return nil
	case keyUsable, keyAdopt, keyHandOver:
		for _, r := range got.Roles {
			want.Roles = addRole(want.Roles, r[0], r[1])
		}
		if err := writeJSONAtomic(bindingPath(session), want); err != nil {
			return fmt.Errorf("%w: write the binding of %s: %v", ErrKeyRetireFailed, session, err)
		}
		return nil
	}
	return retireKey(session, append(got.Roles, want.Roles...))
}

// retireKey moves the token aside (kept for a later revoke), deletes the role credentials of
// every role in roles, and removes the binding. The caller holds the key's exclusive lock. Any
// failure is returned: a token or role credentials left in place would still be used.
func retireKey(session string, roles [][2]string) error {
	path := ssoCachePath(session)
	if err := os.Rename(path, path+".af-retired"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: %v", ErrKeyRetireFailed, err)
	}
	for _, r := range roles {
		if err := os.Remove(ssoRoleCachePath(r[0], r[1], session)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%w: %v", ErrKeyRetireFailed, err)
		}
	}
	if err := os.Remove(bindingPath(session)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: %v", ErrKeyRetireFailed, err)
	}
	return nil
}

// keyLoginState is profileLoginState for sp's key, as decision 10 reads it: a key that vetting
// would retire is "none". A read moves no file.
func keyLoginState(sp Profile, settings map[string]Profile, usable []string, now time.Time) string {
	if v, _, _ := judgeKey(settings, usable, sp.SSOSession()); !usableVerdict(v) {
		return loginStateNone
	}
	return profileLoginState(sp.SSOSession(), now)
}

// keyExpiry is readSSOExpiry for sp's key, only while the key is usable: the end of a token
// for another owner or portal is not this sign-in's.
func keyExpiry(sp Profile, settings map[string]Profile, usable []string, now time.Time) (time.Time, bool) {
	if v, _, _ := judgeKey(settings, usable, sp.SSOSession()); !usableVerdict(v) {
		return time.Time{}, false
	}
	return readSSOExpiry(sp.SSOSession(), now)
}

// VetKeyOf vets the key the profile name logs in through (its source's for a chain), for
// af-aws-exec right before it fetches credentials. An error means the key could not be made
// safe and must not be used.
func VetKeyOf(settings map[string]Profile, name string) error {
	p, ok := settings[name]
	if !ok {
		return nil
	}
	if p.Chained() {
		if p, ok = settings[p.SourceProfile]; !ok {
			return nil
		}
	}
	return vetKey(settings, UsableNow(), p.SSOSession())
}

// vetAll vets every key the usable Settings profiles render, and ends every login attempt whose
// target Settings no longer names, a key Settings dropped included. Every Sync runs it before it
// returns, so a portal change or a rename is acted on before a reader uses the new files.
func vetAll(settings map[string]Profile, usable []string) error {
	var errs []error
	done := map[string]bool{}
	for _, name := range usable {
		p, ok := settings[name]
		if !ok || p.Chained() || done[p.SSOSession()] {
			continue
		}
		done[p.SSOSession()] = true
		errs = append(errs, vetKey(settings, usable, p.SSOSession()))
	}
	endStaleAttempts(settings)
	return errors.Join(errs...)
}

// runningAttempt is one login attempt and the owner and portal it started for. attempt is nil
// until Start returned.
type runningAttempt struct {
	attempt *cloudlogin.Attempt
	target  keyBinding
}

// attempts holds the running login attempts by a registration number of their own, taken before
// the process starts: a sync that sees an attempt's target change ends it and drops the
// registration, a key Settings dropped included, and a landing whose registration is gone is
// never published (bindLanding).
var (
	attemptsMu sync.Mutex
	attempts   = map[uint64]*runningAttempt{}
	attemptSeq uint64
)

// registerAttempt records target before its attempt starts; the returned func drops it.
func registerAttempt(target keyBinding) (uint64, func()) {
	attemptsMu.Lock()
	defer attemptsMu.Unlock()
	attemptSeq++
	id := attemptSeq
	attempts[id] = &runningAttempt{target: target}
	return id, func() {
		attemptsMu.Lock()
		delete(attempts, id)
		attemptsMu.Unlock()
	}
}

// attachAttempt gives registration id its attempt. It reports false when a sync already ended
// the registration (its target changed while the process started): the caller ends a.
func attachAttempt(id uint64, a *cloudlogin.Attempt) bool {
	attemptsMu.Lock()
	defer attemptsMu.Unlock()
	ra, ok := attempts[id]
	if ok {
		ra.attempt = a
	}
	return ok
}

// registered reports whether registration id is still running for its target.
func registered(id uint64) bool {
	attemptsMu.Lock()
	defer attemptsMu.Unlock()
	_, ok := attempts[id]
	return ok
}

// endStaleAttempts ends each running attempt whose target the current Settings does not name,
// under the key's gate, the order a logout takes.
func endStaleAttempts(settings map[string]Profile) {
	var stale []*runningAttempt
	attemptsMu.Lock()
	for id, ra := range attempts {
		if want, ok := settingsOwner(settings, ra.target.Session); ok && want.sameTarget(ra.target) {
			continue
		}
		delete(attempts, id)
		stale = append(stale, ra)
	}
	attemptsMu.Unlock()
	for _, ra := range stale {
		if ra.attempt == nil {
			continue
		}
		gate := logins.Gate(ra.target.Session)
		gate.Lock()
		ra.attempt.End(cloudlogin.PhaseCancelled, settingsChangedLogin)
		gate.Unlock()
	}
}

// settingsChangedLogin is what an attempt ended by a change of its target says.
const settingsChangedLogin = "Settings changed during the login; log in again"

// bindLanding publishes the token a login attempt wrote to its staging cache (staged) as
// session's token, bound to target, inside the key's exclusive lock: only while the attempt is
// still registered (a sync or a replace did not end it), Settings still gives the key that owner
// and portal, and the token is for that portal. Anything else leaves the real cache as it was;
// the staged token goes with the attempt's directory. An attempt's CLI therefore never writes
// where a reader, af-aws-exec or a plain aws, could take its token before it is bound.
func bindLanding(reg uint64, session string, target keyBinding, staged string) error {
	unlock, err := logins.LockKey(session, false)
	if err != nil {
		return err
	}
	defer unlock()
	if !registered(reg) {
		return errors.New(settingsChangedLogin)
	}
	settings := loginSettings()
	want, ok := settingsOwner(settings, session)
	if settings == nil {
		// No Settings list on this Agent yet: the attempt's own target is all there is.
		want, ok = target, true
	}
	url, region, present := tokenPortalAt(staged)
	if !ok || !want.sameTarget(target) || !present || url != target.StartURL || region != target.SSORegion {
		return errors.New(settingsChangedLogin)
	}
	b, err := os.ReadFile(staged)
	if err != nil {
		return err
	}
	var got keyBinding
	if readJSON(bindingPath(session), &got) && !got.sameTarget(want) {
		// Role credentials a former owner took through this key are not this login's.
		for _, r := range got.Roles {
			if err := os.Remove(ssoRoleCachePath(r[0], r[1], session)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		got.Roles = nil
	}
	for _, r := range got.Roles {
		want.Roles = addRole(want.Roles, r[0], r[1])
	}
	if err := os.MkdirAll(filepath.Dir(ssoCachePath(session)), 0o700); err != nil {
		return err
	}
	if err := writeAtomic(ssoCachePath(session), b, 0o600); err != nil {
		return err
	}
	return writeJSONAtomic(bindingPath(session), want)
}

// forgetLeftovers revokes and deletes the per-profile tokens the members of session kept from
// before they joined it under another name (ADR 0125 decision C), but only a key provably left
// by that member. Each is decided inside the key's exclusive lock, from the Settings and the
// member's files as they are then: no usable profile or sign-in renders the key, the member's
// own files define no sso-session of that name (a file that cannot be read keeps everything),
// and its binding names that member's profile.
func forgetLeftovers(members []Profile, session string) {
	for _, m := range members {
		key := "af-" + m.Name
		if key == session || m.ID == "" {
			continue
		}
		forgetLeftover(m, key)
	}
}

func forgetLeftover(m Profile, key string) {
	unlock, err := logins.LockKey(key, false)
	if err != nil {
		return
	}
	defer unlock()
	// Any current Settings owner of the key, usable or not, keeps it: a profile or sign-in that
	// [DEFAULT] or a collision holds back for now still owns its name. No list keeps it too.
	settings := loginSettings()
	if settings == nil {
		return
	}
	if _, owned := settingsOwner(settings, key); owned {
		return
	}
	b, err := os.ReadFile(ConfigPath())
	if err != nil && !os.IsNotExist(err) {
		return
	}
	user, err := stripBlock(string(b))
	if err != nil {
		return
	}
	if _, own := configNames(user); own[key] {
		return
	}
	var got keyBinding
	if !readJSON(bindingPath(key), &got) || got.Owner != "profile:"+m.ID {
		return
	}
	for _, path := range []string{ssoCachePath(key), ssoCachePath(key) + ".af-retired"} {
		snap, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if os.Remove(path) != nil {
			return
		}
		_, _ = revokeSSOToken(snap, m.SSORegion)
	}
	for _, r := range addRole(got.Roles, m.AccountID, m.RoleName) {
		_ = os.Remove(ssoRoleCachePath(r[0], r[1], key))
	}
	_ = os.Remove(bindingPath(key))
}

// id is the owner and portal of b as one string, for a login request and its waiters.
func (b keyBinding) id() string { return b.Owner + "|" + b.StartURL + "|" + b.SSORegion }

// ErrSettingsChangedRun stops a run whose key Settings gave another owner or portal after the
// run was planned.
var ErrSettingsChangedRun = errors.New("the AWS profile's sign-in changed in Settings while this command ran; run it again")

// plannedTarget is the check exportLocked runs under session's lock: Settings still gives the
// key the owner and portal it gave at plan time and, when Agent Fleet used the key at plan time,
// still uses it (not the member's own sso-session now, not held back by a collision) and its
// cache holds nothing vetting would retire. Nil when no Settings profile renders the key.
func plannedTarget(planSettings map[string]Profile, session string) (func(string) error, string) {
	if planSettings == nil {
		return nil, ""
	}
	planned, ok := settingsOwner(planSettings, session)
	if !ok {
		return nil, ""
	}
	_, fleetUsed := keyOwner(planSettings, UsableNow(), session)
	return func(key string) error {
		settings := loginSettings()
		if settings == nil {
			settings = planSettings
		}
		want, ok := settingsOwner(settings, key)
		if !ok || !want.sameTarget(planned) {
			return ErrSettingsChangedRun
		}
		if !fleetUsed {
			// A profile the member's own files define as Settings does: the files are theirs.
			return nil
		}
		v, _, _ := judgeKey(settings, UsableNow(), key)
		if v == keyNotOurs || !usableVerdict(v) {
			return ErrSettingsChangedRun
		}
		return nil
	}, planned.id()
}
