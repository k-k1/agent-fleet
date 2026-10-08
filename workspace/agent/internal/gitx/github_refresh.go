package gitx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Renewal of an expiring GitHub App user token from the device flow (ADR 0052 decision 8).
//
// A GitHub App with "Expire user authorization tokens" on answers the device flow with an
// 8-hour access token and a 6-month refresh token. The refresh grant of a device-flow
// token needs no client_secret, so the Agent renews directly against github.com and no
// secret is stored: only the refresh token, beside the access token in the same
// encrypted entry. A token with no refresh token (a PAT, or an app that does not expire
// its tokens) never reaches this code.
//
// Refresh tokens are single use. Two callers that both spent the same one would strand
// the connection, so the grant runs under a cross-process file lock (the agent and the
// git credential helper are separate processes) and re-reads the store inside it: a
// caller that finds a token other than the one it saw rejected or expiring simply uses
// that one.

// ErrGitHubUnauthorized marks a 401 from api.github.com: the access token was rejected.
var ErrGitHubUnauthorized = errors.New("github token rejected (re-connect GitHub)")

// ErrGitHubReconnect is returned once GitHub has refused the refresh token for good. The
// member must run the device flow again; nothing else repairs it.
var ErrGitHubReconnect = errors.New("the GitHub connection has expired — reconnect GitHub in Settings > Connections")

// NewGitHubUnauthorized builds a 401 error that keeps its own wording but is recognised
// by errors.Is(err, ErrGitHubUnauthorized).
func NewGitHubUnauthorized(msg string) error { return &unauthorizedError{msg} }

type unauthorizedError struct{ msg string }

func (e *unauthorizedError) Error() string        { return e.msg }
func (e *unauthorizedError) Is(target error) bool { return target == ErrGitHubUnauthorized }

const githubHost = "github.com"

// Variables so tests can point the grant at an httptest server and skip the waits.
var (
	githubTokenURL       = "https://github.com/login/oauth/access_token"
	githubRefreshBackoff = []time.Duration{500 * time.Millisecond, 2 * time.Second}
	// The client refuses every redirect: the request body carries the refresh token and
	// the client id, and nothing at github.com's token endpoint legitimately redirects.
	githubRefreshClient = &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// githubRefreshBudget bounds one renewal end to end: waiting for the lock, the grant
	// and its retries. git's credential helper and the worktree plan both sit in front of
	// it.
	githubRefreshBudget = 30 * time.Second
)

// githubRefreshMargin is how long before expiry a token is renewed. Longer than a clone
// or a push that started just before the deadline.
const githubRefreshMargin = 5 * time.Minute

// errGitHubNotConnected is the sentinel the handlers hand WithGitHubToken so they can
// keep their own "not connected" answer.
var errGitHubNotConnected = errors.New("GitHub is not connected")

// errStoreWrite: GitHub renewed the pair but the store would not take it. Carries no secret.
var errStoreWrite = errors.New("github token was renewed but could not be saved to the credential store; retrying on the next use (reconnect GitHub if this persists)")

// errRefreshBusy: the renewal lock stayed taken for the whole budget. Transient.
var errRefreshBusy = errors.New("github token renewal is busy; retrying on the next use")

var errNoRefresh = errors.New("no github refresh token stored")

// errSuperseded ends a store update that found a different token than the one it was
// renewing: the member reconnected meanwhile, and their entry is not ours to overwrite.
var errSuperseded = errors.New("github connection replaced meanwhile")

// ghPending holds a renewed pair that could not be written to the store. The old refresh
// token is spent by then, so the pair must not be lost: the next caller in this process
// writes it before asking GitHub for anything.
var ghPending struct {
	mu   sync.Mutex
	from string // the access token the store still holds
	next secrets.GitEntry
	set  bool
}

// GitHubReconnectNeeded reports whether the entry's connection can no longer be renewed:
// GitHub refused the refresh token, or both tokens have run out.
func GitHubReconnectNeeded(e secrets.GitEntry) bool {
	if e.ReconnectNeeded {
		return true
	}
	now := time.Now().Unix()
	return e.RefreshToken != "" && e.RefreshExpiry > 0 && now >= e.RefreshExpiry &&
		e.Expiry > 0 && now >= e.Expiry
}

// GitHubToken returns the GitHub access token to use now, renewing it first when it is
// within the margin of expiry. "" with a nil error means GitHub is not connected. s is
// updated in place when a renewal happened. A transient renewal failure keeps the old
// token while it is still valid; the next call retries.
func GitHubToken(s *secrets.Data) (string, error) {
	return GitHubTokenContext(context.Background(), s)
}

// GitHubTokenContext is GitHubToken whose wait (for the renewal lock and for GitHub) also
// ends with ctx, on top of githubRefreshBudget.
func GitHubTokenContext(ctx context.Context, s *secrets.Data) (string, error) {
	e, ok := s.Git[githubHost]
	if !ok || e.Token == "" {
		return "", nil
	}
	if e.RefreshToken == "" {
		return e.Token, nil
	}
	now := time.Now()
	// A pair this process renewed but could not write is saved before anything else:
	// the stored one is spent, and a reconnect flag a racing caller set meanwhile
	// does not make the held pair any less good.
	if !hasGitHubPending(e.Token) {
		if GitHubReconnectNeeded(e) {
			return "", ErrGitHubReconnect
		}
		if e.Expiry == 0 || now.Before(time.Unix(e.Expiry, 0).Add(-githubRefreshMargin)) {
			return e.Token, nil
		}
	}
	ne, err := refreshGitHub(ctx, e.Token)
	if err != nil {
		if errors.Is(err, ErrGitHubReconnect) || errors.Is(err, errStoreWrite) {
			return "", err
		}
		// Judged when the failure is known, not before the waits: the token may have run
		// out while the renewal was retrying.
		if e.Expiry != 0 && time.Now().Before(time.Unix(e.Expiry, 0)) {
			log.Printf("github token refresh failed, using the current token until it expires: %v", err)
			return e.Token, nil
		}
		return "", err
	}
	s.AdoptGitHub(ne)
	return ne.Token, nil
}

// GitHubForceRefresh renews after the API rejected `rejected` with a 401, whatever the
// recorded expiry says (clock skew, or a token GitHub revoked early). When another
// caller already renewed, that newer token comes back without a second grant.
func GitHubForceRefresh(s *secrets.Data, rejected string) (string, error) {
	return GitHubForceRefreshContext(context.Background(), s, rejected)
}

// GitHubForceRefreshContext is GitHubForceRefresh bounded by ctx as well.
func GitHubForceRefreshContext(ctx context.Context, s *secrets.Data, rejected string) (string, error) {
	ne, err := refreshGitHub(ctx, rejected)
	if err != nil {
		return "", err
	}
	s.AdoptGitHub(ne)
	return ne.Token, nil
}

// RenewRejectedGitHubToken is GitHubForceRefresh for a caller that holds no snapshot.
func RenewRejectedGitHubToken(ctx context.Context, rejected string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, githubRefreshBudget) // the first Load is bounded too
	defer cancel()
	s, err := secrets.LoadContext(ctx)
	if err != nil {
		return "", err
	}
	return GitHubForceRefreshContext(ctx, s, rejected)
}

// WithGitHubToken runs fn with the current GitHub token and, when GitHub answers it with
// ErrGitHubUnauthorized and the connection is renewable, renews once and runs fn again.
// A connection with no refresh token behaves exactly as before: one call, the 401 passes
// through. "GitHub is not connected" stays the caller's wording: fn is not run then, and
// notConnected is returned.
func WithGitHubToken(s *secrets.Data, notConnected error, fn func(token string) error) error {
	tok, err := GitHubToken(s)
	if err != nil {
		return err
	}
	if tok == "" {
		return notConnected
	}
	err = fn(tok)
	if !errors.Is(err, ErrGitHubUnauthorized) || s.Git[githubHost].RefreshToken == "" {
		return err
	}
	nt, rerr := GitHubForceRefresh(s, tok)
	if rerr != nil {
		if errors.Is(rerr, ErrGitHubReconnect) {
			return rerr
		}
		return err // transient: report the original rejection
	}
	if nt == tok {
		return err
	}
	return fn(nt)
}

// refreshGitHub renews the pair whose access token is `seen`, under the cross-process
// lock, and returns the entry now in force. The whole call — the wait for the lock, the
// grant and its retries — is bounded by githubRefreshBudget and ctx: a holder that is
// stopped or slow costs the others that long, never indefinitely. Running out of time is
// a transient failure and never marks the connection. Writing the grant's result is the
// one step outside that bound: it has its own short deadline (storeWriteContext, up to
// three tries), so a call can run that much past the budget rather than lose a spent pair.
//
// A renewal is reported as done only once it is in the store. When the write fails the
// spent refresh token cannot be asked again, so the new pair is kept in this process
// (ghPending) and the next call here writes it first. That rescue lives only as long as
// the process: a git credential helper exits after one answer, and the pair is then lost
// — the next renewal finds GitHub refusing the spent token and the member reconnects.
func refreshGitHub(ctx context.Context, seen string) (secrets.GitEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, githubRefreshBudget)
	defer cancel()
	var out secrets.GitEntry
	err := secrets.WithLockContext(ctx, "github-refresh", func() error {
		cur, err := secrets.LoadContext(ctx)
		if err != nil {
			return err
		}
		e := cur.Git[githubHost]
		out = e
		if e.Token == "" || e.RefreshToken == "" {
			return errNoRefresh
		}
		if next, ok := takeGitHubPending(e.Token); ok {
			switch perr := persistGitHub(e.Token, next); {
			case perr == nil:
				out = next
				return nil
			case errors.Is(perr, errSuperseded):
				out = reloadGitHub(out)
				return nil
			default:
				setGitHubPending(e.Token, next)
				log.Printf("github token refresh: still cannot write the renewed pair: %v", perr)
				return errStoreWrite
			}
		}
		if e.Token != seen {
			return nil // somebody else renewed, or the member reconnected
		}
		if e.ReconnectNeeded {
			return ErrGitHubReconnect
		}
		now := time.Now().Unix()
		if e.RefreshExpiry > 0 && now >= e.RefreshExpiry {
			markGitHubReconnect(e.Token)
			return ErrGitHubReconnect
		}
		grant, err := postGitHubRefresh(ctx, e.ClientID, e.RefreshToken)
		if err != nil {
			if errors.Is(err, ErrGitHubReconnect) {
				markGitHubReconnect(e.Token)
			}
			return err
		}
		next := e
		next.Token = grant.AccessToken
		if grant.RefreshToken != "" {
			next.RefreshToken = grant.RefreshToken
		}
		next.Expiry, next.RefreshExpiry = 0, 0
		if grant.ExpiresIn > 0 {
			next.Expiry = now + grant.ExpiresIn
		}
		if grant.RefreshExpiresIn > 0 {
			next.RefreshExpiry = now + grant.RefreshExpiresIn
		}
		var perr error
		for i := 0; i < 3; i++ {
			if perr = persistGitHub(e.Token, next); perr == nil || errors.Is(perr, errSuperseded) {
				break
			}
		}
		switch {
		case perr == nil:
			out = next
		case errors.Is(perr, errSuperseded):
			out = reloadGitHub(e)
		default:
			setGitHubPending(e.Token, next)
			log.Printf("github token refresh: renewed, but writing the store failed: %v", perr)
			return errStoreWrite
		}
		return nil
	})
	if errors.Is(err, secrets.ErrLockTimeout) {
		return out, errRefreshBusy
	}
	return out, err
}

// storeWriteContext bounds a store write that follows a grant. It is deliberately not the
// renewal's budget: the budget may be spent by then, and the spent refresh token makes
// writing the result the one thing that must still be tried.
func storeWriteContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// reloadGitHub returns the entry the store holds now, or fallback when it cannot be read.
func reloadGitHub(fallback secrets.GitEntry) secrets.GitEntry {
	ctx, cancel := storeWriteContext()
	defer cancel()
	if d, err := secrets.LoadContext(ctx); err == nil {
		return d.Git[githubHost]
	}
	return fallback
}

// persistGitHub writes next over the entry whose access token is still `from`. Fields
// the renewal does not own (the cached login and email) are kept from the stored entry.
func persistGitHub(from string, next secrets.GitEntry) error {
	ctx, cancel := storeWriteContext()
	defer cancel()
	return secrets.UpdateContext(ctx, func(d *secrets.Data) error {
		cur := d.Git[githubHost]
		if cur.Token != from {
			return errSuperseded
		}
		cur.Token, cur.RefreshToken = next.Token, next.RefreshToken
		cur.Expiry, cur.RefreshExpiry = next.Expiry, next.RefreshExpiry
		cur.ReconnectNeeded = false
		d.Git[githubHost] = cur
		return nil
	})
}

func markGitHubReconnect(from string) {
	ctx, cancel := storeWriteContext()
	defer cancel()
	_ = secrets.UpdateContext(ctx, func(d *secrets.Data) error {
		cur := d.Git[githubHost]
		if cur.Token != from {
			return errSuperseded
		}
		cur.ReconnectNeeded = true
		d.Git[githubHost] = cur
		return nil
	})
}

func setGitHubPending(from string, next secrets.GitEntry) {
	ghPending.mu.Lock()
	defer ghPending.mu.Unlock()
	ghPending.from, ghPending.next, ghPending.set = from, next, true
}

func hasGitHubPending(from string) bool {
	ghPending.mu.Lock()
	defer ghPending.mu.Unlock()
	return ghPending.set && ghPending.from == from
}

func takeGitHubPending(from string) (secrets.GitEntry, bool) {
	ghPending.mu.Lock()
	defer ghPending.mu.Unlock()
	if !ghPending.set || ghPending.from != from {
		return secrets.GitEntry{}, false
	}
	ghPending.set = false
	return ghPending.next, true
}

type githubGrant struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_token_expires_in"`
	Error            string `json:"error"`
}

// postGitHubRefresh runs the refresh grant. Network errors, 5xx and 429 are retried a
// bounded number of times; an `error` answer from GitHub is final and means reconnect.
// Neither the request nor the response body ever reaches an error string — both carry
// tokens.
func postGitHubRefresh(ctx context.Context, clientID, refreshToken string) (githubGrant, error) {
	if clientID == "" {
		return githubGrant{}, ErrGitHubReconnect // an entry from before the id was stored
	}
	form := url.Values{"client_id": {clientID}, "grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}
	var lastErr error
	for attempt := 0; attempt <= len(githubRefreshBackoff); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return githubGrant{}, lastErr
			case <-time.After(githubRefreshBackoff[attempt-1]):
			}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", githubTokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return githubGrant{}, fmt.Errorf("github refresh: build request")
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := githubRefreshClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("github refresh: github.com unreachable")
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			// Not followed, and the Location (which may carry anything) is not repeated.
			return githubGrant{}, fmt.Errorf("github refresh: unexpected redirect (%d)", resp.StatusCode)
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("github refresh: github.com answered %d", resp.StatusCode)
			continue
		}
		var g githubGrant
		if json.Unmarshal(raw, &g) != nil {
			lastErr = fmt.Errorf("github refresh: unreadable answer (%d)", resp.StatusCode)
			continue
		}
		if g.AccessToken != "" {
			return g, nil
		}
		if g.Error != "" {
			log.Printf("github refresh refused: %s", safeOAuthCode(g.Error))
			return githubGrant{}, ErrGitHubReconnect
		}
		lastErr = fmt.Errorf("github refresh: answer without a token (%d)", resp.StatusCode)
	}
	return githubGrant{}, lastErr
}

// safeOAuthCode keeps an OAuth error code printable: lower-case letters and underscores
// only, bounded. The value is remote input and goes to a log line.
func safeOAuthCode(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || r == '_' {
			b.WriteRune(r)
		}
		if b.Len() >= 40 {
			break
		}
	}
	return b.String()
}
