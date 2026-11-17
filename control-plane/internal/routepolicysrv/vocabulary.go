package routepolicysrv

import (
	"slices"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/routepolicy"
)

// vocabularyWire is what a rule may name, as Rule.Validate accepts it: the one list the Console
// draws its pickers from, so an editor never offers a value the CP refuses or misses one it
// accepts. It is built from the same functions Validate reads (routepolicy.Classes and
// Destinations over the embedded route catalogue), holds nothing that varies by tenant, and
// rides only the two policy GETs, so it reaches no reader who cannot already read a policy.
type vocabularyWire struct {
	// Classes are the values a rule's classes list may hold.
	Classes []string `json:"classes"`
	// Destinations are the values a rule's destinations list may hold: every destination except
	// the unclassified one, which Validate refuses in an allow list.
	Destinations []string `json:"destinations"`
	// Routes are the catalogued routes a rule may name in except and deny, with the class and
	// destination the policy judges each by.
	Routes []vocabularyRouteWire `json:"routes"`
}

// vocabularyRouteWire is one catalogued route. PerConnection marks a route whose real class and
// destination come from the endpoint its connection is bound to: it cannot be excepted.
type vocabularyRouteWire struct {
	ID            string `json:"id"`
	Class         string `json:"class"`
	Destination   string `json:"destination"`
	PerConnection bool   `json:"perConnection,omitempty"`
}

func vocabulary() vocabularyWire {
	out := vocabularyWire{
		Classes:      routepolicy.Classes(),
		Destinations: slices.DeleteFunc(routepolicy.Destinations(), func(d string) bool { return d == routepolicy.Unclassified }),
		Routes:       []vocabularyRouteWire{},
	}
	for _, rt := range agentroutes.Routes() {
		out.Routes = append(out.Routes, vocabularyRouteWire{ID: rt.ID, Class: rt.Class, Destination: rt.Destination, PerConnection: rt.PerConnection})
	}
	return out
}
