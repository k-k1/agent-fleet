package memengine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Nothing in the engine may depend on a model: IDs live in a config file, the
// code knows roles. A vendor or model name in a non-test source file means a
// lineup change would need a code change.
var modelName = regexp.MustCompile(`(?i)haiku|sonnet|opus|fable|gemini|gpt-|luna|argon|oss-\d|llama|mistral`)

func TestNoModelIDsInCode(t *testing.T) {
	if !modelName.MatchString("use claude-sonnet-x here") || modelName.MatchString("small and large roles") {
		t.Fatal("the model-name pattern does not catch what it should")
	}
	n := 0
	filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		n++
		b, _ := os.ReadFile(p)
		for i, l := range strings.Split(string(b), "\n") {
			if modelName.MatchString(l) {
				t.Errorf("%s:%d names a model: %s", p, i+1, strings.TrimSpace(l))
			}
		}
		return nil
	})
	if n < 8 {
		t.Fatalf("scanned %d files", n)
	}
}

func fakeRunner(script string) RunnerSpec { return RunnerSpec{Argv: []string{"sh", "-c", script}} }

func testConfig(small, large string) Config {
	return Config{
		Roles: map[Role]string{RoleSmall: "fake:a", RoleLarge: "fake:b"},
		Runners: map[string]RunnerSpec{
			"fake": fakeRunner(small + "; : {model}"),
			"live": fakeRunner(large),
		},
	}
}

func TestCommandSubstitutesTheModelID(t *testing.T) {
	c := Config{Runners: map[string]RunnerSpec{"r": {Argv: []string{"x", "--model", "{model}", "{model}-y"}}}}
	cmd, err := c.Command("r:id:with:colons")
	if err != nil || strings.Join(cmd.Argv, " ") != "x --model id:with:colons id:with:colons-y" {
		t.Fatalf("%v %v", cmd.Argv, err)
	}
	for _, bad := range []string{"r", "r:", ":m", "nope:m"} {
		if _, err := c.Command(bad); err == nil {
			t.Errorf("%q should not resolve", bad)
		}
	}
	c.Runners["sess"] = RunnerSpec{Session: true}
	if _, err := c.Command("sess:m"); err == nil || !strings.Contains(err.Error(), "session-only") {
		t.Errorf("a session runner must never be called: %v", err)
	}
	if _, err := (Config{}).Model(RoleSmall); err == nil {
		t.Error("an unmapped role must be an error")
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	os.WriteFile(p, []byte(`{"roles":{"small":"claude:x","large":"codex:y"},"candidates":[{"tier":"claude:x","cost":2}],"runners":{"agy-session":{"session":true}}}`), 0o644)
	c, err := LoadConfig(p)
	if err != nil || c.Roles[RoleLarge] != "codex:y" || c.Cost("claude:x") != 2 || !c.Runners["agy-session"].Session || len(c.Runners["claude"].Argv) == 0 {
		t.Fatalf("%+v %v", c, err)
	}
	os.WriteFile(p, []byte(`{"roles":{},"model":"x"}`), 0o644)
	if _, err := LoadConfig(p); err == nil {
		t.Error("unknown fields must be refused")
	}
}

// A retired model fails safe: the runner says "unknown model", the call is an
// error (not an invalid output), the answer is not applied, and the other
// role's model is never tried in its place.
func TestRetiredModelFailsSafe(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "other-model-was-called")
	cfg := testConfig(`cat >/dev/null; echo "error: unknown model" >&2; exit 1`, `touch `+marker+`; cat >/dev/null; echo '{"operation":"add","targets":[],"reason":"r"}'`)
	cfg.Roles[RoleLarge] = "live:b"
	p, err := NewPipeline(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, role, err := p.Classify(context.Background(), ClassifyRequest{Observation: "o"})
	if err == nil || IsInvalid(err) || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("want a failed-call error, got %v", err)
	}
	if a.Operation != "" || role != RoleSmall {
		t.Errorf("nothing may be applied: %+v from %s", a, role)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("the large model was called as a fallback")
	}
	if _, err := p.Patch(context.Background(), PatchRequest{Body: "b"}); err == nil || IsInvalid(err) {
		t.Errorf("patch: %v", err)
	}
}

func TestPipelineEscalatesSupersedeAndMergeOnly(t *testing.T) {
	small := func(op string) string {
		tg := `[]`
		if op == "patch" || op == "supersede" {
			tg = `["m"]`
		}
		return `cat >/dev/null; echo '{"operation":"` + op + `","targets":` + tg + `,"reason":"small"}'`
	}
	large := `cat >/dev/null; echo '{"operation":"no-op","targets":[],"reason":"large"}'`
	for op, want := range map[string]struct {
		role Role
		op   Operation
	}{
		"add":       {RoleSmall, OpAdd},
		"patch":     {RoleSmall, OpPatch},
		"merge":     {RoleLarge, OpNoop},
		"supersede": {RoleLarge, OpNoop},
	} {
		cfg := testConfig(small(op), large)
		cfg.Roles[RoleLarge] = "live:b"
		cfg.Runners["fake"] = fakeRunner(small(op))
		p, _ := NewPipeline(cfg)
		req := ClassifyRequest{Observation: "o", Memories: []Snippet{{Name: "m"}}}
		a, role, err := p.Classify(context.Background(), req)
		if err != nil || role != want.role || a.Operation != want.op {
			t.Errorf("%s: %+v via %s, %v", op, a, role, err)
		}
	}
	if errors.Is(errors.New("x"), ErrInvalid) {
		t.Fatal("unreachable")
	}
}
