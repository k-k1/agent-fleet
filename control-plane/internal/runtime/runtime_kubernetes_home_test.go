package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

var testEpoch = time.Unix(0, 0).UTC()

// --- the scripts, run by sh against a home in a temporary directory ---

func homeFixture(t *testing.T) (home, record string) {
	t.Helper()
	dir := t.TempDir()
	home, record = filepath.Join(dir, "home"), filepath.Join(dir, "record")
	for _, p := range []string{"repos/app/.git", ".cache/x", ".config/agent-fleet", ".ssh", ".claude", ".codex"} {
		if err := os.MkdirAll(filepath.Join(home, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"notes.txt", ".bashrc", ".gitconfig", ".git-credentials", ".claude.json"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(record, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, record
}

func runScript(t *testing.T, script string, env ...string) error {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("script output: %s", out)
	}
	return err
}

func topLevel(t *testing.T, home string) []string {
	t.Helper()
	es, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func keepNames() []string {
	var out []string
	for n := range homeKeep {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Clean home keeps exactly the seven homeKeep names, whatever each one is; the record
// makes a restarted pod remove nothing; a newer Recreate removes ~/repos alone.
func TestHomeWipeScriptCarriesOutEachGenerationOnce(t *testing.T) {
	home, record := homeFixture(t)
	script := homeWipeScript(home, record)
	if err := runScript(t, script, "AF_WIPE_CLEAN=3", "AF_WIPE_REPOS=0"); err != nil {
		t.Fatal(err)
	}
	if got, want := topLevel(t, home), keepNames(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("after Clean home: %v, want %v", got, want)
	}
	// The member works on; the pod restarts with the same template.
	_ = os.MkdirAll(filepath.Join(home, "repos", "new"), 0o755)
	_ = os.WriteFile(filepath.Join(home, "work.txt"), []byte("x"), 0o644)
	if err := runScript(t, script, "AF_WIPE_CLEAN=3", "AF_WIPE_REPOS=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "work.txt")); err != nil {
		t.Fatal("a restart with the same generation removed the member's work")
	}
	// A Recreate requested after it: ~/repos only.
	if err := runScript(t, script, "AF_WIPE_CLEAN=3", "AF_WIPE_REPOS=4"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "repos")); !os.IsNotExist(err) {
		t.Fatal("the Recreate left ~/repos")
	}
	if _, err := os.Stat(filepath.Join(home, "work.txt")); err != nil {
		t.Fatal("the Recreate removed more than ~/repos")
	}
	for k, v := range map[string]string{"clean": "3", "repos": "4"} {
		b, _ := os.ReadFile(filepath.Join(record, k))
		if strings.TrimSpace(string(b)) != v {
			t.Errorf("record %s = %q, want %s", k, b, v)
		}
	}
}

// A record that is not a number stops the script before it removes anything.
func TestHomeWipeScriptFailsClosedOnABadRecord(t *testing.T) {
	home, record := homeFixture(t)
	_ = os.WriteFile(filepath.Join(record, "clean"), []byte("garbage"), 0o644)
	if err := runScript(t, homeWipeScript(home, record), "AF_WIPE_CLEAN=1", "AF_WIPE_REPOS=1"); err == nil {
		t.Fatal("the script accepted a record that is not a number")
	}
	if _, err := os.Stat(filepath.Join(home, "notes.txt")); err != nil {
		t.Fatal("the script removed files before it failed")
	}
	if _, err := os.Stat(filepath.Join(home, "repos")); err != nil {
		t.Fatal("the script removed ~/repos before it failed")
	}
}

// Every record that is not a plain number fails both scripts before anything is removed:
// a number too long to compare, a directory, a symbolic link, an empty file, a missing
// record directory.
func TestHomeScriptsFailClosedOnEveryBadRecord(t *testing.T) {
	for name, spoil := range map[string]func(t *testing.T, record string){
		"huge clean number": func(t *testing.T, r string) {
			_ = os.WriteFile(filepath.Join(r, "clean"), []byte("9999999999999999999999999999999999999999"), 0o644)
		},
		"clean is a directory": func(t *testing.T, r string) { _ = os.MkdirAll(filepath.Join(r, "clean"), 0o755) },
		"repos is a symlink": func(t *testing.T, r string) {
			_ = os.WriteFile(filepath.Join(r, "target"), []byte("1"), 0o644)
			_ = os.Symlink(filepath.Join(r, "target"), filepath.Join(r, "repos"))
		},
		"dangling symlink":    func(t *testing.T, r string) { _ = os.Symlink(filepath.Join(r, "nowhere"), filepath.Join(r, "clean")) },
		"empty record":        func(t *testing.T, r string) { _ = os.WriteFile(filepath.Join(r, "repos"), nil, 0o644) },
		"no record directory": func(t *testing.T, r string) { _ = os.RemoveAll(r) },
	} {
		for _, script := range []string{"wipe", "erase"} {
			if script == "erase" && name == "no record directory" {
				continue // the erase runs without a state claim, so that one is allowed
			}
			t.Run(script+"/"+name, func(t *testing.T) {
				home, record := homeFixture(t)
				spoil(t, record)
				sh := homeWipeScript(home, record)
				if script == "erase" {
					sh = homeEraseScript(home, record)
				}
				if err := runScript(t, sh, "AF_WIPE_CLEAN=5", "AF_WIPE_REPOS=6"); err == nil {
					t.Fatal("the script accepted the record")
				}
				for _, p := range []string{"notes.txt", "repos/app"} {
					if _, err := os.Stat(filepath.Join(home, p)); err != nil {
						t.Fatalf("%s was removed before the script failed", p)
					}
				}
			})
		}
	}
	// A request out of range fails as well.
	home, record := homeFixture(t)
	if err := runScript(t, homeWipeScript(home, record), "AF_WIPE_CLEAN=99999999999999999999", "AF_WIPE_REPOS=1"); err == nil {
		t.Fatal("an out-of-range request was accepted")
	}
}

// The erase removes what Clean home removes, and records the pending marks as done.
func TestHomeEraseScript(t *testing.T) {
	home, record := homeFixture(t)
	if err := runScript(t, homeEraseScript(home, record), "AF_WIPE_CLEAN=2", "AF_WIPE_REPOS=5"); err != nil {
		t.Fatal(err)
	}
	if got, want := topLevel(t, home), keepNames(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("after the erase: %v, want %v", got, want)
	}
	b, _ := os.ReadFile(filepath.Join(record, "repos"))
	if strings.TrimSpace(string(b)) != "5" {
		t.Fatalf("repos record = %q", b)
	}
	// Without the state claim there is no record directory, and that is not an error.
	home2, record2 := homeFixture(t)
	_ = os.RemoveAll(record2)
	if err := runScript(t, homeEraseScript(home2, record2), "AF_WIPE_CLEAN=0", "AF_WIPE_REPOS=0"); err != nil {
		t.Fatal(err)
	}
}

// --- WipeHome against recorded replies ---

// The mark is a generation per kind, written as a merge patch conditional on the
// resource version; a conflict (the controller's status write in between) is read again.
func TestKubeWipeHomeMarksTheStatefulSet(t *testing.T) {
	rt, f := fakeKubeRuntime(t)
	sts := `{"metadata":{"name":"af-ws-x","resourceVersion":"41","generation":6,"annotations":{"agent-fleet.io/home-wipe-generation":"2","agent-fleet.io/home-wipe-clean":"2"}},"spec":{"replicas":0,"template":{"metadata":{},"spec":{"containers":[]}}},"status":{"replicas":0}}`
	f.set(stsPathX, 200, sts)
	var bodies []string
	conflicts := 1
	rt.c.hc.Transport = roundTripHook{rt.c.hc.Transport, func(r *http.Request) {
		if r.Method == http.MethodPatch {
			b, _ := r.GetBody()
			raw := make([]byte, 4096)
			n, _ := b.Read(raw)
			bodies = append(bodies, string(raw[:n]))
			if conflicts > 0 {
				conflicts--
				f.set("PATCH /apis/apps/v1/namespaces/ns/statefulsets/af-ws-x", 409, `{"kind":"Status","reason":"Conflict","code":409}`)
			} else {
				f.set("PATCH /apis/apps/v1/namespaces/ns/statefulsets/af-ws-x", 200, sts)
			}
		}
	}}
	if err := rt.WipeHome(context.Background(), HomeWipeRepos); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("patches = %v, want a retry after the conflict", bodies)
	}
	var p struct {
		Metadata struct {
			ResourceVersion string            `json:"resourceVersion"`
			Annotations     map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	_ = json.Unmarshal([]byte(bodies[1]), &p)
	if p.Metadata.ResourceVersion != "41" || p.Metadata.Annotations[kubeAnnWipeGen] != "3" ||
		p.Metadata.Annotations[kubeAnnWipePrefix+"repos"] != "3" {
		t.Fatalf("patch = %s", bodies[1])
	}
	if _, ok := p.Metadata.Annotations[kubeAnnWipePrefix+"clean"]; ok {
		t.Fatal("a Recreate touched the pending Clean home mark")
	}
	if err := rt.WipeHome(context.Background(), "everything"); err == nil {
		t.Fatal("an unknown wipe was accepted")
	}
}

// The next Start's template carries the wipe as an init container in the same restricted
// shape as the agent, with the generations the marks hold.
func TestKubeAddHomeWipe(t *testing.T) {
	f := &kubeFactory{cfg: &kubeConfig{namespace: "ns", image: "img:1", serviceAccount: "default"}}
	rt := f.New(Workspace{ContainerName: "af-ws-x"}, "", nil).(*kubeRuntime)
	tmpl := rt.podTemplate("img:1@sha256:"+strings.Repeat("0", 64), 2, testEpoch)
	rt.addHomeWipe(&tmpl, map[string]string{})
	if len(tmpl.Spec.InitContainers) != 1 || tmpl.Spec.InitContainers[0].Name != kubeLayoutContainer {
		t.Fatalf("init containers without a mark = %+v", tmpl.Spec.InitContainers)
	}
	rt.addHomeWipe(&tmpl, map[string]string{kubeAnnWipePrefix + "clean": "4", kubeAnnWipePrefix + "repos": "5"})
	ic := tmpl.Spec.InitContainers
	// The wipe runs on the home the layout step has made, so it comes second.
	if len(ic) != 2 || ic[0].Name != kubeLayoutContainer || ic[1].Name != kubeWipeContainer ||
		ic[1].Image != tmpl.Spec.Containers[0].Image || ic[1].SecurityContext != tmpl.Spec.Containers[0].SecurityContext {
		t.Fatalf("init containers = %+v", ic)
	}
	env := map[string]string{}
	for _, e := range ic[1].Env {
		env[e.Name] = e.Value
	}
	if env["AF_WIPE_CLEAN"] != "4" || env["AF_WIPE_REPOS"] != "5" {
		t.Fatalf("env = %v", env)
	}
	for _, m := range tmpl.Spec.Containers[0].VolumeMounts {
		if m.SubPath == kubeWipeRecordSubPath {
			t.Fatal("the workspace container mounts the wipe record: a member could rewrite it")
		}
	}
}

// --- the home's layout on its claim (#1543) ---

// layoutVolume is a claim's root as the kubelet leaves it with fsGroup — group-writable
// and setgid — holding a home of the earlier layout, where the root was the home.
func layoutVolume(t *testing.T) string {
	t.Helper()
	vol := filepath.Join(t.TempDir(), "vol")
	for _, p := range []string{"repos/app/.git", ".config/agent-fleet", ".local/state", "lost+found", "go/pkg/mod/m@v1"} {
		if err := os.MkdirAll(filepath.Join(vol, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"notes.txt", ".bashrc", "..odd", "-dash"} {
		if err := os.WriteFile(filepath.Join(vol, f), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/var/lib/af/keep/.ssh", filepath.Join(vol, ".ssh")); err != nil {
		t.Fatal(err)
	}
	// Go's module cache is read-only; moving a directory needs write permission on it.
	if err := os.Chmod(filepath.Join(vol, "go/pkg/mod/m@v1"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(vol, "go"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(vol, os.ModeSetgid|0o775); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.Walk(filepath.Dir(vol), func(p string, fi os.FileInfo, err error) error {
			if err == nil && fi.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	return vol
}

// assertPrivateHome is the part of cloudexec.PrivateDir's ancestor walk the home decides:
// owned by this user and writable by nobody else.
func assertPrivateHome(t *testing.T, home string) {
	t.Helper()
	fi, err := os.Lstat(home)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o022 != 0 {
		t.Fatalf("home %s: dir=%v uid=%d mode=%v; want a directory of uid %d without group or other write",
			home, fi.IsDir(), st.Uid, fi.Mode(), os.Getuid())
	}
}

// An existing home moves into the subdirectory whole — hidden names, a link, a read-only
// directory — and comes out private; lost+found stays on the root; a second start changes
// nothing.
func TestKubeHomeLayoutMovesAnExistingHome(t *testing.T) {
	vol := layoutVolume(t)
	home := filepath.Join(vol, kubeHomeSubPath)
	for run := 1; run <= 2; run++ {
		if err := runScript(t, "umask 002\n"+homeLayoutScript(vol)); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if got := topLevel(t, vol); !reflect.DeepEqual(got, []string{kubeHomeSubPath, "lost+found"}) {
			t.Fatalf("run %d: the claim's root holds %v", run, got)
		}
		want := []string{"-dash", "..odd", ".bashrc", ".config", ".local", ".ssh", "go", "notes.txt", "repos"}
		if got := topLevel(t, home); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: the home holds %v, want %v", run, got, want)
		}
		assertPrivateHome(t, home)
	}
	if b, err := os.ReadFile(filepath.Join(home, "..odd")); err != nil || string(b) != "..odd" {
		t.Fatalf("..odd = %q, %v", b, err)
	}
	if l, err := os.Readlink(filepath.Join(home, ".ssh")); err != nil || l != "/var/lib/af/keep/.ssh" {
		t.Fatalf(".ssh link = %q, %v", l, err)
	}
	if fi, err := os.Stat(filepath.Join(home, "go")); err != nil || fi.Mode().Perm() != 0o555 {
		t.Fatalf("the read-only directory came out %v, %v", fi.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(home, "repos/app/.git")); err != nil {
		t.Fatal(err)
	}
}

// A new claim gets an empty private home; one left group-writable (a recursive fsGroup
// change) is made private again.
func TestKubeHomeLayoutFreshAndRegroupedClaims(t *testing.T) {
	vol := filepath.Join(t.TempDir(), "vol")
	if err := os.MkdirAll(filepath.Join(vol, "lost+found"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(vol, os.ModeSetgid|0o775); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(vol, kubeHomeSubPath)
	if err := runScript(t, "umask 002\n"+homeLayoutScript(vol)); err != nil {
		t.Fatal(err)
	}
	if got := topLevel(t, home); len(got) != 0 {
		t.Fatalf("a new home holds %v", got)
	}
	assertPrivateHome(t, home)
	if err := os.Chmod(home, os.ModeSetgid|0o775); err != nil {
		t.Fatal(err)
	}
	if err := runScript(t, homeLayoutScript(vol)); err != nil {
		t.Fatal(err)
	}
	assertPrivateHome(t, home)
}

// A start stopped halfway through the move finishes it at the next start; the home is
// never visible with only part of its files.
func TestKubeHomeLayoutResumesAnInterruptedMove(t *testing.T) {
	vol := layoutVolume(t)
	part := filepath.Join(vol, kubeHomeSubPath+".new")
	if err := os.Mkdir(part, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(vol, "repos"), filepath.Join(part, "repos")); err != nil {
		t.Fatal(err)
	}
	if err := runScript(t, homeLayoutScript(vol)); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(vol, kubeHomeSubPath)
	if got := topLevel(t, vol); !reflect.DeepEqual(got, []string{kubeHomeSubPath, "lost+found"}) {
		t.Fatalf("the claim's root holds %v", got)
	}
	for _, p := range []string{"repos/app/.git", "notes.txt", ".config/agent-fleet"} {
		if _, err := os.Stat(filepath.Join(home, p)); err != nil {
			t.Fatal(err)
		}
	}
}

// The erase pod runs the layout and then the Clean home on the claim's root, so an
// administrator's Clean home of a claim of the earlier layout removes the home's files and
// not lost+found, which dev cannot remove.
func TestKubeEraseScriptOnAnEarlierLayout(t *testing.T) {
	vol := layoutVolume(t)
	// Out of this test: rm as dev cannot remove a read-only directory's contents.
	for _, d := range []string{"go", "go/pkg/mod/m@v1"} {
		if err := os.Chmod(filepath.Join(vol, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	record := t.TempDir()
	home := filepath.Join(vol, kubeHomeSubPath)
	sh := homeLayoutScript(vol) + "\n" + homeEraseScript(home, record)
	if err := runScript(t, sh, "AF_WIPE_CLEAN=1", "AF_WIPE_REPOS=0"); err != nil {
		t.Fatal(err)
	}
	if got := topLevel(t, vol); !reflect.DeepEqual(got, []string{kubeHomeSubPath, "lost+found"}) {
		t.Fatalf("the claim's root holds %v", got)
	}
	if got := topLevel(t, home); !reflect.DeepEqual(got, []string{".config", ".ssh"}) {
		t.Fatalf("the home after Clean home holds %v", got)
	}
	assertPrivateHome(t, home)
}

// What the layout cannot make into a home stops the pod rather than mounting something else.
func TestKubeHomeLayoutRefusesALinkOrAFile(t *testing.T) {
	for _, mk := range []func(string) error{
		func(p string) error { return os.Symlink("/tmp", p) },
		func(p string) error { return os.WriteFile(p, []byte("x"), 0o644) },
	} {
		vol := t.TempDir()
		if err := mk(filepath.Join(vol, kubeHomeSubPath)); err != nil {
			t.Fatal(err)
		}
		if err := runScript(t, homeLayoutScript(vol)); err == nil {
			t.Fatal("the layout accepted a home that is not a directory")
		}
	}
}

// Every container that mounts the home through subPath starts after the layout step, which
// alone mounts the claim's root; the agent never sees the root.
func TestKubePodMountsTheHomeThroughTheLayout(t *testing.T) {
	f := &kubeFactory{cfg: &kubeConfig{namespace: "ns", image: "img:1", serviceAccount: "default"}}
	rt := f.New(Workspace{ContainerName: "af-ws-x"}, "", nil).(*kubeRuntime)
	tmpl := rt.podTemplate("img:1@sha256:"+strings.Repeat("0", 64), 2, testEpoch)
	rt.addHomeWipe(&tmpl, map[string]string{kubeAnnWipePrefix + "repos": "1"})
	homeMounts := func(c kContainer) []kVolumeMount {
		var out []kVolumeMount
		for _, m := range c.VolumeMounts {
			if m.Name == "home" {
				out = append(out, m)
			}
		}
		return out
	}
	ic := tmpl.Spec.InitContainers
	if len(ic) == 0 || ic[0].Name != kubeLayoutContainer ||
		!reflect.DeepEqual(homeMounts(ic[0]), []kVolumeMount{{Name: "home", MountPath: kubeHomeVolumePath}}) {
		t.Fatalf("the first init container is not the layout on the claim's root: %+v", ic)
	}
	if ic[0].SecurityContext != tmpl.Spec.Containers[0].SecurityContext {
		t.Fatal("the layout step does not run with the agent's security settings")
	}
	for _, c := range append(ic[1:], tmpl.Spec.Containers...) {
		want := []kVolumeMount{{Name: "home", MountPath: kubeHomePath, SubPath: kubeHomeSubPath}}
		if got := homeMounts(c); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s mounts the home as %+v, want %+v", c.Name, got, want)
		}
	}
}

// --- ResizeHome against recorded replies ---

func TestKubeResizeHome(t *testing.T) {
	claim := func(req, capacity, cond string) string {
		c := ""
		if cond != "" {
			c = `,"conditions":[{"type":"` + cond + `","status":"True"}]`
		}
		return `{"metadata":{"name":"af-ws-x-home"},"spec":{"accessModes":["ReadWriteOnce"],"resources":{"requests":{"storage":"` + req +
			`"}},"volumeName":"pv-1"},"status":{"phase":"Bound","capacity":{"storage":"` + capacity + `"}` + c + `}}`
	}
	const get = "GET /api/v1/namespaces/ns/persistentvolumeclaims/af-ws-x-home"
	const patch = "PATCH /api/v1/namespaces/ns/persistentvolumeclaims/af-ws-x-home"
	for _, c := range []struct {
		name      string
		claim     string
		patchCode int
		want      string
		wantPatch bool
	}{
		{"no home yet", "", 0, HomeResizeNoHome, false},
		{"grow", claim("10Gi", "10Gi", ""), 200, HomeResizeGrowing, true},
		{"refused", claim("10Gi", "10Gi", ""), 403, HomeResizeFailed, true},
		{"same and done", claim("20Gi", "20Gi", ""), 0, HomeResizeSame, false},
		{"same, file system pending", claim("20Gi", "20Gi", "FileSystemResizePending"), 0, HomeResizeGrowing, false},
		{"same, capacity behind", claim("20Gi", "10Gi", ""), 0, HomeResizeGrowing, false},
		{"shrink", claim("30Gi", "30Gi", ""), 0, HomeResizeShrink, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt, f := fakeKubeRuntime(t)
			rt.homeGiB = 20
			if c.claim != "" {
				f.set(get, 200, c.claim)
			}
			if c.patchCode != 0 {
				f.set(patch, c.patchCode, `{"kind":"Status","message":"forbidden: only dynamically provisioned pvc can be resized","code":`+itoa(c.patchCode)+`}`)
			}
			hr, err := rt.ResizeHome(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if hr.Outcome != c.want || f.saw(patch) != c.wantPatch {
				t.Fatalf("ResizeHome = %+v, patched %v; want %s, patched %v", hr, f.saw(patch), c.want, c.wantPatch)
			}
			if c.want == HomeResizeFailed && !strings.Contains(hr.Detail, "only dynamically provisioned") {
				t.Fatalf("Detail = %q, want the API server's refusal", hr.Detail)
			}
		})
	}
}

func TestQuantityBytes(t *testing.T) {
	for in, want := range map[string]int64{"10Gi": 10 * gib, "1Ti": 1024 * gib, "500M": 500e6, "1073741824": gib, "1.5Gi": 3 * gib / 2} {
		if got, ok := quantityBytes(in); !ok || got != want {
			t.Errorf("quantityBytes(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	if _, ok := quantityBytes("ten"); ok {
		t.Error("accepted a non-number")
	}
}

// --- Stale and the StorageClass check ---

// Stale compares the fingerprint Start recorded on the template with what the tag
// resolves to now; anything unknown is false.
func TestKubeStale(t *testing.T) {
	rt, f := fakeKubeRuntime(t)
	now := "linux/amd64=sha256:new"
	rt.pins = fakePinner{fingerprint: &now}
	clear := func() {
		Freshness.mu.Lock()
		delete(Freshness.m, rt.staleStampKey())
		delete(Freshness.m, rt.staleImageKey())
		Freshness.mu.Unlock()
	}
	clear()
	f.set(stsPathX, 200, `{"metadata":{"name":"af-ws-x","generation":2},"spec":{"replicas":1,"template":{"metadata":{"annotations":{"agent-fleet.io/image-fingerprint":"linux/amd64=sha256:old"}},"spec":{"containers":[]}}},"status":{}}`)
	if !rt.Stale(context.Background()) {
		t.Fatal("not stale after the tag moved")
	}
	clear()
	now = "linux/amd64=sha256:old"
	if rt.Stale(context.Background()) {
		t.Fatal("stale with the same content")
	}
	clear()
	now = ""
	if rt.Stale(context.Background()) {
		t.Fatal("stale with the registry unreadable")
	}
	clear()
	now = "linux/amd64=sha256:new"
	f.set(stsPathX, 200, `{"metadata":{"name":"af-ws-x","generation":2},"spec":{"replicas":1,"template":{"metadata":{},"spec":{"containers":[]}}},"status":{}}`)
	if rt.Stale(context.Background()) {
		t.Fatal("stale without a recorded fingerprint")
	}
	// Start primes the launched side, so a cached older value does not linger.
	rt.primeStale("linux/amd64=sha256:new")
	if rt.Stale(context.Background()) {
		t.Fatal("stale right after a start of the current content")
	}
	clear()
}

func TestCheckStorageClass(t *testing.T) {
	f := &fakeKube{}
	c, _ := startFakeKube(t, f, "t")
	f.set("GET /apis/storage.k8s.io/v1/storageclasses/good", 200,
		`{"metadata":{"name":"good"},"provisioner":"pd.csi.storage.gke.io","reclaimPolicy":"Delete","volumeBindingMode":"WaitForFirstConsumer","allowVolumeExpansion":true}`)
	f.set("GET /apis/storage.k8s.io/v1/storageclasses/bad", 200,
		`{"metadata":{"name":"bad"},"provisioner":"x","reclaimPolicy":"Retain","volumeBindingMode":"Immediate"}`)
	if p := checkStorageClass(context.Background(), c, "good"); len(p) != 0 {
		t.Fatalf("good class: %v", p)
	}
	p := checkStorageClass(context.Background(), c, "bad")
	if len(p) != 3 {
		t.Fatalf("bad class: %v, want three problems", p)
	}
	if p := checkStorageClass(context.Background(), c, "missing"); len(p) != 1 || !strings.Contains(p[0], "cannot read") {
		t.Fatalf("missing class: %v", p)
	}
}

// Two saves racing: this one read 10Gi and wants 20Gi, while another has meanwhile set
// 30Gi. The write is conditional on the 10Gi it read, so the API server refuses it, and
// the re-read sees 30Gi and reports a shrink instead of taking the claim back to 20Gi.
func TestKubeResizeHomeDoesNotUndoAConcurrentResize(t *testing.T) {
	rt, f := fakeKubeRuntime(t)
	rt.homeGiB = 20
	const get = "GET /api/v1/namespaces/ns/persistentvolumeclaims/af-ws-x-home"
	const patch = "PATCH /api/v1/namespaces/ns/persistentvolumeclaims/af-ws-x-home"
	claim := func(req string) string {
		return `{"metadata":{"name":"af-ws-x-home"},"spec":{"resources":{"requests":{"storage":"` + req + `"}}},"status":{"capacity":{"storage":"10Gi"}}}`
	}
	f.set(get, 200, claim("10Gi"))
	f.set(patch, 422, `{"kind":"Status","reason":"Invalid","message":"the server rejected our request due to an error in our request","code":422}`)
	var bodies []string
	rt.c.hc.Transport = roundTripHook{rt.c.hc.Transport, func(r *http.Request) {
		if r.Method == http.MethodPatch {
			b, _ := r.GetBody()
			raw := make([]byte, 4096)
			n, _ := b.Read(raw)
			bodies = append(bodies, string(raw[:n]))
			f.set(get, 200, claim("30Gi")) // the other save landed first
		}
	}}
	hr, err := rt.ResizeHome(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hr.Outcome != HomeResizeShrink || hr.FromGiB != 30 {
		t.Fatalf("ResizeHome = %+v, want shrink from 30 (the concurrent request kept)", hr)
	}
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"op":"test","path":"/spec/resources/requests/storage","value":"10Gi"`) {
		t.Fatalf("patches = %v, want one, conditional on the 10Gi read", bodies)
	}
}

// erasePodJSON is the erase pod as the API server returns it: phase and the erase
// container's state as given.
func erasePodJSON(phase, state string) string {
	return `{"metadata":{"name":"af-ws-x-erase","labels":{"agent-fleet.io/workspace":"af-ws-x","agent-fleet.io/role":"erase"}},` +
		`"spec":{"restartPolicy":"Never","containers":[{"name":"erase","image":"i"}]},` +
		`"status":{"phase":"` + phase + `","containerStatuses":[{"name":"erase","ready":false,"restartCount":0,"state":` + state + `}]}}`
}

const (
	erasePodPath     = "/api/v1/namespaces/ns/pods/af-ws-x-erase"
	eraseRunning     = `{"running":{"startedAt":"2026-10-02T03:00:00Z"}}`
	eraseTerminated  = `{"terminated":{"exitCode":0,"reason":"Completed"}}`
	homeClaimGetPath = "GET /api/v1/namespaces/ns/persistentvolumeclaims/af-ws-x-home"
)

func TestPodFinished(t *testing.T) {
	for _, c := range []struct {
		phase, state string
		want         bool
	}{
		{"Succeeded", eraseTerminated, true},
		{"Failed", `{"terminated":{"exitCode":1}}`, true},
		{"Failed", eraseRunning, false}, // evicted: the status is written before the kill
		{"Failed", `{"waiting":{"reason":"ContainerCreating"}}`, false},
		{"Running", eraseTerminated, false},
	} {
		var p kPod
		if err := json.Unmarshal([]byte(erasePodJSON(c.phase, c.state)), &p); err != nil {
			t.Fatal(err)
		}
		if got := podFinished(&p); got != c.want {
			t.Errorf("podFinished(%s, %s) = %v, want %v", c.phase, c.state, got, c.want)
		}
	}
	if podFinished(&kPod{Status: kPodStatus{Phase: "Failed"}}) {
		t.Error("a Failed pod with no container status counts as finished")
	}
}

// Start does not launch next to an erase pod whose containers may still run, nor next to
// a finished one that has not gone yet: only the pod's absence proves no rm runs on the
// home, which ReadWriteOnce does not exclude on one node.
func TestKubeStartWaitsForTheErasePodToBeGone(t *testing.T) {
	defer func(d time.Duration) { kubeErasePodGoneBudget = d }(kubeErasePodGoneBudget)
	kubeErasePodGoneBudget = 200 * time.Millisecond
	defer func(d time.Duration) { kubeErasePoll = d }(kubeErasePoll)
	kubeErasePoll = 20 * time.Millisecond
	for _, c := range []struct {
		name, phase, state string
		wantErr            string
	}{
		{"evicted, container still running", "Failed", eraseRunning, "still running"},
		{"finished but not gone", "Succeeded", eraseTerminated, "has not gone"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt, f := fakeKubeRuntime(t)
			f.set(stsPathX, 200, stsJSON(0, 6, 6, 0, "r", "2"))
			f.set(podsPathX, 200, podListJSON())
			f.set("GET "+erasePodPath, 200, erasePodJSON(c.phase, c.state))
			f.set("DELETE "+erasePodPath, 200, erasePodJSON(c.phase, c.state)) // accepted; the pod stays
			err := rt.Start(context.Background())
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Start = %v, want %q", err, c.wantErr)
			}
			for _, s := range f.seen {
				if strings.HasPrefix(s, "PATCH /apis") || strings.HasPrefix(s, "POST /apis") {
					t.Fatalf("Start wrote the StatefulSet: %v", f.seen)
				}
			}
			if c.phase == "Failed" && f.saw("DELETE "+erasePodPath) {
				t.Fatal("Start deleted an erase pod that may still be running")
			}
		})
	}
}

// EraseHome treats an evicted erase pod whose container still runs as still running: it
// waits, and does not report a result or delete it.
func TestKubeEraseHomeWaitsForAnEvictedPodToStop(t *testing.T) {
	defer func(d time.Duration) { kubeErasePoll = d }(kubeErasePoll)
	kubeErasePoll = 20 * time.Millisecond
	rt, f := fakeKubeRuntime(t)
	f.set(stsPathX, 200, stsJSON(0, 6, 6, 0, "r", "2"))
	f.set(podsPathX, 200, podListJSON())
	f.set(homeClaimGetPath, 200, `{"metadata":{"name":"af-ws-x-home"},"spec":{"resources":{}}}`)
	f.set("GET "+erasePodPath, 200, erasePodJSON("Failed", eraseRunning))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := rt.EraseHome(ctx)
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("EraseHome = %v, want still running", err)
	}
	if f.saw("DELETE "+erasePodPath) || f.saw("POST /api/v1/namespaces/ns/pods") {
		t.Fatalf("EraseHome deleted or replaced a pod that may still run: %v", f.seen)
	}
}
