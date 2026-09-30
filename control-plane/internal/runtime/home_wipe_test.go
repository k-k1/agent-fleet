package runtime

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// seedLocalHome builds <dataDir>/home with the keep list, a working copy, and the kind of
// clutter a Clean home exists for.
func seedLocalHome(t *testing.T, dataDir string) {
	t.Helper()
	home := filepath.Join(dataDir, "home")
	for _, dir := range []string{".config/agent-fleet", ".ssh", ".claude", ".codex", "repos/app/.git", ".local/bin", ".cache/npm"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{".config/agent-fleet/secrets.enc", ".git-credentials", ".gitconfig", ".claude.json", "repos/app/README.md", ".local/bin/claude", ".bashrc"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func homeEntries(t *testing.T, dataDir string) []string {
	t.Helper()
	es, err := os.ReadDir(filepath.Join(dataDir, "home"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// The two local adapters are the ones whose home IS <dataDir>/home, so they wipe it where
// it is. Each operation removes exactly its own set: Recreate the working copies, Clean
// home everything but the keep list.
func TestLocalAdaptersWipeTheirHome(t *testing.T) {
	ctx := context.Background()
	adapters := map[string]func(dir string) Runtime{
		"docker": func(dir string) Runtime { return &dockerRuntime{name: "af-ws-acme-alice", dataDir: dir} },
		"native": func(dir string) Runtime { return &nativeRuntime{name: "af-ws-acme-alice", dataDir: dir} },
	}
	keep := []string{".claude", ".claude.json", ".codex", ".config", ".git-credentials", ".gitconfig", ".ssh"}
	for name, mk := range adapters {
		t.Run(name+" recreate removes ~/repos only", func(t *testing.T) {
			dir := t.TempDir()
			seedLocalHome(t, dir)
			if err := WipeHome(ctx, mk(dir), HomeWipeRepos); err != nil {
				t.Fatalf("WipeHome(repos): %v", err)
			}
			got := homeEntries(t, dir)
			want := append([]string{".bashrc", ".cache", ".local"}, keep...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("home after Recreate = %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("home after Recreate = %v, want %v", got, want)
				}
			}
		})
		for op, wipe := range map[string]func(Runtime) error{
			"clean home (member)": func(rt Runtime) error { return WipeHome(ctx, rt, HomeWipeClean) },
			"clean home (admin)":  func(rt Runtime) error { return EraseHome(ctx, rt) },
		} {
			t.Run(name+" "+op+" keeps only the keep list", func(t *testing.T) {
				dir := t.TempDir()
				seedLocalHome(t, dir)
				if err := wipe(mk(dir)); err != nil {
					t.Fatalf("%s: %v", op, err)
				}
				got := homeEntries(t, dir)
				if len(got) != len(keep) {
					t.Fatalf("home after %s = %v, want exactly the keep list %v", op, got, keep)
				}
				for i := range keep {
					if got[i] != keep[i] {
						t.Fatalf("home after %s = %v, want exactly the keep list %v", op, got, keep)
					}
				}
				if _, err := os.Stat(filepath.Join(dir, "home", ".config", "agent-fleet", "secrets.enc")); err != nil {
					t.Errorf("the encrypted connection store did not survive %s: %v", op, err)
				}
			})
		}
		t.Run(name+" a home that was never created is nothing to wipe", func(t *testing.T) {
			dir := t.TempDir()
			for _, what := range []HomeWipe{HomeWipeRepos, HomeWipeClean} {
				if err := WipeHome(ctx, mk(dir), what); err != nil {
					t.Errorf("WipeHome(%s) on a workspace that never started: %v", what, err)
				}
			}
		})
	}
}

// With no data directory, "home/repos" would resolve against the CP's working directory.
func TestLocalWipeRefusesAnEmptyDataDir(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.MkdirAll(filepath.Join(cwd, "home", "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rt := range []Runtime{&dockerRuntime{name: "x"}, &nativeRuntime{name: "x"}} {
		if err := WipeHome(ctx, rt, HomeWipeRepos); err == nil {
			t.Errorf("%T wiped with an empty data directory", rt)
		}
		if err := EraseHome(ctx, rt); err == nil {
			t.Errorf("%T erased with an empty data directory", rt)
		}
	}
	if _, err := os.Stat(filepath.Join(cwd, "home", "repos")); err != nil {
		t.Errorf("a path relative to the working directory was removed: %v", err)
	}
	if err := WipeHome(ctx, &dockerRuntime{name: "x", dataDir: t.TempDir()}, HomeWipe("everything")); err == nil {
		t.Error("an unknown wipe was accepted")
	}
}

// A runtime that does not claim a port gets the sentinel, not a silent success.
func TestHomeWipeOnARuntimeThatCannotReachTheHome(t *testing.T) {
	ctx := context.Background()
	rt := &ecsRuntime{name: "af-ws-acme-alice"}
	if CanWipeHome(rt) || CanEraseHome(rt) {
		t.Fatal("the Fargate adapter claims a home wipe it cannot perform")
	}
	if err := WipeHome(ctx, rt, HomeWipeRepos); err != ErrHomeWipeUnsupported {
		t.Errorf("WipeHome on Fargate = %v, want ErrHomeWipeUnsupported", err)
	}
	if err := EraseHome(ctx, rt); err != ErrHomeWipeUnsupported {
		t.Errorf("EraseHome on Fargate = %v, want ErrHomeWipeUnsupported", err)
	}
	if _, ok, err := HomeBackupsOf(ctx, rt); ok || err != nil {
		t.Errorf("HomeBackupsOf on Fargate = ok %v, err %v; want not supported", ok, err)
	}
	if _, ok, err := DeleteHomeBackups(ctx, rt); ok || err != nil {
		t.Errorf("DeleteHomeBackups on Fargate = ok %v, err %v; want not supported", ok, err)
	}
}

// HomeOperationsOf is what the Console is told, so it has to agree with what the CP will
// actually do for each profile.
func TestHomeOperationsPerProfile(t *testing.T) {
	cases := []struct {
		name string
		f    RuntimeFactory
		want HomeOperations
	}{
		{"docker", &dockerFactory{rootDataDir: StaticRootDataDir("/srv/data", "")}, HomeOperations{Wipe: true, Erase: true}},
		{"native", &nativeFactory{rootDataDir: StaticRootDataDir("/srv/data", "")}, HomeOperations{Wipe: true, Erase: true}},
		{"ecs", &ecsFactory{}, HomeOperations{}},
		{"ecs-ec2", newEC2Harness(t).factory(), HomeOperations{Erase: true, Backups: true}},
	}
	for _, c := range cases {
		if got := HomeOperationsOf(c.f); got != c.want {
			t.Errorf("%s: HomeOperationsOf = %+v, want %+v", c.name, got, c.want)
		}
	}
}
