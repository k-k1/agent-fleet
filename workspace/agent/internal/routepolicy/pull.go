package routepolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cpurl"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/redirectpolicy"
)

// EnvToken and EnvPolicy are what the CP injects: the bridge credential and the effective policy
// at the Workspace's start.
const (
	EnvToken  = "AF_ROUTE_POLICY_TOKEN"
	EnvPolicy = "AF_ROUTE_POLICY"
)

// bridgePath is the CP's answer (control-plane routepolicysrv.BridgePath).
const bridgePath = "/internal/route-policy"

// statusError is an answer of the CP other than 200.
type statusError struct {
	code int
	body string
}

func (e statusError) Error() string {
	return fmt.Sprintf("CP route policy answered %d: %s", e.code, e.body)
}

// unreachable reports a pull failure that shows the CP cannot answer: a network failure (no
// connection, a name that does not resolve, a dropped connection, a timeout), a 5xx (the CP or
// the ingress in front of it down), or no bridge in this deployment (no policy will ever come).
// Anything the CP or the client decided is not an outage and lets no start without a floor go
// on: a 4xx, a malformed 200, a redirect the policy refuses (net/http wraps that in *url.Error
// too, so the wrapper alone proves nothing), a TLS certificate the client rejects.
func unreachable(err error) bool {
	var se statusError
	switch {
	case errors.As(err, &se):
		return se.code >= 500
	case errors.Is(err, ErrBridgeOff):
		return true
	case redirectpolicy.IsRefused(err):
		return false
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		return false
	}
	var ne net.Error
	return ue.Timeout() || errors.As(ue.Err, &ne) || errors.Is(ue.Err, io.EOF) || errors.Is(ue.Err, io.ErrUnexpectedEOF)
}

// ErrBridgeOff reports a deployment that injects no bridge (no PUBLIC_BASE_URL, a CP older than
// the policy): there is then no policy to hold, which is a normal state.
var ErrBridgeOff = errors.New("the credential policy is not distributed in this deployment")

var (
	tokenOnce sync.Once
	token     string
)

// captureToken takes the bridge token out of the environment, once: it is the Agent's own
// credential and no CLI it starts has a use for it, so it leaves os.Environ before any child can
// inherit it (Managed runtimes and Terminal panes alike start from that environment).
func captureToken() string {
	tokenOnce.Do(func() {
		token = os.Getenv(EnvToken)
		_ = os.Unsetenv(EnvToken)
	})
	return token
}

// Fetch asks the CP once. An answer that is not a complete policy is an error, never an empty
// policy.
func Fetch(ctx context.Context) (Policy, error) {
	base, tok := cpurl.Request(), captureToken()
	if base == "" || tok == "" {
		return Policy{}, ErrBridgeOff
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+bridgePath, nil)
	if err != nil {
		return Policy{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := redirectpolicy.Client(0).Do(req)
	if err != nil {
		return Policy{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Policy{}, statusError{code: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}
	return parse(body)
}

// parse reads a policy. Every field is required, the two flags included: a missing or null
// "restricted" read as false would let a partial answer replace a restricting policy.
func parse(b []byte) (Policy, error) {
	var wire struct {
		Revision          *int64    `json:"revision"`
		Restricted        *bool     `json:"restricted"`
		CeilingRestricted *bool     `json:"ceilingRestricted"`
		Allowed           *[]string `json:"allowed"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return Policy{}, fmt.Errorf("CP route policy does not parse: %w", err)
	}
	if wire.Revision == nil || wire.Restricted == nil || wire.CeilingRestricted == nil || wire.Allowed == nil || *wire.Revision < 0 {
		return Policy{}, errors.New("CP route policy is incomplete (revision, restricted, ceilingRestricted and allowed are all required)")
	}
	return Policy{Revision: *wire.Revision, Restricted: *wire.Restricted, CeilingRestricted: *wire.CeilingRestricted, Allowed: *wire.Allowed}, nil
}

// startEnv is the start environment's envelope: the policy, when the CP issued it, or that it
// could not compute it under a restricting ceiling.
type startEnv struct {
	policy   Policy
	issuedAt int64
	unknown  bool
	id       string // the unknown marker's id
}

func readStartEnv() (startEnv, bool) {
	v := strings.TrimSpace(os.Getenv(EnvPolicy))
	if v == "" {
		return startEnv{}, false
	}
	var meta struct {
		IssuedAt *int64 `json:"issuedAt"`
		Unknown  bool   `json:"unknown"`
		ID       string `json:"id"`
	}
	if err := json.Unmarshal([]byte(v), &meta); err != nil {
		log.Printf("route policy: %s does not parse: %v", EnvPolicy, err)
		return startEnv{}, false
	}
	if meta.Unknown {
		if meta.ID == "" {
			log.Printf("route policy: %s says unknown without an id", EnvPolicy)
			return startEnv{}, false
		}
		return startEnv{unknown: true, id: meta.ID}, true
	}
	p, err := parse([]byte(v))
	if err != nil || meta.IssuedAt == nil {
		log.Printf("route policy: %s is incomplete: %v", EnvPolicy, errOr(err, "no issuedAt"))
		return startEnv{}, false
	}
	return startEnv{policy: p, issuedAt: *meta.IssuedAt}, true
}

func errOr(err error, alt string) string {
	if err != nil {
		return err.Error()
	}
	return alt
}

// seedFromEnv takes the start environment into s: the policy, confirmed only up to when the CP
// issued it, or a placeholder when the CP could not compute it under a restricting ceiling.
func (s *Store) seedFromEnv() {
	e, ok := readStartEnv()
	switch {
	case !ok:
	case e.unknown:
		changed, err := s.SeedUnknown(e.id)
		if err != nil {
			log.Printf("route policy (start): %v", err)
		}
		if changed {
			log.Printf("route policy (start): the Control Plane could not compute it under a restricting ceiling; %s", s.State().Describe())
		}
	default:
		changed, err := s.Seed(e.policy, e.issuedAt)
		if err != nil {
			log.Printf("route policy (start): %v", err)
		}
		if changed {
			log.Printf("route policy (start): %s", s.State().Describe())
		}
	}
}

// Sync takes one live answer into the store, logging what changed. It is the puller's step.
//
// A start that could record no floor is released by the CP's current answer (Accept), or by a
// failure that shows the CP cannot answer at all (unreachable): what is on disk then decides.
func (s *Store) Sync(ctx context.Context, why string) error {
	p, err := Fetch(ctx)
	if err != nil {
		if ctx.Err() == nil && unreachable(err) {
			s.release("the Control Plane cannot be reached")
		}
		return err
	}
	changed, err := s.Accept(p)
	if changed {
		log.Printf("route policy (%s): now %s", why, s.State().Describe())
	}
	return err
}

// Pull scheduling. A healthy Agent pulls every 20 to 30 s (jittered, so a fleet restarted
// together does not stay in step), which keeps D12's "at least every 30 seconds". After a
// failure it backs off, doubling from 5 s to at most 2 minutes, jittered too, so an outage or a
// recovery is not met by every Agent at once.
const (
	healthyMin = 20 * time.Second
	healthyMax = 30 * time.Second
	backoffMin = 5 * time.Second
	backoffMax = 2 * time.Minute
	firstMax   = 5 * time.Second
)

// nextDelay is the wait before the next pull after failures consecutive failures; rnd is in [0,1).
func nextDelay(failures int, rnd float64) time.Duration {
	if failures == 0 {
		return healthyMin + time.Duration(rnd*float64(healthyMax-healthyMin))
	}
	d := backoffMin
	for i := 1; i < failures && d < backoffMax; i++ {
		d *= 2
	}
	d = min(d, backoffMax)
	// Equal jitter: between half and all of the backoff.
	return d/2 + time.Duration(rnd*float64(d/2))
}

// Run pulls after a short jittered wait, then on the schedule above, until ctx ends. Boot does not
// wait on it: the start environment was taken when the store was opened (Shared).
func (s *Store) Run(ctx context.Context) {
	s.run(ctx, rand.Float64, func(d time.Duration) <-chan time.Time { return time.After(d) }, time.Now)
}

func (s *Store) run(ctx context.Context, rnd func() float64, after func(time.Duration) <-chan time.Time, now func() time.Time) {
	wait := time.Duration(rnd() * float64(firstMax))
	if s.State().Holding {
		wait = 0 // new work waits on this pull, and one Agent's restart is no herd
	}
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-after(wait):
		}
		started := now()
		// Logged on the transitions only: a long outage is one line when it starts and one when
		// it ends.
		switch err := s.Sync(ctx, "pull"); {
		case errors.Is(err, ErrBridgeOff):
			return
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			if failures == 0 {
				log.Printf("route policy pull: %v (keeping %s)", err, s.State().Describe())
			}
			failures++
		default:
			if failures > 0 {
				log.Printf("route policy pull: the Control Plane answers again (%s)", s.State().Describe())
			}
			failures = 0
		}
		wait = nextDelay(failures, rnd())
		if failures == 0 {
			// A healthy period runs from one request's start to the next's, so a slow answer
			// does not stretch it past healthyMax.
			wait = max(wait-now().Sub(started), 0)
		}
	}
}
