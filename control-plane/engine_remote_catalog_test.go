package main

// engine_remote_catalog_test.go — the far deployment's catalogue, mirrored (ADR 0079 decisions 2
// and 7).
//
// No test here can reach another Agent Fleet, and none needs to: what breaks a borrowed engine is
// not the bytes but the four facts the wire does not carry. `kind` is absent (a LoRA is a LoRA
// because of WHICH ARRAY it arrived in), `enabled` is absent (only enabled rows are published),
// the file key is spelled `s3_key` there and `s3Key` in the store, and a role the far side
// switched off is absent from the answer rather than reported empty. Each of them is silent when
// it is wrong: a LoRA read as a model makes `hasModels` answer true, which starts a GPU to run
// nothing.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// --- a far Agent Fleet -----------------------------------------------------------

// farFleet is another deployment's /internal/engine/catalog and nothing else. What it answers is
// swapped between fetches, because every behaviour in this file is about the SECOND fetch:
// a role that went away, a far side that came back, a far side that broke.
type farFleet struct {
	mu     sync.Mutex
	status int
	body   string
	hits   int
	bearer string
	paths  []string
}

func (f *farFleet) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits++
	f.bearer = r.Header.Get("Authorization")
	f.paths = append(f.paths, r.URL.Path)
	status, body := f.status, f.body
	f.mu.Unlock()
	if status != http.StatusOK {
		http.Error(w, `{"error":{"code":"unauthorized","message":"no such membership"}}`, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

// answers replaces what the next fetch reads.
func (f *farFleet) answers(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *farFleet) seen() (int, string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits, f.bearer, append([]string(nil), f.paths...)
}

// newFarFleet stands one up, already answering the given catalogue.
func newFarFleet(t *testing.T, body string) (*farFleet, *httptest.Server) {
	t.Helper()
	f := &farFleet{status: http.StatusOK, body: body}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

// farCatalogBody is what the far Control Plane would publish for these engines.
//
// Built through engineCatalogRowFor — the REAL publisher — rather than hand-written, so that a
// field this deployment renames on the answering side cannot pass here while the mirror on the
// borrowing side goes on reading the old spelling. Both halves are in this repository, and the
// far deployment is this code running somewhere else.
func farCatalogBody(t *testing.T, rows ...map[string]any) string {
	t.Helper()
	for i, row := range rows {
		if row == nil {
			t.Fatalf("engine %d has nothing enabled, so the far side would not publish it at all", i)
		}
	}
	b, err := json.Marshal(map[string]any{"engines": rows})
	if err != nil {
		t.Fatalf("marshalling the far catalogue: %v", err)
	}
	return string(b)
}

// farImageEngine is an image role on the far fleet: one checkpoint with two files, a declared
// recipe and a negative prompt, plus a LoRA — which rides in its own array and is the whole
// reason `kind` has to be put back by the mirror.
func farImageEngine(t *testing.T) map[string]any {
	t.Helper()
	def := engineDef{Key: "image", API: engineAPIImages, Provider: "comfy",
		Service: "af-image", URL: "http://image.far.invalid:8188"}
	models := []store.EngineModel{{
		Role: "image", ID: "sdxl-base-1.0", Kind: "checkpoint", Enabled: true, Selected: true,
		BaseModel: "sdxl", Description: "the far administrator's checkpoint",
		Sizes: []string{"1024x1024", "832x1216"}, NegativePrompt: "blurry, watermark",
		Params: &store.EngineParams{Steps: 30, CFG: 6.5, Sampler: "dpmpp_2m", Scheduler: "karras"},
		Files: []store.EngineModelFile{
			// The far bucket's own layout, which is exactly what travels: only the basename is
			// ever used, because the box mirrors bucket keys onto disk verbatim.
			{Flag: "", S3Key: "image/checkpoints/sd_xl_base_1.0.safetensors", Bytes: 6_900_000_000},
			{Flag: "--vae", S3Key: "image/vae/sdxl_vae.safetensors"},
		},
	}, {
		Role: "image", ID: "detail-slider", Kind: engineModelKindLora, Enabled: true,
		Params: &store.EngineParams{Weight: 0.6},
		Files:  []store.EngineModelFile{{S3Key: "image/loras/detail_slider.safetensors"}},
	}}
	return engineCatalogRowFor(def, models, "sdxl-base-1.0", "text, signature")
}

// farLLMEngine is a chat role on the far fleet, for the per-model window — the one number that
// must not arrive as a zero, because opencode reads a limit of 0 as "no auto-compaction".
func farLLMEngine(t *testing.T) map[string]any {
	t.Helper()
	def := engineDef{Key: "llm", Provider: "llamacpp", Service: "af-llm", URL: "http://llm.far.invalid:8080"}
	models := []store.EngineModel{{
		Role: "llm", ID: "qwen3-30b", Enabled: true, Default: true,
		ContextTokens: 32768, MaxOutputTokens: 4096,
	}}
	return engineCatalogRowFor(def, models, "", "")
}

// --- the borrowing side ----------------------------------------------------------

// newTestRemotes is the operator's declaration, read the way a real process reads it.
func newTestRemotes(t *testing.T, base, keys string) *engineRemotes {
	t.Helper()
	t.Setenv("AF_REMOTE_ENGINE_URL", base)
	t.Setenv("AF_REMOTE_ENGINE_TOKEN", "afei_borrower-membership")
	t.Setenv("AF_REMOTE_ENGINE_KEYS", keys)
	r := newEngineRemotes(nil)
	if r == nil {
		t.Fatal("newEngineRemotes read a complete declaration as nothing borrowed")
	}
	return r
}

// newTestRemoteRegistry is the registry of a borrowing Control Plane before its first catalogue
// fetch: no rows at all, and the builder that lets a row arrive later.
//
// The closure is the `notManagedHere` branch of newEngineRegistry's own build, reduced to what a
// remote row gets. That it is still what the real one does is not assumed here —
// TestBorrowingOnlyCPAdoptsThroughTheRealBuilder drives newEngineRegistry itself.
func newTestRemoteRegistry(rem *engineRemotes) *engineRegistry {
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{}}
	reg.build = func(d engineDef) *engineRuntimeState {
		st := &engineRuntimeState{def: d, catalog: newEngineCatalog(nil, d.Key)}
		if d.remote() {
			st.remote = rem.forKey(d.Key)
			st.catalog.source = st.remote.catalogSource()
		}
		return st
	}
	return reg
}

// mirrorOf reads one borrowed role's rows straight off the mirror, past engineCatalog's
// ten-second cache — for the assertions that are about what was STORED rather than about what a
// request would see.
func mirrorOf(t *testing.T, e *engineRuntimeState) []store.EngineModel {
	t.Helper()
	src := e.remote.catalogSource()
	if src == nil {
		t.Fatal("a borrowed row has no catalogue source: hasModels then answers true for a " +
			"catalogue nothing can read, and serve 404s every named model")
	}
	rows, err := src(context.Background())
	if err != nil {
		t.Fatalf("reading the mirror: %v", err)
	}
	if rows == nil {
		t.Fatal("the mirror is nil rather than an empty list")
	}
	return rows
}

func modelByID(rows []store.EngineModel, id string) (store.EngineModel, bool) {
	for _, m := range rows {
		if m.ID == id {
			return m, true
		}
	}
	return store.EngineModel{}, false
}

// --- decision 7: what the mirror has to put back ---------------------------------

// The round trip, field by field. Two roles arrive in one fetch, the rows land in this
// deployment's own catalogue shape, and the three facts the wire does not carry — `kind`,
// `enabled` and the file key's other spelling — come out right.
func TestRemoteMirrorReadsTheFarCatalogue(t *testing.T) {
	ctx := context.Background()
	_, srv := newFarFleet(t, farCatalogBody(t, farImageEngine(t), farLLMEngine(t)))
	rem := newTestRemotes(t, srv.URL, "")
	reg := newTestRemoteRegistry(rem)

	rem.refreshAll(ctx, reg)

	// Both roles are borrowed, with the api and provider the FAR administrator declared: a
	// guess here posts a ComfyUI graph at an OpenAI-compatible endpoint.
	img, llm := reg.get("image"), reg.get("llm")
	if img == nil || llm == nil {
		t.Fatalf("borrowed rows = %v", reg.byKey)
	}
	for _, c := range []struct {
		e                  *engineRuntimeState
		api, provider, url string
	}{
		{img, engineAPIImages, "comfy", srv.URL},
		{llm, engineAPIChat, "llamacpp", srv.URL},
	} {
		if c.e.def.api() != c.api || c.e.def.Provider != c.provider {
			t.Errorf("%s = %s/%s, want %s/%s", c.e.def.Key, c.e.def.api(), c.e.def.Provider, c.api, c.provider)
		}
		if !c.e.def.remote() || c.e.def.external() {
			t.Errorf("%s lifecycle = %q, want remote (external fails a cold start the far side would have waited out)",
				c.e.def.Key, c.e.def.lifecycle())
		}
		// The far FLEET's URL, not the far engine's: the request goes through its gateway.
		if c.e.def.URL != c.url {
			t.Errorf("%s url = %q, want %q", c.e.def.Key, c.e.def.URL, c.url)
		}
	}

	// The rows themselves, read through the row's own catalogue — the reader every request uses.
	rows := img.catalog.list(ctx)
	if len(rows) != 2 {
		t.Fatalf("image rows = %+v, want the checkpoint and the LoRA", rows)
	}
	ckpt, ok := modelByID(rows, "sdxl-base-1.0")
	if !ok {
		t.Fatalf("no checkpoint in %+v", rows)
	}
	if ckpt.Role != "image" {
		t.Errorf("role = %q, want the LOCAL key the row is served under", ckpt.Role)
	}
	if !ckpt.Enabled {
		t.Error("enabled = false: the far side publishes only enabled rows, so a mirrored row is " +
			"enabled by construction and this one would never be offered")
	}
	if engineModelIsLora(ckpt) {
		t.Errorf("kind = %q, want a model's empty kind", ckpt.Kind)
	}
	if !ckpt.Selected || ckpt.Default {
		t.Errorf("selected/default = %v/%v, want true/false", ckpt.Selected, ckpt.Default)
	}
	if ckpt.BaseModel != "sdxl" || ckpt.NegativePrompt != "blurry, watermark" ||
		ckpt.Description != "the far administrator's checkpoint" {
		t.Errorf("declarations lost: %+v", ckpt)
	}
	if !reflect.DeepEqual(ckpt.Sizes, []string{"1024x1024", "832x1216"}) {
		t.Errorf("sizes = %v", ckpt.Sizes)
	}
	if want := (&store.EngineParams{Steps: 30, CFG: 6.5, Sampler: "dpmpp_2m", Scheduler: "karras"}); !reflect.DeepEqual(ckpt.Params, want) {
		t.Errorf("params = %+v, want %+v (the provider reads this field by field over its family's recipe)", ckpt.Params, want)
	}
	// 🔴 `s3_key` on the wire, `s3Key` in the store: the two spellings are why the wire has its
	// own type. The far key travels verbatim, because only its basename is ever used.
	want := []store.EngineModelFile{
		{S3Key: "image/checkpoints/sd_xl_base_1.0.safetensors"},
		{Flag: "--vae", S3Key: "image/vae/sdxl_vae.safetensors"},
	}
	if !reflect.DeepEqual(ckpt.Files, want) {
		t.Errorf("files = %+v, want %+v", ckpt.Files, want)
	}

	// The LoRA, which is one only because of the array it arrived in.
	lora, ok := modelByID(rows, "detail-slider")
	if !ok {
		t.Fatalf("no LoRA in %+v", rows)
	}
	if !engineModelIsLora(lora) {
		t.Errorf("kind = %q, want %q — a borrowed LoRA read as a model enters the launch menu",
			lora.Kind, engineModelKindLora)
	}
	if !lora.Enabled || lora.Params == nil || lora.Params.Weight != 0.6 {
		t.Errorf("lora = %+v, want it enabled and carrying its recommended weight", lora)
	}
	if ids := img.modelIDs(ctx); !reflect.DeepEqual(ids, []string{"comfy/sdxl-base-1.0"}) {
		t.Errorf("model ids = %v, want the checkpoint alone", ids)
	}

	// The window rides per model on the llm role, and it is absent rather than zero when the far
	// side never declared one.
	llmRows := llm.catalog.list(ctx)
	if len(llmRows) != 1 || llmRows[0].ContextTokens != 32768 || llmRows[0].MaxOutputTokens != 4096 {
		t.Errorf("llm rows = %+v, want one row with the declared window", llmRows)
	}
	if !llmRows[0].Default {
		t.Error("default = false: a request that names no model would then be refused")
	}
	if ckpt.ContextTokens != 0 || ckpt.MaxOutputTokens != 0 {
		t.Errorf("the image row invented a window: %d/%d", ckpt.ContextTokens, ckpt.MaxOutputTokens)
	}

	// Warmth is the far deployment's own observation, read rather than probed (decision 10):
	// asking would be the health call decision 5 refuses, from the admin panel, on every load.
	if !img.warm(ctx) {
		t.Error("the image row is cold although the far catalogue flagged its checkpoint warm")
	}
	if llm.warm(ctx) {
		t.Error("the llm row is warm although the far catalogue flagged nothing")
	}

	// And the credential: the issuing token, at the far side's own catalogue route.
	hits, bearer, paths := srv.Config.Handler.(*farFleet).seen()
	if hits != 1 || len(paths) != 1 || paths[0] != "/internal/engine/catalog" {
		t.Errorf("far fleet saw %d request(s) at %v", hits, paths)
	}
	if bearer != "Bearer afei_borrower-membership" {
		t.Errorf("authorization = %q", bearer)
	}
}

// A catalogue with nothing but LoRAs starts nothing. This is the trap in its pure form: read the
// `loras` array as models and hasModels answers true, which is a $1.26/hour box woken up to serve
// an accessory no request can name.
func TestRemoteMirrorLorasAreNotModels(t *testing.T) {
	ctx := context.Background()
	_, srv := newFarFleet(t, `{"engines":[{"key":"image","api":"images","provider":"comfy",
	 "base_url":"/engine/image/v1","models":["detail-slider"],"model_rows":[],
	 "loras":[{"id":"detail-slider","files":[{"flag":"","s3_key":"image/loras/detail_slider.safetensors"}]}]}]}`)
	rem := newTestRemotes(t, srv.URL, "")
	reg := newTestRemoteRegistry(rem)

	rem.refreshAll(ctx, reg)

	e := reg.get("image")
	if e == nil {
		t.Fatalf("rows = %v", reg.byKey)
	}
	rows := mirrorOf(t, e)
	if len(rows) != 1 || !engineModelIsLora(rows[0]) {
		t.Fatalf("rows = %+v, want one LoRA", rows)
	}
	if rows[0].Files[0].S3Key != "image/loras/detail_slider.safetensors" {
		t.Errorf("file = %+v, want the wire's own `s3_key` spelling read", rows[0].Files[0])
	}
	if e.catalog.hasModels(ctx) {
		t.Error("hasModels = true for a catalogue of LoRAs alone — the far engine would be woken to run nothing")
	}
	if ids := e.modelIDs(ctx); len(ids) != 0 {
		t.Errorf("model ids = %v, want none: a LoRA is not something a launch menu can offer", ids)
	}
}

// --- decision 2: the row appears when the far fleet answers -----------------------

// The far fleet is down when this process starts, which is the ordinary case for a laptop that
// boots before its VPN. Nothing is borrowed and nothing panics; the row arrives on the first
// fetch that succeeds, through the same adopt() the table reloader uses.
func TestRemoteRowIsAdoptedOnceTheFarFleetAnswers(t *testing.T) {
	ctx := context.Background()
	far, srv := newFarFleet(t, "")
	far.answers(http.StatusBadGateway, "")
	rem := newTestRemotes(t, srv.URL, "")
	reg := newTestRemoteRegistry(rem)

	var buf bytes.Buffer
	done := captureLog(&buf)
	rem.refreshAll(ctx, reg)
	done()
	if len(reg.byKey) != 0 {
		t.Fatalf("a failed fetch produced rows: %v", reg.byKey)
	}
	if !strings.Contains(buf.String(), srv.URL) {
		t.Errorf("the failure was logged as %q, want the far URL an operator can go and look at", buf.String())
	}

	far.answers(http.StatusOK, farCatalogBody(t, farImageEngine(t)))
	rem.refreshAll(ctx, reg)

	e := reg.get("image")
	if e == nil {
		t.Fatalf("the row did not arrive on the fetch that succeeded: %v", reg.byKey)
	}
	if e.catalog.source == nil {
		t.Fatal("the adopted row reads the database rather than the mirror")
	}
	if len(e.catalog.list(ctx)) != 2 {
		t.Errorf("rows = %+v", e.catalog.list(ctx))
	}
	// Adopted rows are built by the registry's own builder — an engine assembled anywhere else
	// would be missing whatever the rest of this process assumes a row has.
	if reg.build == nil {
		t.Error("reg.build is nil, so adopt() answers false and a borrowing CP never gets a row")
	}
}

// The same thing on the real lane: a Control Plane whose entire engine declaration is the two
// remote variables. Four gates used to stop it — the registry gate, the empty-table gate, the
// builder that was attached only with AWS, and a poller that needed SSM — and falling through
// any of them leaves `/engine/…` unrouted.
func TestBorrowingOnlyCPAdoptsThroughTheRealBuilder(t *testing.T) {
	ctx := context.Background()
	_, srv := newFarFleet(t, farCatalogBody(t, farImageEngine(t)))
	t.Setenv("AF_ENGINES_SSM_PARAM", "")
	t.Setenv("AF_ENGINES_JSON", "")
	t.Setenv("AF_COMFY_URL", "")
	t.Setenv("AF_REMOTE_ENGINE_URL", srv.URL)
	t.Setenv("AF_REMOTE_ENGINE_TOKEN", "afei_borrower-membership")
	t.Setenv("AF_REMOTE_ENGINE_KEYS", "image")

	reg := newEngineRegistry(ctx, nil)
	if reg == nil {
		t.Fatal("a CP that declares nothing but AF_REMOTE_ENGINE_URL got no registry at all, " +
			"so registerEngineRoutes would not even route /engine/…")
	}
	// The poll's first tick is immediate rather than an interval away, so that a launch menu is
	// not empty for ten minutes after a restart. It runs on its own goroutine; this waits for it.
	e := waitForBorrowedRow(t, reg, "image")
	if !e.def.remote() || e.def.Provider != "comfy" {
		t.Errorf("def = %+v, want the far declaration", e.def)
	}
	if e.remote == nil || e.catalog.source == nil {
		t.Fatal("the real builder gave the remote row no mirror to read")
	}
	if got := e.modelIDs(ctx); !reflect.DeepEqual(got, []string{"comfy/sdxl-base-1.0"}) {
		t.Errorf("model ids = %v, want the borrowed checkpoint", got)
	}
	if !e.def.notManagedHere() {
		t.Error("the borrowed row would be given an ECS service to move a desired count on")
	}
}

// waitForBorrowedRow waits for the catalogue poll's first tick to adopt a role.
func waitForBorrowedRow(t *testing.T, reg *engineRegistry, key string) *engineRuntimeState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if e := reg.get(key); e != nil {
			return e
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s row five seconds after the registry was built", key)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- decision 7: what a second fetch may and may not change -----------------------

// A role the far administrator switches off DISAPPEARS from its catalogue rather than being
// reported empty. The mirror has to follow it down to an empty list — and to an empty list
// rather than to nothing, because a catalogue that cannot be read at all answers "there are
// models" and then 404s every one of them.
func TestRemoteMirrorEmptiesWhenTheFarRoleIsSwitchedOff(t *testing.T) {
	ctx := context.Background()
	far, srv := newFarFleet(t, farCatalogBody(t, farImageEngine(t)))
	rem := newTestRemotes(t, srv.URL, "")
	reg := newTestRemoteRegistry(rem)

	rem.refreshAll(ctx, reg)
	e := reg.get("image")
	if e == nil || len(mirrorOf(t, e)) != 2 {
		t.Fatalf("the first fetch did not mirror the role: %v", reg.byKey)
	}
	// A request arrives while the role is still offered, which leaves the row's ten-second cache
	// warm — the state the switch-off has to get past rather than wait out.
	if len(e.catalog.list(ctx)) != 2 {
		t.Fatalf("the row's catalogue answers %+v", e.catalog.list(ctx))
	}

	far.answers(http.StatusOK, `{"engines":[]}`)
	rem.refreshAll(ctx, reg)

	// The row stays. Removing a live row is not something the registry supports, and it does not
	// need to: an empty catalogue is already refused with `engine_unavailable`.
	if reg.get("image") != e {
		t.Fatalf("the row was taken out of the registry: %v", reg.byKey)
	}
	if rows := mirrorOf(t, e); len(rows) != 0 {
		t.Errorf("mirror = %+v, want no models", rows)
	}
	// Through the reader a request uses, which means the ten-second cache was dropped as well.
	if rows := e.catalog.list(ctx); rows == nil || len(rows) != 0 {
		t.Errorf("the row's catalogue answers %+v, want an empty list within the same second", rows)
	}
	if e.catalog.hasModels(ctx) {
		t.Error("hasModels = true for a role the far fleet switched off")
	}
	if e.warm(ctx) {
		t.Error("the row is still warm although the far fleet no longer offers it")
	}
}

// A fetch that fails is not an answer of "no models". It has to keep the previous one: the false
// direction takes a borrowed engine out of the launch menu and answers 503 to whoever was using
// it, over a blip.
func TestRemoteMirrorSurvivesAFailedFetch(t *testing.T) {
	ctx := context.Background()
	far, srv := newFarFleet(t, farCatalogBody(t, farImageEngine(t)))
	rem := newTestRemotes(t, srv.URL, "")
	reg := newTestRemoteRegistry(rem)
	rem.refreshAll(ctx, reg)
	e := reg.get("image")
	if e == nil {
		t.Fatalf("rows = %v", reg.byKey)
	}

	for _, broken := range []struct {
		status int
		body   string
	}{
		{http.StatusUnauthorized, ""},                   // the borrowing membership was removed
		{http.StatusOK, "<html>a proxy said no</html>"}, // something that is not a catalogue
	} {
		far.answers(broken.status, broken.body)
		rem.refreshAll(ctx, reg)
		if rows := mirrorOf(t, e); len(rows) != 2 {
			t.Fatalf("after %d the mirror is %+v, want the previous answer kept", broken.status, rows)
		}
		if !e.warm(ctx) {
			t.Errorf("after %d the row went cold", broken.status)
		}
	}
}

// 🔴 A key a local row already holds is not borrowed. The registry is one map keyed by role, so
// replacing a managed row would leave its ECS service running with nobody to stop it — the
// direction engineTableWithEnvRow already takes for AF_COMFY_URL.
func TestRemoteCatalogueDoesNotReplaceALocalRow(t *testing.T) {
	ctx := context.Background()
	_, srv := newFarFleet(t, farCatalogBody(t, farImageEngine(t), farLLMEngine(t)))
	rem := newTestRemotes(t, srv.URL, "")
	reg := newTestRemoteRegistry(rem)
	local := &engineRuntimeState{
		def: engineDef{Key: "image", API: engineAPIImages, Provider: "sdcpp",
			Service: "af-image", URL: "http://image.af.internal:8080"},
		catalog: newEngineCatalog(nil, "image"),
	}
	reg.byKey["image"] = local

	var buf bytes.Buffer
	done := captureLog(&buf)
	rem.refreshAll(ctx, reg)
	done()

	if reg.get("image") != local {
		t.Fatal("the borrowed row replaced the managed one, which leaves its ECS service with nobody to stop it")
	}
	if local.def.remote() || local.def.Service != "af-image" || local.def.Provider != "sdcpp" {
		t.Errorf("the local row was rewritten: %+v", local.def)
	}
	if local.catalog.source != nil {
		t.Error("the local row's catalogue was pointed at the far fleet's mirror")
	}
	// Not even mirrored: nothing reads it, and a mirror kept for a role somebody else serves is
	// a second answer to "what does this engine offer".
	if rows, _ := rem.forKey("image").catalogSource()(ctx); len(rows) != 0 {
		t.Errorf("a mirror was kept for the local role: %+v", rows)
	}
	if !strings.Contains(buf.String(), "already served by a managed row") {
		t.Errorf("log = %q, want a line naming why the far declaration was refused", buf.String())
	}
	// The other role is unaffected — one conflict does not cost the rest of the catalogue.
	if reg.get("llm") == nil {
		t.Error("the llm role was not borrowed")
	}
}

// `api` and `provider` are the far administrator's to state and are never guessed here: an image
// role is `comfy` on one deployment and `sdcpp` on another, and the Workspace composes a
// completely different request for each. A row without an `api` is a wrong URL answering with
// JSON, or a role this process already borrows that has gone away — never a new engine.
func TestRemoteCatalogueRefusesARowWithNoAPI(t *testing.T) {
	ctx := context.Background()
	_, srv := newFarFleet(t, `{"engines":[{"key":"image","models":["x"],"model_rows":[{"id":"x"}]},{"models":["y"]}]}`)
	rem := newTestRemotes(t, srv.URL, "")
	reg := newTestRemoteRegistry(rem)

	rem.refreshAll(ctx, reg)

	if len(reg.byKey) != 0 {
		t.Errorf("rows = %v, want nothing taken on from a declaration that names no api", reg.byKey)
	}
}

// AF_REMOTE_ENGINE_KEYS is the operator's filter, and a role left out of it is neither borrowed
// nor mirrored.
func TestRemoteCatalogueBorrowsOnlyTheDeclaredKeys(t *testing.T) {
	ctx := context.Background()
	_, srv := newFarFleet(t, farCatalogBody(t, farImageEngine(t), farLLMEngine(t)))
	rem := newTestRemotes(t, srv.URL, " image ")
	reg := newTestRemoteRegistry(rem)

	rem.refreshAll(ctx, reg)

	if reg.get("image") == nil {
		t.Error("the declared role was not borrowed")
	}
	if e := reg.get("llm"); e != nil {
		t.Errorf("llm = %+v, want a role the operator did not ask for left alone", e.def)
	}
}
