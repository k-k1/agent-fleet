// A golden's identity is compared by image CONTENT, not by the reference string
// (docs/log/72 §72.6.4). Both directions of that are pinned here.
//
// CP and workspace share one ImageTag, so replacing only the CP still puts the workspace
// on the same tag. That is a re-tag inside ECR and the digest is identical, but the
// golden's af-image is a reference string INCLUDING the tag, so it did not match and both
// architectures' homes were baked from scratch (measured: ~10 minutes, two EC2 slots).
//
// The other direction is worse: pushing new content to a mutable tag (`:dev`) leaves the
// string matching, and only new members are handed a home baked from the old image.
// Either mistake passes with the tests and the live deployment both green.
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

const (
	goldenImgOldTag = "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/af-workspace:0.10.1-dev-54033c64"
	goldenImgNewTag = "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/af-workspace:0.10.1-dev-d7e0173c"
)

// withFreshCache swaps the memo the identity is cached in. Production keys it on the
// image string, so two tests that use the same image would otherwise see each other's
// answer (and the second one's fake ECR would never be called).
func withFreshCache(t *testing.T) {
	t.Helper()
	orig := Freshness
	t.Cleanup(func() { Freshness = orig })
	now := time.Unix(1000, 0)
	Freshness = &TTLCache{m: map[string]TTLEntry{}, now: func() time.Time { return now }}
}

// addGoldenIdentity puts a completed x86_64 golden in the fake world with both stamps —
// fp "" means "baked before af-image-fp existed", which every deployment's current
// golden is.
func addGoldenIdentity(h *ec2Harness, id, image, fp string) {
	h.ec2.addGolden(id, "clu", image, ec2types.SnapshotStateCompleted, time.Now())
	if fp != "" {
		h.ec2.snapshots[id].Tags = append(h.ec2.snapshots[id].Tags,
			ec2types.Tag{Key: aws.String(ec2TagImageFP), Value: aws.String(fp)})
	}
}

// fpOf is what the CP will compute for a given fake registry answer.
func fpOf(t *testing.T, reg *fakeECR, image string) string {
	t.Helper()
	fp := hashImageFingerprint(ecrImageFingerprint(context.Background(), reg, image))
	if fp == "" {
		t.Fatal("the fake registry produced no fingerprint")
	}
	return fp
}

// The path that cost the 10 minutes and the two slots: different tag, same content.
func TestGoldenSurvivesARetagWithTheSameContent(t *testing.T) {
	withFreshCache(t)
	ctx := context.Background()
	h := newEC2Harness(t)
	reg := &fakeECR{manifest: index("sha256:aaaa0001", "sha256:bbbb0001", false), digest: "sha256:index0001"}
	h.rt.base.ecr = reg
	h.rt.base.cfg.workspaceImage = goldenImgNewTag

	// Baked while the previous tag was current, but with the content in use now.
	addGoldenIdentity(h, "snap-golden", goldenImgOldTag, fpOf(t, reg, goldenImgOldTag))

	if got := h.rt.goldenSnapshot(ctx); got != "snap-golden" {
		t.Fatalf("goldenSnapshot = %q; 同じ digest の再タグで golden を捨てた（実機では約 10 分・スロット 2 本の焼き直し）", got)
	}
}

// The other direction: new content pushed onto a mutable tag, which a comparison of
// strings alone cannot see.
func TestGoldenIsRefusedWhenTheSameTagMovedToNewContent(t *testing.T) {
	withFreshCache(t)
	ctx := context.Background()
	h := newEC2Harness(t)
	reg := &fakeECR{manifest: index("sha256:aaaa0002", "sha256:bbbb0002", false), digest: "sha256:index0002"}
	h.rt.base.ecr = reg
	h.rt.base.cfg.workspaceImage = goldenImgNewTag

	// Baked under the same tag, but the content behind it then was a different image.
	addGoldenIdentity(h, "snap-old-content", goldenImgNewTag, "sha256:"+
		"0000000000000000000000000000000000000000000000000000000000000000")

	if got := h.rt.goldenSnapshot(ctx); got != "" {
		t.Fatalf("goldenSnapshot = %q; 中身が変わったタグの golden を使った（新規メンバーだけが古い home を配られる）", got)
	}
}

// With a fingerprint on only one side, or on neither, the comparison falls back to the
// reference string. Reading unknown as a mismatch would throw away every deployment's
// golden the moment it upgrades.
func TestGoldenIdentityFallsBackToTheReference(t *testing.T) {
	withFreshCache(t)
	ctx := context.Background()

	tags := func(image, fp string) []ec2types.Tag {
		out := []ec2types.Tag{{Key: aws.String(ec2TagImage), Value: aws.String(image)}}
		if fp != "" {
			out = append(out, ec2types.Tag{Key: aws.String(ec2TagImageFP), Value: aws.String(fp)})
		}
		return out
	}

	// ECR unreadable (missing permission, a different registry): this side has no
	// fingerprint.
	noECR := goldenIdentityFor(ctx, nil, goldenImgNewTag)
	if noECR.fp != "" {
		t.Fatal("no registry, yet a fingerprint appeared")
	}
	if !noECR.matches(tags(goldenImgNewTag, "sha256:whatever")) {
		t.Error("指紋が読めないのに、スナップショット側の指紋で不一致にした（＝全 golden を捨てる側）")
	}
	if noECR.matches(tags(goldenImgOldTag, "")) {
		t.Error("参照文字列が違うのに一致した")
	}

	// A golden baked before the fingerprint tag existed is still matched by the string.
	reg := &fakeECR{manifest: index("sha256:aaaa0001", "sha256:bbbb0001", false), digest: "sha256:index0001"}
	id := goldenIdentityFor(ctx, reg, goldenImgNewTag)
	if id.fp == "" {
		t.Fatal("fingerprint not resolved from the fake registry")
	}
	if !id.matches(tags(goldenImgNewTag, "")) {
		t.Error("af-image-fp を持たない旧 golden が使われなくなった（アップグレードで全部焼き直しになる）")
	}
}

// Both stamps are written at bake time: the reference string for people and for the
// existing tools (bake-golden.sh / dev-deploy.sh), the fingerprint for the CP's own
// comparison.
func TestGoldenCaptureStampsBothIdentities(t *testing.T) {
	withFreshCache(t)
	ctx := context.Background()
	h := newEC2Harness(t)
	reg := &fakeECR{manifest: index("sha256:aaaa0001", "sha256:bbbb0001", false), digest: "sha256:index0001"}
	f := h.factory()
	f.base.ecr = reg
	f.base.cfg.workspaceImage = goldenImgNewTag

	id, err := f.SnapshotHome(ctx, "vol-1", "af-ws-acme-alice", EC2ArchX86)
	if err != nil {
		t.Fatalf("SnapshotHome: %v", err)
	}
	snap := h.ec2.snapshots[id]
	if snap == nil {
		t.Fatalf("snapshot %q was not created", id)
	}
	if got := ec2TagValue(snap.Tags, ec2TagImage); got != goldenImgNewTag {
		t.Errorf("af-image = %q, want %q", got, goldenImgNewTag)
	}
	if got := ec2TagValue(snap.Tags, ec2TagImageFP); got != fpOf(t, reg, goldenImgNewTag) {
		t.Errorf("af-image-fp = %q, want the content fingerprint", got)
	}
}

// hashImageFingerprint exists to fit the fingerprint into an EC2 tag value (256 chars).
// Empty stays empty: turning "unknown" into a value would defeat the fallback above.
func TestHashImageFingerprint(t *testing.T) {
	if got := hashImageFingerprint(""); got != "" {
		t.Errorf("hashImageFingerprint(\"\") = %q, want \"\"", got)
	}
	raw := "linux/amd64=sha256:aaaa0001 linux/arm64=sha256:bbbb0001"
	got := hashImageFingerprint(raw)
	if got != hashImageFingerprint(raw) {
		t.Error("not deterministic")
	}
	if len(got) != len("sha256:")+64 {
		t.Errorf("tag value is %d chars: %q", len(got), got)
	}
	if got == hashImageFingerprint(raw+" ") {
		t.Error("two different fingerprints hashed to the same tag value")
	}
}
