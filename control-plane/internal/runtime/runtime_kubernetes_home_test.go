package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
	if len(tmpl.Spec.InitContainers) != 0 {
		t.Fatal("an init container without a mark")
	}
	rt.addHomeWipe(&tmpl, map[string]string{kubeAnnWipePrefix + "clean": "4", kubeAnnWipePrefix + "repos": "5"})
	ic := tmpl.Spec.InitContainers
	if len(ic) != 1 || ic[0].Image != tmpl.Spec.Containers[0].Image || ic[0].SecurityContext != tmpl.Spec.Containers[0].SecurityContext {
		t.Fatalf("init containers = %+v", ic)
	}
	env := map[string]string{}
	for _, e := range ic[0].Env {
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
