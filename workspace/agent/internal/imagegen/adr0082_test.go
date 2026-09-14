package imagegen

// ADR 0082 P0: the images ROW is the routing unit, not the provider KIND. This file exercises
// decision 1 (Providers() builds one instance per row, keyed by the row's own key), decision 4
// (every currently declared row is the fleet's own, full stop) and decision 5 (a fall-through
// between two fleet rows must not claim a member's plan was spent).

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// withImageRows installs the two seams the Agent's own engines.go fills in production
// (EngineImageRows and EngineLookup), both restored on cleanup — the same discipline
// withEngineLookup already follows in http_test.go, widened to more than one row.
func withImageRows(t *testing.T, rows []EngineImageRow, lookup func(ctx context.Context, key string) (EngineConn, bool)) {
	t.Helper()
	oldRows, oldLookup := EngineImageRows, EngineLookup
	EngineImageRows = func(context.Context) []EngineImageRow { return rows }
	EngineLookup = lookup
	t.Cleanup(func() { EngineImageRows, EngineLookup = oldRows, oldLookup })
}

// Decision 1's whole point: a managed row and a borrowed row of the SAME kind (ADR 0079) sit
// side by side, Providers() builds one instance per row, and each is reachable by its OWN key —
// never by the bare kind name both rows share, which is what made a second row unreachable
// before this ADR.
func TestProvidersBuildsOneInstancePerImagesRowReachableByItsOwnKey(t *testing.T) {
	hitsByKey := map[string]int{}
	newStub := func(key string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hitsByKey[key]++
			_, _ = io.WriteString(w, openaiCompatAnswer(t, tinyPNG(t, 2, 2)))
		}))
	}
	managed := newStub("image")
	t.Cleanup(managed.Close)
	borrowed := newStub("comfy-lan")
	t.Cleanup(borrowed.Close)
	srvs := map[string]*httptest.Server{"image": managed, "comfy-lan": borrowed}

	withImageRows(t,
		[]EngineImageRow{{Key: "image", Provider: ProviderOpenAICompat}, {Key: "comfy-lan", Provider: ProviderOpenAICompat}},
		func(_ context.Context, key string) (EngineConn, bool) {
			srv, ok := srvs[key]
			if !ok {
				return EngineConn{}, false
			}
			return EngineConn{BaseURL: srv.URL, Token: "t", Models: []string{"sdxl-base-1.0"}}, true
		})

	ids := map[string]bool{}
	for _, p := range Providers() {
		ids[p.ID()] = true
	}
	for _, want := range []string{ProviderCodex, ProviderAgy, "image", "comfy-lan"} {
		if !ids[want] {
			t.Errorf("Providers() ids = %v, missing %q", ids, want)
		}
	}
	if len(ids) != 4 {
		t.Errorf("Providers() = %v, want exactly one instance per declared row plus the two vendor routes", ids)
	}

	for _, key := range []string{"image", "comfy-lan"} {
		p := providerByID(key)
		if p == nil {
			t.Fatalf("no provider registered under its own row key %q", key)
		}
		if !p.Ready(context.Background()) {
			t.Fatalf("%s: not ready even though its own row's lookup answers", key)
		}
		res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
		if err != nil {
			t.Fatalf("%s: Generate() = %v", key, err)
		}
		if res.Provider != key {
			t.Errorf("%s: result.Provider = %q, want the row's own key", key, res.Provider)
		}
	}
	if hitsByKey["image"] != 1 || hitsByKey["comfy-lan"] != 1 {
		t.Errorf("hits = %v, want exactly one request landing on each row's own server", hitsByKey)
	}
}

// Decision 4: an undeclared id nobody wrote a row for is never the fleet's own — but every id
// EngineImageRows currently names IS, unconditionally. Getting this backwards reproduces the
// measured ADR 0072 2026-09-11 accident: a provider treated as external is inserted BEHIND a
// member's own plan, so "auto" spends that plan before it ever reaches hardware already paid for.
func TestProviderIsFleetForDynamicEngineKeys(t *testing.T) {
	withImageRows(t, []EngineImageRow{{Key: "comfy-lan", Provider: ProviderComfy}}, nil)
	if !providerIsFleet("comfy-lan") {
		t.Error("a declared images row was not treated as the fleet's own")
	}
	if providerIsFleet("not-declared") {
		t.Error("an id nobody declared a row for was treated as the fleet's own")
	}
	// A borrowed row (ADR 0079) is still this deployment's own to fleet-label — ADR 0079
	// decision 9 already decided that borrowed-engine pictures are counted on THIS side.
	withImageRows(t, []EngineImageRow{{Key: "borrowed-image", Provider: ProviderOpenAICompat}}, nil)
	if !providerIsFleet("borrowed-image") {
		t.Error("a borrowed row was not treated as the fleet's own")
	}
}

// Decision 5: when BOTH the row that failed and the row that answered instead are this
// deployment's own — no member's plan moved — the warning must say so instead of repeating the
// "different account's plan" sentence written for the vendor-route case, which would now be a
// lie.
func TestFallbackBetweenTwoFleetRowsDoesNotClaimAnotherAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_USAGE_DIR", home+"/usage")
	withImageRows(t, []EngineImageRow{
		{Key: "comfy-lan", Provider: ProviderComfy},
		{Key: "borrowed-image", Provider: ProviderOpenAICompat},
	}, nil)
	withStubProvider(t,
		stubProvider{id: "comfy-lan", err: errNotUp("the LAN host did not answer")},
		stubProvider{id: "borrowed-image", res: Result{Provider: "borrowed-image",
			Images: []Image{{Bytes: tinyPNG(t, 4, 4), MIME: "image/png", Width: 4, Height: 4}}}},
	)

	out, err := Run(context.Background(), Job{Session: "slot01", SID: "sid-1",
		Request: Request{Op: OpGenerate, Prompt: "a cat"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Provider != "borrowed-image" {
		t.Fatalf("provider = %q, want the fall-through to have produced it", out.Provider)
	}
	var got string
	for _, w := range out.Warnings {
		if strings.Contains(w, "fell back") {
			got = w
		}
	}
	if got == "" {
		t.Fatalf("warnings = %v — a fall-through said nothing", out.Warnings)
	}
	if strings.Contains(got, "different account") {
		t.Errorf("warning = %q, want no claim about a different account — nobody's plan moved between two fleet rows", got)
	}
	if !strings.Contains(got, "comfy-lan") || !strings.Contains(got, "did not answer") {
		t.Errorf("warning = %q, want it to name the row that failed and why", got)
	}
}

// The mirror of the test above, with the built-in default (both static ranks unchanged): a
// fall-through from the fleet's own engine to a member's personal plan (agy) must still say so —
// decision 5 only changes the wording when NEITHER side spends a member's plan.
func TestFallbackFromFleetToVendorStillClaimsAnotherAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_USAGE_DIR", home+"/usage")
	withStubProvider(t,
		stubProvider{id: ProviderOpenAICompat, err: errNotUp("the engine did not come up")},
		stubProvider{id: ProviderAgy, res: Result{Provider: ProviderAgy,
			Images: []Image{{Bytes: tinyPNG(t, 4, 4), MIME: "image/png", Width: 4, Height: 4}}}},
	)
	out, err := Run(context.Background(), Job{Session: "slot01", SID: "sid-1",
		Request: Request{Op: OpGenerate, Prompt: "a cat"}})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, w := range out.Warnings {
		if strings.Contains(w, "fell back") {
			got = w
		}
	}
	if got == "" || !strings.Contains(got, "different account") {
		t.Fatalf("warning = %q, want the different-account phrasing preserved for a fleet-to-vendor fall-through", got)
	}
}

// Decision 3's second half: a preference saved before this ADR could only ever have named the
// bare KIND ("comfy"/"openai-compat"). Dropping it instead of expanding it is the accident this
// normalization step exists to prevent — a fleet engine whose key is not literally the kind name
// falls out of the stored order entirely.
func TestNormalizeStoredProviderOrderExpandsLegacyKindAlias(t *testing.T) {
	rows := []EngineImageRow{{Key: "image", Provider: ProviderComfy}, {Key: "comfy-lan", Provider: ProviderComfy}}
	got := normalizeStoredProviderOrder([]string{ProviderComfy, ProviderAgy, ProviderCodex}, rows)
	want := []string{"image", "comfy-lan", ProviderAgy, ProviderCodex}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeStoredProviderOrder = %v, want %v", got, want)
	}
	// A row whose key IS the kind name is not expanded twice.
	got = normalizeStoredProviderOrder([]string{ProviderOpenAICompat}, []EngineImageRow{{Key: ProviderOpenAICompat, Provider: ProviderOpenAICompat}})
	if want := []string{ProviderOpenAICompat}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeStoredProviderOrder = %v, want %v", got, want)
	}
}

// End to end through effectiveOrder: a deployment upgraded from a single "comfy" row to a
// renamed key must still rank it where the stored preference put the alias — at the front, since
// the preference in this test ranked the fleet's engine first.
func TestEffectiveOrderExpandsLegacyAliasIntoTheDeclaredRow(t *testing.T) {
	withImageRows(t, []EngineImageRow{{Key: "comfy-lan", Provider: ProviderComfy}}, nil)
	oldPref := ProviderOrderPref
	ProviderOrderPref = func() []string { return []string{ProviderComfy, ProviderAgy, ProviderCodex} }
	t.Cleanup(func() { ProviderOrderPref = oldPref })

	got := effectiveOrder()
	posOf := func(id string) int {
		for i, g := range got {
			if g == id {
				return i
			}
		}
		return -1
	}
	lan, agy, codex := posOf("comfy-lan"), posOf(ProviderAgy), posOf(ProviderCodex)
	if lan < 0 {
		t.Fatalf("effectiveOrder = %v, want the legacy alias expanded to the declared row", got)
	}
	if lan > agy || lan > codex {
		t.Fatalf("effectiveOrder = %v, want the declared row ranked ahead of both vendor routes"+
			" (the preference named the legacy alias first)", got)
	}
}

type errNotUp string

func (e errNotUp) Error() string { return string(e) }
