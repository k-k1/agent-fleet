package mcpproj

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeF(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readF(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPlanNeverWrites: the single most important invariant of the plan half —
// docs/log/56 §5 "書かずに結果を計算する".
func TestPlanNeverWrites(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{"syosetu":{"type":"stdio","command":"uv","args":["${HOME}/x"]}}}`)

	before := readF(t, dir, ".mcp.json")
	_, err := Plan(dir, []Op{{
		Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "syosetu"},
		To: &OpFileRef{File: "opencode.json"}, OnConflict: "overwrite", Dialect: "translate",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.json")); !os.IsNotExist(err) {
		t.Fatalf("Plan must not create opencode.json")
	}
	if readF(t, dir, ".mcp.json") != before {
		t.Fatalf(".mcp.json touched by Plan")
	}
}

// TestApplyCopyNovelLabTranslate replays docs/log/56 §1 end to end: copy claude's
// syosetu into a BRAND NEW opencode.json, translated into opencode's dialect.
func TestApplyCopyNovelLabTranslate(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{"syosetu":{"type":"stdio","command":"uv",
	  "args":["run","--quiet","${HOME}/repos/narou-mcp-stdio/narou_mcp.py"],
	  "env":{"SYOSETU_MIN_INTERVAL":"0.7"}}}}`)

	plan, err := Plan(dir, []Op{{
		Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "syosetu"},
		To: &OpFileRef{File: "opencode.json"}, Dialect: "translate", WithSecrets: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Ops) != 1 || plan.Ops[0].Status != "ok" {
		t.Fatalf("plan: %+v", plan.Ops)
	}
	if plan.Ops[0].After.Args[2] != "{env:HOME}/repos/narou-mcp-stdio/narou_mcp.py" {
		t.Fatalf("preview not translated: %+v", plan.Ops[0].After)
	}

	applied, err := Apply(dir, []Op{{
		Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "syosetu"},
		To: &OpFileRef{File: "opencode.json"}, Dialect: "translate", WithSecrets: true,
	}}, plan.PlanHash)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Ops[0].Status != "ok" {
		t.Fatalf("apply: %+v", applied.Ops[0])
	}
	out := readF(t, dir, "opencode.json")
	if !strings.Contains(out, "{env:HOME}/repos/narou-mcp-stdio/narou_mcp.py") {
		t.Fatalf("opencode.json missing translated value:\n%s", out)
	}
	if !strings.Contains(out, `"$schema"`) {
		t.Fatalf("expected opencode's own seed on a freshly created file:\n%s", out)
	}
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
}

func TestApplyCopyOnConflictOverwrite(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{"srv":{"type":"stdio","command":"new-cmd"}}}`)
	writeF(t, dir, "opencode.json", `{"mcp":{"srv":{"type":"local","command":["old-cmd"],"enabled":true}}}`)

	op := Op{Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "srv"},
		To: &OpFileRef{File: "opencode.json"}, OnConflict: "overwrite", Dialect: "as-is"}
	plan, err := Plan(dir, []Op{op})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ops[0].Before == nil || plan.Ops[0].Before.Command != "old-cmd" {
		t.Fatalf("expected Before to show the old entry: %+v", plan.Ops[0])
	}
	applied, err := Apply(dir, []Op{op}, plan.PlanHash)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Ops[0].Status != "ok" {
		t.Fatalf("apply: %+v", applied.Ops[0])
	}
	out := readF(t, dir, "opencode.json")
	if strings.Contains(out, "old-cmd") || !strings.Contains(out, "new-cmd") {
		t.Fatalf("overwrite did not replace:\n%s", out)
	}
}

func TestApplyCopyOnConflictSkip(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{"srv":{"type":"stdio","command":"new-cmd"}}}`)
	writeF(t, dir, "opencode.json", `{"mcp":{"srv":{"type":"local","command":["old-cmd"],"enabled":true}}}`)

	op := Op{Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "srv"},
		To: &OpFileRef{File: "opencode.json"}, OnConflict: "skip", Dialect: "as-is"}
	plan, err := Plan(dir, []Op{op})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ops[0].Status != "skipped" || plan.Ops[0].Reason != CodeCopyConflict {
		t.Fatalf("expected a skip: %+v", plan.Ops[0])
	}
	before := readF(t, dir, "opencode.json")
	if _, err := Apply(dir, []Op{op}, plan.PlanHash); err != nil {
		t.Fatal(err)
	}
	if readF(t, dir, "opencode.json") != before {
		t.Fatalf("skip must not write")
	}
}

func TestApplyCopyOnConflictRename(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{"srv":{"type":"stdio","command":"new-cmd"}}}`)
	writeF(t, dir, "opencode.json", `{"mcp":{"srv":{"type":"local","command":["old-cmd"],"enabled":true}}}`)

	op := Op{Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "srv"},
		To: &OpFileRef{File: "opencode.json"}, OnConflict: "rename", Dialect: "as-is"}
	plan, err := Plan(dir, []Op{op})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ops[0].ResolvedName != "srv-2" {
		t.Fatalf("expected auto-renamed to srv-2, got %+v", plan.Ops[0])
	}
	if _, err := Apply(dir, []Op{op}, plan.PlanHash); err != nil {
		t.Fatal(err)
	}
	out := readF(t, dir, "opencode.json")
	if !strings.Contains(out, "old-cmd") || !strings.Contains(out, "new-cmd") || !strings.Contains(out, "srv-2") {
		t.Fatalf("expected BOTH entries to survive under different names:\n%s", out)
	}
}

func TestApplyCopyWithoutSecretsEmptiesValues(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{"srv":{"type":"http","url":"https://example.com",
	  "headers":{"Authorization":"Bearer super-secret-token"}}}}`)

	op := Op{Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "srv"},
		To: &OpFileRef{File: "opencode.json"}, OnConflict: "overwrite", Dialect: "as-is", WithSecrets: false}
	plan, err := Plan(dir, []Op{op})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(dir, []Op{op}, plan.PlanHash); err != nil {
		t.Fatal(err)
	}
	out := readF(t, dir, "opencode.json")
	if strings.Contains(out, "super-secret-token") {
		t.Fatalf("withSecrets=false must not write the real value:\n%s", out)
	}
	if !strings.Contains(out, `"Authorization"`) {
		t.Fatalf("expected the header KEY to still be copied:\n%s", out)
	}
}

// TestPlanResultNeverLeaksSecrets is the independent assertion docs/log/56 §13 calls
// for, applied to plan/apply's wire response (not just GET's).
func TestPlanResultNeverLeaksSecrets(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	const secret = "af-test-fixture-1234567890abcdefx"
	writeF(t, dir, ".mcp.json", `{"mcpServers":{"srv":{"type":"http","url":"https://example.com",
	  "headers":{"Authorization":"Bearer `+secret+`"}}}}`)

	op := Op{Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "srv"},
		To: &OpFileRef{File: "opencode.json"}, OnConflict: "overwrite", Dialect: "as-is", WithSecrets: true}
	plan, err := Plan(dir, []Op{op})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("secret leaked into plan response: %s", b)
	}
	applied, err := Apply(dir, []Op{op}, plan.PlanHash)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(applied)
	if strings.Contains(string(b2), secret) {
		t.Fatalf("secret leaked into apply response: %s", b2)
	}
	// The real value SHOULD be on disk (withSecrets=true means the file itself
	// carries it, same as any hand-written config) — only the WIRE response masks.
	if !strings.Contains(readF(t, dir, "opencode.json"), secret) {
		t.Fatalf("expected the real secret ON DISK when withSecrets=true")
	}
}

func TestApplyRefusesStalePlan(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{"srv":{"type":"stdio","command":"a"}}}`)

	op := Op{Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "srv"},
		To: &OpFileRef{File: "opencode.json"}, OnConflict: "overwrite", Dialect: "as-is"}
	plan, err := Plan(dir, []Op{op})
	if err != nil {
		t.Fatal(err)
	}
	// Someone else touches the DESTINATION between plan and apply.
	writeF(t, dir, "opencode.json", `{"mcp":{"unrelated":{"type":"local","command":["x"],"enabled":true}}}`)

	_, err = Apply(dir, []Op{op}, plan.PlanHash)
	if err != ErrPlanStale {
		t.Fatalf("expected ErrPlanStale, got %v", err)
	}
	// The concurrent edit must survive untouched — apply refused, not partial.
	if !strings.Contains(readF(t, dir, "opencode.json"), "unrelated") {
		t.Fatalf("concurrent edit was clobbered")
	}
}

func TestApplyAcceptsFreshPlan(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{"srv":{"type":"stdio","command":"a"}}}`)
	op := Op{Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "srv"},
		To: &OpFileRef{File: "opencode.json"}, OnConflict: "overwrite", Dialect: "as-is"}
	plan, err := Plan(dir, []Op{op})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(dir, []Op{op}, plan.PlanHash); err != nil {
		t.Fatalf("unexpected staleness on an untouched plan: %v", err)
	}
}

func TestApplyIgnoreExclude(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{}}`)

	op := Op{Op: "ignore", File: ".mcp.json", Where: "exclude"}
	plan, err := Plan(dir, []Op{op})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ops[0].AlreadyPresent {
		t.Fatalf("should not be present yet: %+v", plan.Ops[0])
	}
	excludePath := filepath.Join(dir, ".git", "info", "exclude")
	// `git init` itself seeds this file with a boilerplate comment template, so
	// Plan-must-not-write is checked by content (the pattern must be absent), not
	// by non-existence.
	if strings.Contains(readF(t, dir, filepath.Join(".git", "info", "exclude")), ".mcp.json") {
		t.Fatalf("Plan must not write the exclude file")
	}
	if _, err := Apply(dir, []Op{op}, plan.PlanHash); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), ".mcp.json") {
		t.Fatalf("pattern not written: %q", b)
	}

	// A second apply is idempotent (AlreadyPresent), not a duplicate line.
	plan2, err := Plan(dir, []Op{op})
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.Ops[0].AlreadyPresent {
		t.Fatalf("expected AlreadyPresent on the second pass: %+v", plan2.Ops[0])
	}
}

func TestApplyIgnoreGitignoreNeverCommits(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	op := Op{Op: "ignore", File: ".mcp.json", Where: "gitignore"}
	plan, err := Plan(dir, []Op{op})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(dir, []Op{op}, plan.PlanHash); err != nil {
		t.Fatal(err)
	}
	status := gitStatusPorcelain(t, dir)
	if !strings.Contains(status, "?? .gitignore") {
		t.Fatalf("expected .gitignore to be untracked/unstaged (mcpproj never commits): %q", status)
	}
}

func TestPlanCopySourceMissingIsAnError(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{}}`)
	plan, err := Plan(dir, []Op{{
		Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "nope"},
		To: &OpFileRef{File: "opencode.json"}, OnConflict: "overwrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ops[0].Status != "error" || plan.Ops[0].Reason != CodeCopySourceMissing {
		t.Fatalf("got %+v", plan.Ops[0])
	}
}

func TestPlanUnreadableDestIsAnError(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{"srv":{"type":"stdio","command":"a"}}}`)
	writeF(t, dir, "opencode.json", `{not json`)
	plan, err := Plan(dir, []Op{{
		Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "srv"},
		To: &OpFileRef{File: "opencode.json"}, OnConflict: "overwrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ops[0].Status != "error" || plan.Ops[0].Reason != CodeCopyDestUnreadable {
		t.Fatalf("got %+v", plan.Ops[0])
	}
	if readF(t, dir, "opencode.json") != "{not json" {
		t.Fatalf("unreadable dest must never be touched")
	}
}

// TestApplyCopyCreatesNewFilePrettyPrinted pins a real bug found by manual
// testing against ~/repos/novel-lab: a brand-new file with NO seed (cursor, kiro,
// .github/mcp.json) used to come out fully compact/minified — no CLI actually
// writes MCP config that way — because the empty-object seed had no newline, and
// UpsertJSONEntry's compact-source detection (deliberately meant for a source file
// that IS already single-line) mistook "nothing here yet" for "this file's own
// style is minified".
func TestApplyCopyCreatesNewFilePrettyPrinted(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{"srv":{"type":"stdio","command":"a"}}}`)
	op := Op{Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "srv"},
		To: &OpFileRef{File: ".cursor/mcp.json"}, OnConflict: "overwrite"}
	plan, err := Plan(dir, []Op{op})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(dir, []Op{op}, plan.PlanHash); err != nil {
		t.Fatal(err)
	}
	out := readF(t, dir, ".cursor/mcp.json")
	if !strings.Contains(out, "\n") {
		t.Fatalf("expected a pretty-printed new file, got compact: %q", out)
	}
	if !strings.Contains(out, "  \"mcpServers\"") {
		t.Fatalf("expected 2-space indentation, got:\n%s", out)
	}
}

func TestApplyCopyIntoCodexTOML(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{"srv":{"type":"stdio","command":"uv","args":["run","x.py"],
	  "env":{"FOO":"bar"}}}}`)
	op := Op{Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "srv"},
		To: &OpFileRef{File: ".codex/config.toml"}, OnConflict: "overwrite", Dialect: "as-is", WithSecrets: true}
	plan, err := Plan(dir, []Op{op})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(dir, []Op{op}, plan.PlanHash); err != nil {
		t.Fatal(err)
	}
	out := readF(t, dir, ".codex/config.toml")
	if !strings.Contains(out, "[mcp_servers.srv]") || !strings.Contains(out, `command = "uv"`) {
		t.Fatalf("codex TOML not written correctly:\n%s", out)
	}
	if !strings.Contains(out, "[mcp_servers.srv.env]") || !strings.Contains(out, `FOO = "bar"`) {
		t.Fatalf("codex env table missing:\n%s", out)
	}
}

func TestApplyTwoCopiesIntoSameFileBothLand(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeF(t, dir, ".mcp.json", `{"mcpServers":{
	  "alpha":{"type":"stdio","command":"a"},
	  "beta":{"type":"stdio","command":"b"}
	}}`)
	ops := []Op{
		{Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "alpha"}, To: &OpFileRef{File: "opencode.json"}, OnConflict: "overwrite"},
		{Op: "copy", From: &OpEntryRef{File: ".mcp.json", Name: "beta"}, To: &OpFileRef{File: "opencode.json"}, OnConflict: "overwrite"},
	}
	plan, err := Plan(dir, ops)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(dir, ops, plan.PlanHash); err != nil {
		t.Fatal(err)
	}
	out := readF(t, dir, "opencode.json")
	if !strings.Contains(out, `"alpha"`) || !strings.Contains(out, `"beta"`) {
		t.Fatalf("expected both entries in one write:\n%s", out)
	}
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
}

func gitStatusPorcelain(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
