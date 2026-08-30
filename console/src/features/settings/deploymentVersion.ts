import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";

// Deployment build identity — the answer to "which version is this, and which image is
// it running" (docs/log/35 §35.6.1, CP side in version_info.go).
//
// It is a DIFFERENT thing from lib/version.ts: that stamp is the Console bundle in this
// tab. On ECS the code arrives as an image, and what an operator deploys is the ImageTag
// — so a bug report needs both, and the account menu prints them together.
export interface ImageInfo {
  /** Repository name; the registry host (and with it the AWS account id) is stripped CP-side. */
  repo: string;
  tag?: string;
  /** Full content digest ("sha256:…") when known — `:dev` is mutable, so the tag alone is not an identity. */
  digest?: string;
}

export interface DeploymentVersion {
  version: string;
  /** Deployment kind ("local" | "native" | "ecs" | "ecs-ec2"). */
  runtime?: string;
  /** The image the control plane (and the Console bundle it serves) runs. ECS only. */
  image?: ImageInfo;
  /** The image a workspace start would launch. ECS only. */
  workspace_image?: ImageInfo;
}

// One fetch per tab, shared by every caller: none of this moves while the CP serves the
// tab (a CP that rolled to a new image is a new CP, and the update toast reloads us).
let pending: Promise<DeploymentVersion | null> | null = null;

function load(): Promise<DeploymentVersion | null> {
  pending ??= api("api/version")
    .then((res) => (res && !res.error && typeof res.version === "string" ? (res as DeploymentVersion) : null))
    .catch(() => null);
  return pending;
}

/** Test seam: drop the per-tab cache. */
export function resetDeploymentVersionCache(): void {
  pending = null;
}

// useDeploymentVersion — fetched lazily (pass enabled=true when the surface that shows
// it actually opens) rather than at boot: nobody needs it until they look, and every
// boot request is paid by every tab of every user (docs Console↔CP traffic).
// null while loading and on any failure, so callers render nothing.
export function useDeploymentVersion(enabled: boolean): DeploymentVersion | null {
  const [st, setSt] = useState<DeploymentVersion | null>(null);
  useEffect(() => {
    if (!enabled) return;
    let live = true;
    void load().then((v) => {
      if (live) setSt(v);
    });
    return () => {
      live = false;
    };
  }, [enabled]);
  return st;
}

/** "af-workspace:0.6.0 (cafe123)" — compact enough for the menu, specific enough to act on. */
export function imageLabel(img: ImageInfo): string {
  const ref = img.tag ? `${img.repo}:${img.tag}` : img.repo;
  const short = shortDigest(img.digest);
  return short ? `${ref} (${short})` : ref;
}

/** The first 7 hex chars of the digest, git-style. "" when there is none. */
export function shortDigest(digest?: string): string {
  const hex = (digest || "").split(":").pop() || "";
  return hex.slice(0, 7);
}
