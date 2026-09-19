package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadWriteEditRoundTrip(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	ctx := context.Background()

	if _, err := runWrite(ctx, rt, `{"path":"a.txt","content":"hello\nworld\n"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := runRead(ctx, rt, `{"path":"a.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Fatalf("read output = %q", out)
	}

	if _, err := runEdit(ctx, rt, `{"path":"a.txt","old_string":"world","new_string":"there"}`); err != nil {
		t.Fatalf("edit: %v", err)
	}
	out, err = runRead(ctx, rt, `{"path":"a.txt"}`)
	if err != nil {
		t.Fatalf("read after edit: %v", err)
	}
	if !strings.Contains(out, "there") || strings.Contains(out, "world") {
		t.Fatalf("read after edit = %q", out)
	}
}

func TestEditRejectsAmbiguousMatch(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	ctx := context.Background()
	if _, err := runWrite(ctx, rt, `{"path":"a.txt","content":"x x x"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := runEdit(ctx, rt, `{"path":"a.txt","old_string":"x","new_string":"y"}`); err == nil {
		t.Fatal("expected an error for a non-unique old_string without replace_all")
	}
	if _, err := runEdit(ctx, rt, `{"path":"a.txt","old_string":"x","new_string":"y","replace_all":true}`); err != nil {
		t.Fatalf("replace_all edit: %v", err)
	}
}

func TestReadWriteRefuseEscapingCwd(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	ctx := context.Background()
	if _, err := runRead(ctx, rt, `{"path":"../outside.txt"}`); err == nil {
		t.Fatal("read accepted a path outside cwd")
	}
	if _, err := runWrite(ctx, rt, `{"path":"../outside.txt","content":"x"}`); err == nil {
		t.Fatal("write accepted a path outside cwd")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(rt.Cwd), "outside.txt")); err == nil {
		t.Fatal("write actually created a file outside cwd")
	}
}

func TestLsListsEntries(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(rt.Cwd, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rt.Cwd, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runLs(ctx, rt, `{}`)
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(out, "f.txt") || !strings.Contains(out, "sub/") {
		t.Fatalf("ls output = %q", out)
	}
}

func TestGlobFindsFilesRecursively(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(rt.Cwd, "sub", "deeper"), 0o755))
	must(os.WriteFile(filepath.Join(rt.Cwd, "top.go"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(rt.Cwd, "sub", "mid.go"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(rt.Cwd, "sub", "deeper", "leaf.go"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(rt.Cwd, "sub", "deeper", "leaf.txt"), []byte("x"), 0o644))

	out, err := runGlob(ctx, rt, `{"pattern":"**/*.go"}`)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, want := range []string{"top.go", "sub/mid.go", "sub/deeper/leaf.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("glob output missing %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "leaf.txt") {
		t.Errorf("glob output should not include leaf.txt:\n%s", out)
	}
}

func TestGlobSkipsGitDir(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(rt.Cwd, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt.Cwd, ".git", "objects", "pack.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runGlob(ctx, rt, `{"pattern":"**/*.go"}`)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if strings.Contains(out, ".git") {
		t.Fatalf("glob descended into .git: %s", out)
	}
}

func TestGrepFindsMatchesWithLineNumbers(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(rt.Cwd, "f.go"), []byte("package x\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runGrep(ctx, rt, `{"pattern":"func Foo"}`)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(out, "f.go:2:") {
		t.Fatalf("grep output = %q", out)
	}
}

func TestGrepGlobFilter(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(rt.Cwd, "a.go"), []byte("needle"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt.Cwd, "b.txt"), []byte("needle"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runGrep(ctx, rt, `{"pattern":"needle","glob":"*.go"}`)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(out, "a.go") || strings.Contains(out, "b.txt") {
		t.Fatalf("grep glob filter failed: %q", out)
	}
}
