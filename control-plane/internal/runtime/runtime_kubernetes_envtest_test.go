// The kubernetes adapter against a real kube-apiserver, etcd and kube-controller-manager
// (deploy/local/k8s-test-binaries.sh installs them). There is no kubelet and no
// scheduler: the real StatefulSet controller creates and deletes the pods, and these
// tests play the node — they bind a pod, write the status a kubelet would, and finish a
// graceful deletion. The tests skip when the binaries are absent, unless
// AF_K8S_TEST_REQUIRED=1 (ci.yml's control-plane job), where that is a failure.
//
// The adapter runs as its own identity with the Role and ClusterRole of ADR 0106
// decision 8, not as an administrator, in a namespace that enforces the `restricted`
// Pod Security Standard: a pod spec the level refuses never appears, and a missing
// permission fails the test.
package runtime

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const kubeTestVersion = "v1.34.12" // deploy/local/k8s-test-binaries.sh

// kubeTestEnv is one control plane shared by every test of the package run.
type kubeTestEnv struct {
	dir      string
	base     string
	ca       []byte
	admin    *kubeClient
	cpToken  string
	procs    []*exec.Cmd
	kcm      *exec.Cmd
	nsSerial int
	mu       sync.Mutex
}

var (
	kubeEnvOnce sync.Once
	kubeEnv     *kubeTestEnv
	kubeEnvErr  error
	kubeEnvSkip string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if kubeEnv != nil {
		kubeEnv.stop()
	}
	os.Exit(code)
}

func kubeTestBinDir() string {
	if d := os.Getenv("AF_K8S_TEST_BIN_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "af-k8s-test", kubeTestVersion)
}

// needKubeEnv starts the control plane on first use and returns a fresh namespace in it.
func needKubeEnv(t *testing.T) (*kubeTestEnv, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("kubernetes control plane tests skipped in -short")
	}
	kubeEnvOnce.Do(func() {
		bin := kubeTestBinDir()
		for _, b := range []string{"etcd", "kube-apiserver", "kube-controller-manager"} {
			if _, err := os.Stat(filepath.Join(bin, b)); err != nil {
				kubeEnvSkip = fmt.Sprintf("%s not found in %s (run deploy/local/k8s-test-binaries.sh)", b, bin)
				return
			}
		}
		kubeEnv, kubeEnvErr = startKubeTestEnv(bin)
	})
	if kubeEnvSkip != "" {
		// CI sets AF_K8S_TEST_REQUIRED after installing the binaries, so a missing one is
		// a failure there instead of a quietly green skip.
		if os.Getenv("AF_K8S_TEST_REQUIRED") == "1" {
			t.Fatal(kubeEnvSkip)
		}
		t.Skip(kubeEnvSkip)
	}
	if kubeEnvErr != nil {
		t.Fatalf("start kubernetes control plane: %v", kubeEnvErr)
	}
	return kubeEnv, kubeEnv.namespace(t)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func writePEM(path, typ string, der []byte) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600)
}

func startKubeTestEnv(bin string) (*kubeTestEnv, error) {
	dir, err := os.MkdirTemp("", "af-k8s-env-")
	if err != nil {
		return nil, err
	}
	e := &kubeTestEnv{dir: dir}
	fail := func(err error) (*kubeTestEnv, error) {
		e.stop()
		return nil, err
	}
	// A CA, the API server's serving certificate, and the service account signing key.
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "af-test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return fail(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srvDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "kube-apiserver"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		DNSNames:    []string{"localhost", "kubernetes.default.svc"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		return fail(err)
	}
	srvKeyDER, _ := x509.MarshalECPrivateKey(srvKey)
	saKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	saPub, _ := x509.MarshalPKIXPublicKey(&saKey.PublicKey)
	for _, f := range []struct {
		name, typ string
		der       []byte
	}{
		{"ca.crt", "CERTIFICATE", caDER}, {"server.crt", "CERTIFICATE", srvDER},
		{"server.key", "EC PRIVATE KEY", srvKeyDER},
		{"sa.key", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(saKey)},
		{"sa.pub", "PUBLIC KEY", saPub},
	} {
		if err := writePEM(filepath.Join(dir, f.name), f.typ, f.der); err != nil {
			return fail(err)
		}
	}
	e.ca, _ = os.ReadFile(filepath.Join(dir, "ca.crt"))
	adminToken, cpToken := randHexT(16), randHexT(16)
	e.cpToken = cpToken
	tokens := fmt.Sprintf("%s,admin,admin-uid,\"system:masters\"\n%s,af-cp,af-cp-uid\n", adminToken, cpToken)
	if err := os.WriteFile(filepath.Join(dir, "tokens.csv"), []byte(tokens), 0o600); err != nil {
		return fail(err)
	}
	ports := make([]int, 3)
	for i := range ports {
		if ports[i], err = freePort(); err != nil {
			return fail(err)
		}
	}
	etcdURL := fmt.Sprintf("http://127.0.0.1:%d", ports[0])
	peerURL := fmt.Sprintf("http://127.0.0.1:%d", ports[1])
	logf, err := os.Create(filepath.Join(dir, "control-plane.log"))
	if err != nil {
		return fail(err)
	}
	run := func(name string, args ...string) (*exec.Cmd, error) {
		cmd := exec.Command(filepath.Join(bin, name), args...)
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		e.procs = append(e.procs, cmd)
		return cmd, nil
	}
	if _, err := run("etcd", "--name=default", "--data-dir="+filepath.Join(dir, "etcd"),
		"--listen-client-urls="+etcdURL, "--advertise-client-urls="+etcdURL,
		"--listen-peer-urls="+peerURL, "--initial-advertise-peer-urls="+peerURL,
		"--initial-cluster=default="+peerURL, "--unsafe-no-fsync", "--log-level=error"); err != nil {
		return fail(err)
	}
	if _, err := run("kube-apiserver",
		"--etcd-servers="+etcdURL,
		"--bind-address=127.0.0.1", "--advertise-address=127.0.0.1",
		fmt.Sprintf("--secure-port=%d", ports[2]),
		"--tls-cert-file="+filepath.Join(dir, "server.crt"),
		"--tls-private-key-file="+filepath.Join(dir, "server.key"),
		"--token-auth-file="+filepath.Join(dir, "tokens.csv"),
		"--authorization-mode=RBAC",
		"--service-account-issuer=https://kubernetes.default.svc",
		"--service-account-key-file="+filepath.Join(dir, "sa.pub"),
		"--service-account-signing-key-file="+filepath.Join(dir, "sa.key"),
		"--service-cluster-ip-range=10.0.0.0/24",
		"--enable-priority-and-fairness=false",
		"--allow-privileged=false",
		"--v=0"); err != nil {
		return fail(err)
	}
	e.base = fmt.Sprintf("https://127.0.0.1:%d", ports[2])
	adminFile := filepath.Join(dir, "admin.token")
	if err := os.WriteFile(adminFile, []byte(adminToken), 0o600); err != nil {
		return fail(err)
	}
	if e.admin, err = newKubeClient(e.base, e.ca, adminFile); err != nil {
		return fail(err)
	}
	ready := false
	for i := 0; i < 240 && !ready; i++ {
		if e.admin.get(context.Background(), "/readyz", nil) == nil {
			ready = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ready {
		return fail(fmt.Errorf("kube-apiserver not ready after 60s; see %s", logf.Name()))
	}
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: t
  cluster: {server: %q, certificate-authority: %q}
users:
- name: admin
  user: {token: %q}
contexts:
- name: t
  context: {cluster: t, user: admin}
current-context: t
`, e.base, filepath.Join(dir, "ca.crt"), adminToken)
	kc := filepath.Join(dir, "admin.kubeconfig")
	if err := os.WriteFile(kc, []byte(kubeconfig), 0o600); err != nil {
		return fail(err)
	}
	if e.kcm, err = run("kube-controller-manager", "--kubeconfig="+kc,
		"--controllers=statefulset,serviceaccount,garbagecollector,pvc-protection,pv-protection,persistentvolume-binder",
		"--leader-elect=false", "--secure-port=0", "--concurrent-statefulset-syncs=2", "--v=0"); err != nil {
		return fail(err)
	}
	if err := e.installCPRole(); err != nil {
		return fail(err)
	}
	return e, nil
}

func randHexT(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (e *kubeTestEnv) stop() {
	for i := len(e.procs) - 1; i >= 0; i-- {
		p := e.procs[i]
		if p.Process == nil {
			continue
		}
		_ = p.Process.Signal(syscall.SIGCONT)
		_ = p.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = p.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = p.Process.Kill()
			<-done
		}
	}
	e.procs = nil
	if e.dir != "" {
		_ = os.RemoveAll(e.dir)
	}
}

// installCPRole is ADR 0106 decision 8's grant for the CP, bound in each test namespace
// (namespace) and once for the cluster-wide reads.
func (e *kubeTestEnv) installCPRole() error {
	ctx := context.Background()
	cr := map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": "af-cp-read"},
		"rules": []any{
			map[string]any{"apiGroups": []string{""}, "resources": []string{"persistentvolumes"}, "verbs": []string{"get"}},
			map[string]any{"apiGroups": []string{"storage.k8s.io"}, "resources": []string{"storageclasses"}, "verbs": []string{"get"}},
		},
	}
	if err := e.admin.create(ctx, "/apis/rbac.authorization.k8s.io/v1/clusterroles", cr, nil); err != nil {
		return err
	}
	crb := map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": "af-cp-read"},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "af-cp-read"},
		"subjects": []any{map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": "af-cp"}},
	}
	return e.admin.create(ctx, "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings", crb, nil)
}

// namespace creates a namespace enforcing `restricted`, with the CP's Role bound in it,
// and waits for its default service account (the ServiceAccount admission plugin refuses
// pods until the controller has made it).
func (e *kubeTestEnv) namespace(t *testing.T) string {
	t.Helper()
	e.mu.Lock()
	e.nsSerial++
	ns := fmt.Sprintf("ws-%d", e.nsSerial)
	e.mu.Unlock()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(e.admin.create(ctx, "/api/v1/namespaces", map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": ns, "labels": map[string]string{
			"pod-security.kubernetes.io/enforce":         "restricted",
			"pod-security.kubernetes.io/enforce-version": "latest",
		}},
	}, nil))
	all := []string{"get", "list", "create", "update", "patch", "delete"}
	must(e.admin.create(ctx, "/apis/rbac.authorization.k8s.io/v1/namespaces/"+ns+"/roles", map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role",
		"metadata": map[string]any{"name": "af-cp"},
		"rules": []any{
			map[string]any{"apiGroups": []string{"apps"}, "resources": []string{"statefulsets"}, "verbs": all},
			map[string]any{"apiGroups": []string{""}, "resources": []string{"services", "persistentvolumeclaims", "secrets"}, "verbs": all},
			map[string]any{"apiGroups": []string{""}, "resources": []string{"pods"}, "verbs": []string{"get", "list", "watch", "create", "delete"}},
			map[string]any{"apiGroups": []string{""}, "resources": []string{"events"}, "verbs": []string{"get", "list"}},
		},
	}, nil))
	must(e.admin.create(ctx, "/apis/rbac.authorization.k8s.io/v1/namespaces/"+ns+"/rolebindings", map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding",
		"metadata": map[string]any{"name": "af-cp"},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "af-cp"},
		"subjects": []any{map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": "af-cp"}},
	}, nil))
	eventually(t, 30*time.Second, "default service account in "+ns, func() bool {
		var sa json.RawMessage
		return e.admin.get(ctx, "/api/v1/namespaces/"+ns+"/serviceaccounts/default", &sa) == nil
	})
	return ns
}

// factory builds the adapter's factory as the CP would, authenticated as af-cp. Each
// call is a fresh client: a second call is a restarted CP.
func (e *kubeTestEnv) factory(t *testing.T, ns string) *kubeFactory {
	t.Helper()
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte(e.cpToken), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := newKubeClient(e.base, e.ca, tok)
	if err != nil {
		t.Fatal(err)
	}
	return &kubeFactory{
		cfg: &kubeConfig{
			namespace: ns, image: "registry.example/af/workspace:test",
			homeGiB: 10, stateGiB: 1, serviceAccount: "default",
			templateEnv: []string{"WS_TEMPLATE=1"},
		},
		c:          c,
		pins:       fakePinner{},
		poll:       50 * time.Millisecond,
		stopMargin: 2 * time.Second,
	}
}

// fakePinner resolves every image to a fixed digest; fingerprint, when set, is what the
// registry says the tag's content is now (Stale's "now" side).
type fakePinner struct{ fingerprint *string }

func (f fakePinner) resolve(_ context.Context, image string) (resolvedImage, error) {
	fp := "linux/amd64=sha256:" + strings.Repeat("ab", 32)
	if f.fingerprint != nil {
		fp = *f.fingerprint
	}
	return resolvedImage{pinned: image + "@sha256:" + strings.Repeat("ab", 32), fingerprint: fp}, nil
}

func eventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", within, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// --- the node the cluster does not have ---

type kubeNode struct {
	t  *testing.T
	e  *kubeTestEnv
	ns string
}

func (n kubeNode) podsPath() string { return "/api/v1/namespaces/" + n.ns + "/pods" }

// pods returns the workspace pods of base, as an administrator sees them.
func (n kubeNode) pods(base string) []kPod {
	n.t.Helper()
	var l kPodList
	q := url.Values{"labelSelector": {kubeLabelWorkspace + "=" + base}}
	if err := n.e.admin.list(context.Background(), n.podsPath(), q, &l); err != nil {
		n.t.Fatal(err)
	}
	return l.Items
}

// waitPod waits for the controller's pod <base>-0 that is not being deleted.
func (n kubeNode) waitPod(base string) kPod {
	n.t.Helper()
	var got kPod
	eventually(n.t, 30*time.Second, "pod of "+base, func() bool {
		for _, p := range n.pods(base) {
			if p.Metadata.DeletionTimestamp == nil {
				got = p
				return true
			}
		}
		return false
	})
	return got
}

// bind places the pod on a node, which makes its deletion graceful: the API server then
// keeps it, with a deletionTimestamp, until the node confirms the containers are gone.
func (n kubeNode) bind(pod string) {
	n.t.Helper()
	err := n.e.admin.create(context.Background(), n.podsPath()+"/"+pod+"/binding", map[string]any{
		"apiVersion": "v1", "kind": "Binding",
		"metadata": map[string]any{"name": pod},
		"target":   map[string]any{"apiVersion": "v1", "kind": "Node", "name": "node-a"},
	}, nil)
	if err != nil {
		n.t.Fatal(err)
	}
}

// status writes what a kubelet reports: the agent container running or waiting, and
// the Ready condition.
func (n kubeNode) status(pod string, running, ready bool, waitingReason string) {
	n.t.Helper()
	ctx := context.Background()
	var raw map[string]any
	if err := n.e.admin.get(ctx, n.podsPath()+"/"+pod, &raw); err != nil {
		n.t.Fatal(err)
	}
	b := func(v bool) string {
		if v {
			return "True"
		}
		return "False"
	}
	state := map[string]any{"waiting": map[string]any{"reason": "ContainerCreating"}}
	if waitingReason != "" {
		state = map[string]any{"waiting": map[string]any{"reason": waitingReason, "message": "test"}}
	}
	if running {
		state = map[string]any{"running": map[string]any{"startedAt": time.Now().UTC().Format(time.RFC3339)}}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	raw["status"] = map[string]any{
		"phase": map[bool]string{true: "Running", false: "Pending"}[running],
		"conditions": []any{
			map[string]any{"type": "PodScheduled", "status": "True", "lastTransitionTime": now},
			map[string]any{"type": "Ready", "status": b(ready), "lastTransitionTime": now},
		},
		"containerStatuses": []any{map[string]any{
			"name": kubeAgentName, "ready": ready, "restartCount": 0,
			"image": "registry.example/af/workspace:test", "imageID": "", "state": state,
		}},
	}
	if err := n.e.admin.replace(ctx, n.podsPath()+"/"+pod+"/status", raw, nil); err != nil {
		n.t.Fatal(err)
	}
}

// finishDeletions completes the graceful deletion of every pod of base that is being
// deleted, as the kubelet does once the containers have stopped.
func (n kubeNode) finishDeletions(base string) int {
	n.t.Helper()
	done := 0
	for _, p := range n.pods(base) {
		if p.Metadata.DeletionTimestamp == nil {
			continue
		}
		err := n.e.admin.do(context.Background(), http.MethodDelete, n.podsPath()+"/"+p.Metadata.Name, nil, kubeJSON,
			map[string]any{"kind": "DeleteOptions", "apiVersion": "v1", "gracePeriodSeconds": 0}, nil)
		if err != nil && !isKubeNotFound(err) {
			n.t.Fatal(err)
		}
		done++
	}
	return done
}

// serveDeletions plays a healthy node in the background until the returned func is
// called: every pod being deleted is finished at once.
func (n kubeNode) serveDeletions(base string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			var l kPodList
			q := url.Values{"labelSelector": {kubeLabelWorkspace + "=" + base}}
			if n.e.admin.list(context.Background(), n.podsPath(), q, &l) != nil {
				continue
			}
			for _, p := range l.Items {
				if p.Metadata.DeletionTimestamp != nil {
					_ = n.e.admin.do(context.Background(), http.MethodDelete, n.podsPath()+"/"+p.Metadata.Name, nil, kubeJSON,
						map[string]any{"kind": "DeleteOptions", "apiVersion": "v1", "gracePeriodSeconds": 0}, nil)
				}
			}
		}
	}()
	return func() { close(stop); <-done }
}

// run brings a workspace to `running`: Start, then the node binds the pod and reports
// it ready.
func (n kubeNode) run(rt *kubeRuntime) kPod {
	n.t.Helper()
	if err := rt.Start(context.Background()); err != nil {
		n.t.Fatalf("Start: %v", err)
	}
	p := n.waitPod(rt.base)
	n.bind(p.Metadata.Name)
	n.status(p.Metadata.Name, true, true, "")
	eventually(n.t, 30*time.Second, "running", func() bool { return rt.State(context.Background()) == "running" })
	return p
}

func newKubeTestRuntime(f *kubeFactory, name string) *kubeRuntime {
	return f.New(Workspace{ContainerName: name, AgentToken: "agent-token-value", MemBytes: 2 * gib, CPUUnits: 1024},
		"dek-value", []string{"AF_MINTED_TOKEN=minted-value"}).(*kubeRuntime)
}

// --- the cases ---

// The whole cycle: what Start creates (and that the pod passes `restricted`), where the
// secrets are, running and starting by readiness, the task count, and a Stop that
// returns only once the pod is gone — reading `stopped` while it terminates, with Start
// refusing to launch over it.
func TestKubernetesEnvLifecycle(t *testing.T) {
	e, ns := needKubeEnv(t)
	t.Setenv("AF_STOP_GRACE_SEC", "30")
	f := e.factory(t, ns)
	rt := newKubeTestRuntime(f, "af-ws-alice")
	node := kubeNode{t, e, ns}
	ctx := context.Background()

	if got := rt.State(ctx); got != "none" {
		t.Fatalf("State before any start = %q, want none", got)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := rt.State(ctx); got != "starting" {
		t.Fatalf("State right after Start = %q, want starting", got)
	}
	// The controller made a pod, so the template passed the namespace's `restricted`
	// admission; the template holds no secret, the Secret holds them all.
	p := node.waitPod(rt.base)
	var sts json.RawMessage
	if err := e.admin.get(ctx, "/apis/apps/v1/namespaces/"+ns+"/statefulsets/"+rt.base, &sts); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"agent-token-value", "dek-value", "minted-value"} {
		if bytes.Contains(sts, []byte(secret)) {
			t.Errorf("the StatefulSet carries the secret value %q", secret)
		}
	}
	var sec kSecret
	if err := e.admin.get(ctx, "/api/v1/namespaces/"+ns+"/secrets/"+rt.secretName(), &sec); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"AGENT_TOKEN": "agent-token-value", "AF_SECRET_KEY": "dek-value",
		"AF_MINTED_TOKEN": "minted-value", "WS_TEMPLATE": "1"} {
		if string(sec.Data[k]) != v {
			t.Errorf("Secret[%s] = %q, want %q", k, sec.Data[k], v)
		}
	}
	for _, path := range []string{"/api/v1/namespaces/" + ns + "/services/" + rt.base,
		"/api/v1/namespaces/" + ns + "/persistentvolumeclaims/" + rt.homeClaim(),
		"/api/v1/namespaces/" + ns + "/persistentvolumeclaims/" + rt.stateClaim()} {
		var o json.RawMessage
		if err := e.admin.get(ctx, path, &o); err != nil {
			t.Errorf("Start did not create %s: %v", path, err)
		}
	}
	if p.Metadata.Annotations[kubeAnnStartGen] != "1" {
		t.Errorf("pod start generation = %q, want 1", p.Metadata.Annotations[kubeAnnStartGen])
	}

	node.bind(p.Metadata.Name)
	node.status(p.Metadata.Name, true, false, "")
	if got := rt.State(ctx); got != "starting" {
		t.Fatalf("State with the container running but not Ready = %q, want starting", got)
	}
	if n, err := rt.RunningTasks(ctx); err != nil || n != 1 {
		t.Fatalf("RunningTasks before Ready = %d, %v; want 1 (a running container counts whatever its readiness)", n, err)
	}
	node.status(p.Metadata.Name, true, true, "")
	if got := rt.State(ctx); got != "running" {
		t.Fatalf("State when Ready = %q, want running", got)
	}
	// An agent that stops answering makes the workspace `starting` again, and is still a task.
	node.status(p.Metadata.Name, true, false, "")
	if got := rt.State(ctx); got != "starting" {
		t.Fatalf("State after readiness lost = %q, want starting", got)
	}
	if n, _ := rt.RunningTasks(ctx); n != 1 {
		t.Fatalf("RunningTasks after readiness lost = %d, want 1", n)
	}
	node.status(p.Metadata.Name, true, true, "")

	// Stop: the controller deletes the pod, which the node holds while it terminates.
	stopErr := make(chan error, 1)
	go func() { stopErr <- rt.Stop(ctx) }()
	eventually(t, 30*time.Second, "the pod to be terminating", func() bool {
		for _, p := range node.pods(rt.base) {
			if p.Metadata.DeletionTimestamp != nil {
				return true
			}
		}
		return false
	})
	if got := rt.State(ctx); got != "stopped" {
		t.Fatalf("State while the pod terminates = %q, want stopped (never starting: the start handler would drop the Start)", got)
	}
	if err := rt.Start(ctx); err == nil || !strings.Contains(err.Error(), "has not settled") {
		t.Fatalf("Start over a terminating pod = %v, want a not-settled error", err)
	}
	select {
	case err := <-stopErr:
		t.Fatalf("Stop returned (%v) while the pod still existed", err)
	case <-time.After(300 * time.Millisecond):
	}
	if node.finishDeletions(rt.base) == 0 {
		t.Fatal("no terminating pod to finish")
	}
	select {
	case err := <-stopErr:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Stop did not return after the pod was gone")
	}
	if got := rt.State(ctx); got != "stopped" {
		t.Fatalf("State after Stop = %q, want stopped", got)
	}
	// The next start is a new generation.
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start after a settled stop: %v", err)
	}
	if p := node.waitPod(rt.base); p.Metadata.Annotations[kubeAnnStartGen] != "2" {
		t.Errorf("second start's pod generation = %q, want 2", p.Metadata.Annotations[kubeAnnStartGen])
	}
}

// Stop then Start at once (Recreate's order): the Start follows a Stop that has already
// settled, so it launches, on a new start generation.
func TestKubernetesEnvStopThenStartAtOnce(t *testing.T) {
	e, ns := needKubeEnv(t)
	t.Setenv("AF_STOP_GRACE_SEC", "5")
	rt := newKubeTestRuntime(e.factory(t, ns), "af-ws-bob")
	node := kubeNode{t, e, ns}
	ctx := context.Background()
	node.run(rt)
	stopNode := node.serveDeletions(rt.base)
	defer stopNode()
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start right after Stop: %v", err)
	}
	p := node.waitPod(rt.base)
	if g := p.Metadata.Annotations[kubeAnnStartGen]; g != "2" {
		t.Fatalf("pod after Stop+Start carries generation %q, want 2", g)
	}
	if got := rt.State(ctx); got != "starting" {
		t.Fatalf("State = %q, want starting until the new pod is Ready", got)
	}
}

// A State read between Start's write and the controller's status update: with the
// controller paused, the new generation is not observed and the state is `starting`,
// never `running`.
func TestKubernetesEnvStateBeforeControllerObserves(t *testing.T) {
	e, ns := needKubeEnv(t)
	t.Setenv("AF_STOP_GRACE_SEC", "5")
	rt := newKubeTestRuntime(e.factory(t, ns), "af-ws-carol")
	node := kubeNode{t, e, ns}
	ctx := context.Background()
	node.run(rt)
	stopNode := node.serveDeletions(rt.base)
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	stopNode()
	if err := e.kcm.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	resumed := false
	resume := func() {
		if !resumed {
			_ = e.kcm.Process.Signal(syscall.SIGCONT)
			resumed = true
		}
	}
	defer resume()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for i := 0; i < 5; i++ {
		if got := rt.State(ctx); got != "starting" {
			t.Fatalf("State before the controller observed the start = %q, want starting", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
	resume()
	p := node.waitPod(rt.base)
	node.bind(p.Metadata.Name)
	node.status(p.Metadata.Name, true, true, "")
	eventually(t, 30*time.Second, "running", func() bool { return rt.State(ctx) == "running" })
}

// Stop while starting: an unscheduled pod goes at once; a scheduled one is waited for.
func TestKubernetesEnvStopWhileStarting(t *testing.T) {
	e, ns := needKubeEnv(t)
	t.Setenv("AF_STOP_GRACE_SEC", "5")
	rt := newKubeTestRuntime(e.factory(t, ns), "af-ws-dave")
	node := kubeNode{t, e, ns}
	ctx := context.Background()

	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	node.waitPod(rt.base)
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("Stop of an unscheduled start: %v", err)
	}
	if got := rt.State(ctx); got != "stopped" {
		t.Fatalf("State = %q, want stopped", got)
	}

	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	p := node.waitPod(rt.base)
	node.bind(p.Metadata.Name)
	node.status(p.Metadata.Name, false, false, "")
	stopNode := node.serveDeletions(rt.base)
	defer stopNode()
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("Stop of a scheduled start: %v", err)
	}
	if pods := node.pods(rt.base); len(pods) != 0 {
		t.Fatalf("Stop returned with %d pod(s) left", len(pods))
	}
}

// A CP restart in the middle of a start: a new adapter value, built from a new client,
// reads the same state from the cluster and carries the start through.
func TestKubernetesEnvRestartMidStart(t *testing.T) {
	e, ns := needKubeEnv(t)
	t.Setenv("AF_STOP_GRACE_SEC", "5")
	first := newKubeTestRuntime(e.factory(t, ns), "af-ws-erin")
	node := kubeNode{t, e, ns}
	ctx := context.Background()
	if err := first.Start(ctx); err != nil {
		t.Fatal(err)
	}
	p := node.waitPod(first.base)
	second := newKubeTestRuntime(e.factory(t, ns), "af-ws-erin")
	if got := second.State(ctx); got != "starting" {
		t.Fatalf("restarted CP reads %q, want starting", got)
	}
	// The restarted CP's handler would not Start again, but if it did, nothing moves.
	if err := second.Start(ctx); err != nil {
		t.Fatalf("Start on a launch under way: %v", err)
	}
	if again := node.waitPod(first.base); again.Metadata.UID != p.Metadata.UID {
		t.Fatal("a second Start replaced the pod of the launch under way")
	}
	node.bind(p.Metadata.Name)
	node.status(p.Metadata.Name, true, true, "")
	eventually(t, 30*time.Second, "running", func() bool { return second.State(ctx) == "running" })
	stopNode := node.serveDeletions(first.base)
	defer stopNode()
	if err := second.Stop(ctx); err != nil {
		t.Fatalf("Stop from the restarted CP: %v", err)
	}
}

// A running workspace's pod deleted behind the CP's back (an unplanned drain): the
// controller recreates it on the same start generation, and the workspace returns
// through `starting` to `running` with no CP action.
func TestKubernetesEnvPodDeletedBehindTheCP(t *testing.T) {
	e, ns := needKubeEnv(t)
	t.Setenv("AF_STOP_GRACE_SEC", "5")
	rt := newKubeTestRuntime(e.factory(t, ns), "af-ws-frank")
	node := kubeNode{t, e, ns}
	ctx := context.Background()
	old := node.run(rt)
	if err := e.admin.delete(ctx, node.podsPath()+"/"+old.Metadata.Name); err != nil {
		t.Fatal(err)
	}
	if got := rt.State(ctx); got != "starting" {
		t.Fatalf("State with the pod being drained = %q, want starting", got)
	}
	node.finishDeletions(rt.base)
	var p kPod
	eventually(t, 30*time.Second, "the replacement pod", func() bool {
		for _, q := range node.pods(rt.base) {
			if q.Metadata.UID != old.Metadata.UID && q.Metadata.DeletionTimestamp == nil {
				p = q
				return true
			}
		}
		return false
	})
	if p.Metadata.Annotations[kubeAnnStartGen] != old.Metadata.Annotations[kubeAnnStartGen] {
		t.Fatalf("replacement pod generation %q, want the same start's %q",
			p.Metadata.Annotations[kubeAnnStartGen], old.Metadata.Annotations[kubeAnnStartGen])
	}
	if got := rt.State(ctx); got != "starting" {
		t.Fatalf("State with the replacement booting = %q, want starting", got)
	}
	node.bind(p.Metadata.Name)
	node.status(p.Metadata.Name, true, true, "")
	eventually(t, 30*time.Second, "running again", func() bool { return rt.State(ctx) == "running" })
}

// A stop that does not settle (a node that stopped answering) is an error after the
// grace plus the margin, and leaves `stopped` with a pod that Start and Destroy refuse
// to work over. Once the node answers, Destroy removes everything.
func TestKubernetesEnvStopNotSettledAndDestroy(t *testing.T) {
	e, ns := needKubeEnv(t)
	t.Setenv("AF_STOP_GRACE_SEC", "1")
	rt := newKubeTestRuntime(e.factory(t, ns), "af-ws-grace")
	node := kubeNode{t, e, ns}
	ctx := context.Background()
	node.run(rt)
	err := rt.Stop(ctx)
	if err == nil || !strings.Contains(err.Error(), "not settled") {
		t.Fatalf("Stop with a node that never confirms = %v, want a not-settled error", err)
	}
	if got := rt.State(ctx); got != "stopped" {
		t.Fatalf("State after the failed stop = %q, want stopped", got)
	}
	if err := rt.Start(ctx); err == nil {
		t.Fatal("Start launched over a pod that still exists")
	}
	if res, err := rt.Destroy(ctx); err == nil {
		t.Fatalf("Destroy over a pod that still exists returned %v, nil", res)
	}
	node.finishDeletions(rt.base)
	res, err := rt.Destroy(ctx)
	if err != nil || len(res) != 0 {
		t.Fatalf("Destroy = %v, %v; want no residue", res, err)
	}
	for _, path := range []string{
		"/apis/apps/v1/namespaces/" + ns + "/statefulsets/" + rt.base,
		"/api/v1/namespaces/" + ns + "/services/" + rt.base,
		"/api/v1/namespaces/" + ns + "/secrets/" + rt.secretName(),
		"/api/v1/namespaces/" + ns + "/persistentvolumeclaims/" + rt.homeClaim(),
		"/api/v1/namespaces/" + ns + "/persistentvolumeclaims/" + rt.stateClaim(),
	} {
		var o json.RawMessage
		if err := e.admin.get(ctx, path, &o); !isKubeNotFound(err) {
			t.Errorf("after Destroy %s: %v, want not found", path, err)
		}
	}
	if got := rt.State(ctx); got != "none" {
		t.Fatalf("State after Destroy = %q, want none", got)
	}
}

// A pod the namespace refuses never appears, and the boot phase says why instead of a
// bare `starting`. The refusal is the real `restricted` admission, which also shows that
// the adapter's own template is what passes it.
func TestKubernetesEnvBootPhaseNamesARefusedPod(t *testing.T) {
	e, ns := needKubeEnv(t)
	rt := newKubeTestRuntime(e.factory(t, ns), "af-ws-heidi")
	rt.editTemplate = func(tmpl *kPodTemplateSpec) {
		tmpl.Spec.Containers[0].SecurityContext.Capabilities = nil
	}
	ctx := context.Background()
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "a blocked boot phase", func() bool {
		return rt.State(ctx) == "starting" && strings.HasPrefix(rt.BootPhase(), "blocked: ")
	})
	if !strings.Contains(rt.BootPhase(), "restricted") {
		t.Errorf("BootPhase = %q, want the Pod Security refusal", rt.BootPhase())
	}
	if pods := (kubeNode{t, e, ns}).pods(rt.base); len(pods) != 0 {
		t.Fatalf("%d pod(s) exist for a template `restricted` refuses", len(pods))
	}
}

// The boot phase follows the pod: waiting for a node, then a container that cannot
// pull its image, then the agent booting; and it is cleared once running.
func TestKubernetesEnvBootPhaseFollowsThePod(t *testing.T) {
	e, ns := needKubeEnv(t)
	rt := newKubeTestRuntime(e.factory(t, ns), "af-ws-ivan")
	node := kubeNode{t, e, ns}
	ctx := context.Background()
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	p := node.waitPod(rt.base)
	node.bind(p.Metadata.Name)
	node.status(p.Metadata.Name, false, false, "ImagePullBackOff")
	if rt.State(ctx); !strings.HasPrefix(rt.BootPhase(), "blocked: ImagePullBackOff") {
		t.Fatalf("BootPhase = %q, want blocked: ImagePullBackOff", rt.BootPhase())
	}
	node.status(p.Metadata.Name, true, false, "")
	if rt.State(ctx); rt.BootPhase() != "agent: starting" {
		t.Fatalf("BootPhase = %q, want agent: starting", rt.BootPhase())
	}
	// Another adapter value reads the phase the first one noted: BootPhase takes no
	// context, so it is process-wide.
	if other := newKubeTestRuntime(e.factory(t, ns), "af-ws-ivan"); other.BootPhase() != "agent: starting" {
		t.Fatalf("BootPhase from another adapter value = %q", other.BootPhase())
	}
	node.status(p.Metadata.Name, true, true, "")
	if got := rt.State(ctx); got != "running" || rt.BootPhase() != "" {
		t.Fatalf("State = %q, BootPhase = %q; want running and no phase", got, rt.BootPhase())
	}
}

// Start's write is conditional on the StatefulSet it checked. A spec change between its
// read and its write (here an operator's, made from inside the window) makes the real
// API server refuse the JSON Patch test, and Start returns the error with neither the
// template nor the replicas overwritten.
func TestKubernetesEnvStartRefusesAStatefulSetChangedUnderIt(t *testing.T) {
	e, ns := needKubeEnv(t)
	t.Setenv("AF_STOP_GRACE_SEC", "5")
	rt := newKubeTestRuntime(e.factory(t, ns), "af-ws-judy")
	node := kubeNode{t, e, ns}
	ctx := context.Background()
	node.run(rt)
	stopNode := node.serveDeletions(rt.base)
	defer stopNode()
	if err := rt.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	stsPath := "/apis/apps/v1/namespaces/" + ns + "/statefulsets/" + rt.base
	rt.editTemplate = func(*kPodTemplateSpec) {
		if err := e.admin.jsonPatch(ctx, stsPath, []kubePatchOp{{Op: "add", Path: "/spec/minReadySeconds", Value: 1}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := rt.Start(ctx); err == nil {
		t.Fatal("Start wrote over a StatefulSet whose spec changed after it checked")
	}
	var s kStatefulSet
	if err := e.admin.get(ctx, stsPath, &s); err != nil {
		t.Fatal(err)
	}
	if stsReplicas(&s) != 0 || s.Spec.Template.Metadata.Annotations[kubeAnnStartGen] != "1" {
		t.Fatalf("after the refused Start: replicas %d, start generation %q; want 0 and the first start's 1",
			stsReplicas(&s), s.Spec.Template.Metadata.Annotations[kubeAnnStartGen])
	}
	// Once the controller has observed the change the stop is settled again, and the next
	// Start reads the new generation and goes through. Until then it refuses, correctly.
	rt.editTemplate = nil
	var lastErr error
	eventually(t, 30*time.Second, "a Start after the change", func() bool {
		lastErr = rt.Start(ctx)
		return lastErr == nil
	})
	if p := node.waitPod(rt.base); p.Metadata.Annotations[kubeAnnStartGen] != "2" {
		t.Fatalf("generation %q, want 2", p.Metadata.Annotations[kubeAnnStartGen])
	}
}
