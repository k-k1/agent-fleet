package imagegen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func TestChooseImageProviders(t *testing.T) {
	caps := func(id string) Caps {
		switch id {
		case "codex":
			return Caps{Ops: []Op{OpGenerate, OpEdit}}
		case "bedrock":
			return Caps{Ops: []Op{OpGenerate, OpInpaint}}
		}
		return Caps{}
	}
	order := []string{"codex", "bedrock"}
	for _, tc := range []struct {
		name, pref string
		op         Op
		ready      map[string]bool
		want       []string
	}{
		// auto returns EVERY usable provider in order, not just the first: readiness is
		// checked before the call and exhaustion only shows up during it.
		{name: "auto lists every ready, capable provider", op: OpGenerate,
			ready: map[string]bool{"codex": true, "bedrock": true}, want: []string{"codex", "bedrock"}},
		{name: "auto skips an unready provider", op: OpGenerate,
			ready: map[string]bool{"bedrock": true}, want: []string{"bedrock"}},
		{name: "auto skips one that cannot do the op", op: OpInpaint,
			ready: map[string]bool{"codex": true, "bedrock": true}, want: []string{"bedrock"}},
		{name: "nothing can serve it", op: OpUpscale,
			ready: map[string]bool{"codex": true, "bedrock": true}},
		{name: "empty pref means auto", pref: "", op: OpGenerate,
			ready: map[string]bool{"codex": true}, want: []string{"codex"}},
		// An explicit choice is honoured even when unready, and NEVER falls through to
		// another: silently billing a different account for the picture is the failure this
		// prevents.
		{name: "an explicit choice is honoured while unready", pref: "bedrock", op: OpGenerate,
			ready: map[string]bool{"codex": true}, want: []string{"bedrock"}},
		{name: "an explicit choice never falls through", pref: "codex", op: OpGenerate,
			ready: map[string]bool{"codex": true, "bedrock": true}, want: []string{"codex"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseImageProviders(tc.pref, Request{Op: tc.op}, order, tc.ready, caps)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("chooseImageProviders = %v, want %v", got, tc.want)
			}
		})
	}
}

// The stored preference is normalized into a TOTAL order. A list written before a provider
// existed must still rank it, or adding a provider would make it unreachable until the user
// happened to re-save their settings.
func TestEffectiveOrder(t *testing.T) {
	oldOrder, oldPref := providerOrder, ProviderOrderPref
	providerOrder = []string{"codex", "bedrock", "sd"}
	t.Cleanup(func() { providerOrder, ProviderOrderPref = oldOrder, oldPref })

	for _, tc := range []struct {
		name string
		pref []string
		want []string
	}{
		{name: "no preference at all", want: []string{"codex", "bedrock", "sd"}},
		{name: "a full reordering", pref: []string{"sd", "bedrock", "codex"}, want: []string{"sd", "bedrock", "codex"}},
		// The two that matter: a partial list still ranks the rest, and junk cannot make a
		// provider vanish.
		{name: "a partial list appends the rest", pref: []string{"sd"}, want: []string{"sd", "codex", "bedrock"}},
		{name: "unknown ids and dupes are dropped", pref: []string{"nope", "sd", "sd", ""},
			want: []string{"sd", "codex", "bedrock"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ProviderOrderPref = func() []string { return tc.pref }
			if got := effectiveOrder(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("effectiveOrder = %v, want %v", got, tc.want)
			}
		})
	}
}

// What a provider could not honour is reported, never hidden (ADR 0069 decision 7).
func TestRequestWarnings(t *testing.T) {
	res := Result{Images: []Image{{Width: 1254, Height: 1254}}}
	none := Caps{}
	got := requestWarnings(Request{Size: "1024x1024", Count: 1}, res, none)
	if len(got) != 1 || got[0] != "size=1024x1024 requested, 1254x1254 produced" {
		t.Fatalf("warnings = %v", got)
	}
	if got := requestWarnings(Request{Size: "auto", Count: 2}, res, none); len(got) != 1 ||
		got[0] != "count=2 requested, 1 produced" {
		t.Fatalf("warnings = %v, want the count one only", got)
	}
	if got := requestWarnings(Request{Size: "1254x1254", Count: 1}, res, none); len(got) != 0 {
		t.Fatalf("warnings = %v, want none when the request was honoured", got)
	}
	// An aspect ratio asked of a route that has none is invisible in the produced dimensions —
	// nothing else would ever tell the caller it was dropped.
	if got := requestWarnings(Request{AspectRatio: "16:9", Count: 1}, res, none); len(got) != 1 ||
		got[0] != "aspect_ratio=16:9 requested, but this route cannot choose an aspect ratio" {
		t.Fatalf("warnings = %v, want the aspect-ratio one", got)
	}
	// ...and a route that DOES offer ratios says for itself what it did with one, so the core
	// must stay quiet rather than warn twice.
	withRatios := Caps{AspectRatios: []string{"1:1", "16:9"}}
	if got := requestWarnings(Request{AspectRatio: "16:9", Count: 1}, res, withRatios); len(got) != 0 {
		t.Fatalf("warnings = %v, want none from the core", got)
	}
	// A LoRA is the same shape of silent loss (ADR 0072 phase P3): the picture that comes back
	// without it looks perfectly fine, so nothing but this says it was dropped.
	loraReq := Request{Count: 1, Size: "1254x1254", Loras: []LoraRef{{Name: "watercolor-v2"}}}
	if got := requestWarnings(loraReq, res, none); len(got) != 1 ||
		got[0] != "loras=watercolor-v2 requested, but this route cannot apply a LoRA" {
		t.Fatalf("warnings = %v, want the lora one", got)
	}
	// A route that can apply them refuses an unusable pairing itself, so the core stays quiet.
	withLoras := Caps{Loras: []LoraInfo{{Name: "watercolor-v2", BaseModel: "sdxl"}}}
	if got := requestWarnings(loraReq, res, withLoras); len(got) != 0 {
		t.Fatalf("warnings = %v, want none from the core", got)
	}
	// A dropped seed is the most invisible loss of the three: the picture is fine, and the caller
	// only finds out on the SECOND call, which is the whole reason they pinned one.
	seed := int64(1234)
	seedReq := Request{Count: 1, Size: "1254x1254", Seed: &seed}
	if got := requestWarnings(seedReq, res, none); len(got) != 1 ||
		!strings.Contains(got[0], "seed=1234 requested") {
		t.Fatalf("warnings = %v, want the seed one", got)
	}
	if got := requestWarnings(seedReq, res, Caps{Seed: true}); len(got) != 0 {
		t.Fatalf("warnings = %v, want none from a route that pins it", got)
	}
	// Seed 0 warns as loudly as any other, which it would not if the check read the value
	// instead of whether one was given.
	zero := int64(0)
	if got := requestWarnings(Request{Count: 1, Size: "1254x1254", Seed: &zero}, res, none); len(got) != 1 {
		t.Fatalf("warnings = %v, want seed 0 reported too", got)
	}
}

// --- the codex route ----------------------------------------------------------------------

// tinyPNG is a real 2x3 PNG, so DecodeConfig has something truthful to read.
func tinyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeCodex writes a stand-in `codex` that emits the JSONL stream and drops files where the
// real CLI's image_gen does. The real thing is not driven from a test: one run costs a real
// image against the user's ChatGPT plan.
func fakeCodex(t *testing.T, home string, files map[string][]byte, stream string, exit int) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	var body bytes.Buffer
	body.WriteString("#!/bin/sh\ncat > /dev/null\n")
	for name, data := range files {
		full := filepath.Join(home, "generated_images", name)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(dir, filepath.Base(name)+".src")
		if err := os.WriteFile(src, data, 0o600); err != nil {
			t.Fatal(err)
		}
		// Written by the script, not by the test: the collector's whole job is to notice
		// files that appeared DURING the run.
		body.WriteString("mkdir -p '" + filepath.Dir(full) + "'\n")
		body.WriteString("cp '" + src + "' '" + full + "'\n")
	}
	for _, ln := range bytes.Split([]byte(stream), []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		body.WriteString("cat <<'AFEOF'\n" + string(ln) + "\nAFEOF\n")
	}
	body.WriteString("exit " + strconv.Itoa(exit) + "\n")
	if err := os.WriteFile(script, body.Bytes(), 0o700); err != nil {
		t.Fatal(err)
	}
	return script
}

const codexHappyStream = `{"type":"thread.started","thread_id":"th-1"}
{"type":"item.completed","item":{"type":"agent_message","text":"done"}}
{"type":"turn.completed","usage":{"input_tokens":60000,"cached_input_tokens":24000,"output_tokens":300}}`

func TestCodexCollectsByDirectoryDiff(t *testing.T) {
	home := t.TempDir()
	// A file already sitting in the thread directory is NOT this run's output. Without the
	// pre-run snapshot a repeated thread id would hand this session someone else's image.
	stale := filepath.Join(home, "generated_images", "th-1", "old.png")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, tinyPNG(t, 8, 8), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &codexProvider{model: "m", home: home}
	p.exe = fakeCodex(t, home, map[string][]byte{"th-1/call_1.png": tinyPNG(t, 12, 7)}, codexHappyStream, 0)

	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a cat", Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 1 {
		t.Fatalf("images = %d, want 1 (the stale file must not be collected)", len(res.Images))
	}
	if res.Images[0].Width != 12 || res.Images[0].Height != 7 {
		t.Fatalf("dimensions = %dx%d, want 12x7 (read from the file, not from the request)",
			res.Images[0].Width, res.Images[0].Height)
	}
	if res.Images[0].MIME != "image/png" || len(res.Images[0].Bytes) == 0 {
		t.Fatalf("image = %+v", res.Images[0])
	}
	// The tokens are the driver turn's, exact; the plan quota the image consumed is not
	// expressible in them and is deliberately absent.
	if !res.Usage.Measured || res.Usage.In != 36000 || res.Usage.CacheRead != 24000 || res.Usage.Out != 300 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	// The source copy is removed: unswept, this directory reached 80 MB at 2-3 MB an image on
	// the container this was designed in.
	if _, err := os.Stat(filepath.Join(home, "generated_images", "th-1", "call_1.png")); !os.IsNotExist(err) {
		t.Fatal("the collected source file was left behind in CODEX_HOME")
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatal("a file this run did not produce was deleted")
	}
}

// The honest failure the defensive shape exists to produce: prose is not evidence of a file.
// Upstream has an open report of a session finding image_gen unavailable and scripting a
// placeholder PNG instead; under -s read-only it cannot, and this collector would not take it.
func TestCodexFailsWhenNoFileAppeared(t *testing.T) {
	home := t.TempDir()
	p := &codexProvider{model: "m", home: home}
	p.exe = fakeCodex(t, home, nil,
		`{"type":"thread.started","thread_id":"th-1"}
{"type":"item.completed","item":{"type":"agent_message","text":"I saved the image to /tmp/out.png"}}
{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`, 0)

	if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a cat"}); err == nil {
		t.Fatal("a run that produced no file was reported as a success")
	}
}

func TestCodexSurfacesTurnFailure(t *testing.T) {
	home := t.TempDir()
	p := &codexProvider{model: "m", home: home}
	p.exe = fakeCodex(t, home, nil,
		`{"type":"thread.started","thread_id":"th-1"}
{"type":"turn.failed","error":{"message":"rate limited"}}`, 0)

	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a cat"})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("rate limited")) {
		t.Fatalf("err = %v, want codex's own reason", err)
	}
}

// Without a thread id there is no directory that is provably ours, and picking the newest file
// anywhere under the root would hand this session another session's image.
func TestCodexRefusesWithoutThreadID(t *testing.T) {
	home := t.TempDir()
	p := &codexProvider{model: "m", home: home}
	p.exe = fakeCodex(t, home, map[string][]byte{"th-1/call_1.png": tinyPNG(t, 4, 4)},
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`, 0)

	if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a cat"}); err == nil {
		t.Fatal("a run with no thread id collected a file anyway")
	}
}

func TestCodexRefusesUnsupportedOps(t *testing.T) {
	p := &codexProvider{model: "m", home: t.TempDir(), exe: "/bin/false"}
	if _, err := p.Generate(context.Background(), Request{Op: OpInpaint, Prompt: "x"}); err == nil {
		t.Fatal("inpaint was accepted on a route that cannot do it")
	}
	if _, err := p.Generate(context.Background(), Request{Op: OpEdit, Prompt: "x", Mask: "/tmp/m.png"}); err == nil {
		t.Fatal("a mask was accepted on a route with no mask input")
	}
}

// Caps must not claim a size the route cannot honour: measured twice, both runs produced
// 1254x1254 whatever was asked for.
func TestCodexCapsAdvertiseNoSizes(t *testing.T) {
	c := (&codexProvider{}).Caps("")
	if len(c.Sizes) != 0 || len(c.Backgrounds) != 0 {
		t.Fatalf("caps = %+v, want no size/background choice on the codex route", c)
	}
	if !c.Supports(OpGenerate) || c.Supports(OpInpaint) {
		t.Fatalf("ops = %v", c.Ops)
	}
}

// --- store and retention ------------------------------------------------------------------

func TestStoreImagesWritesUnderTheSessionDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	files, err := storeImages("sid-1", []Image{
		{Bytes: tinyPNG(t, 3, 4), MIME: "image/png", Width: 3, Height: 4},
		{Bytes: tinyPNG(t, 5, 6), MIME: "image/png", Width: 5, Height: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name == files[1].Name {
		t.Fatalf("files = %+v", files)
	}
	for _, f := range files {
		if filepath.Dir(f.Path) != GeneratedDir("sid-1") {
			t.Fatalf("path = %s, want it under %s", f.Path, GeneratedDir("sid-1"))
		}
		info, err := os.Stat(f.Path)
		if err != nil || info.Size() != f.Bytes {
			t.Fatalf("stat %s: %v (size %d, reported %d)", f.Path, err, info.Size(), f.Bytes)
		}
		// No leftover temp file: a reader watching the directory must never see a half-written
		// PNG, which is why the write is tmp+rename.
		if filepath.Base(f.Path)[0] == '.' {
			t.Fatalf("name = %s", f.Name)
		}
	}
}

func TestSweepGeneratedDropsOnlyExpired(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sid-1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old, fresh := filepath.Join(dir, "old.png"), filepath.Join(dir, "fresh.png")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, tinyPNG(t, 2, 2), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	sweepGeneratedNow(root, time.Now().Add(-generatedTTL))
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("an expired image survived the sweep")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("a fresh image was swept")
	}
	// The session directory stays while anything is in it, and goes when nothing is.
	if err := os.Remove(fresh); err != nil {
		t.Fatal(err)
	}
	sweepGeneratedNow(root, time.Now().Add(-generatedTTL))
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("an empty session directory was left behind")
	}
}

// --- the core ------------------------------------------------------------------------------

type stubProvider struct {
	id       string
	res      Result
	err      error
	notReady bool
	calls    *[]string
	// caps overrides the default generate-only capability, for the tests that are about what a
	// provider ADVERTISES rather than what it produces.
	caps *Caps
	// gotReq captures the Request the core handed over, for the tests that are about what
	// survives the wire between the tool and the provider.
	gotReq *Request
}

func (s stubProvider) ID() string { return s.id }
func (s stubProvider) Caps(string) Caps {
	if s.caps != nil {
		return *s.caps
	}
	return Caps{Ops: []Op{OpGenerate}}
}
func (s stubProvider) Ready(context.Context) bool { return !s.notReady }
func (s stubProvider) Generate(_ context.Context, req Request) (Result, error) {
	if s.calls != nil {
		*s.calls = append(*s.calls, s.id)
	}
	if s.gotReq != nil {
		*s.gotReq = req
	}
	return s.res, s.err
}

func withStubProvider(t *testing.T, ps ...Provider) {
	t.Helper()
	oldProviders, oldOrder, oldPref := Providers, providerOrder, ProviderOrderPref
	order := make([]string, 0, len(ps))
	for _, p := range ps {
		order = append(order, p.ID())
	}
	Providers = func() []Provider { return ps }
	providerOrder = order
	ProviderOrderPref = nil
	t.Cleanup(func() { Providers, providerOrder, ProviderOrderPref = oldProviders, oldOrder, oldPref })
}

// The case the whole ordering exists for: the first provider says it is ready and then fails
// anyway (the plan ran out between the check and the call). auto moves on, and BOTH attempts
// are in the ledger — the failed one burned tokens and hiding it would understate the cost.
func TestRunFallsThroughToTheNextProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_USAGE_DIR", filepath.Join(home, "usage"))
	var calls []string
	withStubProvider(t,
		stubProvider{id: ProviderCodex, calls: &calls,
			res: Result{Provider: ProviderCodex, Usage: Usage{In: 40, Measured: true}},
			err: errors.New("out of quota")},
		stubProvider{id: "sd", calls: &calls, res: Result{Provider: "sd",
			Images: []Image{{Bytes: tinyPNG(t, 4, 4), MIME: "image/png", Width: 4, Height: 4}}}},
	)

	out, err := Run(context.Background(), Job{Session: "slot01", SID: "sid-1",
		Request: Request{Op: OpGenerate, Prompt: "a cat"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Provider != "sd" {
		t.Fatalf("provider = %q, want the fallback to have produced it", out.Provider)
	}
	if want := []string{ProviderCodex, "sd"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	rows := usagex.ReadRows()
	if len(rows) != 2 || rows[0].OK || !rows[1].OK {
		t.Fatalf("ledger rows = %+v, want a failed codex row then a successful sd row", rows)
	}
	if rows[0].Kind != "codex" || rows[0].In != 40 {
		t.Fatalf("the failed attempt was not recorded honestly: %+v", rows[0])
	}
}

// An explicit choice must never be quietly served by someone else — that is a different
// account being billed for the picture.
func TestRunDoesNotFallThroughOnAnExplicitChoice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_USAGE_DIR", filepath.Join(home, "usage"))
	var calls []string
	withStubProvider(t,
		stubProvider{id: ProviderCodex, calls: &calls, res: Result{Provider: ProviderCodex},
			err: errors.New("out of quota")},
		stubProvider{id: "sd", calls: &calls, res: Result{Provider: "sd",
			Images: []Image{{Bytes: tinyPNG(t, 4, 4), MIME: "image/png"}}}},
	)

	if _, err := Run(context.Background(), Job{Session: "slot01", SID: "sid-1", Pref: ProviderCodex,
		Request: Request{Op: OpGenerate, Prompt: "a cat"}}); err == nil {
		t.Fatal("an explicit codex request was served by another provider")
	}
	if want := []string{ProviderCodex}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want only the named provider", calls)
	}
}

// When everything fails the caller gets EVERY reason: "codex is out of quota, and the local
// engine is not running" is actionable in a way that either half alone is not.
func TestRunReportsEveryFailedAttempt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_USAGE_DIR", filepath.Join(home, "usage"))
	withStubProvider(t,
		stubProvider{id: ProviderCodex, res: Result{Provider: ProviderCodex}, err: errors.New("out of quota")},
		stubProvider{id: "sd", res: Result{Provider: "sd"}, err: errors.New("engine not running")},
	)
	_, err := Run(context.Background(), Job{Session: "slot01", SID: "s",
		Request: Request{Op: OpGenerate, Prompt: "a cat"}})
	if err == nil {
		t.Fatal("every provider failed and Run reported success")
	}
	for _, want := range []string{"out of quota", "engine not running", "codex", "sd"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to mention %q", err, want)
		}
	}
}

// A provider that is not ready is never called at all — that is what keeps auto off a route
// whose plan is already exhausted.
func TestRunSkipsAnUnreadyProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_USAGE_DIR", filepath.Join(home, "usage"))
	var calls []string
	withStubProvider(t,
		stubProvider{id: ProviderCodex, calls: &calls, notReady: true},
		stubProvider{id: "sd", calls: &calls, res: Result{Provider: "sd",
			Images: []Image{{Bytes: tinyPNG(t, 4, 4), MIME: "image/png"}}}},
	)
	if _, err := Run(context.Background(), Job{Session: "slot01", SID: "sid-1",
		Request: Request{Op: OpGenerate, Prompt: "a cat"}}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"sd"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want the unready provider skipped entirely", calls)
	}
}

// The ledger row for one generation (ADR 0069 decision 9): images and pixels counted, the
// plan quota left unmeasured rather than zero-filled.
func TestRunRecordsUsageWithImagesAndPixels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_USAGE_DIR", filepath.Join(home, "usage"))
	withStubProvider(t, stubProvider{id: ProviderCodex, res: Result{
		Provider: ProviderCodex, Model: "gpt-5.4-mini",
		Images: []Image{{Bytes: tinyPNG(t, 10, 20), MIME: "image/png", Width: 10, Height: 20}},
		Usage:  Usage{In: 100, Out: 5, Measured: true},
	}})

	out, err := Run(context.Background(), Job{Session: "slot01", SID: "sid-1",
		Request: Request{Op: OpGenerate, Prompt: "a cat", Size: "1024x1024"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Files) != 1 || out.Provider != ProviderCodex {
		t.Fatalf("stored = %+v", out)
	}
	// The core adds the comparison the provider could not make.
	if len(out.Warnings) != 1 {
		t.Fatalf("warnings = %v, want the size mismatch", out.Warnings)
	}

	rows := usagex.ReadRows()
	if len(rows) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Feature != usagex.FeatureToolImagegen || r.Ref != "slot01" || r.Kind != "codex" {
		t.Fatalf("row = %+v", r)
	}
	if r.Images != 1 || r.Pixels != 200 {
		t.Fatalf("images/pixels = %d/%d, want 1/200", r.Images, r.Pixels)
	}
	// partial, not exact: the driver tokens are exact but the plan quota the image consumed
	// is not in them, and a row read as the whole consumption would understate it.
	if r.Measured != usagex.MeasuredPartial || !r.OK {
		t.Fatalf("measured = %q ok = %v", r.Measured, r.OK)
	}
	if r.CostUSD != 0 {
		t.Fatalf("cost = %v, want no cost claim on this route", r.CostUSD)
	}
}

// A failed generation still burned the driver turn, so the row has to exist.
func TestRunRecordsFailedGeneration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_USAGE_DIR", filepath.Join(home, "usage"))
	withStubProvider(t, stubProvider{id: ProviderCodex,
		res: Result{Provider: ProviderCodex, Usage: Usage{In: 40, Measured: true}},
		err: errors.New("codex generated no image")})

	if _, err := Run(context.Background(), Job{Session: "slot01", SID: "sid-1",
		Request: Request{Prompt: "a cat"}}); err == nil {
		t.Fatal("a provider error was reported as a success")
	}
	rows := usagex.ReadRows()
	if len(rows) != 1 || rows[0].OK || rows[0].In != 40 {
		t.Fatalf("rows = %+v, want one ok:false row with the tokens it burned", rows)
	}
}

// A request refused before the provider did any work still leaves a row, and that row must
// still say WHICH route was going to run it — the provider never stamped its id on a Result
// it did not produce, so the kind column came out empty until Run passed its own choice in.
func TestRunRecordsTheChosenProviderOnAnEarlyRefusal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_USAGE_DIR", filepath.Join(home, "usage"))
	withStubProvider(t, stubProvider{id: ProviderCodex, err: errors.New("a mask needs a provider that inpaints")})

	if _, err := Run(context.Background(), Job{Session: "slot01", SID: "s",
		Request: Request{Op: OpGenerate, Prompt: "a cat", Mask: "/tmp/m.png"}}); err == nil {
		t.Fatal("the refusal was reported as a success")
	}
	rows := usagex.ReadRows()
	if len(rows) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(rows))
	}
	if rows[0].Kind != "codex" {
		t.Fatalf("kind = %q, want codex — an empty kind hides which plan the row belongs to", rows[0].Kind)
	}
}

func TestRunRefusesWhenNothingCanServeIt(t *testing.T) {
	withStubProvider(t, stubProvider{id: ProviderCodex})
	_, err := Run(context.Background(), Job{Session: "slot01", SID: "s",
		Request: Request{Op: OpInpaint, Prompt: "a cat"}})
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("err = %v, want ErrNoProvider", err)
	}
}

// The status endpoint must never say "ready" while the gate is off, or the MCP server would
// advertise a tool whose every call is refused.
func TestHandleStatusReportsTheGate(t *testing.T) {
	oldEnabled := Enabled
	t.Cleanup(func() { Enabled = oldEnabled })
	Enabled = func() bool { return false }

	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/imagegen/status?session=slot01", nil))
	var st struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Enabled {
		t.Fatal("status reported enabled with the preference off")
	}

	// And the generate route refuses outright, rather than trusting the MCP surface to be the
	// only caller: the Agent REST is reachable by anything holding AGENT_TOKEN.
	rec = httptest.NewRecorder()
	body := strings.NewReader(`{"session":"slot01","prompt":"a cat"}`)
	req := httptest.NewRequest(http.MethodPost, "/imagegen/generate", body)
	req.Header.Set("Content-Type", "application/json")
	HandleGenerate(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("generate with the gate off = %d, want 403", rec.Code)
	}
}

// A fall-through moves the bill onto a different account, and the built-in order exists to
// decide exactly that. Measured on a live deployment (ADR 0071 P1): the self-hosted engine
// failed on a cold start, `auto` produced the image on a member's Antigravity plan, and the
// only trace was `provider: agy` — which is what a deliberate choice looks like too.
func TestAutoSaysWhenItFellThroughToAnotherAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_USAGE_DIR", filepath.Join(home, "usage"))
	withStubProvider(t,
		stubProvider{id: ProviderSdcpp, err: errors.New("the engine did not come up")},
		stubProvider{id: ProviderAgy, res: Result{Provider: ProviderAgy,
			Images: []Image{{Bytes: tinyPNG(t, 4, 4), MIME: "image/png", Width: 4, Height: 4}}}},
	)

	out, err := Run(context.Background(), Job{Session: "slot01", SID: "sid-1",
		Request: Request{Op: OpGenerate, Prompt: "a cat"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Provider != ProviderAgy {
		t.Fatalf("provider = %q, want the fall-through to have produced it", out.Provider)
	}
	var got string
	for _, w := range out.Warnings {
		if strings.Contains(w, "fell back") {
			got = w
		}
	}
	if got == "" {
		t.Fatalf("warnings = %v — a fall-through onto another account said nothing", out.Warnings)
	}
	if !strings.Contains(got, ProviderSdcpp) || !strings.Contains(got, "did not come up") {
		t.Errorf("warning = %q, want it to name what failed and why", got)
	}
}

// The mirror. Without it the check above would pass on a build that warns unconditionally,
// and every ordinary generation would carry a warning about a failure that never happened.
func TestAutoIsSilentWhenTheFirstProviderServed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_USAGE_DIR", filepath.Join(home, "usage"))
	withStubProvider(t,
		stubProvider{id: ProviderSdcpp, res: Result{Provider: ProviderSdcpp,
			Images: []Image{{Bytes: tinyPNG(t, 4, 4), MIME: "image/png", Width: 4, Height: 4}}}},
		stubProvider{id: ProviderAgy},
	)

	out, err := Run(context.Background(), Job{Session: "slot01", SID: "sid-1",
		Request: Request{Op: OpGenerate, Prompt: "a cat"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range out.Warnings {
		if strings.Contains(w, "fell back") {
			t.Errorf("warned about a fall-through that did not happen: %q", w)
		}
	}
}
