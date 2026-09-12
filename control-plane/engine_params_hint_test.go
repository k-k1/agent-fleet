package main

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The shapes model authors actually write. The first is Civitai's house style — a table of
// "Label: value" lines inside HTML — and the second is the same information in a sentence,
// which is just as common and which a line-based parser would miss.
func TestParamsReadTheWaysAuthorsWriteThem(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want store.EngineParams
	}{
		{
			name: "civitai table",
			in: engineStripHTML(`<h3>Recommended settings</h3><p>Steps: 30</p>` +
				`<p>CFG Scale: 4.5</p><p>Sampler: DPM++ 2M Karras</p><p>Clip skip: 2</p>`),
			want: store.EngineParams{Steps: 30, CFG: 4.5, Sampler: "dpmpp_2m", Scheduler: "karras", ClipSkip: 2},
		},
		{
			name: "a sentence",
			in:   "I get the best results at 28 steps with cfg 7 and euler a.",
			want: store.EngineParams{Steps: 28, CFG: 7},
		},
		{
			name: "scheduler on its own line",
			in:   "steps: 8\nscheduler: sgm_uniform\nsampler: res_multistep",
			want: store.EngineParams{Steps: 8, Sampler: "res_multistep", Scheduler: "sgm_uniform"},
		},
		{
			name: "a lora's strength",
			in:   "Use at weight 0.8 for the best likeness.",
			want: store.EngineParams{Weight: 0.8},
		},
		{
			name: "nothing to find",
			in:   "A photoreal merge. Have fun, and please credit me if you post the results.",
			want: store.EngineParams{},
		},
	} {
		got := engineParamsFromText(tc.in)
		if got.Params != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got.Params, tc.want)
		}
	}
}

// 🔴 The trap this whole design is arranged around. These patterns run over ordinary prose, and
// "trained for 25000 steps" is a TRAINING step count — a plausible, wrong sampler setting that
// would quietly become what every request runs at. Out of range is dropped, never clamped: 150
// steps is a slow picture, 25000 is a GPU hour.
func TestParamsDropNumbersThatCannotBeSettings(t *testing.T) {
	for _, in := range []string{
		"Trained for 25000 steps on 4xA100.",
		"cfg 99",
		"clip skip 9",
		"at weight 45 strength",
	} {
		if got := engineParamsFromText(in); !got.empty() {
			t.Errorf("%q produced %+v, want nothing", in, got.Params)
		}
	}
	// The positive control: the same sentences with values inside the range DO produce
	// settings, so the test above cannot be passing because the patterns never match.
	for _, tc := range []struct {
		in   string
		want store.EngineParams
	}{
		{"Trained for 25 steps on 4xA100.", store.EngineParams{Steps: 25}},
		{"cfg 9", store.EngineParams{CFG: 9}},
		{"clip skip 2", store.EngineParams{ClipSkip: 2}},
		{"at weight 0.45 strength", store.EngineParams{Weight: 0.45}},
	} {
		if got := engineParamsFromText(tc.in); got.Params != tc.want {
			t.Errorf("%q produced %+v, want %+v", tc.in, got.Params, tc.want)
		}
	}
}

// The quote is what makes the numbers judgeable rather than trusted: it is the author's own
// sentence, and it goes on screen beside the fields the ingest form filled in.
func TestParamsCarryTheSentenceTheyCameFrom(t *testing.T) {
	got := engineParamsFromText(engineStripHTML(
		`<p>Nice model.</p><p>Recommended: Steps 30, CFG 4, DPM++ 2M Karras.</p>`))
	if got.Quote == "" {
		t.Fatal("no quote — the numbers would be on screen with nothing to judge them by")
	}
	if !strings.Contains(got.Quote, "Steps 30") {
		t.Errorf("quote = %q, want the line the numbers were in", got.Quote)
	}
	// 🔴 One LINE, not the whole document. Civitai descriptions run to 12 KB of HTML, and a
	// quote that long is not a quote.
	if len([]rune(got.Quote)) > 201 {
		t.Errorf("quote is %d runes long", len([]rune(got.Quote)))
	}
}

// engineStripHTML has one job beyond removing tags: turning block boundaries into line breaks,
// so that "one line" means something in a document that is entirely `<p>` elements.
func TestStripHTMLKeepsTheLineStructure(t *testing.T) {
	out := engineStripHTML(`<p>Steps: 30</p><p>CFG: 4</p>`)
	if !strings.Contains(out, "Steps: 30") || !strings.Contains(out, "CFG: 4") {
		t.Fatalf("stripped = %q", out)
	}
	if strings.Contains(out, "Steps: 30CFG") {
		t.Errorf("paragraphs ran together: %q", out)
	}
	if strings.Contains(out, "<") || strings.Contains(out, ">") {
		t.Errorf("markup survived: %q", out)
	}
	if got := engineStripHTML("A &amp; B&nbsp;C"); got != "A & B C" {
		t.Errorf("entities = %q", got)
	}
}

// engineParamsClean is the gate between a client and the catalogue. It DROPS the fields it
// cannot stand behind and keeps the rest, because failing the whole call would lose four good
// fields over one bad one.
func TestParamsCleanBoundsWhatReachesTheCatalogue(t *testing.T) {
	if got := engineParamsClean(nil); got != nil {
		t.Errorf("clean(nil) = %+v, want nil", got)
	}
	// A body that says nothing must not become a row that claims to have an opinion.
	if got := engineParamsClean(&store.EngineParams{}); got != nil {
		t.Errorf("clean(empty) = %+v, want nil", got)
	}
	got := engineParamsClean(&store.EngineParams{Steps: 9000, CFG: 4, Sampler: "DPM++ 2M Karras"})
	if got == nil {
		t.Fatal("clean dropped everything over one out-of-range field")
	}
	if got.Steps != 0 {
		t.Errorf("steps = %d, want dropped", got.Steps)
	}
	if got.CFG != 4 {
		t.Errorf("cfg = %v, want kept", got.CFG)
	}
	// A display name is translated into the two things ComfyUI actually enumerates.
	if got.Sampler != "dpmpp_2m" || got.Scheduler != "karras" {
		t.Errorf("sampler/scheduler = %q/%q, want dpmpp_2m/karras", got.Sampler, got.Scheduler)
	}
	// 🔴 And a name that already looks like one of ComfyUI's own passes THROUGH. Its sampler
	// list is longer than any table written here and grows with the engine, so an operator who
	// typed a newer name must not have it silently deleted.
	pass := engineParamsClean(&store.EngineParams{Sampler: "dpmpp_2m_sde_gpu"})
	if pass == nil || pass.Sampler != "dpmpp_2m_sde_gpu" {
		t.Errorf("clean(dpmpp_2m_sde_gpu) = %+v, want it kept", pass)
	}
	// What a person typed in the scheduler field wins over what the sampler phrase implied.
	both := engineParamsClean(&store.EngineParams{Sampler: "DPM++ 2M Karras", Scheduler: "simple"})
	if both == nil || both.Scheduler != "simple" {
		t.Errorf("explicit scheduler = %+v, want simple", both)
	}
}
