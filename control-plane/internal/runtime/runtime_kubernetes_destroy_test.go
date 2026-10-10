package runtime

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
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

// destroyCluster is a namespace as Destroy sees it, kept in memory: a StatefulSet whose
// annotations take merge patches, two claims bound to volumes, and the volumes. Deleting
// a claim deletes its volume unless the test says the disk stays (retain), and every
// write is logged in order.
type destroyCluster struct {
	mu      sync.Mutex
	sts     *kStatefulSet
	claims  map[string]kPVC
	pvs     map[string]kPV
	retain  map[string]bool // volumes that outlive their claim
	stuck   map[string]bool // claims whose delete is accepted but that stay (a finalizer)
	writes  []string
	pvReads int
	// onInventory runs when the inventory is written: the moment a late bind can land
	// between Destroy's read of a claim and its delete.
	onInventory func(c *destroyCluster)
	rv          int
}

// touch gives a claim a new resource version, as any write to it does.
func (c *destroyCluster) touch(name string) {
	c.rv++
	pvc := c.claims[name]
	pvc.Metadata.ResourceVersion = "rv" + itoa(c.rv)
	c.claims[name] = pvc
}

func newDestroyCluster() *destroyCluster {
	zero := int32(0)
	return &destroyCluster{
		sts: &kStatefulSet{
			Metadata: kObjectMeta{Name: "af-ws-x", Namespace: "ns", Generation: 6, ResourceVersion: "9",
				Annotations: map[string]string{kubeAnnWorkspace: "af-ws-x"}},
			Spec:   kStatefulSetSpec{Replicas: &zero},
			Status: kStatefulSetStatus{ObservedGeneration: 6},
		},
		claims: map[string]kPVC{},
		pvs:    map[string]kPV{},
		retain: map[string]bool{},
		stuck:  map[string]bool{},
	}
}

func (c *destroyCluster) bind(claim, volume string, protected bool) {
	c.claims[claim] = kPVC{Metadata: kObjectMeta{Name: claim, UID: "uid-" + claim}, Spec: kPVCSpec{VolumeName: volume}}
	pv := kPV{Metadata: kObjectMeta{Name: volume, Finalizers: []string{"kubernetes.io/pv-protection"}}}
	if protected {
		pv.Metadata.Finalizers = append(pv.Metadata.Finalizers, "external-provisioner.volume.kubernetes.io/finalizer")
	}
	c.pvs[volume] = pv
	c.touch(claim)
}

// unbound adds a claim with no volume; annotations as the scheduler or PV controller left them.
func (c *destroyCluster) unbound(claim string, annotations map[string]string) {
	c.claims[claim] = kPVC{Metadata: kObjectMeta{Name: claim, UID: "uid-" + claim, Annotations: annotations}}
	c.touch(claim)
}

func (c *destroyCluster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	path := r.URL.Path
	const nsp = "/api/v1/namespaces/ns/"
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	notFound := func() {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(kubeNotFoundBody))
	}
	if r.Method != http.MethodGet {
		c.writes = append(c.writes, r.Method+" "+path)
	}
	switch {
	case path == "/apis/apps/v1/namespaces/ns/statefulsets/af-ws-x":
		if c.sts == nil {
			notFound()
			return
		}
		switch r.Method {
		case http.MethodGet:
			reply(c.sts)
		case http.MethodPatch:
			var p struct {
				Metadata struct {
					Annotations map[string]string `json:"annotations"`
				} `json:"metadata"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &p)
			for k, v := range p.Metadata.Annotations {
				c.sts.Metadata.Annotations[k] = v
			}
			if _, ok := p.Metadata.Annotations[kubeAnnInventory]; ok && c.onInventory != nil {
				f := c.onInventory
				c.onInventory = nil
				f(c)
			}
			reply(c.sts)
		case http.MethodDelete:
			c.sts = nil
			reply(map[string]any{})
		}
	case path == nsp+"pods" && r.Method == http.MethodGet:
		reply(kPodList{})
	case strings.HasPrefix(path, nsp+"pods/"), strings.HasPrefix(path, nsp+"services/"), strings.HasPrefix(path, nsp+"secrets/"):
		notFound()
	case strings.HasPrefix(path, nsp+"persistentvolumeclaims/"):
		name := strings.TrimPrefix(path, nsp+"persistentvolumeclaims/")
		pvc, ok := c.claims[name]
		if !ok {
			notFound()
			return
		}
		if r.Method == http.MethodDelete {
			var opts struct {
				Preconditions struct {
					UID             string `json:"uid"`
					ResourceVersion string `json:"resourceVersion"`
				} `json:"preconditions"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &opts)
			pc := opts.Preconditions
			if (pc.UID != "" && pc.UID != pvc.Metadata.UID) || (pc.ResourceVersion != "" && pc.ResourceVersion != pvc.Metadata.ResourceVersion) {
				w.WriteHeader(409)
				_, _ = w.Write([]byte(`{"kind":"Status","reason":"Conflict","message":"Precondition failed","code":409}`))
				return
			}
			if c.stuck[name] {
				reply(pvc)
				return
			}
			delete(c.claims, name)
			if v := pvc.Spec.VolumeName; v != "" && !c.retain[v] {
				delete(c.pvs, v)
			}
		}
		reply(pvc)
	case strings.HasPrefix(path, "/api/v1/persistentvolumes/"):
		c.pvReads++
		pv, ok := c.pvs[strings.TrimPrefix(path, "/api/v1/persistentvolumes/")]
		if !ok {
			notFound()
			return
		}
		reply(pv)
	default:
		notFound()
	}
}

func destroyRuntime(t *testing.T, c *destroyCluster) *kubeRuntime {
	t.Helper()
	srv := httptest.NewTLSServer(c)
	t.Cleanup(srv.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	tok := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(tok, []byte("t"), 0o600)
	cl, err := newKubeClient(srv.URL, ca, tok)
	if err != nil {
		t.Fatal(err)
	}
	f := &kubeFactory{cfg: &kubeConfig{namespace: "ns", image: "img:1"}, c: cl, pins: fakePinner{},
		poll: 10 * time.Millisecond, stopMargin: 100 * time.Millisecond}
	return f.New(Workspace{ContainerName: "af-ws-x"}, SecretKeys{}, nil).(*kubeRuntime)
}

func shortDestroyBudget(t *testing.T) {
	old := kubeDestroyClaimBudget
	kubeDestroyClaimBudget = 150 * time.Millisecond
	t.Cleanup(func() { kubeDestroyClaimBudget = old })
}

func indexOf(list []string, prefix string) int {
	for i, s := range list {
		if strings.HasPrefix(s, prefix) {
			return i
		}
	}
	return -1
}

// Everything goes: the inventory is written before any claim is deleted, the erase pod
// is deleted before the claims, and the StatefulSet goes last.
func TestKubeDestroyRemovesEverythingInOrder(t *testing.T) {
	shortDestroyBudget(t)
	c := newDestroyCluster()
	c.bind("af-ws-x-home", "pv-home", true)
	c.bind("af-ws-x-state", "pv-state", true)
	rt := destroyRuntime(t, c)
	res, err := rt.Destroy(context.Background())
	if err != nil || len(res) != 0 {
		t.Fatalf("Destroy = %v, %v; want no residue", res, err)
	}
	inv, erase := indexOf(c.writes, "PATCH /apis/apps"), indexOf(c.writes, "DELETE /api/v1/namespaces/ns/pods/af-ws-x-erase")
	claim, sts := indexOf(c.writes, "DELETE /api/v1/namespaces/ns/persistentvolumeclaims/"), indexOf(c.writes, "DELETE /apis/apps")
	if !(inv >= 0 && erase >= 0 && claim > inv && claim > erase && sts > claim && sts == len(c.writes)-1) {
		t.Fatalf("write order = %v", c.writes)
	}
}

// A disk that stays (reclaimPolicy Retain, or a provisioner that failed) keeps the
// StatefulSet with its inventory. A re-run, with the claims long gone, still knows the
// volume from the inventory and reports it; once the volume goes the re-run finishes.
func TestKubeDestroyKeepsTheInventoryUntilEveryVolumeIsGone(t *testing.T) {
	shortDestroyBudget(t)
	c := newDestroyCluster()
	c.bind("af-ws-x-home", "pv-home", true)
	c.bind("af-ws-x-state", "pv-state", true)
	c.retain["pv-home"] = true
	rt := destroyRuntime(t, c)
	want := []string{"pv:pv-home", "statefulset:ns/af-ws-x"}
	res, err := rt.Destroy(context.Background())
	if err != nil || !reflect.DeepEqual(res, want) {
		t.Fatalf("first run = %v, %v; want %v", res, err, want)
	}
	var inv kubeInventory
	if err := json.Unmarshal([]byte(c.sts.Metadata.Annotations[kubeAnnInventory]), &inv); err != nil ||
		inv.Claims["uid-af-ws-x-home"].Volume != "pv-home" || inv.Claims["uid-af-ws-x-home"].Name != "af-ws-x-home" ||
		!inv.Volumes["pv-home"].Protected || !inv.Volumes["pv-state"].Protected {
		t.Fatalf("inventory = %+v (%v)", inv, err)
	}
	res, err = rt.Destroy(context.Background())
	if err != nil || !reflect.DeepEqual(res, want) {
		t.Fatalf("re-run with the claims gone = %v, %v; want %v", res, err, want)
	}
	if c.sts == nil {
		t.Fatal("the re-run deleted the StatefulSet while a volume of its inventory remained")
	}
	delete(c.pvs, "pv-home") // the operator removes the disk, as the runbook says
	res, err = rt.Destroy(context.Background())
	if err != nil || len(res) != 0 || c.sts != nil {
		t.Fatalf("last run = %v, %v, StatefulSet left %v; want everything gone", res, err, c.sts != nil)
	}
}

// A volume whose object goes without a deletion-protection finalizer is no proof that
// its disk went, so it stays a residue.
func TestKubeDestroyDoesNotTrustAnUnprotectedVolume(t *testing.T) {
	shortDestroyBudget(t)
	c := newDestroyCluster()
	c.bind("af-ws-x-home", "pv-home", false)
	c.bind("af-ws-x-state", "pv-state", true)
	rt := destroyRuntime(t, c)
	res, _ := rt.Destroy(context.Background())
	if want := []string{"pv:pv-home", "statefulset:ns/af-ws-x"}; !reflect.DeepEqual(res, want) {
		t.Fatalf("residue = %v, want %v", res, want)
	}
}

// A claim gone before any inventory recorded it (a Destroy of an earlier build, or an
// operator): its volume is unknown, so the StatefulSet stays even when everything this
// run can see goes.
func TestKubeDestroyKeepsTheStatefulSetForAClaimItNeverRecorded(t *testing.T) {
	shortDestroyBudget(t)
	c := newDestroyCluster()
	c.bind("af-ws-x-state", "pv-state", true)
	rt := destroyRuntime(t, c)
	res, _ := rt.Destroy(context.Background())
	if want := []string{"statefulset:ns/af-ws-x"}; !reflect.DeepEqual(res, want) {
		t.Fatalf("residue = %v, want %v", res, want)
	}
	if _, ok := c.claims["af-ws-x-state"]; ok {
		t.Fatal("the claim that was there was not deleted")
	}
}

// An inventory that cannot be read is neither trusted nor overwritten.
func TestKubeDestroyKeepsAnUnreadableInventory(t *testing.T) {
	shortDestroyBudget(t)
	c := newDestroyCluster()
	c.bind("af-ws-x-home", "pv-home", true)
	c.bind("af-ws-x-state", "pv-state", true)
	c.sts.Metadata.Annotations[kubeAnnInventory] = "{not json"
	rt := destroyRuntime(t, c)
	res, _ := rt.Destroy(context.Background())
	if want := []string{"statefulset:ns/af-ws-x"}; !reflect.DeepEqual(res, want) {
		t.Fatalf("residue = %v, want %v", res, want)
	}
	if c.sts.Metadata.Annotations[kubeAnnInventory] != "{not json" {
		t.Fatal("the unreadable inventory was overwritten")
	}
}

// A claim bound after Destroy read it unbound (a provisioner finishing late): the delete
// is conditional on the version read, so it is refused, the claim is read again, and the
// late volume enters the inventory before the claim goes — and stays a residue when its
// disk outlives the claim.
func TestKubeDestroyRecordsAVolumeBoundLate(t *testing.T) {
	shortDestroyBudget(t)
	c := newDestroyCluster()
	c.unbound("af-ws-x-home", nil)
	c.bind("af-ws-x-state", "pv-state", true)
	c.onInventory = func(c *destroyCluster) {
		pvc := c.claims["af-ws-x-home"]
		pvc.Spec.VolumeName = "pv-late-home"
		c.claims["af-ws-x-home"] = pvc
		c.touch("af-ws-x-home")
		c.pvs["pv-late-home"] = kPV{Metadata: kObjectMeta{Name: "pv-late-home",
			Finalizers: []string{"external-provisioner.volume.kubernetes.io/finalizer"}}}
		c.retain["pv-late-home"] = true
	}
	rt := destroyRuntime(t, c)
	res, err := rt.Destroy(context.Background())
	if want := []string{"pv:pv-late-home", "statefulset:ns/af-ws-x"}; err != nil || !reflect.DeepEqual(res, want) {
		t.Fatalf("Destroy = %v, %v; want %v", res, err, want)
	}
	if c.sts == nil || !strings.Contains(c.sts.Metadata.Annotations[kubeAnnInventory], "pv-late-home") {
		t.Fatal("the late volume is not in the inventory the StatefulSet keeps")
	}
}

// A claim recorded without a volume is unknown whatever its annotations: a static or
// pre-bound volume gets its claimRef saved before the claim's volumeName, so the claim can
// read unbound while a disk is already tied to it. The StatefulSet stays on this run and
// on a re-run that finds the claim gone.
func TestKubeDestroyUnboundClaimsAreUnknown(t *testing.T) {
	shortDestroyBudget(t)
	for _, ann := range []map[string]string{nil, {"volume.kubernetes.io/selected-node": "node-a"}} {
		c := newDestroyCluster()
		c.unbound("af-ws-x-home", ann)
		c.bind("af-ws-x-state", "pv-state", true)
		rt := destroyRuntime(t, c)
		for run := 1; run <= 2; run++ {
			res, _ := rt.Destroy(context.Background())
			if want := []string{"statefulset:ns/af-ws-x"}; !reflect.DeepEqual(res, want) {
				t.Fatalf("annotations %v, run %d: residue = %v, want %v", ann, run, res, want)
			}
		}
	}
}

// A half-bound volume: the volume already names the claim, the claim does not
// name the volume yet, and the volume outlives the claim.
func TestKubeDestroyHalfBoundStaticVolume(t *testing.T) {
	shortDestroyBudget(t)
	c := newDestroyCluster()
	c.unbound("af-ws-x-home", nil)
	c.bind("af-ws-x-state", "pv-state", true)
	c.pvs["pv-static"] = kPV{Metadata: kObjectMeta{Name: "pv-static"}} // claimRef → af-ws-x-home, not visible to Destroy
	res, _ := destroyRuntime(t, c).Destroy(context.Background())
	if want := []string{"statefulset:ns/af-ws-x"}; !reflect.DeepEqual(res, want) || c.sts == nil {
		t.Fatalf("residue = %v, StatefulSet kept %v; want %v and kept", res, c.sts != nil, want)
	}
}

// An unknown recorded for one claim is not erased by a claim recreated under the same
// name with another UID (a Start after a Destroy that died before returning).
func TestKubeDestroyKeepsAnOldUnknownWhenTheClaimIsReplaced(t *testing.T) {
	shortDestroyBudget(t)
	c := newDestroyCluster()
	c.unbound("af-ws-x-home", map[string]string{"volume.kubernetes.io/selected-node": "node-a"})
	c.bind("af-ws-x-state", "pv-state", true)
	rt := destroyRuntime(t, c)
	if res, _ := rt.Destroy(context.Background()); !reflect.DeepEqual(res, []string{"statefulset:ns/af-ws-x"}) {
		t.Fatalf("first run = %v", res)
	}
	c.bind("af-ws-x-home", "pv-new-home", true)
	pvc := c.claims["af-ws-x-home"]
	pvc.Metadata.UID = "uid-new-home"
	c.claims["af-ws-x-home"] = pvc
	c.bind("af-ws-x-state", "pv-new-state", true)
	res, _ := rt.Destroy(context.Background())
	if want := []string{"statefulset:ns/af-ws-x"}; !reflect.DeepEqual(res, want) || c.sts == nil {
		t.Fatalf("after the replacement = %v, StatefulSet kept %v; want %v and kept", res, c.sts != nil, want)
	}
	var inv kubeInventory
	_ = json.Unmarshal([]byte(c.sts.Metadata.Annotations[kubeAnnInventory]), &inv)
	if old, ok := inv.Claims["uid-af-ws-x-home"]; !ok || old.Volume != "" || inv.Claims["uid-new-home"].Volume != "pv-new-home" {
		t.Fatalf("inventory = %+v; want the old unknown and the new claim side by side", inv.Claims)
	}
}

// The one way an unknown resolves: the same claim, read again, names its volume.
func TestKubeDestroyResolvesAnUnknownWhenTheSameClaimNamesItsVolume(t *testing.T) {
	shortDestroyBudget(t)
	c := newDestroyCluster()
	c.unbound("af-ws-x-home", nil)
	c.bind("af-ws-x-state", "pv-state", true)
	c.stuck["af-ws-x-home"] = true // its delete waits on a finalizer
	rt := destroyRuntime(t, c)
	if res, _ := rt.Destroy(context.Background()); !reflect.DeepEqual(res, []string{"pvc:ns/af-ws-x-home", "statefulset:ns/af-ws-x"}) {
		t.Fatalf("first run = %v", res)
	}
	pvc := c.claims["af-ws-x-home"]
	pvc.Spec.VolumeName = "pv-home"
	c.claims["af-ws-x-home"] = pvc
	c.touch("af-ws-x-home")
	c.pvs["pv-home"] = kPV{Metadata: kObjectMeta{Name: "pv-home", Finalizers: []string{"external-provisioner.volume.kubernetes.io/finalizer"}}}
	delete(c.stuck, "af-ws-x-home")
	res, err := rt.Destroy(context.Background())
	if err != nil || len(res) != 0 || c.sts != nil {
		t.Fatalf("second run = %v, %v, StatefulSet left %v; want everything gone", res, err, c.sts != nil)
	}
}
