// The kubernetes adapter against a real cluster: the live harness of ADR 0106 decision 10.
// The envtest suite (runtime_kubernetes_envtest_test.go) has no kubelet, scheduler or volume
// provisioner and plays the node itself; this one has all of them, so it proves what a
// fake cannot: real pods, real disks, the workspace image's own entrypoint and the timing
// of a real controller.
//
// Opt-in, because it creates billable disks and needs a cluster:
//
//	AF_K8S_LIVE=1 go test -run 'TestKubernetesLive' -v -timeout 50m ./internal/runtime/
//
// with the environment of deploy/kubernetes/README.md, "The live harness". Two identities
// are involved, as in the ecs-ec2 harness (useCPTaskRole): the product talks to the API
// server with a token of the CP's own service account (AF_K8S_LIVE_TOKEN_FILE), so a
// permission the CP's Role lacks fails the run; the harness's own eyes, its cleanup and
// the actions that stand for someone else (an unplanned drain, an operator) use kubectl
// with the ambient KUBECONFIG, an identity allowed more than the CP.
//
// Every workspace the harness creates is named live-<scenario>-<random> and removed when
// its test ends, by Destroy and then, for whatever Destroy left, by kubectl.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// kubeLiveHold is the finalizer the harness puts on a volume to keep it from going, for
// the "volume left behind" case of Destroy. Cleanup removes it from every volume of the
// workspace whatever happened.
const kubeLiveHold = "agent-fleet.io/live-harness-hold"

// kubeLiveRunning bounds a start on a real cluster: two disks provisioned and attached,
// the image pulled, the entrypoint run, and possibly a node added by the autoscaler.
const kubeLiveRunning = 8 * time.Minute

type kubeLive struct {
	t       *testing.T
	ns      string
	kubectl string
	tokens  map[string]string // workspace name -> AGENT_TOKEN, stable across adapter values
}

var kubeLiveIdentity struct {
	once sync.Once
	err  error
	user string
}

// needKubeLive skips the test, visibly, unless AF_K8S_LIVE=1. With the gate set, a missing
// variable is a failure: a harness that was asked to run must not pass by doing nothing.
func needKubeLive(t *testing.T) *kubeLive {
	t.Helper()
	if os.Getenv("AF_K8S_LIVE") != "1" {
		t.Skip("SKIPPED: live kubernetes harness — set AF_K8S_LIVE=1 with the environment of deploy/kubernetes/README.md, \"The live harness\"")
	}
	for _, k := range []string{"AF_K8S_LIVE_SERVER", "AF_K8S_LIVE_CA_FILE", "AF_K8S_LIVE_TOKEN_FILE",
		"AF_K8S_NAMESPACE", "AF_K8S_WORKSPACE_IMAGE", "AF_K8S_STORAGE_CLASS"} {
		if os.Getenv(k) == "" {
			t.Fatalf("AF_K8S_LIVE=1 but %s is unset (deploy/kubernetes/README.md, \"The live harness\")", k)
		}
	}
	kc := os.Getenv("AF_K8S_LIVE_KUBECTL")
	if kc == "" {
		kc = "kubectl"
	}
	if _, err := exec.LookPath(kc); err != nil {
		t.Fatalf("the harness needs kubectl for its own checks and cleanup: %v", err)
	}
	// Stop's budget is the grace plus a margin; keep it short so a scenario that waits for
	// several stops stays inside the token's hour.
	t.Setenv("AF_STOP_GRACE_SEC", "20")
	l := &kubeLive{t: t, ns: os.Getenv("AF_K8S_NAMESPACE"), kubectl: kc, tokens: map[string]string{}}
	l.checkIdentity()
	return l
}

// checkIdentity proves, once per run, that the product's token is the CP's service account
// and not something stronger: it names a service account called af-cp, it can read the
// configured StorageClass (the CP's ClusterRole), and it cannot list nodes, which the CP's
// role does not grant and any administrator could.
func (l *kubeLive) checkIdentity() {
	l.t.Helper()
	kubeLiveIdentity.once.Do(func() {
		c := l.factory(0).c
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var rev struct {
			Status struct {
				UserInfo struct {
					Username string `json:"username"`
				} `json:"userInfo"`
			} `json:"status"`
		}
		if err := c.create(ctx, "/apis/authentication.k8s.io/v1/selfsubjectreviews",
			map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "SelfSubjectReview"}, &rev); err != nil {
			kubeLiveIdentity.err = fmt.Errorf("self subject review: %w", err)
			return
		}
		user := rev.Status.UserInfo.Username
		kubeLiveIdentity.user = user
		want := os.Getenv("AF_K8S_LIVE_CP_USER")
		switch {
		case want != "" && user != want:
			kubeLiveIdentity.err = fmt.Errorf("the product's token is %q, want %q", user, want)
			return
		case want == "" && (!strings.HasPrefix(user, "system:serviceaccount:") || !strings.HasSuffix(user, ":af-cp")):
			kubeLiveIdentity.err = fmt.Errorf("the product's token is %q, not the CP's service account (mint one with kubectl create token af-cp)", user)
			return
		}
		var sc json.RawMessage
		if err := c.get(ctx, "/apis/storage.k8s.io/v1/storageclasses/"+os.Getenv("AF_K8S_STORAGE_CLASS"), &sc); err != nil {
			kubeLiveIdentity.err = fmt.Errorf("the CP's token cannot read its StorageClass: %w", err)
			return
		}
		var nodes json.RawMessage
		if err := c.get(ctx, "/api/v1/nodes", &nodes); kubeErrCode(err) != 403 {
			kubeLiveIdentity.err = fmt.Errorf("listing nodes with the product's token answered %v, want 403: the token is stronger than the CP's role", err)
		}
	})
	if kubeLiveIdentity.err != nil {
		l.t.Fatal(kubeLiveIdentity.err)
	}
	l.t.Logf("the product runs as %s — the CP's own RBAC, not the harness's", kubeLiveIdentity.user)
}

// factory builds the adapter's factory the way the CP's boot does, from the same AF_K8S_*
// variables, against the API server named in the environment. Each call is a fresh
// client: a second call is a restarted CP. homeGiB > 0 overrides the home size.
func (l *kubeLive) factory(homeGiB int) *kubeFactory {
	l.t.Helper()
	cfg, err := kubeConfigFromEnv(envOr, Config{})
	if err != nil {
		l.t.Fatal(err)
	}
	if homeGiB > 0 {
		cfg.homeGiB = homeGiB
	}
	ca, err := os.ReadFile(os.Getenv("AF_K8S_LIVE_CA_FILE"))
	if err != nil {
		l.t.Fatal(err)
	}
	c, err := newKubeClient(os.Getenv("AF_K8S_LIVE_SERVER"), ca, os.Getenv("AF_K8S_LIVE_TOKEN_FILE"))
	if err != nil {
		l.t.Fatal(err)
	}
	return &kubeFactory{
		cfg:        cfg,
		c:          c,
		pins:       newRegistryClient(pullSecretCreds(c, cfg.namespace, cfg.pullSecret)),
		poll:       time.Second,
		stopMargin: kubeStopMargin,
	}
}

// workspace names a new workspace for scenario and registers its cleanup.
func (l *kubeLive) workspace(scenario string) string {
	l.t.Helper()
	name := "live-" + scenario + "-" + randHexT(2)
	l.tokens[name] = randHexT(16)
	l.t.Cleanup(func() { l.purge(name) })
	return name
}

func (l *kubeLive) runtime(f *kubeFactory, name string) *kubeRuntime {
	return f.New(Workspace{ContainerName: name, AgentToken: l.tokens[name], MemBytes: 2 * gib, CPUUnits: 512},
		randHexT(16), nil).(*kubeRuntime)
}

// --- the harness's own eyes: kubectl as the ambient identity ---

func (l *kubeLive) run(args ...string) (string, error) {
	cmd := exec.Command(l.kubectl, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("kubectl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func (l *kubeLive) must(args ...string) string {
	l.t.Helper()
	out, err := l.run(args...)
	if err != nil {
		l.t.Fatal(err)
	}
	return out
}

func (l *kubeLive) getJSON(out any, args ...string) {
	l.t.Helper()
	raw := l.must(append(args, "-o", "json")...)
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		l.t.Fatalf("decode kubectl %v: %v", args, err)
	}
}

func (l *kubeLive) pods(base string) []kPod {
	l.t.Helper()
	var list kPodList
	l.getJSON(&list, "-n", l.ns, "get", "pods", "-l", kubeLabelWorkspace+"="+base+","+kubeLabelRole+"="+kubeRoleWorkspace)
	return list.Items
}

func (l *kubeLive) sts(base string) kStatefulSet {
	l.t.Helper()
	var s kStatefulSet
	l.getJSON(&s, "-n", l.ns, "get", "statefulset", base)
	return s
}

// sh runs script in the workspace container as dev and returns its output.
func (l *kubeLive) sh(rt *kubeRuntime, script string) string {
	l.t.Helper()
	return strings.TrimSpace(l.must("-n", l.ns, "exec", rt.base+"-0", "-c", kubeAgentName, "--", "sh", "-c", script))
}

// volumesOf names the volumes bound to the workspace's two claims, read from the claims
// (when they still exist) and from the volumes' claimRef (when they do not).
func (l *kubeLive) volumesOf(rt *kubeRuntime) []string {
	l.t.Helper()
	var list struct {
		Items []struct {
			Metadata kObjectMeta `json:"metadata"`
			Spec     struct {
				ClaimRef *struct {
					Namespace string `json:"namespace"`
					Name      string `json:"name"`
				} `json:"claimRef"`
			} `json:"spec"`
		} `json:"items"`
	}
	l.getJSON(&list, "get", "pv")
	var out []string
	for _, pv := range list.Items {
		if r := pv.Spec.ClaimRef; r != nil && r.Namespace == l.ns && (r.Name == rt.homeClaim() || r.Name == rt.stateClaim()) {
			out = append(out, pv.Metadata.Name)
		}
	}
	slices.Sort(out)
	return out
}

// requireDisksGone checks the provider's side, when AF_K8S_LIVE_GCE_PROJECT names a
// Google Cloud project: the PD CSI driver names each disk after its volume.
func (l *kubeLive) requireDisksGone(volumes []string) {
	l.t.Helper()
	project := os.Getenv("AF_K8S_LIVE_GCE_PROJECT")
	if project == "" || len(volumes) == 0 {
		l.t.Logf("disks behind %v: NOT CHECKED on the provider (set AF_K8S_LIVE_GCE_PROJECT)", volumes)
		return
	}
	filter := "name=(" + strings.Join(volumes, ",") + ")"
	deadline := time.Now().Add(3 * time.Minute)
	for {
		out, err := exec.Command("gcloud", "compute", "disks", "list", "--project", project,
			"--filter", filter, "--format", "value(name)").Output()
		if err != nil {
			l.t.Fatalf("gcloud compute disks list: %v", err)
		}
		left := strings.TrimSpace(string(out))
		if left == "" {
			l.t.Logf("disks behind %v: gone on the provider", volumes)
			return
		}
		if time.Now().After(deadline) {
			l.t.Fatalf("disks still on the provider after Destroy: %s", left)
		}
		time.Sleep(5 * time.Second)
	}
}

// purge removes everything of the workspace: Destroy from a fresh adapter first, as the CP
// would, then kubectl for whatever is left, the harness's hold finalizer included. It
// reports what it had to remove by hand, and fails the test if anything stays.
func (l *kubeLive) purge(name string) {
	t := l.t
	f := l.factory(0)
	rt := l.runtime(f, name)
	var vols []string
	if out, err := l.run("get", "pv", "-o", "json"); err == nil {
		var list struct {
			Items []struct {
				Metadata kObjectMeta `json:"metadata"`
				Spec     struct {
					ClaimRef *struct{ Namespace, Name string } `json:"claimRef"`
				} `json:"spec"`
			} `json:"items"`
		}
		_ = json.Unmarshal([]byte(out), &list)
		for _, pv := range list.Items {
			if r := pv.Spec.ClaimRef; r != nil && r.Namespace == l.ns && (r.Name == rt.homeClaim() || r.Name == rt.stateClaim()) {
				vols = append(vols, pv.Metadata.Name)
				if slices.Contains(pv.Metadata.Finalizers, kubeLiveHold) {
					l.unhold(pv.Metadata.Name)
				}
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := rt.Destroy(ctx)
	if err != nil || len(res) > 0 {
		t.Logf("cleanup of %s: Destroy = %v, %v; removing the rest with kubectl", name, res, err)
	}
	sel := kubeLabelWorkspace + "=" + rt.base
	for _, kind := range []string{"statefulset", "pod", "service", "secret", "pvc"} {
		if _, err := l.run("-n", l.ns, "delete", kind, "-l", sel, "--ignore-not-found", "--wait=false"); err != nil {
			t.Errorf("cleanup of %s: %v", name, err)
		}
	}
	deadline := time.Now().Add(4 * time.Minute)
	for {
		var left []string
		for _, kind := range []string{"statefulset", "pod", "pvc"} {
			if out, err := l.run("-n", l.ns, "get", kind, "-l", sel, "-o", "name"); err == nil && strings.TrimSpace(out) != "" {
				left = append(left, strings.Fields(out)...)
			}
		}
		for _, v := range vols {
			if out, _ := l.run("get", "pv", v, "--ignore-not-found", "-o", "name"); strings.TrimSpace(out) != "" {
				left = append(left, "pv/"+v)
			}
		}
		if len(left) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("cleanup of %s: still present after 4m: %v", name, left)
			return
		}
		time.Sleep(3 * time.Second)
	}
	if len(vols) > 0 && os.Getenv("AF_K8S_LIVE_GCE_PROJECT") != "" {
		l.requireDisksGone(vols)
	}
}

func (l *kubeLive) hold(pv string) {
	l.t.Helper()
	l.must("patch", "pv", pv, "--type=json", "-p",
		`[{"op":"add","path":"/metadata/finalizers/-","value":"`+kubeLiveHold+`"}]`)
}

func (l *kubeLive) unhold(pv string) {
	var obj kPV
	out, err := l.run("get", "pv", pv, "-o", "json")
	if err != nil || json.Unmarshal([]byte(out), &obj) != nil {
		return
	}
	i := slices.Index(obj.Metadata.Finalizers, kubeLiveHold)
	if i < 0 {
		return
	}
	if _, err := l.run("patch", "pv", pv, "--type=json", "-p",
		`[{"op":"test","path":"/metadata/finalizers/`+strconv.Itoa(i)+`","value":"`+kubeLiveHold+`"},`+
			`{"op":"remove","path":"/metadata/finalizers/`+strconv.Itoa(i)+`"}]`); err != nil {
		l.t.Errorf("remove the harness's finalizer from %s: %v", pv, err)
	}
}

// --- waiting ---

// waitState polls State until it reads want, logging every change with the time since
// the wait began, and returns the states seen in order.
func (l *kubeLive) waitState(rt *kubeRuntime, want string, budget time.Duration) []string {
	l.t.Helper()
	ctx := context.Background()
	start := time.Now()
	var seen []string
	for {
		got := rt.State(ctx)
		if len(seen) == 0 || seen[len(seen)-1] != got {
			seen = append(seen, got)
			l.t.Logf("  %6.1fs %s: %s %s", time.Since(start).Seconds(), rt.base, got, rt.BootPhase())
		}
		if got == want {
			return seen
		}
		if time.Since(start) > budget {
			l.t.Fatalf("%s: not %s after %s (states seen %v, boot phase %q)", rt.base, want, budget, seen, rt.BootPhase())
		}
		time.Sleep(time.Second)
	}
}

func (l *kubeLive) eventually(budget time.Duration, what string, ok func() bool) {
	l.t.Helper()
	deadline := time.Now().Add(budget)
	for !ok() {
		if time.Now().After(deadline) {
			l.t.Fatalf("timed out after %s waiting for %s", budget, what)
		}
		time.Sleep(time.Second)
	}
}

// startRunning starts the workspace and waits until it runs.
func (l *kubeLive) startRunning(rt *kubeRuntime) {
	l.t.Helper()
	t0 := time.Now()
	if err := rt.Start(context.Background()); err != nil {
		l.t.Fatalf("Start: %v", err)
	}
	l.waitState(rt, "running", kubeLiveRunning)
	l.t.Logf("%s: Start to running in %s", rt.base, time.Since(t0).Round(time.Second))
}

func (l *kubeLive) stop(rt *kubeRuntime) {
	l.t.Helper()
	t0 := time.Now()
	if err := rt.Stop(context.Background()); err != nil {
		l.t.Fatalf("Stop: %v", err)
	}
	if pods := l.pods(rt.base); len(pods) != 0 {
		l.t.Fatalf("Stop returned with %d pod(s) left", len(pods))
	}
	l.t.Logf("%s: Stop settled in %s", rt.base, time.Since(t0).Round(time.Second))
}

func (l *kubeLive) podGen(rt *kubeRuntime) string {
	l.t.Helper()
	for _, p := range l.pods(rt.base) {
		if p.Metadata.DeletionTimestamp == nil {
			return p.Metadata.Annotations[kubeAnnStartGen]
		}
	}
	return ""
}

// requireDestroyClean destroys the workspace and requires that nothing is left: no
// residue, no object of the workspace, its volumes gone, and their disks too when the
// provider can be asked.
func (l *kubeLive) requireDestroyClean(rt *kubeRuntime) {
	l.t.Helper()
	vols := l.volumesOf(rt)
	if len(vols) != 2 {
		l.t.Fatalf("the workspace has volumes %v, want two (home and state)", vols)
	}
	t0 := time.Now()
	res, err := rt.Destroy(context.Background())
	if err != nil || len(res) != 0 {
		l.t.Fatalf("Destroy = %v, %v; want no residue", res, err)
	}
	l.t.Logf("%s: Destroy returned clean in %s", rt.base, time.Since(t0).Round(time.Second))
	sel := kubeLabelWorkspace + "=" + rt.base
	for _, kind := range []string{"statefulset", "service", "secret", "pvc", "pod"} {
		if out := strings.TrimSpace(l.must("-n", l.ns, "get", kind, "-l", sel, "-o", "name")); out != "" {
			l.t.Errorf("after Destroy: %s", out)
		}
	}
	for _, v := range vols {
		if out := strings.TrimSpace(l.must("get", "pv", v, "--ignore-not-found", "-o", "name")); out != "" {
			l.t.Errorf("after Destroy the volume %s still exists", v)
		}
	}
	l.requireDisksGone(vols)
}

// --- the scenarios ---

// TestKubernetesLiveLifecycle: a new workspace started by one CP and carried through by a
// restarted one; every mount of it writable by dev, the keep links in place and the home
// private (ADR 0106 decision 4, #1543); its pod deleted behind the CP's back (an
// unplanned drain); a resize while stopped; and a clean Destroy.
func TestKubernetesLiveLifecycle(t *testing.T) {
	l := needKubeLive(t)
	ctx := context.Background()
	name := l.workspace("life")
	first := l.runtime(l.factory(0), name)
	var rt *kubeRuntime

	ok := t.Run("CPRestartMidStart", func(t *testing.T) {
		l := *l
		l.t = t
		if got := first.State(ctx); got != "none" {
			t.Fatalf("State before any start = %q, want none", got)
		}
		t0 := time.Now()
		if err := first.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		// The CP restarts: a new adapter value on a new client finds the start under way.
		rt = l.runtime(l.factory(0), name)
		if got := rt.State(ctx); got != "starting" {
			t.Fatalf("the restarted CP reads %q, want starting", got)
		}
		l.eventually(2*time.Minute, "the first pod", func() bool { return len(l.pods(rt.base)) > 0 })
		uid := l.pods(rt.base)[0].Metadata.UID
		// Its handler would not Start again, but if it did, nothing moves.
		if err := rt.Start(ctx); err != nil {
			t.Fatalf("Start on a launch under way: %v", err)
		}
		l.waitState(rt, "running", kubeLiveRunning)
		t.Logf("first start: Start to running in %s", time.Since(t0).Round(time.Second))
		pods := l.pods(rt.base)
		if len(pods) != 1 || pods[0].Metadata.UID != uid {
			t.Fatalf("the restarted CP's Start replaced the pod of the launch under way")
		}
		if g := l.podGen(rt); g != "1" {
			t.Fatalf("start generation %q, want 1", g)
		}
		if n, err := rt.RunningTasks(ctx); err != nil || n != 1 {
			t.Fatalf("RunningTasks = %d, %v; want 1", n, err)
		}
	})
	if !ok {
		return
	}

	t.Run("NewWorkspaceMountsAndHome", func(t *testing.T) {
		l := *l
		l.t = t
		// Every mount dev writes to, and every keep entry through its link: the keep files
		// are dangling links on a new home, and a write through one must land on the
		// state volume.
		out := l.sh(rt, `set -u
id -un; id -gn
for d in /home/dev /var/lib/af/claude /var/lib/af/keep /tmp; do
  f="$d/.live-write-$$"; if echo x > "$f" && rm -f "$f"; then echo "write ok $d"; else echo "write FAILED $d"; fi
done
for e in .config .ssh .claude .codex .git-credentials .gitconfig .claude.json; do
  l=$(readlink "$HOME/$e" || echo NOT-A-LINK); echo "link $e -> $l"
done
for e in .config .ssh .claude .codex; do
  if echo x > "$HOME/$e/.live-write" && [ -f "/var/lib/af/keep/$e/.live-write" ]; then echo "keep-write ok $e"; else echo "keep-write FAILED $e"; fi
  rm -f "$HOME/$e/.live-write"
done
stat -c 'home %U:%G %A' /home/dev`)
		t.Logf("inside the new workspace:\n%s", out)
		lines := strings.Split(out, "\n")
		if len(lines) < 2 || lines[0] != "dev" || lines[1] != "dev" {
			t.Errorf("the agent runs as %v, want dev:dev", lines[:min(2, len(lines))])
		}
		if strings.Contains(out, "FAILED") {
			t.Errorf("dev cannot write to every mount (decision 4)")
		}
		for _, e := range []string{".config", ".ssh", ".claude", ".codex", ".git-credentials", ".gitconfig", ".claude.json"} {
			if !strings.Contains(out, "link "+e+" -> "+kubeKeepPath+"/"+e+"\n") {
				t.Errorf("~/%s is not a link into %s", e, kubeKeepPath)
			}
		}
		var mode string
		for _, ln := range lines {
			if strings.HasPrefix(ln, "home ") {
				mode = ln
			}
		}
		f := strings.Fields(mode)
		if len(f) != 3 || f[1] != "dev:dev" || len(f[2]) != 10 || f[2][0] != 'd' || f[2][5] == 'w' || f[2][8] == 'w' {
			t.Errorf("/home/dev is %q, want dev:dev with no group or other write (#1543)", mode)
		}
	})

	t.Run("PodDeletedBehindTheCP", func(t *testing.T) {
		l := *l
		l.t = t
		before := l.sts(rt.base)
		old := l.pods(rt.base)[0]
		// An unplanned drain: someone else deletes the pod; the CP does nothing.
		l.must("-n", l.ns, "delete", "pod", old.Metadata.Name, "--wait=false")
		t0 := time.Now()
		seen := l.waitState(rt, "running", kubeLiveRunning)
		if !slices.Contains(seen, "starting") {
			t.Errorf("states seen %v: the workspace did not pass through starting", seen)
		}
		var cur kPod
		for _, p := range l.pods(rt.base) {
			if p.Metadata.DeletionTimestamp == nil {
				cur = p
			}
		}
		if cur.Metadata.UID == old.Metadata.UID {
			t.Fatal("running again on the deleted pod")
		}
		if g, w := cur.Metadata.Annotations[kubeAnnStartGen], old.Metadata.Annotations[kubeAnnStartGen]; g != w {
			t.Errorf("the replacement carries start generation %q, want the same start's %q", g, w)
		}
		after := l.sts(rt.base)
		if after.Metadata.Generation != before.Metadata.Generation {
			t.Errorf("the StatefulSet's spec changed (generation %d -> %d) though the CP did nothing",
				before.Metadata.Generation, after.Metadata.Generation)
		}
		t.Logf("pod deleted behind the CP: running again in %s on node %s (was %s)",
			time.Since(t0).Round(time.Second), cur.Spec.NodeName, old.Spec.NodeName)
	})

	t.Run("ResizeWhileStopped", func(t *testing.T) {
		l := *l
		l.t = t
		l.stop(rt)
		var claim kPVC
		l.getJSON(&claim, "-n", l.ns, "get", "pvc", rt.homeClaim())
		have, _ := quantityBytes(claim.Spec.Resources.Requests["storage"])
		want := int(have/gib) + 5
		big := l.runtime(l.factory(want), name)
		r, err := big.ResizeHome(ctx)
		if err != nil || r.Outcome != HomeResizeGrowing || int(r.ToGiB) != want {
			t.Fatalf("ResizeHome while stopped = %+v, %v; want growing to %d", r, err, want)
		}
		// Again before the next mount: the request is raised, the file system is not.
		r, err = big.ResizeHome(ctx)
		t.Logf("ResizeHome again while stopped = %+v, %v", r, err)
		if err != nil || r.Outcome == HomeResizeFailed || r.Outcome == HomeResizeShrink {
			t.Fatalf("ResizeHome again while stopped = %+v, %v", r, err)
		}
		l.startRunning(big)
		l.eventually(5*time.Minute, "the home claim's capacity to reach the request", func() bool {
			r, err = big.ResizeHome(ctx)
			return err == nil && r.Outcome == HomeResizeSame
		})
		df := l.sh(big, `df -k /home/dev | awk 'NR==2{print $2}'`)
		kb, _ := strconv.ParseInt(df, 10, 64)
		if kb*1024 < int64(want-1)*gib {
			t.Errorf("the home's file system is %d KiB after growing to %d GiB", kb, want)
		}
		t.Logf("resize while stopped: %d -> %d GiB, file system %d KiB", have/gib, want, kb)
		rt = big
	})

	t.Run("DestroyClean", func(t *testing.T) {
		l := *l
		l.t = t
		l.requireDestroyClean(rt)
	})
}

// TestKubernetesLiveStartStopRaces: a Stop issued while the controller is creating the
// pod, a Stop while starting, State read between Start's write and the controller's next
// status update, and a Stop then a Start at once.
func TestKubernetesLiveStartStopRaces(t *testing.T) {
	l := needKubeLive(t)
	ctx := context.Background()
	name := l.workspace("race")
	rt := l.runtime(l.factory(0), name)

	t.Run("StopWhileControllerCreatesPod", func(t *testing.T) {
		l := *l
		l.t = t
		// The controller's create cannot be held back on a real cluster without changing
		// its admission, so the window is aimed at instead: Stop follows Start with no
		// delay, and the controller's events show whether it had created a pod by then.
		// Whatever it had done, Stop must return only once no pod exists, and no pod may
		// appear after it returned.
		const rounds = 5
		created := 0
		for i := 0; i < rounds; i++ {
			before := l.countEvents(rt.base, "SuccessfulCreate")
			if err := rt.Start(ctx); err != nil {
				t.Fatalf("round %d: Start: %v", i, err)
			}
			t0 := time.Now()
			if err := rt.Stop(ctx); err != nil {
				t.Fatalf("round %d: Stop right after Start: %v", i, err)
			}
			d := time.Since(t0)
			for j := 0; j < 10; j++ {
				if pods := l.pods(rt.base); len(pods) != 0 {
					t.Fatalf("round %d: a pod (%s) exists %s after Stop returned", i, pods[0].Metadata.Name, time.Duration(j)*time.Second)
				}
				time.Sleep(time.Second)
			}
			if got := rt.State(ctx); got != "stopped" {
				t.Fatalf("round %d: State = %q, want stopped", i, got)
			}
			n := l.countEvents(rt.base, "SuccessfulCreate") - before
			if n > 0 {
				created++
			}
			t.Logf("round %d: Stop settled in %s; the controller created %d pod(s) in the window", i, d.Round(time.Millisecond), n)
		}
		t.Logf("Stop right after Start: the controller had created a pod in %d of %d rounds", created, rounds)
	})

	t.Run("StopWhileStarting", func(t *testing.T) {
		l := *l
		l.t = t
		if err := rt.Start(ctx); err != nil {
			t.Fatal(err)
		}
		var p kPod
		l.eventually(3*time.Minute, "a pod placed on a node", func() bool {
			for _, q := range l.pods(rt.base) {
				if q.Spec.NodeName != "" {
					p = q
					return true
				}
			}
			return false
		})
		if got := rt.State(ctx); got != "starting" {
			t.Fatalf("State with the pod %s on %s = %q, want starting", p.Metadata.Name, p.Spec.NodeName, got)
		}
		t.Logf("stopping while starting: boot phase %q, pod phase %s", rt.BootPhase(), p.Status.Phase)
		l.stop(rt)
	})

	t.Run("StateBeforeControllerObserves", func(t *testing.T) {
		l := *l
		l.t = t
		if err := rt.Start(ctx); err != nil {
			t.Fatal(err)
		}
		// Read as fast as the API server answers: the first reads land before the
		// controller has observed the new template. `running` may be reported only with a
		// Ready pod of this start, checked right after.
		lagging, reads := 0, 0
		start := time.Now()
		for {
			s, err := rt.getStatefulSet(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if s.Status.ObservedGeneration < s.Metadata.Generation {
				lagging++
			}
			got := rt.State(ctx)
			reads++
			if reads == 1 && got != "starting" {
				t.Fatalf("the first State after Start = %q, want starting", got)
			}
			if got == "running" {
				want := l.sts(rt.base).Spec.Template.Metadata.Annotations[kubeAnnStartGen]
				fine := false
				for _, p := range l.pods(rt.base) {
					if p.Metadata.DeletionTimestamp == nil && podReady(&p) && p.Metadata.Annotations[kubeAnnStartGen] == want {
						fine = true
					}
				}
				if !fine {
					t.Fatalf("State said running with no Ready pod of start generation %s", want)
				}
				break
			}
			if time.Since(start) > kubeLiveRunning {
				t.Fatalf("not running after %s", kubeLiveRunning)
			}
			if lagging == 0 || time.Since(start) > 5*time.Second {
				time.Sleep(500 * time.Millisecond)
			}
		}
		t.Logf("State read %d times before running (%s); %d read(s) saw the controller behind the template", reads, time.Since(start).Round(time.Second), lagging)
	})

	t.Run("StopThenStartAtOnce", func(t *testing.T) {
		l := *l
		l.t = t
		gen := l.podGen(rt)
		l.stop(rt)
		if err := rt.Start(ctx); err != nil {
			t.Fatalf("Start right after Stop: %v", err)
		}
		l.waitState(rt, "running", kubeLiveRunning)
		g, _ := strconv.Atoi(gen)
		if got := l.podGen(rt); got != strconv.Itoa(g+1) {
			t.Fatalf("start generation %q after Stop+Start, want %d", got, g+1)
		}
	})
}

func (l *kubeLive) countEvents(base, reason string) int {
	l.t.Helper()
	var list struct {
		Items []struct {
			Count  int32 `json:"count"`
			Series *struct {
				Count int32 `json:"count"`
			} `json:"series"`
		} `json:"items"`
	}
	l.getJSON(&list, "-n", l.ns, "get", "events", "--field-selector",
		"involvedObject.kind=StatefulSet,involvedObject.name="+base+",reason="+reason)
	n := 0
	for _, e := range list.Items {
		c := max(int(e.Count), 1)
		if e.Series != nil {
			c = max(c, int(e.Series.Count))
		}
		n += c
	}
	return n
}

// TestKubernetesLiveHomeWipes: a member's Recreate and Clean home, each with a keep file
// that a tool replaced by a plain file since the last boot. The plain file must survive
// both and come back as a link holding its content; Recreate removes ~/repos only, Clean
// home everything but the keep names; a pod restart repeats neither.
func TestKubernetesLiveHomeWipes(t *testing.T) {
	l := needKubeLive(t)
	ctx := context.Background()
	rt := l.runtime(l.factory(0), l.workspace("wipe"))
	l.startRunning(rt)
	l.sh(rt, `set -e
mkdir -p ~/repos/a ~/other; echo r > ~/repos/a/f; echo o > ~/other/f; echo s > ~/.ssh/live-marker
rm -f ~/.gitconfig; printf '[user]\n\tname = plain-one\n' > ~/.gitconfig; [ ! -L ~/.gitconfig ]`)

	t.Run("Recreate", func(t *testing.T) {
		l := *l
		l.t = t
		l.stop(rt)
		if err := rt.WipeHome(ctx, HomeWipeRepos); err != nil {
			t.Fatalf("WipeHome(repos): %v", err)
		}
		l.startRunning(rt)
		out := l.sh(rt, `[ -e ~/repos ] && echo repos-present || echo repos-gone
cat ~/other/f 2>/dev/null || echo other-gone
cat ~/.ssh/live-marker 2>/dev/null || echo ssh-gone
[ -L ~/.gitconfig ] && echo gitconfig-link || echo gitconfig-plain
cat ~/.gitconfig`)
		t.Logf("after Recreate:\n%s", out)
		for _, want := range []string{"repos-gone", "\no\n", "\ns\n", "gitconfig-link", "plain-one"} {
			if !strings.Contains("\n"+out+"\n", want) {
				t.Errorf("after Recreate the home lacks %q", strings.TrimSpace(want))
			}
		}
	})

	t.Run("CleanHome", func(t *testing.T) {
		l := *l
		l.t = t
		l.sh(rt, `set -e
mkdir -p ~/repos/b; rm -f ~/.gitconfig; printf '[user]\n\tname = plain-two\n' > ~/.gitconfig; [ ! -L ~/.gitconfig ]`)
		l.stop(rt)
		if err := rt.WipeHome(ctx, HomeWipeClean); err != nil {
			t.Fatalf("WipeHome(clean): %v", err)
		}
		l.startRunning(rt)
		out := l.sh(rt, `[ -e ~/repos/b ] && echo repos-present || echo repos-gone
[ -e ~/other ] && echo other-present || echo other-gone
cat ~/.ssh/live-marker 2>/dev/null || echo ssh-gone
[ -L ~/.gitconfig ] && echo gitconfig-link || echo gitconfig-plain
cat ~/.gitconfig`)
		t.Logf("after Clean home:\n%s", out)
		for _, want := range []string{"repos-gone", "other-gone", "\ns\n", "gitconfig-link", "plain-two"} {
			if !strings.Contains("\n"+out+"\n", want) {
				t.Errorf("after Clean home the home lacks %q", strings.TrimSpace(want))
			}
		}
		// A pod restart finds the wipe recorded and removes nothing.
		l.sh(rt, `mkdir -p ~/after-wipe/repos; echo k > ~/after-wipe/f; mkdir -p ~/repos; echo k > ~/repos/kept`)
		old := l.pods(rt.base)[0]
		l.must("-n", l.ns, "delete", "pod", old.Metadata.Name, "--wait=false")
		l.eventually(2*time.Minute, "the replacement pod", func() bool {
			for _, p := range l.pods(rt.base) {
				if p.Metadata.UID != old.Metadata.UID {
					return true
				}
			}
			return false
		})
		l.waitState(rt, "running", kubeLiveRunning)
		if out := l.sh(rt, `cat ~/after-wipe/f ~/repos/kept`); out != "k\nk" {
			t.Errorf("a pod restart wiped the home again: %q", out)
		}
	})
}

// TestKubernetesLiveEraseHomeRestart: an administrator's Clean home whose CP dies while
// the erase pod runs. The restarted CP's EraseHome finds the pod by name and finishes;
// a Start in between refuses; the keep file a tool replaced survives.
func TestKubernetesLiveEraseHomeRestart(t *testing.T) {
	l := needKubeLive(t)
	name := l.workspace("erase")
	rt := l.runtime(l.factory(0), name)
	l.startRunning(rt)
	l.sh(rt, `set -e
mkdir -p ~/repos/a ~/bulk; echo r > ~/repos/a/f; echo s > ~/.ssh/live-marker
cd ~/bulk && seq 1 20000 | xargs touch
rm -f ~/.gitconfig; printf '[user]\n\tname = plain-erase\n' > ~/.gitconfig`)
	l.stop(rt)

	// The first CP starts the erase and dies as soon as the erase pod exists.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	t0 := time.Now()
	go func() { done <- rt.EraseHome(ctx) }()
	l.eventually(time.Minute, "the erase pod", func() bool {
		out, _ := l.run("-n", l.ns, "get", "pod", rt.erasePodName(), "--ignore-not-found", "-o", "name")
		return strings.TrimSpace(out) != ""
	})
	cancel()
	if err := <-done; err == nil {
		t.Log("the first EraseHome finished before its CP died; the restart below finds no pod running")
	} else {
		t.Logf("the first CP died: %v", err)
	}
	var ep kPod
	l.getJSON(&ep, "-n", l.ns, "get", "pod", rt.erasePodName())
	t.Logf("the erase pod at the restart: phase %s", ep.Status.Phase)

	restarted := l.runtime(l.factory(0), name)
	if !podFinished(&ep) {
		if err := restarted.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "Clean home") {
			t.Errorf("Start next to a running erase pod = %v, want a refusal", err)
		} else {
			t.Logf("Start next to the running erase pod refused: %v", err)
		}
	}
	ectx, ecancel := context.WithTimeout(context.Background(), homeEraseBudgetLive)
	defer ecancel()
	if err := restarted.EraseHome(ectx); err != nil {
		t.Fatalf("EraseHome from the restarted CP: %v", err)
	}
	t.Logf("EraseHome across a CP restart: %s", time.Since(t0).Round(time.Second))
	if out, _ := l.run("-n", l.ns, "get", "pod", rt.erasePodName(), "--ignore-not-found", "-o", "name"); strings.TrimSpace(out) != "" {
		t.Errorf("the erase pod is still there after EraseHome returned: %s", out)
	}
	l.startRunning(restarted)
	out := l.sh(restarted, `[ -e ~/repos ] && echo repos-present || echo repos-gone
[ -e ~/bulk ] && echo bulk-present || echo bulk-gone
cat ~/.ssh/live-marker 2>/dev/null || echo ssh-gone
cat ~/.gitconfig 2>/dev/null || echo gitconfig-gone`)
	t.Logf("after the erase:\n%s", out)
	for _, want := range []string{"repos-gone", "bulk-gone", "\ns\n", "plain-erase"} {
		if !strings.Contains("\n"+out+"\n", want) {
			t.Errorf("after the erase the home lacks %q", strings.TrimSpace(want))
		}
	}
}

// homeEraseBudgetLive is the budget the CP gives an administrator's Clean home
// (homeEraseBudget in workspace_lifecycle.go).
const homeEraseBudgetLive = 5 * time.Minute

// TestKubernetesLiveDestroyRestart: Destroy whose CP dies once the claims are gone, and
// Destroy with a volume held back, each finished by a restarted CP — across the boundary
// between Destroy returning and the CP recording the residue, where the StatefulSet's
// inventory is the only record.
func TestKubernetesLiveDestroyRestart(t *testing.T) {
	l := needKubeLive(t)

	t.Run("CPRestartAfterClaimsGone", func(t *testing.T) {
		l := *l
		l.t = t
		name := l.workspace("dcrash")
		rt := l.runtime(l.factory(0), name)
		l.startRunning(rt)
		vols := l.volumesOf(rt)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			res, err := rt.Destroy(ctx)
			t.Logf("the first CP's Destroy (its answer is lost with the CP): %v, %v", res, err)
		}()
		l.eventually(5*time.Minute, "both claims gone", func() bool {
			out, _ := l.run("-n", l.ns, "get", "pvc", rt.homeClaim(), rt.stateClaim(), "--ignore-not-found", "-o", "name")
			return strings.TrimSpace(out) == ""
		})
		cancel()
		<-done
		s := l.sts(rt.base)
		inv, ok := readInventory(&s)
		if !ok || len(inv.Volumes) != 2 {
			t.Fatalf("the StatefulSet's inventory after the crash = %+v (readable %v), want both volumes", inv, ok)
		}
		restarted := l.runtime(l.factory(0), name)
		res, err := restarted.Destroy(context.Background())
		if err != nil || len(res) != 0 {
			t.Fatalf("Destroy from the restarted CP = %v, %v; want no residue", res, err)
		}
		if out := strings.TrimSpace(l.must("-n", l.ns, "get", "statefulset", rt.base, "--ignore-not-found", "-o", "name")); out != "" {
			t.Errorf("the StatefulSet is still there after a clean Destroy")
		}
		l.requireDisksGone(vols)
	})

	t.Run("VolumeLeftBehind", func(t *testing.T) {
		l := *l
		l.t = t
		name := l.workspace("dleft")
		rt := l.runtime(l.factory(0), name)
		l.startRunning(rt)
		vols := l.volumesOf(rt)
		if len(vols) != 2 {
			t.Fatalf("volumes %v, want two", vols)
		}
		held := vols[0]
		l.hold(held)
		prev := kubeDestroyClaimBudget
		kubeDestroyClaimBudget = 45 * time.Second
		defer func() { kubeDestroyClaimBudget = prev }()
		res, err := rt.Destroy(context.Background())
		want := []string{"pv:" + held, "statefulset:" + l.ns + "/" + rt.base}
		if err != nil || !slices.Equal(res, want) {
			t.Fatalf("Destroy with %s held = %v, %v; want %v", held, res, err, want)
		}
		// The CP restarts before recording that residue: the StatefulSet is now the only
		// record, and a re-run must still name the volume, not rebuild the inventory from
		// claims that are gone.
		restarted := l.runtime(l.factory(0), name)
		res, err = restarted.Destroy(context.Background())
		if err != nil || !slices.Equal(res, want) {
			t.Fatalf("Destroy re-run by a restarted CP = %v, %v; want %v", res, err, want)
		}
		l.unhold(held)
		kubeDestroyClaimBudget = prev
		l.eventually(3*time.Minute, "the released volume to go", func() bool {
			out, _ := l.run("get", "pv", held, "--ignore-not-found", "-o", "name")
			return strings.TrimSpace(out) == ""
		})
		res, err = restarted.Destroy(context.Background())
		if err != nil || len(res) != 0 {
			t.Fatalf("Destroy once the volume went = %v, %v; want no residue", res, err)
		}
		if out := strings.TrimSpace(l.must("-n", l.ns, "get", "statefulset", rt.base, "--ignore-not-found", "-o", "name")); out != "" {
			t.Errorf("the StatefulSet is still there after a clean Destroy")
		}
		l.requireDisksGone(vols)
	})
}

// kubeLiveProbe is one TCP destination probed from inside a workspace pod.
type kubeLiveProbe struct {
	label, host, port string
	open              bool   // what decision 7 expects
	known             string // a filed issue when the cluster is known to differ; "" = a hard assertion
}

// TestKubernetesLiveNetworkProbes: decision 7's probes from inside a workspace pod, as the
// workspace user, with nothing but the image's bash (/dev/tcp) and timeout: the metadata
// server, the API server's Service and the control-plane endpoint, the node, kube-dns, and
// the deployment's own addresses from AF_K8S_LIVE_PROBE_OPEN / AF_K8S_LIVE_PROBE_CLOSED
// (host:port lists: the CP's internal port, its other ports, Cloud SQL, unused VPC
// addresses).
//
// Every node's addresses, the pod's own node included, are reachable on GKE Dataplane V2
// (#1578). Decision 7 accepts the own node (NetworkPolicy always allows it, and the
// requirement there is no unauthenticated service, so the kubelet's read-only port is a
// hard assertion); the other nodes are expected closed and recorded as a known failure
// until #1578 is settled. AF_K8S_LIVE_STRICT=1 turns known failures into failures.
func TestKubernetesLiveNetworkProbes(t *testing.T) {
	l := needKubeLive(t)
	rt := l.runtime(l.factory(0), l.workspace("net"))
	l.startRunning(rt)

	var probes []kubeLiveProbe
	add := func(label, hostport string, open bool, known string) {
		h, p, ok := strings.Cut(hostport, ":")
		if !ok || h == "" || p == "" {
			t.Fatalf("probe %s: %q is not host:port", label, hostport)
		}
		probes = append(probes, kubeLiveProbe{label, h, p, open, known})
	}
	add("metadata server", "169.254.169.254:80", false, "")
	add("API server Service", strings.TrimSpace(l.must("get", "svc", "kubernetes", "-n", "default", "-o", "jsonpath={.spec.clusterIP}:{.spec.ports[0].port}")), false, "")
	if u, err := url.Parse(os.Getenv("AF_K8S_LIVE_SERVER")); err == nil && u.Hostname() != "" {
		port := u.Port()
		if port == "" {
			port = "443"
		}
		add("control-plane endpoint", u.Hostname()+":"+port, false, "")
	}
	add("kube-dns", strings.TrimSpace(l.must("get", "svc", "kube-dns", "-n", "kube-system", "-o", "jsonpath={.spec.clusterIP}"))+":53", true, "")
	for _, hp := range splitCSV(os.Getenv("AF_K8S_LIVE_PROBE_OPEN")) {
		add("deployment (open)", hp, true, "")
	}
	for _, hp := range splitCSV(os.Getenv("AF_K8S_LIVE_PROBE_CLOSED")) {
		add("deployment (closed)", hp, false, "")
	}
	own := l.pods(rt.base)[0].Spec.NodeName
	var nodes struct {
		Items []struct {
			Metadata kObjectMeta `json:"metadata"`
			Status   struct {
				Addresses []struct{ Type, Address string } `json:"addresses"`
			} `json:"status"`
		} `json:"items"`
	}
	l.getJSON(&nodes, "get", "nodes")
	others := 0
	for _, n := range nodes.Items {
		ip := ""
		for _, a := range n.Status.Addresses {
			if a.Type == "InternalIP" {
				ip = a.Address
			}
		}
		if ip == "" {
			continue
		}
		if n.Metadata.Name == own {
			// Reachable by design; it must offer nothing unauthenticated.
			add("own node kubelet read-only", ip+":10255", false, "")
			for _, p := range []string{"22", "10250", "10256"} {
				probes = append(probes, kubeLiveProbe{label: "own node (informational)", host: ip, port: p, open: true, known: "informational"})
			}
			continue
		}
		others++
		for _, p := range []string{"22", "10250", "10256"} {
			add("other node "+n.Metadata.Name, ip+":"+p, false, "#1578")
		}
	}
	if others == 0 {
		t.Log("other-node probe: NOT RUN — the cluster has a single node")
	}

	var script strings.Builder
	script.WriteString("probe() { if timeout 3 bash -c \"exec 3<>/dev/tcp/$1/$2\" 2>/dev/null; then echo \"$1:$2 open\"; else echo \"$1:$2 closed\"; fi; }\n")
	for _, p := range probes {
		fmt.Fprintf(&script, "probe %s %s\n", shellQuote(p.host), shellQuote(p.port))
	}
	out := l.must("-n", l.ns, "exec", rt.base+"-0", "-c", kubeAgentName, "--", "bash", "-c", script.String())
	got := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if hp, state, ok := strings.Cut(line, " "); ok {
			got[hp] = state == "open"
		}
	}
	strict := os.Getenv("AF_K8S_LIVE_STRICT") == "1"
	var knownFailures []string
	for _, p := range probes {
		hp := p.host + ":" + p.port
		isOpen, ok := got[hp]
		if !ok {
			t.Errorf("%s %s: no answer from the probe script", p.label, hp)
			continue
		}
		word := map[bool]string{true: "open", false: "closed"}
		t.Logf("%-28s %-24s %s (expected %s)", p.label, hp, word[isOpen], word[p.open])
		switch {
		case p.known == "informational" || isOpen == p.open:
		case p.known != "" && !strict:
			knownFailures = append(knownFailures, fmt.Sprintf("%s %s is %s", p.label, hp, word[isOpen]))
		default:
			t.Errorf("%s %s is %s, want %s", p.label, hp, word[isOpen], word[p.open])
		}
	}
	if len(knownFailures) > 0 {
		t.Logf("KNOWN FAILURE (#1578, expected closed): %s", strings.Join(knownFailures, "; "))
	}
}

// --- DISRUPTIVE: these act on nodes, and need AF_K8S_LIVE_DISRUPTIVE=1 besides ---

func needKubeLiveDisruptive(t *testing.T) *kubeLive {
	t.Helper()
	l := needKubeLive(t)
	if os.Getenv("AF_K8S_LIVE_DISRUPTIVE") != "1" {
		t.Skip("SKIPPED: disruptive node scenario — set AF_K8S_LIVE_DISRUPTIVE=1 (it cordons nodes or stops a node's VM; read deploy/kubernetes/README.md, \"The live harness\", first)")
	}
	l.guardPod()
	return l
}

// guardPod watches a pod the scenario must not disturb — a member's workspace on a shared
// cluster — named by AF_K8S_LIVE_GUARD_POD as <namespace>/<name>: it must be Running before
// the test and, once every other cleanup has run, still be the same pod (UID) on the same
// node with the same restart count.
func (l *kubeLive) guardPod() {
	l.t.Helper()
	ref := os.Getenv("AF_K8S_LIVE_GUARD_POD")
	if ref == "" {
		l.t.Log("no AF_K8S_LIVE_GUARD_POD: nothing outside the harness is watched")
		return
	}
	ns, name, ok := strings.Cut(ref, "/")
	if !ok {
		l.t.Fatalf("AF_K8S_LIVE_GUARD_POD=%q: want <namespace>/<name>", ref)
	}
	read := func() (kPod, string) {
		var p kPod
		out, err := l.run("-n", ns, "get", "pod", name, "-o", "json")
		if err != nil || json.Unmarshal([]byte(out), &p) != nil {
			return p, fmt.Sprintf("unreadable: %v", err)
		}
		restarts := 0
		for _, cs := range p.Status.ContainerStatuses {
			restarts += int(cs.RestartCount)
		}
		return p, fmt.Sprintf("uid=%s node=%s phase=%s restarts=%d", p.Metadata.UID, p.Spec.NodeName, p.Status.Phase, restarts)
	}
	p, before := read()
	if p.Status.Phase != "Running" {
		l.t.Fatalf("the guarded pod %s is not Running before the test: %s", ref, before)
	}
	l.t.Logf("guarded pod %s before: %s", ref, before)
	// Registered first, so it runs after every other cleanup.
	l.t.Cleanup(func() {
		_, after := read()
		l.t.Logf("guarded pod %s after: %s", ref, after)
		if after != before {
			l.t.Errorf("the guarded pod %s changed: before %s, after %s", ref, before, after)
		}
	})
}

// nodeExpand fills {node} and {zone} into an operator command.
func (l *kubeLive) nodeExpand(cmd, node string) string {
	l.t.Helper()
	zone := strings.TrimSpace(l.must("get", "node", node, "-o", `jsonpath={.metadata.labels.topology\.kubernetes\.io/zone}`))
	return strings.NewReplacer("{node}", node, "{zone}", zone).Replace(cmd)
}

func (l *kubeLive) operator(cmd string) string {
	l.t.Helper()
	l.t.Logf("operator: %s", cmd)
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		l.t.Fatalf("%s: %v\n%s", cmd, err, out)
	}
	return string(out)
}

// cordon cordons each node for the rest of the test and uncordons it at the end.
func (l *kubeLive) cordon(nodes ...string) {
	l.t.Helper()
	for _, n := range nodes {
		if n == "" {
			continue
		}
		l.must("cordon", n)
		l.t.Cleanup(func() {
			if _, err := l.run("uncordon", n); err != nil {
				l.t.Errorf("UNCORDON BY HAND: %v", err)
			}
			if out, _ := l.run("get", "node", n, "-o", "jsonpath={.spec.unschedulable}"); strings.TrimSpace(out) == "true" {
				l.t.Errorf("node %s is still unschedulable: UNCORDON BY HAND", n)
			} else {
				l.t.Logf("node %s is schedulable again", n)
			}
		})
	}
}

func (l *kubeLive) nodeReady(node string) bool {
	var n struct {
		Status struct {
			Conditions []kPodCondition `json:"conditions"`
		} `json:"status"`
	}
	out, err := l.run("get", "node", node, "-o", "json")
	if err != nil || json.Unmarshal([]byte(out), &n) != nil {
		return false
	}
	for _, c := range n.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

func (l *kubeLive) runningPod(rt *kubeRuntime) kPod {
	l.t.Helper()
	for _, p := range l.pods(rt.base) {
		if p.Metadata.DeletionTimestamp == nil {
			return p
		}
	}
	l.t.Fatalf("%s has no pod", rt.base)
	return kPod{}
}

// TestKubernetesLivePlannedUpgrade: the runbook's planned node upgrade, up to the point
// that concerns the adapter: with AF_K8S_LIVE_CORDON_NODES cordoned, a Start lands on
// another node (the autoscaler adds one when none is free). It does not drain: a drain
// would cut every session on the node, the harness's own scenarios have stopped theirs.
func TestKubernetesLivePlannedUpgrade(t *testing.T) {
	l := needKubeLiveDisruptive(t)
	cordoned := splitCSV(os.Getenv("AF_K8S_LIVE_CORDON_NODES"))
	if len(cordoned) == 0 {
		t.Fatal("set AF_K8S_LIVE_CORDON_NODES to the node(s) to cordon, comma-separated")
	}
	l.cordon(cordoned...)
	rt := l.runtime(l.factory(0), l.workspace("upgr"))
	l.startRunning(rt)
	p := l.runningPod(rt)
	if slices.Contains(cordoned, p.Spec.NodeName) {
		t.Fatalf("the Start issued while %v was cordoned landed on %s", cordoned, p.Spec.NodeName)
	}
	t.Logf("with %v cordoned, the start landed on %s", cordoned, p.Spec.NodeName)
}

// TestKubernetesLiveNodeUnreachable: a node made unreachable under a running workspace.
// Stop returns an error and Start refuses while the pod is there; the runbook's recovery
// is followed — proof from the provider that the VM is stopped, then the out-of-service
// taint — and the workspace starts again elsewhere.
//
// The node is the one the workspace lands on. AF_K8S_LIVE_CORDON_NODES keeps the
// workspace off nodes that run anything else (cordoned for the whole test), so that only
// a node the autoscaler added for it is stopped. The three commands take {node} and
// {zone}: AF_K8S_LIVE_NODE_STOP_CMD stops the VM, AF_K8S_LIVE_NODE_STATUS_CMD prints its
// state from the provider (TERMINATED is the proof the runbook asks for), and
// AF_K8S_LIVE_NODE_START_CMD starts it again at the end.
func TestKubernetesLiveNodeUnreachable(t *testing.T) {
	l := needKubeLiveDisruptive(t)
	stopCmd, statusCmd, startCmd := os.Getenv("AF_K8S_LIVE_NODE_STOP_CMD"), os.Getenv("AF_K8S_LIVE_NODE_STATUS_CMD"), os.Getenv("AF_K8S_LIVE_NODE_START_CMD")
	if stopCmd == "" || statusCmd == "" || startCmd == "" {
		t.Fatal("set AF_K8S_LIVE_NODE_STOP_CMD, AF_K8S_LIVE_NODE_STATUS_CMD and AF_K8S_LIVE_NODE_START_CMD")
	}
	ctx := context.Background()
	cordoned := splitCSV(os.Getenv("AF_K8S_LIVE_CORDON_NODES"))
	l.cordon(cordoned...)
	rt := l.runtime(l.factory(0), l.workspace("unreach"))
	l.startRunning(rt)
	node := l.runningPod(rt).Spec.NodeName
	if slices.Contains(cordoned, node) {
		t.Fatalf("the workspace landed on the cordoned node %s", node)
	}
	var others struct {
		Items []kPod `json:"items"`
	}
	l.getJSON(&others, "get", "pods", "-A", "--field-selector", "spec.nodeName="+node)
	for _, p := range others.Items {
		if p.Metadata.Namespace == l.ns && p.Metadata.Labels[kubeLabelWorkspace] != rt.base {
			t.Fatalf("node %s also runs %s; refusing to stop it", node, p.Metadata.Name)
		}
	}
	t.Logf("making %s unreachable", node)
	t.Cleanup(func() {
		l.run("taint", "nodes", node, "node.kubernetes.io/out-of-service-")
		if out, err := exec.Command("sh", "-c", l.nodeExpand(startCmd, node)).CombinedOutput(); err != nil {
			t.Errorf("START THE VM BY HAND (%s): %v\n%s", node, err, out)
		}
	})
	t0 := time.Now()
	l.operator(l.nodeExpand(stopCmd, node))
	l.eventually(10*time.Minute, node+" not Ready", func() bool { return !l.nodeReady(node) })
	t.Logf("%s not Ready after %s", node, time.Since(t0).Round(time.Second))

	err := rt.Stop(ctx)
	if err == nil || !strings.Contains(err.Error(), "not settled") {
		t.Fatalf("Stop with the node unreachable = %v, want a not-settled error", err)
	}
	t.Logf("Stop: %v", err)
	if got := rt.State(ctx); got != "stopped" {
		t.Errorf("State after the failed Stop = %q, want stopped", got)
	}
	if err := rt.Start(ctx); err == nil || !strings.Contains(err.Error(), "has not settled") {
		t.Fatalf("Start over the unreachable pod = %v, want a refusal", err)
	}

	// The runbook: proof from the provider first, then the taint.
	if st := l.operator(l.nodeExpand(statusCmd, node)); !strings.Contains(st, "TERMINATED") {
		t.Fatalf("the provider says %q, not TERMINATED: the runbook does nothing without that proof", strings.TrimSpace(st))
	}
	l.must("taint", "nodes", node, "node.kubernetes.io/out-of-service=nodeshutdown:NoExecute", "--overwrite")
	t1 := time.Now()
	l.eventually(10*time.Minute, "the pod on the stopped node to go", func() bool { return len(l.pods(rt.base)) == 0 })
	t.Logf("pod gone %s after the taint", time.Since(t1).Round(time.Second))
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("Stop once the pod is gone: %v", err)
	}
	l.startRunning(rt)
	if p := l.runningPod(rt); p.Spec.NodeName == node {
		t.Fatalf("running again on the stopped node %s", node)
	}
	t.Logf("node unreachable: recovered and running again on %s, %s after the VM stop", l.runningPod(rt).Spec.NodeName, time.Since(t0).Round(time.Second))
}
