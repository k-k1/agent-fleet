// Attachment-draft persistence (IndexedDB), the companion of lib/draft.ts. The composers
// already keep the typed text through a close/reopen or a view switch; the files staged
// next to it used to die with the component, which is the same accident — a screenshot
// pasted into 作業を始める, or dropped on the mirror, was gone as soon as the user went to
// check the branch and came back.
//
// Why IndexedDB and not localStorage (where the text lives): these records carry image
// BYTES. Base64 in localStorage would eat the ~5MB per-origin budget the Console also
// keeps its settings and UI prefs in, and the write that overflows it is somebody else's.
// IndexedDB takes the bytes as they are, with a quota measured in hundreds of MB.
//
// The bytes are stored as ArrayBuffer rather than the Blob/File itself. Both are legal
// IndexedDB values, but a Blob only round-trips through the browser's own structured
// clone: under jsdom + fake-indexeddb the clone silently degrades it to `{}` — the write
// succeeds, the read hands back an empty object, and a test suite that stored a Blob
// could only ever prove the plumbing, never the bytes. ArrayBuffer clones identically
// everywhere, so the tests measure the same thing the browser does.
//
// Every accessor swallows its errors (private mode, quota, a browser without IndexedDB):
// the draft just doesn't persist, exactly as before this file existed.

import { useEffect, useRef, useState } from "react";

const DB_NAME = "af-drafts";
const DB_VERSION = 1;
const STORE = "attachments";
// Abandoned drafts are pruned on the first open of the page session: an attachment nobody
// came back for in two weeks is not a draft anymore, and its bytes are not free.
const MAX_AGE_MS = 14 * 24 * 60 * 60 * 1000;

/** One stored attachment. `bytes` is absent for a file that is already uploaded and needs
 *  no thumbnail — its bytes live in the session's pasted dir and the chip is icon + name. */
export interface AttachDraftRec {
  /** Chip label: the server-side basename once uploaded, the picked filename before that. */
  name: string;
  /** MIME type, so a restored File is re-uploadable with the right extension. */
  type: string;
  /** Thumbnail-able image (drives both the preview and whether bytes are kept). */
  image: boolean;
  /** Absolute path the agent will read, or "" while the upload has no session yet. */
  path: string;
  bytes?: ArrayBuffer;
}

/** What a composer hands to writeAttachDraft: the record's fields plus the live file the
 *  bytes come from (absent only for a metadata-only chip restored from a draft). */
export interface AttachDraftInput {
  name: string;
  type: string;
  image: boolean;
  path: string;
  file?: File;
}

// Bytes come back from the structured clone, which may have built them in another realm
// (jsdom's window vs node's, a browser's worker): `instanceof ArrayBuffer` is false there
// even though the value is one, and the attachment would silently restore file-less.
const isBytes = (v: unknown): v is ArrayBuffer => Object.prototype.toString.call(v) === "[object ArrayBuffer]";

interface StoredDraft {
  key: string;
  at: number;
  items: AttachDraftRec[];
}

let dbPromise: Promise<IDBDatabase | null> | null = null;

// openDB resolves the shared connection, or null when IndexedDB is unusable. The promise
// is cached including the null, so a browser that refuses once isn't asked on every edit.
function openDB(): Promise<IDBDatabase | null> {
  if (dbPromise) return dbPromise;
  dbPromise = new Promise<IDBDatabase | null>((resolve) => {
    try {
      if (typeof indexedDB === "undefined") return resolve(null);
      const req = indexedDB.open(DB_NAME, DB_VERSION);
      req.onupgradeneeded = () => {
        if (!req.result.objectStoreNames.contains(STORE)) req.result.createObjectStore(STORE, { keyPath: "key" });
      };
      req.onsuccess = () => {
        const db = req.result;
        // A newer tab upgraded the schema under us: close and stop using it rather than
        // block that tab's upgrade forever.
        db.onversionchange = () => {
          db.close();
          dbPromise = Promise.resolve(null);
        };
        prune(db);
        resolve(db);
      };
      req.onerror = () => resolve(null);
      req.onblocked = () => resolve(null);
    } catch {
      resolve(null);
    }
  });
  return dbPromise;
}

// prune drops drafts nobody came back for. Best effort — a failure here must never keep
// the caller from reading the draft it actually asked for.
function prune(db: IDBDatabase): void {
  try {
    const cutoff = Date.now() - MAX_AGE_MS;
    const store = db.transaction(STORE, "readwrite").objectStore(STORE);
    const req = store.openCursor();
    req.onsuccess = () => {
      const cur = req.result;
      if (!cur) return;
      const rec = cur.value as StoredDraft;
      if (!rec || typeof rec.at !== "number" || rec.at < cutoff) cur.delete();
      cur.continue();
    };
  } catch {
    /* ignore */
  }
}

// run performs one store operation. `guard` (checked after the connection is in hand,
// immediately before the transaction opens) lets a superseded write bow out: both the
// byte read and the open are async, so without it "paste, then remove it again" can land
// in either order and the draft restores a chip the user already threw away.
function run<T>(
  mode: IDBTransactionMode,
  fn: (store: IDBObjectStore) => IDBRequest,
  guard?: () => boolean,
): Promise<T | null> {
  return openDB().then(
    (db) =>
      new Promise<T | null>((resolve) => {
        if (!db || (guard && !guard())) return resolve(null);
        try {
          const tx = db.transaction(STORE, mode);
          const req = fn(tx.objectStore(STORE));
          req.onsuccess = () => resolve(req.result as T);
          req.onerror = () => resolve(null);
          tx.onabort = () => resolve(null);
        } catch {
          resolve(null);
        }
      }),
  );
}

/** readAttachDraft loads a key's staged attachments ([] when none / unavailable). */
export async function readAttachDraft(key: string | null): Promise<AttachDraftRec[]> {
  if (!key) return [];
  const rec = await run<StoredDraft | undefined>("readonly", (s) => s.get(key));
  const items = rec?.items;
  if (!Array.isArray(items)) return [];
  // Records come back from another page session — treat every field as untrusted so one
  // malformed entry can't take the whole composer down at mount.
  return items
    .filter((it): it is AttachDraftRec => !!it && typeof it.name === "string")
    .map((it) => ({
      name: it.name,
      type: typeof it.type === "string" ? it.type : "",
      image: !!it.image,
      path: typeof it.path === "string" ? it.path : "",
      bytes: isBytes(it.bytes) ? it.bytes : undefined,
    }));
}

// Per-key write ticket: taken by every write and by every clear, so an older write that
// was still reading bytes when the newer one landed knows to drop its result.
const writeSeq = new Map<string, number>();
const takeTicket = (key: string): number => {
  const n = (writeSeq.get(key) ?? 0) + 1;
  writeSeq.set(key, n);
  return n;
};

/** clearAttachDraft forgets a key's staged attachments (sent / launched / abandoned). */
export async function clearAttachDraft(key: string | null): Promise<void> {
  if (!key) return;
  takeTicket(key); // a write still in flight must not resurrect what was just cleared
  await run("readwrite", (s) => s.delete(key));
}

// Read-back cache: a composer rewrites the whole list on every edit, and without this the
// third pasted image would re-read the first two off disk. Keyed by the File, so it dies
// with it.
const bytesOf = new WeakMap<File, Promise<ArrayBuffer | null>>();

function fileBytes(file: File): Promise<ArrayBuffer | null> {
  let p = bytesOf.get(file);
  if (!p) {
    p = file.arrayBuffer().catch(() => null);
    bytesOf.set(file, p);
  }
  return p;
}

// writeAttachDraft replaces a key's staged attachments. Bytes are kept only where they
// are the only copy (nothing uploaded yet) or where a thumbnail needs them — an uploaded
// non-image chip is name + icon, and duplicating a 30MB PDF into the browser to draw an
// icon would be a poor trade. So a restored image always has its file, and a file-less
// restored chip is always an uploaded non-image.
//
// An empty list deletes the record; a failed write deletes it too, because a half-written
// draft that restores as something the user never staged is worse than one that doesn't
// restore at all.
export async function writeAttachDraft(key: string | null, items: AttachDraftInput[]): Promise<void> {
  if (!key) return;
  if (!items.length) return clearAttachDraft(key);
  const ticket = takeTicket(key);
  const current = () => writeSeq.get(key) === ticket;
  const recs = (
    await Promise.all(
      items.map(async (it): Promise<AttachDraftRec | null> => {
        const needsBytes = it.image || !it.path;
        const bytes = needsBytes && it.file ? await fileBytes(it.file) : null;
        // Wanted the bytes and couldn't read them (the picked file moved / was replaced
        // on disk): leave the item out entirely rather than store a chip that restores
        // with a broken thumbnail, or — worse, for one not uploaded yet — as a picture of
        // a file that would silently not be sent.
        if (needsBytes && !bytes) return null;
        return {
          name: it.name,
          type: it.type,
          image: it.image,
          path: it.path,
          ...(bytes ? { bytes } : {}),
        };
      }),
    )
  ).filter((r): r is AttachDraftRec => !!r);
  if (!recs.length) return clearAttachDraft(key);
  const ok = await run("readwrite", (s) => s.put({ key, at: Date.now(), items: recs } satisfies StoredDraft), current);
  if (ok === null && current()) await clearAttachDraft(key);
}

// resetAttachDraftDB drops the cached connection. Tests only — a new test file installs a
// fresh fake IndexedDB and must not keep talking to the previous one.
export function resetAttachDraftDB(): void {
  dbPromise = null;
}

/** One staged attachment as the composers hold it. `file` is the bytes (missing only for
 *  an already-uploaded non-image restored from a draft), `url` the object URL behind an
 *  image thumbnail ("" when there is nothing to show), `id` a stable React key — the path
 *  is empty before upload and the URL changes on restore, so neither can be one. */
export interface Attachment {
  id: string;
  name: string;
  type: string;
  image: boolean;
  path: string;
  file?: File;
  url: string;
}

let seq = 0;
const nextId = (): string => "at" + ++seq;

// makeAttachment wraps a file in the composer's shape, minting the preview URL for an
// image. `name` defaults to the file's own (作業を始める stages files before any upload);
// the mirror passes the server-side basename it got back instead.
export function makeAttachment(file: File, opts?: { name?: string; path?: string; image?: boolean }): Attachment {
  const image = opts?.image ?? file.type.startsWith("image/");
  return {
    id: nextId(),
    name: opts?.name || file.name,
    type: file.type,
    image,
    path: opts?.path || "",
    file,
    url: image ? URL.createObjectURL(file) : "",
  };
}

function fromRec(rec: AttachDraftRec): Attachment {
  const file = rec.bytes ? new File([rec.bytes], rec.name, { type: rec.type }) : undefined;
  return {
    id: nextId(),
    name: rec.name,
    type: rec.type,
    image: rec.image,
    path: rec.path,
    file,
    url: rec.image && file ? URL.createObjectURL(file) : "",
  };
}

/** What a composer gets back from useAttachDraft. */
export interface AttachDraft {
  items: Attachment[];
  add: (items: Attachment[]) => void;
  remove: (i: number) => void;
  clear: () => void;
}

// useAttachDraft is the composer's attachment list, backed by the store above: hydrated
// from `key` on mount and on every key change (session switch / another repo picked in
// the はじめる hub), written through on every edit, and dropped by clear() when the turn
// was actually sent or the session actually launched.
//
// Two orderings matter and are both handled here:
//   - Hydration is async. Nothing is written back until it has landed for the current key,
//     or the empty initial state would delete the very draft being loaded.
//   - The user can paste while it is still in flight. The restored files are prepended to
//     whatever was staged meanwhile instead of replacing it.
// Object URLs are revoked the moment an item leaves the list (removed, cleared, key
// switched, unmounted), by identity — items are immutable, so a survivor keeps its URL.
export function useAttachDraft(key: string | null): AttachDraft {
  const [items, setItems] = useState<Attachment[]>([]);
  // `ready` is state, not a ref, on purpose: it has to re-run the write-through below when
  // it flips. A file staged while an EMPTY draft was still loading changes nothing else
  // afterwards, so a ref would leave it staged on screen and never written down.
  const [ready, setReady] = useState(false);
  const keyRef = useRef<string | null>(key);

  useEffect(() => {
    keyRef.current = key;
    setReady(false);
    setItems([]); // the previous key's chips are not this key's
    let alive = true;
    void readAttachDraft(key).then((recs) => {
      if (!alive || keyRef.current !== key) return;
      if (recs.length) setItems((prev) => [...recs.map(fromRec), ...prev]);
      setReady(true);
    });
    return () => {
      alive = false;
    };
  }, [key]);

  useEffect(() => {
    if (!ready || keyRef.current !== key) return;
    void writeAttachDraft(key, items);
  }, [items, key, ready]);

  const prevRef = useRef<Attachment[]>([]);
  useEffect(() => {
    const gone = prevRef.current.filter((p) => !items.includes(p));
    prevRef.current = items;
    gone.forEach((a) => a.url && URL.revokeObjectURL(a.url));
  }, [items]);
  useEffect(() => () => prevRef.current.forEach((a) => a.url && URL.revokeObjectURL(a.url)), []);

  const add = (next: Attachment[]) => {
    if (next.length) setItems((prev) => [...prev, ...next]);
  };
  const remove = (i: number) => setItems((prev) => prev.filter((_, idx) => idx !== i));
  // clear() drops the record right away instead of leaning on the write-through effect:
  // the launch path unmounts the modal in the same breath, and an effect that never runs
  // would leave the sent attachments staged for the next launch.
  const clear = () => {
    setItems((prev) => (prev.length ? [] : prev));
    void clearAttachDraft(key);
  };
  return { items, add, remove, clear };
}
