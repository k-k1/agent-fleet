package mcpx

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// mcpx cannot import sessionx, so the registry is read from the CP's checked-in kind catalog.
// package main's TestKindCatalogSnapshot holds that file byte-equal to sessionx's registry.
const kindCatalogPath = "../../../../control-plane/internal/agentkinds/catalog.json"

type catalogKind struct {
	Kind           string `json:"kind"`
	ManagedDefault bool   `json:"managedDefault"`
}

func readKindCatalog(t *testing.T) []catalogKind {
	t.Helper()
	b, err := os.ReadFile(kindCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	var cat struct {
		Kinds []catalogKind `json:"kinds"`
	}
	if err := json.Unmarshal(b, &cat); err != nil {
		t.Fatal(err)
	}
	if len(cat.Kinds) == 0 {
		t.Fatalf("%s lists no kinds", kindCatalogPath)
	}
	return cat.Kinds
}

func findTool(t *testing.T, tools []map[string]any, name string) map[string]any {
	t.Helper()
	for _, tool := range tools {
		if tool["name"] == name {
			return tool
		}
	}
	t.Fatalf("tool %s is not in the list", name)
	return nil
}

func kindParamDescription(t *testing.T, tool map[string]any) string {
	t.Helper()
	props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
	return props["kind"].(map[string]any)["description"].(string)
}

// kindEnumeration matches the first "a | b | c" (or "a / b / c") run in a description, with
// an optional default marker after a kind; later runs such as "shell/ssm are refused" are not it.
var kindEnumeration = regexp.MustCompile(`[a-z]+(?:（既定）| \(default\))?(?:\s*[|/]\s*[a-z]+(?:（既定）| \(default\))?)+`)

func advertisedKinds(desc string) []string {
	run := kindEnumeration.FindString(desc)
	var out []string
	for _, w := range regexp.MustCompile(`[a-z]+`).FindAllString(run, -1) {
		if w != "default" {
			out = append(out, w)
		}
	}
	return out
}

// Every kind create_session and list_models accept has to be in the enumeration their `kind`
// parameter advertises: a model only offers what it was told about, so a missing kind is one
// it never launches (muse fell out of the operator's create_session list this way).
func TestAdvertisedKindListsCoverTheRegistry(t *testing.T) {
	cat := readKindCatalog(t)
	cases := []struct {
		surface string
		tools   []map[string]any
		tool    string
		// absent are the kinds the tool may leave out, each for the reason given.
		absent map[string]string
	}{
		{"operator", mcpStdioWriteTools, "create_session", map[string]string{
			"ssm": "needs an ssm_target the tool has no parameter for",
		}},
		{"operator", mcpStdioWriteTools, "list_models", map[string]string{
			"shell": "runs no model", "ssm": "runs no model",
		}},
		{"session", mcpStdioFleetSpawnTools(), "create_session", map[string]string{
			"shell": "refused for a session", "ssm": "refused for a session",
		}},
		{"session", mcpStdioFleetSpawnTools(), "list_models", map[string]string{
			"shell": "runs no model", "ssm": "runs no model",
		}},
	}
	for _, c := range cases {
		got := advertisedKinds(kindParamDescription(t, findTool(t, c.tools, c.tool)))
		if len(got) == 0 {
			t.Errorf("%s %s: no kind enumeration found in the kind description", c.surface, c.tool)
			continue
		}
		for _, k := range cat {
			if _, ok := c.absent[k.Kind]; ok {
				continue
			}
			if !slices.Contains(got, k.Kind) {
				t.Errorf("%s %s: kind %q is registered but not advertised (got %v)", c.surface, c.tool, k.Kind, got)
			}
		}
		for _, k := range got {
			if !slices.ContainsFunc(cat, func(e catalogKind) bool { return e.Kind == k }) {
				t.Errorf("%s %s: advertises %q, which is not a registered kind", c.surface, c.tool, k)
			}
		}
	}
}

// The operator's create_session description tells the model which kinds start managed; that
// sentence has to name every kind whose MCP launch actually asks for the managed driver
// (Caps.ManagedDefault or ManagedOnly, which the catalog folds into managedDefault).
func TestCreateSessionManagedSentenceNamesTheManagedDefaultKinds(t *testing.T) {
	cat := readKindCatalog(t)
	const marker = "managed で開始"
	desc := findTool(t, mcpStdioWriteTools, "create_session")["description"].(string)
	i := strings.Index(desc, marker)
	if i < 0 {
		t.Fatalf("operator create_session: no sentence containing %q", marker)
	}
	start := strings.LastIndex(desc[:i], "。") + len("。")
	if start < len("。") {
		start = 0
	}
	end := len(desc)
	if k := strings.Index(desc[i:], "。"); k >= 0 {
		end = i + k + len("。")
	}
	sentence := desc[start:end]
	for _, k := range cat {
		if k.ManagedDefault && !strings.Contains(strings.ToLower(sentence), k.Kind) {
			t.Errorf("operator create_session: managed sentence %q does not name %q", sentence, k.Kind)
		}
	}
}
