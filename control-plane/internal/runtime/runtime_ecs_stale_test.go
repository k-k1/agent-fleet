// runtime_ecs_stale_test.go — contract tests for the ECS "restart required" check. They
// pin that the two traps docker already fell into (comparing the two sides through
// different queries, and comparing digests) are not repeated here, and that an unknown
// answer stays silent.
package runtime

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

// fakeECR is a registry that answers BatchGetImage and nothing else. Swapping its manifest
// and digest is how "the tag moved" and "re-pushed with identical content" are told apart.
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

// index builds a multi-platform index manifest. With attest set it appends buildx's
// attestation manifest (platform unknown/unknown) — the path where the index digest moves
// while the content does not.
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

// TestECSStaleImageStamp walks the whole shape of the check: the fingerprint Start stamped
// against the fingerprint the tag resolves to now. Choosing a running task's
// containers[].imageDigest or an index digest as the content would light the badge
// permanently whenever a representation moves under identical content — the trap docker fell
// into twice. What this test guards is that a provenance-only re-push stays silent.
func TestECSStaleImageStamp(t *testing.T) {
	orig := Freshness
	defer func() { Freshness = orig }()
	now := time.Unix(1000, 0)
	Freshness = &TTLCache{m: map[string]TTLEntry{}, now: func() time.Time { return now }}

	const (
		amd64Old = "sha256:aaaa0001"
		arm64Old = "sha256:bbbb0001"
		amd64New = "sha256:aaaa0002"
	)
	reg := &fakeECR{manifest: index(amd64Old, arm64Old, false), digest: "sha256:index0001"}
	rt, fe := newStaleTestECS(t, reg)
	ctx := context.Background()

	// Nothing started yet (no service) = unknown → false.
	if rt.Stale(ctx) {
		t.Fatal("no service: stale, want false")
	}
	now = now.Add(2 * ecsStaleTTL)

	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// AcceptedMediaTypes is mandatory: without it ECR answers with a manifest converted to
	// schema 1, and the content comparison has nothing left to stand on.
	if len(reg.lastIn.AcceptedMediaTypes) == 0 {
		t.Error("BatchGetImage without AcceptedMediaTypes — ECR would answer with a converted schema-1 manifest")
	}
	if got := fe.regCalls[0].ContainerDefinitions[0].DockerLabels[afImageStampLabel]; got == "" {
		t.Fatal("Start registered a task definition without an image stamp")
	}
	if rt.Stale(ctx) {
		t.Fatal("same image: stale, want false")
	}

	// An attestation was added, moving only the index digest while not a byte of content
	// changed: a stop→start would run the same code, so nothing may light.
	reg.manifest, reg.digest = index(amd64Old, arm64Old, true), "sha256:index0002"
	now = now.Add(2 * ecsStaleTTL)
	if rt.Stale(ctx) {
		t.Fatal("provenance-only re-push (same platform manifests): stale, want false")
	}

	// A genuinely new image was pushed to the same tag → report stale.
	reg.manifest, reg.digest = index(amd64New, arm64Old, true), "sha256:index0003"
	now = now.Add(2 * ecsStaleTTL)
	if !rt.Stale(ctx) {
		t.Fatal("tag moved to new content: not stale, want stale")
	}

	// Right after the user acts on the badge it must clear at once, whatever the TTL has
	// left, because Start overwrites the cache with what it just stamped.
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if rt.Stale(ctx) {
		t.Fatal("restarted onto the new image: stale, want false")
	}

	// Unreadable tag (AccessDenied, tag deleted) = unknown → false.
	reg.err = fmt.Errorf("AccessDeniedException: not authorized to perform ecr:BatchGetImage")
	now = now.Add(2 * ecsStaleTTL)
	if rt.Stale(ctx) {
		t.Fatal("unreadable tag: stale, want false")
	}
}

// TestECSStaleUnknownNeverNags pins every path that must fall to the unknown side. Any one
// of them returning true produces a badge that stays lit however often it is acted on, and
// that costs the badge as a whole its credibility.
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
			orig := Freshness
			defer func() { Freshness = orig }()
			Freshness = &TTLCache{m: map[string]TTLEntry{}}

			reg := &fakeECR{manifest: index("sha256:aaaa0001", "sha256:bbbb0001", false), digest: "sha256:index0001"}
			rt, fe := newStaleTestECS(t, reg)
			if err := rt.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			tc.setup(rt, fe)
			// Drop the cache Start primed, so the bare check is what is measured.
			Freshness = &TTLCache{m: map[string]TTLEntry{}}
			// The tag has moved, i.e. a working check would answer true here.
			reg.manifest, reg.digest = index("sha256:aaaa9999", "sha256:bbbb0001", false), "sha256:index9999"
			if rt.Stale(ctx) {
				t.Fatal("stale, want false")
			}
		})
	}
}

// TestECSEC2StaleDelegatesToBase pins the delegation: ecs-ec2 uses the same check (same
// image, same task-definition family). If the delegation is dropped, only the EC2 slot
// profile silently loses detection.
func TestECSEC2StaleDelegatesToBase(t *testing.T) {
	orig := Freshness
	defer func() { Freshness = orig }()
	Freshness = &TTLCache{m: map[string]TTLEntry{}}

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
	Freshness = &TTLCache{m: map[string]TTLEntry{}}
	if !rt.Stale(ctx) {
		t.Fatal("tag moved: not stale, want stale")
	}
}

func TestManifestFingerprint(t *testing.T) {
	// For a single manifest the digest itself hashes config + layers, i.e. the content.
	single := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",` +
		`"config":{"digest":"sha256:cfg"},"layers":[{"digest":"sha256:l1"}]}`
	if got := manifestFingerprint(single, "sha256:single01"); got != "sha256:single01" {
		t.Errorf("single manifest: %q", got)
	}
	// An index lists only the real platform manifests, order-independently.
	a := manifestFingerprint(index("sha256:a1", "sha256:b1", false), "sha256:idx1")
	b := manifestFingerprint(index("sha256:b1", "sha256:a1", false), "sha256:idx2") // amd64/arm64 swapped
	if a == b {
		t.Error("platform assignment ignored — swapping which arch has which digest must change the fingerprint")
	}
	if withAttest := manifestFingerprint(index("sha256:a1", "sha256:b1", true), "sha256:idx3"); withAttest != a {
		t.Errorf("attestation entry leaked into the fingerprint: %q != %q", withAttest, a)
	}
	// A broken or empty manifest is unknown.
	if got := manifestFingerprint("", "sha256:x"); got != "" {
		t.Errorf("unparseable manifest: %q, want empty", got)
	}
	// So is an index with no real platform in it (attestations only).
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

// TestECSEC2TaskDefReuseSeesTheImageStamp pins a combination a merge can quietly break: the
// task-definition reuse cache (commit 7ae97ea1, which keeps a re-wake onto the same slot
// from re-registering and forcing a deployment) and this fingerprint stamp.
//
// The stamp is one of the inputs taskDefFingerprint folds, and that is required, not
// incidental: with a mutable tag (:dev) the image string never moves, so the stamp is the
// only thing that tells the fingerprint the tag's content changed. Drop it from the
// fingerprint and a re-wake after an image push reuses the old revision — the task does pull
// the new image, but the revision keeps the old stamp, giving a badge no number of restarts
// can clear.
//
// The converse must hold too: a transient ECR failure must not move the fingerprint, or the
// unnecessary forced deployment brings back the 1-2 minute Service Connect window 7ae97ea1
// removed.
func TestECSEC2TaskDefReuseSeesTheImageStamp(t *testing.T) {
	origFresh := Freshness
	defer func() { Freshness = origFresh }()
	Freshness = &TTLCache{m: map[string]TTLEntry{}}

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
		// Mimic the real service settling at desiredCount 0, as the existing tests do.
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

	// An unchanged re-wake: neither a re-registration nor a forced deployment.
	rewake("unchanged re-wake")
	if len(h.ecs.regCalls) != 1 {
		t.Fatalf("regCalls after an unchanged re-wake = %d, want 1 — the stamp is churning the fingerprint", len(h.ecs.regCalls))
	}

	// A re-wake while ECR is briefly unreadable: keep the stamp, still no re-registration.
	reg.err = fmt.Errorf("RequestError: send request failed")
	rewake("ECR blip re-wake")
	reg.err = nil
	if len(h.ecs.regCalls) != 1 {
		t.Fatalf("regCalls after a transient ECR failure = %d, want 1 — a blip must not force a deployment", len(h.ecs.regCalls))
	}

	// The tag moved (a re-push onto the mutable tag): this must produce a new revision.
	reg.manifest, reg.digest = index("sha256:aaaa0002", "sha256:bbbb0001", false), "sha256:index0002"
	rewake("image moved")
	if len(h.ecs.regCalls) != 2 {
		t.Fatalf("regCalls after the tag moved = %d, want 2 — the stamp is NOT reaching the fingerprint, so the badge would never clear", len(h.ecs.regCalls))
	}
	if got := h.ecs.regCalls[1].ContainerDefinitions[0].DockerLabels[afImageStampLabel]; got == stamp {
		t.Errorf("the new revision carries the old stamp %q", got)
	}
}
