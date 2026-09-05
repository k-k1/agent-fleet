package opencode

// Catalog shaping (catalog.go). A shrunk fixed input, modelled on a measured ordering
// (61 Zen entries followed by 18 Go ones, some sharing a name), pins the per-plan filtering,
// the ordering, and the fallback conditions.

import (
	"strings"
	"testing"
)

// live is a shrunk version of a measured catalog: on the Zen side a free model, a twin
// sharing its name with a Go one, and a model Go does not have; on the Go side that twin and
// a Go-only model; plus another provider the user connected directly.
var live = []string{
	"opencode/deepseek-v4-flash-free",
	"opencode/claude-opus-5",
	"opencode/deepseek-v4-pro",
	"opencode/glm-5.2",
	"anthropic/claude-opus-5",
	"opencode-go/deepseek-v4-pro",
	"opencode-go/glm-5.2",
	"opencode-go/kimi-k3",
}

func ids(t *testing.T, pref string) []string {
	t.Helper()
	var out []string
	for _, c := range Catalog(live, pref) {
		if c.ID != c.Label {
			t.Errorf("label must stay the raw id (Console localizes): %+v", c)
		}
		out = append(out, c.ID)
	}
	return out
}

// withFreeIDs pins the zero-cost set the way a daemon read would leave it.
func withFreeIDs(t *testing.T, free ...string) {
	t.Helper()
	m := map[string]bool{}
	for _, id := range free {
		m[id] = true
	}
	modelsMu.Lock()
	prev := freeIDs
	freeIDs = m
	modelsMu.Unlock()
	t.Cleanup(func() {
		modelsMu.Lock()
		freeIDs = prev
		modelsMu.Unlock()
	})
}

// Zen does not filter the opencode.ai side: with Go also in use, both appear. Go is moved to
// the front but nothing is dropped, and within each group the order is normalized to
// ascending id, so the source's own ordering is never carried through.
func TestCatalogZenKeepsBothRoutesGoFirst(t *testing.T) {
	got := ids(t, UsageZen)
	want := []string{
		"opencode-go/deepseek-v4-pro", "opencode-go/glm-5.2", "opencode-go/kimi-k3",
		"anthropic/claude-opus-5", "opencode/claude-opus-5", "opencode/deepseek-v4-flash-free",
		"opencode/deepseek-v4-pro", "opencode/glm-5.2",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("zen = %v", got)
	}
}

// Reproduces and pins the "the order is sometimes scrambled" report: for the same catalog
// the input order differs between the daemon (GET /api/model, the upstream's raw order) and
// the CLI (opencode models, ascending id). After shaping they must be identical, otherwise
// the user sees the order change just by reopening the launch modal.
func TestCatalogOrderIsIndependentOfSource(t *testing.T) {
	// A shrunk copy of a measured daemon ordering: neither by name nor by provider.
	fromDaemon := []string{
		"opencode-go/kimi-k3",
		"opencode/deepseek-v4-flash-free",
		"opencode-go/glm-5.2",
		"anthropic/claude-opus-5",
		"opencode/glm-5.2",
		"opencode-go/deepseek-v4-pro",
		"opencode/claude-opus-5",
		"opencode/deepseek-v4-pro",
	}
	for _, pref := range []string{UsageZen, UsageGo} {
		a, b := Catalog(fromDaemon, pref), Catalog(live, pref) // live stands in for the CLI (different order)
		if len(a) != len(b) {
			t.Fatalf("%s: different counts: %d vs %d", pref, len(a), len(b))
		}
		for i := range a {
			if a[i].ID != b[i].ID {
				t.Fatalf("%s: order depends on the source: entry %d is %q vs %q", pref, i, a[i].ID, b[i].ID)
			}
		}
	}
}

// Go-only drops just the metered opencode/... ids. Dropping the directly connected
// providers the user wired up themselves (anthropic/... and so on) would make their own keys
// unusable.
func TestCatalogGoDropsOnlyMeteredIDs(t *testing.T) {
	got := ids(t, UsageGo)
	want := []string{
		"opencode-go/deepseek-v4-pro", "opencode-go/glm-5.2", "opencode-go/kimi-k3",
		"anthropic/claude-opus-5",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("go = %v", got)
	}
}

// On the free plan the opencode.ai side is limited to free models. Directly connected
// providers are billed separately, so they are kept here too.
func TestCatalogFreeKeepsZeroCostAndDirectProviders(t *testing.T) {
	withFreeIDs(t, "opencode/deepseek-v4-flash-free")
	got := ids(t, UsageFree)
	want := []string{"anthropic/claude-opus-5", "opencode/deepseek-v4-flash-free"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("free = %v", got)
	}
}

// With no price data (a CLI-sourced catalog leaves freeIDs empty) everything passes through.
// The free plan injects no OPENCODE_API_KEY, so the opencode.ai list that CLI returns
// already contains only free-plan models (measured); dropping everything here would leave
// the picker empty.
func TestCatalogFreeWithoutCostDataPassesThrough(t *testing.T) {
	withFreeIDs(t) // empty means unknown
	if got := ids(t, UsageFree); len(got) != len(live) {
		t.Errorf("free(no price data) = %v, want everything passed through", got)
	}
}

// An account with no Go contract (not a single opencode-go/... id) that selects Go-only must
// still not end up with an empty picker: ignoring the setting beats being unable to launch.
func TestCatalogFallsBackWhenItWouldEmptyThePicker(t *testing.T) {
	zenOnly := []string{"opencode/deepseek-v4-pro", "opencode/glm-5.2"}
	if got := Catalog(zenOnly, UsageGo); len(got) != 2 {
		t.Fatalf("got %+v, want the full list back", got)
	}
	withFreeIDs(t, "opencode/nothing-here")
	if got := Catalog(zenOnly, UsageFree); len(got) != 2 {
		t.Fatalf("free: got %+v, want the full list back", got)
	}
}

// Migration from the legacy values: "hide zen" expressed wanting to see only Go, while "go
// first" and "show all" expressed wanting both. Unset or unknown falls back to Off until
// something is chosen explicitly.
func TestCatalogPrefMigratesLegacyValues(t *testing.T) {
	for v, want := range map[string]string{
		"hide-zen": UsageGo,
		"go-first": UsageZen,
		"all":      UsageZen,
		"":         UsageOff,
		"nonsense": UsageOff,
		"FREE":     UsageOff,
		UsageFree:  UsageFree,
		UsageGo:    UsageGo,
		UsageZen:   UsageZen,
		UsageOff:   UsageOff,
	} {
		if got := CatalogPref(v); got != want {
			t.Errorf("CatalogPref(%q) = %q, want %q", v, got, want)
		}
	}
}

// off is an explicit declaration of "do not use this at all", so it is exempt from Catalog's
// empty-picker rescue (the Zen fallback that exists to avoid being unable to launch): staying
// empty is correct. Nothing is offered, including ids of other providers that do not go
// through opencode.ai.
func TestCatalogOffStaysEmpty(t *testing.T) {
	ids := []string{"opencode/deepseek-v4-pro", "opencode-go/glm-5.2", "anthropic/claude-opus-5"}
	if got := Catalog(ids, UsageOff); len(got) != 0 {
		t.Errorf("off = %+v, want empty", got)
	}
}

// An empty catalog (no CLI, or offline) is returned empty; the caller then offers only the
// default.
func TestCatalogEmptyStaysEmpty(t *testing.T) {
	if got := Catalog(nil, UsageGo); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}
