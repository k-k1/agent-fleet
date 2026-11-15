package routepolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
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

// ErrBridgeOff reports a deployment that injects no bridge (no PUBLIC_BASE_URL, a CP older than
// the policy): there is then no policy to hold, which is a normal state.
var ErrBridgeOff = errors.New("the credential policy is not distributed in this deployment")

// Fetch asks the CP once. An answer that is not a policy (an error page, a body without the
// allowed list) is an error, never an empty policy.
func Fetch(ctx context.Context) (Policy, error) {
	base, token := cpurl.Request(), os.Getenv(EnvToken)
	if base == "" || token == "" {
		return Policy{}, ErrBridgeOff
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+bridgePath, nil)
	if err != nil {
		return Policy{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := redirectpolicy.Client(0).Do(req)
	if err != nil {
		return Policy{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Policy{}, fmt.Errorf("CP route policy answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return parse(body)
}

func parse(b []byte) (Policy, error) {
	var wire struct {
		Revision          *int64    `json:"revision"`
		Restricted        bool      `json:"restricted"`
		CeilingRestricted bool      `json:"ceilingRestricted"`
		Allowed           *[]string `json:"allowed"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return Policy{}, fmt.Errorf("CP route policy does not parse: %w", err)
	}
	if wire.Revision == nil || wire.Allowed == nil || *wire.Revision < 0 {
		return Policy{}, errors.New("CP route policy has no revision or no allowed list")
	}
	return Policy{Revision: *wire.Revision, Restricted: wire.Restricted, CeilingRestricted: wire.CeilingRestricted, Allowed: *wire.Allowed}, nil
}

// FromEnv is the policy the CP put in the start environment, ok=false when there is none.
func FromEnv() (Policy, bool) {
	v := strings.TrimSpace(os.Getenv(EnvPolicy))
	if v == "" {
		return Policy{}, false
	}
	p, err := parse([]byte(v))
	if err != nil {
		log.Printf("route policy: %s: %v", EnvPolicy, err)
		return Policy{}, false
	}
	return p, true
}

// Sync takes one answer into the store, logging what changed. It is the puller's step.
func (s *Store) Sync(ctx context.Context, why string) error {
	p, err := Fetch(ctx)
	if err != nil {
		return err
	}
	changed, err := s.Accept(p)
	if changed {
		log.Printf("route policy (%s): now %s", why, s.State().Describe())
	}
	return err
}

// Run takes the start environment's policy, then pulls now and every PullInterval until ctx ends.
// The first pull is not waited for by the caller: boot must not wait on the CP.
func (s *Store) Run(ctx context.Context) {
	if p, ok := FromEnv(); ok {
		if changed, err := s.Accept(p); err != nil {
			log.Printf("route policy: keeping the start policy: %v", err)
		} else if changed {
			log.Printf("route policy (start): %s", s.State().Describe())
		}
	}
	t := time.NewTicker(PullInterval)
	defer t.Stop()
	failing := false
	for {
		// Logged on the transitions only: a pull every 30 s through a long outage is one line
		// when it starts and one when it ends.
		switch err := s.Sync(ctx, "pull"); {
		case errors.Is(err, ErrBridgeOff):
			return
		case err != nil && ctx.Err() == nil:
			if !failing {
				log.Printf("route policy pull: %v (keeping %s)", err, s.State().Describe())
			}
			failing = true
		case err == nil && failing:
			log.Printf("route policy pull: the Control Plane answers again (%s)", s.State().Describe())
			failing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
