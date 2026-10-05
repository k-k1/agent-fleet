// runtime_kubernetes_stale.go — would a stop→start run different code (ADR 0106
// decision 9), and the boot check of the configured StorageClass (decision 4).
//
// Stale follows the rules at the head of runtime_ecs_stale.go: both sides are content
// fingerprints computed by one function (manifestFingerprint over the registry's manifest,
// an index unwrapped one level and attestations dropped), never a digest compared with a
// digest, never a version. The launched side is the annotation Start wrote onto the pod
// template from the same registry read that pinned the digest. When in doubt — no
// annotation, an unreadable registry — the answer is false.
package runtime

import (
	"context"
	"fmt"
	"log"
	"time"
)

// kubeStaleTTL bounds how often Stale reads the cluster and the registry: /api/workspace
// is polled every 4 s per open Console, and both facts move only on a push or a Start.
// Start primes both entries, so the badge never lingers after the restart that clears it.
const kubeStaleTTL = 60 * time.Second

func (k *kubeRuntime) staleStampKey() string { return "k8s-stamp:" + k.cfg.namespace + "/" + k.base }
func (k *kubeRuntime) staleImageKey() string { return "k8s-img:" + k.cfg.image }

// Stale reports whether the configured tag now resolves to different content than the
// image this workspace's current start launched.
func (k *kubeRuntime) Stale(ctx context.Context) bool {
	was := Freshness.get(k.staleStampKey(), kubeStaleTTL, func() string {
		s, err := k.getStatefulSet(ctx)
		if err != nil || s == nil {
			return ""
		}
		return s.Spec.Template.Metadata.Annotations[kubeAnnImageFingerprint]
	})
	if was == "" {
		return false
	}
	now := Freshness.get(k.staleImageKey(), kubeStaleTTL, func() string {
		img, err := k.pins.resolve(ctx, k.cfg.image)
		if err != nil {
			return ""
		}
		return img.fingerprint
	})
	return now != "" && now != was
}

// primeStale records what Start just launched, so a cached pre-start value cannot make a
// fresh start look stale for a minute. An empty fingerprint is recorded too: keeping the
// previous start's would be a claim about an image this start did not run.
func (k *kubeRuntime) primeStale(fp string) {
	Freshness.set(k.staleStampKey(), fp)
	if fp != "" {
		Freshness.set(k.staleImageKey(), fp)
	}
}

// checkStorageClass returns what the configured class lacks of ADR 0106 decision 4's
// requirements. The CP reports them at boot and does not refuse to start: an unreadable
// class is often RBAC applied after the CP, and each violation already shows where it
// bites — a claim that stays Pending, a resize that is refused, a Destroy residue.
func checkStorageClass(ctx context.Context, c *kubeClient, name string) []string {
	if name == "" {
		return []string{"AF_K8S_STORAGE_CLASS is unset, so claims use the cluster's default class, which is not checked"}
	}
	var sc kStorageClass
	if err := c.get(ctx, "/apis/storage.k8s.io/v1/storageclasses/"+name, &sc); err != nil {
		return []string{fmt.Sprintf("cannot read it: %v", err)}
	}
	var out []string
	if sc.VolumeBindingMode != "WaitForFirstConsumer" {
		out = append(out, fmt.Sprintf("volumeBindingMode is %q, want WaitForFirstConsumer: a home may be created in a zone where its pod cannot run", sc.VolumeBindingMode))
	}
	if sc.AllowVolumeExpansion == nil || !*sc.AllowVolumeExpansion {
		out = append(out, "allowVolumeExpansion is not true: a home cannot grow")
	}
	if sc.ReclaimPolicy != "" && sc.ReclaimPolicy != "Delete" {
		out = append(out, fmt.Sprintf("reclaimPolicy is %q, want Delete: Destroy cannot remove a disk, and reports it as a residue", sc.ReclaimPolicy))
	}
	return out
}

func logStorageClassCheck(c *kubeClient, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, p := range checkStorageClass(ctx, c, name) {
		log.Printf("kubernetes runtime: WARNING: storage class %q: %s", name, p)
	}
}
