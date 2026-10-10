package runtime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- a recorded API server ---

// fakeKube answers from a table of recorded replies, keyed "METHOD path" (the query
// string is ignored), and records every request it saw.
type fakeKube struct {
	mu      sync.Mutex
	replies map[string]fakeReply
	seen    []string
	auth    []string
	// onRequest, when set, runs for every request before it is answered; it may block
	// (until r.Context() is done, say) to hold a request in flight.
	onRequest func(r *http.Request)
}

type fakeReply struct {
	code int
	body string
}

func (f *fakeKube) set(key string, code int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[key] = fakeReply{code, body}
}

func (f *fakeKube) saw(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.seen {
		if s == key {
			return true
		}
	}
	return false
}

const kubeNotFoundBody = `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"not found","reason":"NotFound","code":404}`

func (f *fakeKube) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.seen = append(f.seen, key)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	rep, ok := f.replies[key]
	hook := f.onRequest
	f.mu.Unlock()
	if hook != nil {
		hook(r)
	}
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(kubeNotFoundBody))
		return
	}
	w.WriteHeader(rep.code)
	_, _ = w.Write([]byte(rep.body))
}

// startFakeKube serves f over TLS and returns a client that trusts its certificate.
func startFakeKube(t *testing.T, f *fakeKube, token string) (*kubeClient, *httptest.Server) {
	t.Helper()
	if f.replies == nil {
		f.replies = map[string]fakeReply{}
	}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := newKubeClient(srv.URL, ca, tok)
	if err != nil {
		t.Fatal(err)
	}
	return c, srv
}

// --- the client ---

// The service account token is a projected token the kubelet rotates. A request that
// meets a 401 reads the file again and retries once; within kubeTokenMaxAge the cached
// copy is reused, and after it the file is read again without waiting for a 401.
func TestKubeClientRereadsTheTokenOn401(t *testing.T) {
	var valid struct {
		sync.Mutex
		tok string
	}
	valid.tok = "first"
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		valid.Lock()
		defer valid.Unlock()
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seen = append(seen, got)
		if got != valid.tok {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"kind":"Status","reason":"Unauthorized","code":401}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	file := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(file, []byte("first\n"), 0o600)
	c, err := newKubeClient(srv.URL, ca, file)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_000_000, 0)
	c.now = func() time.Time { return clock }
	ctx := context.Background()
	if err := c.get(ctx, "/x", nil); err != nil {
		t.Fatalf("first request: %v", err)
	}
	// The kubelet rotates the file and the old token expires.
	_ = os.WriteFile(file, []byte("second\n"), 0o600)
	valid.Lock()
	valid.tok = "second"
	valid.Unlock()
	if err := c.get(ctx, "/x", nil); err != nil {
		t.Fatalf("request after rotation: %v", err)
	}
	if want := []string{"first", "first", "second"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("tokens sent = %v, want %v (cached copy, a 401, then the re-read one)", seen, want)
	}
	// A rotation the server has not enforced yet is picked up once the cache is stale.
	_ = os.WriteFile(file, []byte("third\n"), 0o600)
	clock = clock.Add(kubeTokenMaxAge + time.Second)
	valid.Lock()
	valid.tok = "third"
	valid.Unlock()
	if err := c.get(ctx, "/x", nil); err != nil {
		t.Fatal(err)
	}
	if last := seen[len(seen)-1]; last != "third" || len(seen) != 4 {
		t.Fatalf("after the cache aged out sent %v, want one request with the third token", seen)
	}
	// Only one retry: a token the server keeps refusing is an error, not a loop.
	valid.Lock()
	valid.tok = "nobody"
	valid.Unlock()
	err = c.get(ctx, "/x", nil)
	if kubeErrCode(err) != http.StatusUnauthorized {
		t.Fatalf("persistent 401 = %v, want a 401 error", err)
	}
	if len(seen) != 6 {
		t.Fatalf("persistent 401 sent %d requests in all, want 6 (one retry)", len(seen))
	}
}

// The API server is verified against the service account's CA, and the token never
// leaves for a server that fails that check.
func TestKubeClientVerifiesTheServerAgainstTheCA(t *testing.T) {
	f := &fakeKube{}
	_, srv := startFakeKube(t, f, "secret-token")
	// Every httptest server shares one certificate, so the wrong CA is made here.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "other-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	wrongCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	file := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(file, []byte("secret-token"), 0o600)
	c, err := newKubeClient(srv.URL, wrongCA, file)
	if err != nil {
		t.Fatal(err)
	}
	err = c.get(context.Background(), "/api/v1/namespaces/ns/pods", nil)
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("request to a server the CA did not sign = %v, want a certificate error", err)
	}
	if len(f.seen) != 0 {
		t.Fatalf("the server saw %v; the request must not reach a server that fails verification", f.seen)
	}
	if _, err := newKubeClient(srv.URL, []byte("not a certificate"), file); err == nil {
		t.Fatal("a CA bundle without a certificate was accepted")
	}
}

func TestKubeClientDecodesStatusErrors(t *testing.T) {
	f := &fakeKube{}
	c, _ := startFakeKube(t, f, "t")
	f.set("POST /api/v1/namespaces/ns/services", 409,
		`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"services \"x\" already exists","reason":"AlreadyExists","code":409}`)
	err := c.get(context.Background(), "/api/v1/namespaces/ns/services/x", nil)
	if !isKubeNotFound(err) {
		t.Fatalf("404 = %v, want not found", err)
	}
	err = c.create(context.Background(), "/api/v1/namespaces/ns/services", map[string]any{}, nil)
	if !isKubeConflict(err) || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("409 = %v, want a conflict carrying the server's message", err)
	}
	if f.auth[0] != "Bearer t" {
		t.Fatalf("Authorization = %q", f.auth[0])
	}
}

// --- State against recorded replies ---

// stsJSON is a StatefulSet as the API server returns it, trimmed to what matters.
func stsJSON(replicas, generation, observed, statusReplicas int, updateRev, startGen string) string {
	return `{"kind":"StatefulSet","apiVersion":"apps/v1","metadata":{"name":"af-ws-x","namespace":"ns","generation":` +
		itoa(generation) + `,"resourceVersion":"812"},"spec":{"replicas":` + itoa(replicas) +
		`,"selector":{"matchLabels":{"agent-fleet.io/role":"workspace","agent-fleet.io/workspace":"af-ws-x"}},` +
		`"template":{"metadata":{"annotations":{"agent-fleet.io/start-generation":"` + startGen + `"}},"spec":{"containers":[{"name":"agent","image":"i"}]}},` +
		`"serviceName":"af-ws-x","podManagementPolicy":"OrderedReady","updateStrategy":{"type":"RollingUpdate","rollingUpdate":{"partition":0}},"revisionHistoryLimit":2},` +
		`"status":{"observedGeneration":` + itoa(observed) + `,"replicas":` + itoa(statusReplicas) +
		`,"currentRevision":"af-ws-x-6d4b9c7f5","updateRevision":"` + updateRev + `","collisionCount":0,"availableReplicas":0}}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// podJSON is the controller's pod as the API server returns it.
func podJSON(rev, startGen string, ready, running, deleting bool) string {
	del := ""
	if deleting {
		del = `,"deletionTimestamp":"2026-10-02T03:00:00Z","deletionGracePeriodSeconds":30`
	}
	state := `{"waiting":{"reason":"ContainerCreating"}}`
	if running {
		state = `{"running":{"startedAt":"2026-10-02T02:59:00Z"}}`
	}
	r := "False"
	if ready {
		r = "True"
	}
	return `{"metadata":{"name":"af-ws-x-0","namespace":"ns","labels":{"agent-fleet.io/role":"workspace","agent-fleet.io/workspace":"af-ws-x",` +
		`"apps.kubernetes.io/pod-index":"0","controller-revision-hash":"` + rev + `","statefulset.kubernetes.io/pod-name":"af-ws-x-0"},` +
		`"annotations":{"agent-fleet.io/start-generation":"` + startGen + `"}` + del + `},` +
		`"spec":{"nodeName":"gke-pool-a-1","containers":[{"name":"agent","image":"i"}]},` +
		`"status":{"phase":"Running","conditions":[{"type":"PodScheduled","status":"True"},{"type":"Ready","status":"` + r + `"}],` +
		`"containerStatuses":[{"name":"agent","ready":` + map[bool]string{true: "true", false: "false"}[ready] + `,"restartCount":0,"state":` + state + `}]}}`
}

func podListJSON(pods ...string) string {
	return `{"kind":"PodList","apiVersion":"v1","metadata":{"resourceVersion":"900"},"items":[` + strings.Join(pods, ",") + `]}`
}

func fakeKubeRuntime(t *testing.T) (*kubeRuntime, *fakeKube) {
	t.Helper()
	f := &fakeKube{}
	c, _ := startFakeKube(t, f, "t")
	fac := &kubeFactory{
		cfg: &kubeConfig{namespace: "ns", image: "reg.example/ws:1", homeGiB: 10, stateGiB: 1, serviceAccount: "default"},
		c:   c, pins: fakePinner{}, poll: 10 * time.Millisecond, stopMargin: 100 * time.Millisecond,
	}
	return fac.New(Workspace{ContainerName: "af-ws-x"}, SecretKeys{}, nil).(*kubeRuntime), f
}

const (
	stsPathX  = "GET /apis/apps/v1/namespaces/ns/statefulsets/af-ws-x"
	podsPathX = "GET /api/v1/namespaces/ns/pods"
)

// Every row of ADR 0106 decision 3's table, and the windows each condition closes.
func TestKubeStateTable(t *testing.T) {
	const newRev, oldRev = "af-ws-x-7f8c9d6b4", "af-ws-x-6d4b9c7f5"
	for _, c := range []struct {
		name string
		sts  string // "" = no StatefulSet
		pods []string
		want string
	}{
		{"no StatefulSet", "", nil, "none"},
		{"running", stsJSON(1, 4, 4, 1, newRev, "2"), []string{podJSON(newRev, "2", true, true, false)}, "running"},
		{"pod not Ready", stsJSON(1, 4, 4, 1, newRev, "2"), []string{podJSON(newRev, "2", false, true, false)}, "starting"},
		{"no pod yet", stsJSON(1, 4, 4, 0, newRev, "2"), nil, "starting"},
		// Right after Start's write: the controller has not observed it, and the only Ready
		// pod is the old start's. It must not pass for the new one.
		{"generation not observed", stsJSON(1, 5, 4, 1, oldRev, "3"), []string{podJSON(oldRev, "2", true, true, false)}, "starting"},
		{"pod of the old revision", stsJSON(1, 5, 5, 1, newRev, "3"), []string{podJSON(oldRev, "2", true, true, false)}, "starting"},
		{"pod of an older start generation", stsJSON(1, 5, 5, 1, newRev, "3"), []string{podJSON(newRev, "2", true, true, false)}, "starting"},
		// Each guard alone: the others hold, so only that guard makes it `starting`.
		{"only the generation not observed", stsJSON(1, 5, 4, 1, newRev, "3"), []string{podJSON(newRev, "3", true, true, false)}, "starting"},
		{"only the revision is old", stsJSON(1, 5, 5, 1, newRev, "3"), []string{podJSON(oldRev, "3", true, true, false)}, "starting"},
		{"only the start generation is old", stsJSON(1, 5, 5, 1, newRev, "3"), []string{podJSON(newRev, "2", true, true, false)}, "starting"},
		{"no updateRevision reported yet", stsJSON(1, 5, 5, 1, "", "3"), []string{podJSON("", "3", true, true, false)}, "starting"},
		{"Ready pod being deleted", stsJSON(1, 4, 4, 1, newRev, "2"), []string{podJSON(newRev, "2", true, true, true)}, "starting"},
		{"stopped", stsJSON(0, 6, 6, 0, newRev, "2"), nil, "stopped"},
		// A stop in progress must never read as starting (workspace_handlers.go would drop
		// the Start of a Recreate).
		{"stopped with a pod terminating", stsJSON(0, 6, 5, 1, newRev, "2"), []string{podJSON(newRev, "2", true, true, true)}, "stopped"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt, f := fakeKubeRuntime(t)
			if c.sts != "" {
				f.set(stsPathX, 200, c.sts)
			}
			f.set(podsPathX, 200, podListJSON(c.pods...))
			if got := rt.State(context.Background()); got != c.want {
				t.Fatalf("State = %q, want %q", got, c.want)
			}
		})
	}
}

// The task count is containers running, Ready or not, and an unreadable list is an
// error — the start deadline treats an error as running and does not stop.
func TestKubeRunningTasks(t *testing.T) {
	rt, f := fakeKubeRuntime(t)
	const rev = "af-ws-x-1"
	f.set(podsPathX, 200, podListJSON(podJSON(rev, "1", false, true, false)))
	if n, err := rt.RunningTasks(context.Background()); n != 1 || err != nil {
		t.Fatalf("RunningTasks = %d, %v; want 1 for a running container that is not Ready", n, err)
	}
	f.set(podsPathX, 200, podListJSON(podJSON(rev, "1", false, false, false)))
	if n, err := rt.RunningTasks(context.Background()); n != 0 || err != nil {
		t.Fatalf("RunningTasks = %d, %v; want 0 while the container is created", n, err)
	}
	f.set(podsPathX, 403, `{"kind":"Status","reason":"Forbidden","message":"pods is forbidden","code":403}`)
	if _, err := rt.RunningTasks(context.Background()); err == nil {
		t.Fatal("RunningTasks hid an unreadable pod list")
	}
}

// --- Destroy ---

type roundTripHook struct {
	rt   http.RoundTripper
	hook func(*http.Request)
}

func (h roundTripHook) RoundTrip(r *http.Request) (*http.Response, error) {
	h.hook(r)
	return h.rt.RoundTrip(r)
}

// Destroy starts from the settled stop and refuses without it.
func TestKubeDestroyRequiresTheSettledStop(t *testing.T) {
	t.Setenv("AF_STOP_GRACE_SEC", "1")
	rt, f := fakeKubeRuntime(t)
	f.set(stsPathX, 200, stsJSON(0, 6, 6, 1, "r", "2"))
	f.set(podsPathX, 200, podListJSON(podJSON("r", "2", false, true, true)))
	if res, err := rt.Destroy(context.Background()); err == nil || !strings.Contains(err.Error(), "not settled") {
		t.Fatalf("Destroy = %v, %v; want the stop's not-settled error", res, err)
	}
	for _, s := range f.seen {
		if strings.HasPrefix(s, "DELETE") {
			t.Fatalf("Destroy deleted %s without a settled stop", s)
		}
	}
}

// --- the pod template ---

// The template passes `restricted` field by field (the envtest checks the real
// admission; this keeps the fields pinned where the binaries are absent), and no value
// that varies per start, the secrets above all, is in it.
func TestKubePodTemplateIsRestrictedAndCarriesNoSecret(t *testing.T) {
	t.Setenv("AF_STOP_GRACE_SEC", "40")
	f := &kubeFactory{cfg: &kubeConfig{namespace: "ns", image: "img:1", homeGiB: 10, stateGiB: 1,
		serviceAccount: "default", templateEnv: []string{"WS_ENV_TOKEN=template-secret"},
		nodeSelector: map[string]string{"pool": "ws"}, pullSecret: "regcred"}}
	rt := f.New(Workspace{ContainerName: "af-ws-x", AgentToken: "agent-token-value", MemBytes: 3 * gib, CPUUnits: 512},
		SecretKeys{Key: "dek-value"}, []string{"AF_MINTED=minted-value"}).(*kubeRuntime)
	tmpl := rt.podTemplate("img:1@sha256:"+strings.Repeat("0", 64), 7, time.Unix(0, 0))
	raw, _ := json.Marshal(tmpl)
	for _, s := range []string{"agent-token-value", "dek-value", "minted-value", "template-secret"} {
		if strings.Contains(string(raw), s) {
			t.Errorf("the template carries %q", s)
		}
	}
	env := rt.secretEnv()
	for k, v := range map[string]string{"AGENT_TOKEN": "agent-token-value", "AF_SECRET_KEY": "dek-value",
		"AF_MINTED": "minted-value", "WS_ENV_TOKEN": "template-secret"} {
		if env[k] != v {
			t.Errorf("Secret %s = %q, want %q", k, env[k], v)
		}
	}
	s := tmpl.Spec
	c := s.Containers[0]
	sc := c.SecurityContext
	switch {
	case s.SecurityContext == nil || s.SecurityContext.RunAsNonRoot == nil || !*s.SecurityContext.RunAsNonRoot:
		t.Error("runAsNonRoot is not set")
	case s.SecurityContext.SeccompProfile == nil || s.SecurityContext.SeccompProfile.Type != "RuntimeDefault":
		t.Error("seccomp is not RuntimeDefault")
	case sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation:
		t.Error("allowPrivilegeEscalation is not false")
	case sc.Capabilities == nil || !reflect.DeepEqual(sc.Capabilities.Drop, []string{"ALL"}):
		t.Error("capabilities are not dropped")
	case s.ShareProcessNamespace == nil || !*s.ShareProcessNamespace:
		t.Error("shareProcessNamespace is not set: the image has no init")
	case s.AutomountServiceAccountToken == nil || *s.AutomountServiceAccountToken:
		t.Error("the service account token is mounted")
	case s.EnableServiceLinks == nil || *s.EnableServiceLinks:
		t.Error("service links are on")
	case s.TerminationGracePeriodSeconds == nil || *s.TerminationGracePeriodSeconds != 40:
		t.Error("terminationGracePeriodSeconds is not the stop grace")
	}
	for _, v := range s.Volumes {
		if v.PersistentVolumeClaim == nil && v.EmptyDir == nil {
			t.Errorf("volume %s is neither a claim nor an emptyDir", v.Name)
		}
	}
	envOf := map[string]string{}
	for _, e := range c.Env {
		envOf[e.Name] = e.Value
	}
	if envOf["AGENT_STOP_GRACE_SEC"] != "35" || envOf["AF_WS_KEEP"] != kubeKeepPath || envOf["CLAUDE_CONFIG_DIR"] != kubeStatePath ||
		envOf[BrowserUnavailableEnv] != "kubernetes" {
		t.Errorf("template env = %v", envOf)
	}
	if c.Resources.Limits["memory"] != "3221225472" || c.Resources.Limits["cpu"] != "500m" || c.Resources.Limits["ephemeral-storage"] == "" {
		t.Errorf("limits = %v", c.Resources.Limits)
	}
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil || c.ReadinessProbe.HTTPGet.Path != "/healthz" {
		t.Error("the readiness probe is not the agent's /healthz")
	}
	if tmpl.Metadata.Annotations[kubeAnnStartGen] != "7" || tmpl.Metadata.Annotations["cluster-autoscaler.kubernetes.io/safe-to-evict"] != "false" {
		t.Errorf("annotations = %v", tmpl.Metadata.Annotations)
	}
	if !strings.Contains(c.Image, "@sha256:") {
		t.Errorf("image %q is not pinned", c.Image)
	}
	if s.NodeSelector["pool"] != "ws" || len(s.ImagePullSecrets) != 1 {
		t.Errorf("placement = %v / %v", s.NodeSelector, s.ImagePullSecrets)
	}
}

// --- names and configuration ---

func TestKubeObjectName(t *testing.T) {
	long := "af-ws-" + strings.Repeat("tenant", 6) + "-" + strings.Repeat("user", 10)
	a, b := kubeObjectName(long), kubeObjectName(long+"x")
	if len(a) > 40 || len(b) > 40 || a == b || a != kubeObjectName(long) {
		t.Fatalf("kubeObjectName: %q / %q", a, b)
	}
	if got := kubeObjectName("af-ws-alice"); got != "af-ws-alice" {
		t.Fatalf("a short name changed: %q", got)
	}
}

func TestKubeConfigFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string, string) string {
		return func(k, def string) string {
			if v, ok := m[k]; ok {
				return v
			}
			return def
		}
	}
	ok := map[string]string{"AF_K8S_NAMESPACE": "af-ws", "AF_K8S_WORKSPACE_IMAGE": "reg/ws:1",
		"AF_K8S_NODE_SELECTOR": "pool=ws, zone=a", "AF_K8S_HOME_GIB": "80"}
	cfg, err := kubeConfigFromEnv(env(ok), Config{Memory: "4g", ExtraEnv: []string{"A=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.homeGiB != 80 || cfg.stateGiB != kubeDefaultStateGiB || cfg.serviceAccount != "default" ||
		cfg.defaultMemBytes != 4*gib || !reflect.DeepEqual(cfg.nodeSelector, map[string]string{"pool": "ws", "zone": "a"}) {
		t.Fatalf("cfg = %+v", cfg)
	}
	for name, bad := range map[string]map[string]string{
		"no namespace":    {"AF_K8S_WORKSPACE_IMAGE": "x"},
		"selector":        {"AF_K8S_NAMESPACE": "n", "AF_K8S_WORKSPACE_IMAGE": "x", "AF_K8S_NODE_SELECTOR": "pool"},
		"home size":       {"AF_K8S_NAMESPACE": "n", "AF_K8S_WORKSPACE_IMAGE": "x", "AF_K8S_HOME_GIB": "0"},
		"image":           {"AF_K8S_NAMESPACE": "n", "AF_K8S_WORKSPACE_IMAGE": "x@sha256:12"},
		"no image at all": {"AF_K8S_NAMESPACE": "n"},
	} {
		if _, err := kubeConfigFromEnv(env(bad), Config{}); err == nil {
			t.Errorf("%s: accepted %v", name, bad)
		}
	}
}

// NewFactory builds the adapter for both spellings when the CP runs in a pod, and
// refuses outside one rather than talking to some other API server.
func TestNewFactoryKubernetes(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	ca := filepath.Join(dir, "ca.crt")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	tok := filepath.Join(dir, "token")
	_ = os.WriteFile(tok, []byte("t"), 0o600)
	defer func(a, b string) { kubeSACAFile, kubeSATokenFile = a, b }(kubeSACAFile, kubeSATokenFile)
	kubeSACAFile, kubeSATokenFile = ca, tok
	t.Setenv("AF_K8S_NAMESPACE", "af-ws")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	if _, err := NewFactory("kubernetes", Config{Image: "reg/ws:1"}); err == nil || !strings.Contains(err.Error(), "inside the cluster") {
		t.Fatalf("outside a pod: %v, want the inside-the-cluster error", err)
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	for _, p := range []string{"kubernetes", "k8s"} {
		f, err := NewFactory(p, Config{Image: "reg/ws:1"})
		if err != nil {
			t.Fatalf("NewFactory(%q): %v", p, err)
		}
		kf, ok := f.(*kubeFactory)
		if !ok {
			t.Fatalf("NewFactory(%q) = %T", p, f)
		}
		if kf.c.base != "https://10.0.0.1:443" || kf.WorkspaceImage() != "reg/ws:1" {
			t.Fatalf("factory = %s / %s", kf.c.base, kf.WorkspaceImage())
		}
		if sp := kf.SizingProfile(); sp.Runtime != "kubernetes" || !sp.DiskGrowOnly || sp.DiskMeaning != DiskMeaningHome {
			t.Fatalf("SizingProfile = %+v", sp)
		}
		if cp := kf.CostProfile(); cp.Runtime != "kubernetes" || cp.Available {
			t.Fatalf("CostProfile = %+v", cp)
		}
	}
}

// With the egress proxy in the template env, workspace→CP calls to the internal URL
// must not go through it (ADR 0106 decision 8).
func TestKubeSecretEnvBypassesTheProxyForTheCP(t *testing.T) {
	f := &kubeFactory{cfg: &kubeConfig{namespace: "ns", image: "img:1", templateEnv: []string{
		"HTTPS_PROXY=http://cp:3128", "https_proxy=http://cp:3128",
		"NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1"}}}
	rt := f.New(Workspace{ContainerName: "af-ws-x"}, SecretKeys{}, []string{"AF_CP_INTERNAL_URL=http://af-cp-internal.af-cp.svc:8098"}).(*kubeRuntime)
	env := rt.secretEnv()
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		if env[k] != "localhost,127.0.0.1,::1,af-cp-internal.af-cp.svc" {
			t.Errorf("%s = %q", k, env[k])
		}
	}
	// No proxy, nothing to bypass: NO_PROXY is left as the deployment set it.
	f.cfg.templateEnv = nil
	rt = f.New(Workspace{ContainerName: "af-ws-x"}, SecretKeys{}, []string{"AF_CP_INTERNAL_URL=http://af-cp-internal.af-cp.svc:8098"}).(*kubeRuntime)
	if env := rt.secretEnv(); env["NO_PROXY"] != "" || env["no_proxy"] != "" {
		t.Errorf("NO_PROXY set without a proxy: %v", env)
	}
	// A proxy without NO_PROXY gets one.
	f.cfg.templateEnv = []string{"HTTP_PROXY=http://cp:3128"}
	rt = f.New(Workspace{ContainerName: "af-ws-x"}, SecretKeys{}, []string{"AF_CP_INTERNAL_URL=http://af-cp-internal.af-cp.svc:8098"}).(*kubeRuntime)
	if env := rt.secretEnv(); env["NO_PROXY"] != "af-cp-internal.af-cp.svc" {
		t.Errorf("NO_PROXY = %q", env["NO_PROXY"])
	}
}

// A Start whose conditional write is refused (the generation or replicas moved since it
// checked: 422 from a failed JSON Patch test, or 409) returns the error and writes
// nothing else to the StatefulSet.
func TestKubeStartRefusedWriteIsAnError(t *testing.T) {
	for _, code := range []int{422, 409} {
		rt, f := fakeKubeRuntime(t)
		f.set(stsPathX, 200, stsJSON(0, 6, 6, 0, "r", "2"))
		f.set(podsPathX, 200, podListJSON())
		f.set("GET /api/v1/namespaces/ns/persistentvolumeclaims/af-ws-x-home", 200, `{"metadata":{"name":"af-ws-x-home"},"spec":{"resources":{}}}`)
		f.set("GET /api/v1/namespaces/ns/persistentvolumeclaims/af-ws-x-state", 200, `{"metadata":{"name":"af-ws-x-state"},"spec":{"resources":{}}}`)
		f.set("GET /api/v1/namespaces/ns/services/af-ws-x", 200, `{"metadata":{"name":"af-ws-x"},"spec":{}}`)
		f.set("PUT /api/v1/namespaces/ns/secrets/af-ws-x-env", 200, `{}`)
		f.set("PATCH /apis/apps/v1/namespaces/ns/statefulsets/af-ws-x", code,
			`{"kind":"Status","status":"Failure","message":"the server rejected our request due to an error in our request","reason":"Invalid","code":`+itoa(code)+`}`)
		err := rt.Start(context.Background())
		if kubeErrCode(err) != code {
			t.Fatalf("Start with the write refused (%d) = %v, want that error", code, err)
		}
		n := 0
		for _, s := range f.seen {
			if strings.HasPrefix(s, "PATCH ") || strings.HasPrefix(s, "PUT /apis") || strings.HasPrefix(s, "POST /apis") {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("Start sent %d writes to the StatefulSet, want the one refused patch: %v", n, f.seen)
		}
	}
}
