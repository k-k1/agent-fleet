// Web Share Target glue (docs/log/21 image attachments). registerShareSW() installs the minimal
// service worker (public/sw.js) that receives Android share-sheet POSTs; consumeShare()
// runs on the memo panel's mount, reads the stash the SW left in CacheStorage for a
// ?share=<id> launch, evicts it, and returns the shared text + image File objects for
// the composer to prefill and upload. Keys are origin-absolute and match the SW's.

const SHARE_CACHE = "af-share-v1";
const dataKey = (id: string) => `${location.origin}/__afshare__/${id}/data`;
const fileKey = (id: string, n: number) => `${location.origin}/__afshare__/${id}/f${n}`;

// registerShareSW installs the service worker so the PWA becomes a share target. The
// script URL is relative to <base> (works behind a path-stripping proxy) and its scope
// is the app root. Best-effort: no SW (unsupported / insecure origin) just means the
// share entry point isn't offered — everything else in the Console is unaffected.
export function registerShareSW(): void {
  if (!("serviceWorker" in navigator)) return;
  try {
    const url = new URL("sw.js", document.baseURI).toString();
    void navigator.serviceWorker.register(url, { scope: "./" }).catch(() => {});
  } catch {
    /* insecure origin / blocked — ignore */
  }
}

export interface SharePayload {
  text: string; // title / text / url joined, ready to seed the composer body
  files: File[]; // shared images to upload as memo attachments
}

// consumeShare returns the payload for a ?share=<id> launch (or null). It strips the
// param first (so a reload can't re-consume) and deletes the cache entries after reading.
export async function consumeShare(): Promise<SharePayload | null> {
  const params = new URLSearchParams(location.search);
  const id = params.get("share");
  if (!id) return null;
  params.delete("share");
  const qs = params.toString();
  history.replaceState(null, "", location.pathname + (qs ? "?" + qs : "") + location.hash);

  if (!("caches" in window)) return null;
  try {
    const cache = await caches.open(SHARE_CACHE);
    const dRes = await cache.match(dataKey(id));
    if (!dRes) return null;
    const meta = (await dRes.json()) as { title?: string; text?: string; url?: string; names?: string[] };
    const names = meta.names || [];
    const files: File[] = [];
    for (let i = 0; i < names.length; i++) {
      const fRes = await cache.match(fileKey(id, i));
      if (!fRes) continue;
      const blob = await fRes.blob();
      files.push(new File([blob], names[i] || `shared-${i}.png`, { type: blob.type }));
    }
    void cache.delete(dataKey(id));
    for (let i = 0; i < names.length; i++) void cache.delete(fileKey(id, i));
    const text = [meta.title, meta.text, meta.url].filter(Boolean).join("\n").trim();
    return { text, files };
  } catch {
    return null;
  }
}
