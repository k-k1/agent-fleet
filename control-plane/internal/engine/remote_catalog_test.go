package engine

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// doerFunc answers the far side's calls from memory.
type doerFunc func(req *http.Request) (*http.Response, error)

func (f doerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

// catalogDoer answers the catalogue route with whatever *body holds at the time of the call.
func catalogDoer(body *string) doerFunc {
	return func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/internal/engine/catalog" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(*body))}, nil
	}
}

// fakeHost is the registry side of borrowing: it serves what it adopted, and records what the
// cluster asked of it.
type fakeHost struct {
	served      map[string]Def
	adopted     []Def
	notified    []string
	invalidated int
	refuseAdopt bool
}

func (h *fakeHost) Served(key string) (Def, func(), bool) {
	d, ok := h.served[key]
	return d, func() { h.invalidated++ }, ok
}

func (h *fakeHost) Adopt(d Def) bool {
	if h.refuseAdopt {
		return false
	}
	if h.served == nil {
		h.served = map[string]Def{}
	}
	h.served[d.Key] = d
	h.adopted = append(h.adopted, d)
	return true
}

func (h *fakeHost) NotifyCatalogChanged(_ context.Context, key string) {
	h.notified = append(h.notified, key)
}

// The signature is sorted because the far side owes this deployment no order. A catalogue whose
// rows arrive shuffled would otherwise fingerprint differently on every tick, and every tick
// would POST to every running workspace.
func TestMirrorSignatureIgnoresRowOrder(t *testing.T) {
	a := store.EngineModel{Role: "image", ID: "anima", Enabled: true}
	b := store.EngineModel{Role: "image", ID: "krea2", Enabled: true}
	if mirrorSignature([]store.EngineModel{a, b}) != mirrorSignature([]store.EngineModel{b, a}) {
		t.Error("the same two rows in the other order fingerprint differently")
	}
	c := b
	c.Selected = true
	if mirrorSignature([]store.EngineModel{a, b}) == mirrorSignature([]store.EngineModel{a, c}) {
		t.Error("which checkpoint is the default fingerprints the same either way")
	}
}

// A row with a recipe carries its Params as a pointer, and every fetch allocates a new one. The
// signature must compare what it points at, or every tick of a borrowed image engine would POST
// to every running workspace.
func TestMirrorSignatureComparesParamsByValue(t *testing.T) {
	a := store.EngineModel{Role: "image", ID: "anima", Params: &store.EngineParams{Steps: 30}}
	b := store.EngineModel{Role: "image", ID: "anima", Params: &store.EngineParams{Steps: 30}}
	if mirrorSignature([]store.EngineModel{a}) != mirrorSignature([]store.EngineModel{b}) {
		t.Error("equal recipes in different allocations fingerprint differently")
	}
	b.Params.Steps = 20
	if mirrorSignature([]store.EngineModel{a}) == mirrorSignature([]store.EngineModel{b}) {
		t.Error("a changed recipe fingerprints the same")
	}
	none := store.EngineModel{Role: "image", ID: "anima"}
	if mirrorSignature([]store.EngineModel{a}) == mirrorSignature([]store.EngineModel{none}) {
		t.Error("a withdrawn recipe fingerprints the same")
	}
}

// setMirror's answer is what decides whether every running workspace is told. The first mirror
// is never a change (the adoption that follows tells), a re-read of the same answer is not one,
// and neither is the warm model moving: it is not part of the signature.
func TestSetMirrorReportsOnlyRealMovement(t *testing.T) {
	r := NewRemote("image")
	row := remoteCatalogRow{Key: "image", Models: []remoteCatalogModel{{ID: "anima", Warm: true}, {ID: "krea2"}}}
	if r.setMirror(row) {
		t.Error("the first mirror reported a change")
	}
	if r.setMirror(row) {
		t.Error("the same answer twice reported a change")
	}
	otherWarm := remoteCatalogRow{Key: "image", Models: []remoteCatalogModel{{ID: "anima"}, {ID: "krea2", Warm: true}}}
	if r.setMirror(otherWarm) {
		t.Error("only the warm model moved, and that must not wake the workspaces")
	}
	if !r.Warm() {
		t.Error("Warm() = false while the far catalogue reports a warm model")
	}
	added := remoteCatalogRow{Key: "image", Models: []remoteCatalogModel{{ID: "anima"}, {ID: "krea2"}, {ID: "sdxl"}}}
	if !r.setMirror(added) {
		t.Error("a model added on the far side did not report a change")
	}
	if r.Warm() {
		t.Error("Warm() = true after the far side reported no warm model")
	}
	if !r.setMirror(row) {
		t.Error("a model withdrawn on the far side did not report a change")
	}
}

// A LoRA that shares the warm model's id must not make a sleeping engine read as warm, and the
// kind comes from the array the row arrived in.
func TestSetMirrorKindAndWarmComeFromTheirOwnArrays(t *testing.T) {
	r := NewRemote("image")
	r.setMirror(remoteCatalogRow{Key: "image",
		Models: []remoteCatalogModel{{ID: "anima"}},
		Loras:  []remoteCatalogModel{{ID: "anima", Warm: true}}})
	if r.Warm() {
		t.Error("a warm LoRA made the engine read as warm")
	}
	rows, err := r.CatalogSource()(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatalf("mirror = %+v, %v", rows, err)
	}
	if rows[0].Kind != "" || rows[1].Kind != ModelKindLora {
		t.Errorf("kinds = %q, %q; want a model then a lora", rows[0].Kind, rows[1].Kind)
	}
}

// The mirror reads as an empty list and never nil, before the first fetch and after an empty one:
// a nil source answer makes hasModels read true for a catalogue that holds nothing.
func TestCatalogSourceIsAnEmptyListNotNil(t *testing.T) {
	r := NewRemote("image")
	rows, err := r.CatalogSource()(t.Context())
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("before the first fetch: %#v, %v", rows, err)
	}
	r.setMirror(remoteCatalogRow{Key: "image"})
	rows, err = r.CatalogSource()(t.Context())
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("after an empty row: %#v, %v", rows, err)
	}
}

// The whole poll against a fake host: adopt once and tell once, say nothing for a repeat, tell
// again for a real move, and keep the far administrator's exclusion list trimmed.
func TestRefreshAllAdoptsMirrorsAndNotifiesOnlyOnMovement(t *testing.T) {
	body := `{"engines":[{"key":"image","api":"images","provider":"comfy","negative_always":"  watermark  ",
	  "model_rows":[{"id":"anima","warm":true,"params":{"steps":30}}]}]}`
	r := NewRemotesFor("http://far.invalid", "afei_borrower", nil, catalogDoer(&body))
	host := &fakeHost{}

	r.RefreshAll(t.Context(), host)
	if len(host.adopted) != 1 || host.adopted[0].Key != "image" || host.adopted[0].API != "images" ||
		host.adopted[0].Provider != "comfy" || !host.adopted[0].Remote() || host.adopted[0].URL != "http://far.invalid" {
		t.Fatalf("adopted = %+v", host.adopted)
	}
	if len(host.notified) != 1 {
		t.Fatalf("adopting a role told %d time(s), want 1", len(host.notified))
	}
	rem := r.ForKey("image")
	if got := rem.NegativeAlways(); got != "watermark" {
		t.Errorf("NegativeAlways() = %q, want the far side's list, trimmed", got)
	}
	if !rem.Warm() {
		t.Error("Warm() = false while the far catalogue reports a warm model")
	}
	rows, _ := rem.CatalogSource()(t.Context())
	if len(rows) != 1 || rows[0].Params == nil || rows[0].Params.Steps != 30 {
		t.Fatalf("mirror = %+v", rows)
	}

	r.RefreshAll(t.Context(), host)
	if len(host.notified) != 1 {
		t.Errorf("the same catalogue twice told again: %v", host.notified)
	}
	if host.invalidated != 1 {
		t.Errorf("a served row's cache was dropped %d time(s), want 1", host.invalidated)
	}

	body = `{"engines":[{"key":"image","api":"images","provider":"comfy",
	  "model_rows":[{"id":"anima"},{"id":"krea2"}]}]}`
	r.RefreshAll(t.Context(), host)
	if len(host.notified) != 2 {
		t.Errorf("a model enabled on the far side told %d time(s) in all, want 2", len(host.notified))
	}
	if got := rem.NegativeAlways(); got != "" {
		t.Errorf("NegativeAlways() = %q after the far side dropped its list", got)
	}
}

// A key a LOCAL row already holds is not borrowed: the far declaration is not even mirrored.
func TestRefreshAllLeavesALocalRowAlone(t *testing.T) {
	body := `{"engines":[{"key":"image","api":"images","provider":"comfy","model_rows":[{"id":"anima"}]}]}`
	r := NewRemotesFor("http://far.invalid", "afei_borrower", nil, catalogDoer(&body))
	host := &fakeHost{served: map[string]Def{"image": {Key: "image", API: "images", Provider: "comfy"}}}

	r.RefreshAll(t.Context(), host)
	if len(host.adopted) != 0 || len(host.notified) != 0 {
		t.Errorf("a local row was borrowed over: adopted %v, told %v", host.adopted, host.notified)
	}
	if rows, _ := r.ForKey("image").CatalogSource()(t.Context()); len(rows) != 0 {
		t.Errorf("a local row's key was mirrored: %+v", rows)
	}
}
