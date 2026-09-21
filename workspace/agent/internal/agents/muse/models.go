package muse

// The launch-time model catalog and the reasoning-effort list (ADR 0095 decision 10), both
// read off the protocol rather than a CLI subcommand: `muse --help` has no `models` verb at
// all, so `model/list` over `serve` is the only route. It is a query — no commandId, no
// durable record — and it needs no session, which is what makes a short-lived host acceptable.

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// modelsTTL is kiro's and cursor's: the Console refetches the picker on every mount, and the
// answer is an account catalog that moves at release cadence. It matters more here than
// there, because a miss costs a 299 MB binary's process start rather than a CLI call.
const modelsTTL = 10 * time.Minute

var modelsMu sync.Mutex
var modelsAt time.Time
var modelsList []agents.ModelChoice // nil = never fetched, or every fetch so far failed
var modelsSafe string               // the catalog's first non-data-sharing id; "" when it has none

// Models returns the account's selectable launch models. Empty is a valid answer — the picker
// then offers only the default — and so is the pre-install, pre-sign-in state, which is the
// common one: the catalog is the authenticated account's (`source: providerCatalog`), so
// there is nothing to ask before a credential exists.
func Models() []agents.ModelChoice {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	if modelsList != nil && time.Since(modelsAt) < modelsTTL {
		return modelsList
	}
	list, safe, err := probeModels()
	if err != nil {
		// Stale-if-error, the shape every other kind's catalog uses: a transient failure must
		// not empty a picker that worked a minute ago.
		return modelsList
	}
	modelsList, modelsSafe, modelsAt = list, safe, time.Now()
	return modelsList
}

// SafeDefaultModel is the model id AF starts a session on when the member chose none, over a
// connection the caller already holds. "" means "send no modelId", which hands the choice back
// to the host.
//
// 🔴 It exists because the host's own default is the one decision 6 clamp 8 is about. Measured
// on 1.3.0-R3401.1, the catalog's `isDefault: true` row is `muse-spark-1.3-contributor`, whose
// description reads "Your content, including inter-session messages, may be used for product
// improvement" — so omitting `modelId` is not a neutral act, it opts the member's conversation
// into product-improvement use without their ever seeing the word. The member keeps the choice
// (the contributor variants stay in the picker, at the same price); what AF picks for them when
// they have not made one is the other direction.
func SafeDefaultModel(cl *msp.Client) string {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	if modelsList != nil && time.Since(modelsAt) < modelsTTL {
		return modelsSafe
	}
	list, safe, err := modelsFrom(cl)
	if err != nil {
		// The catalog is not answerable right now. Returning the stale pick rather than ""
		// keeps a restart on the model the session already had; "" would silently fall back
		// to the host's contributor default.
		return modelsSafe
	}
	modelsList, modelsSafe, modelsAt = list, safe, time.Now()
	return modelsSafe
}

// effortChoices is what the picker offers for reasoning effort, and it is the generated wire
// enum rather than a list of its own (msp.ReasoningEffortValues, schema order — which is also
// the order `muse --help` prints). Every model gets the same set: effort is a per-turn
// parameter of `turn/start`, not a property of the catalog entry, and the catalog says nothing
// about it.
func effortChoices() []string {
	out := make([]string, 0, len(msp.ReasoningEffortValues))
	for _, e := range msp.ReasoningEffortValues {
		out = append(out, string(e))
	}
	return out
}

var errMuseAbsent = errors.New("muse is not installed")
var errMuseSignedOut = errors.New("muse has no stored credential")

// probeModels answers from a live session's host when there is one, and spawns a short-lived
// one otherwise.
//
// Reusing a live host is not only cheaper, it is the case that actually happens: the picker is
// usually opened while muse sessions are running, and `model/list` is a query that touches no
// session state. A live host that fails the call falls through to a fresh one rather than
// reporting failure — it may be mid-shutdown.
func probeModels() ([]agents.ModelChoice, string, error) {
	if !Installed() {
		return nil, "", errMuseAbsent
	}
	if !readCredential().Present {
		return nil, "", errMuseSignedOut
	}
	for _, h := range liveHandles() {
		h.mu.Lock()
		cl := h.cl
		h.mu.Unlock()
		if cl == nil {
			continue
		}
		if list, safe, err := modelsFrom(cl); err == nil {
			return list, safe, nil
		}
	}
	cl, stop, err := dialProbe()
	if err != nil {
		return nil, "", err
	}
	defer stop()
	return modelsFrom(cl)
}

// modelsFrom turns one `model/list` answer into the picker's rows.
//
// No sessionId is sent: that parameter only flags the row matching a session's effective
// model, and this list is a LAUNCH menu with no session behind it. `isDefault` is not folded
// into the labels either — the picker's own "Default" entry means "let AF choose", and what AF
// chooses is the second return: the first row the vendor does not say it may learn from, in
// the catalog's own order, which is newest first.
func modelsFrom(cl *msp.Client) ([]agents.ModelChoice, string, error) {
	var res msp.ModelListResult
	if err := cl.CallInto(msp.MethodModelList, msp.ModelListParams{}, callTimeout, &res); err != nil {
		return nil, "", err
	}
	efforts := effortChoices()
	list := make([]agents.ModelChoice, 0, len(res.Models))
	safe := ""
	for _, m := range res.Models {
		if m.ModelID == "" {
			continue
		}
		label := m.DisplayLabel
		if label == "" {
			label = m.ModelID
		}
		if safe == "" && !dataSharingModel(m) {
			safe = m.ModelID
		}
		// DefaultEffort stays empty on purpose. The catalog carries no effort at all and
		// nothing on the wire echoes the one a session would use with none sent, so naming
		// the CLI flag's documented default here would be a claim about `serve` made from
		// `muse --help`. The picker then says plain "Default", which is exactly true.
		list = append(list, agents.ModelChoice{ID: m.ModelID, Label: label, Efforts: efforts})
	}
	return list, safe, nil
}

// dataSharingModel reports that the vendor says this model's conversations may be used to
// improve the product (decision 6 clamp 8).
//
// Two signals, and either one is enough. Measured on 1.3.0-R3401.1 they agree exactly — the
// four rows are `muse-spark-1.{2,3}` and `muse-spark-1.{2,3}-contributor`, the two contributor
// rows are the only ones carrying a `description` at all, and it is the product-improvement
// sentence verbatim — so today the union is redundant. It is a union anyway because the two
// fail in opposite directions: a renamed suffix leaves the sentence, a reworded sentence
// leaves the suffix, and the cost of the two errors is not symmetric. A false positive costs
// a model AF will not pick by default; a false negative costs the member's conversations.
func dataSharingModel(m msp.ModelCatalogEntry) bool {
	if strings.HasSuffix(m.ModelID, "-contributor") {
		return true
	}
	if m.Description == nil {
		return false
	}
	return strings.Contains(strings.ToLower(*m.Description), "product improvement")
}

// dialProbe starts a host for a query and returns it with the function that reaps it.
//
// Its argv is deliberately NOT serveArgs(): `--trust-workspace` is a decision about a working
// copy (it loads that repository's rules and skills) and a catalog query has no working copy
// to make it about. `--disable-sandbox` stays, because it is what this container can run at
// all (ADR 0095 gate A).
func dialProbe() (*msp.Client, func(), error) {
	// The clamps before the spawn, the same order and the same fail-close as the driver's
	// Resume. This host starts no session and can therefore run no subagent, no observer and
	// no context assembly — but "every path that spawns a host applies the clamps first" is
	// an invariant that survives someone teaching this path to do more, and reasoning about
	// which host is harmless is exactly the reasoning that rots.
	if err := EnsureClamps(); err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(Bin(), "serve", "--disable-sandbox")
	cmd.Dir = paths.HomeDir()
	cmd.Env = childEnv(os.Environ())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("muse serve の起動に失敗しました: %w", err)
	}
	stop := func() { stopChild(cmd, stdin) }
	// No handler: a host with no session emits nothing this caller reacts to, and the client
	// drops what it cannot route.
	cl := msp.NewClient(stdin, stdout, msp.Handler{})
	res, err := msp.Handshake(cl, clientVersion, nil)
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("Muse Code との接続に失敗しました: %w", err)
	}
	if d := msp.SchemaDrift(res); d != "" {
		log.Printf("muse: model/list probe: %s", d)
	}
	return cl, stop, nil
}
