package gitx

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// bbFreshWindow renews slightly before the stamp says to: a token that expires while a
// request is in flight is a 401 the caller then has to recover from.
const bbFreshWindow = 120 * time.Second

// bbRefreshMu serializes the grant within this process, in front of the cross-process
// lock, so handlers of the Agent queue here instead of polling the file lock.
var bbRefreshMu sync.Mutex

// bbRefreshWait bounds one renewal: the wait for the cross-process lock, the store read and
// the grant itself (every grant attempt ends with the same context). Worst-case hold of the
// lock is therefore the wait plus the 10 s persist, which can exceed a waiter's own wait: it
// then fails once and the next call adopts the stored pair. Callers sit in front of git (the
// credential helper) and must not wait forever.
var bbRefreshWait = 30 * time.Second

// errBitbucketReplaced: the stored connection is no longer the one being renewed.
var errBitbucketReplaced = errors.New("bitbucket connection was disconnected or replaced")

// RefreshBitbucketStored renews the stored Bitbucket OAuth pair and updates c in place.
// It is the only path that may run the refresh grant.
//
// The Agent's handlers and every `workspace-agent cred` git spawns are separate
// processes holding the same refresh token; if bitbucket.org rotates it, presenting one
// twice can revoke the chain. So the grant runs under the "bitbucket-refresh" store lock,
// and inside it the store is read again and is the only source: a caller whose token is
// no longer the stored one adopts what another process renewed (when that pair is still
// fresh) instead of exchanging, and a caller whose token IS the stored one exchanges the
// stored refresh token, never its own possibly stale copy.
//
// A forced refresh (a 401 despite a fresh-looking token) is a legitimate caller whose token
// IS the stored one, so there is deliberately no "stored token is still fresh, do nothing"
// shortcut here: callers decide whether a renewal is due.
//
// The result is written with secrets.Update, never Load … Save: a snapshot taken before the
// grant must not resurrect a connection that was disconnected, or replaced by a reconnect,
// while the grant ran. In that case nothing is written and the error is errBitbucketReplaced.
func RefreshBitbucketStored(ctx context.Context, c *secrets.BitbucketCreds) error {
	bbRefreshMu.Lock()
	defer bbRefreshMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, bbRefreshWait)
	defer cancel()
	seen := c.AccessToken
	return secrets.WithLockContext(wctx, "bitbucket-refresh", func() error {
		cur, err := secrets.LoadContext(wctx)
		if err != nil {
			return err
		}
		d := cur.Bitbucket
		if d == nil || d.AccessToken == "" {
			return errBitbucketReplaced
		}
		if c.Account != "" && d.Account != "" && c.Account != d.Account {
			return errBitbucketReplaced
		}
		if d.AccessToken != seen && d.Expiry > time.Now().Add(bbFreshWindow).Unix() {
			adoptBitbucket(c, d)
			return nil
		}
		nc, err := refreshBitbucketGrantContext(wctx, *d)
		if err != nil {
			return err
		}
		// The grant has already spent the old refresh token, so the write gets its own
		// deadline rather than whatever is left of the wait.
		pctx, pcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer pcancel()
		err = secrets.UpdateContext(pctx, func(s *secrets.Data) error {
			if s.Bitbucket == nil || s.Bitbucket.AccessToken == "" {
				return errBitbucketReplaced
			}
			if s.Bitbucket.RefreshToken != d.RefreshToken {
				// Reconnected during the grant (a reconnect does not take the lock): what is
				// stored is newer than this grant's pair. Hand it to the caller instead,
				// unless it is another account's.
				if c.Account != "" && s.Bitbucket.Account != "" && c.Account != s.Bitbucket.Account {
					return errBitbucketReplaced
				}
				nc = *s.Bitbucket
				return nil
			}
			adoptBitbucket(s.Bitbucket, &nc)
			return nil
		})
		if err != nil {
			if errors.Is(err, errBitbucketReplaced) {
				return err
			}
			return fmt.Errorf("persisting the refreshed bitbucket token: %w", err)
		}
		adoptBitbucket(c, &nc)
		return nil
	})
}

// adoptBitbucket copies the token fields of src into dst, leaving the identity fields
// (Account, Email) of dst alone. Scopes are kept when src records none. The legacy client
// key/secret travel with the pair: a snapshot that kept an old copy would write it back
// on its next Save after another process dropped it.
func adoptBitbucket(dst, src *secrets.BitbucketCreds) {
	dst.AccessToken, dst.RefreshToken, dst.Expiry = src.AccessToken, src.RefreshToken, src.Expiry
	dst.Key, dst.Secret = src.Key, src.Secret
	if src.Scopes != "" {
		dst.Scopes = src.Scopes
	}
}
