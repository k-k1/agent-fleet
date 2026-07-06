package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSplitFrontmatter(t *testing.T) {
	meta, body := splitFrontmatter("---\ndescription: \"Do the thing\"\nname: thing\n---\nHello {{repo}}\n")
	if meta["description"] != "Do the thing" || meta["name"] != "thing" {
		t.Fatalf("meta = %#v", meta)
	}
	if body != "Hello {{repo}}\n" {
		t.Fatalf("body = %q", body)
	}
	// No frontmatter → whole content is the body.
	if m, b := splitFrontmatter("just text"); len(m) != 0 || b != "just text" {
		t.Fatalf("no-fm: %#v %q", m, b)
	}
}

func TestReadCommandTemplates(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".claude", "commands", "review.md"),
		"---\ndescription: 変更をレビュー\n---\nこのレポの変更をレビューして。")
	writeFile(t, filepath.Join(dir, ".claude", "commands", "git", "commit.md"),
		"ステージ済みの変更をコミット。") // no frontmatter → label falls back to namespaced id

	items := readCommandTemplates(dir)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d: %#v", len(items), items)
	}
	// Sorted by id: "git/commit" < "review".
	if items[0].ID != "git/commit" || items[0].Label != "git:commit" {
		t.Errorf("item0 = %#v", items[0])
	}
	if items[1].ID != "review" || items[1].Label != "変更をレビュー" || items[1].Body != "このレポの変更をレビューして。" {
		t.Errorf("item1 = %#v", items[1])
	}
}

func TestReadSkillTemplates(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".claude", "skills", "deep-research", "SKILL.md"),
		"---\nname: deep-research\ndescription: Fan-out research harness\n---\nbody ignored")
	items := readSkillTemplates(dir)
	if len(items) != 1 {
		t.Fatalf("want 1, got %d", len(items))
	}
	if items[0].Body != "/deep-research " || items[0].Label != "Fan-out research harness" {
		t.Errorf("skill item = %#v", items[0])
	}
}

func TestReadFileTemplates(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".agent-fleet", "launch-prompts.md"),
		"# Launch prompts\n\n## バグ調査\n再現手順を確認して原因を特定して。\n\n## リファクタ\n重複を整理して。\n")
	items := readFileTemplates(dir)
	if len(items) != 2 {
		t.Fatalf("want 2 sections, got %d: %#v", len(items), items)
	}
	if items[0].Label != "バグ調査" || items[0].Body != "再現手順を確認して原因を特定して。" {
		t.Errorf("section0 = %#v", items[0])
	}
	if items[1].Label != "リファクタ" || items[1].Body != "重複を整理して。" {
		t.Errorf("section1 = %#v", items[1])
	}

	// A file with no "##" headings collapses to a single template.
	dir2 := t.TempDir()
	writeFile(t, filepath.Join(dir2, ".agent-fleet", "launch-prompts.md"), "ただのプロンプト。\n")
	one := readFileTemplates(dir2)
	if len(one) != 1 || one[0].Body != "ただのプロンプト。" {
		t.Fatalf("single = %#v", one)
	}
}

func TestReadTemplatesMissingDirs(t *testing.T) {
	dir := t.TempDir() // empty working copy → all readers return nothing, no error
	if len(readCommandTemplates(dir)) != 0 || len(readSkillTemplates(dir)) != 0 || len(readFileTemplates(dir)) != 0 {
		t.Fatal("expected no templates for an empty repo")
	}
}
