package afmemory

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const proposalPatch = "diff --git a/AGENTS.md b/AGENTS.md\n--- a/AGENTS.md\n+++ b/AGENTS.md\n@@ -1 +1,5 @@\n # Agents\n+\n+## Release\n+\n+Run it.\n"

func proposalAgent(t *testing.T, answer string) (*Client, *[]string, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var reqs []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, r.Method+" "+r.URL.RequestURI())
		switch r.URL.Path {
		case "/agents/memory/entries/projects":
			io.WriteString(w, `{"projects":[{"project":{"id":"demo-0123456789ab","display":"demo"},"count":2}]}`)
		case "/agents/memory/entries/promotion-proposal":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			bodies = append(bodies, b)
			io.WriteString(w, answer)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL, HTTP: srv.Client()}, &reqs, &bodies
}

func proposalAnswer(ready bool, pre string, extra string) string {
	b, _ := json.Marshal(proposalPatch)
	r := "false"
	if ready {
		r = "true"
	}
	return `{"project":{"id":"demo-0123456789ab","display":"demo"},"ref":"origin/main","commit":"0123456789abcdef0123456789abcdef01234567",` +
		`"target":{"repo":"github.com/acme/demo","path":"AGENTS.md","heading":"Release"},"fileExists":true,"sectionExists":false,` +
		`"sources":[{"name":"release-steps","scope":"project","revision":3,"bodySha256":"` + strings.Repeat("ab", 32) + `"}],"text":"Run it.","patch":` + string(b) + `,` +
		`"warnings":["language-differs"],"scans":{"secret":[],"preCommit":` + pre + `},"ready":` + r + extra + `}`
}

// The name resolves to the project id, the flags go in the body, and the report names the target,
// the source memory and revision, the warning, both scans, and the patch.
func TestAFMemoryPromoteProposal(t *testing.T) {
	c, reqs, bodies := proposalAgent(t, proposalAnswer(true, `{"ran":true,"hook":"core.hooksPath .githooks","passed":true,"output":"ok"}`, ""))
	code, out, errw := run(c, "promote-proposal", "--project", "demo", "--name", "release-steps", "--file", "AGENTS.md", "--section", "Release it's", "--days", "3")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errw)
	}
	for _, want := range []string{
		"== proposal: AGENTS.md (ready to commit)",
		"repository: github.com/acme/demo  [origin/main @ 0123456789ab]",
		`section:    "Release" (new section)`,
		"from:       release-steps r3 (project)",
		"rewrite the text in the repository's language",
		"secret scan: clean",
		"pre-commit:  passed (core.hooksPath .githooks)",
		"+## Release",
		`AF pushes nothing: apply it with ` + "`" + `af-memory promote-proposal --project demo-0123456789ab --name release-steps --file AGENTS.md --section 'Release it'\''s' --days 3 --patch | git apply` + "`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if len(*bodies) != 1 {
		t.Fatalf("bodies %v", *bodies)
	}
	b := (*bodies)[0]
	if b["project"] != "demo-0123456789ab" || b["name"] != "release-steps" || b["file"] != "AGENTS.md" || b["section"] != "Release it's" || b["days"] != float64(3) {
		t.Fatalf("body %v", b)
	}
	if (*reqs)[len(*reqs)-1] != "POST /agents/memory/entries/promotion-proposal" {
		t.Fatalf("reqs %v", *reqs)
	}

	// --patch prints the patch alone, for git apply.
	code, out, _ = run(c, "promote-proposal", "--project", "demo-0123456789ab", "--name", "release-steps", "--patch")
	if code != 0 || out != proposalPatch {
		t.Fatalf("--patch: %d %q", code, out)
	}
	code, out, _ = run(c, "promote-proposal", "--project", "demo", "--name", "release-steps", "--json")
	var p proposal
	// The digest a supersede has to quote back survives the CLI's decoding.
	if code != 0 || json.Unmarshal([]byte(out), &p) != nil || !p.Ready || p.Target.Heading != "Release" ||
		len(p.Sources) != 1 || p.Sources[0].BodySHA256 != strings.Repeat("ab", 32) {
		t.Fatalf("--json: %d %s", code, out)
	}
}

// A failing hook is shown with its output and the proposal is not ready.
func TestAFMemoryPromoteProposalFailingHook(t *testing.T) {
	c, _, _ := proposalAgent(t, proposalAnswer(false, `{"ran":true,"hook":"core.hooksPath .githooks","passed":false,"output":"forbidden token\n","note":"the hook failed (exit 1)"}`, ""))
	code, out, _ := run(c, "promote-proposal", "--project", "demo", "--name", "release-steps")
	if code != 0 || !strings.Contains(out, "NOT ready") || !strings.Contains(out, "pre-commit:  FAILED (core.hooksPath .githooks): the hook failed (exit 1)") ||
		!strings.Contains(out, "     | forbidden token") {
		t.Fatalf("%d\n%s", code, out)
	}
}

func TestAFMemoryPromoteProposalUsage(t *testing.T) {
	c, reqs, _ := proposalAgent(t, "{}")
	for _, args := range [][]string{
		{"promote-proposal"},
		{"promote-proposal", "--project", "demo"},
		{"promote-proposal", "--name", "x"},
		{"promote-proposal", "--project", "demo", "--name", "x", "--days", "0"},
		{"promote-proposal", "--project", "demo", "--name", "x", "--days", "91"},
		{"promote-proposal", "--project", "demo", "--name", "x", "--json", "--patch"},
		{"promote-proposal", "--project", "demo", "--name", "x", "extra"},
	} {
		if code, _, _ := run(c, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if len(*reqs) != 0 {
		t.Fatalf("a usage error asked the Agent: %v", *reqs)
	}
	if code, _, errw := run(c, "promote-proposal", "--project", "nope", "--name", "x"); code != 1 || !strings.Contains(errw, `no project "nope"`) {
		t.Fatalf("unknown project: %d %s", code, errw)
	}
}

// A hook that changed the staged change: the report says the patch is the one given to the hook.
func TestAFMemoryPromoteProposalChangedByTheHook(t *testing.T) {
	c, _, _ := proposalAgent(t, proposalAnswer(false, `{"ran":true,"hook":"core.hooksPath .githooks","passed":true,"changed":true}`, ""))
	code, out, _ := run(c, "promote-proposal", "--project", "demo", "--name", "release-steps")
	if code != 0 || !strings.Contains(out, "NOT ready") || !strings.Contains(out, "the patch below is the one given to it, not what it left") {
		t.Fatalf("%d\n%s", code, out)
	}
}
