package imagegen

// The per-family knobs past the four a plain KSampler reads: clip skip (sd15, sdxl), FLUX.1's
// distilled guidance (flux1) and anima's sampling shift. Each one used to be either fixed in the
// template or carried on the wire and applied nowhere.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// comfyKnobFixtures are the graphs a declared value produces, pinned the way the family goldens
// are. Each is its family's fixture from comfyFamilyFixtures with one Params field set.
var comfyKnobFixtures = []struct {
	name   string
	family comfyFamily
	params EngineParams
}{
	{"sdxl_clip_skip", ComfyFamilySDXL, EngineParams{ClipSkip: 2}},
	{"flux1_guidance", ComfyFamilyFlux1, EngineParams{Guidance: 5}},
	{"anima_shift", ComfyFamilyAnima, EngineParams{Shift: 14}},
}

func comfyFixtureFiles(t *testing.T, family comfyFamily) comfyFiles {
	t.Helper()
	for _, c := range comfyFamilyFixtures {
		if c.family == family {
			return c.files
		}
	}
	t.Fatalf("no fixture for %s", family)
	return comfyFiles{}
}

func TestComfyKnobGraphsMatchGoldenFixtures(t *testing.T) {
	for _, c := range comfyKnobFixtures {
		t.Run(c.name, func(t *testing.T) {
			p := comfyGoldenParams
			p.Params = c.params
			g, err := comfyBuildGraph(c.family, comfyFixtureFiles(t, c.family), p)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.MarshalIndent(g, "", "  ")
			got = append(got, '\n')
			path := filepath.Join("testdata", "comfy_"+c.name+".golden.json")
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			if string(got) != string(want) {
				t.Errorf("%s no longer matches %s.\nGot:\n%s", c.name, path, got)
			}
		})
	}
}

// A declared clip skip n is CLIPSetLastLayer(-n), and both encodes read it — on sd15 too, which
// shares the template. Undeclared adds no node: the family goldens pin that half.
func TestComfyClipSkipReachesBothEncodes(t *testing.T) {
	for _, family := range []comfyFamily{ComfyFamilySD15, ComfyFamilySDXL} {
		t.Run(string(family), func(t *testing.T) {
			p := comfyGoldenParams
			p.Params = EngineParams{ClipSkip: 3}
			p.Loras = []comfyLora{{Name: "a.safetensors", Weight: 1}}
			g, err := comfyBuildGraph(family, comfyFixtureFiles(t, family), p)
			if err != nil {
				t.Fatal(err)
			}
			n, ok := g["clipskip"]
			if !ok || n.ClassType != "CLIPSetLastLayer" || n.Inputs["stop_at_clip_layer"] != -3 {
				t.Fatalf("clipskip = %+v, want CLIPSetLastLayer(-3)", n)
			}
			// After the LoRA chain, so the encodes read the clip every adapter patched.
			if got := comfyLinkAt(t, g, "clipskip.clip"); got[0] != "lora1" || got[1] != 1 {
				t.Errorf("clipskip.clip reads %v, want the patched CLIP [lora1 1]", got)
			}
			for _, ref := range []string{"pos.clip", "neg.clip"} {
				if got := comfyLinkAt(t, g, ref); got[0] != "clipskip" {
					t.Errorf("%s reads %v, want the clip skip", ref, got)
				}
			}
		})
	}
}

// A row's declaration past the node's range keeps the family's own rather than failing the whole
// prompt at the engine's validation, the same leniency an unknown sampler name gets.
func TestComfyKnobsOutOfRangeFallBack(t *testing.T) {
	cases := []struct {
		family comfyFamily
		params EngineParams
		node   string
	}{
		{ComfyFamilySDXL, EngineParams{ClipSkip: comfyMaxClipSkip + 1}, "clipskip"},
		{ComfyFamilyAnima, EngineParams{Shift: comfyMaxShift + 1}, "ms"},
	}
	for _, c := range cases {
		p := comfyGoldenParams
		p.Params = c.params
		g, err := comfyBuildGraph(c.family, comfyFixtureFiles(t, c.family), p)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := g[c.node]; ok {
			t.Errorf("%s: %+v added %s", c.family, c.params, c.node)
		}
	}
	p := comfyGoldenParams
	p.Params = EngineParams{Guidance: comfyMaxGuidance + 1}
	g, err := comfyBuildGraph(ComfyFamilyFlux1, comfyFixtureFiles(t, ComfyFamilyFlux1), p)
	if err != nil {
		t.Fatal(err)
	}
	if got := g["guidance"].Inputs["guidance"]; got != 3.5 {
		t.Errorf("flux1 guidance = %v, want the family's 3.5", got)
	}
}

// The anima shift sits after the LoRAs and is what the sampler samples with.
func TestComfyAnimaShiftPatchesTheSampledModel(t *testing.T) {
	p := comfyGoldenParams
	p.Params = EngineParams{Shift: 20}
	p.Loras = []comfyLora{{Name: "a.safetensors", Weight: 1}}
	g, err := comfyBuildGraph(ComfyFamilyAnima, comfyFixtureFiles(t, ComfyFamilyAnima), p)
	if err != nil {
		t.Fatal(err)
	}
	if n := g["ms"]; n.ClassType != "ModelSamplingAuraFlow" || n.Inputs["shift"] != 20.0 {
		t.Fatalf("ms = %+v", n)
	}
	if got := comfyLinkAt(t, g, "ms.model"); got[0] != "lora1" || got[1] != 0 {
		t.Errorf("ms.model reads %v, want the patched MODEL", got)
	}
	if got := comfyLinkAt(t, g, "ks.model"); got[0] != "ms" {
		t.Errorf("ks.model reads %v, want the shifted model", got)
	}
}

// The request lays over the row one knob at a time, as for the other four.
func TestComfyEffectiveParamsTakesTheRequestsKnobs(t *testing.T) {
	row := EngineParams{Steps: 30, ClipSkip: 2, Guidance: 3.5, Shift: 3}
	got := comfyEffectiveParams(row, &EngineParams{Guidance: 4.5, Shift: 18})
	if got.Steps != 30 || got.ClipSkip != 2 || got.Guidance != 4.5 || got.Shift != 18 {
		t.Errorf("effective = %+v", got)
	}
	got = comfyEffectiveParams(row, &EngineParams{ClipSkip: 1})
	if got.ClipSkip != 1 || got.Guidance != 3.5 {
		t.Errorf("effective = %+v", got)
	}
}

// What the form shows as "default" is what runs: the family's value, overlaid by the row.
func TestComfyEffectiveDefaultsReportTheFamilyKnobs(t *testing.T) {
	conn := EngineConn{Params: map[string]EngineParams{"flux": {Guidance: 4}}}
	if got := comfyEffectiveDefaults(conn, ComfyFamilyFlux1, "flux"); got.Guidance != 4 {
		t.Errorf("flux1 guidance default = %v, want the row's 4", got.Guidance)
	}
	if got := comfyEffectiveDefaults(conn, ComfyFamilyAnima, "anima"); got.Shift != 3 {
		t.Errorf("anima shift default = %v, want the model's own 3", got.Shift)
	}
	if got := comfyEffectiveDefaults(conn, ComfyFamilySDXL, "sdxl"); got.ClipSkip != 2 {
		t.Errorf("sdxl clip skip default = %v, want the encoders' own 2", got.ClipSkip)
	}
}

func TestValidateRequestParamsBoundsTheNewKnobs(t *testing.T) {
	for _, p := range []EngineParams{
		{ClipSkip: -1}, {ClipSkip: comfyMaxClipSkip + 1},
		{Guidance: -1}, {Guidance: paramsMaxGuidance + 1},
		{Shift: -1}, {Shift: comfyMaxShift + 1},
	} {
		if err := validateRequestParams(&p); err == nil {
			t.Errorf("%+v was accepted", p)
		}
	}
	ok := EngineParams{ClipSkip: 2, Guidance: 4.5, Shift: 24}
	if err := validateRequestParams(&ok); err != nil {
		t.Errorf("%+v refused: %v", ok, err)
	}
}

// A knob sent to a family that does not read it is said out loud, and one it does read is not.
func TestComfyWarnsAboutAKnobTheFamilyDoesNotRead(t *testing.T) {
	p := &EngineParams{ClipSkip: 2, Guidance: 4, Shift: 10}
	for family, want := range map[comfyFamily][]string{
		ComfyFamilySDXL:  {"guidance=4", "shift=10"},
		ComfyFamilyFlux1: {"clip_skip=2", "shift=10"},
		ComfyFamilyAnima: {"clip_skip=2", "guidance=4"},
	} {
		got := strings.Join(comfyIgnoredParamWarnings(family, p), "\n")
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: warnings lack %s: %s", family, w, got)
			}
		}
		if n := strings.Count(got, "was not applied"); n != len(want) {
			t.Errorf("%s: %d warnings, want %d: %s", family, n, len(want), got)
		}
	}
}

// The reproduction record reads each knob back off the family that takes it, and not off the
// families whose template fixes a node of the same class (zimage's shift 3).
func TestPropsReadTheFamilyKnobsBack(t *testing.T) {
	read := func(family comfyFamily, params EngineParams) EngineParams {
		t.Helper()
		p := comfyGoldenParams
		p.Params = params
		g, err := comfyBuildGraph(family, comfyFixtureFiles(t, family), p)
		if err != nil {
			t.Fatal(err)
		}
		// Through JSON, as a PNG's embedded graph arrives: numbers come back as float64.
		b, _ := json.Marshal(g)
		var nodes map[string]comfyNode
		if err := json.Unmarshal(b, &nodes); err != nil {
			t.Fatal(err)
		}
		rg := comfyReadGraph{}
		for id, n := range nodes {
			rg[id] = comfyReadNode{Class: n.ClassType, Inputs: n.Inputs}
		}
		out := rg.props()
		if out.Params == nil {
			t.Fatalf("%s: no params read", family)
		}
		return *out.Params
	}
	if got := read(ComfyFamilySDXL, EngineParams{ClipSkip: 2}); got.ClipSkip != 2 {
		t.Errorf("sdxl clip_skip = %d", got.ClipSkip)
	}
	if got := read(ComfyFamilyFlux1, EngineParams{Guidance: 4.5}); got.Guidance != 4.5 {
		t.Errorf("flux1 guidance = %v", got.Guidance)
	}
	if got := read(ComfyFamilyAnima, EngineParams{Shift: 14}); got.Shift != 14 {
		t.Errorf("anima shift = %v", got.Shift)
	}
	if got := read(ComfyFamilyZImage, EngineParams{}); got.Shift != 0 {
		t.Errorf("zimage shift = %v, want none: its template fixes it", got.Shift)
	}
}

// The advice carries the new ranges under the names the Console reads.
func TestFamilyAdviceCarriesGuidanceAndShiftRanges(t *testing.T) {
	if a := familyAdviceFor(string(ComfyFamilyFlux1)); !slices.Equal(a.GuidanceRange, []float64{3.5, 5}) {
		t.Errorf("flux1 guidance range = %v", a.GuidanceRange)
	}
	if a := familyAdviceFor(string(ComfyFamilyAnima)); !slices.Equal(a.ShiftRange, []float64{3, 24}) {
		t.Errorf("anima shift range = %v", a.ShiftRange)
	}
	b, _ := json.Marshal(familyAdviceFor(string(ComfyFamilyFlux1)))
	if !strings.Contains(string(b), `"guidance_range":[3.5,5]`) {
		t.Errorf("advice JSON = %s", b)
	}
}
