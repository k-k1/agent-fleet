package routepolicy

import (
	"net/http"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

var (
	sharedOnce sync.Once
	shared     *Store
)

// Shared is the Agent's one store: opened at Path on first use, with the bridge token taken out of
// the environment and the start environment's policy taken in before anyone can ask it.
func Shared() *Store {
	sharedOnce.Do(func() {
		captureToken()
		// A tmux server started before the token left this environment (a pane's environment
		// comes from the server) keeps it in its global environment; every launch unsets it.
		tmuxx.AddLaunchEnv([]string{EnvToken}, nil)
		shared = Open(Path(), MirrorPath())
		shared.seedFromEnv()
	})
	return shared
}

// EnforceCapability is what /healthz reports as "route_policy": this Agent enforces the policy at
// its create and start points, so the Control Plane may hand it a restricted member's create
// (ADR 0138 D12, Upgrade). It must equal the Control Plane's routepolicysrv.AgentEnforceCapability.
const EnforceCapability = "enforce-v1"

// stateWire is GET /route-policy: the held policy and its freshness, for the Console and for
// diagnosis. Nothing in it is secret.
type stateWire struct {
	Held              bool     `json:"held"`
	Placeholder       bool     `json:"placeholder,omitempty"`
	Revision          int64    `json:"revision"`
	Restricted        bool     `json:"restricted"`
	CeilingRestricted bool     `json:"ceilingRestricted"`
	Allowed           []string `json:"allowed"`
	LastOK            string   `json:"lastOk,omitempty"`
	Confirmed         bool     `json:"confirmed"`
	Holding           bool     `json:"holding,omitempty"`
}

// HandleState serves GET /route-policy from the shared store.
func HandleState(w http.ResponseWriter, _ *http.Request) {
	s := Shared()
	st := s.State()
	out := stateWire{Held: st.Held, Placeholder: st.Placeholder, Revision: st.Policy.Revision, Restricted: st.Policy.Restricted,
		CeilingRestricted: st.Policy.CeilingRestricted, Allowed: st.Policy.Allowed, Confirmed: st.Confirmed(s.now()), Holding: st.Holding}
	if out.Allowed == nil {
		out.Allowed = []string{}
	}
	if st.Held && st.LastOK.Unix() > 0 {
		out.LastOK = st.LastOK.UTC().Format(time.RFC3339)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
