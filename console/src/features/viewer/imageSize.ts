// imageSize — a picture's real width and height, read by the Workspace agent from the file's
// header (POST fs/imagesize, workspace/agent/fs_imagesize.go; ADR 0080 decision 16).
//
// Never from a thumbnail: `naturalWidth` of a `thumb=` copy is the size after an integer
// downscale, and right only for the small images served as originals — sometimes right is
// worse than always wrong.
//
// What keeps a 500-image folder from becoming 500 requests:
//   - callers ask per card, but only once a card is near the viewport (the gallery's
//     `useArmed`), and every ask in the same BATCH_WAIT_MS window rides one request;
//   - one request at a time, BATCH_MAX paths each (the Agent's own cap); asks that arrive
//     meanwhile wait for the next;
//   - answers are memoized per (path, mtime), so walking back into a folder asks nothing.
//
// Two things the memo must never do:
//   - answer for another workspace. The tenant can be switched without a reload, and the same
//     relative path names a different file there, so every key and every batch carries the
//     (user, tenant) it was asked under, and a batch is sent to THAT tenant explicitly.
//   - outlive a file it cannot see change. Without an mtime (the mirror's lightbox has none) a
//     key names a path, not a revision, so such an answer is only shared while it is in flight
//     and every new look asks again.
import { useEffect, useState } from "react";
import { api, getTenant, getUser } from "../../core/api/client.ts";

export interface ImageSize {
  w: number;
  h: number;
}

/** Must not exceed the Agent's imageSizeMaxPaths: past it, paths come back unknown. */
export const BATCH_MAX = 100;
const BATCH_WAIT_MS = 30;
const MAX_ENTRIES = 2000; // oldest evicted, like pathResolve's memo

const scopeOf = () => getUser() + "\u0000" + getTenant();
const keyOf = (scope: string, path: string, mtime?: number) => scope + "\u0000" + path + "\u0000" + (mtime ?? "");

/** Settled answers; `null` is "asked, and the Agent could not say" (not an image it reads). */
const known = new Map<string, ImageSize | null>();
const waiting = new Map<string, Promise<ImageSize | null>>();
let queue: {
  path: string;
  key: string;
  scope: string;
  tenant: string;
  /** Only an answer about a known revision may be kept after it settles. */
  memo: boolean;
  done: (v: ImageSize | null) => void;
}[] = [];
let timer: ReturnType<typeof setTimeout> | null = null;
let inFlight = false;

function remember(key: string, v: ImageSize | null) {
  known.delete(key);
  known.set(key, v);
  while (known.size > MAX_ENTRIES) {
    for (const oldest of known.keys()) {
      known.delete(oldest);
      break;
    }
  }
}

function flush() {
  timer = null;
  if (inFlight || queue.length === 0) return;
  // One scope per request: asks made under another tenant wait for a request of their own.
  const scope = queue[0].scope;
  const tenant = queue[0].tenant;
  const batch = queue.filter((q) => q.scope === scope).slice(0, BATCH_MAX);
  const sent = new Set(batch);
  queue = queue.filter((q) => !sent.has(q));
  inFlight = true;
  const paths = [...new Set(batch.map((b) => b.path))];
  // The tenant the asks were made under, not whichever is selected by the time the window
  // closes: the fetch wrapper only fills X-AF-Tenant in when the request has none.
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (tenant) headers["X-AF-Tenant"] = tenant;
  api("api/fs/imagesize", { method: "POST", headers, body: JSON.stringify({ paths }) })
    .then((d: { sizes?: Record<string, ImageSize>; error?: unknown } | null) => {
      // A stopped workspace or a proxy hiccup is not "this file has no size": nothing is
      // remembered, so the next look asks again.
      const failed = !d || d.error !== undefined;
      for (const b of batch) {
        const s = d?.sizes?.[b.path];
        const v = s && s.w > 0 && s.h > 0 ? { w: s.w, h: s.h } : null;
        if (!failed && b.memo) remember(b.key, v);
        waiting.delete(b.key);
        b.done(v);
      }
    })
    .catch(() => {
      for (const b of batch) {
        waiting.delete(b.key);
        b.done(null);
      }
    })
    .finally(() => {
      inFlight = false;
      if (queue.length) flush();
    });
}

/** The size if it is already known — `undefined` when it has not been asked yet. */
export function knownImageSize(path: string, mtime?: number): ImageSize | null | undefined {
  return known.get(keyOf(scopeOf(), path, mtime));
}

/** Ask for one picture's size; batched with every other ask in the same short window. */
export function imageSize(path: string, mtime?: number): Promise<ImageSize | null> {
  const scope = scopeOf();
  const key = keyOf(scope, path, mtime);
  const hit = known.get(key);
  if (hit !== undefined) return Promise.resolve(hit);
  const pending = waiting.get(key);
  if (pending) return pending;
  const memo = mtime !== undefined;
  const p = new Promise<ImageSize | null>((done) => queue.push({ path, key, scope, tenant: getTenant(), memo, done }));
  waiting.set(key, p);
  if (!timer && !inFlight) timer = setTimeout(flush, BATCH_WAIT_MS);
  return p;
}

/** The size of `path`, asked for only while `enabled` (a card that has come near the viewport). */
export function useImageSize(path: string | null, mtime?: number, enabled = true): ImageSize | null {
  // What this mount was told, held by the key it was asked under: without an mtime nothing is
  // memoized, and an answer from before a tenant switch must not be shown after it.
  const key = path ? keyOf(scopeOf(), path, mtime) : "";
  const [got, setGot] = useState<{ key: string; v: ImageSize | null } | null>(null);
  const cached = path ? known.get(key) : null;
  useEffect(() => {
    if (!path || !enabled || cached !== undefined) return;
    let alive = true;
    void imageSize(path, mtime).then((v) => alive && setGot({ key, v }));
    return () => {
      alive = false;
    };
  }, [key, path, mtime, enabled, cached]);
  if (cached !== undefined) return cached;
  return got?.key === key ? got.v : null;
}

/** "832×1216" — W×H the way the file viewer's info bar writes it. */
export const formatImageSize = (s: ImageSize): string => `${s.w}×${s.h}`;

/** Tests only: the memo is module-level and would otherwise leak from one test to the next. */
export function clearImageSizeCache() {
  known.clear();
  waiting.clear();
  queue = [];
  if (timer) clearTimeout(timer);
  timer = null;
  inFlight = false;
}
