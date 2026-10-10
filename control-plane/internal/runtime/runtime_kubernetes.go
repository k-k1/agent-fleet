// runtime_kubernetes.go — the `kubernetes` Runtime adapter (alias `k8s`, ADR 0106).
//
// A workspace is a StatefulSet at 0 or 1 replicas with its own ClusterIP Service, a
// Secret holding the whole per-start environment, and two PersistentVolumeClaims (the
// home, and the state that survives a home reset). Every object is found by a
// deterministic name and the label agent-fleet.io/workspace, so a restarted or second CP
// recovers everything from the cluster; the adapter keeps nothing but the boot phase in
// the process (BootPhase).
//
// The state machine is ADR 0106 decision 3, and three rules carry it:
//
//   - Stop returns only when the stop has settled: the controller has observed the
//     generation Stop wrote, reports no replicas, and no pod of the workspace exists. A
//     controller that read replicas 1 just before the Stop can still be creating a pod,
//     which would appear after a check that saw none.
//   - Start requires that same settled stop (or no StatefulSet), and then writes the pod
//     template and replicas 1 in one update, so no container of an earlier start can read
//     the Secret it has just rewritten.
//   - `stopped` is replicas 0, whether or not a pod is still terminating. A stop in
//     progress must never read as `starting`, because the CP's start handler returns
//     early on `starting` and the Start of a Recreate would be dropped
//     (workspace_handlers.go). So `stopped` does not prove that nothing runs, and the
//     adapter's own destructive operations check the settled stop themselves.
//
// The CP never deletes a workspace pod, and never force-deletes any pod: that frees the
// name without proof that the process is dead, and could run two agents on one home.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	kubeAgentPort      int32 = 7700
	kubeAgentName            = "agent" // the workspace container's name
	kubeLabelWorkspace       = "agent-fleet.io/workspace"
	// kubeLabelRole separates the StatefulSet's own pods from the one-shot pods the
	// adapter runs against a workspace's claims (the erase pod of ADR 0106 decision 4):
	// the settled-stop check and State look at role=workspace only.
	kubeLabelRole     = "agent-fleet.io/role"
	kubeRoleWorkspace = "workspace"

	// kubeAnnStartGen is the start generation, on the pod template. Every Start raises
	// it, so every start is a new controller revision even when nothing else changed,
	// and State can tell the pod of this start from a pod of an earlier one.
	kubeAnnStartGen = "agent-fleet.io/start-generation"
	// kubeAnnStartedAt is when the Start that wrote the template ran (RFC 3339). The boot
	// phase ignores events older than it.
	kubeAnnStartedAt = "agent-fleet.io/started-at"
	// kubeAnnWorkspace is the CP's own name for the workspace, which may be longer than
	// a Kubernetes name allows (kubeObjectName).
	kubeAnnWorkspace = "agent-fleet.io/workspace-name"
	// kubeAnnImage is the image as configured, before Start pinned its digest.
	kubeAnnImage = "agent-fleet.io/image"
	// kubeAnnImageFingerprint is the content fingerprint of the image this start
	// launched, on the template, for Stale (runtime_kubernetes_stale.go).
	kubeAnnImageFingerprint = "agent-fleet.io/image-fingerprint"

	kubeHomePath  = "/home/dev"
	kubeStatePath = "/var/lib/af/claude"
	kubeKeepPath  = "/var/lib/af/keep"
	kubeDevUID    = 1000 // the image's dev user and group (workspace/Dockerfile)

	// The node-disk bounds of a workspace pod (ADR 0106 decision 7): /tmp is an emptyDir
	// on the node's disk, and the container's writable layer and logs share it. The home
	// and the state are claims and do not count against either.
	kubeTmpSizeLimit     = "8Gi"
	kubeEphemeralRequest = "1Gi"
	kubeEphemeralLimit   = "16Gi"
	kubeDefaultHomeGiB   = 50
	kubeDefaultStateGiB  = 5
	kubeStopMargin       = 15 * time.Second
)

// kubeConfig is the deployment-wide placement, read once at boot from AF_K8S_*.
type kubeConfig struct {
	namespace      string
	image          string // AF_K8S_WORKSPACE_IMAGE: a tag; Start pins the digest
	storageClass   string // "" = the cluster's default class
	homeGiB        int
	stateGiB       int
	pullSecret     string
	nodeSelector   map[string]string
	serviceAccount string
	sessionCmd     string
	// templateEnv is Config.ExtraEnv: WS_ENV and the egress proxy variables. Unlike the
	// ECS adapters this one passes it on, as docker and native do — the egress proxy is
	// the reason — and it travels in the Secret with the rest of the environment, since
	// an operator's WS_ENV may hold a credential.
	templateEnv     []string
	defaultMemBytes int64 // Config.Memory (WS_MEMORY) in bytes; 0 = no default
}

// imageResolver reads an image reference's digest and content fingerprint
// (runtime_kubernetes_registry.go).
type imageResolver interface {
	resolve(ctx context.Context, image string) (resolvedImage, error)
}

type kubeFactory struct {
	cfg  *kubeConfig
	c    *kubeClient
	pins imageResolver
	// poll is how often Stop and Destroy re-read the cluster while they wait.
	poll time.Duration
	// stopMargin is what Stop waits beyond the stop grace before it gives up.
	stopMargin time.Duration
}

var (
	_ RuntimeFactory = (*kubeFactory)(nil)
	_ Runtime        = (*kubeRuntime)(nil)
	_ TaskCounter    = (*kubeRuntime)(nil)
)

func newKubeFactory(mcfg Config) (RuntimeFactory, error) {
	cfg, err := kubeConfigFromEnv(envOr, mcfg)
	if err != nil {
		return nil, err
	}
	c, err := newInClusterKubeClient()
	if err != nil {
		return nil, err
	}
	log.Printf("runtime=kubernetes namespace=%s image=%s storageClass=%q", cfg.namespace, cfg.image, cfg.storageClass)
	logStorageClassCheck(c, cfg.storageClass)
	return &kubeFactory{
		cfg:        cfg,
		c:          c,
		pins:       newRegistryClient(pullSecretCreds(c, cfg.namespace, cfg.pullSecret)),
		poll:       time.Second,
		stopMargin: kubeStopMargin,
	}, nil
}

// kubeConfigFromEnv reads AF_K8S_* through get (envOr in production). Malformed values
// fail the boot: a node selector or size that silently fell back to a default would
// place workspaces where the operator said they must not go.
func kubeConfigFromEnv(get func(k, def string) string, mcfg Config) (*kubeConfig, error) {
	cfg := &kubeConfig{
		namespace:      get("AF_K8S_NAMESPACE", ""),
		image:          get("AF_K8S_WORKSPACE_IMAGE", mcfg.Image),
		storageClass:   get("AF_K8S_STORAGE_CLASS", ""),
		pullSecret:     get("AF_K8S_IMAGE_PULL_SECRET", ""),
		serviceAccount: get("AF_K8S_SERVICE_ACCOUNT", "default"),
		sessionCmd:     mcfg.SessionCmd,
		templateEnv:    append([]string(nil), mcfg.ExtraEnv...),
	}
	if cfg.namespace == "" {
		return nil, errors.New("kubernetes runtime: AF_K8S_NAMESPACE is required")
	}
	if cfg.image == "" {
		return nil, errors.New("kubernetes runtime: AF_K8S_WORKSPACE_IMAGE is required")
	}
	if _, err := parseImageReference(cfg.image); err != nil {
		return nil, fmt.Errorf("kubernetes runtime: AF_K8S_WORKSPACE_IMAGE: %w", err)
	}
	var err error
	if cfg.homeGiB, err = positiveInt(get("AF_K8S_HOME_GIB", ""), kubeDefaultHomeGiB); err != nil {
		return nil, fmt.Errorf("kubernetes runtime: AF_K8S_HOME_GIB: %w", err)
	}
	if cfg.stateGiB, err = positiveInt(get("AF_K8S_STATE_GIB", ""), kubeDefaultStateGiB); err != nil {
		return nil, fmt.Errorf("kubernetes runtime: AF_K8S_STATE_GIB: %w", err)
	}
	if cfg.nodeSelector, err = parseNodeSelector(get("AF_K8S_NODE_SELECTOR", "")); err != nil {
		return nil, fmt.Errorf("kubernetes runtime: AF_K8S_NODE_SELECTOR: %w", err)
	}
	if mcfg.Memory != "" {
		if cfg.defaultMemBytes, err = parseDockerBytes(mcfg.Memory); err != nil {
			return nil, fmt.Errorf("kubernetes runtime: WS_MEMORY: %w", err)
		}
	}
	return cfg, nil
}

func positiveInt(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("want a positive integer, got %q", s)
	}
	return n, nil
}

// parseNodeSelector reads key=value[,key=value].
func parseNodeSelector(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, kv := range splitCSV(s) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("want key=value, got %q", kv)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

// parseDockerBytes reads a docker --memory value ("4g", "512m", "1073741824").
func parseDockerBytes(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "g"):
		mult, s = gib, strings.TrimSuffix(s, "g")
	case strings.HasSuffix(s, "m"):
		mult, s = mib, strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "k"):
		mult, s = kib, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "b"):
		s = strings.TrimSuffix(s, "b")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("want a byte count like 4g, got %q", s)
	}
	return n * mult, nil
}

func (f *kubeFactory) New(ws Workspace, keys SecretKeys, extraEnv []string) Runtime {
	mem := f.cfg.defaultMemBytes
	if ws.MemBytes > 0 {
		mem = ws.MemBytes
	}
	var cpuMilli int64
	if ws.CPUUnits > 0 {
		// Carried in Fargate units (1024 = one vCPU), as on every adapter.
		cpuMilli = max(int64(ws.CPUUnits)*1000/1024, 1)
	}
	home := f.cfg.homeGiB
	if ws.DiskGB > 0 {
		home = ws.DiskGB
	}
	return &kubeRuntime{
		cfg:        f.cfg,
		c:          f.c,
		pins:       f.pins,
		poll:       f.poll,
		stopMargin: f.stopMargin,
		wsName:     ws.ContainerName,
		base:       kubeObjectName(ws.ContainerName),
		token:      ws.AgentToken,
		keys:       keys,
		extraEnv:   append([]string(nil), extraEnv...),
		memBytes:   mem,
		cpuMilli:   cpuMilli,
		homeGiB:    home,
	}
}

// WorkspaceImage is the configured image, for the banner and version info.
func (f *kubeFactory) WorkspaceImage() string { return f.cfg.image }

// SizingProfile — CPU and memory are requests and limits of the pod; the disk axis is
// the home claim, which can grow and never shrink.
func (f *kubeFactory) SizingProfile() WorkspaceSizing {
	return WorkspaceSizing{
		Runtime: "kubernetes", CPUEffective: true,
		MemMeaning: MemMeaningLimit, DiskMeaning: DiskMeaningHome,
		DiskDefaultGB: f.cfg.homeGiB, DiskGrowOnly: true,
	}
}

// CostProfile — no bill is read on this profile. It is claimed anyway so that version
// info names the runtime instead of falling back to `local` (cost_profile.go).
func (f *kubeFactory) CostProfile() CostProfile { return CostProfile{Runtime: "kubernetes"} }

// kubeObjectName maps the CP's workspace name onto a Kubernetes name. The CP's name can
// reach 87 characters (af-ws-<slug>-<key>, manager.go), while a pod's hostname and the
// controller-revision-hash label (<statefulset>-<10 characters>) must stay within 63, and
// the adapter adds suffixes of up to six to the base. A long name keeps a readable
// prefix and gains a hash of the whole, so two names that share the prefix stay apart.
func kubeObjectName(name string) string {
	const max = 40
	name = strings.ToLower(name)
	if len(name) <= max {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return strings.TrimRight(name[:max-9], "-") + "-" + hex.EncodeToString(sum[:])[:8]
}

// kubeRuntime is one workspace on the kubernetes profile.
type kubeRuntime struct {
	cfg        *kubeConfig
	c          *kubeClient
	pins       imageResolver
	wsName     string // the CP's name for the workspace (Workspace.ContainerName)
	base       string // the Kubernetes name of the StatefulSet and the Service
	token      string
	keys       SecretKeys
	extraEnv   []string
	memBytes   int64
	cpuMilli   int64
	homeGiB    int
	poll       time.Duration
	stopMargin time.Duration
	// editTemplate changes the pod template before Start writes it (tests only: a
	// template the namespace's Pod Security level refuses, for the boot phase).
	editTemplate func(*kPodTemplateSpec)
}

func (k *kubeRuntime) Name() string  { return k.wsName }
func (k *kubeRuntime) Token() string { return k.token }

// BrowserUnavailable: the restricted pod leaves Chromium neither a setuid sandbox nor user
// namespaces (browser_support.go).
func (k *kubeRuntime) BrowserUnavailable() string { return "kubernetes" }

// Endpoint is the Service's cluster DNS name. `<svc>.<ns>.svc` resolves through the
// pod's search path whatever the cluster domain is, and unlike a Service Connect alias it
// resolves for a Service created after the CP started.
func (k *kubeRuntime) Endpoint() string {
	return fmt.Sprintf("http://%s.%s.svc:%d", k.base, k.cfg.namespace, kubeAgentPort)
}

func (k *kubeRuntime) secretName() string { return k.base + "-env" }
func (k *kubeRuntime) homeClaim() string  { return k.base + "-home" }
func (k *kubeRuntime) stateClaim() string { return k.base + "-state" }

func (k *kubeRuntime) nsPath(group string) string {
	if group == "" {
		return "/api/v1/namespaces/" + k.cfg.namespace
	}
	return "/apis/" + group + "/namespaces/" + k.cfg.namespace
}

func (k *kubeRuntime) stsPath() string { return k.nsPath("apps/v1") + "/statefulsets/" + k.base }

func (k *kubeRuntime) podLabels() map[string]string {
	return map[string]string{kubeLabelWorkspace: k.base, kubeLabelRole: kubeRoleWorkspace}
}

func (k *kubeRuntime) objectMeta(name string) kObjectMeta {
	return kObjectMeta{
		Name:        name,
		Namespace:   k.cfg.namespace,
		Labels:      map[string]string{kubeLabelWorkspace: k.base},
		Annotations: map[string]string{kubeAnnWorkspace: k.wsName},
	}
}

// getStatefulSet returns nil, nil when there is none.
func (k *kubeRuntime) getStatefulSet(ctx context.Context) (*kStatefulSet, error) {
	var s kStatefulSet
	if err := k.c.get(ctx, k.stsPath(), &s); err != nil {
		if isKubeNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// workspacePods lists the StatefulSet's own pods, never the one-shot pods that share
// the workspace label.
func (k *kubeRuntime) workspacePods(ctx context.Context) ([]kPod, error) {
	var l kPodList
	q := url.Values{"labelSelector": {kubeLabelWorkspace + "=" + k.base + "," + kubeLabelRole + "=" + kubeRoleWorkspace}}
	if err := k.c.list(ctx, k.nsPath("")+"/pods", q, &l); err != nil {
		return nil, err
	}
	return l.Items, nil
}

func stsReplicas(s *kStatefulSet) int32 {
	if s.Spec.Replicas == nil {
		return 1 // the API's default
	}
	return *s.Spec.Replicas
}

// stopSettled is the settled stop of ADR 0106 decision 3, checked in its order. why
// names the first condition that does not hold, for the error a caller shows.
func (k *kubeRuntime) stopSettled(ctx context.Context, s *kStatefulSet) (ok bool, why string, err error) {
	if stsReplicas(s) != 0 {
		return false, "replicas is " + strconv.Itoa(int(stsReplicas(s))), nil
	}
	if s.Status.ObservedGeneration < s.Metadata.Generation {
		return false, fmt.Sprintf("the controller has observed generation %d of %d", s.Status.ObservedGeneration, s.Metadata.Generation), nil
	}
	if s.Status.Replicas != 0 {
		return false, fmt.Sprintf("the controller reports %d replicas", s.Status.Replicas), nil
	}
	pods, err := k.workspacePods(ctx)
	if err != nil {
		return false, "", err
	}
	if len(pods) > 0 {
		p := pods[0]
		where := ""
		if p.Spec.NodeName != "" {
			where = " on node " + p.Spec.NodeName
		}
		return false, fmt.Sprintf("pod %s still exists%s", p.Metadata.Name, where), nil
	}
	return true, "", nil
}

// --- State ---

// State reads the cluster on every call (ADR 0106 decision 3's table):
//
//	none     — no StatefulSet
//	running  — replicas 1, the controller has observed the latest generation, and a pod
//	           of status.updateRevision carrying the template's start generation, not
//	           being deleted, is Ready (the readiness probe is the agent's /healthz)
//	starting — replicas 1 and `running` does not hold
//	stopped  — replicas 0, whether or not a pod is still terminating
//
// An unreadable cluster reads as `none`, as on the ECS adapters: the Start that may
// follow reads the cluster itself and fails there rather than launching blind.
func (k *kubeRuntime) State(ctx context.Context) string {
	s, err := k.getStatefulSet(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("kubernetes state: %s: %v", k.base, err)
		}
		return "none"
	}
	if s == nil {
		k.setPhase("")
		return "none"
	}
	if stsReplicas(s) == 0 {
		k.setPhase("")
		return "stopped"
	}
	pods, err := k.workspacePods(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("kubernetes state: list pods of %s: %v", k.base, err)
		}
		return "starting"
	}
	if s.Status.ObservedGeneration >= s.Metadata.Generation {
		for i := range pods {
			if podServesThisStart(s, &pods[i]) && podReady(&pods[i]) {
				k.setPhase("")
				return "running"
			}
		}
	}
	k.notePhase(ctx, s, pods)
	return "starting"
}

// podServesThisStart is the half of `running` that keeps an old agent from passing for
// the new one: right after Start writes the template the controller may not have
// processed it, and status.updateRevision still names the old revision.
func podServesThisStart(s *kStatefulSet, p *kPod) bool {
	if p.Metadata.DeletionTimestamp != nil {
		return false
	}
	if s.Status.UpdateRevision == "" || p.Metadata.Labels["controller-revision-hash"] != s.Status.UpdateRevision {
		return false
	}
	want := s.Spec.Template.Metadata.Annotations[kubeAnnStartGen]
	return want != "" && p.Metadata.Annotations[kubeAnnStartGen] == want
}

func podReady(p *kPod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

// RunningTasks satisfies TaskCounter: pods whose workspace container is running,
// whether or not they are Ready. A running agent that has stopped answering is still a
// task, and the CP's start deadline must not stop it; an unreadable answer is an error,
// which the deadline treats as running.
func (k *kubeRuntime) RunningTasks(ctx context.Context) (int, error) {
	pods, err := k.workspacePods(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range pods {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name == kubeAgentName && cs.State.Running != nil {
				n++
			}
		}
	}
	return n, nil
}

// --- Stop ---

// Stop scales the StatefulSet to 0 and returns once the stop has settled, or with an
// error after the stop grace plus kubeStopMargin. The pod receives SIGTERM, the agent
// winds its panes down within AGENT_STOP_GRACE_SEC, and the kubelet kills what is left at
// terminationGracePeriodSeconds — the same two stages as docker stop.
//
// A pod on a node that stops answering never goes away by itself, and this returns an
// error for as long as it is there: recovering it is the operator's (ADR 0106 decision 3).
func (k *kubeRuntime) Stop(ctx context.Context) error {
	s, err := k.getStatefulSet(ctx)
	if err != nil {
		return fmt.Errorf("kubernetes stop %s: %w", k.base, err)
	}
	if s == nil {
		return nil
	}
	k.setPhase("")
	if stsReplicas(s) != 0 {
		var out kStatefulSet
		if err := k.c.jsonPatch(ctx, k.stsPath(), []kubePatchOp{
			{Op: "replace", Path: "/spec/replicas", Value: 0},
		}, &out); err != nil {
			if isKubeNotFound(err) {
				return nil
			}
			return fmt.Errorf("kubernetes stop %s: %w", k.base, err)
		}
		s = &out
	}
	budget := time.Duration(stopGraceSec())*time.Second + k.stopMargin
	return k.waitSettled(ctx, s, budget)
}

// waitSettled polls until the stop of s has settled. The generation it waits for is the
// one s carries, which is the generation of the write that set replicas 0.
func (k *kubeRuntime) waitSettled(ctx context.Context, s *kStatefulSet, budget time.Duration) error {
	want := s.Metadata.Generation
	deadline := time.Now().Add(budget)
	for {
		cur, err := k.getStatefulSet(ctx)
		if err != nil {
			return fmt.Errorf("kubernetes stop %s: %w", k.base, err)
		}
		if cur == nil {
			return nil
		}
		if cur.Metadata.Generation > want && stsReplicas(cur) != 0 {
			// Someone scaled it up again behind this Stop; that is a lifecycle race the
			// CP's lease should have prevented, and waiting on would never end.
			return fmt.Errorf("kubernetes stop %s: the StatefulSet was scaled up again (generation %d) while stopping", k.base, cur.Metadata.Generation)
		}
		ok, why, err := k.stopSettled(ctx, cur)
		if err != nil {
			return fmt.Errorf("kubernetes stop %s: %w", k.base, err)
		}
		if ok && cur.Status.ObservedGeneration >= want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("kubernetes stop %s: not settled after %s: %s", k.base, budget, why)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("kubernetes stop %s: %w (%s)", k.base, ctx.Err(), why)
		case <-time.After(k.poll):
		}
	}
}

// --- Start ---

// Start requires the settled stop of the previous run (or no StatefulSet), creates the
// claims and the Service when they are missing, rewrites the Secret, pins the image and
// then writes the template and replicas 1 in one update. It does not wait for the agent:
// State follows the convergence, as on the ECS adapters (21 §21.2).
//
// A StatefulSet already at replicas 1 is a launch that is under way or done, and Start
// leaves it alone, as every adapter does for `running` and `starting` — the Secret in
// particular is not rewritten under a pod that may read it.
func (k *kubeRuntime) Start(ctx context.Context) error {
	s, err := k.getStatefulSet(ctx)
	if err != nil {
		return fmt.Errorf("kubernetes start %s: %w", k.base, err)
	}
	if s != nil {
		if stsReplicas(s) != 0 {
			return nil
		}
		ok, why, err := k.stopSettled(ctx, s)
		if err != nil {
			return fmt.Errorf("kubernetes start %s: %w", k.base, err)
		}
		if !ok {
			return fmt.Errorf("kubernetes start %s: the previous stop has not settled (%s); start again once it has", k.base, why)
		}
	}
	// An erase pod holds the home claim: a running one is an administrator's Clean home
	// in progress, and a finished one is left over from a CP that died before removing it.
	if err := k.clearFinishedErasePod(ctx); err != nil {
		return fmt.Errorf("kubernetes start %s: %w", k.base, err)
	}
	if err := k.ensureClaim(ctx, k.homeClaim(), k.homeGiB); err != nil {
		return err
	}
	if err := k.ensureClaim(ctx, k.stateClaim(), k.cfg.stateGiB); err != nil {
		return err
	}
	if err := k.ensureService(ctx); err != nil {
		return err
	}
	if err := k.writeSecret(ctx); err != nil {
		return err
	}
	img, err := k.pins.resolve(ctx, k.cfg.image)
	if err != nil {
		return fmt.Errorf("kubernetes start %s: %w", k.base, err)
	}
	gen := int64(1)
	if s != nil {
		if prev, err := strconv.ParseInt(s.Spec.Template.Metadata.Annotations[kubeAnnStartGen], 10, 64); err == nil {
			gen = prev + 1
		}
	}
	tmpl := k.podTemplate(img.pinned, gen, time.Now().UTC())
	if img.fingerprint != "" {
		tmpl.Metadata.Annotations[kubeAnnImageFingerprint] = img.fingerprint
	}
	if s != nil {
		k.addHomeWipe(&tmpl, s.Metadata.Annotations)
	}
	if k.editTemplate != nil {
		k.editTemplate(&tmpl)
	}
	if s == nil {
		one := int32(1)
		obj := k.statefulSet(tmpl, one)
		if err := k.c.create(ctx, k.nsPath("apps/v1")+"/statefulsets", obj, nil); err != nil {
			return fmt.Errorf("kubernetes start %s: create statefulset: %w", k.base, err)
		}
		k.setPhase("")
		k.primeStale(img.fingerprint)
		return nil
	}
	// The two tests make the write conditional on the StatefulSet this Start checked: a
	// spec written by anyone since (another Start, a Stop, an operator) changes the
	// generation, and the write is refused instead of launching over it. The resource
	// version is not tested: the controller's status writes move it on their own.
	if err := k.c.jsonPatch(ctx, k.stsPath(), []kubePatchOp{
		{Op: "test", Path: "/metadata/generation", Value: s.Metadata.Generation},
		{Op: "test", Path: "/spec/replicas", Value: 0},
		{Op: "replace", Path: "/spec/template", Value: tmpl},
		{Op: "replace", Path: "/spec/replicas", Value: 1},
	}, nil); err != nil {
		return fmt.Errorf("kubernetes start %s: update statefulset: %w", k.base, err)
	}
	k.setPhase("")
	k.primeStale(img.fingerprint)
	return nil
}

func (k *kubeRuntime) ensureClaim(ctx context.Context, name string, gib int) error {
	path := k.nsPath("") + "/persistentvolumeclaims"
	var got kPVC
	err := k.c.get(ctx, path+"/"+name, &got)
	if err == nil {
		return nil
	}
	if !isKubeNotFound(err) {
		return fmt.Errorf("kubernetes start %s: claim %s: %w", k.base, name, err)
	}
	pvc := kPVC{
		APIVersion: "v1", Kind: "PersistentVolumeClaim",
		Metadata: k.objectMeta(name),
		Spec: kPVCSpec{
			// ReadWriteOncePod is preferred where the CSI driver supports it (ADR 0106
			// decision 4) and is confirmed per cluster in the live acceptance; until then
			// the claim asks for what every block driver provides.
			AccessModes: []string{"ReadWriteOnce"},
			Resources:   kResources{Requests: map[string]string{"storage": strconv.Itoa(gib) + "Gi"}},
		},
	}
	if k.cfg.storageClass != "" {
		sc := k.cfg.storageClass
		pvc.Spec.StorageClassName = &sc
	}
	if err := k.c.create(ctx, path, pvc, nil); err != nil && !isKubeConflict(err) {
		return fmt.Errorf("kubernetes start %s: create claim %s: %w", k.base, name, err)
	}
	return nil
}

func (k *kubeRuntime) ensureService(ctx context.Context) error {
	path := k.nsPath("") + "/services"
	var got kService
	err := k.c.get(ctx, path+"/"+k.base, &got)
	if err == nil {
		return nil
	}
	if !isKubeNotFound(err) {
		return fmt.Errorf("kubernetes start %s: service: %w", k.base, err)
	}
	svc := kService{
		APIVersion: "v1", Kind: "Service",
		Metadata: k.objectMeta(k.base),
		Spec: kServiceSpec{
			Type:     "ClusterIP",
			Selector: k.podLabels(),
			Ports:    []kServicePort{{Name: "agent", Port: kubeAgentPort, TargetPort: kubeAgentPort, Protocol: "TCP"}},
		},
	}
	if err := k.c.create(ctx, path, svc, nil); err != nil && !isKubeConflict(err) {
		return fmt.Errorf("kubernetes start %s: create service: %w", k.base, err)
	}
	return nil
}

// secretEnv is the whole per-start environment: the deployment's template env, the
// start's own variables (the minted tokens among them), then AGENT_TOKEN and the DEK. A
// later entry wins, the order docker gives its -e flags.
func (k *kubeRuntime) secretEnv() map[string]string {
	env := map[string]string{}
	for _, list := range [][]string{k.cfg.templateEnv, k.extraEnv} {
		for _, kv := range list {
			if key, v, ok := strings.Cut(kv, "="); ok && key != "" {
				env[key] = v
			}
		}
	}
	bypassProxyForCP(env)
	if k.token != "" {
		env["AGENT_TOKEN"] = k.token
	}
	for _, kv := range k.keys.envPairs() {
		env[kv[0]] = kv[1]
	}
	return env
}

// bypassProxyForCP adds the host of AF_CP_INTERNAL_URL to NO_PROXY when the environment
// routes egress through the proxy (ADR 0106 decision 8): the workspace reaches the CP's
// internal listener inside the cluster, and the proxy, which runs in the CP, is neither
// the way there nor a place for those bearer tokens to pass. Both spellings are kept in
// step, as main.go sets both.
func bypassProxyForCP(env map[string]string) {
	internal := env["AF_CP_INTERNAL_URL"]
	if internal == "" {
		return
	}
	proxied := false
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if env[k] != "" {
			proxied = true
		}
	}
	u, err := url.Parse(internal)
	if !proxied || err != nil || u.Hostname() == "" {
		return
	}
	host := u.Hostname()
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		cur := env[k]
		if slices.Contains(splitCSV(cur), host) {
			continue
		}
		if cur == "" {
			env[k] = host
		} else {
			env[k] = cur + "," + host
		}
	}
}

// writeSecret replaces the workspace's Secret whole. Start reaches this only after the
// settled stop, so no pod exists that could read a mix of two starts' values.
func (k *kubeRuntime) writeSecret(ctx context.Context) error {
	path := k.nsPath("") + "/secrets"
	sec := kSecret{
		APIVersion: "v1", Kind: "Secret",
		Metadata:   k.objectMeta(k.secretName()),
		Type:       "Opaque",
		StringData: k.secretEnv(),
	}
	err := k.c.replace(ctx, path+"/"+k.secretName(), sec, nil)
	if isKubeNotFound(err) {
		err = k.c.create(ctx, path, sec, nil)
	}
	if err != nil {
		return fmt.Errorf("kubernetes start %s: write secret: %w", k.base, err)
	}
	return nil
}

func (k *kubeRuntime) statefulSet(tmpl kPodTemplateSpec, replicas int32) kStatefulSet {
	history := int32(2)
	return kStatefulSet{
		APIVersion: "apps/v1", Kind: "StatefulSet",
		Metadata: k.objectMeta(k.base),
		Spec: kStatefulSetSpec{
			Replicas:             &replicas,
			Selector:             &kLabelSelector{MatchLabels: k.podLabels()},
			ServiceName:          k.base,
			Template:             tmpl,
			RevisionHistoryLimit: &history,
		},
	}
}

// podTemplate is the workspace pod. It passes the `restricted` Pod Security Standard
// (ADR 0106 decision 7): non-root as the image's dev user, no privilege escalation, every
// capability dropped, the runtime's default seccomp profile, nothing from the host. It
// carries no secret: the environment that varies per start is in the Secret, read
// through envFrom.
func (k *kubeRuntime) podTemplate(image string, gen int64, now time.Time) kPodTemplateSpec {
	t, f := true, false
	uid := int64(kubeDevUID)
	grace := int64(stopGraceSec())
	// The fixed settings stay on the template. `env` overrides envFrom, so the start's
	// variables cannot move the keep path or the Claude state off their volumes.
	env := []kEnvVar{
		{Name: "CLAUDE_CONFIG_DIR", Value: kubeStatePath},
		{Name: "AF_WS_KEEP", Value: kubeKeepPath},
		{Name: "AGENT_STOP_GRACE_SEC", Value: strconv.Itoa(agentStopGraceSec())},
		{Name: BrowserUnavailableEnv, Value: k.BrowserUnavailable()},
	}
	if k.cfg.sessionCmd != "" {
		env = append(env, kEnvVar{Name: "AGENT_SESSION_CMD", Value: k.cfg.sessionCmd})
	}
	res := kResources{
		Requests: map[string]string{"ephemeral-storage": kubeEphemeralRequest},
		Limits:   map[string]string{"ephemeral-storage": kubeEphemeralLimit},
	}
	if k.memBytes > 0 {
		m := strconv.FormatInt(k.memBytes, 10)
		res.Requests["memory"], res.Limits["memory"] = m, m
	}
	if k.cpuMilli > 0 {
		c := strconv.FormatInt(k.cpuMilli, 10) + "m"
		res.Requests["cpu"], res.Limits["cpu"] = c, c
	}
	spec := kPodSpec{
		// The pause container becomes PID 1 and reaps orphans, as tini does under docker:
		// the image has no init, and without one every CLI and tmux exit leaves a zombie.
		ShareProcessNamespace:         &t,
		AutomountServiceAccountToken:  &f,
		ServiceAccountName:            k.cfg.serviceAccount,
		EnableServiceLinks:            &f, // one variable set per Service in the namespace otherwise
		TerminationGracePeriodSeconds: &grace,
		SecurityContext: &kPodSecurityContext{
			RunAsNonRoot: &t, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid,
			// A home holds hundreds of thousands of files; the default policy would chown
			// all of them at every mount.
			FSGroupChangePolicy: "OnRootMismatch",
			SeccompProfile:      &kSeccompProfile{Type: "RuntimeDefault"},
		},
		NodeSelector: k.cfg.nodeSelector,
		Containers: []kContainer{{
			Name:            kubeAgentName,
			Image:           image,
			ImagePullPolicy: "IfNotPresent", // pinned by digest, so a cached copy is the same image
			Ports:           []kContainerPort{{Name: "agent", ContainerPort: kubeAgentPort, Protocol: "TCP"}},
			Env:             env,
			EnvFrom:         []kEnvFromSource{{SecretRef: &kSecretEnvSource{Name: k.secretName()}}},
			Resources:       res,
			VolumeMounts: []kVolumeMount{
				{Name: "home", MountPath: kubeHomePath, SubPath: kubeHomeSubPath},
				{Name: "state", MountPath: kubeStatePath, SubPath: "claude"},
				{Name: "state", MountPath: kubeKeepPath, SubPath: "keep"},
				{Name: "tmp", MountPath: "/tmp"},
			},
			// Ready means what `running` means on the other targets: the agent answers.
			// There is no liveness probe on purpose — an agent that stopped answering is
			// still a session, and killing it is not this adapter's call.
			ReadinessProbe: &kProbe{
				HTTPGet:       &kHTTPGetAction{Path: "/healthz", Port: kubeAgentPort},
				PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 3,
			},
			SecurityContext: &kContainerSecurityContext{
				AllowPrivilegeEscalation: &f,
				Privileged:               &f,
				RunAsNonRoot:             &t,
				Capabilities:             &kCapabilities{Drop: []string{"ALL"}},
				SeccompProfile:           &kSeccompProfile{Type: "RuntimeDefault"},
			},
		}},
		Volumes: []kVolume{
			{Name: "home", PersistentVolumeClaim: &kPVCVolumeSource{ClaimName: k.homeClaim()}},
			{Name: "state", PersistentVolumeClaim: &kPVCVolumeSource{ClaimName: k.stateClaim()}},
			{Name: "tmp", EmptyDir: &kEmptyDirVolumeSource{SizeLimit: kubeTmpSizeLimit}},
		},
	}
	spec.InitContainers = []kContainer{homeLayoutContainer(spec.Containers[0])}
	if k.cfg.pullSecret != "" {
		spec.ImagePullSecrets = []kLocalObjectRef{{Name: k.cfg.pullSecret}}
	}
	return kPodTemplateSpec{
		Metadata: kObjectMeta{
			Labels: k.podLabels(),
			Annotations: map[string]string{
				kubeAnnStartGen:  strconv.FormatInt(gen, 10),
				kubeAnnStartedAt: now.Format(time.RFC3339),
				kubeAnnWorkspace: k.wsName,
				kubeAnnImage:     k.cfg.image,
				// The autoscaler must not evict a live session to shrink the pool; the
				// pool shrinks as workspaces stop (ADR 0106 decision 13).
				"cluster-autoscaler.kubernetes.io/safe-to-evict": "false",
			},
		},
		Spec: spec,
	}
}

// --- boot phase ---

// kubePhase holds the latest boot phase per workspace, written by State while it reports
// `starting` and read by BootPhase, which takes no context and cannot call the cluster.
// Process-local by design: another CP computes its own on its next State.
var kubePhase sync.Map // namespace/base -> string

func (k *kubeRuntime) phaseKey() string { return k.cfg.namespace + "/" + k.base }

func (k *kubeRuntime) setPhase(p string) {
	if p == "" {
		kubePhase.Delete(k.phaseKey())
		return
	}
	kubePhase.Store(k.phaseKey(), p)
}

// BootPhase satisfies the optional interface GET /api/workspace probes for.
func (k *kubeRuntime) BootPhase() string {
	if v, ok := kubePhase.Load(k.phaseKey()); ok {
		s, _ := v.(string)
		return s
	}
	return ""
}

// kubeBlockedReasons are the container waiting reasons that do not resolve by waiting.
var kubeBlockedReasons = map[string]bool{
	"ErrImagePull": true, "ImagePullBackOff": true, "InvalidImageName": true,
	"CreateContainerConfigError": true, "CreateContainerError": true,
	"CrashLoopBackOff": true, "RunContainerError": true,
}

// notePhase derives the boot phase from what the cluster says about this start: the
// StatefulSet's events (a pod the quota or Pod Security refused), the pod's scheduling
// condition, its container's waiting reason and its own events. "blocked: " marks a
// start that will not finish without someone acting (console/src/lib/bootPhase.ts).
func (k *kubeRuntime) notePhase(ctx context.Context, s *kStatefulSet, pods []kPod) {
	since := s.Spec.Template.Metadata.Annotations[kubeAnnStartedAt]
	var p *kPod
	for i := range pods {
		if pods[i].Metadata.DeletionTimestamp == nil {
			p = &pods[i]
			break
		}
	}
	if p == nil {
		if ev := k.latestEvent(ctx, "StatefulSet", k.base, since, func(e kEvent) bool { return e.Reason == "FailedCreate" }); ev != nil {
			k.setPhase("blocked: " + ev.Message)
			return
		}
		k.setPhase("pod: creating")
		return
	}
	for _, c := range p.Status.Conditions {
		if c.Type == "PodScheduled" && c.Status == "False" {
			// Often transient — a node being added by the autoscaler — so not "blocked".
			k.setPhase(strings.TrimSpace("pod: waiting for a node: " + c.Message))
			return
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name != kubeAgentName {
			continue
		}
		if w := cs.State.Waiting; w != nil && kubeBlockedReasons[w.Reason] {
			k.setPhase(strings.TrimSpace("blocked: " + w.Reason + ": " + w.Message))
			return
		}
		if cs.State.Running != nil {
			k.setPhase("agent: starting")
			return
		}
	}
	if ev := k.latestEvent(ctx, "Pod", p.Metadata.Name, since, func(e kEvent) bool {
		switch e.Reason {
		case "Pulling", "FailedMount", "FailedAttachVolume":
			return true
		}
		return false
	}); ev != nil {
		if ev.Reason == "Pulling" {
			k.setPhase("pod: pulling image")
		} else {
			k.setPhase("pod: " + ev.Reason + ": " + ev.Message)
		}
		return
	}
	k.setPhase("pod: starting")
}

// latestEvent returns the newest event about one object that matches keep and is not
// older than since (RFC 3339; "" = any). Errors read as "no event": the phase is a hint.
func (k *kubeRuntime) latestEvent(ctx context.Context, kind, name, since string, keep func(kEvent) bool) *kEvent {
	var l kEventList
	q := url.Values{"fieldSelector": {"involvedObject.kind=" + kind + ",involvedObject.name=" + name}}
	if err := k.c.list(ctx, k.nsPath("")+"/events", q, &l); err != nil {
		return nil
	}
	sinceT, _ := time.Parse(time.RFC3339, since)
	var best *kEvent
	var bestT time.Time
	for i := range l.Items {
		e := &l.Items[i]
		if !keep(*e) {
			continue
		}
		t := eventTime(e)
		if !sinceT.IsZero() && t.Before(sinceT) {
			continue
		}
		if best == nil || t.After(bestT) {
			best, bestT = e, t
		}
	}
	return best
}

func eventTime(e *kEvent) time.Time {
	var out time.Time
	for _, s := range []string{e.LastTimestamp, e.EventTime, e.FirstTimestamp} {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil && t.After(out) {
			out = t
		}
	}
	return out
}
