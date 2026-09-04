package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// The cost-allocation tags (docs/log/67, ADR 0048). These are read by NOTHING in the CP —
// only by AWS's bill — which is exactly why they need tests: a tag that silently stops
// being written costs real money that can never be attributed afterwards (cost
// allocation has no backfill), and no user-visible behaviour changes to give it away.

func ec2Tag(key, value string) ec2types.Tag {
	return ec2types.Tag{Key: aws.String(key), Value: aws.String(value)}
}

func instTag(h *ec2Harness, id, key string) string {
	inst := h.ec2.instances[id]
	if inst == nil {
		return ""
	}
	return ec2TagValue(inst.Tags, key)
}

// The slot INSTANCE is the thing that bills (measured: 91% of the attributable cost),
// and before this it carried af-role/af-pool/af-slot-size but nothing that said whose
// it was. A Start has to stamp it.
func TestECSEC2StartStampsTheSlotOwner(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.base.tenantSlug = "acme"
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := instTag(h, "i-hot", EC2TagMembership); got != "M-1" {
		t.Errorf("instance af-membership = %q, want M-1", got)
	}
	if got := instTag(h, "i-hot", EC2TagTenant); got != "acme" {
		t.Errorf("instance af-tenant = %q, want acme", got)
	}
}

// A workspace whose tenant slug never made it into the Workspace record must not stamp
// an EMPTY af-tenant: an empty cost allocation tag value is a real group in the bill and
// reads as "tenant = (blank)" rather than "not tagged".
func TestECSEC2SlotOwnerOmitsAnUnknownTenant(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.base.tenantSlug = ""
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := instTag(h, "i-hot", EC2TagMembership); got != "M-1" {
		t.Errorf("instance af-membership = %q, want M-1", got)
	}
	for _, tg := range h.ec2.instances["i-hot"].Tags {
		if aws.ToString(tg.Key) == EC2TagTenant {
			t.Errorf("af-tenant must be absent, not empty; got %q", aws.ToString(tg.Value))
		}
	}
}

// Releasing the slot hands the box back to the pool, so its hours stop being this
// person's cost and become shared (an idle warm slot belongs to nobody — ADR 0048
// decision 4).
func TestECSEC2ReleaseClearsTheSlotOwner(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.base.tenantSlug = "acme"
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.rt.tagSlotOwner(ctx, "i-hot")
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	if err := h.rt.releaseSlot(ctx); err != nil {
		t.Fatalf("releaseSlot: %v", err)
	}
	if got := instTag(h, "i-hot", EC2TagMembership); got != "" {
		t.Errorf("af-membership survived the release: %q", got)
	}
	if got := instTag(h, "i-hot", EC2TagTenant); got != "" {
		t.Errorf("af-tenant survived the release: %q", got)
	}
}

// A quarantined box is nobody's. Normally it never got stamped (quarantine happens when
// the mount fails, before launch stamps anything), but a previous tenancy whose release
// failed would otherwise keep billing someone for a box they can no longer be given.
func TestECSEC2QuarantineClearsTheSlotOwner(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.base.tenantSlug = "acme"
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-bad", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-bad", time.Now())
	h.rt.tagSlotOwner(ctx, "i-bad")

	h.rt.quarantineSlot(ctx, ec2Placement{volumeID: "vol-1", instanceID: "i-bad"}, context.DeadlineExceeded)

	if got := instTag(h, "i-bad", EC2TagRole); got != ec2RoleQuarantined {
		t.Fatalf("af-role = %q, want quarantined", got)
	}
	if got := instTag(h, "i-bad", EC2TagMembership); got != "" {
		t.Errorf("a quarantined slot still bills %q", got)
	}
}

// The home volume is the other resource that bills per person, and it now has to carry
// the tenant too so the invoice can be grouped without the Console.
func TestECSEC2NewHomeVolumeCarriesTheTenantTag(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.base.tenantSlug = "acme"

	created, err := h.rt.createHomeVolume(ctx, "ap-northeast-1a")
	if err != nil {
		t.Fatalf("createHomeVolume: %v", err)
	}
	// Assert on what AWS ends up holding, not on the struct the call returns — that one
	// is assembled from three fields and never carried tags in the first place.
	vol := h.ec2.volumes[aws.ToString(created.VolumeId)]
	if vol == nil {
		t.Fatalf("volume %q not in the fake", aws.ToString(created.VolumeId))
	}
	if got := ec2TagValue(vol.Tags, EC2TagTenant); got != "acme" {
		t.Errorf("home volume af-tenant = %q, want acme", got)
	}
	if got := ec2TagValue(vol.Tags, EC2TagMembership); got != "M-1" {
		t.Errorf("home volume af-membership = %q, want M-1", got)
	}
}

// The repair half. The pool logic never reads these tags, so a CP that dies between the
// detach and the untag leaves a stale one that NOTHING would ever notice — except the
// invoice, months later. Both directions have to be repaired, and they are different
// failures: a stale tag over-bills a person, a missing one loses the attribution to
// "shared" (and cost allocation cannot be backfilled, so it is lost for good).
func TestECSEC2SweepRepairsSlotOwnerTags(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)

	// i-stale: tagged for M-1, but M-1's home is not on it (a release that half-finished).
	h.ec2.addSlot("i-stale", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.instances["i-stale"].Tags = append(h.ec2.instances["i-stale"].Tags,
		ec2Tag(EC2TagMembership, "M-1"), ec2Tag(EC2TagTenant, "acme"))

	// i-unstamped: actually holding M-2's home, but never stamped (a crash mid-attach,
	// or a box that predates this code).
	h.ec2.addSlot("i-unstamped", "ap-northeast-1a", "m7i.large", true, false)
	v2 := h.ec2.addHomeVolume("vol-2", "M-2", "af-ws-acme-bob", "ap-northeast-1a")
	v2.Tags = append(v2.Tags, ec2Tag(EC2TagTenant, "acme"))
	h.ec2.attach("vol-2", "i-unstamped", time.Now())

	// i-free: no home, no tags. Must be left alone (and must not provoke a DeleteTags).
	h.ec2.addSlot("i-free", "ap-northeast-1a", "m7i.large", true, false)

	if err := h.factory().sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if got := instTag(h, "i-stale", EC2TagMembership); got != "" {
		t.Errorf("i-stale still bills %q", got)
	}
	if got := instTag(h, "i-stale", EC2TagTenant); got != "" {
		t.Errorf("i-stale still carries tenant %q", got)
	}
	if got := instTag(h, "i-unstamped", EC2TagMembership); got != "M-2" {
		t.Errorf("i-unstamped af-membership = %q, want M-2", got)
	}
	if got := instTag(h, "i-unstamped", EC2TagTenant); got != "acme" {
		t.Errorf("i-unstamped af-tenant = %q, want acme (copied from the volume)", got)
	}
	if got := instTag(h, "i-free", EC2TagMembership); got != "" {
		t.Errorf("a free slot got stamped with %q", got)
	}
}
