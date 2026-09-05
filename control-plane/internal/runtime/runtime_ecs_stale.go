// runtime_ecs_stale.go — the staleRuntime implementation (workspace_stale.go)
// for the ECS profiles, `ecs` (Fargate) and `ecs-ec2` (EC2 slots): would a
// stop→start right now run different code? The Console's "restart required"
// badge and the update toast's "backend updated too" line appear only once this
// answers true. A task definition is re-registered on every Start, so picking up
// a new image does require the stop→start.
//
// One rule, the same one docker and native use:
//
//	compare the image content this workspace actually started from against
//	the content a Start would use now.
//
// On ECS each side has a place to live:
//
//   - Started from: a fingerprint baked into the task-definition revision Start
//     registered. This adapter keeps no state on the CP side (ADR 0012), so it
//     needs an equivalent of docker's <dataDir>/image.rootfs-stamp. Start always
//     registers a fresh revision (registerTaskDef), so a DockerLabel there
//     corresponds one-to-one with that launch and survives a CP restart.
//   - Would use now: the AF_ECS_WORKSPACE_IMAGE tag, resolved against ECR.
//
// Three things this must not do.
//
//   - Never compare versions (see workspace_stale.go). CP and Agent versions
//     drift on purpose, so comparing them lights the badge permanently in a
//     healthy deployment.
//   - Never compare the two sides through different queries. A running task's
//     containers[].imageDigest and a tag resolved in ECR can be different
//     representations of the same image — an index digest and a platform
//     manifest digest — so both sides go through imageFingerprint().
//   - Never treat a digest itself as the content. A digest is a representation,
//     and it moves while the content does not: re-pushing provenance alone is
//     enough (measured on docker), and a multi-platform index is exactly the
//     layer where that happens. So manifestFingerprint unwraps the index one
//     level and fingerprints the set of real platform manifest digests, dropping
//     attestation manifests (platform unknown/unknown, or carrying the
//     vnd.docker.reference.type annotation). A single manifest's own digest
//     already hashes config + layers, so it is used as is.
//
// When in doubt, answer false (do not claim a restart is needed). A non-ECR
// registry, missing permissions and a revision registered before the fingerprint
// existed are all "unknown", not "updated".
package runtime

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
	was := Freshness.get(ecsStampKey(e.name), ecsStaleTTL, func() string { return e.runningImageStamp(ctx) })
	if was == "" {
		return false
	}
	now := Freshness.get(ecrFingerprintKey(e.cfg.workspaceImage), ecsStaleTTL, func() string { return e.imageFingerprint(ctx) })
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
//
// ⚠️ The probe is fresh (not read through the TTL cache), because a stamp taken from a
// value up to a minute old would be a lie about what this task launched from. But when
// it fails we fall back to the LAST KNOWN fingerprint rather than to "unknown", and that
// is not cosmetic: on the EC2 launch type this map is part of what taskDefFingerprint
// hashes, so letting a transient ECR blip drop the label would change the fingerprint,
// miss the task-definition reuse (reuseOrRegisterTaskDef) and force a deployment — the
// 1-2 minute Service Connect window that commit 7ae97ea1 exists to avoid. With no
// previous value at all it stays empty, i.e. "unknown", which is the safe side.
func (e *ecsRuntime) stampImage(ctx context.Context) map[string]string {
	key := ecrFingerprintKey(e.cfg.workspaceImage)
	fp := e.imageFingerprint(ctx)
	if fp == "" {
		fp = Freshness.peek(key)
	}
	Freshness.set(key, fp)
	Freshness.set(ecsStampKey(e.name), fp)
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
	return ecrImageFingerprint(ctx, e.ecr, e.cfg.workspaceImage)
}

// ecrImageFingerprint is the body of the above, with the image and the client passed in
// rather than read off a workspace runtime. The golden baker asks the same question
// about the same image from the POOL side (runtime_ecs_ec2_golden.go), where there is
// no per-workspace runtime to ask — and two ways of computing "the content this tag
// resolves to" would be two things to keep in step (the badge's own doc comment says
// why both sides of a comparison must come from one function).
func ecrImageFingerprint(ctx context.Context, api ecrAPI, image string) string {
	if api == nil {
		return ""
	}
	ref, ok := parseECRRef(image)
	if !ok {
		return ""
	}
	// A digest reference cannot move, so it IS its own fingerprint — and asking ECR
	// would only be able to disagree with itself.
	if ref.digest != "" {
		return ref.digest
	}
	out, err := api.BatchGetImage(ctx, &ecr.BatchGetImageInput{
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
