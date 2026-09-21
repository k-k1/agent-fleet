package mcpx

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubImageGenStatus points the loopback Agent client at a server answering
// /imagegen/status with st, and /imagegen/generate through generate (nil = 500, which is
// what "the route is not part of this test" should look like).
func stubImageGenStatus(t *testing.T, st mcpImageGenStatus) {
	t.Helper()
	stubAgentForImageGen(t, st, nil)
}

func stubAgentForImageGen(t *testing.T, st mcpImageGenStatus, generate http.HandlerFunc) {
	t.Helper()
	body, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	stubAgentForImageGenRaw(t, string(body), generate)
}

// stubAgentForImageGenRaw answers /imagegen/status with a BODY rather than with a struct. It
// exists for the one failure the typed helper above structurally cannot see: a field the Agent
// really sends and mcpImageGenStatus has no home for is dropped in silence, and a test that
// encodes the very struct it then decodes agrees with itself either way (the same blind spot
// sessionWire had). A test that cares whether a fact SURVIVES the relay writes the JSON out.
func stubAgentForImageGenRaw(t *testing.T, status string, generate http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/imagegen/status":
			_, _ = w.Write([]byte(status))
		case "/imagegen/generate":
			if generate == nil {
				http.Error(w, `{"error":{"code":"unexpected","message":"not stubbed"}}`, http.StatusInternalServerError)
				return
			}
			generate(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)
}

func withImageGen(t *testing.T, on bool) {
	t.Helper()
	oldSelfReport, oldEnabled, oldSource := selfReportOnly(), mcpImageGenEnabled, mcpSourceSession
	setSelfReportOnly(true)
	mcpImageGenEnabled = on
	mcpSourceSession = "slot01"
	t.Cleanup(func() {
		setSelfReportOnly(oldSelfReport)
		mcpImageGenEnabled, mcpSourceSession = oldEnabled, oldSource
	})
}

// The tool literal must spell its name out for the advertised-schema scan (it only reads
// string literals), so the constant the dispatch uses could drift away from it unnoticed.
func TestImageGenToolNameMatchesConstant(t *testing.T) {
	tools := mcpStdioImageGenTools(imageGenOffer{Providers: []string{"agy"}, Ops: []string{"generate"}})
	if len(tools) != 1 || tools[0]["name"] != mcpToolGenerateImage {
		t.Fatalf("advertised name = %v, want %q", tools[0]["name"], mcpToolGenerateImage)
	}
}

// imageGenSchemaProps digs the tool's inputSchema properties out, so a test can ask what the
// schema actually offers rather than what it was meant to.
func imageGenSchemaProps(tools []map[string]any) map[string]any {
	if len(tools) == 0 {
		return nil
	}
	schema, _ := tools[0]["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	return props
}

// The exclusion of ADR 0069 decision 8, and its negative control: a session whose own CLI a
// route would drive already has that CLI's built-in image tool, so going out through a second
// process of it would double the cost for nothing — but the same session DOES want the fleet
// tool for every OTHER route, which is why the rule is applied per provider rather than to the
// effective one only.
func TestImageGenAdvertisedByKindAndProvider(t *testing.T) {
	codex := mcpImageGenProvider{ID: "codex", Ops: []string{"generate", "edit"}}
	agy := mcpImageGenProvider{ID: "agy", Ops: []string{"generate", "edit"}, AspectRatios: []string{"1:1", "16:9"}}
	bedrock := mcpImageGenProvider{ID: "bedrock", Ops: []string{"generate", "inpaint"}}

	for _, tc := range []struct {
		name          string
		status        mcpImageGenStatus
		wantAdv       bool
		wantProviders []string
		wantOps       []string
		wantRatios    int
		imageGen      bool
	}{
		{
			name: "a codex session is offered every route but codex",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "codex",
				Providers: []mcpImageGenProvider{codex, agy}},
			imageGen: true, wantAdv: true,
			wantProviders: []string{"agy"}, wantOps: []string{"generate", "edit"}, wantRatios: 2,
		},
		{
			name: "a codex session with only codex ready loses the tool entirely",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "codex",
				Providers: []mcpImageGenProvider{codex}},
			imageGen: true,
		},
		{
			name: "an agy session is excluded from agy for the same reason",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "agy",
				Providers: []mcpImageGenProvider{agy, codex}},
			imageGen: true, wantAdv: true,
			wantProviders: []string{"codex"}, wantOps: []string{"generate", "edit"},
		},
		{
			// The union is the point: auto already routes an op only the SECOND provider
			// supports to that provider, so advertising the first one's ops alone hid it.
			name: "a claude session gets every provider, and the union of what they can do",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{agy, bedrock}},
			imageGen: true, wantAdv: true,
			wantProviders: []string{"agy", "bedrock"},
			wantOps:       []string{"generate", "edit", "inpaint"}, wantRatios: 2,
		},
		{
			// Version skew: this child can outlive an Agent that predates the per-provider
			// list. The effective one in the flat fields still has to work.
			name: "an Agent that answers with only the flat fields still serves the tool",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Provider: "codex", Ops: []string{"generate"}},
			imageGen: true, wantAdv: true,
			wantProviders: []string{"codex"}, wantOps: []string{"generate"},
		},
		{
			name:     "no provider is ready",
			status:   mcpImageGenStatus{Enabled: true, Ready: false, Kind: "claude"},
			imageGen: true,
		},
		{
			name: "the flag is off, so the Agent is never even asked",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{codex}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withImageGen(t, tc.imageGen)
			stubImageGenStatus(t, tc.status)
			offer, ok := mcpImageGenAdvertise()
			if ok != tc.wantAdv {
				t.Fatalf("advertise = %v, want %v", ok, tc.wantAdv)
			}
			if !reflect.DeepEqual(offer.Providers, tc.wantProviders) {
				t.Fatalf("providers = %v, want %v", offer.Providers, tc.wantProviders)
			}
			if !reflect.DeepEqual(offer.Ops, tc.wantOps) {
				t.Fatalf("ops = %v, want %v", offer.Ops, tc.wantOps)
			}
			if len(offer.AspectRatios) != tc.wantRatios {
				t.Fatalf("aspect ratios = %v, want %d", offer.AspectRatios, tc.wantRatios)
			}
			if got := advertisedNames(t); tc.wantAdv != got[mcpToolGenerateImage] {
				t.Fatalf("generate_image in tools/list = %v, want %v", got[mcpToolGenerateImage], tc.wantAdv)
			}

			props := imageGenSchemaProps(mcpStdioImageGenTools(offer))
			// Each parameter appears only where it is real: aspect_ratio only when some
			// provider takes one, provider only when there is a choice to make. Advertising
			// either otherwise is the size mistake again — a knob that moves nothing.
			if _, has := props["aspect_ratio"]; has != (tc.wantRatios > 0) {
				t.Fatalf("aspect_ratio in schema = %v, want %v", has, tc.wantRatios > 0)
			}
			prov, has := props["provider"].(map[string]any)
			if has != (len(tc.wantProviders) > 1) {
				t.Fatalf("provider in schema = %v, want %v", has, len(tc.wantProviders) > 1)
			}
			if has {
				// The enum must be the offer, so the session's own CLI is not nameable.
				if got, _ := prov["enum"].([]string); !reflect.DeepEqual(got, tc.wantProviders) {
					t.Fatalf("provider enum = %v, want %v", got, tc.wantProviders)
				}
			}
		})
	}
}

// An unreachable Agent means the tool could not work anyway; advertising it would produce a
// tool whose every call fails.
// ADR 0072 decision 5, phase P2: model follows the exact same "only with a real choice" rule
// provider already does, and the enum/warm marker have to survive the round trip through
// mcpImageGenAdvertise into the tool schema.
func TestImageGenModelOfferedOnlyWithARealChoice(t *testing.T) {
	sdxl := mcpImageGenModel{ID: "sdxl-base-1.0", Description: "photoreal, general purpose"}
	klein := mcpImageGenModel{ID: "klein-4b", Warm: true}

	for _, tc := range []struct {
		name       string
		status     mcpImageGenStatus
		wantModels []mcpImageGenModel
	}{
		{
			// offer.Models is still the union (same as Ops/AspectRatios) — the "real choice"
			// gate is a SCHEMA-layer decision below, not something the offer itself narrows.
			name: "one model is not a choice",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "comfy", Ops: []string{"generate"}, Models: []mcpImageGenModel{sdxl}}}},
			wantModels: []mcpImageGenModel{sdxl},
		},
		{
			name: "two models on the same provider is a real choice",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "comfy", Ops: []string{"generate"}, Models: []mcpImageGenModel{sdxl, klein}}}},
			wantModels: []mcpImageGenModel{sdxl, klein},
		},
		{
			name: "a provider with no Models field at all offers none",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "agy", Ops: []string{"generate"}}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withImageGen(t, true)
			stubImageGenStatus(t, tc.status)
			offer, ok := mcpImageGenAdvertise()
			if !ok {
				t.Fatal("expected the tool to be advertised")
			}
			if !reflect.DeepEqual(offer.Models, tc.wantModels) {
				t.Fatalf("models = %+v, want %+v", offer.Models, tc.wantModels)
			}
			props := imageGenSchemaProps(mcpStdioImageGenTools(offer))
			model, has := props["model"].(map[string]any)
			if has != (len(tc.wantModels) > 1) {
				t.Fatalf("model in schema = %v, want %v", has, len(tc.wantModels) > 1)
			}
			if has {
				enum, _ := model["enum"].([]string)
				if len(enum) != len(tc.wantModels) {
					t.Fatalf("model enum = %v, want %d entries", enum, len(tc.wantModels))
				}
				desc, _ := model["description"].(string)
				if !strings.Contains(desc, "photoreal, general purpose") {
					t.Errorf("description does not carry the catalogue's own line: %s", desc)
				}
				if !strings.Contains(desc, "klein-4b") {
					t.Errorf("description does not name the warm model: %s", desc)
				}
			}
		})
	}
}

// ADR 0072 decision 5, phase P3. Two rules that differ from `model` on purpose: ONE LoRA is
// already a choice (applying it or not are two pictures), and the enum is every LoRA rather than
// the ones that fit the chosen checkpoint — a schema is built at tools/list, before `model`
// exists, so it cannot depend on it. That is why each line has to carry its own family.
func TestImageGenLorasOfferedWithTheirFamilies(t *testing.T) {
	water := mcpImageGenLora{Name: "watercolor-v2", Description: "soft watercolour", BaseModel: "sdxl"}
	lineart := mcpImageGenLora{Name: "klein-lineart", BaseModel: "flux2-klein"}

	for _, tc := range []struct {
		name      string
		status    mcpImageGenStatus
		wantLoras []mcpImageGenLora
	}{
		{
			name: "one LoRA is already a choice",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "comfy", Ops: []string{"generate"}, Loras: []mcpImageGenLora{water}}}},
			wantLoras: []mcpImageGenLora{water},
		},
		{
			name: "LoRAs of both families are offered together",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "comfy", Ops: []string{"generate"}, Loras: []mcpImageGenLora{water, lineart}}}},
			wantLoras: []mcpImageGenLora{water, lineart},
		},
		{
			name: "a provider with none offers none",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "agy", Ops: []string{"generate"}}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withImageGen(t, true)
			stubImageGenStatus(t, tc.status)
			offer, ok := mcpImageGenAdvertise()
			if !ok {
				t.Fatal("expected the tool to be advertised")
			}
			if !reflect.DeepEqual(offer.Loras, tc.wantLoras) {
				t.Fatalf("loras = %+v, want %+v", offer.Loras, tc.wantLoras)
			}
			props := imageGenSchemaProps(mcpStdioImageGenTools(offer))
			loras, has := props["loras"].(map[string]any)
			if has != (len(tc.wantLoras) > 0) {
				t.Fatalf("loras in schema = %v, want %v", has, len(tc.wantLoras) > 0)
			}
			if !has {
				return
			}
			items, _ := loras["items"].(map[string]any)
			itemProps, _ := items["properties"].(map[string]any)
			nameProp, _ := itemProps["name"].(map[string]any)
			enum, _ := nameProp["enum"].([]string)
			if len(enum) != len(tc.wantLoras) {
				t.Fatalf("name enum = %v, want %d entries", enum, len(tc.wantLoras))
			}
			desc, _ := loras["description"].(string)
			for _, want := range []string{"watercolor-v2", "sdxl", "soft watercolour"} {
				if !strings.Contains(desc, want) {
					t.Errorf("description does not carry %q: %s", want, desc)
				}
			}
		})
	}
}

// ADR 0081 decision 5, on the tool route. An adapter applied without the words it was trained on
// loads, costs the whole generation and changes nothing visible — so the words have to be in
// front of the caller WHILE it writes the prompt, which on this route means the schema.
//
// The status is written as JSON rather than built as a struct on purpose: what this guards is a
// relay, and the Agent has published `trained_words` on /imagegen/status since ADR 0081 while
// mcpImageGenLora quietly had nowhere to put them. Encoding the same struct the decoder fills
// would have agreed with itself both before and after that fix.
func TestImageGenLoraTriggerWordsReachTheSchema(t *testing.T) {
	withImageGen(t, true)
	stubAgentForImageGenRaw(t, `{"enabled":true,"ready":true,"kind":"claude","providers":[
		{"id":"comfy","ops":["generate"],"loras":[
			{"name":"watercolor-v2","description":"soft watercolour","baseModel":"sdxl",
			 "trained_words":["wtrcolor style","loose wash"]},
			{"name":"klein-lineart","baseModel":"flux2-klein"}]}]}`, nil)

	offer, ok := mcpImageGenAdvertise()
	if !ok {
		t.Fatal("expected the tool to be advertised")
	}
	if got := offer.Loras[0].TrainedWords; !reflect.DeepEqual(got, []string{"wtrcolor style", "loose wash"}) {
		t.Fatalf("trained words = %v — the Agent sent them and this layer dropped them", got)
	}
	props := imageGenSchemaProps(mcpStdioImageGenTools(offer))
	loras, _ := props["loras"].(map[string]any)
	desc, _ := loras["description"].(string)
	for _, want := range []string{"wtrcolor style", "loose wash"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description does not carry the trigger word %q: %s", want, desc)
		}
	}
	// An adapter that publishes none must not grow an empty clause — a bare "triggers:" is worse
	// than saying nothing, and every word here is paid for in every session's first turn.
	if i := strings.Index(desc, "klein-lineart"); i < 0 || strings.Contains(desc[i:], "triggers:") {
		t.Errorf("a LoRA with no trigger words should be spelled plainly: %s", desc)
	}
	// The word list must not use the separator that divides one adapter from the next, or the
	// last trigger of one runs into the name of the other.
	if strings.Contains(desc, "wtrcolor style / loose wash") {
		t.Errorf("trigger words share the line separator, so the adapter boundary is gone: %s", desc)
	}
}

// seed is offered only where a route takes one, and the union is an OR across the offered
// providers — the same "advertise it where it reaches something" rule as aspect_ratio.
func TestImageGenSeedOfferedOnlyWhereARouteTakesOne(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   mcpImageGenStatus
		wantSeed bool
	}{
		{
			name: "a route that takes one",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "comfy", Ops: []string{"generate"}, Seed: true}}},
			wantSeed: true,
		},
		{
			name: "no route takes one",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "agy", Ops: []string{"generate"}}}},
		},
		{
			name: "one of two takes one",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{
					{ID: "agy", Ops: []string{"generate"}},
					{ID: "comfy", Ops: []string{"generate"}, Seed: true},
				}},
			wantSeed: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withImageGen(t, true)
			stubImageGenStatus(t, tc.status)
			offer, ok := mcpImageGenAdvertise()
			if !ok {
				t.Fatal("expected the tool to be advertised")
			}
			if offer.Seed != tc.wantSeed {
				t.Fatalf("offer.Seed = %v, want %v", offer.Seed, tc.wantSeed)
			}
			props := imageGenSchemaProps(mcpStdioImageGenTools(offer))
			seed, has := props["seed"].(map[string]any)
			if has != tc.wantSeed {
				t.Fatalf("seed in schema = %v, want %v", has, tc.wantSeed)
			}
			if !has {
				return
			}
			if seed["type"] != "integer" {
				t.Errorf("seed type = %v, want integer", seed["type"])
			}
			// The ceiling is JavaScript's safe-integer limit: a bigger number comes back from a
			// JSON client as a DIFFERENT seed, which breaks the one thing a seed is for.
			if seed["maximum"] != 9007199254740991 {
				t.Errorf("seed maximum = %v, want the JS safe-integer limit", seed["maximum"])
			}
		})
	}
}

// strength follows the same union rule as seed, and one more of its own: it is what an EDIT does
// to the caller's picture, so a route that cannot edit must not carry the argument. inpaint does
// not count — that op repaints its masked area in full, and an argument the graph is obliged to
// ignore is exactly what this schema keeps out.
func TestImageGenStrengthOfferedOnlyWithEdit(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       mcpImageGenStatus
		wantOffer    bool
		wantInSchema bool
	}{
		{
			name: "a route that edits and varies it",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "comfy", Ops: []string{"generate", "edit"}, Strength: true}}},
			wantOffer: true, wantInSchema: true,
		},
		{
			name: "a route that edits but cannot vary it",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "agy", Ops: []string{"generate", "edit"}}}},
		},
		{
			name: "inpaint alone is not a reason to offer it",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "comfy", Ops: []string{"generate", "inpaint"}, Strength: true}}},
			wantOffer: true,
		},
		{
			name: "one of two varies it",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{
					{ID: "agy", Ops: []string{"generate", "edit"}},
					{ID: "comfy", Ops: []string{"generate", "edit"}, Strength: true},
				}},
			wantOffer: true, wantInSchema: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withImageGen(t, true)
			stubImageGenStatus(t, tc.status)
			offer, ok := mcpImageGenAdvertise()
			if !ok {
				t.Fatal("expected the tool to be advertised")
			}
			if offer.Strength != tc.wantOffer {
				t.Fatalf("offer.Strength = %v, want %v", offer.Strength, tc.wantOffer)
			}
			props := imageGenSchemaProps(mcpStdioImageGenTools(offer))
			strength, has := props["strength"].(map[string]any)
			if has != tc.wantInSchema {
				t.Fatalf("strength in schema = %v, want %v", has, tc.wantInSchema)
			}
			if !has {
				return
			}
			if strength["type"] != "number" {
				t.Errorf("strength type = %v, want number", strength["type"])
			}
			// 0 is excluded by the schema as well as refused by the Agent: at denoise 0 the
			// sampler hands the latent straight back, so it would wake a GPU box for nothing.
			if strength["exclusiveMinimum"] != 0 || strength["maximum"] != 1 {
				t.Errorf("strength range = (%v, %v], want (0, 1]", strength["exclusiveMinimum"], strength["maximum"])
			}
			// The direction has to be in the text: upstream is not consistent about it (diffusers'
			// `strength` and Stability's `image_strength` run opposite ways), so a caller that
			// infers it from the name has a 50% chance of asking for the opposite picture.
			desc, _ := strength["description"].(string)
			if !strings.Contains(desc, "op=edit") || !strings.Contains(desc, "0.6 when omitted") {
				t.Errorf("description does not say which op it belongs to or what the default is: %s", desc)
			}
		})
	}
}

func TestImageGenNotAdvertisedWhenAgentUnreachable(t *testing.T) {
	withImageGen(t, true)
	t.Setenv("AGENT_ADDR", ":1") // nothing listens
	if _, ok := mcpImageGenAdvertise(); ok {
		t.Fatal("generate_image was advertised with no Agent to serve it")
	}
}

// The tool has to SAY which image service each route reaches, because the ids are CLI names.
// Measured 2026-09-08: a codex session whose only route was `agy` answered a request to compare
// the two services with "the Gemini route is not available in this session" — the word Gemini
// appeared nowhere in the tool, and a one-entry offer drops the `provider` enum that is the only
// other place a name could have shown up.
func TestImageGenDescriptionNamesTheServices(t *testing.T) {
	agy := mcpImageGenProvider{ID: "agy", Service: "Gemini の画像生成", Ops: []string{"generate"}}
	codex := mcpImageGenProvider{ID: "codex", Service: "GPT Image", Ops: []string{"generate"}}

	t.Run("a codex session is told what its one route is, and why the other is missing", func(t *testing.T) {
		withImageGen(t, true)
		stubImageGenStatus(t, mcpImageGenStatus{Enabled: true, Ready: true, Kind: "codex",
			Providers: []mcpImageGenProvider{agy, codex}})
		offer, ok := mcpImageGenAdvertise()
		if !ok || offer.SelfExcluded != "codex" {
			t.Fatalf("advertise = %v, offer = %+v, want codex recorded as excluded", ok, offer)
		}
		tools := mcpStdioImageGenTools(offer)
		desc, _ := tools[0]["description"].(string)
		// The route it HAS, the one it does not, and what to do instead. Without the last two a
		// refusal to compare reads as "that service is unreachable from here".
		for _, want := range []string{"Gemini の画像生成", "GPT Image", "built-in"} {
			if !strings.Contains(desc, want) {
				t.Fatalf("description does not mention %q: %s", want, desc)
			}
		}
		// Nothing to choose between, so the enum is absent — which is exactly why the
		// description is the only place either name can appear.
		if _, has := imageGenSchemaProps(tools)["provider"]; has {
			t.Fatalf("a single-route offer advertised a provider argument")
		}
	})

	t.Run("a session with both routes gets both names and no exclusion note", func(t *testing.T) {
		withImageGen(t, true)
		stubImageGenStatus(t, mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
			Providers: []mcpImageGenProvider{agy, codex}})
		offer, ok := mcpImageGenAdvertise()
		if !ok || offer.SelfExcluded != "" {
			t.Fatalf("advertise = %v, offer = %+v, want nothing excluded", ok, offer)
		}
		desc, _ := mcpStdioImageGenTools(offer)[0]["description"].(string)
		for _, want := range []string{"agy = Gemini の画像生成", "codex = GPT Image"} {
			if !strings.Contains(desc, want) {
				t.Fatalf("description does not map %q: %s", want, desc)
			}
		}
		if strings.Contains(desc, "not reachable from this tool") {
			t.Fatalf("description claims a route is missing when none is: %s", desc)
		}
	})
}

func advertisedNames(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, tool := range mcpStdioToolList() {
		name, _ := tool["name"].(string)
		out[name] = true
	}
	return out
}

// The advertised set is the scope boundary: a client that guesses the name must not reach a
// route that spends the user's plan quota. Both surfaces refuse — the session one because the
// tool is not in its advertised set, the assistant one because it never gets the flag at all.
func TestGenerateImageRefusedWhenFlagOff(t *testing.T) {
	for _, selfReport := range []bool{true, false} {
		withImageGen(t, false)
		setSelfReportOnly(selfReport)
		resp := callGenerateImage(t, map[string]any{"prompt": "a cat"})
		if !strings.Contains(resp, `"isError":true`) {
			t.Fatalf("selfReport=%v: result = %s, want a refusal", selfReport, resp)
		}
	}
}

func TestGenerateImageReturnsPathAndWarnings(t *testing.T) {
	withImageGen(t, true)
	var got map[string]any
	stubAgentForImageGen(t,
		mcpImageGenStatus{Enabled: true, Ready: true, Provider: "codex", Kind: "claude", Ops: []string{"generate"}},
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"files":[{"path":"/home/u/.cache/agent-fleet/generated/sid/image-1.png","name":"image-1.png","mime":"image/png","bytes":848000,"width":1254,"height":1254}],"provider":"codex","model":"gpt-5.4-mini","warnings":["size=1024x1024 requested, 1254x1254 produced"]}`))
		})

	resp := callGenerateImage(t, map[string]any{"prompt": "a cat", "size": "1024x1024", "count": 1, "model": "klein-4b"})
	if got["session"] != "slot01" || got["prompt"] != "a cat" || got["size"] != "1024x1024" {
		t.Fatalf("forwarded body = %v", got)
	}
	if got["model"] != "klein-4b" {
		t.Fatalf("forwarded body's model = %v, want klein-4b (ADR 0072 decision 5)", got["model"])
	}
	if got["loras"] != nil {
		t.Fatalf("forwarded body's loras = %v, want nothing when none was asked for", got["loras"])
	}
	for _, want := range []string{"image-1.png", "1254x1254", "codex"} {
		if !strings.Contains(resp, want) {
			t.Fatalf("result = %s, want it to contain %q", resp, want)
		}
	}
	// The bytes themselves are never returned: a measured PNG is 848 KB, and base64 of it
	// would ride in the session's context for the rest of the conversation.
	if strings.Contains(resp, `"type":"image"`) || strings.Contains(resp, "base64") {
		t.Fatalf("result carried image bytes: %s", resp)
	}
}

// The `loras` argument reaches the Agent as it was written, name and strength both (ADR 0072
// decision 5, phase P3). Whether the pairing works is the Agent's refusal to make, by name —
// this layer dropping it would advertise a knob that moves nothing.
func TestGenerateImageForwardsLoras(t *testing.T) {
	withImageGen(t, true)
	var got map[string]any
	stubAgentForImageGen(t,
		mcpImageGenStatus{Enabled: true, Ready: true, Provider: "comfy", Kind: "claude", Ops: []string{"generate"}},
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"files":[{"path":"/tmp/image-1.png","name":"image-1.png","mime":"image/png","bytes":1}],"provider":"comfy"}`))
		})

	callGenerateImage(t, map[string]any{"prompt": "a cat",
		"loras": []any{map[string]any{"name": "watercolor-v2", "weight": 0.6}}})

	loras, _ := got["loras"].([]any)
	if len(loras) != 1 {
		t.Fatalf("forwarded body's loras = %v", got["loras"])
	}
	first, _ := loras[0].(map[string]any)
	if first["name"] != "watercolor-v2" || first["weight"] != 0.6 {
		t.Fatalf("forwarded lora = %v, want the name and the strength", first)
	}
}

// The seed reaches the Agent as a number, and an omitted one is absent from the body rather
// than sent as 0 — the wire has to keep the same distinction the Request type does.
func TestGenerateImageForwardsTheSeed(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want any
	}{
		{"a pinned seed", map[string]any{"prompt": "a cat", "seed": 1234}, float64(1234)},
		{"seed zero", map[string]any{"prompt": "a cat", "seed": 0}, float64(0)},
		{"no seed", map[string]any{"prompt": "a cat"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withImageGen(t, true)
			var got map[string]any
			stubAgentForImageGen(t,
				mcpImageGenStatus{Enabled: true, Ready: true, Provider: "comfy", Kind: "claude", Ops: []string{"generate"}},
				func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewDecoder(r.Body).Decode(&got)
					_, _ = w.Write([]byte(`{"files":[{"path":"/tmp/i.png","name":"i.png","mime":"image/png","bytes":1}],"provider":"comfy"}`))
				})
			callGenerateImage(t, tc.args)
			if got["seed"] != tc.want {
				t.Fatalf("forwarded seed = %v (%T), want %v", got["seed"], got["seed"], tc.want)
			}
		})
	}
}

// strength rides the same wire with the same distinction, and here it is what lets the Agent
// refuse 0 by value: folded into "not given" at this layer, `"strength": 0` would arrive as a
// request to edit at the full default amount — the opposite of what was asked for.
func TestGenerateImageForwardsTheStrength(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want any
	}{
		{"a gentle edit", map[string]any{"prompt": "a cat", "op": "edit", "strength": 0.25}, 0.25},
		{"zero reaches the Agent to be refused", map[string]any{"prompt": "a cat", "op": "edit", "strength": 0}, float64(0)},
		{"none is absent", map[string]any{"prompt": "a cat", "op": "edit"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withImageGen(t, true)
			var got map[string]any
			stubAgentForImageGen(t,
				mcpImageGenStatus{Enabled: true, Ready: true, Provider: "comfy", Kind: "claude", Ops: []string{"generate", "edit"}},
				func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewDecoder(r.Body).Decode(&got)
					_, _ = w.Write([]byte(`{"files":[{"path":"/tmp/i.png","name":"i.png","mime":"image/png","bytes":1}],"provider":"comfy"}`))
				})
			callGenerateImage(t, tc.args)
			if got["strength"] != tc.want {
				t.Fatalf("forwarded strength = %v (%T), want %v", got["strength"], got["strength"], tc.want)
			}
		})
	}
}

// The sampler overlay is offered only where a route BUILDS the sampler graph, which is exactly
// what having names to send means: the vendor routes have neither a step count nor a sampler,
// and an argument they are obliged to ignore is what this schema exists to keep out.
func TestImageGenParamsOfferedOnlyWhereTheGraphIsBuilt(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		status                   mcpImageGenStatus
		wantParams, wantSchedule bool
	}{
		{
			name: "a route that builds the graph",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "comfy", Ops: []string{"generate"},
					Samplers: []string{"euler", "dpmpp_2m"}, Schedulers: []string{"normal", "karras"}}}},
			wantParams: true, wantSchedule: true,
		},
		{
			name: "a vendor route drives a CLI with a prompt and has no knobs at all",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "agy", Ops: []string{"generate"}}}},
		},
		{
			// An Agent that sends samplers but no schedule names must not have one invented for
			// it: the field would be an enum with nothing in it, which no caller can satisfy.
			name: "samplers without schedulers offer only the sampler",
			status: mcpImageGenStatus{Enabled: true, Ready: true, Kind: "claude",
				Providers: []mcpImageGenProvider{{ID: "comfy", Ops: []string{"generate"},
					Samplers: []string{"euler"}}}},
			wantParams: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withImageGen(t, true)
			stubImageGenStatus(t, tc.status)
			offer, ok := mcpImageGenAdvertise()
			if !ok {
				t.Fatal("expected the tool to be advertised")
			}
			props := imageGenSchemaProps(mcpStdioImageGenTools(offer))
			params, has := props["params"].(map[string]any)
			if has != tc.wantParams {
				t.Fatalf("params in schema = %v, want %v", has, tc.wantParams)
			}
			if !has {
				return
			}
			fields, _ := params["properties"].(map[string]any)
			for _, want := range []string{"steps", "cfg", "sampler"} {
				if _, ok := fields[want]; !ok {
					t.Errorf("params has no %s: %v", want, fields)
				}
			}
			if _, ok := fields["scheduler"]; ok != tc.wantSchedule {
				t.Errorf("scheduler in params = %v, want %v", ok, tc.wantSchedule)
			}
			// The two numbers are per-checkpoint and this schema cannot say which — the enum
			// of a sibling argument is built at tools/list, before `model` exists — so the
			// description has to warn instead of leaving the caller to infer a default.
			desc, _ := params["description"].(string)
			if !strings.Contains(desc, "per-checkpoint") || !strings.Contains(desc, "Leave it unset") {
				t.Errorf("description does not warn about the per-checkpoint numbers: %s", desc)
			}
		})
	}
}

// The enum comes from the AGENT's own allow-list, relayed, never from a copy in this package: a
// schema that offers a name this binary will not send is the pair disagreeing in front of the
// caller, one round trip later.
//
// The status is written as RAW JSON on purpose. A test that encodes the very struct it then
// decodes agrees with itself even when the field never reaches the wire — the blind spot
// sessionWire had.
func TestImageGenSamplerEnumComesFromTheAgent(t *testing.T) {
	withImageGen(t, true)
	stubAgentForImageGenRaw(t, `{"enabled":true,"ready":true,"kind":"claude","providers":[
		{"id":"comfy","ops":["generate"],
		 "samplers":["euler","euler_ancestral","dpmpp_2m"],
		 "schedulers":["normal","karras"]}]}`, nil)
	offer, ok := mcpImageGenAdvertise()
	if !ok {
		t.Fatal("expected the tool to be advertised")
	}
	if !reflect.DeepEqual(offer.Samplers, []string{"euler", "euler_ancestral", "dpmpp_2m"}) {
		t.Fatalf("offer samplers = %v, want the three the Agent sent", offer.Samplers)
	}
	props := imageGenSchemaProps(mcpStdioImageGenTools(offer))
	params, _ := props["params"].(map[string]any)
	fields, _ := params["properties"].(map[string]any)
	sampler, _ := fields["sampler"].(map[string]any)
	if !reflect.DeepEqual(sampler["enum"], []string{"euler", "euler_ancestral", "dpmpp_2m"}) {
		t.Fatalf("sampler enum = %v, want the Agent's own list", sampler["enum"])
	}
	scheduler, _ := fields["scheduler"].(map[string]any)
	if !reflect.DeepEqual(scheduler["enum"], []string{"normal", "karras"}) {
		t.Fatalf("scheduler enum = %v, want the Agent's own list", scheduler["enum"])
	}
}

// The overlay reaches the Agent under the same key and in the same shape the queue's route and
// the catalogue row use, per field. It is NOT flattened here: a caller who named only the
// sampler must not have steps and cfg zeroed on the way, because zero is what the merge reads
// as "declared nothing" and the checkpoint's own published numbers are what fills it.
func TestGenerateImageForwardsTheSamplerOverlay(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want map[string]any
	}{
		{
			name: "all four",
			args: map[string]any{"prompt": "a cat", "params": map[string]any{
				"steps": 30, "cfg": 6, "sampler": "dpmpp_2m", "scheduler": "karras"}},
			want: map[string]any{"steps": float64(30), "cfg": float64(6), "sampler": "dpmpp_2m", "scheduler": "karras"},
		},
		{
			name: "the sampler alone leaves the rest undeclared",
			args: map[string]any{"prompt": "a cat", "params": map[string]any{"sampler": "euler_ancestral"}},
			want: map[string]any{"sampler": "euler_ancestral"},
		},
		{"none is absent", map[string]any{"prompt": "a cat"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withImageGen(t, true)
			var got map[string]any
			stubAgentForImageGen(t,
				mcpImageGenStatus{Enabled: true, Ready: true, Provider: "comfy", Kind: "claude", Ops: []string{"generate"}},
				func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewDecoder(r.Body).Decode(&got)
					_, _ = w.Write([]byte(`{"files":[{"path":"/tmp/i.png","name":"i.png","mime":"image/png","bytes":1}],"provider":"comfy"}`))
				})
			callGenerateImage(t, tc.args)
			params, _ := got["params"].(map[string]any)
			if !reflect.DeepEqual(params, tc.want) {
				t.Fatalf("forwarded params = %v, want %v", got["params"], tc.want)
			}
		})
	}
}

// A failed generation must keep the Agent's own reason, or the model is told "it failed" with
// nothing to act on.
func TestGenerateImageKeepsAgentReason(t *testing.T) {
	withImageGen(t, true)
	stubAgentForImageGen(t,
		mcpImageGenStatus{Enabled: true, Ready: true, Provider: "codex", Kind: "claude", Ops: []string{"generate"}},
		func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":{"code":"imagegen_no_provider","message":"codex is not logged in"}}`, http.StatusServiceUnavailable)
		})
	resp := callGenerateImage(t, map[string]any{"prompt": "a cat"})
	if !strings.Contains(resp, "codex is not logged in") {
		t.Fatalf("result = %s, want the Agent's reason", resp)
	}
}

func callGenerateImage(t *testing.T, args map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	params, _ := json.Marshal(map[string]any{"name": mcpToolGenerateImage, "arguments": json.RawMessage(raw)})
	return string(mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params}))
}

// The heartbeat is what lifts opencode's 60 s per-call ceiling (measured: a 90 s call
// succeeded with a notification every 10 s, and was cut at 60.0 s without one).
func TestProgressHeartbeatWritesNotifications(t *testing.T) {
	var buf bytes.Buffer
	old, oldEvery := stdioOut, mcpProgressEvery
	stdioOut, mcpProgressEvery = &stdioWriter{w: bufio.NewWriter(&buf)}, 5*time.Millisecond
	t.Cleanup(func() { stdioOut, mcpProgressEvery = old, oldEvery })

	params, _ := json.Marshal(map[string]any{"_meta": map[string]any{"progressToken": "tok-1"}})
	stop := startProgressHeartbeat(mcpReq{ID: json.RawMessage(`1`), Params: params}, "生成中")
	time.Sleep(40 * time.Millisecond)
	stop()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("wrote %d notifications, want at least 2: %q", len(lines), buf.String())
	}
	var first struct {
		Method string `json:"method"`
		Params struct {
			ProgressToken string `json:"progressToken"`
			Progress      int    `json:"progress"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line is not JSON: %v (%q)", err, lines[0])
	}
	if first.Method != "notifications/progress" || first.Params.ProgressToken != "tok-1" || first.Params.Progress != 1 {
		t.Fatalf("first notification = %+v", first)
	}
	// Nothing may be written after stop returns: a heartbeat emitted after the tools/call
	// result would arrive out of order on the wire.
	before := buf.Len()
	time.Sleep(30 * time.Millisecond)
	if buf.Len() != before {
		t.Fatal("the heartbeat kept writing after stop()")
	}
}

// No token means the client is not listening for progress; an unaddressed notification is
// dropped, and writing one anyway is noise on the same pipe the responses use.
func TestProgressHeartbeatSilentWithoutToken(t *testing.T) {
	var buf bytes.Buffer
	old, oldEvery := stdioOut, mcpProgressEvery
	stdioOut, mcpProgressEvery = &stdioWriter{w: bufio.NewWriter(&buf)}, time.Millisecond
	t.Cleanup(func() { stdioOut, mcpProgressEvery = old, oldEvery })

	stop := startProgressHeartbeat(mcpReq{ID: json.RawMessage(`1`), Params: json.RawMessage(`{}`)}, "生成中")
	time.Sleep(10 * time.Millisecond)
	stop()
	if buf.Len() != 0 {
		t.Fatalf("wrote %q with no progressToken", buf.String())
	}
}

// 🔴 The call-side scope check must not go over the network (ADR 0072 H3, measured 2026-09-11).
//
// Re-deriving the advertised set on the call path meant asking the Agent's /imagegen/status with a
// 3-second budget, and that status asks every provider's Ready() — a round trip to the Control
// Plane for the fleet's own engines. A tools/call landing while the CP was busy (a CP replaced
// moments earlier) blew the budget, and a tool sitting in the client's own tool list answered with
// what read like a permission refusal.
//
// What the client may call is what it was told it may call, and this process already knows that.
func TestAdvertisedToolStaysCallableWhenTheAgentIsSlow(t *testing.T) {
	withImageGen(t, true)
	t.Cleanup(forgetAdvertised)

	var statusHits, slow int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/imagegen/status":
			atomic.AddInt32(&statusHits, 1)
			if atomic.LoadInt32(&slow) == 1 {
				// Longer than agentImageGenStatus' own 3-second budget, which is what a busy
				// Control Plane looked like.
				time.Sleep(4 * time.Second)
			}
			_ = json.NewEncoder(w).Encode(mcpImageGenStatus{
				Enabled: true, Ready: true, Provider: "comfy", Kind: "claude", Ops: []string{"generate"},
				Providers: []mcpImageGenProvider{{ID: "comfy", Ops: []string{"generate"}}},
			})
		case "/imagegen/generate":
			_, _ = w.Write([]byte(`{"files":[{"path":"/tmp/i.png","name":"i.png","mime":"image/png","bytes":1}],"provider":"comfy"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)

	// The client lists while the Agent is answering normally, which is what a real one does.
	if m := stdioDispatch(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); m == nil {
		t.Fatal("tools/list returned nothing")
	}
	if !mcpStdioToolAdvertised(mcpToolGenerateImage) {
		t.Fatal("generate_image was not advertised, so this test would prove nothing")
	}
	listed := atomic.LoadInt32(&statusHits)

	// Now the Agent is slow. The call must still go through, and must not ask again to find out.
	atomic.StoreInt32(&slow, 1)
	resp := callGenerateImage(t, map[string]any{"prompt": "a cat"})
	if strings.Contains(resp, "tools/list に無いツール名") {
		t.Fatalf("an advertised tool refused its own call: %s", resp)
	}
	if got := atomic.LoadInt32(&statusHits) - listed; got != 0 {
		t.Errorf("the call path asked /imagegen/status %d more time(s); it must read what was advertised", got)
	}

	// The positive control: without the remembered set the call falls back to re-deriving it, the
	// slow status times out, and today's refusal comes back. That is what this fix removed.
	forgetAdvertised()
	if resp := callGenerateImage(t, map[string]any{"prompt": "a cat"}); !strings.Contains(resp, "tools/list に無いツール名") {
		t.Errorf("re-deriving over a slow Agent did NOT refuse, so the test above proves nothing: %s", resp)
	}
}

// The boundary itself is unchanged: a name this server never advertised is still refused on the
// call path, so a client that guesses one cannot reach a handler through it.
func TestUnadvertisedToolIsStillRefusedAfterAList(t *testing.T) {
	withImageGen(t, false) // no image tool in the advertised set
	t.Cleanup(forgetAdvertised)
	stubImageGenStatus(t, mcpImageGenStatus{Enabled: false})

	if m := stdioDispatch(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); m == nil {
		t.Fatal("tools/list returned nothing")
	}
	for _, name := range []string{mcpToolGenerateImage, "list_my_sessions", "made_up_tool"} {
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": map[string]any{}})
		resp := string(mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params}))
		if !strings.Contains(resp, `"isError":true`) {
			t.Errorf("%s was not refused: %s", name, resp)
		}
	}
}

// The `inputs` ceiling is the offer's UNION and reaches the schema (ADR 0094 decision 5). It used
// to be the literal 5 on every route — which since the instruction-edit families over-promises by
// four on a comfy-only session, and the caller learns that by spending a round trip on a refusal.
func TestImageGenInputsCeilingComesFromTheOffer(t *testing.T) {
	for _, tc := range []struct {
		name      string
		providers []mcpImageGenProvider
		want      float64
	}{{
		name:      "one route, its own ceiling",
		providers: []mcpImageGenProvider{{ID: "image", Ops: []string{"edit"}, MaxInputs: 2}},
		want:      2,
	}, {
		name: "two routes, the larger of the two",
		providers: []mcpImageGenProvider{
			{ID: "image", Ops: []string{"edit"}, MaxInputs: 2},
			{ID: "agy", Ops: []string{"generate", "edit"}, MaxInputs: 3},
		},
		want: 3,
	}, {
		// The same pair the other way round. Without it "the largest" and "the last one wins"
		// are the same answer, and the second is wrong — it would hand a comfy-only ceiling to a
		// session that also has agy.
		name: "the larger one FIRST is still the answer",
		providers: []mcpImageGenProvider{
			{ID: "agy", Ops: []string{"generate", "edit"}, MaxInputs: 3},
			{ID: "image", Ops: []string{"edit"}, MaxInputs: 2},
		},
		want: 3,
	}, {
		// An Agent from before this field. Keeping the old literal is deliberate: the Agent
		// refuses what it cannot take either way, so guessing LOW here would take away an
		// argument that works on the routes which have always read five.
		name:      "an Agent that reports none keeps the old ceiling",
		providers: []mcpImageGenProvider{{ID: "codex", Ops: []string{"generate", "edit"}}},
		want:      mcpImageGenDefaultMaxInputs,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			withImageGen(t, true)
			stubImageGenStatus(t, mcpImageGenStatus{
				Enabled: true, Ready: true, Kind: "claude", Providers: tc.providers})
			offer, ok := mcpImageGenAdvertise()
			if !ok {
				t.Fatal("expected the tool to be advertised")
			}
			props := imageGenSchemaProps(mcpStdioImageGenTools(offer))
			inputs, has := props["inputs"].(map[string]any)
			if !has {
				t.Fatal("no `inputs` in the schema")
			}
			if got := inputs["maxItems"]; got != int(tc.want) {
				t.Errorf("inputs.maxItems = %v, want %v", got, tc.want)
			}
		})
	}
}
