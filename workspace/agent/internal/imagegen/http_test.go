package imagegen

// The REST face. Both routes are driven end to end by imagegen_live_test.go against real CLIs;
// what is checked here is the part that decides WHICH provider a call may reach, because that
// is the part that spends somebody's plan.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// withImagegenSession installs a provider set (reusing imagegen_test.go's stub) plus a session
// for the request to belong to, and turns the feature on.
func withImagegenSession(t *testing.T, kind string, provs ...Provider) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.WriteMeta(session.Meta{Name: "slot01", Kind: kind, Dir: home})
	withStubProvider(t, provs...)
	oldEnabled := Enabled
	Enabled = func() bool { return true }
	t.Cleanup(func() { Enabled = oldEnabled })
}

func capsOf(ops []Op, ratios ...string) *Caps {
	return &Caps{Ops: ops, AspectRatios: ratios}
}

// The status endpoint reports EVERY ready provider with its own capabilities — the flat fields
// describe the first one only, which stopped being enough once the tool grew a provider
// argument: a tool that offers a choice has to say what each choice can do.
func TestStatusListsEveryReadyProvider(t *testing.T) {
	withImagegenSession(t, session.KindClaude,
		stubProvider{id: "agy", caps: capsOf([]Op{OpGenerate, OpEdit}, "1:1", "16:9")},
		stubProvider{id: "codex", caps: capsOf([]Op{OpGenerate})},
		stubProvider{id: "bedrock", notReady: true, caps: capsOf([]Op{OpInpaint})},
	)
	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/imagegen/status?session=slot01", nil))

	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("status is not JSON: %v (%s)", err, rec.Body)
	}
	if !got.Ready || got.Provider != "agy" {
		t.Fatalf("effective provider = %+v, want the first ready one", got)
	}
	if len(got.Providers) != 2 || got.Providers[0].ID != "agy" || got.Providers[1].ID != "codex" {
		t.Fatalf("providers = %+v, want both ready ones in order and no unready one", got.Providers)
	}
	// Per-provider capabilities, not the first one's repeated: this is what makes the tool's
	// enums honest when the caller names a provider.
	if len(got.Providers[0].AspectRatios) != 2 || len(got.Providers[1].AspectRatios) != 0 {
		t.Fatalf("aspect ratios = %+v, want them per provider", got.Providers)
	}
	if got.Kind != session.KindClaude {
		t.Fatalf("kind = %q", got.Kind)
	}
}

// The status names the image SERVICE behind each route, not only the CLI id it is keyed by.
// The id is what the tool's enum carries, and a session asked for a picture "from Gemini"
// cannot map that onto `agy` on its own — the one that could not reported the service as
// unavailable while holding the only route to it.
func TestStatusNamesTheServiceBehindEachProvider(t *testing.T) {
	withImagegenSession(t, session.KindClaude,
		stubProvider{id: ProviderAgy, caps: capsOf([]Op{OpGenerate})},
		stubProvider{id: ProviderCodex, caps: capsOf([]Op{OpGenerate})},
	)
	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/imagegen/status?session=slot01", nil))

	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("status is not JSON: %v (%s)", err, rec.Body)
	}
	want := map[string]string{ProviderAgy: "Gemini", ProviderCodex: "GPT Image"}
	for _, p := range got.Providers {
		if !strings.Contains(p.Service, want[p.ID]) {
			t.Fatalf("provider %s service = %q, want it to name %q", p.ID, p.Service, want[p.ID])
		}
	}
	// The flat field describes the effective provider, like every other flat field here.
	if !strings.Contains(got.Service, "Gemini") {
		t.Fatalf("effective service = %q, want the first ready provider's", got.Service)
	}
}

// withEngineLookup installs the seam the Agent normally fills from the engine table, pointed at
// a server that fails the test if anything actually dials it: the status route may answer from
// what the Control Plane already handed us and nothing more (a request here would wake a
// sleeping GPU box on a call that only reads a tool description).
func withEngineLookup(t *testing.T, conn EngineConn) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the status route dialled the engine: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	conn.BaseURL = srv.URL
	old := EngineLookup
	EngineLookup = func(context.Context, string) (EngineConn, bool) { return conn, true }
	t.Cleanup(func() { EngineLookup = old })
}

// The fleet's OWN engine routes have a service and a model on the status too. comfy had
// neither — no case in serviceLabelOf or driverModelOf — so the route reached the tool
// description as an id with no service to match a request against and no checkpoint to expect,
// the same gap that once had a session report a service it held the only route to as
// unavailable (ADR 0076 P0; the hole is the ECS deployment's as much as the LAN one's).
func TestStatusNamesTheFleetEngineServiceAndModel(t *testing.T) {
	for _, tc := range []struct {
		name string
		warm string
		want string
	}{
		// Whatever is already loaded, because a switch costs 1-2.5 minutes of disk re-read.
		{"the warm checkpoint", "sdxl-base-1.0", "sdxl-base-1.0"},
		// Nothing warm yet (a just-started engine, or a CP that lost its in-memory state).
		{"else the catalogue's first", "", "flux1-dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withEngineLookup(t, EngineConn{
				Token:  "t",
				Models: []string{"flux1-dev", "sdxl-base-1.0"},
				Warm:   tc.warm,
			})
			withImagegenSession(t, session.KindClaude,
				stubProvider{id: ProviderComfy, caps: capsOf([]Op{OpGenerate})})
			rec := httptest.NewRecorder()
			HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/imagegen/status?session=slot01", nil))

			var got statusResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("status is not JSON: %v (%s)", err, rec.Body)
			}
			if len(got.Providers) != 1 {
				t.Fatalf("providers = %+v, want the one ready route", got.Providers)
			}
			// The engine, not the fleet's GPU: since ADR 0076 the same route also reaches a
			// ComfyUI on the operator's LAN, so the label may not claim one box or the other.
			if !strings.Contains(got.Providers[0].Service, "ComfyUI") {
				t.Errorf("comfy service = %q, want it to name ComfyUI", got.Providers[0].Service)
			}
			if got.Providers[0].Model != tc.want {
				t.Errorf("comfy model = %q, want %q", got.Providers[0].Model, tc.want)
			}
			// The flat fields describe the effective provider, which here is the only one.
			if got.Service != got.Providers[0].Service || got.Model != got.Providers[0].Model {
				t.Errorf("flat fields = %q/%q, want the effective route's", got.Service, got.Model)
			}
		})
	}
	// openai-compat is the route comfy's case was modelled on; both fleet routes answer, and an
	// empty answer from either is what this test exists to catch.
	if s, m := serviceLabelOf(ProviderOpenAICompat), serviceLabelOf(ProviderComfy); s == "" || m == "" {
		t.Errorf("service labels = %q/%q, want both fleet routes named", s, m)
	}
}

// The LoRAs a provider accepts reach the tool surface with the family each was trained for
// (ADR 0072 decision 5, phase P3). Without baseModel on the wire the enum would be a list of
// names an agent cannot pair with a checkpoint, and every mismatch would cost a refused call.
func TestStatusListsTheProvidersLoras(t *testing.T) {
	withImagegenSession(t, session.KindClaude, stubProvider{id: ProviderComfy, caps: &Caps{
		Ops: []Op{OpGenerate},
		Loras: []LoraInfo{
			{Name: "watercolor-v2", Description: "soft watercolour", BaseModel: "sdxl"},
			{Name: "klein-lineart", BaseModel: "flux2-klein"},
		},
	}})
	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/imagegen/status?session=slot01", nil))

	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("status is not JSON: %v (%s)", err, rec.Body)
	}
	if len(got.Providers) != 1 || len(got.Providers[0].Loras) != 2 {
		t.Fatalf("loras = %+v, want both on the wire", got.Providers)
	}
	first := got.Providers[0].Loras[0]
	if first.Name != "watercolor-v2" || first.BaseModel != "sdxl" || first.Description != "soft watercolour" {
		t.Errorf("lora = %+v, want name, family and description all carried", first)
	}
	// A provider with none says nothing rather than an empty list — the same rule the other
	// optional capabilities follow.
	withImagegenSession(t, session.KindClaude, stubProvider{id: ProviderCodex})
	rec = httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/imagegen/status?session=slot01", nil))
	if strings.Contains(rec.Body.String(), "loras") {
		t.Errorf("body = %s, want no loras key for a route that has none", rec.Body)
	}
}

// The seed rides the same wire, and it has to survive as a POINTER: a route that folded an
// absent seed and `"seed": 0` together would make 0 the one seed nobody can pin.
func TestGenerateForwardsTheSeed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       *int64
	}{
		{"a pinned seed", `{"session":"slot01","prompt":"a cat","seed":1234}`, ptrInt64(1234)},
		{"seed zero is a seed", `{"session":"slot01","prompt":"a cat","seed":0}`, ptrInt64(0)},
		{"no seed stays nil", `{"session":"slot01","prompt":"a cat"}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got Request
			withImagegenSession(t, session.KindClaude, stubProvider{
				id:     ProviderComfy,
				gotReq: &got,
				res:    Result{Images: []Image{{Bytes: tinyPNG(t, 1, 1), MIME: "image/png"}}, Provider: ProviderComfy},
				caps:   &Caps{Ops: []Op{OpGenerate}, Seed: true},
			})
			rec := httptest.NewRecorder()
			HandleGenerate(rec, httptest.NewRequest(http.MethodPost, "/imagegen/generate", strings.NewReader(tc.body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body)
			}
			switch {
			case tc.want == nil && got.Seed != nil:
				t.Errorf("seed = %d, want none", *got.Seed)
			case tc.want != nil && got.Seed == nil:
				t.Errorf("seed = nil, want %d", *tc.want)
			case tc.want != nil && *got.Seed != *tc.want:
				t.Errorf("seed = %d, want %d", *got.Seed, *tc.want)
			}
		})
	}
}

func ptrInt64(v int64) *int64 { return &v }

// The status says which routes take a seed, so the tool offers the argument only where it
// reaches something.
func TestStatusReportsWhichRoutesTakeASeed(t *testing.T) {
	withImagegenSession(t, session.KindClaude,
		stubProvider{id: ProviderComfy, caps: &Caps{Ops: []Op{OpGenerate}, Seed: true}},
		stubProvider{id: ProviderAgy, caps: &Caps{Ops: []Op{OpGenerate}}},
	)
	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/imagegen/status?session=slot01", nil))
	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("status is not JSON: %v", err)
	}
	if len(got.Providers) != 2 || !got.Providers[0].Seed || got.Providers[1].Seed {
		t.Fatalf("seed flags = %+v, want it on comfy alone", got.Providers)
	}
}

// `strength` rides the same wire and is a POINTER for the same reason the seed is — though not
// because 0 is usable: it is refused, and it can only be refused by value if an absent key and
// `"strength": 0` are still distinguishable when they get here.
func TestGenerateForwardsTheStrength(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantStatus int
		want       *float64
	}{
		{"an edit strength", `{"session":"slot01","op":"edit","prompt":"a cat","strength":0.25}`, http.StatusOK, ptrFloat64(0.25)},
		{"a full redraw", `{"session":"slot01","op":"edit","prompt":"a cat","strength":1}`, http.StatusOK, ptrFloat64(1)},
		{"none stays nil", `{"session":"slot01","op":"edit","prompt":"a cat"}`, http.StatusOK, nil},
		// Refused rather than clamped: at denoise 0 the sampler hands the latent back untouched,
		// so this would spend a GPU box on a VAE round-trip of a picture the caller already has.
		{"zero is refused", `{"session":"slot01","op":"edit","prompt":"a cat","strength":0}`, http.StatusBadRequest, nil},
		{"above one is refused", `{"session":"slot01","op":"edit","prompt":"a cat","strength":1.5}`, http.StatusBadRequest, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got Request
			withImagegenSession(t, session.KindClaude, stubProvider{
				id:     ProviderComfy,
				gotReq: &got,
				res:    Result{Images: []Image{{Bytes: tinyPNG(t, 1, 1), MIME: "image/png"}}, Provider: ProviderComfy},
				caps:   &Caps{Ops: []Op{OpGenerate, OpEdit}, Strength: true},
			})
			rec := httptest.NewRecorder()
			HandleGenerate(rec, httptest.NewRequest(http.MethodPost, "/imagegen/generate", strings.NewReader(tc.body)))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body)
			}
			if tc.wantStatus != http.StatusOK {
				// The reason has to reach the caller by name, not as a bare 400.
				if !strings.Contains(rec.Body.String(), "bad_strength") {
					t.Fatalf("body = %s, want the reason on the wire", rec.Body)
				}
				return
			}
			switch {
			case tc.want == nil && got.Strength != nil:
				t.Errorf("strength = %v, want none", *got.Strength)
			case tc.want != nil && got.Strength == nil:
				t.Errorf("strength = nil, want %v", *tc.want)
			case tc.want != nil && *got.Strength != *tc.want:
				t.Errorf("strength = %v, want %v", *got.Strength, *tc.want)
			}
		})
	}
}

func ptrFloat64(v float64) *float64 { return &v }

// The status says which routes vary it, so the tool offers the argument only where it reaches
// something — the same rule the seed follows.
func TestStatusReportsWhichRoutesTakeAStrength(t *testing.T) {
	withImagegenSession(t, session.KindClaude,
		stubProvider{id: ProviderComfy, caps: &Caps{Ops: []Op{OpGenerate, OpEdit}, Strength: true}},
		stubProvider{id: ProviderAgy, caps: &Caps{Ops: []Op{OpGenerate, OpEdit}}},
	)
	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/imagegen/status?session=slot01", nil))
	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("status is not JSON: %v", err)
	}
	if len(got.Providers) != 2 || !got.Providers[0].Strength || got.Providers[1].Strength {
		t.Fatalf("strength flags = %+v, want it on comfy alone", got.Providers)
	}
}

// The sampler overlay rides the blocking route under the SAME key and shape the job queue's
// route uses, so that one request shape serves both surfaces. A field this layer drops is a
// field the tool advertises and no graph ever reads.
func TestGenerateForwardsTheSamplerOverlay(t *testing.T) {
	var got Request
	withImagegenSession(t, session.KindClaude, stubProvider{
		id:     ProviderComfy,
		gotReq: &got,
		res:    Result{Images: []Image{{Bytes: tinyPNG(t, 1, 1), MIME: "image/png"}}, Provider: ProviderComfy},
		caps:   &Caps{Ops: []Op{OpGenerate}, Params: true},
	})
	body := `{"session":"slot01","prompt":"a cat","params":{"steps":30,"cfg":6,"sampler":"dpmpp_2m","scheduler":"karras"}}`
	rec := httptest.NewRecorder()
	HandleGenerate(rec, httptest.NewRequest(http.MethodPost, "/imagegen/generate", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got.Params == nil {
		t.Fatal("request params = nil, want the four the caller typed")
	}
	if want := (EngineParams{Steps: 30, CFG: 6, Sampler: "dpmpp_2m", Scheduler: "karras"}); *got.Params != want {
		t.Errorf("request params = %+v, want %+v", *got.Params, want)
	}
}

// 🔴 The overlay is refused BY VALUE on this route too, not only on the queue's. Nothing below
// this layer enforces a ceiling — comfyRecipe.with is deliberately lenient, because it also
// merges an administrator's catalogue row — so without this a tools/call could buy an hour of a
// shared GPU with one number, and an unknown sampler name would reach the engine as a
// `Value not in list` failure after the cold start somebody waited through.
func TestGenerateRefusesATypedOverlayByValue(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"a sampler this Agent does not send", `{"session":"slot01","prompt":"a cat","params":{"sampler":"euler_a"}}`},
		{"a scheduler this Agent does not send", `{"session":"slot01","prompt":"a cat","params":{"scheduler":"kl_optimal"}}`},
		{"steps past the ceiling", `{"session":"slot01","prompt":"a cat","params":{"steps":10000}}`},
		{"cfg past the ceiling", `{"session":"slot01","prompt":"a cat","params":{"cfg":99}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got Request
			withImagegenSession(t, session.KindClaude, stubProvider{
				id:     ProviderComfy,
				gotReq: &got,
				res:    Result{Images: []Image{{Bytes: tinyPNG(t, 1, 1), MIME: "image/png"}}, Provider: ProviderComfy},
				caps:   &Caps{Ops: []Op{OpGenerate}, Params: true},
			})
			rec := httptest.NewRecorder()
			HandleGenerate(rec, httptest.NewRequest(http.MethodPost, "/imagegen/generate", strings.NewReader(tc.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			// By name, and with the list: "unknown sampler" that does not say which names exist
			// sends the caller back for another round trip to find out.
			if !strings.Contains(rec.Body.String(), "bad_params") {
				t.Fatalf("body = %s, want the reason on the wire", rec.Body)
			}
			if got.Prompt != "" {
				t.Fatal("the refused request reached the provider anyway")
			}
		})
	}
}

// The other half of the same wire: `loras` in the POST body reaches the provider's Request. A
// field the REST layer drops is a field the tool advertises and nothing applies.
func TestGenerateForwardsLoras(t *testing.T) {
	var got Request
	withImagegenSession(t, session.KindClaude, stubProvider{
		id:     ProviderComfy,
		gotReq: &got,
		res:    Result{Images: []Image{{Bytes: tinyPNG(t, 1, 1), MIME: "image/png"}}, Provider: ProviderComfy},
		caps:   &Caps{Ops: []Op{OpGenerate}, Loras: []LoraInfo{{Name: "watercolor-v2", BaseModel: "sdxl"}}},
	})
	body := `{"session":"slot01","prompt":"a cat","loras":[{"name":"watercolor-v2","weight":0.6}]}`
	rec := httptest.NewRecorder()
	HandleGenerate(rec, httptest.NewRequest(http.MethodPost, "/imagegen/generate", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if len(got.Loras) != 1 || got.Loras[0].Name != "watercolor-v2" || got.Loras[0].Weight != 0.6 {
		t.Errorf("request loras = %+v, want the one that was asked for", got.Loras)
	}
}

// ADR 0094 decision 2/4's edge refusal, on the blocking route — P0 completion condition (2)
// requires this on both the blocking route and the queue's (jobs_test.go's TestSpecRefuses*
// covers that side). This uses the REAL comfyProvider (comfyStub), not stubProvider, because the
// refusal resolves a family off the engine's own catalogue, which a hand-built stub has none of.
func TestGenerateRefusesStrengthAndSizeAgainstQwenImageEdit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.WriteMeta(session.Meta{Name: "slot01", Kind: session.KindClaude, Dir: home})
	oldEnabled := Enabled
	Enabled = func() bool { return true }
	t.Cleanup(func() { Enabled = oldEnabled })

	p, _ := comfyStub(t, qwenEditConn(), nil)
	withStubProvider(t, p)

	body := `{"session":"slot01","op":"edit","model":"qwen-edit-row","prompt":"x","strength":0.3}`
	rec := httptest.NewRecorder()
	HandleGenerate(rec, httptest.NewRequest(http.MethodPost, "/imagegen/generate", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_strength") {
		t.Fatalf("status = %d, body = %s, want 400 bad_strength", rec.Code, rec.Body)
	}

	body = `{"session":"slot01","op":"edit","model":"qwen-edit-row","prompt":"x","size":"1024x1024"}`
	rec = httptest.NewRecorder()
	HandleGenerate(rec, httptest.NewRequest(http.MethodPost, "/imagegen/generate", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_size") {
		t.Fatalf("status = %d, body = %s, want 400 bad_size", rec.Code, rec.Body)
	}
}

// A named provider may not be the caller's own CLI. The tool's enum already leaves it out, but
// the advertised set is a scope boundary — a guessed name in tools/call must not cross it and
// spend the plan twice for a picture this session can make with its own built-in tool.
func TestGenerateRefusesTheSessionsOwnCLI(t *testing.T) {
	withImagegenSession(t, session.KindCodex,
		stubProvider{id: "codex"},
		stubProvider{id: "agy"},
	)
	body := `{"session":"slot01","prompt":"a cat","provider":"codex"}`
	rec := httptest.NewRecorder()
	HandleGenerate(rec, httptest.NewRequest(http.MethodPost, "/imagegen/generate", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want a refusal", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "imagegen_own_cli") {
		t.Fatalf("body = %s, want the reason on the wire", rec.Body)
	}
	// Naming the service is what stops the refusal being read as "GPT Image is unreachable from
	// here": it is reachable, through this session's own built-in tool.
	if !strings.Contains(rec.Body.String(), "GPT Image") {
		t.Fatalf("body = %s, want the service named so the refusal is actionable", rec.Body)
	}
}
