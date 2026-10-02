// The home operations and Destroy against the real control plane of
// runtime_kubernetes_envtest_test.go, with the tests playing the node (and, for the
// volumes, the provisioner).
package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func initEnv(p kPod) map[string]string {
	out := map[string]string{}
	for _, c := range p.Spec.InitContainers {
		if c.Name == kubeWipeContainer {
			for _, e := range c.Env {
				out[e.Name] = e.Value
			}
		}
	}
	return out
}

// A member's wipe is a mark on the StatefulSet that the next Start turns into the init
// container (which `restricted` admits, or no pod would appear). A mark made while the
// workspace runs does not roll its pod.
func TestKubernetesEnvWipeHomeReachesTheNextStart(t *testing.T) {
	e, ns := needKubeEnv(t)
	t.Setenv("AF_STOP_GRACE_SEC", "5")
	rt := newKubeTestRuntime(e.factory(t, ns), "af-ws-kim")
	node := kubeNode{t, e, ns}
	ctx := context.Background()
	if got := initEnv(node.run(rt)); len(got) != 0 {
		t.Fatalf("a first start without a mark has a wipe init container: %v", got)
	}
	stopNode := node.serveDeletions(rt.base)
	if err := rt.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	stopNode()
	if err := rt.WipeHome(ctx, HomeWipeClean); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	p := node.waitPod(rt.base)
	if got := initEnv(p); got["AF_WIPE_CLEAN"] != "1" || got["AF_WIPE_REPOS"] != "0" {
		t.Fatalf("init env after Clean home = %v", got)
	}
	node.bind(p.Metadata.Name)
	node.status(p.Metadata.Name, true, true, "")
	eventually(t, 30*time.Second, "running", func() bool { return rt.State(ctx) == "running" })
	if err := rt.WipeHome(ctx, HomeWipeRepos); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if pods := node.pods(rt.base); len(pods) != 1 || pods[0].Metadata.UID != p.Metadata.UID || rt.State(ctx) != "running" {
		t.Fatal("a mark on a running workspace rolled its pod")
	}
	stopNode = node.serveDeletions(rt.base)
	defer stopNode()
	if err := rt.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if got := initEnv(node.waitPod(rt.base)); got["AF_WIPE_CLEAN"] != "1" || got["AF_WIPE_REPOS"] != "2" {
		t.Fatalf("init env after Recreate = %v", got)
	}
}

func (n kubeNode) finishPod(name string, ok bool, message string) {
	n.t.Helper()
	ctx := context.Background()
	var raw map[string]any
	if err := n.e.admin.get(ctx, n.podsPath()+"/"+name, &raw); err != nil {
		n.t.Fatal(err)
	}
	phase, code := "Succeeded", 0
	if !ok {
		phase, code = "Failed", 1
	}
	raw["status"] = map[string]any{
		"phase": phase,
		"containerStatuses": []any{map[string]any{
			"name": kubeRoleErase, "ready": false, "restartCount": 0, "image": "i", "imageID": "",
			"state": map[string]any{"terminated": map[string]any{"exitCode": code, "reason": map[bool]string{true: "Completed", false: "Error"}[ok], "message": message}},
		}},
	}
	if err := n.e.admin.replace(ctx, n.podsPath()+"/"+name+"/status", raw, nil); err != nil {
		n.t.Fatal(err)
	}
}

// evictPod writes what an eviction writes first: phase Failed, with the container still
// running until the kubelet gets to kill it.
func (n kubeNode) evictPod(name string) {
	n.t.Helper()
	ctx := context.Background()
	var raw map[string]any
	if err := n.e.admin.get(ctx, n.podsPath()+"/"+name, &raw); err != nil {
		n.t.Fatal(err)
	}
	raw["status"] = map[string]any{
		"phase": "Failed", "reason": "Evicted",
		"containerStatuses": []any{map[string]any{
			"name": kubeRoleErase, "ready": false, "restartCount": 0, "image": "i", "imageID": "",
			"state": map[string]any{"running": map[string]any{"startedAt": time.Now().UTC().Format(time.RFC3339)}},
		}},
	}
	if err := n.e.admin.replace(ctx, n.podsPath()+"/"+name+"/status", raw, nil); err != nil {
		n.t.Fatal(err)
	}
}

func (n kubeNode) waitErasePod(rt *kubeRuntime) kPod {
	n.t.Helper()
	var p kPod
	eventually(n.t, 30*time.Second, "the erase pod", func() bool {
		return n.e.admin.get(context.Background(), n.podsPath()+"/"+rt.erasePodName(), &p) == nil && p.Metadata.DeletionTimestamp == nil
	})
	return p
}

// An administrator's Clean home: a one-shot pod that `restricted` admits, that is not a
// workspace pod, that Start refuses to run beside, and whose result EraseHome reports. An
// erase that outlives the deadline leaves its pod, and the next EraseHome waits for it.
func TestKubernetesEnvEraseHome(t *testing.T) {
	e, ns := needKubeEnv(t)
	t.Setenv("AF_STOP_GRACE_SEC", "5")
	rt := newKubeTestRuntime(e.factory(t, ns), "af-ws-lee")
	node := kubeNode{t, e, ns}
	ctx := context.Background()
	node.run(rt)
	// Bounded, so that an EraseHome that wrongly went ahead fails here instead of waiting
	// for an erase pod nobody finishes.
	running, cancelRunning := context.WithTimeout(ctx, 5*time.Second)
	err := rt.EraseHome(running)
	cancelRunning()
	if err == nil || !strings.Contains(err.Error(), "has not stopped") {
		t.Fatalf("EraseHome on a running workspace = %v", err)
	}
	stopNode := node.serveDeletions(rt.base)
	defer stopNode()
	if err := rt.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- rt.EraseHome(ctx) }()
	p := node.waitErasePod(rt)
	if p.Metadata.Labels[kubeLabelWorkspace] != rt.base || p.Metadata.Labels[kubeLabelRole] != kubeRoleErase || p.Spec.RestartPolicy != "Never" {
		t.Fatalf("erase pod = %+v", p.Metadata.Labels)
	}
	if got := rt.State(ctx); got != "stopped" {
		t.Fatalf("State with the erase pod running = %q, want stopped", got)
	}
	if err := rt.Start(ctx); err == nil || !strings.Contains(err.Error(), "Clean home is still running") {
		t.Fatalf("Start beside a running erase = %v", err)
	}
	node.finishPod(p.Metadata.Name, true, "")
	if err := <-done; err != nil {
		t.Fatalf("EraseHome: %v", err)
	}
	var gone kPod
	if err := e.admin.get(ctx, node.podsPath()+"/"+rt.erasePodName(), &gone); !isKubeNotFound(err) {
		t.Fatalf("the finished erase pod was left: %v", err)
	}

	// Past the deadline: an error, and the pod keeps running.
	short, cancel := context.WithTimeout(ctx, time.Second)
	err = rt.EraseHome(short)
	cancel()
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("EraseHome past its deadline = %v", err)
	}
	p = node.waitErasePod(rt)
	// The next EraseHome waits for that pod rather than starting another, and reports
	// its failure.
	go func() { done <- rt.EraseHome(ctx) }()
	time.Sleep(300 * time.Millisecond)
	var again kPod
	if err := e.admin.get(ctx, node.podsPath()+"/"+rt.erasePodName(), &again); err != nil || again.Metadata.UID != p.Metadata.UID {
		t.Fatalf("the second EraseHome replaced the running erase pod (%v)", err)
	}
	node.finishPod(p.Metadata.Name, false, "rm: cannot remove 'x': Permission denied")
	if err := <-done; err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("EraseHome with the pod failed = %v", err)
	}

	// A finished erase pod nobody removed (the CP died first) is removed by Start — but
	// not while it is evicted with its container still running: the phase is written
	// before the kubelet kills the container.
	short, cancel = context.WithTimeout(ctx, time.Second)
	_ = rt.EraseHome(short)
	cancel()
	p = node.waitErasePod(rt)
	node.evictPod(p.Metadata.Name)
	if err := rt.Start(ctx); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("Start beside an evicted erase pod still running = %v", err)
	}
	node.finishPod(p.Metadata.Name, true, "")
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start with a finished erase pod left over: %v", err)
	}
	if err := e.admin.get(ctx, node.podsPath()+"/"+rt.erasePodName(), &gone); !isKubeNotFound(err) {
		t.Fatalf("Start left the finished erase pod: %v", err)
	}
}

// createBoundVolume makes a volume pre-bound to the claim, as a provisioner would, with
// the provisioner's deletion-protection finalizer and Retain, so it outlives the claim
// until the test removes it.
func createBoundVolume(t *testing.T, e *kubeTestEnv, ns, name, claim, size string) {
	t.Helper()
	err := e.admin.create(context.Background(), "/api/v1/persistentvolumes", map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolume",
		"metadata": map[string]any{"name": name, "finalizers": []string{"external-provisioner.volume.kubernetes.io/finalizer"}},
		"spec": map[string]any{
			"capacity":                      map[string]string{"storage": size},
			"accessModes":                   []string{"ReadWriteOnce"},
			"persistentVolumeReclaimPolicy": "Retain",
			"storageClassName":              "",
			"claimRef":                      map[string]any{"namespace": ns, "name": claim},
			"csi":                           map[string]any{"driver": "fake.csi.example", "volumeHandle": name},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

// removeVolume is the disk going away: the provisioner drops its finalizer and the
// object is deleted.
func removeVolume(t *testing.T, e *kubeTestEnv, name string) {
	t.Helper()
	ctx := context.Background()
	path := "/api/v1/persistentvolumes/" + name
	if err := e.admin.jsonPatch(ctx, path, []kubePatchOp{{Op: "remove", Path: "/metadata/finalizers/0"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.admin.delete(ctx, path); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "volume "+name+" gone", func() bool {
		var o json.RawMessage
		return isKubeNotFound(e.admin.get(ctx, path, &o))
	})
}

// Destroy with real bound volumes: it records the inventory, deletes the claims, and
// keeps the StatefulSet while a volume is still there; a re-run, with the claims long
// gone, finds the volumes through the inventory and finishes once they are gone.
func TestKubernetesEnvDestroyWithBoundVolumes(t *testing.T) {
	e, ns := needKubeEnv(t)
	t.Setenv("AF_STOP_GRACE_SEC", "5")
	shortDestroyBudget(t)
	kubeDestroyClaimBudget = 3 * time.Second
	rt := newKubeTestRuntime(e.factory(t, ns), "af-ws-max")
	node := kubeNode{t, e, ns}
	ctx := context.Background()
	homePV, statePV := ns+"-pv-home", ns+"-pv-state"
	createBoundVolume(t, e, ns, homePV, rt.homeClaim(), "10Gi")
	createBoundVolume(t, e, ns, statePV, rt.stateClaim(), "1Gi")
	node.run(rt)
	for _, c := range []string{rt.homeClaim(), rt.stateClaim()} {
		eventually(t, 30*time.Second, c+" bound", func() bool {
			var pvc kPVC
			return e.admin.get(ctx, "/api/v1/namespaces/"+ns+"/persistentvolumeclaims/"+c, &pvc) == nil && pvc.Spec.VolumeName != ""
		})
	}
	stopNode := node.serveDeletions(rt.base)
	defer stopNode()
	want := []string{"pv:" + homePV, "pv:" + statePV, "statefulset:" + ns + "/" + rt.base}
	res, err := rt.Destroy(ctx)
	if err != nil || strings.Join(res, ",") != strings.Join(want, ",") {
		t.Fatalf("Destroy with the disks still there = %v, %v; want %v", res, err, want)
	}
	var s kStatefulSet
	if err := e.admin.get(ctx, rt.stsPath(), &s); err != nil {
		t.Fatalf("the StatefulSet was deleted: %v", err)
	}
	if inv := s.Metadata.Annotations[kubeAnnInventory]; !strings.Contains(inv, homePV) || !strings.Contains(inv, statePV) {
		t.Fatalf("inventory = %s", inv)
	}
	for _, c := range []string{rt.homeClaim(), rt.stateClaim()} {
		var o json.RawMessage
		if !isKubeNotFound(e.admin.get(ctx, "/api/v1/namespaces/"+ns+"/persistentvolumeclaims/"+c, &o)) {
			t.Fatalf("claim %s was not deleted", c)
		}
	}
	removeVolume(t, e, statePV)
	res, _ = rt.Destroy(ctx)
	if want := []string{"pv:" + homePV, "statefulset:" + ns + "/" + rt.base}; strings.Join(res, ",") != strings.Join(want, ",") {
		t.Fatalf("re-run = %v, want %v", res, want)
	}
	removeVolume(t, e, homePV)
	res, err = rt.Destroy(ctx)
	if err != nil || len(res) != 0 {
		t.Fatalf("last run = %v, %v", res, err)
	}
	var o json.RawMessage
	if err := e.admin.get(ctx, rt.stsPath(), &o); !isKubeNotFound(err) {
		t.Fatalf("the StatefulSet is still there: %v", err)
	}
}
