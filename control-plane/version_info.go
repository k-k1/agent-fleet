// version_info.go — 「いま動いているのはどの版か」に答える面（docs/35 §35.6.1）。
//
// `GET /api/version` は元々 `{"version": buildVersion}` だけを返していた。それで
// 足りるのはバイナリを直接配るデプロイ（native / compose）だけで、ECS ではコードは
// *イメージ* として届く。しかも CP と workspace は 30-ingress.yaml の単一 `ImageTag`
// から作られ、運用者が上げ下げするのはそのタグの方なので、「版」と「イメージ」は
// 別の問いになる。障害報告には両方が要る（Console のアカウントメニュー最下部）。
//
// 置き場をここ（既存ルートの追記）にしたのは意図的:
//   - `{"version": ...}` は追記なので既存の読み手（routes_test / docs/35 の実機手順）
//     が壊れない。
//   - 新しい REST を足すと agent 側と CP プロキシの allowlist の両方に登録が要る
//     （毎回それを忘れて 404 になる）。既存の CP 専用ルートならその面倒が無い。
//
// 判らない項目は **キーごと落とす**。docker / native にはタスクメタデータも
// `AF_ECS_WORKSPACE_IMAGE` も無いので、プロファイル文字列で分岐しなくても自然に
// 消える — 能力で判定する（golden_bake.go の流儀）。Console 側もキーの有無だけを
// 見て描くか描かないかを決める。
//
// ★ 版どうしを比べて「更新あり」と言ってはならない。CP と Agent の版が意図的に
//
//	ずれるのは正常な状態で、比較すると恒久点灯する（workspace_stale.go の禁じ手）。
//	ここは表示だけで、要再起動の判定は既存の stale 機構（runtime_ecs_stale.go）の
//	担当。二つ目のバッジをここから生やさない。
//
// ★ レジストリ host（`<account>.dkr.ecr.<region>.amazonaws.com`）は落として返す。
//
//	全メンバーが読む面に AWS アカウント ID を載せる理由が無い。タグと digest だけで
//	「どの実体か」は言えるし、ECR を引くのは運用者の手元で足りる。
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// imageInfo is one image as the Console is allowed to print it: repository name
// (registry host stripped), the tag it was deployed under, and the content digest
// when it is known. `:dev` は MUTABLE なので、タグだけでは実体を特定できない —
// digest はそのための添え物であって、比較材料ではない。
type imageInfo struct {
	Repo   string `json:"repo"`
	Tag    string `json:"tag,omitempty"`
	Digest string `json:"digest,omitempty"`
}

// versionPayload composes GET /api/version. Everything past "version" is best-effort
// and omitted when unknown.
func versionPayload(ctx context.Context, m *manager) map[string]any {
	out := map[string]any{"version": buildVersion}
	if m != nil {
		// Which KIND of deployment this is — free (pure function of the factory) and
		// the single most useful word in a bug report after the version itself.
		if rt := m.cloudCostProfile().Runtime; rt != "" {
			out["runtime"] = rt
		}
		// The image a Start would launch for a workspace. Only the ECS factories
		// implement WorkspaceImage(); on docker/native the workspace runs whatever
		// mgr.image names locally, which is not a deployed version and stays out.
		if f, ok := m.rtFactory.(interface{ WorkspaceImage() string }); ok {
			if ii := parseImageRef(f.WorkspaceImage(), ""); ii != nil {
				out["workspace_image"] = ii
			}
		}
	}
	// The image THIS control plane (and therefore the Console bundle it serves) was
	// launched from.
	if ii := controlPlaneImage(ctx); ii != nil {
		out["image"] = ii
	}
	return out
}

// --- the CP's own image ------------------------------------------------------------

// ecsMetadataTimeout bounds the task-metadata probe. It is a link-local endpoint on the
// same host and answers in milliseconds; the timeout exists so that a hung one costs the
// account menu a beat rather than the request.
const ecsMetadataTimeout = 2 * time.Second

// cpImageRetryAfter throttles retries after a failed probe. A SUCCESS is cached forever
// on purpose: the image a running task was launched from cannot change while that task
// lives, so re-asking could only produce the same answer.
const cpImageRetryAfter = 60 * time.Second

var cpImageCache struct {
	mu    sync.Mutex
	info  *imageInfo
	tried bool
	at    time.Time
}

// controlPlaneImage reports the image this CP task runs, or nil when that cannot be
// known (every non-ECS deployment, where the metadata endpoint does not exist).
func controlPlaneImage(ctx context.Context) *imageInfo {
	cpImageCache.mu.Lock()
	if cpImageCache.info != nil {
		info := cpImageCache.info
		cpImageCache.mu.Unlock()
		return info
	}
	if cpImageCache.tried && time.Since(cpImageCache.at) < cpImageRetryAfter {
		cpImageCache.mu.Unlock()
		return nil
	}
	cpImageCache.mu.Unlock()

	info := fetchECSContainerImage(ctx) // outside the lock: one slow probe blocks nobody
	cpImageCache.mu.Lock()
	cpImageCache.tried, cpImageCache.at = true, time.Now()
	if info != nil {
		cpImageCache.info = info
	}
	cpImageCache.mu.Unlock()
	return info
}

// fetchECSContainerImage reads the ECS task metadata endpoint (v4) for THIS container.
// It needs no IAM and no AWS SDK — the agent injects the URL into the container's
// environment and serves it over the link-local address — which is why the CP can name
// its own image on a deployment where nothing else tells it (the CFN passes ImageTag to
// the container's Image, not to an env var).
func fetchECSContainerImage(ctx context.Context) *imageInfo {
	base := os.Getenv("ECS_CONTAINER_METADATA_URI_V4")
	if base == "" {
		base = os.Getenv("ECS_CONTAINER_METADATA_URI") // v3 (older agents)
	}
	if base == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, ecsMetadataTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var meta struct {
		Image   string `json:"Image"`   // e.g. <acct>.dkr.ecr.<rg>.amazonaws.com/af-control-plane:0.6.0
		ImageID string `json:"ImageID"` // the digest ECS resolved that tag to at pull time
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta); err != nil {
		return nil
	}
	return parseImageRef(meta.Image, meta.ImageID)
}

// --- reference parsing -------------------------------------------------------------

// parseImageRef splits an image reference into what the Console may print, dropping the
// registry host. digest is the separately-known content digest (metadata ImageID); a
// digest carried by the reference itself wins nothing over it — either identifies the
// same content — so the explicit one is preferred and the embedded one is the fallback.
//
// nil for an empty/unusable reference: the caller's answer for "unknown" is to omit the
// key, never to print a half-parsed string.
func parseImageRef(ref, digest string) *imageInfo {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	info := &imageInfo{Digest: strings.TrimSpace(digest)}
	if i := strings.Index(ref, "@"); i >= 0 {
		if info.Digest == "" {
			info.Digest = ref[i+1:]
		}
		ref = ref[:i]
	}
	// A ":" is the tag separator only when nothing after it looks like a path — that is
	// what keeps a registry port (localhost:5000/foo) from being read as a tag.
	if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i+1:], "/") {
		info.Repo, info.Tag = ref[:i], ref[i+1:]
	} else {
		info.Repo = ref
	}
	// Strip the registry host: the first path element is a host when it carries a "."
	// or a ":" (the Docker reference rule), which is exactly the ECR case.
	if i := strings.Index(info.Repo, "/"); i > 0 && strings.ContainsAny(info.Repo[:i], ".:") {
		info.Repo = info.Repo[i+1:]
	}
	if info.Repo == "" {
		return nil
	}
	return info
}
