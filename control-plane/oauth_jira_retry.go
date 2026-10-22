// oauth_jira_retry.go — the retry policy for calls to auth.atlassian.com/oauth/token.
//
// Some deployments see bursts of TCP resets towards Atlassian, several within minutes, so
// one short blip must not read as "reconnect Jira". One helper serves both the code grant
// (member waiting on a browser tab) and the refresh grant (the Agent waiting on the CP).
package main

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"net/http"
	"time"
)

// jiraBackoff bounds one token-endpoint call as a whole. budget is the hard ceiling: no
// attempt or sleep is started that cannot finish inside it, because the caller's own
// deadline is shorter than the sum of generous per-attempt timeouts.
type jiraBackoff struct {
	attempts       int
	base, max      time.Duration // exponential: base, 2*base, ... capped at max
	attemptTimeout time.Duration
	budget         time.Duration
	// jitter and sleep are fields so tests run without real seconds or randomness.
	jitter func(time.Duration) time.Duration
	sleep  func(ctx context.Context, d time.Duration) error
}

// equalJitter keeps half the delay and randomises the rest, so callers that were reset
// together do not come back together.
func equalJitter(d time.Duration) time.Duration {
	if d <= 1 {
		return d
	}
	h := d / 2
	return h + time.Duration(rand.Int63n(int64(d-h)+1))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// The refresh budget sits under the 20 s the Agent gives its call to the CP bridge
// (gitx.RefreshOAuthViaCP), which is itself inside the Agent's 30 s jiraRefreshBudget.
// Raising it makes the Agent give up while the CP is still retrying with a token it
// would then not know was spent.
var (
	jiraRefreshRetry = jiraBackoff{
		attempts: 5, base: 500 * time.Millisecond, max: 4 * time.Second,
		attemptTimeout: 6 * time.Second, budget: 17 * time.Second,
		jitter: equalJitter, sleep: sleepCtx,
	}
	// The code grant answers a browser tab, so there is no tight upstream deadline.
	jiraExchangeRetry = jiraBackoff{
		attempts: 4, base: 500 * time.Millisecond, max: 4 * time.Second,
		attemptTimeout: 8 * time.Second, budget: 25 * time.Second,
		jitter: equalJitter, sleep: sleepCtx,
	}
)

// minUsefulAttempt is the least time worth starting another attempt with.
const minUsefulAttempt = time.Second

func transientStatus(code int) bool { return code == http.StatusTooManyRequests || code >= 500 }

// post sends payload as JSON to jiraTokenURL. It retries transport errors, 429 and 5xx and
// returns at once on anything else, including a permanent 4xx such as invalid_grant, so a
// revoked grant is never hammered. When attempts run out it returns the last outcome:
// err != nil for a transport failure, otherwise the last status and body.
func (b jiraBackoff) post(ctx context.Context, payload []byte) (status int, body []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, b.budget)
	defer cancel()
	delay := b.base
	for i := 0; i < b.attempts; i++ {
		if i > 0 {
			d := b.jitter(delay)
			if dl, ok := ctx.Deadline(); ok && time.Until(dl) < d+minUsefulAttempt {
				return status, body, err
			}
			if serr := b.sleep(ctx, d); serr != nil {
				return status, body, err
			}
			if delay *= 2; delay > b.max {
				delay = b.max
			}
		}
		status, body, err = b.once(ctx, payload)
		if err == nil && !transientStatus(status) {
			return status, body, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return status, body, err
}

func (b jiraBackoff) once(ctx context.Context, payload []byte) (int, []byte, error) {
	actx, cancel := context.WithTimeout(ctx, b.attemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, "POST", jiraTokenURL, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := bbHTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if rerr != nil {
		// A reset mid-body is a transport failure too, whatever the status line said.
		return 0, nil, rerr
	}
	return resp.StatusCode, raw, nil
}
