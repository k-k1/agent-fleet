package imagegen

// The Agent's REST face for image generation (ADR 0069 decision 8). Its only caller is the
// session-side `af` MCP server, which reaches it over agentBaseURL() the way every other
// session tool does — mcpx cannot import main, and the provider work has to happen in the
// Agent, where the container's credentials and disk are.
//
// Neither route is proxied by the Control Plane: nothing in the Console calls them, so they
// stay off the CP's agent-proxy allowlist deliberately rather than by omission.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Enabled is the opt-in gate, installed by the ui-prefs layer at startup (the same hook shape
// as mcpreg.PeerMessagingEnabled). nil means OFF, which is also the product default: this
// spends the user's ChatGPT plan quota from sessions that are not Codex sessions, invisibly.
//
// The gate is applied HERE as well as in the MCP server's advertised set. The MCP surface
// alone would leave the REST route open to anything holding AGENT_TOKEN — which every
// session's own MCP server does — so the preference would be one curl away from bypass.
var Enabled func() bool

func enabled() bool { return Enabled != nil && Enabled() }

// statusResponse answers "should this session be offered the tool, and by what route". The
// RULE that turns it into a yes or no lives in the MCP server (mcpx), where the tool list is
// built; this endpoint reports facts.
type statusResponse struct {
	Enabled  bool     `json:"enabled"`
	Provider string   `json:"provider,omitempty"` // the effective provider, "" when none is ready
	Ready    bool     `json:"ready"`
	Kind     string   `json:"kind,omitempty"` // the asking session's agent kind, "" when unknown
	Model    string   `json:"model,omitempty"`
	Ops      []string `json:"ops,omitempty"`
	// AspectRatios is the effective provider's own list, so the MCP schema can offer the
	// parameter only where it actually reaches the tool. Empty means the tool must not
	// advertise it at all rather than accept it and drop it.
	AspectRatios []string `json:"aspectRatios,omitempty"`
	// Order is the effective provider order, so the answer to "why did it route there" is
	// readable without guessing at a preference file, and a settings UI has something to
	// render when there is more than one provider to rank.
	Order []string `json:"order,omitempty"`
	// Providers is EVERY ready provider, in the effective order, each with its own capability
	// list. The flat fields above describe only the first one — which was enough while the
	// caller could not choose, and stopped being enough the moment `generate_image` grew a
	// `provider` argument: a tool that offers a choice has to advertise what each choice can
	// do, or the enum is a guess.
	Providers []providerStatus `json:"providers,omitempty"`
}

// providerStatus is one ready provider as the tool surface needs to see it.
type providerStatus struct {
	ID           string   `json:"id"`
	Model        string   `json:"model,omitempty"`
	Ops          []string `json:"ops,omitempty"`
	AspectRatios []string `json:"aspectRatios,omitempty"`
}

// HandleStatus answers GET /imagegen/status?session=<name>.
func HandleStatus(w http.ResponseWriter, r *http.Request) {
	out := statusResponse{Enabled: enabled(), Order: effectiveOrder()}
	if name := r.URL.Query().Get("session"); session.ValidName(name) {
		if m, ok := session.ReadMeta(name); ok {
			out.Kind = m.Kind
		}
	}
	// Every ready provider, in the effective order. The FIRST is what auto would route to, and
	// is repeated in the flat fields; the rest are what an explicit `provider` can name.
	byID := map[string]Provider{}
	for _, p := range Providers() {
		byID[p.ID()] = p
	}
	for _, id := range out.Order {
		p, ok := byID[id]
		if !ok || !p.Ready(r.Context()) {
			continue
		}
		caps := p.Caps("")
		st := providerStatus{ID: p.ID(), Model: driverModelOf(p.ID()), AspectRatios: caps.AspectRatios}
		for _, op := range caps.Ops {
			st.Ops = append(st.Ops, string(op))
		}
		out.Providers = append(out.Providers, st)
		if !out.Ready {
			out.Provider, out.Ready = st.ID, true
			out.Model, out.Ops, out.AspectRatios = st.Model, st.Ops, st.AspectRatios
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// driverModelOf reports the model a generation would run on, per provider. "" for a provider
// that is not driven by a model of ours to name.
func driverModelOf(id string) string {
	switch id {
	case ProviderCodex:
		return codexDriverModel()
	case ProviderAgy:
		return agyDriverModel()
	case ProviderSdcpp:
		// Not a driver model but the CHECKPOINT the engine was started with — the only model
		// this route has, and the one its Caps are keyed to. Answered from the stack's
		// declaration, so asking costs nothing and does not wake the box.
		return sdcppDriverModel()
	}
	return ""
}

type generateRequest struct {
	Session     string   `json:"session"`
	Provider    string   `json:"provider"`
	Op          string   `json:"op"`
	Prompt      string   `json:"prompt"`
	Size        string   `json:"size"`
	AspectRatio string   `json:"aspectRatio"`
	Background  string   `json:"background"`
	Count       int      `json:"count"`
	Inputs      []string `json:"inputs"`
	Mask        string   `json:"mask"`
	Model       string   `json:"model"`
}

// HandleGenerate answers POST /imagegen/generate. It blocks for the whole generation: P0 is
// synchronous by decision, because each poll of a job+poll shape would be a driver-model turn
// on the CALLER's plan, and no P0 provider is asynchronous by nature (ADR 0069, open
// question 1). The MCP server keeps its client's clock alive with notifications/progress
// while this runs.
func HandleGenerate(w http.ResponseWriter, r *http.Request) {
	if !enabled() {
		httpx.WriteErr(w, http.StatusForbidden, "imagegen_disabled",
			"image generation is off (Settings > Agents)")
		return
	}
	var body generateRequest
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_prompt", "prompt is required")
		return
	}
	op := Op(strings.TrimSpace(body.Op))
	if op == "" {
		op = OpGenerate
	}
	if !ValidOp(op) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_op", "unknown op: "+string(op))
		return
	}
	if !session.ValidName(body.Session) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "session is required")
		return
	}
	meta, ok := session.ReadMeta(body.Session)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "no_session", "session not found: "+body.Session)
		return
	}
	// A named provider may not be the caller's OWN CLI. The tool's enum already leaves it out,
	// but the advertised set is a scope boundary and a guessed name in tools/call must not cross
	// it: this session can make that picture with its own built-in tool, and going out through a
	// second process of the same CLI would spend the plan twice for it (ADR 0069 decision 8).
	if p := strings.TrimSpace(body.Provider); p != "" && p == meta.Kind {
		httpx.WriteErr(w, http.StatusBadRequest, "imagegen_own_cli",
			"this session is a "+meta.Kind+" session: use its own built-in image tool rather than spending the plan twice through the fleet one")
		return
	}

	job := Job{
		Session: body.Session,
		SID:     session.UUID(meta.Dir, body.Session),
		Pref:    body.Provider,
		Request: Request{
			Op: op, Prompt: body.Prompt, Size: body.Size, AspectRatio: body.AspectRatio,
			Background: body.Background, Count: body.Count, Inputs: body.Inputs,
			Mask: body.Mask, Model: body.Model,
		},
	}
	out, err := Run(r.Context(), job)
	if err != nil {
		writeGenerateErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// writeGenerateErr keeps the reason on the wire. A generic 500 here would reach the model as
// "generation failed" with nothing to act on, and "you are not logged in to Codex" and "the
// prompt was refused" call for opposite responses.
func writeGenerateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoProvider):
		httpx.WriteErr(w, http.StatusServiceUnavailable, "imagegen_no_provider", err.Error())
	case errors.Is(err, ErrUnknownProvider):
		httpx.WriteErr(w, http.StatusBadRequest, "imagegen_unknown_provider", err.Error())
	default:
		httpx.WriteErr(w, http.StatusBadGateway, "imagegen_failed", err.Error())
	}
}
