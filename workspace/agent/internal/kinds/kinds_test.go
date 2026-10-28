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
