package mcpx

// The generate_image tool's own machinery (ADR 0069): the availability question asked at
// tools/list time, the call itself, and the progress heartbeat that keeps a client's per-call
// clock from cutting a generation in half.
//
// The tool DEFINITION stays in mcp_stdio.go, because the advertised-schema test finds the
// source of truth by parsing that file's literals — a tool declared anywhere else would be
// invisible to the gate that checks every advertised schema.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// mcpImageGenStatus is the Agent's answer to "can this session generate images, and how".
// It reports FACTS; the rule that turns them into a yes or no lives in mcpImageGenAdvertise,
// where the tool list is built.
type mcpImageGenStatus struct {
	Enabled      bool                  `json:"enabled"`
	Provider     string                `json:"provider"`
	Service      string                `json:"service"`
	Ready        bool                  `json:"ready"`
	Kind         string                `json:"kind"`
	Model        string                `json:"model"`
	Ops          []string              `json:"ops"`
	AspectRatios []string              `json:"aspectRatios"`
	Providers    []mcpImageGenProvider `json:"providers"`
}

// mcpImageGenProvider is one ready provider: what it is called, and what it can do. The
// per-provider list is what makes an explicit `provider` argument honest — the tool's enums are
// built from the providers this session may actually name, not from the first one.
type mcpImageGenProvider struct {
	ID string `json:"id"`
	// Service is the image service the id stands for ("GPT Image", "Gemini …"). It is what the
	// tool description says out loud: the enum values are CLI names, and a session asked for a
	// picture "from Gemini" cannot map that onto `agy` on its own.
	Service      string   `json:"service"`
	Model        string   `json:"model"`
	Ops          []string `json:"ops"`
	AspectRatios []string `json:"aspectRatios"`
	// Models is every checkpoint this provider may be asked for by name (ADR 0072 decision 5,
	// phase P2) — absent for a provider with nothing to choose between, the same rule
	// `provider` itself follows for a session with only one route.
	Models []mcpImageGenModel `json:"models,omitempty"`
	// Loras is every fine-tune this provider will accept (ADR 0072 decision 5, phase P3). One
	// entry is already a choice — with it or without it — so unlike Models there is no
	// "more than one" rule.
	Loras []mcpImageGenLora `json:"loras,omitempty"`
	// Seed is whether this route lets the caller pin the sampler's seed (ADR 0069 follow-up).
	Seed bool `json:"seed,omitempty"`
	// Negative is whether any model on this route samples with a negative branch (ADR 0072
	// follow-up, negative prompts).
	Negative bool `json:"negative,omitempty"`
}

// mcpImageGenModel is one checkpoint `model` may name.
type mcpImageGenModel struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	// Warm is the checkpoint a request naming none would get (ADR 0072 decision 7) — surfaced
	// so an agent can say "the warm one is fine" instead of naming a cold one and paying a
	// 1-2.5 minute switch it did not need to ask for.
	Warm bool `json:"warm,omitempty"`
}

// mcpImageGenLora is one fine-tune `loras` may name. baseModel is on the wire because the enum
// cannot be narrowed to the chosen checkpoint (it is built at tools/list, before `model` exists),
// so the description has to say which family each one belongs to and the Agent refuses the
// pairings that do not fit.
type mcpImageGenLora struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	BaseModel   string `json:"baseModel,omitempty"`
}

// agentImageGenStatus asks the Agent over the loopback REST every other session tool already
// uses. The short timeout is deliberate: this sits on the tools/list path, which a client
// calls at the start of every turn, so a wedged Agent must cost the turn a moment rather than
// the whole 15 s default.
func agentImageGenStatus(session string) (mcpImageGenStatus, error) {
	var st mcpImageGenStatus
	out, err := agentDoTimeout(http.MethodGet, "/imagegen/status?session="+url.QueryEscape(session), nil, 3*time.Second)
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return st, err
	}
	return st, nil
}

// imageGenArgs is the tool's arguments, already split out of the shared argument struct.
type imageGenArgs struct {
	op, provider, prompt, size, aspectRatio, background string
	count                                               int
	inputs                                              []string
	// mask is an absolute path, and only inpaint uses it. It arrives here rather than being
	// folded into inputs because a mask is not a reference image: handing one to a route that
	// has no mask parameter produces a picture OF the mask, which is why the Codex provider
	// refuses it outright (ADR 0069) and the self-hosted one requires it (ADR 0071 P1).
	mask string
	// model names a checkpoint (ADR 0072 decision 5, phase P2) — "" leaves it to the provider's
	// own default, which for the fleet's own engines is the warm one when known (decision 7).
	model string
	// loras are the fine-tunes to apply on top of it (phase P3), forwarded as they arrived: an
	// unknown name and a family that does not match the checkpoint are the Agent's refusals to
	// make, by name, rather than something to drop here.
	loras []imageGenLoraArg
	// seed pins the sampler's starting noise. A POINTER, because 0 is a usable seed and an
	// absent argument is not the same request as `"seed": 0`.
	seed *int64
	// negativePrompt is what to keep out of the picture. Forwarded as typed: what it is added to
	// (the catalogue row's own, the deployment's list) is the Agent's business, not this layer's.
	negativePrompt string
}

// imageGenLoraArg is one entry of the tool's `loras` argument.
type imageGenLoraArg struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight,omitempty"`
}

// mcpImageGenCallTimeout is this layer's budget for one generation. It must EXCEED every
// provider's own so that a slow generation is reported by the provider with a real reason,
// rather than by this client as a bare timeout. The chain, longest last:
//
//	engine gateway 15 min  <  sdcpp provider 16 min  <  this 18 min
//	codex provider  8 min  <  this
//
// 18 rather than the 10 it started at, because the self-hosted route can legitimately spend a
// quarter of an hour: a request to a stopped image engine is held by the Control Plane while
// a GPU box is bought, booted, and loaded with a checkpoint (ADR 0071 decision 5). Waiting is
// what the progress heartbeat below exists to make survivable.
//
// It is also why the budget is bounded rather than left open: RunStdio's loop dispatches
// serially, so for as long as a generation is in flight this server reads nothing else from
// stdin — including notifications/cancelled. That is tolerable because the one client on this
// pipe is the agent waiting on this very call, but an unbounded wait would turn a wedged
// provider into a wedged Agent Fleet server.
//
// ⚠️ A codex session is capped below this by its own tool_timeout_sec (600 s, stamped by the
// materializer), so on that kind a cold engine start can still be cut short by the client.
const mcpImageGenCallTimeout = 18 * time.Minute

func mcpGenerateImage(req mcpReq, a imageGenArgs) []byte {
	if strings.TrimSpace(a.prompt) == "" {
		return mcpToolErr(req.ID, "prompt（生成する絵の説明）が必要です")
	}
	self, err := mcpOwningSession()
	if err != nil {
		return mcpToolErr(req.ID, err.Error())
	}
	body, _ := json.Marshal(map[string]any{
		"session": self, "op": a.op, "provider": a.provider, "prompt": a.prompt,
		"size": a.size, "aspectRatio": a.aspectRatio, "background": a.background,
		"count": a.count, "inputs": a.inputs, "mask": a.mask, "model": a.model,
		"loras": a.loras, "seed": a.seed, "negativePrompt": a.negativePrompt,
	})

	// The heartbeat runs for as long as the Agent is working. Without it opencode cuts the
	// call at 60 s flat (measured: "MCP error -32001: Request timed out" at 60.0 s), while a
	// notification every 10 s took a 90 s call through cleanly; claude ignores progress for
	// timeout purposes and needs nothing; codex is covered by the tool_timeout_sec the
	// materializer stamps.
	stop := startProgressHeartbeat(req, "画像を生成しています…")
	out, err := agentDoTimeout(http.MethodPost, "/imagegen/generate", body, mcpImageGenCallTimeout)
	stop()
	if err != nil {
		return mcpToolErr(req.ID, "画像を生成できませんでした: "+agentErrDetail(err))
	}
	var res struct {
		Files []struct {
			Path   string `json:"path"`
			Name   string `json:"name"`
			MIME   string `json:"mime"`
			Bytes  int64  `json:"bytes"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"files"`
		Provider    string   `json:"provider"`
		Model       string   `json:"model"`
		Region      string   `json:"region"`
		Destination string   `json:"destination"`
		Warnings    []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Files) == 0 {
		return mcpToolErr(req.ID, "画像生成の結果を読み取れませんでした")
	}
	value := map[string]any{
		"files":    res.Files,
		"provider": res.Provider,
	}
	if res.Model != "" {
		value["model"] = res.Model
	}
	if res.Region != "" {
		value["region"] = res.Region
	}
	// Where the prompt went, when the provider id does not say it on its own (ADR 0069
	// decision 11): `sdcpp` is the fleet's own GPU box, not a vendor, and the tool's own
	// description says "an external image service" because that is true of the other routes.
	if res.Destination != "" {
		value["destination"] = res.Destination
	}
	// Always present, empty included: a caller that only looks for the key when something went
	// wrong is the caller that reports a size it never got.
	value["warnings"] = append([]string{}, res.Warnings...)
	value["note"] = "パスは生成済みのファイル。絵を確認する必要があるときだけ開くこと。warnings は実際に何が起きたかで、無視して寸法が合っている前提の説明をしないこと。"
	return mcpStructuredResult(req.ID, value)
}

// agentErrDetail keeps the Agent's own message (why the provider refused, whether Codex is
// logged in) in the model-visible error. A generic "generation failed" would leave the model
// nothing to act on, and the two ends of the range — "you are not logged in" and "the prompt
// was refused" — call for opposite responses.
func agentErrDetail(err error) string {
	var httpErr *agentHTTPError
	if !errors.As(err, &httpErr) {
		return err.Error()
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(httpErr.Body), &body) == nil && body.Error.Message != "" {
		return body.Error.Message
	}
	return httpErr.Error()
}

// --- progress heartbeat -------------------------------------------------------------------

// mcpProgressEvery is the heartbeat interval. 10 s against opencode's 60 s ceiling leaves
// five missed notifications of headroom before the client would give up. A var only so a test
// can shorten it — a test that waited for the real interval would be a ten-second test.
var mcpProgressEvery = 10 * time.Second

// startProgressHeartbeat emits notifications/progress for this request until the returned
// stop is called. It is a no-op when the client sent no progressToken (the token is what a
// notification is addressed to, and an unaddressed one is dropped) or when there is no stdout
// to write to, which is the case in the unit tests.
func startProgressHeartbeat(req mcpReq, message string) (stop func()) {
	token := mcpProgressToken(req)
	if len(token) == 0 || stdioOut == nil {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(mcpProgressEvery)
		defer ticker.Stop()
		var n int
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				n++
				// progress must increase; there is no total, because the provider cannot say
				// how far along an image is.
				notif, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"method":  "notifications/progress",
					"params": map[string]any{
						"progressToken": token,
						"progress":      n,
						"message":       fmt.Sprintf("%s (%ds)", message, n*int(mcpProgressEvery/time.Second)),
					},
				})
				stdioOut.writeLine(notif)
			}
		}
	}()
	return func() {
		close(done)
		// Wait for the goroutine to leave before the caller writes the response, so a
		// heartbeat can never be emitted after the result it belongs to.
		<-finished
	}
}

// mcpProgressToken reads params._meta.progressToken. It is returned as raw JSON because the
// spec allows a string OR a number and the notification has to echo back exactly what arrived
// — re-typing it here is how a client stops recognising its own token.
func mcpProgressToken(req mcpReq) json.RawMessage {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(req.Params) == 0 || json.Unmarshal(req.Params, &p) != nil {
		return nil
	}
	tok := p.Meta["progressToken"]
	if len(tok) == 0 || string(tok) == "null" {
		return nil
	}
	return tok
}
