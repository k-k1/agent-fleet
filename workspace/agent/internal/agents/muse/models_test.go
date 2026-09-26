package muse

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
)

// catalogHost answers `model/list` with the given rows and records the params it was sent.
func catalogHost(t *testing.T, raw string) (*msp.Client, *json.RawMessage) {
	t.Helper()
	host, cl := msptest.New(t, msp.Handler{})
	var got json.RawMessage
	host.Handle(msp.MethodModelList, func(m msptest.Message) (any, *msp.Error) {
		got = m.Params
		return json.RawMessage(raw), nil
	})
	return cl, &got
}

func TestModelsFromMapsTheCatalog(t *testing.T) {
	cl, _ := catalogHost(t, `{"providerId":"meta","source":"providerCatalog","models":[
		{"modelId":"muse-spark-1.3","displayLabel":"Muse Spark 1.3","providerId":"meta","isDefault":true,"isActive":false},
		{"modelId":"muse-forge-1.3","displayLabel":"Muse Forge 1.3","providerId":"meta","isDefault":false,"isActive":false}]}`)

	list, _, err := modelsFrom(cl)
	if err != nil {
		t.Fatalf("modelsFrom: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d models, want 2: %+v", len(list), list)
	}
	if list[0].ID != "muse-spark-1.3" || list[0].Label != "Muse Spark 1.3" {
		t.Errorf("first row is %+v", list[0])
	}
	// Order is the catalog's own (newest releaseDate first, the schema says), never re-sorted
	// here — the picker shows what the vendor recommends.
	if list[1].ID != "muse-forge-1.3" {
		t.Errorf("the catalog order was not preserved: %+v", list)
	}
	// Effort is a turn parameter, not a catalog property, so every row carries the same set.
	for _, m := range list {
		if len(m.Efforts) != len(msp.ReasoningEffortValues) {
			t.Errorf("%s offers %d efforts, want %d", m.ID, len(m.Efforts), len(msp.ReasoningEffortValues))
		}
		if m.DefaultEffort != "" {
			t.Errorf("%s claims a default effort (%q) the catalog does not carry", m.ID, m.DefaultEffort)
		}
	}
}

// A row with no id is not selectable, and a row with no label has to show something. Both are
// schema-legal shapes: displayLabel is required but the empty string satisfies it.
func TestModelsFromDropsIdlessRowsAndFallsBackToTheId(t *testing.T) {
	cl, _ := catalogHost(t, `{"providerId":"meta","source":"providerCatalog","models":[
		{"modelId":"","displayLabel":"nameless","providerId":"meta"},
		{"modelId":"muse-spark-1.3","displayLabel":"","providerId":"meta"}]}`)

	list, _, err := modelsFrom(cl)
	if err != nil {
		t.Fatalf("modelsFrom: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d models, want 1: %+v", len(list), list)
	}
	if list[0].Label != "muse-spark-1.3" {
		t.Errorf("an empty label did not fall back to the id: %+v", list[0])
	}
}

// The launch menu has no session behind it, so the query must not name one. sessionId only
// flags the row matching a session's effective model, and sending someone else's session id
// would mark a row active in a menu that is about to create a different session.
func TestModelsFromNamesNoSession(t *testing.T) {
	cl, params := catalogHost(t, `{"providerId":"meta","source":"providerCatalog","models":[]}`)
	if _, _, err := modelsFrom(cl); err != nil {
		t.Fatalf("modelsFrom: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(*params, &sent); err != nil {
		t.Fatalf("model/list params: %v", err)
	}
	if _, ok := sent["sessionId"]; ok {
		t.Errorf("model/list carried a sessionId: %s", *params)
	}
	// It is a query, not a command: the schema gives it no commandId and inventing one would
	// claim a durable intake record that does not exist.
	if _, ok := sent["commandId"]; ok {
		t.Errorf("model/list carried a commandId: %s", *params)
	}
}

// An empty catalog is a valid answer (the schema says so), and it must be distinguishable
// from a failed fetch: the caller caches the former and keeps the previous list for the latter.
func TestModelsFromReturnsEmptyNotNilForAnEmptyCatalog(t *testing.T) {
	cl, _ := catalogHost(t, `{"providerId":"meta","source":"providerCatalog","models":[]}`)
	list, _, err := modelsFrom(cl)
	if err != nil {
		t.Fatalf("modelsFrom: %v", err)
	}
	if list == nil {
		t.Error("an empty catalog came back as nil, which Models() reads as 'never fetched'")
	}
}

func TestModelsFromPropagatesARefusal(t *testing.T) {
	host, cl := msptest.New(t, msp.Handler{})
	host.Handle(msp.MethodModelList, func(msptest.Message) (any, *msp.Error) {
		return nil, &msp.Error{Code: msp.ErrCodeOverloaded, Message: "overloaded"}
	})
	if _, _, err := modelsFrom(cl); err == nil {
		t.Error("a refused model/list came back as a successful empty catalog")
	}
}

// The picker's effort list and the driver's accepted set are the same list, and this is what
// pins it. Offering a value `turn/start` would refuse is a control that fails when used; a
// value the driver accepts but the picker never offers is a feature nobody can reach.
func TestEveryOfferedEffortIsAcceptedByTheDriver(t *testing.T) {
	offered := effortChoices()
	if len(offered) != len(msp.ReasoningEffortValues) {
		t.Fatalf("the picker offers %d efforts, the bundle declares %d", len(offered), len(msp.ReasoningEffortValues))
	}
	for _, e := range offered {
		if reasoningEffort(e) == nil {
			t.Errorf("the picker offers %q and the driver refuses it", e)
		}
	}
	// The control: without it the loop above passes against a reasoningEffort that accepted
	// everything, including a value the host would reject.
	if reasoningEffort("thorough") != nil {
		t.Error("the driver accepted an effort MSP does not declare")
	}
}

// resetModelCatalogCache isolates a test from the package-level catalog cache, which is shared
// by the picker and by every session start. Without it the first test to fetch decides what
// the rest see.
func resetModelCatalogCache(t *testing.T) {
	t.Helper()
	modelsMu.Lock()
	prevList, prevSafe, prevAt := modelsList, modelsSafe, modelsAt
	modelsList, modelsSafe, modelsAt = nil, nil, time.Time{}
	modelsMu.Unlock()
	t.Cleanup(func() {
		modelsMu.Lock()
		modelsList, modelsSafe, modelsAt = prevList, prevSafe, prevAt
		modelsMu.Unlock()
	})
}

// 🔴 The clamp-8 pick. The catalog's own `isDefault` row is the contributor one (measured), so
// a default that followed the vendor's flag — or sent no modelId at all — would put every
// session that never touched the picker onto the model whose description says the conversation
// may be used for product improvement.
func TestSafeDefaultSkipsTheVendorsContributorDefault(t *testing.T) {
	cl, _ := catalogHost(t, `{"providerId":"meta","source":"providerCatalog","models":[
		{"modelId":"muse-spark-1.3","displayLabel":"muse-spark-1.3","providerId":"meta","isDefault":false},
		{"modelId":"muse-spark-1.3-contributor","displayLabel":"muse-spark-1.3-contributor","providerId":"meta","isDefault":true,
		 "description":"Your content, including inter-session messages, may be used for product improvement."}]}`)

	_, safeIDs, err := modelsFrom(cl)
	safe := firstID(safeIDs)
	if err != nil {
		t.Fatalf("modelsFrom: %v", err)
	}
	if safe != "muse-spark-1.3" {
		t.Errorf("safe default is %q, want the non-contributor row", safe)
	}
}

// Every safe row is kept, in catalog order, so a member who hides the newest one still gets a
// safe model rather than none (SafeExecModels, #1020 review).
func TestSafeModelsKeepEveryNonSharingRowInOrder(t *testing.T) {
	cl, _ := catalogHost(t, `{"providerId":"meta","source":"providerCatalog","models":[
		{"modelId":"muse-spark-1.3","displayLabel":"a","providerId":"meta"},
		{"modelId":"muse-spark-1.3-contributor","displayLabel":"b","providerId":"meta"},
		{"modelId":"muse-spark-1.2","displayLabel":"c","providerId":"meta"},
		{"modelId":"muse-spark-1.2-contributor","displayLabel":"d","providerId":"meta"}]}`)

	_, safeIDs, err := modelsFrom(cl)
	if err != nil {
		t.Fatalf("modelsFrom: %v", err)
	}
	if want := []string{"muse-spark-1.3", "muse-spark-1.2"}; !slices.Equal(safeIDs, want) {
		t.Errorf("safe ids = %v, want %v", safeIDs, want)
	}
}

// The second half of the union predicate, and the only test that can tell the two signals
// apart: an id with no `-contributor` suffix whose description carries the claim anyway.
// Without this, a predicate that read the suffix alone would look identical.
func TestSafeDefaultReadsTheDescriptionNotOnlyTheSuffix(t *testing.T) {
	cl, _ := catalogHost(t, `{"providerId":"meta","source":"providerCatalog","models":[
		{"modelId":"muse-spark-2.0-community","displayLabel":"muse-spark-2.0-community","providerId":"meta",
		 "description":"Your content may be used for product improvement."},
		{"modelId":"muse-spark-1.3","displayLabel":"muse-spark-1.3","providerId":"meta"}]}`)

	_, safeIDs, err := modelsFrom(cl)
	safe := firstID(safeIDs)
	if err != nil {
		t.Fatalf("modelsFrom: %v", err)
	}
	if safe != "muse-spark-1.3" {
		t.Errorf("safe default is %q: the description's claim was ignored", safe)
	}
}

// Every row sharing is a real possibility for a future catalog, and the honest answer is "AF
// has nothing safe to pick" — not the first row. The caller then sends no modelId, which is
// the host's choice and is what the member's picker still overrides.
func TestSafeDefaultIsEmptyWhenEveryRowShares(t *testing.T) {
	cl, _ := catalogHost(t, `{"providerId":"meta","source":"providerCatalog","models":[
		{"modelId":"muse-spark-1.3-contributor","displayLabel":"a","providerId":"meta"},
		{"modelId":"muse-spark-1.2-contributor","displayLabel":"b","providerId":"meta"}]}`)

	_, safeIDs, err := modelsFrom(cl)
	safe := firstID(safeIDs)
	if err != nil {
		t.Fatalf("modelsFrom: %v", err)
	}
	if safe != "" {
		t.Errorf("safe default is %q, want none", safe)
	}
}

// The contributor rows stay SELECTABLE — the member owns the choice, they are the same model
// at the same price (decision 6 clamp 8). Dropping them from the picker would be AF deciding
// for them, which is the thing the clamp was explicitly softened to avoid.
func TestTheContributorRowsAreStillOffered(t *testing.T) {
	cl, _ := catalogHost(t, `{"providerId":"meta","source":"providerCatalog","models":[
		{"modelId":"muse-spark-1.3","displayLabel":"muse-spark-1.3","providerId":"meta"},
		{"modelId":"muse-spark-1.3-contributor","displayLabel":"muse-spark-1.3-contributor","providerId":"meta"}]}`)

	list, _, err := modelsFrom(cl)
	if err != nil {
		t.Fatalf("modelsFrom: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("the picker offers %d of 2 rows: %+v", len(list), list)
	}
}
