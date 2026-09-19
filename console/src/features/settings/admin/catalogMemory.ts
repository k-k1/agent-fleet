// What the model catalogue pane remembers while its tab is not the one on screen.
//
// Why it exists: a tabbed cell renders only the one selected view (PaneHost's selectedView), so
// switching to another tab unmounts the whole catalogue. Everything it was holding — the page of
// hits, the source and sort, the family chip, the filter typed into the registered list — lives in
// React state and goes with it, and coming back re-searches. That last part is the expensive half:
// the search is an UPSTREAM request to Civitai or Hugging Face, one per return, at hosts that shed
// load with a 503. The state cannot live inside React, so it lives outside the component — the same
// answer, and for the same reason, as features/viewer/scrollMemory.ts.
//
// The layout store is deliberately not the place: it is serialised and written on every geometry
// change, and a page of a hundred hits with preview URLs is not what belongs in it. Only the face
// that is open (search vs registered) is written back there, by the caller, because that one is
// worth surviving a reload.
//
// Store only: it touches neither React nor the DOM, so it runs in the node project.
import type { CatalogSource, EngineObjectRow, EngineRow, IngestHit } from "./engineTypes.ts";

/** How many (pane, engine, face, kind) entries to keep. A pane has a handful — two faces times two
 *  kinds per engine — so this is worth some tens of panes. On overflow the oldest goes first (Map
 *  keeps insertion order), which is the same bound scrollMemory uses. */
const MAX_ENTRIES = 40;

/** What the 探す face was showing. `hits` is the page itself: restoring it is what makes a return
 *  cost nothing, and it is the only field here that is big. */
export type CatalogSearchMemory = {
  source: CatalogSource;
  sort: string;
  family: string;
  /** What is in the box, and what was actually asked — they differ while somebody is typing, and
   *  the 続きを読む button is only offered for the submitted one. */
  query: string;
  submittedQuery: string;
  hits: IngestHit[] | null;
  cursor: string;
};

/** What the 登録済み face was showing. The rows themselves are not here: they come with the engine
 *  list, which has its own slot below. */
export type CatalogRegisteredMemory = {
  query: string;
  sort: string;
  /** The open family chip. 🔴 `null` is "all of them" and `""` is the group of rows that declare no
   *  family — two different states that a single sentinel would merge (ADR 0088). */
  family: string | null;
};

/** Which engine, face and kind the pane was on. Keyed by the pane alone: it is the shell around
 *  both faces, and it is what makes a returning tab open where it was left. */
export type CatalogShellMemory = {
  engineKey: string;
  view: "search" | "registered";
  kind: "model" | "lora";
};

/** The bucket listing, as the ledger answered it. Kept per ENGINE rather than per pane: what the
 *  bucket holds is a fact about the deployment, not about who is looking. */
export type CatalogObjectsMemory = {
  objects: EngineObjectRow[];
  checkedAt: string;
};

/** The engine list. One slot, because `GET api/admin/engines` answers the whole deployment. */
export type CatalogEnginesMemory = {
  rows: EngineRow[];
  isSuper: boolean;
  sources: CatalogSource[];
};

const searches = new Map<string, CatalogSearchMemory>();
const registered = new Map<string, CatalogRegisteredMemory>();
const shells = new Map<string, CatalogShellMemory>();
const buckets = new Map<string, CatalogObjectsMemory>();
let engines: CatalogEnginesMemory | null = null;

/** Key for one face of one pane. A different pane is a separate memory — the same catalogue can be
 *  open twice, on two engines, scrolled to two places.
 *
 *  🔴 The separator is NUL, and it is always written escaped: a raw control byte makes the file
 *  count as binary and drops it out of grep (src/test/noRawControlChars.test.ts). Same rule, same
 *  spelling, as scrollMemoryKey. */
export function catalogKey(paneId: string | undefined, engineKey: string, view: string, kind: string): string {
  return `${paneId || "-"}\u0000${engineKey}\u0000${view}\u0000${kind}`;
}

function keep<T>(store: Map<string, T>, key: string, value: T): void {
  if (!key) return;
  store.delete(key); // re-insert so the map stays in least-recently-used order
  store.set(key, value);
  if (store.size > MAX_ENTRIES) {
    const oldest = store.keys().next();
    if (!oldest.done) store.delete(oldest.value);
  }
}

export function saveSearch(key: string, value: CatalogSearchMemory): void {
  keep(searches, key, value);
}

export function loadSearch(key: string): CatalogSearchMemory | null {
  return searches.get(key) ?? null;
}

export function saveRegistered(key: string, value: CatalogRegisteredMemory): void {
  keep(registered, key, value);
}

export function loadRegistered(key: string): CatalogRegisteredMemory | null {
  return registered.get(key) ?? null;
}

export function saveShell(paneId: string | undefined, value: CatalogShellMemory): void {
  keep(shells, paneId || "-", value);
}

export function loadShell(paneId: string | undefined): CatalogShellMemory | null {
  return shells.get(paneId || "-") ?? null;
}

export function saveObjects(engineKey: string, value: CatalogObjectsMemory): void {
  keep(buckets, engineKey, value);
}

export function loadObjects(engineKey: string): CatalogObjectsMemory | null {
  return buckets.get(engineKey) ?? null;
}

export function saveEngines(value: CatalogEnginesMemory): void {
  engines = value;
}

export function loadEngines(): CatalogEnginesMemory | null {
  return engines;
}

/** For tests. 🔴 Not optional there: a dom test mounts the catalogue several times with no paneId,
 *  so every case shares one key — without this the second case reads the first one's page and the
 *  search it asserts is never sent. */
export function clearCatalogMemory(): void {
  searches.clear();
  registered.clear();
  shells.clear();
  buckets.clear();
  engines = null;
}
