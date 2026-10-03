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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	// kubePin holds the flags every kubectl call carries once pinKubectl has checked the
	// context against the product's API server and CA: the context for the credentials, and
	// the verified server and CA themselves, so that neither a switched context nor a
	// context redefined in the kubeconfig mid-run can send the harness's deletes, cordons
	// and taints to another cluster.
	kubePin []string
	tokens  map[string]string // workspace name -> AGENT_TOKEN, stable across adapter values
}

// kubeLiveCmdBudget bounds one kubectl call (an exec into a pod included) and one operator
// command: a hung API server or CLI fails the step, and the test's cleanups still run.
// go test's own -timeout panics without running them.
const (
	kubeLiveCmdBudget      = 3 * time.Minute
	kubeLiveOperatorBudget = 6 * time.Minute
)

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
	l.pinKubectl()
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

// pinKubectl checks that kubectl's current context is the cluster the product talks to —
// the same API server URL and the same CA — and pins every later call to that context.
func (l *kubeLive) pinKubectl() {
	l.t.Helper()
	ctxName := strings.TrimSpace(l.must("config", "current-context"))
	if ctxName == "" {
		l.t.Fatal("kubectl has no current context")
	}
	server := strings.TrimSpace(l.must("config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}"))
	if want := strings.TrimRight(os.Getenv("AF_K8S_LIVE_SERVER"), "/"); strings.TrimRight(server, "/") != want {
		l.t.Fatalf("kubectl's context %s points at %s, the product at %s: refusing to act on another cluster", ctxName, server, want)
	}
	// --flatten inlines a CA file, wherever its relative path resolves from.
	b64 := strings.TrimSpace(l.must("config", "view", "--raw", "--flatten", "--minify", "-o", "jsonpath={.clusters[0].cluster.certificate-authority-data}"))
	ca, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(ca) == 0 {
		l.t.Fatalf("kubectl's context %s carries no readable CA (%v)", ctxName, err)
	}
	want, err := os.ReadFile(os.Getenv("AF_K8S_LIVE_CA_FILE"))
	if err != nil {
		l.t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(ca), bytes.TrimSpace(want)) {
		l.t.Fatalf("kubectl's context %s trusts another CA than AF_K8S_LIVE_CA_FILE: refusing to act on another cluster", ctxName)
	}
	l.kubePin = []string{"--context", ctxName, "--server", server,
		"--certificate-authority", os.Getenv("AF_K8S_LIVE_CA_FILE"), "--insecure-skip-tls-verify=false"}
}

// workspace names a new workspace for scenario, refuses a name anything in the namespace
// already uses, and registers its cleanup only then: the cleanup destroys by name, and
// must never reach a workspace the harness did not create.
func (l *kubeLive) workspace(scenario string) string {
	l.t.Helper()
	name := "live-" + scenario + "-" + randHexT(6)
	base := kubeObjectName(name)
	if !strings.HasPrefix(base, "live-") || base != name {
		l.t.Fatalf("workspace name %q maps to %q; the harness only touches names it can recognise as its own", name, base)
	}
	left := l.mustList("-n", l.ns, "get", "statefulset,pod,pvc,service,secret", "-l", kubeLabelWorkspace+"="+base)
	for _, obj := range []string{"statefulset/" + base, "pvc/" + base + "-home", "pvc/" + base + "-state",
		"service/" + base, "secret/" + base + "-env", "pod/" + base + "-erase"} {
		left = append(left, l.mustList("-n", l.ns, "get", obj, "--ignore-not-found")...)
	}
	if len(left) > 0 {
		l.t.Fatalf("workspace %s: objects with its names already exist (%v); refusing to use it", name, left)
	}
	l.tokens[name] = randHexT(16)
	l.t.Cleanup(func() { l.purge(name) })
	return name
}

// mustList runs a kubectl get and returns the object names it printed; an error fails the
// test, never reads as "nothing there".
func (l *kubeLive) mustList(args ...string) []string {
	l.t.Helper()
	return strings.Fields(l.must(append(args, "-o", "name")...))
}

// exists reports whether kubectl get finds the object. An error is an error: an
// unreadable API server must not pass for an object that is gone.
func (l *kubeLive) exists(args ...string) (bool, error) {
	out, err := l.run(append(args, "--ignore-not-found", "-o", "name")...)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// gone is exists for a polling condition inside the test: an error fails the test.
func (l *kubeLive) gone(args ...string) bool {
	l.t.Helper()
	ok, err := l.exists(args...)
	if err != nil {
		l.t.Fatal(err)
	}
	return !ok
}

func (l *kubeLive) runtime(f *kubeFactory, name string) *kubeRuntime {
	return f.New(Workspace{ContainerName: name, AgentToken: l.tokens[name], MemBytes: 2 * gib, CPUUnits: 512},
		randHexT(16), nil).(*kubeRuntime)
}

// --- the harness's own eyes: kubectl as the ambient identity ---

func (l *kubeLive) run(args ...string) (string, error) {
	args = append(slices.Clone(l.kubePin), args...)
	ctx, cancel := context.WithTimeout(context.Background(), kubeLiveCmdBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, l.kubectl, args...)
	cmd.WaitDelay = 10 * time.Second
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
		ctx, cancel := context.WithTimeout(context.Background(), kubeLiveCmdBudget)
		out, err := exec.CommandContext(ctx, "gcloud", "compute", "disks", "list", "--project", project,
			"--filter", filter, "--format", "value(name)").Output()
		cancel()
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
	if out, err := l.run("get", "pv", "-o", "json"); err != nil {
		// Without the list the volumes cannot be followed, nor a hold released: say so.
		t.Errorf("cleanup of %s: cannot list the volumes, check them by hand: %v", name, err)
	} else {
		var list struct {
			Items []struct {
				Metadata kObjectMeta `json:"metadata"`
				Spec     struct {
					ClaimRef *struct{ Namespace, Name string } `json:"claimRef"`
				} `json:"spec"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(out), &list); err != nil {
			t.Errorf("cleanup of %s: decode the volume list: %v", name, err)
		}
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
		// An unreadable answer counts as still there: only a read that found nothing is gone.
		var left []string
		for _, kind := range []string{"statefulset", "pod", "service", "secret", "pvc"} {
			out, err := l.run("-n", l.ns, "get", kind, "-l", sel, "-o", "name")
			if err != nil {
				left = append(left, kind+" (unreadable: "+err.Error()+")")
			} else {
				left = append(left, strings.Fields(out)...)
			}
		}
		for _, v := range vols {
			if ok, err := l.exists("get", "pv", v); err != nil || ok {
				left = append(left, fmt.Sprintf("pv/%s (%v)", v, err))
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

// hold keeps the volume from going until unhold; the release is registered before the
// hold is placed, so a test that stops halfway still lets the volume go.
func (l *kubeLive) hold(pv string) {
	l.t.Helper()
	l.t.Cleanup(func() { l.unhold(pv) })
	l.must("patch", "pv", pv, "--type=json", "-p",
		`[{"op":"add","path":"/metadata/finalizers/-","value":"`+kubeLiveHold+`"}]`)
}

func (l *kubeLive) unhold(pv string) {
	var obj kPV
	out, err := l.run("get", "pv", pv, "--ignore-not-found", "-o", "json")
	if err != nil {
		l.t.Errorf("read %s to release the harness's hold, release it by hand: %v", pv, err)
		return
	}
	if strings.TrimSpace(out) == "" {
		return
	}
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		l.t.Errorf("decode %s: %v", pv, err)
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
		if created == 0 {
			t.Errorf("INCONCLUSIVE: the controller created no pod in any round, so no Stop met a pod being created")
		}
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
		if lagging == 0 {
			t.Errorf("INCONCLUSIVE: no read landed before the controller observed the new template")
		}
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

	// The first CP starts the erase and dies as soon as the erase pod exists. The pod then
	// still has its volume to attach, so it is pending, not finished, at the restart; a run
	// where it had finished is reported as inconclusive, not passed.
	ctx, cancel := context.WithCancel(context.Background())
	var firstErr error
	done := make(chan struct{})
	t0 := time.Now()
	go func() { defer close(done); firstErr = rt.EraseHome(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	l.eventually(time.Minute, "the erase pod", func() bool { return !l.gone("-n", l.ns, "get", "pod", rt.erasePodName()) })
	cancel()
	<-done
	var ep kPod
	l.getJSON(&ep, "-n", l.ns, "get", "pod", rt.erasePodName())
	t.Logf("the first CP died (%v); the erase pod at the restart: phase %s", firstErr, ep.Status.Phase)
	if firstErr == nil || podFinished(&ep) {
		t.Errorf("INCONCLUSIVE: the erase finished before the CP restart, so the restart did not meet a running erase pod")
	}

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
	if !l.gone("-n", l.ns, "get", "pod", rt.erasePodName()) {
		t.Errorf("the erase pod is still there after EraseHome returned")
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
		if len(vols) != 2 {
			t.Fatalf("volumes %v, want two", vols)
		}
		// One volume is held so that the first Destroy is still waiting when its CP dies:
		// with a fast provisioner it could otherwise finish, StatefulSet and all, first.
		l.hold(vols[0])
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			res, err := rt.Destroy(ctx)
			t.Logf("the first CP's Destroy (its answer is lost with the CP): %v, %v", res, err)
		}()
		t.Cleanup(func() { cancel(); <-done })
		l.eventually(5*time.Minute, "both claims gone", func() bool {
			return l.gone("-n", l.ns, "get", "pvc", rt.homeClaim()) && l.gone("-n", l.ns, "get", "pvc", rt.stateClaim())
		})
		cancel()
		<-done
		l.unhold(vols[0])
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
		if !l.gone("-n", l.ns, "get", "statefulset", rt.base) {
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
		l.eventually(3*time.Minute, "the released volume to go", func() bool { return l.gone("get", "pv", held) })
		res, err = restarted.Destroy(context.Background())
		if err != nil || len(res) != 0 {
			t.Fatalf("Destroy once the volume went = %v, %v; want no residue", res, err)
		}
		if !l.gone("-n", l.ns, "get", "statefulset", rt.base) {
			t.Errorf("the StatefulSet is still there after a clean Destroy")
		}
		l.requireDisksGone(vols)
	})
}

// kubeLiveProbe is one TCP destination probed from inside a workspace pod.
type kubeLiveProbe struct {
	label, host, port string
	open              bool // what decision 7 expects
}

// TestKubernetesLiveNetworkProbes: decision 7's probes from inside a workspace pod, as the
// workspace user, with nothing but the image's bash (/dev/tcp) and timeout: the metadata
// server, the API server's Service and the control-plane endpoint, every node (the pod's
// own included), kube-dns, an internet address as the open control
// (AF_K8S_LIVE_PROBE_INTERNET, 1.1.1.1:443 by default), and the deployment's own addresses
// from AF_K8S_LIVE_PROBE_OPEN / AF_K8S_LIVE_PROBE_CLOSED (host:port lists: the CP's internal
// port, its other ports, Cloud SQL, unused VPC addresses).
//
// No node is reachable: on GKE Dataplane V2 an ipBlock that contains a node address also
// selects the nodes, which an `except` cannot remove, so the egress policy lists the
// complement of the private ranges instead (#1578). A cluster still on the older policy
// fails the node probes here.
func TestKubernetesLiveNetworkProbes(t *testing.T) {
	l := needKubeLive(t)
	rt := l.runtime(l.factory(0), l.workspace("net"))
	l.startRunning(rt)

	var probes []kubeLiveProbe
	add := func(label, hostport string, open bool) {
		h, p, ok := strings.Cut(hostport, ":")
		if !ok || h == "" || p == "" {
			t.Fatalf("probe %s: %q is not host:port", label, hostport)
		}
		probes = append(probes, kubeLiveProbe{label, h, p, open})
	}
	add("metadata server", "169.254.169.254:80", false)
	add("API server Service", strings.TrimSpace(l.must("get", "svc", "kubernetes", "-n", "default", "-o", "jsonpath={.spec.clusterIP}:{.spec.ports[0].port}")), false)
	if u, err := url.Parse(os.Getenv("AF_K8S_LIVE_SERVER")); err == nil && u.Hostname() != "" {
		port := u.Port()
		if port == "" {
			port = "443"
		}
		add("control-plane endpoint", u.Hostname()+":"+port, false)
	}
	add("kube-dns", strings.TrimSpace(l.must("get", "svc", "kube-dns", "-n", "kube-system", "-o", "jsonpath={.spec.clusterIP}"))+":53", true)
	add("internet (control)", envOr("AF_K8S_LIVE_PROBE_INTERNET", "1.1.1.1:443"), true)
	for _, hp := range splitCSV(os.Getenv("AF_K8S_LIVE_PROBE_OPEN")) {
		add("deployment (open)", hp, true)
	}
	for _, hp := range splitCSV(os.Getenv("AF_K8S_LIVE_PROBE_CLOSED")) {
		add("deployment (closed)", hp, false)
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
	for _, n := range nodes.Items {
		ip := ""
		for _, a := range n.Status.Addresses {
			if a.Type == "InternalIP" {
				ip = a.Address
			}
		}
		if ip == "" {
			t.Errorf("node %s has no InternalIP to probe", n.Metadata.Name)
			continue
		}
		label := "other node " + n.Metadata.Name
		if n.Metadata.Name == own {
			label = "own node " + n.Metadata.Name
		}
		for _, p := range []string{"22", "10250", "10255", "10256"} {
			add(label, ip+":"+p, false)
		}
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
	word := map[bool]string{true: "open", false: "closed"}
	for _, p := range probes {
		hp := p.host + ":" + p.port
		isOpen, ok := got[hp]
		if !ok {
			t.Errorf("%s %s: no answer from the probe script", p.label, hp)
			continue
		}
		t.Logf("%-28s %-24s %s (expected %s)", p.label, hp, word[isOpen], word[p.open])
		if isOpen != p.open {
			t.Errorf("%s %s is %s, want %s", p.label, hp, word[isOpen], word[p.open])
		}
	}
}

// mustQuiet is run for cleanups: an error is logged, not fatal.
func (l *kubeLive) mustQuiet(args ...string) string {
	out, err := l.run(args...)
	if err != nil {
		l.t.Logf("%v", err)
	}
	return out
}

func (l *kubeLive) nodeUIDQuiet(node string) string {
	return strings.TrimSpace(l.mustQuiet("get", "node", node, "--ignore-not-found", "-o", "jsonpath={.metadata.uid}"))
}

// nodeReplaced reads whether the Node object named node is no longer the one with UID
// orig: gone (a successful read that found nothing) or another object. An unreadable node
// is read again a few times and then fails the test as INCONCLUSIVE — an API error must
// never pass for a node the provider replaced, which is what several verdicts rest on.
func (l *kubeLive) nodeReplaced(node, orig string) bool {
	l.t.Helper()
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(5 * time.Second)
		}
		var out string
		out, err = l.run("get", "node", node, "--ignore-not-found", "-o", "jsonpath={.metadata.uid}")
		var gone bool
		if gone, err = nodeReplacedFrom(out, err, orig); err == nil {
			return gone
		}
	}
	l.t.Fatalf("INCONCLUSIVE: cannot tell whether node %s was replaced: %v", node, err)
	return false
}

// nodeReplacedFrom decides from one read of a node's UID (kubectl get --ignore-not-found):
// replaced when the read found nothing or another UID; an error decides nothing.
func nodeReplacedFrom(uid string, readErr error, orig string) (bool, error) {
	if readErr != nil {
		return false, readErr
	}
	uid = strings.TrimSpace(uid)
	return uid == "" || uid != orig, nil
}

func (l *kubeLive) nodeUID(node string) string {
	l.t.Helper()
	return strings.TrimSpace(l.must("get", "node", node, "--ignore-not-found", "-o", "jsonpath={.metadata.uid}"))
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

// nodeCommands fills {node} and {zone} into the operator commands, once, before anything
// is stopped: a cleanup must not depend on reading the node again.
func (l *kubeLive) nodeCommands(node string, cmds ...string) []string {
	l.t.Helper()
	zone := strings.TrimSpace(l.must("get", "node", node, "-o", `jsonpath={.metadata.labels.topology\.kubernetes\.io/zone}`))
	if zone == "" {
		l.t.Fatalf("node %s has no zone label", node)
	}
	// {kubectl} is the harness's own pinned kubectl, so an operator command that acts
	// through the cluster reaches the verified one.
	kc := []string{shellQuote(l.kubectl)}
	for _, a := range l.kubePin {
		kc = append(kc, shellQuote(a))
	}
	r := strings.NewReplacer("{node}", node, "{zone}", zone, "{kubectl}", strings.Join(kc, " "))
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = r.Replace(c)
	}
	return out
}

func (l *kubeLive) operatorRun(cmd string) (string, error) {
	l.t.Logf("operator: %s", cmd)
	ctx, cancel := context.WithTimeout(context.Background(), kubeLiveOperatorBudget)
	defer cancel()
	// The command runs in a process group of its own, and the deadline kills the whole
	// group: killing sh alone would leave a gcloud child running, stopping a VM after the
	// test gave up, and holding the output pipe so that Wait never returns.
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = 10 * time.Second
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %v\n%s", cmd, err, out)
	}
	return string(out), nil
}

func (l *kubeLive) operator(cmd string) string {
	l.t.Helper()
	out, err := l.operatorRun(cmd)
	if err != nil {
		l.t.Fatal(err)
	}
	return out
}

// cordon cordons each node for the rest of the test. The restore is registered before the
// cordon and puts back the state the node had: a node an operator had already cordoned
// stays cordoned.
func (l *kubeLive) cordon(nodes ...string) {
	l.t.Helper()
	for _, n := range nodes {
		if n == "" {
			continue
		}
		was := strings.TrimSpace(l.must("get", "node", n, "-o", "jsonpath={.spec.unschedulable}")) == "true"
		if was {
			l.t.Logf("node %s was already cordoned; it is left cordoned", n)
			continue
		}
		uid := l.nodeUID(n)
		l.t.Cleanup(func() {
			// Only the Node object this test cordoned: one recreated under the same name is
			// someone else's, and was never cordoned by the harness.
			if now := l.nodeUIDQuiet(n); now != uid {
				l.t.Logf("node %s is gone or another object now (UID %q, was %q); nothing to uncordon", n, now, uid)
				return
			}
			if _, err := l.run("uncordon", n); err != nil {
				l.t.Errorf("UNCORDON %s BY HAND: %v", n, err)
				return
			}
			out, err := l.run("get", "node", n, "-o", "jsonpath={.spec.unschedulable}")
			switch {
			case err != nil:
				l.t.Errorf("cannot read node %s after the uncordon, check it by hand: %v", n, err)
			case strings.TrimSpace(out) == "true":
				l.t.Errorf("node %s is still unschedulable: UNCORDON BY HAND", n)
			default:
				l.t.Logf("node %s is schedulable again", n)
			}
		})
		l.must("cordon", n)
	}
}

// nodeNotReady reports a node whose Ready condition is explicitly False or Unknown. A
// node that cannot be read fails the test rather than passing for an unreachable one.
func (l *kubeLive) nodeNotReady(node string) bool {
	l.t.Helper()
	var n struct {
		Status struct {
			Conditions []kPodCondition `json:"conditions"`
		} `json:"status"`
	}
	l.getJSON(&n, "get", "node", node)
	for _, c := range n.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "False" || c.Status == "Unknown"
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
// would cut every session on the node.
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

// kubeLiveNodePod is the part of a pod the node check reads.
type kubeLiveNodePod struct {
	Metadata struct {
		Namespace       string            `json:"namespace"`
		Name            string            `json:"name"`
		UID             string            `json:"uid"`
		Labels          map[string]string `json:"labels"`
		Annotations     map[string]string `json:"annotations"`
		OwnerReferences []struct {
			Kind string `json:"kind"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
}

// TestKubernetesLiveNodeUnreachable: a node made unreachable under a running workspace.
// Stop returns an error and Start refuses while the pod is there; the runbook's recovery
// is followed — proof from the provider that the VM is stopped, then the out-of-service
// taint — and the workspace starts again elsewhere.
//
// It fails closed on which node it stops: AF_K8S_LIVE_CORDON_NODES must cordon the nodes
// that run anything else, the node the workspace lands on must carry the workspace node
// selector's labels and have been created after the test began (added by the autoscaler
// for it), and every pod on it, in every namespace, must be the harness's own, a
// DaemonSet's, or one AF_K8S_LIVE_NODE_ALLOW_PODS names explicitly (<namespace>/<name prefix>,
// comma-separated: a replicated system component such as GKE's konnectivity-agent, which
// the scheduler places on any new node). Pods of the workspace namespace other than the
// harness's own are refused whatever the list says.
//
// The stop must cut the node off without a shutdown the guest sees (the check after
// NotReady below). The commands may use {kubectl}, the harness's pinned kubectl; on GKE a
// power-off through a node debug pod does it:
//
//	{kubectl} debug node/{node} -n default --profile=sysadmin --image=<image> -- sh -c 'echo o > /proc/sysrq-trigger'
//
// GKE replaces a VM that terminated by itself within seconds, though, so on GKE the node is
// cut off with its VM running — the kubelet stopped through the same debug pod, with a
// host timer that starts it again should the test die — and AF_K8S_LIVE_NODE_HALT_CMD is
// the runbook operator's VM stop once Stop has failed:
//
//	{kubectl} debug node/{node} ... -- chroot /host sh -c 'systemd-run --on-active=900 systemctl start kubelet && systemctl stop kubelet'
//
// The commands take {node}, {zone} and {kubectl}. AF_K8S_LIVE_NODE_STATUS_CMD prints the
// VM's state from the provider (TERMINATED is the proof the runbook asks for) and, where
// the Node names its VM (GKE's container.googleapis.com/instance_id), that id, so that a
// VM the provider recreated under the same name is noticed. AF_K8S_LIVE_NODE_START_CMD
// starts the VM again, and the optional AF_K8S_LIVE_NODE_RECOVER_CMD recovers a node cut
// off with its VM running. The run counts only while the pod, the Node and the stopped
// VM stay the ones it cut off until the pod is gone; anything the provider replaced
// meanwhile makes it INCONCLUSIVE, since a replaced node removes the pod as well.
//
// AF_K8S_LIVE_NODE_MODE=auto-repair is the GKE path: after Stop has failed and Start has
// refused, nothing touches the VM, and the node's auto-repair has to free the pod within
// kubeLiveRepairWindow while the kubelet is still down. AF_K8S_LIVE_NODE_REPAIR_EVIDENCE_CMD
// (with {node}, {zone} and {since}, the time of the cut) must print the provider's
// operations as JSON — `gcloud container operations list --format=json` — and exactly one of
// them must be an AUTO_REPAIR_NODES of this node in AF_K8S_LIVE_GKE_PROJECT (the ID and/or
// the number; the node's providerID must name one), AF_K8S_LIVE_GKE_LOCATION and
// AF_K8S_LIVE_GKE_CLUSTER, started after the cut and before the pod was seen gone. It must
// then finish without an error, and the Node object must have been replaced. The cut-off kubelet's own timer has to
// outlast the window, and AF_K8S_LIVE_NODE_RECOVER_WAIT (how long the cleanup waits for
// the node) has to cover that timer.
//
// The cleanup recovers the node first (starts a stopped VM, runs the recover command, or
// waits past the stop command's timer for the node to be Ready), then deletes the debug
// pods the stop command reported creating, by name and UID, and removes the taint from
// the Node object it put it on.
func TestKubernetesLiveNodeUnreachable(t *testing.T) {
	l := needKubeLiveDisruptive(t)
	stopCmd, statusCmd, startCmd := os.Getenv("AF_K8S_LIVE_NODE_STOP_CMD"), os.Getenv("AF_K8S_LIVE_NODE_STATUS_CMD"), os.Getenv("AF_K8S_LIVE_NODE_START_CMD")
	haltCmd, recoverCmd := os.Getenv("AF_K8S_LIVE_NODE_HALT_CMD"), os.Getenv("AF_K8S_LIVE_NODE_RECOVER_CMD")
	// "auto-repair" is the GKE path: the provider's node repair, not an operator, frees the
	// pod. Anything else is the runbook's path for clusters where a stopped node stays down.
	autoRepair := os.Getenv("AF_K8S_LIVE_NODE_MODE") == "auto-repair"
	repairEvidenceCmd := os.Getenv("AF_K8S_LIVE_NODE_REPAIR_EVIDENCE_CMD")
	repairAt := kubeLiveRepairTarget{
		projects: splitCSV(os.Getenv("AF_K8S_LIVE_GKE_PROJECT")),
		location: os.Getenv("AF_K8S_LIVE_GKE_LOCATION"),
		cluster:  os.Getenv("AF_K8S_LIVE_GKE_CLUSTER"),
	}
	recoverWait := 17 * time.Minute
	if v := os.Getenv("AF_K8S_LIVE_NODE_RECOVER_WAIT"); v != "" {
		d, err := time.ParseDuration(v)
		// Bounded so that the cleanup still finishes inside an hour's token.
		if err != nil || d <= 0 || d > 45*time.Minute {
			t.Fatalf("AF_K8S_LIVE_NODE_RECOVER_WAIT=%q: want a duration in (0, 45m] (%v)", v, err)
		}
		recoverWait = d
	}
	if autoRepair {
		if repairEvidenceCmd == "" || len(repairAt.projects) == 0 || repairAt.location == "" || repairAt.cluster == "" {
			t.Fatal("AF_K8S_LIVE_NODE_MODE=auto-repair needs AF_K8S_LIVE_NODE_REPAIR_EVIDENCE_CMD, AF_K8S_LIVE_GKE_PROJECT, AF_K8S_LIVE_GKE_LOCATION and AF_K8S_LIVE_GKE_CLUSTER: without the provider's record of this cluster's repair nothing ties the pod's end to it")
		}
		if recoverWait <= kubeLiveRepairWindow {
			t.Fatalf("AF_K8S_LIVE_NODE_RECOVER_WAIT=%s must exceed the %s repair window (and the stop command's kubelet timer must lie between the two)", recoverWait, kubeLiveRepairWindow)
		}
	}
	if stopCmd == "" || statusCmd == "" || startCmd == "" {
		t.Fatal("set AF_K8S_LIVE_NODE_STOP_CMD, AF_K8S_LIVE_NODE_STATUS_CMD and AF_K8S_LIVE_NODE_START_CMD")
	}
	cordoned := splitCSV(os.Getenv("AF_K8S_LIVE_CORDON_NODES"))
	if len(cordoned) == 0 {
		t.Fatal("set AF_K8S_LIVE_CORDON_NODES to every node that runs anything but the harness: this test stops a node's VM")
	}
	ctx := context.Background()
	began := time.Now().Add(-time.Minute) // clock skew between here and the API server
	var node string
	var ours []kubeLiveOwnedPod
	// Registered before anything acts on a node, so it runs after every other cleanup but
	// the guarded pod's: none of the debug pods this test created may be left behind.
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Minute)
		for {
			left, err := l.ownedLeft(ours)
			if err == nil && len(left) == 0 {
				if node != "" {
					t.Logf("none of the %d debug pod(s) this test created on %s is left", len(ours), node)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("debug pods this test created are still present (%v, %v): remove them by hand", left, err)
				return
			}
			time.Sleep(5 * time.Second)
		}
	})
	l.cordon(cordoned...)
	rt := l.runtime(l.factory(0), l.workspace("unreach"))
	l.startRunning(rt)
	node = l.runningPod(rt).Spec.NodeName
	if slices.Contains(cordoned, node) {
		t.Fatalf("the workspace landed on the cordoned node %s", node)
	}
	nodeObj := l.requireNodeFits(node, began)
	if autoRepair {
		// The node's own record of its project (gce://<project>/<zone>/<instance>) has to be
		// one the repair record is matched against: otherwise the record could be another
		// project's.
		parts := strings.Split(strings.TrimPrefix(nodeObj.Spec.ProviderID, "gce://"), "/")
		if !strings.HasPrefix(nodeObj.Spec.ProviderID, "gce://") || len(parts) != 3 || !slices.Contains(repairAt.projects, parts[0]) {
			t.Fatalf("node %s has providerID %q, not a GCE instance of AF_K8S_LIVE_GKE_PROJECT %v", node, nodeObj.Spec.ProviderID, repairAt.projects)
		}
		repairAt.node = node
	}
	// The node is cordoned before its pods are read and stays cordoned until the end, so
	// nothing the scheduler places can land on it between the check and the stop.
	l.cordon(node)
	allow := splitCSV(os.Getenv("AF_K8S_LIVE_NODE_ALLOW_PODS"))
	l.requireNodePods(node, rt, allow)
	cmds := l.nodeCommands(node, stopCmd, statusCmd, startCmd, haltCmd, recoverCmd, repairEvidenceCmd)
	stopCmd, statusCmd, startCmd, haltCmd, recoverCmd, repairEvidenceCmd = cmds[0], cmds[1], cmds[2], cmds[3], cmds[4], cmds[5]
	// The VM's own id, where the node names it (GKE does), so that a VM the provider
	// recreated under the same name is told from the one this test stopped.
	vmID := nodeObj.Metadata.Annotations["container.googleapis.com/instance_id"]
	sameVM := func(status string) bool { return vmID == "" || strings.Contains(status, vmID) }
	// sameNode is for the cleanup, where an unreadable node only means "leave it alone".
	// Everything a verdict rests on uses replaced, which never takes an error for a
	// replacement.
	sameNode := func() bool { u := l.nodeUIDQuiet(node); return u != "" && u == nodeObj.Metadata.UID }
	replaced := func() bool { return l.nodeReplaced(node, nodeObj.Metadata.UID) }

	// Recovery runs before the workspace's own cleanup, which needs a kubelet to remove
	// the pod: the VM is started again, or a cut-off node with its VM running is
	// recovered and waited for, before anything is deleted.
	t.Cleanup(func() {
		st, err := l.operatorRun(statusCmd)
		switch {
		case err != nil:
			t.Logf("the provider's state of %s is unreadable (replaced by repair, or removed?): %v", node, err)
		case strings.Contains(st, "TERMINATED") && sameVM(st):
			if _, err := l.operatorRun(startCmd); err != nil {
				t.Errorf("START THE VM OF %s BY HAND: %v", node, err)
			}
		case !sameVM(st):
			t.Logf("the provider recreated the VM of %s (%s; was id %s)", node, strings.TrimSpace(st), vmID)
		case sameNode() && !l.nodeReadyQuiet(node):
			if recoverCmd != "" {
				if _, err := l.operatorRun(recoverCmd); err != nil {
					t.Errorf("recover %s: %v", node, err)
				}
			} else {
				t.Logf("%s is cut off with its VM running and no AF_K8S_LIVE_NODE_RECOVER_CMD: waiting for the stop command's own timer", node)
			}
		}
		// Whatever happened, wait for a Ready node of that name before the pods on it are
		// deleted: past the stop command's 15-minute timer, with a margin.
		deadline := time.Now().Add(recoverWait)
		for !l.nodeReadyQuiet(node) {
			if present, err := l.exists("get", "node", node); err == nil && !present {
				t.Logf("node %s is gone; its pods go with it", node)
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("node %s is not Ready %s into the cleanup: RECOVER IT BY HAND (kubelet, or reset the VM)", node, recoverWait)
				break
			}
			time.Sleep(10 * time.Second)
		}
		for i := range ours {
			if err := l.deleteOwned(&ours[i]); err != nil {
				t.Errorf("delete the debug pod %s on %s: %v", ours[i].name, node, err)
			}
		}
		if sameNode() {
			if _, err := l.run("taint", "nodes", node, "node.kubernetes.io/out-of-service-"); err != nil && !strings.Contains(err.Error(), "not found") {
				t.Errorf("REMOVE THE TAINT FROM %s BY HAND: %v", node, err)
			}
		}
	})

	// Again right before the stop: the node may have been replaced, uncordoned or given a
	// pod since the checks above.
	if replaced() {
		t.Fatalf("INCONCLUSIVE: node %s was replaced before the stop", node)
	}
	if strings.TrimSpace(l.must("get", "node", node, "-o", "jsonpath={.spec.unschedulable}")) != "true" {
		t.Fatalf("node %s is no longer cordoned; refusing to stop it", node)
	}
	l.requireNodePods(node, rt, allow)
	t.Logf("making %s unreachable", node)
	t0 := time.Now()
	// What the command reports creating is recorded before its own result is looked at: a
	// command that created the debug pod and then failed (an attach error, its deadline)
	// still left that pod behind.
	out, stopErr := l.operatorRun(stopCmd)
	l.recordDebugPods(&ours, out, node, t0)
	if stopErr != nil {
		t.Fatal(stopErr)
	}
	l.eventually(10*time.Minute, node+" not Ready", func() bool { return l.nodeNotReady(node) })
	t.Logf("%s not Ready after %s", node, time.Since(t0).Round(time.Second))
	// A stop the guest sees (an ACPI shutdown, as `gcloud compute instances stop` sends)
	// lets the node terminate the pod and report it before going away: the pod is then
	// terminal, deleting it is immediate, and Stop rightly settles. That is not a node that
	// stopped answering (measured on GKE 1.35: a VM stop took 107 s, and the agent stopped
	// answering within the first minute of it).
	var podUID string
	for _, p := range l.pods(rt.base) {
		if p.Status.Phase != "Succeeded" && p.Status.Phase != "Failed" {
			podUID = p.Metadata.UID
		}
		t.Logf("the workspace pod after the node went: phase %s", p.Status.Phase)
	}
	if podUID == "" {
		t.Fatalf("INCONCLUSIVE: the workspace pod is gone or terminal once %s is not Ready — the stop command shut the node down gracefully instead of cutting it off", node)
	}

	err := rt.Stop(ctx)
	if err == nil && replaced() {
		// GKE's instance group recreates a workspace VM that terminated within seconds
		// (compute.instances.repair.recreateInstance, measured on GKE 1.35), and the node
		// controller then deletes the old Node object and its pods: the stop rightly
		// settles, the deleted VM being the proof, and the node never stays unreachable.
		t.Fatalf("INCONCLUSIVE: the provider replaced %s by itself, which removed the pod, so Stop settled %s after the stop command instead of meeting an unreachable node", node, time.Since(t0).Round(time.Second))
	}
	if err == nil || !strings.Contains(err.Error(), "not settled") {
		t.Fatalf("Stop with the node unreachable = %v, want a not-settled error", err)
	}
	t.Logf("Stop reported not settled %s after the stop command: %v", time.Since(t0).Round(time.Second), err)
	if got := rt.State(ctx); got != "stopped" {
		t.Errorf("State after the failed Stop = %q, want stopped", got)
	}
	if err := rt.Start(ctx); err == nil || !strings.Contains(err.Error(), "has not settled") {
		t.Fatalf("Start over the unreachable pod = %v, want a refusal", err)
	}
	t.Logf("Start refused %s after the stop command", time.Since(t0).Round(time.Second))

	if autoRepair {
		// GKE: a stopped VM in a managed instance group is recreated by the group's own
		// repair within seconds, so "stop the VM, then taint" cannot happen there; the node's
		// auto-repair drains and recreates a node NotReady for about ten minutes. Nothing
		// here touches the VM. The run counts only if the pod goes while the kubelet is still
		// down — a kubelet that came back would free it too — and the provider recorded a
		// repair.
		gen := ""
		for _, p := range l.pods(rt.base) {
			if p.Metadata.UID == podUID {
				gen = p.Metadata.Annotations[kubeAnnStartGen]
			}
		}
		var replacedAt time.Time
		l.eventually(kubeLiveRepairWindow, "the node's auto-repair to free the pod", func() bool {
			if !replaced() && l.nodeReadyQuiet(node) {
				t.Fatalf("INCONCLUSIVE: %s answered again (its kubelet came back) before the pod went, %s after the cut", node, time.Since(t0).Round(time.Second))
			}
			if replacedAt.IsZero() && replaced() {
				replacedAt = time.Now()
				t.Logf("%s was removed or recreated by the provider %s after the cut", node, time.Since(t0).Round(time.Second))
			}
			return !slices.ContainsFunc(l.pods(rt.base), func(p kPod) bool { return p.Metadata.UID == podUID })
		})
		goneAt := time.Now()
		t.Logf("the cut-off pod went %s after the cut, with the kubelet still down and no operator action", time.Since(t0).Round(time.Second))
		// The repair must be this node's, and it must have finished without an error: a repair
		// that failed or was cut short did not recreate the node, and something else freed
		// the pod. The pod can go before the operation is marked done, so it is read again.
		evidence := strings.ReplaceAll(repairEvidenceCmd, "{since}", t0.UTC().Format(time.RFC3339))
		var op kubeLiveRepairOp
		deadline := time.Now().Add(10 * time.Minute)
		for {
			var err error
			if op, err = matchRepair(l.operator(evidence), repairAt, t0, goneAt); err != nil {
				t.Fatalf("INCONCLUSIVE: %v", err)
			}
			err = repairSucceeded(op)
			if err == nil {
				break
			}
			if !errors.Is(err, errRepairRunning) || time.Now().After(deadline) {
				t.Fatalf("INCONCLUSIVE: the repair %s of %s: %v", op.Name, node, err)
			}
			time.Sleep(10 * time.Second)
		}
		if !replaced() {
			t.Fatalf("INCONCLUSIVE: the repair %s is done, yet %s is still the Node object that was cut off", op.Name, node)
		}
		t.Logf("the provider's repair: %s %s of %s, started %s after the cut, %s; the node was replaced", op.OperationType, op.Name, op.TargetLink, op.started.Sub(t0).Round(time.Second), op.Status)
		if err := rt.Stop(ctx); err != nil {
			t.Fatalf("Stop once the pod is gone: %v", err)
		}
		t.Logf("the stop settled %s after the cut", time.Since(t0).Round(time.Second))
		// Stop has set replicas 0, so the workspace returns through a new Start, as a member's
		// next use would start it; the same start generation returning is the unplanned-drain
		// case (Lifecycle/PodDeletedBehindTheCP), where nothing stopped the workspace.
		l.startRunning(rt)
		p := l.runningPod(rt)
		if p.Spec.NodeName == node && !replaced() {
			t.Fatalf("running again on the cut-off node %s", node)
		}
		g, _ := strconv.Atoi(gen)
		if got := p.Metadata.Annotations[kubeAnnStartGen]; got != strconv.Itoa(g+1) {
			t.Errorf("the start after recovery carries generation %q, want %d", got, g+1)
		}
		t.Logf("node unreachable (auto-repair): running again on %s (UID %s), %s after the cut", p.Spec.NodeName, l.nodeUIDQuiet(p.Spec.NodeName), time.Since(t0).Round(time.Second))
		return
	}

	// The runbook: the old process must be proven unable to run — this VM stopped, from the
	// provider — before anything frees the pod. A node cut off with its VM running (a
	// stopped kubelet) is stopped by the operator first, AF_K8S_LIVE_NODE_HALT_CMD.
	t2 := time.Now()
	if st := l.operator(statusCmd); !strings.Contains(st, "TERMINATED") {
		if haltCmd == "" {
			t.Fatalf("the provider says %q, not TERMINATED, and no AF_K8S_LIVE_NODE_HALT_CMD is set: the runbook does nothing without that proof", strings.TrimSpace(st))
		}
		l.operator(haltCmd)
		l.eventually(5*time.Minute, "the provider to report "+node+" TERMINATED", func() bool {
			st, err := l.operatorRun(statusCmd)
			if err != nil || !sameVM(st) || replaced() {
				t.Fatalf("INCONCLUSIVE: the provider replaced or removed %s before the runbook's taint (%s after the halt; %q, %v)", node, time.Since(t2).Round(time.Second), strings.TrimSpace(st), err)
			}
			return strings.Contains(st, "TERMINATED")
		})
		t.Logf("%s TERMINATED %s after the operator's halt", node, time.Since(t2).Round(time.Second))
	}
	// The taint frees the pod only if the pod, the Node and the stopped VM are still the
	// ones this test cut off — up to the moment the pod is seen gone. A node or VM the
	// provider replaced in between removes the pod too, and then nothing shows which did.
	stillOurs := func(what string) {
		st, err := l.operatorRun(statusCmd)
		if err != nil || !strings.Contains(st, "TERMINATED") || !sameVM(st) || replaced() {
			t.Fatalf("INCONCLUSIVE: %s: the provider replaced or removed %s (%q, %v), so the runbook's taint is not what freed the pod", what, node, strings.TrimSpace(st), err)
		}
	}
	stillOurs("before the taint")
	if !slices.ContainsFunc(l.pods(rt.base), func(p kPod) bool { return p.Metadata.UID == podUID }) {
		t.Fatalf("INCONCLUSIVE: the cut-off pod %s went before the runbook's taint", podUID)
	}
	l.must("taint", "nodes", node, "node.kubernetes.io/out-of-service=nodeshutdown:NoExecute", "--overwrite")
	t1 := time.Now()
	l.eventually(10*time.Minute, "the pod on the stopped node to go", func() bool {
		gone := len(l.pods(rt.base)) == 0
		stillOurs("while the taint freed the pod")
		return gone
	})
	t.Logf("pod gone %s after the taint, with the Node and the stopped VM unchanged", time.Since(t1).Round(time.Second))
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("Stop once the pod is gone: %v", err)
	}
	t.Logf("the stop settled %s after the operator's halt began", time.Since(t2).Round(time.Second))
	l.startRunning(rt)
	p := l.runningPod(rt)
	if p.Spec.NodeName == node && !replaced() {
		t.Fatalf("running again on the stopped node %s", node)
	}
	t.Logf("node unreachable: running again on %s %s after the operator's halt began, %s after the node was cut off",
		p.Spec.NodeName, time.Since(t2).Round(time.Second), time.Since(t0).Round(time.Second))
}

// kubeLiveRepairWindow bounds the wait for GKE's node auto-repair to free a cut-off pod:
// it starts after about ten minutes NotReady (measured: 11m15s) and took 2m44s.
const kubeLiveRepairWindow = 20 * time.Minute

type kubeLiveRepairOp struct {
	Name          string `json:"name"`
	OperationType string `json:"operationType"`
	StartTime     string `json:"startTime"`
	Status        string `json:"status"`
	TargetLink    string `json:"targetLink"`
	StatusMessage string `json:"statusMessage"`
	Error         *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	started time.Time
}

// kubeLiveRepairTarget is the node a repair must name: its project (the ID or the number,
// which is what GKE's operation links carry), the cluster's location and name, the node.
type kubeLiveRepairTarget struct {
	projects                []string
	location, cluster, node string
}

// kubeLiveRepairLink is GKE's link to a node of a node pool:
// …/projects/<project>/locations/<location>/clusters/<cluster>/nodePools/<pool>/node/<node>.
var kubeLiveRepairLink = regexp.MustCompile(`/projects/([^/]+)/locations/([^/]+)/clusters/([^/]+)/nodePools/[^/]+/node/([^/]+)$`)

var errRepairRunning = errors.New("the repair has not finished")

// repairSucceeded accepts a repair that finished without an error, and reports one still
// running as errRepairRunning.
func repairSucceeded(op kubeLiveRepairOp) error {
	if op.Error != nil {
		return fmt.Errorf("it failed: %d %s", op.Error.Code, op.Error.Message)
	}
	switch op.Status {
	case "DONE":
		return nil
	case "PENDING", "RUNNING":
		return errRepairRunning
	default:
		return fmt.Errorf("it ended %s: %s", op.Status, op.StatusMessage)
	}
}

// matchRepair finds, in the JSON list of GKE operations out, the one auto-repair of the
// target node in its project, location and cluster that started after the cut and before
// the pod was seen gone. None, or more than one, is an error: then nothing ties the pod's
// end to a repair.
func matchRepair(out string, want kubeLiveRepairTarget, cut, gone time.Time) (kubeLiveRepairOp, error) {
	var ops []kubeLiveRepairOp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &ops); err != nil {
		return kubeLiveRepairOp{}, fmt.Errorf("the repair record is not a JSON list of operations: %v", err)
	}
	var hits []kubeLiveRepairOp
	for _, op := range ops {
		started, err := time.Parse(time.RFC3339Nano, op.StartTime)
		m := kubeLiveRepairLink.FindStringSubmatch(op.TargetLink)
		if err != nil || op.OperationType != "AUTO_REPAIR_NODES" || m == nil ||
			!slices.Contains(want.projects, m[1]) || m[2] != want.location || m[3] != want.cluster || m[4] != want.node ||
			started.Before(cut) || started.After(gone) {
			continue
		}
		op.started = started
		hits = append(hits, op)
	}
	if len(hits) != 1 {
		return kubeLiveRepairOp{}, fmt.Errorf("%d auto-repair operations of %s in cluster %s/%s (project %v) started between the cut and the pod's end (out of %d listed)", len(hits), want.node, want.location, want.cluster, want.projects, len(ops))
	}
	return hits[0], nil
}

// requireNodeFits refuses a node that is not a workspace node or existed before the test
// began, and returns it as read.
func (l *kubeLive) requireNodeFits(node string, began time.Time) kubeLiveNode {
	l.t.Helper()
	var n kubeLiveNode
	l.getJSON(&n, "get", "node", node)
	sel := l.factory(0).cfg.nodeSelector
	if len(sel) == 0 {
		l.t.Fatal("AF_K8S_NODE_SELECTOR is empty: the harness cannot tell a workspace node from any other")
	}
	for k, v := range sel {
		if n.Metadata.Labels[k] != v {
			l.t.Fatalf("node %s is not a workspace node (%s=%q, want %q): refusing to stop it", node, k, n.Metadata.Labels[k], v)
		}
	}
	created, err := time.Parse(time.RFC3339, n.Metadata.CreationTimestamp)
	if err != nil || created.Before(began) {
		l.t.Fatalf("node %s was created at %q, before this test began: refusing to stop a node the harness did not get added", node, n.Metadata.CreationTimestamp)
	}
	return n
}

type kubeLiveNode struct {
	Metadata kObjectMeta `json:"metadata"`
	Spec     struct {
		ProviderID string `json:"providerID"`
	} `json:"spec"`
}

// requireNodePods refuses a node that runs anything but the harness's own workspace pod,
// DaemonSet pods and the pods AF_K8S_LIVE_NODE_ALLOW_PODS names.
func (l *kubeLive) requireNodePods(node string, rt *kubeRuntime, allow []string) {
	l.t.Helper()
	var others struct {
		Items []kubeLiveNodePod `json:"items"`
	}
	l.getJSON(&others, "get", "pods", "-A", "--field-selector", "spec.nodeName="+node)
	for _, p := range others.Items {
		m := p.Metadata
		own := m.Namespace == l.ns && m.Labels[kubeLabelWorkspace] == rt.base
		agent := false
		for _, o := range m.OwnerReferences {
			if o.Kind == "DaemonSet" {
				agent = true
			}
		}
		allowed := false
		for _, a := range allow {
			ns, prefix, ok := strings.Cut(a, "/")
			if ok && prefix != "" && m.Namespace == ns && m.Namespace != l.ns && strings.HasPrefix(m.Name, prefix) {
				allowed = true
				l.t.Logf("node %s also runs %s/%s, allowed by AF_K8S_LIVE_NODE_ALLOW_PODS", node, m.Namespace, m.Name)
			}
		}
		if !own && !agent && !allowed {
			l.t.Fatalf("node %s also runs %s/%s; refusing to stop it", node, m.Namespace, m.Name)
		}
	}
}

// kubeLiveOwnedPod is a pod this test created, by namespace, name and UID: a name prefix
// proves nothing about who made a pod. An entry whose lookup failed keeps the name, the
// node and when the command ran, and is resolved again by the cleanup.
type kubeLiveOwnedPod struct {
	ns, name, uid, node string
	since               time.Time
}

// recordDebugPods appends to ours, one at a time, each pod the stop command reports
// creating (kubectl debug prints "Creating debugging pod <name> …"), with its namespace and
// UID. A failed lookup still appends the name, so the cleanup tries again and says so if it
// cannot settle it; nothing recorded so far is lost to a later failure.
func (l *kubeLive) recordDebugPods(ours *[]kubeLiveOwnedPod, out, node string, since time.Time) {
	for _, m := range regexp.MustCompile(`Creating debugging pod (\S+)`).FindAllStringSubmatch(out, -1) {
		p := kubeLiveOwnedPod{name: m[1], node: node, since: since}
		if err := l.resolveOwned(&p); err != nil {
			l.t.Errorf("the stop command created pod %s on %s; its owner could not be confirmed yet (%v), the cleanup tries again", m[1], node, err)
		} else {
			l.t.Logf("the stop command created %s/%s (UID %s)", p.ns, p.name, p.uid)
		}
		*ours = append(*ours, p)
	}
}

// resolveOwned fills in the namespace and UID of a recorded pod: exactly one pod of that
// name on that node, created no earlier than the command that reported it. Anything else
// is ambiguous, and an ambiguous pod is never deleted.
func (l *kubeLive) resolveOwned(p *kubeLiveOwnedPod) error {
	if p.uid != "" {
		return nil
	}
	out, err := l.run("get", "pods", "-A", "--field-selector", "metadata.name="+p.name+",spec.nodeName="+p.node, "-o", "json")
	if err != nil {
		return err
	}
	var list struct {
		Items []struct {
			Metadata kObjectMeta `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return err
	}
	if len(list.Items) != 1 {
		return fmt.Errorf("%d pods named %s on %s", len(list.Items), p.name, p.node)
	}
	m := list.Items[0].Metadata
	created, err := time.Parse(time.RFC3339, m.CreationTimestamp)
	// Timestamps have one-second resolution, and the clocks may differ by a little.
	if err != nil || created.Before(p.since.Add(-time.Minute)) {
		return fmt.Errorf("pod %s/%s was created at %q, before the command that reported it", m.Namespace, m.Name, m.CreationTimestamp)
	}
	p.ns, p.uid = m.Namespace, m.UID
	return nil
}

// ownedLeft returns the recorded pods still present with their recorded UID, and every
// recorded pod whose owner could still not be confirmed.
func (l *kubeLive) ownedLeft(pods []kubeLiveOwnedPod) ([]string, error) {
	var left []string
	for i := range pods {
		p := &pods[i]
		if err := l.resolveOwned(p); err != nil {
			// Gone by now is settled too; an unreadable or ambiguous one is not.
			if out, gerr := l.run("get", "pods", "-A", "--field-selector", "metadata.name="+p.name+",spec.nodeName="+p.node, "-o", "name"); gerr == nil && strings.TrimSpace(out) == "" {
				continue
			}
			left = append(left, fmt.Sprintf("%s on %s (unconfirmed: %v)", p.name, p.node, err))
			continue
		}
		uid, err := l.run("-n", p.ns, "get", "pod", p.name, "--ignore-not-found", "-o", "jsonpath={.metadata.uid}")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(uid) == p.uid {
			left = append(left, p.ns+"/"+p.name)
		}
	}
	return left, nil
}

// deleteOwned deletes the pod only once its owner is confirmed and while it still has the
// recorded UID. The name carries generateName's random suffix, so the check right before
// the delete leaves no practical window for another pod to take it.
func (l *kubeLive) deleteOwned(p *kubeLiveOwnedPod) error {
	if err := l.resolveOwned(p); err != nil {
		return fmt.Errorf("not deleted, its owner is unconfirmed: %w", err)
	}
	uid, err := l.run("-n", p.ns, "get", "pod", p.name, "--ignore-not-found", "-o", "jsonpath={.metadata.uid}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(uid) != p.uid {
		return nil
	}
	_, err = l.run("-n", p.ns, "delete", "pod", p.name, "--wait=false")
	return err
}

func (l *kubeLive) nodeReadyQuiet(node string) bool {
	out, err := l.run("get", "node", node, "--ignore-not-found", "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
	return err == nil && strings.TrimSpace(out) == "True"
}
