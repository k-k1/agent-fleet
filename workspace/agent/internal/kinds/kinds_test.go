package kinds

import (
	"go/build"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/k-k1/agent-fleet/workspace/agent/"

// inModuleDeps lists the module's packages pkg imports, directly or not, read from the
// sources with go/build (no go command run).
func inModuleDeps(t *testing.T, pkg string) []string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	queue := []string{pkg}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		bp, err := build.Default.ImportDir(filepath.Join(root, strings.TrimPrefix(p, module)), 0)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		for _, imp := range bp.Imports {
			if strings.HasPrefix(imp, module) && !seen[imp] {
				seen[imp] = true
				queue = append(queue, imp)
			}
		}
	}
	var out []string
	for p := range seen {
		out = append(out, strings.TrimPrefix(p, module))
	}
	slices.Sort(out)
	return out
}

// The point of this package is that the packages the agent packages import can consult it.
// Depending on any of them would make that an import cycle, found only by the first PR that
// tries.
func TestKindsStaysBelowTheAgentPackages(t *testing.T) {
	deps := inModuleDeps(t, module+"internal/kinds")
	if !slices.Contains(deps, "internal/session") {
		t.Fatalf("deps = %v, want internal/session among them; the scan is not reading imports", deps)
	}
	for _, d := range deps {
		if d == "internal/agents" || strings.HasPrefix(d, "internal/agents/") ||
			d == "internal/agentreg" || d == "internal/bridge" || d == "internal/mcpreg" {
			t.Errorf("internal/kinds depends on %s, so that package (or one the agent packages import) can no longer import it", d)
		}
	}
}

// The scan above has to see a forbidden dependency where there is one.
func TestInModuleDepsSeesTheAgentPackages(t *testing.T) {
	deps := inModuleDeps(t, module+"internal/agents/claude")
	for _, want := range []string{"internal/agents", "internal/bridge", "internal/mcpreg"} {
		if !slices.Contains(deps, want) {
			t.Errorf("deps of internal/agents/claude = %v, missing %s", deps, want)
		}
	}
}

func TestAllIsACopyInRegistrationOrder(t *testing.T) {
	a := All()
	if len(a) != len(registered) {
		t.Fatalf("All() = %v, want %d kinds", a, len(registered))
	}
	for i, e := range registered {
		if a[i] != e.kind {
			t.Fatalf("All()[%d] = %q, want %q", i, a[i], e.kind)
		}
	}
	a[0] = "changed"
	if All()[0] == "changed" {
		t.Fatal("All() returned the package's own array")
	}
	seen := map[string]bool{}
	for _, e := range registered {
		if seen[e.kind] {
			t.Errorf("%q is registered twice", e.kind)
		}
		seen[e.kind] = true
	}
}

func TestKnownIsExact(t *testing.T) {
	for _, e := range registered {
		if !Known(e.kind) {
			t.Errorf("Known(%q) = false", e.kind)
		}
	}
	for _, k := range []string{"", "Claude", " codex", "codex ", "kiro-cli", "llama.cpp", "pi"} {
		if Known(k) {
			t.Errorf("Known(%q) = true", k)
		}
	}
}

// The product names the notification bridge writes into a chat message. A label edited here
// changes what every connected Discord / Slack channel reads, so the labels already shipped are
// pinned. A newly registered kind needs no row: its label is its registry entry, and only has to
// be a clean name (or empty, for a kind with no outward name).
func TestLabels(t *testing.T) {
	shipped := map[string]string{
		"claude": "Claude Code", "opencode": "opencode", "codex": "Codex", "copilot": "GitHub Copilot",
		"cursor": "Cursor", "kiro": "Kiro", "agy": "Antigravity", "muse": "Muse Code",
		"lcpp": "", "shell": "", "ssm": "",
	}
	for k, l := range shipped {
		if !Known(k) {
			t.Errorf("%q is pinned here but no longer registered; a kind is not removed silently", k)
			continue
		}
		if got := Label(k); got != l {
			t.Errorf("Label(%q) = %q, want %q", k, got, l)
		}
	}
	for _, k := range All() {
		if l := Label(k); l != strings.TrimSpace(l) {
			t.Errorf("Label(%q) = %q has surrounding space", k, l)
		}
	}
	if got := Label("pi"); got != "" {
		t.Errorf(`Label("pi") = %q, want ""`, got)
	}
}
