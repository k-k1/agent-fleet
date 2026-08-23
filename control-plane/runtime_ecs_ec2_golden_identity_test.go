// golden の同一性を「参照文字列」から「内容」へ移した分（docs/72 §72.6.4）のテスト。
//
// 実機で起きたこと: CP と workspace が 1 つの ImageTag を共有するので、CP だけ差し替えたい
// ときも workspace 側を同じタグに置く。ECR 内の再タグなので digest は完全に一致していたが、
// golden の af-image は**タグを含む参照文字列**なので一致せず、CP は 2 アーキ分の home を
// 一から焼き直した（約 10 分・EC2 スロット 2 本）。
//
// 逆向きはもっと悪い: 可変タグ（`:dev`）へ新しい内容を push すると文字列は一致したままで、
// 新規メンバーだけが**古いイメージで焼かれた home** を配られる。どちらの誤りも「テストも
// 実機も緑」で通る型なので、両方向をここで固定する。
package main

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
	orig := freshness
	t.Cleanup(func() { freshness = orig })
	now := time.Unix(1000, 0)
	freshness = &ttlCache{m: map[string]ttlEntry{}, now: func() time.Time { return now }}
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

// ★ これが 10 分と 2 台を払っていた経路。タグは違う・中身は同じ。
func TestGoldenSurvivesARetagWithTheSameContent(t *testing.T) {
	withFreshCache(t)
	ctx := context.Background()
	h := newEC2Harness(t)
	reg := &fakeECR{manifest: index("sha256:aaaa0001", "sha256:bbbb0001", false), digest: "sha256:index0001"}
	h.rt.base.ecr = reg
	h.rt.base.cfg.workspaceImage = goldenImgNewTag

	// 焼かれたのは「前のタグ」のときだが、指紋はいまと同じ。
	addGoldenIdentity(h, "snap-golden", goldenImgOldTag, fpOf(t, reg, goldenImgOldTag))

	if got := h.rt.goldenSnapshot(ctx); got != "snap-golden" {
		t.Fatalf("goldenSnapshot = %q; 同じ digest の再タグで golden を捨てた（実機では約 10 分・スロット 2 本の焼き直し）", got)
	}
}

// ★ 逆向き。可変タグへ新しい内容を push した場合で、文字列だけを見ていると気づけない。
func TestGoldenIsRefusedWhenTheSameTagMovedToNewContent(t *testing.T) {
	withFreshCache(t)
	ctx := context.Background()
	h := newEC2Harness(t)
	reg := &fakeECR{manifest: index("sha256:aaaa0002", "sha256:bbbb0002", false), digest: "sha256:index0002"}
	h.rt.base.ecr = reg
	h.rt.base.cfg.workspaceImage = goldenImgNewTag

	// 同じタグで焼かれているが、そのときの中身は別物だった。
	addGoldenIdentity(h, "snap-old-content", goldenImgNewTag, "sha256:"+
		"0000000000000000000000000000000000000000000000000000000000000000")

	if got := h.rt.goldenSnapshot(ctx); got != "" {
		t.Fatalf("goldenSnapshot = %q; 中身が変わったタグの golden を使った（新規メンバーだけが古い home を配られる）", got)
	}
}

// 指紋が片側にしか無い／どちらにも無いときは、これまでどおり参照文字列で比べる。
// unknown を「不一致」と読むと、**アップグレードした瞬間に全配備の golden が捨てられる**。
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

	// ECR が読めない（権限不足・別レジストリ）＝ こちら側の指紋が空。
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

	// 昔焼いた golden（指紋タグ無し）は、これまでどおり文字列で拾う。
	reg := &fakeECR{manifest: index("sha256:aaaa0001", "sha256:bbbb0001", false), digest: "sha256:index0001"}
	id := goldenIdentityFor(ctx, reg, goldenImgNewTag)
	if id.fp == "" {
		t.Fatal("fingerprint not resolved from the fake registry")
	}
	if !id.matches(tags(goldenImgNewTag, "")) {
		t.Error("af-image-fp を持たない旧 golden が使われなくなった（アップグレードで全部焼き直しになる）")
	}
}

// 焼くときは両方刻む。参照文字列は人と既存の道具（bake-golden.sh / dev-deploy.sh）のため、
// 指紋は CP の突合のため。
func TestGoldenCaptureStampsBothIdentities(t *testing.T) {
	withFreshCache(t)
	ctx := context.Background()
	h := newEC2Harness(t)
	reg := &fakeECR{manifest: index("sha256:aaaa0001", "sha256:bbbb0001", false), digest: "sha256:index0001"}
	f := h.factory()
	f.base.ecr = reg
	f.base.cfg.workspaceImage = goldenImgNewTag

	id, err := f.snapshotHome(ctx, "vol-1", "af-ws-acme-alice", ec2ArchX86)
	if err != nil {
		t.Fatalf("snapshotHome: %v", err)
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

// hashImageFingerprint は EC2 のタグ値に収める（256 文字）ためのもの。空は空のまま
// ——「不明」を「ある値」にしてしまうと、上の fallback が効かなくなる。
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
