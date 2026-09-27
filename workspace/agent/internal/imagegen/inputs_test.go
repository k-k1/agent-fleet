package imagegen

// The reference-image gate (ADR 0100 decision 4). Each refusal is driven with a file that really
// exists and really is readable, so a pass means the GATE refused it — not that the path simply
// was not there.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withInputGate installs the browse root and a denylist of one folder, standing in for fs.go's.
func withInputGate(t *testing.T, browse string) {
	t.Helper()
	oldRoot, oldDenied := BrowseRootDir, PathDenied
	BrowseRootDir = func() string { return browse }
	PathDenied = func(rel string) bool { return rel == ".ssh" || strings.HasPrefix(rel, ".ssh/") }
	t.Cleanup(func() { BrowseRootDir, PathDenied = oldRoot, oldDenied })
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func inputSetsOnDisk(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(ConsoleInputsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func TestInputGateCopiesAReadableReference(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	withInputGate(t, home)
	pic := tinyPNG(t, 4, 4)
	writeFile(t, filepath.Join(home, "generated/console/inputs/a.png"), pic)
	gen := filepath.Join(generatedRootDir(), "console", "b.png")
	writeFile(t, gen, pic)

	req, st, err := stageRequestInputs(Request{Inputs: []string{"generated/console/inputs/a.png", gen}, Mask: gen})
	if err != nil {
		t.Fatalf("stage = %v", err)
	}
	if st.Set == "" || len(st.Origins) != 2 || st.Origins[0] != "generated/console/inputs/a.png" || st.MaskOrigin != gen {
		t.Fatalf("record = %+v, want the caller's own paths", st)
	}
	for _, p := range append(append([]string{}, req.Inputs...), req.Mask) {
		if !strings.HasPrefix(p, filepath.Join(ConsoleInputsDir(), st.Set)+"/") {
			t.Fatalf("request names %s, want a copy inside the input set", p)
		}
		b, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(b, pic) {
			t.Fatalf("copy %s = %v, want the original's bytes", p, err)
		}
	}
}

func TestInputGateRefuses(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	withInputGate(t, home)
	pic := tinyPNG(t, 4, 4)
	secret := filepath.Join(outside, "secret.png")
	writeFile(t, secret, pic)
	writeFile(t, filepath.Join(home, ".ssh", "id.png"), pic)
	// A symlink as the final component, and one as a directory on the way.
	if err := os.Symlink(secret, filepath.Join(home, "link.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "linkdir")); err != nil {
		t.Fatal(err)
	}
	// A symlink under the generated root too: that root is trusted for its location, not for
	// what it contains.
	if err := os.MkdirAll(generatedRootDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(generatedRootDir(), "g.png")); err != nil {
		t.Fatal(err)
	}

	for _, in := range []string{
		"link.png",
		filepath.Join(home, "link.png"),
		"linkdir/secret.png",
		filepath.Join(generatedRootDir(), "g.png"),
		".ssh/id.png",
		filepath.Join(home, ".ssh", "id.png"),
		secret,
		"../" + filepath.Base(outside) + "/secret.png",
		filepath.Join(home, "x", "..", "..", filepath.Base(outside), "secret.png"),
		"",
	} {
		t.Run(in, func(t *testing.T) {
			_, _, err := stageRequestInputs(Request{Inputs: []string{in}})
			if !errors.Is(err, errBadInput) {
				t.Fatalf("stage(%q) = %v, want a refusal", in, err)
			}
			_, _, err = stageRequestInputs(Request{Mask: in})
			if in != "" && !errors.Is(err, errBadInput) {
				t.Fatalf("stage(mask %q) = %v, want a refusal", in, err)
			}
		})
	}
	if got := inputSetsOnDisk(t); len(got) != 0 {
		t.Fatalf("refused requests left input sets behind: %v", got)
	}
}

// With no gate installed nothing is read: a missing hook is not a licence to read anything.
func TestInputGateRefusesWithoutTheHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	withInputGate(t, home)
	BrowseRootDir, PathDenied = nil, nil
	writeFile(t, filepath.Join(home, "a.png"), tinyPNG(t, 4, 4))
	if _, _, err := stageRequestInputs(Request{Inputs: []string{"a.png"}}); !errors.Is(err, errNoBrowseRoot) {
		t.Fatalf("stage = %v, want errNoBrowseRoot", err)
	}
}

// The provider is handed the copy, never the caller's path; the sidecar and the job list keep
// the caller's path; and the set goes when the last job of the group is over.
func TestQueueHandsTheProviderOnlyTheCopy(t *testing.T) {
	q := withJobQueue(t)
	home := os.Getenv("HOME")
	withInputGate(t, home)
	p := newGateProvider(t)
	withStubProvider(t, p)
	writeFile(t, filepath.Join(home, "refs/a.png"), tinyPNG(t, 4, 4))

	body := `{"op":"edit","prompt":"snow","inputs":["refs/a.png"],"jobs":2}`
	rec := httptest.NewRecorder()
	HandleJobs(rec, httptest.NewRequest(http.MethodPost, "/imagegen/jobs", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body.String())
	}
	var out EnqueueResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		waitFor(t, "a job to start", func() bool { return len(p.begun) > 0 })
		<-p.begun
		if sets := inputSetsOnDisk(t); len(sets) != 1 {
			t.Fatalf("input sets while the group runs = %v, want exactly one", sets)
		}
		p.release <- struct{}{}
	}
	waitFor(t, "the group to finish", func() bool {
		for _, j := range q.List().Jobs {
			if !JobState(j.State).finished() {
				return false
			}
		}
		return true
	})

	for _, req := range p.requests() {
		if len(req.Inputs) != 1 || !strings.HasPrefix(req.Inputs[0], ConsoleInputsDir()+"/") {
			t.Fatalf("provider was given %v, want only a copy inside %s", req.Inputs, ConsoleInputsDir())
		}
		if strings.Contains(req.Inputs[0], "refs/a.png") {
			t.Fatalf("provider saw the caller's path %s", req.Inputs[0])
		}
	}
	for _, j := range q.List().Jobs {
		if len(j.Inputs) != 1 || j.Inputs[0] != "refs/a.png" {
			t.Fatalf("job list inputs = %v, want the caller's own path", j.Inputs)
		}
		props, ok := readSidecar(j.Files[0].Path)
		if !ok || len(props.Inputs) != 1 || props.Inputs[0] != "refs/a.png" {
			t.Fatalf("sidecar inputs = %+v, want the caller's own path", props.Inputs)
		}
	}
	waitFor(t, "the input set to be removed", func() bool { return len(inputSetsOnDisk(t)) == 0 })
}

func TestQueueRemovesTheInputSetOnEveryEnding(t *testing.T) {
	stage := func(t *testing.T) JobSpec {
		t.Helper()
		home := os.Getenv("HOME")
		writeFile(t, filepath.Join(home, "refs/a.png"), tinyPNG(t, 4, 4))
		spec, err := stageJobSpec(JobSpec{Request: Request{Op: OpEdit, Prompt: "x", Inputs: []string{"refs/a.png"}}})
		if err != nil {
			t.Fatal(err)
		}
		return spec
	}
	t.Run("queued job cancelled", func(t *testing.T) {
		q := withJobQueue(t)
		withInputGate(t, os.Getenv("HOME"))
		p := newGateProvider(t)
		withStubProvider(t, p)
		enqueue(t, q, JobSpec{Request: Request{Prompt: "running"}})
		waitFor(t, "the first job to start", func() bool { return len(p.begun) > 0 })
		<-p.begun
		out := enqueue(t, q, stage(t))
		if err := q.Cancel(out.Jobs[0].ID); err != nil {
			t.Fatal(err)
		}
		if got := inputSetsOnDisk(t); len(got) != 0 {
			t.Fatalf("input sets after cancelling the queued job = %v", got)
		}
		drain(t, q, p)
	})
	t.Run("group cancelled while queued", func(t *testing.T) {
		q := withJobQueue(t)
		withInputGate(t, os.Getenv("HOME"))
		p := newGateProvider(t)
		withStubProvider(t, p)
		enqueue(t, q, JobSpec{Request: Request{Prompt: "running"}})
		waitFor(t, "the first job to start", func() bool { return len(p.begun) > 0 })
		<-p.begun
		spec := stage(t)
		spec.Jobs = 3
		out := enqueue(t, q, spec)
		if err := q.GroupOp(out.Group, "cancel"); err != nil {
			t.Fatal(err)
		}
		if got := inputSetsOnDisk(t); len(got) != 0 {
			t.Fatalf("input sets after cancelling the queued group = %v", got)
		}
		drain(t, q, p)
	})
	t.Run("enqueue refused", func(t *testing.T) {
		q := withJobQueue(t)
		home := os.Getenv("HOME")
		withInputGate(t, home)
		p := newGateProvider(t)
		withStubProvider(t, p)
		writeFile(t, filepath.Join(home, "refs/a.png"), tinyPNG(t, 4, 4))
		// A trial over the cap is refused by Enqueue itself, AFTER the gate has made the set —
		// the case the handler has to clean up. The provider is held busy so the trials wait.
		enqueue(t, q, JobSpec{Request: Request{Prompt: "running"}})
		waitFor(t, "the first job to start", func() bool { return len(p.begun) > 0 })
		<-p.begun
		for range imagegenTrialMax {
			if _, err := jobs.Enqueue(context.Background(), JobSpec{Request: Request{Prompt: "t"}, Trial: true}); err != nil {
				t.Fatal(err)
			}
		}
		body := `{"op":"edit","prompt":"snow","inputs":["refs/a.png"],"trial":true}`
		rec := httptest.NewRecorder()
		HandleJobs(rec, httptest.NewRequest(http.MethodPost, "/imagegen/jobs", strings.NewReader(body)))
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("POST = %d %s, want the trial cap's 429", rec.Code, rec.Body.String())
		}
		if got := inputSetsOnDisk(t); len(got) != 0 {
			t.Fatalf("input sets after a refused enqueue = %v", got)
		}
		drain(t, q, p)
	})
	t.Run("agent start", func(t *testing.T) {
		withJobQueue(t)
		withInputGate(t, os.Getenv("HOME"))
		stage(t)
		ClearInputSets()
		if got := inputSetsOnDisk(t); len(got) != 0 {
			t.Fatalf("input sets after ClearInputSets = %v", got)
		}
	})
}

// drain lets every job still held by p finish, so nothing is writing into the test's home when
// its TempDir is removed.
func drain(t *testing.T, q *jobQueue, p *gateProvider) {
	t.Helper()
	for i := 0; i < 64; i++ {
		p.release <- struct{}{}
	}
	waitFor(t, "every job to settle", func() bool {
		for _, j := range q.List().Jobs {
			if !JobState(j.State).finished() {
				return false
			}
		}
		return true
	})
}

// A spec whose references skipped the gate is refused rather than run.
func TestEnqueueRefusesUnstagedReferences(t *testing.T) {
	q := withJobQueue(t)
	withStubProvider(t, newGateProvider(t))
	for _, req := range []Request{
		{Prompt: "x", Op: OpEdit, Inputs: []string{"/etc/passwd"}},
		{Prompt: "x", Op: OpInpaint, Mask: "/etc/passwd"},
	} {
		if _, err := q.Enqueue(context.Background(), JobSpec{Request: req}); !errors.Is(err, errUnstagedInputs) {
			t.Fatalf("Enqueue(%+v) = %v, want errUnstagedInputs", req, err)
		}
	}
}

func TestReadRequestFileRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "real.png"), []byte("x"))
	if err := os.Symlink(filepath.Join(dir, "real.png"), filepath.Join(dir, "link.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := readRequestFile(filepath.Join(dir, "real.png")); err != nil {
		t.Fatalf("real file = %v", err)
	}
	if _, err := readRequestFile(filepath.Join(dir, "link.png")); err == nil {
		t.Fatal("a symlink in place of the copy was followed")
	}
}

// providerSources are the files of the providers that read a request's pictures themselves.
// They go through readRequestFile and nothing else, so none of them may open a file directly.
var providerSources = []string{"comfy.go", "comfy_picture_size.go", "comfy_qwenedit_size.go", "comfy_workflows.go", "openai_compat.go"}

// The AST check behind "a provider never reads a request path on its own" (ADR 0100 decision
// 4). Two rules: no provider file calls os.ReadFile / os.Open / os.OpenFile at all, and no file of
// the package passes an expression mentioning Inputs or Mask to one of them.
func TestProvidersReadRequestPathsOnlyThroughTheGate(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	isProvider := map[string]bool{}
	for _, f := range providerSources {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s is listed as a provider source and does not exist — update providerSources", f)
		}
		isProvider[f] = true
	}
	reads := map[string]bool{"ReadFile": true, "Open": true, "OpenFile": true}
	var found []string
	checked := 0
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			base := filepath.Base(name)
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "os" || !reads[sel.Sel.Name] {
					return true
				}
				checked++
				if isProvider[base] {
					found = append(found, fset.Position(call.Pos()).String()+": os."+sel.Sel.Name+" in a provider")
					return true
				}
				for _, arg := range call.Args {
					ast.Inspect(arg, func(m ast.Node) bool {
						if s, ok := m.(*ast.SelectorExpr); ok && (s.Sel.Name == "Inputs" || s.Sel.Name == "Mask") {
							found = append(found, fset.Position(call.Pos()).String()+": os."+sel.Sel.Name+" of a request path")
						}
						return true
					})
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("the walk found no os file call at all — the check is not looking at anything")
	}
	if len(found) > 0 {
		t.Fatalf("a request path is read around the gate (use readRequestFile):\n%s", strings.Join(found, "\n"))
	}
}
