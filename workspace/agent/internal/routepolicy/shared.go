package routepolicy

import (
	"net/http"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
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
		shared = Open(Path())
		shared.seedFromEnv()
	})
	return shared
}

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
}

// HandleState serves GET /route-policy from the shared store.
func HandleState(w http.ResponseWriter, _ *http.Request) {
	s := Shared()
	st := s.State()
	out := stateWire{Held: st.Held, Placeholder: st.Placeholder, Revision: st.Policy.Revision, Restricted: st.Policy.Restricted,
		CeilingRestricted: st.Policy.CeilingRestricted, Allowed: st.Policy.Allowed, Confirmed: st.Confirmed(s.now())}
	if out.Allowed == nil {
		out.Allowed = []string{}
	}
	if st.Held && st.LastOK.Unix() > 0 {
		out.LastOK = st.LastOK.UTC().Format(time.RFC3339)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
