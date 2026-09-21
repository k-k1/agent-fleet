//go:build manuallive

package harness

// Opt-in live check against a real llama.cpp engine — ADR 0093 decision 10's own
// replacement for a TUI string contract test (docs/decisions/0093-lcpp-agent-kind.ja.md
// decision 10, 段 2 に持ち越す負債 7): this kind has no CLI, so its one remaining drift
// axis is llama-server's OWN API and tool-call parsing, which changes with the engine
// image's llama.cpp version, not with anything in this repo. Same opt-in shape as
// live_manual_test.go (AF_LCPP_LIVE_BASE / AF_LCPP_LIVE_TOKEN), never part of
// `go test ./...`, never touches a real GPU box unless explicitly invoked:
//
//	go test ./internal/harness/ -tags manuallive -run TestManualLiveEngineContract -v -timeout 15m
//
// What this pins (each measured live against the dev deployment's "llm" engine,
// b10830-465e49b9c, 2026-09-21 — see this file's own t.Logf output for the version a
// later run actually saw):
//
//  1. GET /props answers without waking the box, and carries build_info.
//  2. On a BORROWED router row (ADR 0079), /props describes the ROUTER, not any one
//     model: role=="router", model_path=="none", default_generation_settings.n_ctx==0.
//     The real window lives in GET {base}/v1/models, data[].meta.n_ctx, for whichever
//     model is actually loaded. This asymmetry is exactly what
//     control-plane/engine_gateway.go's enginePropsAugmentRouterWindow (the段0 bypass)
//     exists to read around; if it ever stops holding, that bypass needs revisiting.
//  3. POST /chat/completions/input_tokens answers with a field named exactly
//     "input_tokens" (not "tokens"/"n_tokens" — client.go's InputTokens keeps those as an
//     unconfirmed fallback, but decision 7's window judgement is built on this exact
//     name), and its value matches the prompt_tokens a same-shape chat completion
//     actually spent.
//  4. POST /chat/completions/control exists, and requires both "model" and "id" (400
//     without either) — the two fields ThreadHandle.Interrupt depends on
//     (docs/log/99-lcpp-agent-kind.md §4.5). A well-formed call for an id with no
//     in-flight completion answers 200 with {"success":false}, not 404 — this route is
//     real, not a stub.
//  5. tool_calls the model emits are valid JSON, name a known tool, and carry every
//     required argument (the same three axes docs/log/99 §14's SUMMARY line counts:
//     invalid_tool_call_json / unknown_tool_calls / bad_arg_shape).
//  6. A streaming chat completion's final chunk carries `usage` — the gateway's own
//     stream_options.include_usage injection (control-plane/engine_gateway.go's
//     askForStreamUsage), which decision 8's exact usage accounting depends on.
//
// NOT covered (confirmed live but NOT reducible to an assertion worth pinning, or not
// confirmable at all without a GPU box this file's own budget could not justify):
//   - chat_template itself: absent from both /props and /v1/models (docs/log/99 §12.5),
//     so there is nothing here to read back and assert against.
//   - Which family's tool-call grammar is chosen (PEG native vs. generic fallback) is an
//     llama.cpp-internal routing decision with no field in the response that names it;
//     only the RESULT (valid JSON, right tool, right args) is observable, which is what
//     axis 5 above checks.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// pollEngine retries fn only while its answer looks like a box waking up — the same
// judgement retryOnWake (live_manual_test.go) applies to a Client error, but shaped for
// a raw (status, body) pair instead: several of the assertions below (an intentionally
// malformed /control body, say) expect a specific non-200 as the PASSING result, so a
// helper that Fatals on any non-200 would break the very cases this file means to check.
// Only a wake-shaped answer (an EngineWaking-classified error body, or a bare 502) is
// retried; anything else is handed straight back to the caller to assert on.
func pollEngine(t *testing.T, ctx context.Context, label string, fn func() (int, []byte, error)) (int, []byte) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Minute)
	for {
		status, body, err := fn()
		retryable := false
		var waitMsg string
		switch {
		case err != nil:
			retryable = true
			waitMsg = err.Error()
		case status == http.StatusBadGateway:
			retryable = true
			waitMsg = fmt.Sprintf("HTTP %d (transient gateway hiccup): %s", status, trimForError(body))
		case status >= 400:
			var ee *EngineError
			if errors.As(readHTTPError(status, body), &ee) && ee.Retryable() {
				retryable = true
				waitMsg = fmt.Sprintf("HTTP %d: %s", status, trimForError(body))
			}
		}
		if !retryable {
			return status, body
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: engine still waking after 10m of retries: %s", label, waitMsg)
		}
		t.Logf("%s: engine waking, retrying in 15s: %s", label, waitMsg)
		select {
		case <-time.After(15 * time.Second):
		case <-ctx.Done():
			t.Fatalf("%s: context done while waiting for the engine to wake: %v", label, ctx.Err())
		}
	}
}

func rawGet(ctx context.Context, url, token string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, engineErrMaxBody))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

func rawPost(ctx context.Context, url, token string, payload any) (int, []byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, engineErrMaxBody))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// propsResponse is the subset of GET /props this file asserts on. Every other field
// (total_slots, router_models, ...) is deliberately left unread — props()'s own doc
// comment (control-plane/engine_gateway.go) promises to relay the body, not parse and
// rebuild it, and this test only needs to pin the fields decision 7/10 actually depend on.
type propsResponse struct {
	BuildInfo                 string `json:"build_info"`
	Role                      string `json:"role"`
	ModelPath                 string `json:"model_path"`
	DefaultGenerationSettings struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

// modelsResponse is GET {base}/v1/models' relevant shape — see enginePropsAugmentRouterWindow's
// own doc comment (control-plane/engine_gateway.go) for why data[].meta.n_ctx, not
// default_generation_settings.n_ctx, is where a ROUTER row's real window lives.
type modelsResponse struct {
	Data []struct {
		ID   string `json:"id"`
		Meta struct {
			NCtx int `json:"n_ctx"`
		} `json:"meta"`
	} `json:"data"`
}

// controlRequest is POST /v1/chat/completions/control's body shape — confirmed live
// (2026-09-21): {} answers 400 "model name is missing from the request", {"model":...}
// alone answers 400 "missing completion id", and {"model":...,"id":...,"action":
// "reasoning_end"} for an id with no in-flight completion answers 200
// {"success":false,"message":"no active completion for this id"}. Exact wording is not
// asserted below (only status codes and the presence of "success"/"model"/"id" as the
// three gating fields) — wording is llama-server's own and not part of the contract
// decision 4's Interrupt depends on.
type controlRequest struct {
	Model  string `json:"model,omitempty"`
	ID     string `json:"id,omitempty"`
	Action string `json:"action,omitempty"`
}

func TestManualLiveEngineContract(t *testing.T) {
	base := os.Getenv("AF_LCPP_LIVE_BASE")
	token := os.Getenv("AF_LCPP_LIVE_TOKEN")
	if base == "" || token == "" {
		t.Skip("AF_LCPP_LIVE_BASE / AF_LCPP_LIVE_TOKEN not set")
	}
	model := os.Getenv("AF_LCPP_LIVE_MODEL")
	if model == "" {
		model = "qwen3.8-27b-uncensored-q4_k_m"
	}
	base = strings.TrimRight(base, "/")
	// GET /props is mounted as a sibling of /v1, not under it (control-plane/
	// engine_gateway.go:215-216) — base ends at .../engine/{key}/v1, so the props URL
	// is base with that trailing /v1 stripped.
	propsURL := strings.TrimSuffix(base, "/v1") + "/props"
	modelsURL := base + "/models"
	inputTokensURL := base + "/chat/completions/input_tokens"
	controlURL := base + "/chat/completions/control"

	client := NewClient(EngineConn{BaseURL: base, Token: token}, model)
	reg := NewRegistry(BuiltinTools()...)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// Warm-up: one real completion, both to load `model` onto the router (so the
	// /props-vs-/v1/models check below has something real to find) and to check
	// streaming usage in the same call. Placed first deliberately, matching
	// retryOnWake's own reasoning in live_manual_test.go: an idle box's cold start (3.5
	// to 7 minutes) belongs to the FIRST call, not to every check after it.
	var warmupUsagePromptTokens int
	t.Run("streaming_usage_present", func(t *testing.T) {
		msgs := []Message{
			{Role: RoleSystem, Content: "You are a terse test assistant used for an automated harness contract check. Reply in a single short sentence."},
			{Role: RoleUser, Content: "Say hello in one short sentence."},
		}
		var turn Turn
		retryOnWake(t, ctx, "warm-up Send", func() error {
			var err error
			turn, err = client.Send(ctx, msgs, nil)
			return err
		})
		t.Logf("model=%s warm-up reply=%q usage=%+v", model, turn.Content, turn.Usage)
		if turn.Content == "" {
			t.Fatal("warm-up completion returned empty content — the engine does not look usable at all, every check below is suspect")
		}
		if turn.Usage.PromptTokens == 0 {
			t.Fatalf("streamed final chunk carried no usage (PromptTokens=0) — the gateway's stream_options.include_usage injection (control-plane/engine_gateway.go askForStreamUsage) may have stopped firing, or llama-server stopped honoring it; decision 8's exact usage accounting depends on this")
		}
		warmupUsagePromptTokens = turn.Usage.PromptTokens
	})

	t.Run("tool_calls_sane_json", func(t *testing.T) {
		sys := "You are a terse test assistant used for an automated harness contract check. " +
			"Respond to every user turn by calling exactly one tool — never a bare text reply."
		msgs := []Message{
			{Role: RoleSystem, Content: sys},
			{Role: RoleUser, Content: "Call the `read` tool to read the file at path \"go.mod\". Call it now."},
		}
		tools := reg.Defs(false)

		checkTurn := func(label string, turn Turn) {
			if len(turn.ToolCalls) == 0 {
				t.Fatalf("%s: model answered with no tool_calls at all (content=%q) — expected exactly one, told to always call a tool", label, turn.Content)
			}
			for _, tc := range turn.ToolCalls {
				if !json.Valid([]byte(tc.Arguments)) {
					t.Errorf("%s: tool_call %q has INVALID JSON arguments: %s — llama-server's tool-call parser for this model/template broke", label, tc.Name, tc.Arguments)
					continue
				}
				tool, ok := reg.lookup(tc.Name)
				if !ok {
					t.Errorf("%s: tool_call names unknown tool %q (args=%s) — expected one of the registered builtins", label, tc.Name, tc.Arguments)
					continue
				}
				if missing := missingRequiredArgs(tool, tc.Arguments); len(missing) > 0 {
					t.Errorf("%s: tool_call %q is missing required argument(s) %v (args=%s)", label, tc.Name, missing, tc.Arguments)
				}
			}
			t.Logf("%s: content=%q tool_calls=%d names=%v", label, truncateForLog(turn.Content), len(turn.ToolCalls), toolCallNames(turn.ToolCalls))
		}

		var turn1 Turn
		retryOnWake(t, ctx, "tool-call turn 1", func() error {
			var err error
			turn1, err = client.Send(ctx, msgs, tools)
			return err
		})
		checkTurn("turn 1 (read)", turn1) // Fatals inside if turn1.ToolCalls is empty

		full := append(append([]Message{}, msgs...),
			Message{Role: RoleAssistant, Content: turn1.Content, Reasoning: turn1.Reasoning, ToolCalls: turn1.ToolCalls},
			Message{Role: RoleTool, ToolCallID: turn1.ToolCalls[0].ID, Content: "module workspace/agent\n\ngo 1.23"},
			Message{Role: RoleUser, Content: "Now call the `ls` tool on path \".\". Call it now."},
		)
		var turn2 Turn
		retryOnWake(t, ctx, "tool-call turn 2", func() error {
			var err error
			turn2, err = client.Send(ctx, full, tools)
			return err
		})
		checkTurn("turn 2 (ls)", turn2)
	})

	t.Run("input_tokens_field_name", func(t *testing.T) {
		msgs := []Message{
			{Role: RoleSystem, Content: "You are a terse test assistant used for an automated harness contract check. Reply in a single short sentence."},
			{Role: RoleUser, Content: "Say hello in one short sentence."},
		}
		status, body := pollEngine(t, ctx, "input_tokens", func() (int, []byte, error) {
			return rawPost(ctx, inputTokensURL, token, inputTokensRequest{Model: model, Messages: toWireMessages(msgs)})
		})
		if status != http.StatusOK {
			t.Fatalf("POST /chat/completions/input_tokens answered %d, want 200: %s", status, trimForError(body))
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("input_tokens answered a body this test could not parse as JSON: %s", trimForError(body))
		}
		raw, ok := doc["input_tokens"]
		if !ok {
			t.Fatalf(`input_tokens response has no "input_tokens" field (got keys=%v, body=%s) — decision 7's window judgement (client.go's InputTokens) reads exactly this field name; the fallback field names ("tokens"/"n_tokens") it also accepts are unconfirmed`, keysOf(doc), trimForError(body))
		}
		var n int
		if err := json.Unmarshal(raw, &n); err != nil || n <= 0 {
			t.Fatalf(`"input_tokens" field is not a positive integer: %s`, raw)
		}
		t.Logf("input_tokens=%d (same messages, same-shape warm-up completion spent prompt_tokens=%d)", n, warmupUsagePromptTokens)
		// warmupUsagePromptTokens came from a DIFFERENT message list (no assistant/tool
		// history yet at that point), so an exact match is not expected here — this
		// call's own request/response pair is what is actually comparable, and that
		// comparison already happened implicitly: this same messages slice, sent as a
		// real completion below, is asserted to match exactly.
		var turn Turn
		retryOnWake(t, ctx, "input_tokens cross-check Send", func() error {
			var err error
			turn, err = client.Send(ctx, msgs, nil)
			return err
		})
		if turn.Usage.PromptTokens != n {
			t.Fatalf("input_tokens=%d does not match the same messages' actual prompt_tokens=%d from a real completion — the pre-send count and the engine's own accounting have drifted apart", n, turn.Usage.PromptTokens)
		}
	})

	t.Run("control_requires_model_and_id", func(t *testing.T) {
		status, body := pollEngine(t, ctx, "control (empty body)", func() (int, []byte, error) {
			return rawPost(ctx, controlURL, token, controlRequest{})
		})
		if status == http.StatusOK {
			t.Fatalf("POST /chat/completions/control with an empty body answered 200 (body=%s) — expected an error demanding \"model\", control now accepts a bare call it used not to", trimForError(body))
		}
		t.Logf("control (empty body): HTTP %d %s", status, trimForError(body))

		status, body = pollEngine(t, ctx, "control (model only)", func() (int, []byte, error) {
			return rawPost(ctx, controlURL, token, controlRequest{Model: model})
		})
		if status == http.StatusOK {
			t.Fatalf("POST /chat/completions/control with only \"model\" set answered 200 (body=%s) — expected an error demanding \"id\" too; ThreadHandle.Interrupt depends on both being required", trimForError(body))
		}
		t.Logf("control (model only): HTTP %d %s", status, trimForError(body))

		status, body = pollEngine(t, ctx, "control (model+id+action, no in-flight completion)", func() (int, []byte, error) {
			return rawPost(ctx, controlURL, token, controlRequest{Model: model, ID: "contract-test-no-such-id", Action: "reasoning_end"})
		})
		if status != http.StatusOK {
			t.Fatalf("POST /chat/completions/control with model+id+action=reasoning_end answered %d, want 200 (a well-formed call for an unknown id should be answered, not refused): %s", status, trimForError(body))
		}
		var doc struct {
			Success *bool `json:"success"`
		}
		if err := json.Unmarshal(body, &doc); err != nil || doc.Success == nil {
			t.Fatalf(`control's 200 response has no "success" field (body=%s) — the shape ThreadHandle.Interrupt would parse has changed`, trimForError(body))
		}
		if *doc.Success {
			t.Fatalf(`control reported success=true for a completion id ("contract-test-no-such-id") that was never in flight — either it stopped validating the id, or this test's "no such id" assumption no longer holds`)
		}
		t.Logf("control (model+id+action, unknown id): HTTP %d %s", status, trimForError(body))
	})

	t.Run("props_build_info_and_router_window_asymmetry", func(t *testing.T) {
		// Never waits for a wake (props() never records demand — control-plane/
		// engine_gateway.go's own doc comment) so this is a single attempt, not
		// pollEngine: a failure here means the deployment itself is unreachable, not
		// that a GPU box is asleep.
		propsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		status, body, err := rawGet(propsCtx, propsURL, token)
		if err != nil {
			t.Fatalf("GET /props failed: %v", err)
		}
		if status != http.StatusOK {
			t.Fatalf("GET /props answered %d, want 200: %s", status, trimForError(body))
		}
		var props propsResponse
		if err := json.Unmarshal(body, &props); err != nil {
			t.Fatalf("GET /props answered a body this test could not parse: %s", trimForError(body))
		}
		if props.BuildInfo == "" {
			t.Fatal("GET /props answered with an empty build_info — this is the one field 段2 負債7's pin depends on (the engine image version, readable without waking the box)")
		}
		t.Logf("engine build_info=%s (段2 負債7: pin the engine image to this digest once it stops moving)", props.BuildInfo)

		// Which of the two shapes this endpoint is decides what /props may be asked for, and
		// the test has to read that off the answer rather than assume it: the same harness
		// talks to a borrowed ROUTER through the CP gateway (the dev deployment, ADR 0079) and
		// to a SINGLE-MODEL llama-server on the operator's own network (guide/operate/
		// 09-llm-lan.md). `role` is absent entirely on the latter — measured 2026-09-21 against
		// b11067-932a68e06: role="" model_path=<the real .gguf path> n_ctx=24064, and the whole
		// router asymmetry simply does not arise because /props IS describing the one model.
		router := props.Role == "router"
		switch {
		case router:
			if props.ModelPath != "none" || props.DefaultGenerationSettings.NCtx != 0 {
				t.Fatalf("GET /props calls itself a router but no longer shows the router shape (model_path=%q default_generation_settings.n_ctx=%d; want \"none\" and 0) — "+
					"control-plane/engine_gateway.go's enginePropsAugmentRouterWindow (the 段0 bypass) reads around exactly this asymmetry and may need to change with it",
					props.ModelPath, props.DefaultGenerationSettings.NCtx)
			}
			t.Logf("router row: /props describes the router itself (model_path=%q n_ctx=%d), so the real window lives in GET /v1/models", props.ModelPath, props.DefaultGenerationSettings.NCtx)
		default:
			if props.ModelPath == "" || props.ModelPath == "none" {
				t.Fatalf("GET /props is not a router (role=%q) and names no model either (model_path=%q) — neither shape holds, so nothing here can be trusted about the window", props.Role, props.ModelPath)
			}
			if props.DefaultGenerationSettings.NCtx <= 0 {
				t.Fatalf("GET /props is a single-model server (model_path=%q) but default_generation_settings.n_ctx=%d — on this shape THIS is where the real window is, and the 段0 bypass has nothing to read around",
					props.ModelPath, props.DefaultGenerationSettings.NCtx)
			}
			t.Logf("single-model row: /props carries the real window itself (n_ctx=%d, model_path=%q) and chat_template is readable here too (absent on a router — docs/log/99 §12.5's 負債 6 is router-only)",
				props.DefaultGenerationSettings.NCtx, props.ModelPath)
		}

		modelsStatus, modelsBody := pollEngine(t, ctx, "GET /v1/models", func() (int, []byte, error) {
			return rawGet(ctx, modelsURL, token)
		})
		if modelsStatus != http.StatusOK {
			t.Fatalf("GET %s answered %d, want 200: %s", modelsURL, modelsStatus, trimForError(modelsBody))
		}
		var models modelsResponse
		if err := json.Unmarshal(modelsBody, &models); err != nil {
			t.Fatalf("GET /v1/models answered a body this test could not parse: %s", trimForError(modelsBody))
		}
		found := false
		for _, m := range models.Data {
			if m.ID == model {
				found = true
				if m.Meta.NCtx <= 0 {
					t.Fatalf("GET /v1/models lists %q with meta.n_ctx=%d — want a positive real window; on a router row this is the ONLY place it is readable (default_generation_settings.n_ctx is 0 there by design)", model, m.Meta.NCtx)
				}
				// Carried on BOTH shapes, measured: the upstream README's single-model example
				// shows only n_ctx_train, but b11067 answers meta.n_ctx=24064 for a plain `-c
				// 24000` server. So this assertion is not router-only, and a single-model row
				// has two places that agree rather than one place that answers.
				t.Logf("GET /v1/models: %q meta.n_ctx=%d (router=%v)", model, m.Meta.NCtx, router)
			}
		}
		if !found {
			ids := make([]string, len(models.Data))
			for i, m := range models.Data {
				ids[i] = m.ID
			}
			t.Fatalf("GET /v1/models does not list %q at all (lists %v) — the warm-up completion above should have loaded it onto the router", model, ids)
		}
	})
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
