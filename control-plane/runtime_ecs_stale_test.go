// runtime_ecs_stale_test.go — ECS 系の「要再起動」判定の契約テスト。
// docker で二度踏んだ罠（二辺比較・digest 比較）を ECS でも踏み直さないことと、
// 判らないときは黙ることを固定する。
package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// fakeECR は BatchGetImage だけを返すレジストリ。manifest/digest を差し替えることで
// 「タグが動いた」「内容は同じまま再 push された」を作り分ける。
type fakeECR struct {
	manifest string
	digest   string
	err      error
	calls    int
	lastIn   *ecr.BatchGetImageInput
}

func (f *fakeECR) BatchGetImage(_ context.Context, in *ecr.BatchGetImageInput, _ ...func(*ecr.Options)) (*ecr.BatchGetImageOutput, error) {
	f.calls++
	f.lastIn = in
	if f.err != nil {
		return nil, f.err
	}
	var tag *string
	if len(in.ImageIds) > 0 {
		tag = in.ImageIds[0].ImageTag
	}
	return &ecr.BatchGetImageOutput{Images: []ecrtypes.Image{{
		ImageId:       &ecrtypes.ImageIdentifier{ImageDigest: aws.String(f.digest), ImageTag: tag},
		ImageManifest: aws.String(f.manifest),
	}}}, nil
}

// index は マルチプラットフォームのインデックス manifest を組み立てる。attest が真の
// ときは buildx の attestation manifest（platform unknown/unknown）を足す — 内容は
// 変わらないのに index の digest だけ動く、あの経路。
func index(amd64, arm64 string, attest bool) string {
	entries := fmt.Sprintf(
		`{"digest":%q,"mediaType":"application/vnd.oci.image.manifest.v1+json","platform":{"architecture":"amd64","os":"linux"}},`+
			`{"digest":%q,"mediaType":"application/vnd.oci.image.manifest.v1+json","platform":{"architecture":"arm64","os":"linux"}}`,
		amd64, arm64)
	if attest {
		entries += `,{"digest":"sha256:attest0001","mediaType":"application/vnd.oci.image.manifest.v1+json",` +
			`"annotations":{"vnd.docker.reference.type":"attestation-manifest","vnd.docker.reference.digest":"` + amd64 + `"},` +
			`"platform":{"architecture":"unknown","os":"unknown"}}`
	}
	return `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` + entries + `]}`
}

const ecrTestImage = "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/af-workspace:0.2.0"

func newStaleTestECS(t *testing.T, reg *fakeECR) (*ecsRuntime, *fakeECS) {
	t.Helper()
	fe := &fakeECS{}
	rt := newTestECS(fe, &fakeEFS{}, &fakeSSM{})
	rt.cfg.workspaceImage = ecrTestImage
	rt.ecr = reg
	return rt, fe
}

// 全体の筋: Start が控えた指紋 vs いまタグを引いた指紋。
//
// ★ ここで「走っているタスクの containers[].imageDigest」や「インデックスの digest」を
//
//	実体に選ぶと、内容が同じでも表現が動いて恒久点灯する（docker で二度踏んだ）。
//	provenance だけ付け直した再 push が黙ることを、このテストが担保している。
func TestECSStaleImageStamp(t *testing.T) {
	orig := freshness
	defer func() { freshness = orig }()
	now := time.Unix(1000, 0)
	freshness = &ttlCache{m: map[string]ttlEntry{}, now: func() time.Time { return now }}

	const (
		amd64Old = "sha256:aaaa0001"
		arm64Old = "sha256:bbbb0001"
		amd64New = "sha256:aaaa0002"
	)
	reg := &fakeECR{manifest: index(amd64Old, arm64Old, false), digest: "sha256:index0001"}
	rt, fe := newStaleTestECS(t, reg)
	ctx := context.Background()

	// まだ何も起動していない（サービスが無い）＝判らない → false。
	if rt.Stale(ctx) {
		t.Fatal("no service: stale, want false")
	}
	now = now.Add(2 * ecsStaleTTL)

	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// AcceptedMediaTypes は必須。渡さないと ECR は schema1 へ変換した manifest を返し、
	// 内容比較の土台が崩れる。
	if len(reg.lastIn.AcceptedMediaTypes) == 0 {
		t.Error("BatchGetImage without AcceptedMediaTypes — ECR would answer with a converted schema-1 manifest")
	}
	if got := fe.regCalls[0].ContainerDefinitions[0].DockerLabels[afImageStampLabel]; got == "" {
		t.Fatal("Start registered a task definition without an image stamp")
	}
	if rt.Stale(ctx) {
		t.Fatal("same image: stale, want false")
	}

	// 内容は 1 バイトも変わらないまま、attestation が付いて index の digest だけ動いた。
	// → 停止→起動しても走るコードは変わらないので、出してはいけない。
	reg.manifest, reg.digest = index(amd64Old, arm64Old, true), "sha256:index0002"
	now = now.Add(2 * ecsStaleTTL)
	if rt.Stale(ctx) {
		t.Fatal("provenance-only re-push (same platform manifests): stale, want false")
	}

	// 本当に新しいイメージが同じタグへ push された → 出す。
	reg.manifest, reg.digest = index(amd64New, arm64Old, true), "sha256:index0003"
	now = now.Add(2 * ecsStaleTTL)
	if !rt.Stale(ctx) {
		t.Fatal("tag moved to new content: not stale, want stale")
	}

	// 利用者が「今すぐ再起動」を押した直後は、TTL の残りに関係なく即座に消えること
	// （Start がその場で控え直した値でキャッシュを上書きするため）。
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if rt.Stale(ctx) {
		t.Fatal("restarted onto the new image: stale, want false")
	}

	// タグが引けない（AccessDenied・タグ削除）＝判らない → false。
	reg.err = fmt.Errorf("AccessDeniedException: not authorized to perform ecr:BatchGetImage")
	now = now.Add(2 * ecsStaleTTL)
	if rt.Stale(ctx) {
		t.Fatal("unreadable tag: stale, want false")
	}
}

// 判らない側へ倒す経路をまとめて固定する。どれか一つでも true を返すようになると、
// 押しても消えないバッジになってバッジ全体の信用が落ちる。
func TestECSStaleUnknownNeverNags(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(*ecsRuntime, *fakeECS)
	}{
		{"ECR クライアントが無い（AWS 設定が組めなかった）", func(rt *ecsRuntime, _ *fakeECS) { rt.ecr = nil }},
		{"ECR ではないレジストリ（Docker Hub / GHCR）", func(rt *ecsRuntime, _ *fakeECS) {
			rt.cfg.workspaceImage = "ghcr.io/k-k1/agent-fleet/workspace:0.2.0"
		}},
		{"この機能より前のリビジョンで走っている（指紋が焼かれていない）", func(_ *ecsRuntime, fe *fakeECS) {
			for _, td := range fe.taskDefs {
				td.ContainerDefinitions[0].DockerLabels = nil
			}
		}},
		{"サービスが指しているリビジョンが消えている", func(_ *ecsRuntime, fe *fakeECS) {
			fe.taskDefs = map[string]*ecstypes.TaskDefinition{}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := freshness
			defer func() { freshness = orig }()
			freshness = &ttlCache{m: map[string]ttlEntry{}}

			reg := &fakeECR{manifest: index("sha256:aaaa0001", "sha256:bbbb0001", false), digest: "sha256:index0001"}
			rt, fe := newStaleTestECS(t, reg)
			if err := rt.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			tc.setup(rt, fe)
			// Start が入れたキャッシュを捨てて、素の判定を見る。
			freshness = &ttlCache{m: map[string]ttlEntry{}}
			// タグは動いている（＝判定できていれば true になる状況）。
			reg.manifest, reg.digest = index("sha256:aaaa9999", "sha256:bbbb0001", false), "sha256:index9999"
			if rt.Stale(ctx) {
				t.Fatal("stale, want false")
			}
		})
	}
}

// ecs-ec2 は同じ判定を使う（同じ image・同じタスク定義ファミリ）。委譲が外れると
// EC2 スロット構成だけ静かに検出できなくなるので、経路をここで固定する。
func TestECSEC2StaleDelegatesToBase(t *testing.T) {
	orig := freshness
	defer func() { freshness = orig }()
	freshness = &ttlCache{m: map[string]ttlEntry{}}

	ctx := context.Background()
	reg := &fakeECR{manifest: index("sha256:aaaa0001", "sha256:bbbb0001", false), digest: "sha256:index0001"}
	base, _ := newStaleTestECS(t, reg)
	if err := base.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	rt := &ecsEC2Runtime{base: base}
	if rt.Stale(ctx) {
		t.Fatal("same image: stale, want false")
	}
	reg.manifest, reg.digest = index("sha256:aaaa0002", "sha256:bbbb0001", false), "sha256:index0002"
	freshness = &ttlCache{m: map[string]ttlEntry{}}
	if !rt.Stale(ctx) {
		t.Fatal("tag moved: not stale, want stale")
	}
}

func TestManifestFingerprint(t *testing.T) {
	// 単一 manifest はその digest 自体が config+layers の内容ハッシュ。
	single := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",` +
		`"config":{"digest":"sha256:cfg"},"layers":[{"digest":"sha256:l1"}]}`
	if got := manifestFingerprint(single, "sha256:single01"); got != "sha256:single01" {
		t.Errorf("single manifest: %q", got)
	}
	// インデックスは実プラットフォームの manifest だけを、順序に依存せず並べる。
	a := manifestFingerprint(index("sha256:a1", "sha256:b1", false), "sha256:idx1")
	b := manifestFingerprint(index("sha256:b1", "sha256:a1", false), "sha256:idx2") // amd64/arm64 入れ替え
	if a == b {
		t.Error("platform assignment ignored — swapping which arch has which digest must change the fingerprint")
	}
	if withAttest := manifestFingerprint(index("sha256:a1", "sha256:b1", true), "sha256:idx3"); withAttest != a {
		t.Errorf("attestation entry leaked into the fingerprint: %q != %q", withAttest, a)
	}
	// 壊れた／空の manifest は「判らない」。
	if got := manifestFingerprint("", "sha256:x"); got != "" {
		t.Errorf("unparseable manifest: %q, want empty", got)
	}
	// 実プラットフォームが一つも無いインデックス（attestation だけ）も「判らない」。
	only := `{"manifests":[{"digest":"sha256:z","platform":{"architecture":"unknown","os":"unknown"}}]}`
	if got := manifestFingerprint(only, "sha256:idx4"); got != "" {
		t.Errorf("attestation-only index: %q, want empty", got)
	}
}

func TestParseECRRef(t *testing.T) {
	cases := []struct {
		uri              string
		ok               bool
		acct, repo, tag  string
		digest, regionID string
	}{
		{uri: ecrTestImage, ok: true, acct: "123456789012", repo: "af-workspace", tag: "0.2.0", regionID: "ap-northeast-1"},
		{uri: "123456789012.dkr.ecr.us-east-1.amazonaws.com/team/af-workspace:dev", ok: true,
			acct: "123456789012", repo: "team/af-workspace", tag: "dev", regionID: "us-east-1"},
		{uri: "123456789012.dkr.ecr.us-east-1.amazonaws.com/af-workspace", ok: true,
			acct: "123456789012", repo: "af-workspace", tag: "latest", regionID: "us-east-1"},
		{uri: "123456789012.dkr.ecr.us-east-1.amazonaws.com/af-workspace@sha256:abc", ok: true,
			acct: "123456789012", repo: "af-workspace", digest: "sha256:abc", regionID: "us-east-1"},
		{uri: "ghcr.io/k-k1/agent-fleet/workspace:0.2.0"},
		{uri: "agent-fleet/workspace:m3"},
		{uri: ""},
	}
	for _, c := range cases {
		got, ok := parseECRRef(c.uri)
		if ok != c.ok {
			t.Errorf("%q: ok=%v, want %v", c.uri, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.registryID != c.acct || got.repository != c.repo || got.tag != c.tag ||
			got.digest != c.digest || got.region != c.regionID {
			t.Errorf("%q: %+v", c.uri, got)
		}
	}
}

// ★ マージで静かに壊れる組み合わせ: 7ae97ea1 のタスク定義再利用キャッシュ
// （同じスロットへの再 wake で再登録＋強制デプロイを避ける）と、この指紋スタンプ。
//
// スタンプは taskDefFingerprint が畳む入力の一部で、それは偶然ではなく**必須**である。
// 可変タグ（:dev）ではイメージ文字列が動かないので、「タグの中身が変わった」ことを
// 入力に伝えているのはスタンプだけ。指紋から外すと、イメージ push 後の再 wake が古い
// リビジョンを再利用し（タスク自体は新しいイメージを pull する）、そのリビジョンは
// 古いスタンプを持ったままになる＝**何度再起動しても消えないバッジ**になる。
//
// 逆に、ECR が一時的に引けなかっただけで指紋が動いてもいけない（不要な強制デプロイ＝
// 7ae97ea1 が消した Service Connect の 1〜2 分の窓が戻る）。両方をここで固定する。
func TestECSEC2TaskDefReuseSeesTheImageStamp(t *testing.T) {
	origFresh := freshness
	defer func() { freshness = origFresh }()
	freshness = &ttlCache{m: map[string]ttlEntry{}}

	ctx := context.Background()
	h := newEC2Harness(t)
	reg := &fakeECR{manifest: index("sha256:aaaa0001", "sha256:bbbb0001", false), digest: "sha256:index0001"}
	h.rt.base.cfg.workspaceImage = ecrTestImage
	h.rt.base.ecr = reg
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true

	rewake := func(what string) {
		t.Helper()
		if err := h.rt.Stop(ctx); err != nil {
			t.Fatalf("%s: Stop: %v", what, err)
		}
		// 実サービスが desiredCount 0 に落ち着いた状態を模す（既存テストと同じ模倣）。
		h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
		if err := h.rt.Start(ctx); err != nil {
			t.Fatalf("%s: Start: %v", what, err)
		}
	}

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if len(h.ecs.regCalls) != 1 {
		t.Fatalf("regCalls after first Start = %d, want 1", len(h.ecs.regCalls))
	}
	stamp := h.ecs.regCalls[0].ContainerDefinitions[0].DockerLabels[afImageStampLabel]
	if stamp == "" {
		t.Fatal("the EC2 revision carries no image stamp — the drift badge can never light on ecs-ec2")
	}

	// 何も変わっていない再 wake: 再登録も強制デプロイもしない。
	rewake("unchanged re-wake")
	if len(h.ecs.regCalls) != 1 {
		t.Fatalf("regCalls after an unchanged re-wake = %d, want 1 — the stamp is churning the fingerprint", len(h.ecs.regCalls))
	}

	// ECR が一時的に引けない再 wake: スタンプを落とさず、やはり再登録しない。
	reg.err = fmt.Errorf("RequestError: send request failed")
	rewake("ECR blip re-wake")
	reg.err = nil
	if len(h.ecs.regCalls) != 1 {
		t.Fatalf("regCalls after a transient ECR failure = %d, want 1 — a blip must not force a deployment", len(h.ecs.regCalls))
	}

	// タグが動いた（可変タグへの再 push）: ここは必ず新しいリビジョンになる。
	reg.manifest, reg.digest = index("sha256:aaaa0002", "sha256:bbbb0001", false), "sha256:index0002"
	rewake("image moved")
	if len(h.ecs.regCalls) != 2 {
		t.Fatalf("regCalls after the tag moved = %d, want 2 — the stamp is NOT reaching the fingerprint, so the badge would never clear", len(h.ecs.regCalls))
	}
	if got := h.ecs.regCalls[1].ContainerDefinitions[0].DockerLabels[afImageStampLabel]; got == stamp {
		t.Errorf("the new revision carries the old stamp %q", got)
	}
}
