package main

// engine_remote_test.go — borrowing another deployment's engines: the lifecycle predicate, the
// four registry gates, and the refusals (ADR 0079 decisions 1, 2, 7, 10).
//
// What these pin is not "borrowing works" — the far half is another deployment — but the places
// this fleet used to assume that "not an ECS service" and "nobody can start it" are the same
// sentence. ADR 0079's review counted nine such places; a miss in any of them fails a borrowed row
// immediately, and eight of the nine are silent.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// --- decision 1: two predicates, not one --------------------------------------

// The split itself. `external` answers "nobody starts it" and is the one that produces an
// immediate failure; `notManagedHere` answers "the vessel is somebody else's" and is what almost
// every branch wants. A remote row must answer the second and NOT the first.
func TestEngineLifecyclePredicatesSplit(t *testing.T) {
	for _, c := range []struct {
		lifecycle                        string
		external, remote, notManagedHere bool
	}{
		{"", false, false, false},
		{"external", true, false, true},
		{"remote", false, true, true},
		// Declared, folded, and tolerant of the spelling an operator types — but never inferred.
		{"  REMOTE  ", false, true, true},
		{"External", true, false, true},
		// Anything else is this deployment's own row, which is the safe direction: a typo must not
		// turn into "somebody else runs this" (ADR 0053).
		{"remot", false, false, false},
		{"managed", false, false, false},
	} {
		d := engineDef{Lifecycle: c.lifecycle}
		if got := d.external(); got != c.external {
			t.Errorf("%q.external() = %v, want %v", c.lifecycle, got, c.external)
		}
		if got := d.remote(); got != c.remote {
			t.Errorf("%q.remote() = %v, want %v", c.lifecycle, got, c.remote)
		}
		if got := d.notManagedHere(); got != c.notManagedHere {
			t.Errorf("%q.notManagedHere() = %v, want %v", c.lifecycle, got, c.notManagedHere)
		}
	}
	// 🔴 The one that matters most, stated on its own because it is the whole reason the predicate
	// was split: a remote row answering external() would inherit ADR 0076 decision 4 and fail
	// every cold start, since ensureStarted's refusal is the "nobody starts it" answer.
	if (engineDef{Lifecycle: engineLifecycleRemote}).external() {
		t.Fatal("a remote row answers external() — it would fail every borrowed cold start")
	}
}

// A `service` is what a desired count moves on, so neither lifecycle that is not this
// deployment's owes one. A hand-written remote table row is not P0's own path (they are
// synthesised from the far catalogue), but the field exists and somebody will write it.
func TestParseEngineTableAllowsARemoteRowWithoutAService(t *testing.T) {
	raw := `{"engines":[{"key":"llm","api":"chat","provider":"llamacpp","lifecycle":"remote",
	 "url":"https://af.example.com"}]}`
	tab, err := parseEngineTable(raw)
	if err != nil {
		t.Fatalf("a remote row without a service was refused: %v", err)
	}
	if len(tab.Engines) != 1 || !tab.Engines[0].remote() {
		t.Fatalf("table = %+v", tab.Engines)
	}
	// And it still needs the two things nothing can invent.
	if _, err := parseEngineTable(`{"engines":[{"lifecycle":"remote","url":"https://x"}]}`); err == nil {
		t.Error("a remote row with no key was accepted")
	}
}

// Borrowing needs no AWS at all: that is the whole point of the lane for a native or docker
// deployment, and loading a config for it would be asking for credentials this host has none of.
func TestEngineTableNeedsNoAWSForARemoteRow(t *testing.T) {
	tab := engineTable{Engines: []engineDef{{Key: "llm", URL: "https://af.example.com", Lifecycle: "remote"}}}
	if engineTableNeedsAWS(tab) {
		t.Error("a remote-only table asked for AWS")
	}
	tab.Engines = append(tab.Engines, engineDef{Key: "image", Service: "svc", URL: "http://x"})
	if !engineTableNeedsAWS(tab) {
		t.Error("a table with a managed row did not ask for AWS")
	}
}

// --- decision 2: the operator declares where and with what --------------------

func TestNewEngineRemotesReadsTheDeclaration(t *testing.T) {
	// Nothing declared is the normal case, and it has to stay exactly nil: every call site in
	// engines.go relies on the nil-safety rather than guarding.
	t.Setenv("AF_REMOTE_ENGINE_URL", "")
	t.Setenv("AF_REMOTE_ENGINE_TOKEN", "")
	if newEngineRemotes(nil) != nil {
		t.Fatal("borrowing was configured from an empty environment")
	}
	// Half a declaration is an operator error, not a partial feature: a URL with no credential
	// could only ever be refused 401.
	t.Setenv("AF_REMOTE_ENGINE_URL", "https://af.example.com")
	if newEngineRemotes(nil) != nil {
		t.Fatal("a URL with no token produced a borrowing config")
	}
	t.Setenv("AF_REMOTE_ENGINE_TOKEN", "afei_x.y")
	t.Setenv("AF_REMOTE_ENGINE_KEYS", " image , llm ")
	r := newEngineRemotes(nil)
	if r == nil {
		t.Fatal("a complete declaration produced no borrowing config")
	}
	// The trailing slash goes: every path this joins begins with one, and //engine/... is a
	// different URL to a proxy that normalises.
	t.Setenv("AF_REMOTE_ENGINE_URL", "https://af.example.com/")
	if got := newEngineRemotes(nil).base; got != "https://af.example.com" {
		t.Errorf("base = %q, want the trailing slash gone", got)
	}
	for key, want := range map[string]bool{"image": true, "llm": true, "LLM": true, "tts": false} {
		if got := r.wants(key); got != want {
			t.Errorf("wants(%q) = %v, want %v", key, got, want)
		}
	}
	// An empty filter borrows whatever the far fleet offers, which is the answer for an operator
	// who does not want to maintain a second list.
	t.Setenv("AF_REMOTE_ENGINE_KEYS", "")
	if !newEngineRemotes(nil).wants("anything") {
		t.Error("an empty AF_REMOTE_ENGINE_KEYS did not borrow every role")
	}
}

// 🔴 The gates. A CP that declares nothing but the two borrowing variables used to fall out of
// newEngineRegistry with no registry object at all — and registerEngineRoutes then skips
// exemptPrefix and all three gateway handlers, so `/engine/…` would not even be routed. Rows
// arrive on the first catalogue fetch (decision 2), so there is nothing in the table at boot and
// the old `len(table.Engines) == 0 && !haveAWS` return caught exactly this deployment.
func TestEngineRegistryFromBorrowingAloneWiresNoAWS(t *testing.T) {
	t.Setenv("AF_ENGINES_SSM_PARAM", "")
	t.Setenv("AF_ENGINES_JSON", "")
	t.Setenv("AF_COMFY_URL", "")
	t.Setenv("AF_REMOTE_ENGINE_URL", "https://af.example.invalid")
	t.Setenv("AF_REMOTE_ENGINE_TOKEN", "afei_x.y")

	reg := newEngineRegistry(context.Background(), nil)
	if reg == nil {
		t.Fatal("borrowing alone produced no registry — the gate still demands a table or AWS")
	}
	// No rows yet, and that is the truth rather than a failure: the far fleet has not answered.
	if len(reg.list()) != 0 {
		t.Errorf("rows at boot = %d, want 0 (they arrive with the first catalogue fetch)", len(reg.list()))
	}
	// 🔴 And the builder is attached, which is what makes adopt possible at all. Without this the
	// poll finds a role, calls adopt, and gets false for ever.
	if reg.build == nil {
		t.Fatal("reg.build is nil on the no-AWS lane — adopt can never take a borrowed row on")
	}
	// Proof that the two together actually produce a served row, which is the property the gates
	// exist for. (What the far catalogue is converted into is engine_remote_catalog_test.go's.)
	if !reg.adopt(engineDef{Key: "llm", API: engineAPIChat, Provider: "llamacpp",
		URL: "https://af.example.invalid", Lifecycle: engineLifecycleRemote}) {
		t.Fatal("adopt refused a borrowed row on a registry built from borrowing alone")
	}
	e := reg.get("llm")
	if e == nil {
		t.Fatal("the adopted row is not served")
	}
	if e.remote == nil {
		t.Error("the adopted row has no remote handle — its credential and mirror have nowhere to live")
	}
	// Everything a row this deployment does not own must NOT get (decision 1). The list is the
	// same one ADR 0076 wrote for an external row.
	for _, c := range []struct {
		what string
		set  bool
	}{
		{"ecs", e.ecs != nil},
		{"controller", e.ctrl != nil},
		{"demand", e.demand != nil},
		{"pending", e.pending != nil},
		{"fleet", e.fleet != nil},
		{"activeParam", e.activeParam != ""},
	} {
		if c.set {
			t.Errorf("a borrowed row was given a %s", c.what)
		}
	}
	// The catalogue is there, and it reads through the mirror rather than the database.
	if e.catalog == nil || e.catalog.source == nil {
		t.Error("a borrowed row's catalogue does not read through the mirror")
	}
}

// --- decision 7: the mirror is a SOURCE, and never nil ------------------------

// 🔴 hasModels answers TRUE for a catalogue it cannot read at all — deliberately, since the false
// direction stops a GPU and refuses every request. So a borrowed row whose mirror were merely
// absent would pass serve's no-models gate and then 404 every named model. An empty list is the
// honest "nothing enabled", which serve refuses with a sentence that says so.
func TestEngineCatalogReadsThroughItsSource(t *testing.T) {
	rows := []store.EngineModel{{Role: "llm", ID: "m1", Enabled: true}}
	calls := 0
	c := newEngineCatalog(nil, "llm")
	c.source = func(context.Context) ([]store.EngineModel, error) {
		calls++
		return rows, nil
	}
	got := c.list(t.Context())
	if len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("list() = %+v, want the mirrored row", got)
	}
	if !c.hasModels(t.Context()) {
		t.Error("hasModels() = false for a mirror that holds an enabled model")
	}
	// Cached like the database read is: the gateway consults this on every request.
	c.list(t.Context())
	if calls != 1 {
		t.Errorf("source calls = %d, want 1 (the TTL did not hold)", calls)
	}

	// An EMPTY mirror is a definite answer and must read as one.
	empty := newEngineCatalog(nil, "llm")
	empty.source = func(context.Context) ([]store.EngineModel, error) { return []store.EngineModel{}, nil }
	if empty.hasModels(t.Context()) {
		t.Error("hasModels() = true for an empty mirror — serve would wake an engine that holds nothing")
	}
	// And a row with NEITHER a store nor a source is the "cannot read at all" case, which stays
	// true. This is the shape a nil source would produce, and why the source must never be nil.
	if !newEngineCatalog(nil, "llm").hasModels(t.Context()) {
		t.Error("hasModels() = false with no source at all — the false direction stops a GPU")
	}
}

// A failed read keeps the previous answer, for the same reason the database path does: a
// transient error must not read as "no models", which means "do not start this engine".
func TestEngineCatalogSourceFailureKeepsThePreviousAnswer(t *testing.T) {
	rows := []store.EngineModel{{Role: "llm", ID: "m1", Enabled: true}}
	fail := false
	c := newEngineCatalog(nil, "llm")
	c.source = func(context.Context) ([]store.EngineModel, error) {
		if fail {
			return nil, context.DeadlineExceeded
		}
		return rows, nil
	}
	c.list(t.Context())
	fail = true
	c.invalidate()
	if got := c.list(t.Context()); len(got) != 1 {
		t.Fatalf("after a failed refresh list() = %+v, want the previous answer kept", got)
	}
}

// --- decision 10: warm is read, never probed ---------------------------------

// 🔴 The site ADR 0079's review found broken. Probing here would be the health call decision 5
// refuses — fired from the admin panel on every load, and on a row whose URL contains the engine
// path it buys a GPU box — and answering false would report every borrowed engine cold for ever.
func TestBorrowedWarmComesFromTheMirror(t *testing.T) {
	rem := &engineRemote{key: "image", tokens: map[string]engineRemoteToken{}}
	e := &engineRuntimeState{
		def:    engineDef{Key: "image", URL: "https://af.example.invalid", Lifecycle: engineLifecycleRemote},
		remote: rem,
	}
	// Nothing observed yet: false, and no request made. The URL is unroutable, so a probe would
	// show up as a multi-second test rather than a wrong answer.
	if e.warm(t.Context()) {
		t.Error("warm() = true before the far side reported anything")
	}
	rem.mu.Lock()
	rem.warmModel = "sdxl"
	rem.mu.Unlock()
	if !e.warm(t.Context()) {
		t.Error("warm() = false while the far catalogue reports a warm model")
	}
	// And the far administrator's exclusion list is read from the same place rather than from a
	// local setting nobody over there can see.
	rem.setNegativeAlways("  watermark  ")
	if got := e.negativeAlways(t.Context()); got != "watermark" {
		t.Errorf("negativeAlways() = %q, want the far side's list", got)
	}
}

// The admin row says whose engine this is, and it says it from the ROW rather than from the
// constant this branch used to write. A remote row that announced itself as `external` would
// leave the Console's "another fleet" label nothing to branch on.
func TestBorrowedAdminRowStatesItsOwnLifecycle(t *testing.T) {
	st := testSettingsStore(t)
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{}}
	e := &engineRuntimeState{
		def:      engineDef{Key: "image", API: engineAPIImages, Provider: "comfy", URL: "https://af.example.invalid", Lifecycle: engineLifecycleRemote},
		settings: st,
		catalog:  newEngineCatalog(nil, "image"),
		remote:   &engineRemote{key: "image", tokens: map[string]engineRemoteToken{}},
	}
	e.catalog.source = e.remote.catalogSource()
	reg.byKey["image"] = e
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	row := a.row(t.Context(), e)
	if row["lifecycle"] != engineLifecycleRemote {
		t.Errorf("lifecycle = %v, want %q", row["lifecycle"], engineLifecycleRemote)
	}
	if row["managed"] != false {
		t.Errorf("managed = %v, want false", row["managed"])
	}
	if row["url"] != e.def.URL {
		t.Errorf("url = %v, want the far fleet's base", row["url"])
	}
	// Omitted rather than zeroed: none of these is this deployment's to claim about somebody
	// else's box (ADR 0076 decision 5, kept by ADR 0079 decision 10).
	for _, k := range []string{"state", "desired", "box", "stop_eta", "idle_secs", "window_secs"} {
		if _, ok := row[k]; ok {
			t.Errorf("the borrowed row carries %q = %v", k, row[k])
		}
	}
}

// 🔴 The refusal decision 7 actually needs. The draft put it on a store implementation handed to
// the row's catalogue, and the review found that such an implementation refuses NOTHING: no write
// travels through a catalogue at all — every one addresses mgr.store directly, keyed by the role
// in the request path. So it lives on the routes.
func TestBorrowedCatalogueWritesAreRefused(t *testing.T) {
	st := testSettingsStore(t)
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{}}
	rem := &engineRemote{key: "image", tokens: map[string]engineRemoteToken{}}
	e := &engineRuntimeState{
		def:      engineDef{Key: "image", API: engineAPIImages, Provider: "comfy", URL: "https://af.example.invalid", Lifecycle: engineLifecycleRemote},
		settings: st,
		catalog:  newEngineCatalog(nil, "image"),
		remote:   rem,
	}
	e.catalog.source = rem.catalogSource()
	reg.byKey["image"] = e
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	ident := store.Identity{ID: "u1"}

	for _, c := range []struct {
		what string
		call func(w http.ResponseWriter, r *http.Request)
		req  *http.Request
	}{
		{"putModel", func(w http.ResponseWriter, r *http.Request) { a.putModel(w, r, ident) },
			httptest.NewRequest(http.MethodPut, "/api/admin/engines/image/models/m1", strings.NewReader(`{"enabled":true}`))},
		{"postModel", func(w http.ResponseWriter, r *http.Request) { a.postModel(w, r, ident) },
			httptest.NewRequest(http.MethodPost, "/api/admin/engines/image/models", strings.NewReader(`{"id":"m1"}`))},
		{"deleteModel", func(w http.ResponseWriter, r *http.Request) { a.deleteModel(w, r, ident) },
			httptest.NewRequest(http.MethodDelete, "/api/admin/engines/image/models/m1", nil)},
		{"putNegative", func(w http.ResponseWriter, r *http.Request) { a.putNegative(w, r, ident) },
			httptest.NewRequest(http.MethodPut, "/api/admin/engines/image/negative", strings.NewReader(`{"negative":"x"}`))},
		{"postIngest", func(w http.ResponseWriter, r *http.Request) {
			a.postIngest(w, r, engineIngestGrant{})
		},
			httptest.NewRequest(http.MethodPost, "/api/admin/engines/image/ingest", strings.NewReader(`{}`))},
	} {
		rec := httptest.NewRecorder()
		c.req.SetPathValue("key", "image")
		c.req.SetPathValue("id", "m1")
		c.call(rec, c.req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s on a borrowed engine = %d, want 400", c.what, rec.Code)
			continue
		}
		var out struct {
			Error struct {
				Code, Message string
			} `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if out.Error.Code != errCodeEngineNotOurs {
			t.Errorf("%s code = %q, want %q", c.what, out.Error.Code, errCodeEngineNotOurs)
		}
		// The refusal has to say WHERE to go instead: the operator's next act is over there, and a
		// message that does not name the deployment is a dead end.
		if !strings.Contains(out.Error.Message, e.def.URL) {
			t.Errorf("%s message does not name the far deployment: %q", c.what, out.Error.Message)
		}
	}
	// `ondemand` is refused for the same reason it is on an external row: `off` here closes the
	// route, it does not stop somebody else's box.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/admin/engines/image", strings.NewReader(`{"mode":"ondemand"}`))
	req.SetPathValue("key", "image")
	a.put(rec, req, ident)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("ondemand on a borrowed engine = %d, want 400", rec.Code)
	}
}

// 🔴 A `lifecycle:"remote"` row with no borrowing declaration is the quiet version of the failure
// decision 7 exists to prevent, and it was found by the session that wrote the mirror: with no
// handle there is no catalogue source, and a catalogue that cannot be read at all answers hasModels
// TRUE — so the row passes serve's no-models gate and then 404s `model_unknown` on every request
// that names a model. Refusing the row is louder and correct.
func TestARemoteRowWithNothingToBorrowFromIsNotServed(t *testing.T) {
	t.Setenv("AF_ENGINES_SSM_PARAM", "")
	t.Setenv("AF_COMFY_URL", "")
	t.Setenv("AF_REMOTE_ENGINE_URL", "")
	t.Setenv("AF_REMOTE_ENGINE_TOKEN", "")
	t.Setenv("AF_ENGINES_JSON", `{"engines":[
	 {"key":"llm","api":"chat","provider":"llamacpp","lifecycle":"remote","url":"https://af.example.invalid"},
	 {"key":"image","api":"images","provider":"comfy","lifecycle":"external","url":"http://192.0.2.20:8188"}]}`)

	reg := newEngineRegistry(context.Background(), nil)
	if reg == nil {
		t.Fatal("the table produced no registry at all")
	}
	if e := reg.get("llm"); e != nil {
		t.Errorf("the borrowed row is served with no far deployment to borrow from: %+v", e.def)
	}
	// The external row beside it is untouched: one unusable row must not take the other with it.
	if reg.get("image") == nil {
		t.Error("the external row went away with the unusable remote one")
	}
}

// The second lock on the same failure: even handed a nil handle, the source answers an empty list
// rather than nil, so hasModels reports a definite "nothing enabled" instead of "cannot read".
func TestABorrowedCatalogSourceIsNeverNil(t *testing.T) {
	var none *engineRemote
	src := none.catalogSource()
	if src == nil {
		t.Fatal("catalogSource() on a nil handle returned nil — the row would read the local database")
	}
	rows, err := src(t.Context())
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("source() = (%v, %v), want an empty non-nil list", rows, err)
	}
	c := newEngineCatalog(nil, "llm")
	c.source = src
	if c.hasModels(t.Context()) {
		t.Error("hasModels() = true through a nil handle — serve's no-models gate would be passed")
	}
}
