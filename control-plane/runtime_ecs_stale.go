// runtime_ecs_stale.go — ECS 系（Fargate = `ecs` / EC2 スロット = `ecs-ec2`）の
// 「いま停止→起動したら、走るコードが変わるか」判定。workspace_stale.go の
// staleRuntime 実装で、Console の WS バー「要再起動」バッジと更新トーストの
// 「BE も更新済み」行はこれが true になって初めて出る。
//
// docker / native には最初から実装があり、ECS だけが「未実装＝常に false」だった。
// 結果として ecs / ecs-ec2 のデプロイでは、新しい workspace イメージを出しても
// 利用者には何も見えず、走り続けているタスクが古いままなのに誰も気づかない
// （タスク定義は Start のたびに作り直されるので、反映は停止→起動が要る）。
//
// 判定則は docker / native と同じひとつだけ:
//
//	起動時に実際に使った実体と、いま Start したら使う実体を比べる。
//
// ECS でこれを成立させるための置き場が二つある。
//
//   - 「起動時に使った実体」= Start が登録したタスク定義リビジョンに焼いた指紋。
//     この adapter は CP 側に状態を持たない（ADR 0012）ので、docker の
//     <dataDir>/image.rootfs-stamp に当たるものが要る。タスク定義は Start ごとに
//     必ず新規登録される（registerTaskDef）ので、そこの DockerLabels に載せれば
//     「そのリビジョンで起動した実体」と一対一で対応し、CP が再起動しても残る。
//   - 「いま Start したら使う実体」= AF_ECS_WORKSPACE_IMAGE のタグを ECR に
//     いま問い合わせた結果。
//
// ★ 版比較は禁じ手（workspace_stale.go の注記）。CP の版と Agent の申告版を
//
//	比べると、版が意図的にずれる正常状態で恒久点灯する。ここでも版は見ない。
//
// ★ 二辺比較も禁じ手。「走っているタスクの containers[].imageDigest」と
//
//	「タグを ECR に引いた digest」は別の問い合わせが返す別表現になり得る
//	（インデックス digest とプラットフォーム manifest digest）。だから両辺とも
//	imageFingerprint() という同じ関数の結果にする。
//
// ★ そして digest そのものを実体にしてはならない。digest は内容ではなく表現で、
//
//	内容が変わらなくても動く（docker では provenance の付け直しで実測）。
//	マルチプラットフォームのインデックスはまさにそれが起きる層なので、
//	manifestFingerprint はインデックスを一段ほどいて「実プラットフォームの
//	manifest digest の集合」を指紋にする。attestation manifest（platform が
//	unknown/unknown、あるいは vnd.docker.reference.type 注釈つき）は落とすので、
//	provenance だけ付け直した再 push では指紋が動かない。単一 manifest の場合は
//	その digest 自身が config + layers の内容ハッシュなのでそのまま使える。
//
// 判らないときは必ず false（＝要再起動と言わない）に倒す。ECR ではないレジストリ、
// 権限不足、まだ指紋を焼いていない古いリビジョン — どれも「不明」であって
// 「更新された」ではない。
package main

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

// afImageStampLabel is the docker label registerTaskDef writes onto the workspace
// container definition: the fingerprint of the image content this Start launched from.
// A label rather than a task-definition tag so reading it back needs no `include=TAGS`
// (and no ecs:TagResource on a path that runs on every /api/workspace poll).
const afImageStampLabel = "af.image.fingerprint"

// ecsStaleTTL bounds how often the two probes actually hit AWS. /api/workspace is
// polled every 4s per open Console and pushed over SSE, so an uncached BatchGetImage
// per request would multiply across tabs and users. Both facts only move on a push or
// a Start, and Start primes the cache with what it just learned (stampImage), so the
// TTL never delays the badge CLEARING after a restart — only its appearance.
const ecsStaleTTL = 60 * time.Second

// ecrAPI is the narrow ECR port (one read-only call), so the runtime stays testable
// against a fake. The real *ecr.Client satisfies it.
type ecrAPI interface {
	BatchGetImage(context.Context, *ecr.BatchGetImageInput, ...func(*ecr.Options)) (*ecr.BatchGetImageOutput, error)
}

// ecrManifestMediaTypes must be passed explicitly: ECR's default accepted media type
// is the schema-1 manifest, which would come back converted and useless for a content
// comparison. Listing all four (docker v2 + OCI, manifest + index) makes the reply the
// manifest that was actually pushed.
var ecrManifestMediaTypes = []string{
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
}

func ecrFingerprintKey(image string) string { return "ecs-img:" + image }
func ecsStampKey(service string) string     { return "ecs-stamp:" + service }

// Stale reports whether the workspace image tag now resolves to different CONTENT than
// the one the running task definition was registered against — i.e. a stop→start would
// swap in new backend code while the live task keeps the old one.
//
// Unknown → false, on both sides: no stamp (a task definition registered before this
// existed, or a deployment whose image is not in ECR), no readable tag (deleted,
// AccessDenied), not an ECR reference at all. Never nag on a guess.
func (e *ecsRuntime) Stale(ctx context.Context) bool {
	was := freshness.get(ecsStampKey(e.name), ecsStaleTTL, func() string { return e.runningImageStamp(ctx) })
	if was == "" {
		return false
	}
	now := freshness.get(ecrFingerprintKey(e.cfg.workspaceImage), ecsStaleTTL, func() string { return e.imageFingerprint(ctx) })
	return now != "" && now != was
}

// Stale on the EC2 launch type is the same question with the same answer: both adapters
// register one task definition family per workspace and both launch e.cfg.workspaceImage,
// so the base implementation reads the right revision. Written out rather than inherited
// because ecsEC2Runtime composes (never embeds) its base — see the type comment.
func (e *ecsEC2Runtime) Stale(ctx context.Context) bool { return e.base.Stale(ctx) }

// stampImage computes the fingerprint of the image the tag resolves to RIGHT NOW and
// returns it as the container's docker labels (nil when unknown, so the task definition
// carries no misleading empty stamp). Called from registerTaskDef on both launch types.
//
// It also primes both TTL caches with what it just learned — the same trick the docker
// adapter plays: a cached PRE-push fingerprint (or the previous revision's stamp) would
// otherwise make a freshly started workspace look stale for up to a minute, which is
// exactly the moment the user is looking at the badge they just acted on.
func (e *ecsRuntime) stampImage(ctx context.Context) map[string]string {
	fp := e.imageFingerprint(ctx)
	freshness.set(ecrFingerprintKey(e.cfg.workspaceImage), fp)
	freshness.set(ecsStampKey(e.name), fp)
	if fp == "" {
		return nil
	}
	return map[string]string{afImageStampLabel: fp}
}

// runningImageStamp reads back the fingerprint stamped into the task definition the
// service currently runs. Start always registers a fresh revision and points the service
// at it (upsertService), so the service's task definition IS what the running task was
// launched from.
func (e *ecsRuntime) runningImageStamp(ctx context.Context) string {
	s, ok, err := e.describeService(ctx)
	if err != nil || !ok {
		return ""
	}
	td := aws.ToString(s.TaskDefinition)
	if td == "" {
		return ""
	}
	out, err := e.ecs.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: aws.String(td)})
	if err != nil || out.TaskDefinition == nil {
		return ""
	}
	for _, c := range out.TaskDefinition.ContainerDefinitions {
		if v := c.DockerLabels[afImageStampLabel]; v != "" {
			return v
		}
	}
	return ""
}

// imageFingerprint resolves cfg.workspaceImage against ECR and returns its content
// identity. "" means unknown (not an ECR ref, call failed, tag gone) — never an error,
// because every caller's right answer for unknown is "do not report a change".
func (e *ecsRuntime) imageFingerprint(ctx context.Context) string {
	if e.ecr == nil {
		return ""
	}
	ref, ok := parseECRRef(e.cfg.workspaceImage)
	if !ok {
		return ""
	}
	// A digest reference cannot move, so it IS its own fingerprint — and asking ECR
	// would only be able to disagree with itself.
	if ref.digest != "" {
		return ref.digest
	}
	out, err := e.ecr.BatchGetImage(ctx, &ecr.BatchGetImageInput{
		RegistryId:         aws.String(ref.registryID),
		RepositoryName:     aws.String(ref.repository),
		ImageIds:           []ecrtypes.ImageIdentifier{{ImageTag: aws.String(ref.tag)}},
		AcceptedMediaTypes: ecrManifestMediaTypes,
	})
	if err != nil || len(out.Images) == 0 {
		return ""
	}
	img := out.Images[0]
	var digest string
	if img.ImageId != nil {
		digest = aws.ToString(img.ImageId.ImageDigest)
	}
	return manifestFingerprint(aws.ToString(img.ImageManifest), digest)
}

// manifestFingerprint turns a pushed manifest into a content identity.
//
//   - single manifest (no `manifests` array): its own digest, which hashes the config
//     and the layer list — the content, not a representation of it.
//   - manifest list / OCI index: the per-platform manifest digests, sorted and joined.
//     Attestation entries are dropped (platform unknown/unknown, or the docker
//     reference-type annotation), which is what makes a provenance-only re-push silent.
//
// "" when the manifest cannot be parsed, or when an index carries no real platform.
func manifestFingerprint(manifest, digest string) string {
	var idx struct {
		Manifests []struct {
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
			Platform    *struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal([]byte(manifest), &idx); err != nil {
		return ""
	}
	if len(idx.Manifests) == 0 {
		return digest
	}
	var parts []string
	for _, m := range idx.Manifests {
		if m.Digest == "" || m.Platform == nil {
			continue
		}
		if m.Platform.Architecture == "" || m.Platform.Architecture == "unknown" || m.Platform.OS == "unknown" {
			continue
		}
		if m.Annotations["vnd.docker.reference.type"] != "" {
			continue
		}
		parts = append(parts, m.Platform.OS+"/"+m.Platform.Architecture+"="+m.Digest)
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// ecrRef is a parsed ECR image reference.
type ecrRef struct {
	registryID string
	region     string
	repository string
	tag        string
	digest     string
}

// ecrRefRe matches the ECR registry host and captures the account, the region and the
// whole path after it. The repository part may contain slashes, so the split of
// repository from tag/digest is done afterwards rather than in the pattern.
var ecrRefRe = regexp.MustCompile(`^([0-9]{12})\.dkr\.ecr(?:-fips)?\.([a-z0-9-]+)\.amazonaws\.com(?:\.cn)?/(.+)$`)

// parseECRRef splits an ECR image URI into its parts. Not an ECR reference (Docker Hub,
// GHCR, a bare name) → ok=false, which the caller turns into "unknown" rather than an
// error: those deployments simply get no badge, exactly as before.
func parseECRRef(uri string) (ecrRef, bool) {
	m := ecrRefRe.FindStringSubmatch(strings.TrimSpace(uri))
	if m == nil {
		return ecrRef{}, false
	}
	r := ecrRef{registryID: m[1], region: m[2]}
	rest := m[3]
	if i := strings.Index(rest, "@"); i >= 0 {
		r.repository, r.digest = rest[:i], rest[i+1:]
		return r, r.repository != "" && r.digest != ""
	}
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		r.repository, r.tag = rest[:i], rest[i+1:]
	} else {
		r.repository, r.tag = rest, "latest"
	}
	return r, r.repository != "" && r.tag != ""
}
