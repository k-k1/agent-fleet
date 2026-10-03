package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// efsFixture is a file system root with two members' homes and claude-config directories,
// each holding every homeKeep entry and some work. Mount detection answers yes.
func efsFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	prev := isEFSMount
	isEFSMount = func(string) (bool, error) { return true, nil }
	t.Cleanup(func() { isEFSMount = prev })
	for _, id := range []string{"M-1", "M-2"} {
		home := filepath.Join(root, "home", id)
		for name := range homeKeep {
			mustWrite(t, filepath.Join(home, name, "x"))
		}
		mustWrite(t, filepath.Join(home, "repos", "app", "main.go"))
		mustWrite(t, filepath.Join(home, ".cache", "blob"))
		mustWrite(t, filepath.Join(home, ".bashrc"))
		mustWrite(t, filepath.Join(root, "claude-config", id, ".credentials.json"))
	}
	return root
}

func mustWrite(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range des {
		names = append(names, d.Name())
	}
	slices.Sort(names)
	return names
}

// The other member's directories are never touched, whatever the operation.
func assertUntouched(t *testing.T, root, id string) {
	t.Helper()
	want := append(keepNames(), ".bashrc", ".cache", "repos")
	slices.Sort(want)
	if got := entries(t, filepath.Join(root, "home", id)); !slices.Equal(got, want) {
		t.Errorf("%s's home = %v, want it untouched", id, got)
	}
	if got := entries(t, filepath.Join(root, "claude-config", id)); len(got) != 1 {
		t.Errorf("%s's claude-config = %v, want it untouched", id, got)
	}
}

func TestEFSHomeOpRemovesWhatEachOperationRemoves(t *testing.T) {
	ctx := context.Background()

	root := efsFixture(t)
	if err := RunEFSHomeOp(ctx, root, "repos", "M-1"); err != nil {
		t.Fatalf("repos: %v", err)
	}
	want := append(keepNames(), ".bashrc", ".cache")
	slices.Sort(want)
	if got := entries(t, filepath.Join(root, "home", "M-1")); !slices.Equal(got, want) {
		t.Errorf("after repos the home = %v, want %v", got, want)
	}
	assertUntouched(t, root, "M-2")

	// Clean home keeps exactly homeKeep, the same list docker, native and ecs-ec2 keep.
	root = efsFixture(t)
	if err := RunEFSHomeOp(ctx, root, "clean", "M-1"); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if got := entries(t, filepath.Join(root, "home", "M-1")); !slices.Equal(got, keepNames()) {
		t.Errorf("after clean the home = %v, want homeKeep %v", got, keepNames())
	}
	if got := entries(t, filepath.Join(root, "claude-config", "M-1")); len(got) != 1 {
		t.Errorf("clean touched claude-config: %v", got)
	}
	assertUntouched(t, root, "M-2")

	root = efsFixture(t)
	if err := RunEFSHomeOp(ctx, root, "destroy", "M-1"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	for _, p := range []string{filepath.Join(root, "home", "M-1"), filepath.Join(root, "claude-config", "M-1")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("destroy left %s (%v)", p, err)
		}
	}
	assertUntouched(t, root, "M-2")
	// Run again on what is gone: a retry after a partial failure succeeds.
	for _, op := range []string{"repos", "clean", "destroy"} {
		if err := RunEFSHomeOp(ctx, root, op, "M-1"); err != nil {
			t.Errorf("%s on a removed home = %v, want success", op, err)
		}
	}
}

// Every refusal happens before anything is removed, and exits 2.
func TestEFSHomeOpRefusesBeforeRemovingAnything(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ name, root, op, id string }{
		{"empty id", "", "destroy", ""},
		{"dot-dot id", "", "destroy", ".."},
		{"dot id", "", "destroy", "."},
		{"slash id", "", "destroy", "M-1/../M-2"},
		{"relative traversal", "", "clean", "../home"},
		{"unknown op", "", "rm-everything", "M-1"},
		{"empty op", "", "", "M-1"},
		{"relative root", "efs", "destroy", "M-1"},
		{"slash root", "/", "destroy", "M-1"},
	} {
		root := efsFixture(t)
		r := root
		if c.root != "" {
			r = c.root
		}
		err := RunEFSHomeOp(ctx, r, c.op, c.id)
		if !errors.Is(err, errHomeOpRefused) || HomeOpExitCode(err) != HomeOpExitRefused {
			t.Errorf("%s: err = %v (exit %d), want a refusal (exit 2)", c.name, err, HomeOpExitCode(err))
		}
		assertUntouched(t, root, "M-1")
		assertUntouched(t, root, "M-2")
	}
}

// Without the file system mounted, the paths are the container's own empty disk; a removal
// there would succeed on nothing and report a cleaned home.
func TestEFSHomeOpRefusesWhenTheRootIsNotAMount(t *testing.T) {
	root := efsFixture(t)
	isEFSMount = func(string) (bool, error) { return false, nil }
	err := RunEFSHomeOp(context.Background(), root, "destroy", "M-1")
	if HomeOpExitCode(err) != HomeOpExitRefused {
		t.Fatalf("unmounted root: err = %v, want a refusal", err)
	}
	assertUntouched(t, root, "M-1")
}

// A home that is a link is refused rather than followed into whatever it points at.
func TestEFSHomeOpRefusesALinkedHome(t *testing.T) {
	root := efsFixture(t)
	home := filepath.Join(root, "home", "M-3")
	if err := os.Symlink(filepath.Join(root, "home", "M-2"), home); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"repos", "clean", "destroy"} {
		if err := RunEFSHomeOp(context.Background(), root, op, "M-3"); HomeOpExitCode(err) != HomeOpExitRefused {
			t.Errorf("%s on a linked home = %v, want a refusal", op, err)
		}
	}
	assertUntouched(t, root, "M-2")
}

// The real mount detection: a temporary directory is not a mount; a mount that is not NFS
// (a tmpfs left at the path) is refused too, not mistaken for the file system.
func TestEFSMountDetection(t *testing.T) {
	if ok, err := efsMount(t.TempDir()); err != nil || ok {
		t.Errorf("a temp dir reads as a mount (%v, %v)", ok, err)
	}
	if _, err := efsMount(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("an absent directory raised no error")
	}
	for _, p := range []string{"/dev", "/proc", "/sys"} {
		if ok, err := efsMount(p); ok {
			t.Errorf("%s reads as the EFS mount (%v)", p, err)
		}
	}
}

// A link above the member's directory — /home itself — is refused as well: resolving it
// would hand the removal a different tree.
func TestEFSHomeOpRefusesALinkedAncestor(t *testing.T) {
	root := efsFixture(t)
	if err := os.Rename(filepath.Join(root, "home"), filepath.Join(root, "real-home")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "claude-config"), filepath.Join(root, "home")); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"repos", "clean", "destroy"} {
		if err := RunEFSHomeOp(context.Background(), root, op, "M-1"); HomeOpExitCode(err) != HomeOpExitRefused {
			t.Errorf("%s under a linked /home = %v, want a refusal", op, err)
		}
	}
	if got := entries(t, filepath.Join(root, "claude-config", "M-1")); len(got) != 1 {
		t.Errorf("claude-config reached through the link: %v", got)
	}
}

// A link inside the home is removed as a link; what it points at stays.
func TestEFSHomeOpRemovesNestedLinksNotTheirTargets(t *testing.T) {
	root := efsFixture(t)
	if err := os.Symlink(filepath.Join(root, "home", "M-2"), filepath.Join(root, "home", "M-1", "repos", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../M-2", filepath.Join(root, "home", "M-1", "up")); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"repos", "clean", "destroy"} {
		if err := RunEFSHomeOp(context.Background(), root, op, "M-1"); err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		assertUntouched(t, root, "M-2")
	}
}

// The directory is checked and then removed through the handle the check opened. Swapping
// the path for a link to another member's home in between changes nothing: the removal
// still lands in the directory that was checked, wherever it has been moved.
func TestEFSHomeOpFollowsTheHandleNotThePath(t *testing.T) {
	for _, op := range []string{"repos", "clean", "destroy"} {
		root := efsFixture(t)
		moved := filepath.Join(root, "moved-M-1")
		homeOpOpened = func() {
			if err := os.Rename(filepath.Join(root, "home", "M-1"), moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "home", "M-2"), filepath.Join(root, "home", "M-1")); err != nil {
				t.Fatal(err)
			}
		}
		err := RunEFSHomeOp(context.Background(), root, op, "M-1")
		homeOpOpened = func() {}
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		assertUntouched(t, root, "M-2")
		if op != "destroy" {
			if got := entries(t, moved); slices.Contains(got, "repos") {
				t.Errorf("%s: the checked directory kept repos: %v", op, got)
			}
		}
	}
}

// Destroy checks both directories before it removes either: a claude-config it must refuse
// leaves the home in place, so "refused, removed nothing" (exit 2) stays true.
func TestEFSHomeOpDestroyRefusesBeforeRemovingEither(t *testing.T) {
	root := efsFixture(t)
	claude := filepath.Join(root, "claude-config", "M-1")
	if err := os.RemoveAll(claude); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "claude-config", "M-2"), claude); err != nil {
		t.Fatal(err)
	}
	err := RunEFSHomeOp(context.Background(), root, "destroy", "M-1")
	if HomeOpExitCode(err) != HomeOpExitRefused {
		t.Fatalf("destroy with a linked claude-config = %v, want a refusal", err)
	}
	want := append(keepNames(), ".bashrc", ".cache", "repos")
	slices.Sort(want)
	if got := entries(t, filepath.Join(root, "home", "M-1")); !slices.Equal(got, want) {
		t.Errorf("the home went although destroy was refused: %v", got)
	}
}

func TestHomeOpExitCode(t *testing.T) {
	if HomeOpExitCode(nil) != 0 || HomeOpExitCode(errors.New("io")) != 1 ||
		HomeOpExitCode(errHomeOpRefused) != 2 {
		t.Error("exit codes moved; the CP reads them (homeTaskOutcome)")
	}
}
