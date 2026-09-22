// GalleryView — one folder's images as a grid of cards (ADR 0080). A pane view like the
// others: it draws a ViewHead, takes the tabbed grid's header actions, and keeps its own
// toggle (`sort`) in the PANE CONTENT rather than in React state, because a tab switch
// unmounts this component and a sort that snapped back every time would read as broken.
//
// What it deliberately does not do:
//   - no listing endpoint of its own: `api/fs/tree` already answers {name,type,size,mtime}
//     for one level. (`api/fs/search` must never be used for this — it honours .gitignore,
//     so generated images would silently disappear from a gallery.)
//   - no extension table of its own: `imageFormat()` decides what a picture is, or the file
//     pane and the gallery start disagreeing about the same file.
//   - no resident poller: refreshes are the FILES policy (refreshPolicy.ts) — mount, the
//     files tick, tab return, and a slow tick only while something is running.
//   - no blank grid for a folder it has already read: the listing, the scroll position and the
//     page size come back out of galleryCache.ts first and the read behind them only corrects
//     what changed (ADR 0080 P2). Walking back into a folder is the common case, and a round
//     trip's worth of empty pane is what made it feel slow.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { ReactNode, KeyboardEvent as RKeyboardEvent, MouseEvent as RMouseEvent } from "react";
import { createPortal } from "react-dom";
import { displayURL, downloadURL, errDetail, fsDelete, fsRename } from "../../core/api/client.ts";
import { humanSize } from "../../lib/filemeta.ts";
import { relTime } from "../../lib/intl.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { displayName } from "../../lib/sessionview.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useFilesStore } from "../files/store.ts";
import { isBusySession } from "../files/sessionRefresh.ts";
import { REVALIDATE_GAP_MS, WORKING_TICK_MS } from "../files/refreshPolicy.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { ImageLightbox } from "../viewer/ImageLightbox.tsx";
import { isContextMenuKey, synthContextMenu } from "../project/contextMenuKey.ts";
import { openGeneratingSession, useGeneratingSession, type GeneratingSession } from "../imagegen/useGeneratingSession.ts";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import {
  PAGE_SIZE,
  breadcrumb,
  effectiveSort,
  focusIndex,
  hasTimes,
  galleryFolders,
  galleryImages,
  galleryTotals,
  parentPath,
  sortImages,
  visibleImages,
  type FsEntry,
  type GalleryImage,
  type GallerySort,
} from "./gallery.ts";
import { openGallery } from "./open.ts";
import { fetchGalleryListing, forgetGallery, prefetchGallery, readGallery, rememberGalleryView } from "./galleryCache.ts";
import "./gallery.css";

/**
 * Longest edge asked of the thumbnail endpoint, for everything card-sized: the grid, a folder's
 * cover, and the lightbox's placeholder. All three deliberately share it — they show the same
 * pictures at the same size, and the Agent's cache is keyed by the edge.
 *
 * Chosen by device pixel ratio, which is a revision of decision 4's flat 512. A card is 150-200
 * CSS px wide at 4:3, so a 1x screen shows about 138x104 to 200x150 — measured on a real
 * generated picture, 512 costs 42 KB against 256's 15 KB for pixels that screen cannot show,
 * and the decode costs the Agent the same either way (57 vs 59 ms: the decode, not the scale,
 * is the work). Decision 4's reason for one number was that the mirror asks for 512 and a
 * second edge means a second decode of the same file — true, but the mirror looks at shared
 * files and the gallery at generated folders, which in practice are different pictures.
 *
 * Read per render rather than once: a window dragged to a different monitor changes it, and the
 * cost of being wrong is one re-request at the other size.
 */
function thumbEdge(): number {
  return (window.devicePixelRatio || 1) > 1.5 ? 512 : 256;
}

/** How long a newly-arrived card stays tinted. Must match the .gal-new animation in
 *  gallery.css — the class is dropped when this elapses, so a longer animation is cut
 *  off mid-fade. Same value and reasoning as the files tree. */
const FRESH_MS = 5000;

/** The longest edge the lightbox asks for. Quantised to three steps rather than taken from the
 *  exact viewport: the Agent caches and decodes per edge, so every distinct window size would
 *  otherwise be its own decode. The smallest step is already past the pictures this exists for
 *  (832x1216), which is the case where `preview` re-encodes instead of downscaling. */
const PREVIEW_STEPS = [1024, 1536, 2048];

function previewEdge(): number {
  const want = Math.max(window.innerWidth, window.innerHeight) * Math.min(window.devicePixelRatio || 1, 2);
  return PREVIEW_STEPS.find((step) => step >= want) ?? PREVIEW_STEPS[PREVIEW_STEPS.length - 1];
}

/** How far outside the gallery's OWN scroll container (`.gal-body`, not the viewport — a pane
 *  can be narrower than the window and is often split) a card must come before its thumbnail is
 *  requested at all. `loading="lazy"` alone is not this: measured against a real 202-image
 *  folder, Chromium requested 54 of them with zero scrolling (scripts/gallery-perf/check.mjs) —
 *  its own "how far ahead is worth it" heuristic is generous, and once a card is unmounted
 *  nothing narrows it back down for the ones still in flight when a reader scrolls further.
 *  Those 50-odd leftover requests then sit ahead of the row someone just scrolled to in the
 *  browser's six-per-host queue (measured: the newly visible row took ~1s to arrive). Bounding
 *  what is armed at all is what keeps that queue short — not a priority hint on top of it. */
const ARM_MARGIN = "480px 0px";

/** How long the pointer rests on a folder card before its listing is fetched. Short enough to
 *  be in hand by the time a click lands, long enough that sweeping the pointer across a row of
 *  folders does not ask for every one of them. */
const HOVER_PREFETCH_MS = 120;

/**
 * Sticky viewport-adjacency for one card: false until this card has been within `ARM_MARGIN` of
 * the gallery's scroll container at least once, true forever after. Never re-arms to false —
 * scrolling a loaded picture back out of view must not re-request it, and the Agent's own cache
 * (`Cache-Control` + `v=<mtime>`) makes a genuine re-look free anyway.
 */
function useArmed(ref: { current: HTMLElement | null }, active = true): boolean {
  const [armed, setArmed] = useState(false);
  useEffect(() => {
    if (armed || !active) return;
    const el = ref.current;
    if (!el) return;
    const obs = new IntersectionObserver(
      (ents) => {
        if (ents.some((e) => e.isIntersecting)) setArmed(true);
      },
      { root: el.closest(".gal-body"), rootMargin: ARM_MARGIN },
    );
    obs.observe(el);
    return () => obs.disconnect();
  }, [active, armed, ref]);
  return armed;
}

const baseName = (p: string): string => p.split("/").filter(Boolean).pop() || p;

/**
 * What one right-click menu acts on. Both card kinds produce the same shape, because every
 * item but the wording works the same on either: the paths are copied, renamed and deleted
 * through the same endpoints, and only the confirm text has to know that a folder takes its
 * contents with it. The "Up" card is NOT a target — it names the folder being left, and
 * renaming or deleting the thing you climbed out of is never what a right-click there meant.
 */
interface MenuTarget {
  kind: "image" | "folder";
  name: string;
  path: string;
}

interface GalleryViewProps {
  paneId: string;
  path: string;
  sort?: GallerySort;
  focus?: string;
  /** The session NAME (slug) from the content; the title is looked up from it here, so
   *  a rename shows through and no display text is frozen into the layout. */
  sessionName?: string;
  headerActions?: ReactNode;
}

export function GalleryView({ paneId, path, sort, focus, sessionName, headerActions }: GalleryViewProps) {
  const tr = useT();
  const showToast = useToast();
  const askConfirm = useConfirm();
  const running = useWorkspaceStore((s) => wsRunning(s.state));
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const filesTick = useFilesStore((s) => s.tick);
  // One boolean out of the session list, not the list itself: this view re-renders on it,
  // and `sessions` is a new array on every poll (see the mirror's polling notes).
  const anyBusy = useSessionsStore((s) => s.sessions.some(isBusySession));
  const sessionTitle = useSessionsStore((s) => (sessionName ? s.sessions.find((x) => x.name === sessionName) : undefined));

  // What this folder looked like the last time it was read, taken ONCE at mount (the change-of
  // -folder path below takes it again). Seeding the state from it is what makes the first paint
  // of a folder already seen have its cards, instead of one render of the loading state and
  // then them — this view is mounted fresh whenever its tab is switched back to.
  const [atMount] = useState(() => readGallery(path));
  const [entries, setEntries] = useState<FsEntry[] | null>(atMount?.entries ?? null);
  const [failed, setFailed] = useState(false);
  const [limit, setLimit] = useState(atMount?.limit ?? PAGE_SIZE);
  /** True while the first read of a folder drawn FROM CACHE is still out. The grid is real and
   *  usable meanwhile; this only keeps the header honest about where it came from. */
  const [stale, setStale] = useState(!!atMount);
  const [fresh, setFresh] = useState<Set<string>>(() => new Set());
  const [broken, setBroken] = useState<Set<string>>(() => new Set());
  // The enlarged picture is remembered by PATH, not by position: a refresh that lands a
  // new generation shifts every index under "newest first", and an index would quietly
  // enlarge a different image while someone is looking at it.
  const [zoomPath, setZoomPath] = useState<string | null>(null);
  // The right-click menu: what it acts on and where to draw it. The entry is held whole rather
  // than by index, for the same reason `zoomPath` is — a background refresh reorders the grid,
  // and a menu that renamed whatever is now at position 3 would be the one failure a file menu
  // must not have.
  const [menu, setMenu] = useState<(MenuTarget & { x: number; y: number }) | null>(null);
  const menuRef = useRef<HTMLUListElement>(null);

  // Refs the refresh path reads: it runs from a timer / event, not from a render, so it
  // must not close over a stale listing or re-subscribe whenever one arrives.
  const namesRef = useRef<Set<string> | null>(
    atMount ? new Set(atMount.entries.map((e) => e?.name).filter(Boolean) as string[]) : null,
  );
  const lastAutoAt = useRef(0);
  const freshTimer = useRef(0);
  /** Whether a listing is on screen for the CURRENT folder (from a read or from the cache).
   *  A transport failure with cards already drawn must not replace them with an error page. */
  const shownRef = useRef(entries !== null);
  /** The scroll container, and which folder its position has already been restored for. */
  const bodyRef = useRef<HTMLDivElement | null>(null);
  const restoredFor = useRef<string | null>(null);
  /** Which folder the state below belongs to. This view is NOT remounted when it walks into a
   *  folder — the pane keeps it and changes `path` — so the switch has to be made here. */
  const shownPath = useRef(path);

  // Change of folder, done DURING the render rather than in an effect (React's "adjust state
  // when a prop changes"). An effect runs after the browser has painted, and that paint would
  // be the previous folder's cards under the new folder's path: a frame of the wrong pictures,
  // each one firing a thumbnail request for a path that does not exist. Measured on the way
  // back out of a folder, which is where a reader notices it.
  //
  // What the folder looked like last time comes out of the cache here, so the first paint of a
  // folder already seen HAS its cards. The remembered names are also what the "new card" tint
  // diffs against: seeded from the cache, walking back into a folder tints nothing and only a
  // picture that really did arrive since lights up.
  if (shownPath.current !== path) {
    shownPath.current = path;
    const cached = readGallery(path);
    setEntries(cached?.entries ?? null);
    setFailed(false);
    setStale(!!cached);
    setLimit(cached?.limit ?? PAGE_SIZE);
    setZoomPath(null);
    setMenu(null); // it names an entry in the folder being left
    setFresh(new Set()); // the tint belongs to the folder it was worked out in
    namesRef.current = cached ? new Set(cached.entries.map((e) => e?.name).filter(Boolean) as string[]) : null;
    shownRef.current = !!cached;
    restoredFor.current = null;
  }

  const found = useMemo(() => galleryImages(entries, path), [entries, path]);
  const images = useMemo(() => sortImages(found, sort), [found, sort]);
  const folders = useMemo(() => galleryFolders(entries, path), [entries, path]);
  const parent = parentPath(path);
  const crumbs = useMemo(() => breadcrumb(path), [path]);
  // Session folders are named by a UUID, so the card would read "03603f64-9cbc-…" — the
  // very reason the file tree is no way in (decision 8). The session list already carries
  // each session's folder and how many pictures are in it, so a folder that matches one is
  // labelled with the session and needs no request of its own (asking the folder itself
  // would be one fs/tree per card).
  //
  // Read from the store rather than subscribed to: `sessions` is a new array on every poll,
  // and this view would then re-render every second. Labels therefore refresh with the
  // LISTING, which is the same beat the cards themselves arrive on.
  const named = useMemo(() => {
    const m = new Map<string, { label: string; count: number }>();
    for (const s of useSessionsStore.getState().sessions) {
      if (s.generatedImagesPath) m.set(s.generatedImagesPath, { label: displayName(s), count: s.generatedImages || 0 });
    }
    return m;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [entries]);
  // What the toggle SHOWS as selected: with no times in the listing "newest" is not on
  // offer, and a highlighted button that sorts by name would be the view lying about
  // itself (decision 2 — old Agents do not send mtime).
  const mode = effectiveSort(found, sort);
  const totals = galleryTotals(images);
  const shown = visibleImages(images, limit);

  /**
   * Read the folder. `initial` is the first read after a change of folder, and decides what a
   * failure means:
   *
   *   - a HARD answer (gone, denied) on a first read shows the error state and forgets the
   *     cached listing — a remembered grid for a folder that no longer exists is the one lie
   *     this cache could tell. A refresh keeps the grid, as it always did: one odd answer
   *     while somebody is browsing is not worth emptying the pane for.
   *   - a transport failure or a 5xx (the CP's answer while the agent restarts) keeps whatever
   *     is on screen and is retried. The error state is only for a first read with nothing
   *     drawn — with a cached grid up, a blip must not replace it.
   *
   * Returns true when settled, which is what the mount effect's backoff wants.
   */
  const load = useCallback(
    async (signal: AbortSignal, initial: boolean): Promise<boolean> => {
      // `warm` asks the Agent to decode this folder's thumbnails into its cache while it
      // answers. A cold thumbnail is ~95 ms and a cached one ~44 µs (measured), so without
      // it the first look at a fresh folder trickles in card by card.
      const r = await fetchGalleryListing(path, thumbEdge(), signal);
      if (signal.aborted) return true;
      if (!r.ok) {
        if (r.hard && initial) {
          forgetGallery(path);
          namesRef.current = null;
          shownRef.current = false;
          setEntries(null);
          setFailed(true);
        } else if (initial && !shownRef.current) {
          setFailed(true);
        }
        if (!r.retry) setStale(false);
        return !r.retry;
      }
      const next = r.entries;
      const names = new Set(next.map((e) => e?.name).filter(Boolean) as string[]);
      // What a re-read ADDED, so the reader sees a generation land. A first read adds
      // nothing: everything is new then, and flashing the whole grid teaches the eye to
      // ignore the tint.
      const before = namesRef.current;
      namesRef.current = names;
      lastAutoAt.current = Date.now();
      shownRef.current = true;
      setFailed(false);
      setStale(false);
      setEntries(next);
      if (before) {
        const added = [...names].filter((n) => !before.has(n));
        if (added.length) {
          setFresh(new Set(added));
          window.clearTimeout(freshTimer.current);
          freshTimer.current = window.setTimeout(() => setFresh(new Set()), FRESH_MS);
        }
      }
      return true;
    },
    [path],
  );

  // Mount (and every change of folder): retry through the window where the workspace is up
  // but its agent is not yet answering, which is exactly when a pane opened from a session
  // menu mounts.
  useEffect(() => {
    const ac = new AbortController();
    let timer = 0;
    let tries = 0;
    const run = () => {
      load(ac.signal, true)
        .then((done) => {
          if (ac.signal.aborted || done) return;
          timer = window.setTimeout(run, Math.min(5000, 700 * 2 ** Math.min(tries++, 3)));
        })
        .catch(() => {});
    };
    run();
    return () => {
      ac.abort();
      window.clearTimeout(timer);
    };
  }, [load]);

  useEffect(() => () => window.clearTimeout(freshTimer.current), []);

  // Put the reader back where they were in a folder they have already scrolled through, before
  // the browser paints — a layout effect, or the grid is drawn at the top for one frame and the
  // restore reads as a jump. Once per change of folder: a background refresh must never move
  // the scroll position under someone.
  useLayoutEffect(() => {
    if (entries === null || restoredFor.current === path) return;
    restoredFor.current = path;
    const top = readGallery(path)?.scrollTop ?? 0;
    if (top && bodyRef.current) bodyRef.current.scrollTop = top;
  }, [entries, path]);

  const refresh = useCallback(
    (force = false) => {
      if (!force && (!running || document.hidden)) return;
      const ac = new AbortController();
      void load(ac.signal, false);
    },
    [load, running],
  );

  // The workspace-wide files tick (start/stop, an upload, a clone). Skipped on the tick the
  // view mounted at — the initial read already covers it.
  const mountedTick = useRef(filesTick);
  useEffect(() => {
    if (filesTick === mountedTick.current) return;
    refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filesTick]);

  // Coming back to the tab is where an unseen picture is most likely to be waiting, and it
  // is the only trigger that covers a generation that finished while the tab was in the
  // background. Held to one read per REVALIDATE_GAP_MS so alt-tabbing is not a poll.
  useEffect(() => {
    const onBack = () => {
      if (!running || document.hidden) return;
      if (Date.now() - lastAutoAt.current < REVALIDATE_GAP_MS) return;
      refresh();
    };
    document.addEventListener("visibilitychange", onBack);
    window.addEventListener("focus", onBack);
    return () => {
      document.removeEventListener("visibilitychange", onBack);
      window.removeEventListener("focus", onBack);
    };
  }, [refresh, running]);

  // Someone watching while a session runs. Generating an image takes minutes, so the
  // end-of-turn signal is far too late for this surface and this slow tick is what makes
  // pictures appear on their own. The timer exists only while something is running and
  // only while this view is mounted, so it is not a resident poller.
  useEffect(() => {
    if (!anyBusy || !running) return;
    const id = window.setInterval(() => refresh(), WORKING_TICK_MS);
    return () => window.clearInterval(id);
  }, [anyBusy, running, refresh]);

  // An opener asked for one picture (the left rail's context menu on an image). Enlarge it
  // once the listing is in, then take the request out of the pane content: it has been
  // honoured, and a reload should restore the folder, not re-open the lightbox.
  useEffect(() => {
    if (!focus || entries === null) return;
    const at = focusIndex(images, focus);
    if (at >= 0) setZoomPath(images[at].path);
    setPaneTarget(paneId, {
      content: { kind: "gallery", galleryPath: path, ...(sort ? { sort } : {}), ...(sessionName ? { gallerySession: sessionName } : {}) },
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focus, entries]);

  // With one picture open, fetch the next and previous originals in the background so ←/→
  // and a swipe land on a picture that is already there. Held back a moment so it never
  // competes with the one somebody is waiting for, and skipped when the browser says the
  // connection is metered — an original averages about a megabyte here.
  useEffect(() => {
    const at = zoomPath ? images.findIndex((i) => i.path === zoomPath) : -1;
    if (at < 0) return;
    const conn = (navigator as { connection?: { saveData?: boolean } }).connection;
    if (conn?.saveData) return;
    const id = window.setTimeout(() => {
      for (const near of [images[at + 1], images[at - 1]]) {
        if (!near) continue;
        const probe = new Image();
        probe.decoding = "async";
        probe.src = displayURL(near.path, previewEdge(), near.mtime);
      }
    }, 400);
    return () => window.clearTimeout(id);
  }, [zoomPath, images]);

  const close = useCallback(() => setZoomPath(null), []);
  // Back closes the lightbox instead of the pane. It is NOT inside ImageLightbox: whoever
  // opens it owns the history entry (the mirror does the same). Forget this and a phone's
  // Back press jumps straight past the picture.
  useBackClose(zoomPath ? close : undefined, !!zoomPath);

  // --- the right-click menu -------------------------------------------------------------
  // Closed by an outside click / Escape / the window losing focus, the same three ways the
  // file tree's menu closes. Registered on `document` rather than on the pane: the menu is
  // portalled to <body> and a click landing anywhere else must dismiss it.
  useEffect(() => {
    if (!menu) return;
    const shut = () => setMenu(null);
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && shut();
    document.addEventListener("mousedown", shut);
    document.addEventListener("keydown", onKey);
    window.addEventListener("blur", shut);
    return () => {
      document.removeEventListener("mousedown", shut);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("blur", shut);
    };
  }, [menu]);
  // Clamped on EVERY render, not once on open: the JSX re-applies the raw cursor coords as
  // inline style each time, and this view re-renders on its own (the files tick, a running
  // session's slow refresh) while the menu is up — a one-shot clamp would be undone by the
  // next poll and a menu opened near the pane's foot would jump back off-screen.
  useLayoutEffect(() => {
    if (menu && menuRef.current) placeFixed(menuRef.current, menu.x, menu.y);
  });
  /** Run a menu action and close the menu — one place, so no item can forget the close. */
  const runMenu = (fn: () => void) => {
    setMenu(null);
    fn();
  };

  const copyText = (text: string, done: string) => {
    if (!navigator.clipboard?.writeText) return showToast(tr("common.copy_failed"), { kind: "error" });
    navigator.clipboard.writeText(text).then(
      () => showToast(done, { kind: "success" }),
      () => showToast(tr("common.copy_failed"), { kind: "error" }),
    );
  };

  /**
   * Rename in place: what is typed is a NAME, and the destination is built in the FOLDER ON
   * SCREEN. A slash is refused rather than joined — "../x.png" would move the picture into a
   * sibling folder and "a/b.png" into one that may not exist, and a rename that silently
   * relocates is worse than one that says no. (The Agent's own path gate stops an escape from
   * the browse root; it has no reason to stop a move WITHIN it, so that check belongs here.)
   *
   * An enlarged picture follows the rename. The lightbox is held by path (`zoomPath`), so
   * leaving it pointing at the old name would 404 the moment the listing lands — the picture
   * would vanish from under whoever renamed it.
   */
  const renameEntry = async (target: MenuTarget) => {
    const typed = window.prompt(tr(target.kind === "folder" ? "gallery.rename_folder_prompt" : "gallery.rename_prompt"), target.name);
    if (typed === null) return;
    const next = typed.trim();
    if (!next || next === target.name) return;
    if (next.includes("/")) return showToast(tr("gallery.rename_bad_name"), { kind: "error" });
    const to = path ? path + "/" + next : next;
    const res = await fsRename(target.path, to);
    if (res.error) return showToast(tr("gallery.rename_failed", { msg: errDetail(res.error) }), { kind: "error" });
    setZoomPath((p) => (p === target.path ? to : p));
    refresh(true);
  };

  /**
   * Delete, behind the shared confirm. A folder takes everything inside it, so it says so and
   * asks with its own wording — the file tree's menu learnt the same lesson (`delete_dir_note`).
   *
   * An enlarged view of the deleted picture is closed rather than left on a URL that now 404s.
   */
  const deleteEntry = async (target: MenuTarget) => {
    const dir = target.kind === "folder";
    const ok = await askConfirm({
      title: tr(dir ? "gallery.delete_folder_title" : "gallery.delete_title"),
      body: tr(dir ? "gallery.delete_folder_body" : "gallery.delete_body", { name: target.name }),
      confirmLabel: tr("common.delete_do"),
      danger: true,
    });
    if (!ok) return;
    const res = await fsDelete(target.path);
    if (res.error) return showToast(tr("gallery.delete_failed", { msg: errDetail(res.error) }), { kind: "error" });
    setZoomPath((p) => (p === target.path ? null : p));
    refresh(true);
  };

  /**
   * Walk into a folder (or up out of one) IN THIS PANE. Not openGallery: that dedupes on
   * the folder and would jump to a gallery of the same folder someone has open elsewhere,
   * which is the opposite of navigating.
   *
   * `gallerySession` is dropped on the way: it titles the pane "Generated images — <name>",
   * and carrying it into a different folder would leave the tab claiming a session whose
   * pictures are no longer on screen. `sort` is a preference for the pane, so it stays.
   *
   * `push: true` is what makes the browser's own Back button retrace these steps: the layout
   * store already keeps one history entry per pushed commit (`layout/store.ts`) and restores
   * it on `popstate` — `setPaneTarget` just opts out of that by default (a sort toggle isn't a
   * place to come back to), and this is the one caller that opts back in. The header's "Up"
   * button calls this same function (with `parentPath(path)`), so it and the back button always
   * agree — pressing one and then the other is a no-op, never a surprise.
   */
  const navigate = (to: string) => {
    setPaneTarget(paneId, { content: { kind: "gallery", galleryPath: to, ...(sort ? { sort } : {}) } }, true);
  };

  const setSort = (next: GallerySort) => {
    setPaneTarget(paneId, {
      content: {
        kind: "gallery",
        galleryPath: path,
        sort: next,
        ...(sessionName ? { gallerySession: sessionName } : {}),
      },
    });
  };

  // The corner button spends a pane on the picture — a DIFFERENT one, the way the mirror
  // opens a shared file (openTargetInNew(force)): replacing this pane would take away the
  // grid the reader is working through. On a phone the layout store folds that back into
  // the current pane by itself, which is the right answer where there is no "beside".
  const openPane = (img: GalleryImage) => {
    useLayoutStore.getState().openTargetInNew({ content: { kind: "file", filePath: img.path } }, true);
  };

  // The session whose generate_image wrote the pictures in THIS folder, when one still exists.
  // Every image card here shares the folder, so it is resolved once for the pane rather than
  // per card — and the header wears it, so "which session made these" is answered before
  // anyone opens a menu (a generated folder is named by a UUID and says nothing by itself).
  const madeBy = useGeneratingSession(path);
  // The same question for whatever the menu is open on: a FOLDER card asks about itself, which
  // is the case that matters most — the generated root is a grid of one folder per session, so
  // that is where someone is looking when they want the conversation behind a batch.
  const menuMadeBy = useGeneratingSession(menu?.kind === "folder" ? menu.path : path);
  const jumpToSession = (target: GeneratingSession | null, split: boolean) => {
    if (!target) return;
    if (!openGeneratingSession(target.name, split)) showToast(tr("gallery.session_gone"), { kind: "info" });
  };

  const title = sessionTitle
    ? tr("gallery.title_session", { name: displayName(sessionTitle) })
    : path
      ? baseName(path)
      : tr("gallery.root");
  // "Nothing here" means nothing to walk into either: a folder with subfolders and no
  // pictures is a perfectly good gallery page (the generated root is exactly that).
  const empty = entries !== null && images.length === 0 && folders.length === 0;
  // A picture that is gone from the folder (deleted between two reads) closes the lightbox
  // rather than freezing on a URL that now 404s.
  const at = zoomPath ? images.findIndex((i) => i.path === zoomPath) : -1;
  const current = at >= 0 ? images[at] : null;

  return (
    <div className="gal">
      <ViewHead
        actions={
          <>
            <IconButton icon="refresh" label={tr("gallery.refresh")} onClick={() => refresh(true)} />
            {headerActions}
          </>
        }
      >
        <span className="view-title" title={path}>
          <Icon name="file-media" /> {title}
        </span>
        {entries !== null && (
          <span className="gal-count">
            {folders.length > 0 && <>{tr("gallery.summary_folders", { n: folders.length })} · </>}
            {tr("gallery.summary", { count: totals.count, size: humanSize(totals.bytes) })}
            {shown.length < totals.count && <> · {tr("gallery.shown", { shown: shown.length, count: totals.count })}</>}
            {/* Drawn from what this folder looked like last time, with the confirming read still
                out. Said out loud rather than shown as a spinner over the grid: the cards are
                real and usable, and the only thing in doubt is whether one more has landed. */}
            {stale && <> · {tr("gallery.updating")}</>}
          </span>
        )}
        <span className="gal-sort" role="group" aria-label={tr("gallery.sort")}>
          {(["new", "name"] as const).map((s) => (
            <button
              key={s}
              type="button"
              className={"ui-btn ui-btn-ghost gal-sort-btn" + (mode === s ? " on" : "")}
              aria-pressed={mode === s}
              // Without times in the listing there is no "newest" to offer. Disabled rather
              // than silently ignored: a button that writes a sort the view then overrules
              // reads as broken.
              disabled={s === "new" && entries !== null && !hasTimes(found)}
              onClick={() => setSort(s)}
            >
              {tr(s === "new" ? "gallery.sort_new" : "gallery.sort_name")}
            </button>
          ))}
        </span>
      </ViewHead>
      {/* The way back out, in its own row: the breadcrumb has nowhere to grow when it shares a
          row with the title, the count and the sort toggle, so a folder a few levels down had
          nowhere left to show its trail. Outside the failed/loading/empty branches below, same
          as the old single-row version — a folder with no pictures (or one that hasn't answered
          yet) must not be a dead end. The "Up" button is ALWAYS drawn (disabled at the root)
          rather than living only as a grid card: a long folder scrolled down hides that card,
          and it goes through the same `navigate()` as the breadcrumb and the browser's own Back
          button, so all three agree on where "up" leads. */}
      <div className="gal-path">
        <IconButton
          icon="arrow-up"
          label={tr("gallery.up")}
          onClick={() => parent !== null && navigate(parent)}
          disabled={parent === null}
        />
        <span className="gal-crumbs" aria-label={tr("gallery.breadcrumb")}>
          <button type="button" className="gal-crumb" onClick={() => navigate("")} disabled={!path}>
            {tr("gallery.root")}
          </button>
          {crumbs.map((c) => (
            <span key={c.path} className="gal-crumb-part">
              <span className="gal-crumb-sep" aria-hidden="true">
                /
              </span>
              <button
                type="button"
                className="gal-crumb"
                onClick={() => navigate(c.path)}
                disabled={c.path === path}
                title={c.path}
              >
                {c.name}
              </button>
            </span>
          ))}
        </span>
        {/* Who made these. A generated folder is named by a UUID, so without this the pane
            cannot say whose pictures it is showing — and the label is the way back to that
            session's conversation, which is where the prompt behind the picture is. Drawn only
            when the session still exists: a button that leads nowhere is worse than none. */}
        {madeBy && (
          <button
            type="button"
            className="ui-btn ui-btn-ghost gal-madeby"
            title={tr("gallery.open_session", { name: madeBy.label })}
            aria-label={tr("gallery.open_session", { name: madeBy.label })}
            onClick={(e) => jumpToSession(madeBy, e.ctrlKey || e.metaKey)}
            onMouseDown={(e) => e.button === 1 && e.preventDefault()}
            onAuxClick={(e) => e.button === 1 && jumpToSession(madeBy, true)}
          >
            <Icon name="comment-discussion" /> {madeBy.label}
          </button>
        )}
      </div>
      {failed ? (
        <EmptyState icon="warning" title={tr("gallery.failed")} hint={path} />
      ) : entries === null ? (
        // Nothing has arrived yet. The grid branch below would draw "Up" alone (folders and
        // images are both empty arrays on a null listing) — a partial page that reads as stuck
        // rather than loading. A refresh never lands here: `load()` keeps the prior listing on
        // screen on anything but the first read of a folder (see its doc comment), so this is
        // only the first look at a folder, never a background poll.
        <EmptyState icon="loading" title={tr("gallery.loading")} hint={path} />
      ) : empty ? (
        <EmptyState icon="file-media" title={tr("gallery.empty")} hint={path} />
      ) : (
        <div
          className="gal-body"
          ref={bodyRef}
          // Where the reader is, kept with the listing, so coming back lands them there. A plain
          // map write (galleryCache.ts) — a scroll handler that set state would re-render the
          // whole grid on every wheel notch.
          onScroll={(e) => rememberGalleryView(path, { scrollTop: e.currentTarget.scrollTop })}
        >
          <div className="gal-grid" role="list">
            {parent !== null && (
              <FolderCard
                label={tr("gallery.up")}
                icon="arrow-up"
                title={tr("gallery.up")}
                onPrefetch={() => prefetchGallery(parent, thumbEdge())}
                onOpen={(newPane) => (newPane ? openGallery(parent, { newPane: true }) : navigate(parent))}
              />
            )}
            {folders.map((f) => (
              <FolderCard
                key={f.path}
                label={named.get(f.path)?.label || f.name}
                // The Agent's own count when it sent one — it knows about every folder, not
                // only the ones a session generated into. The session list stays the fallback
                // for an older Agent that does not peek.
                meta={
                  typeof f.count === "number"
                    ? tr("gallery.folder_images", { n: f.count })
                    : named.has(f.path)
                      ? tr("gallery.folder_images", { n: named.get(f.path)!.count })
                      : undefined
                }
                cover={f.cover}
                icon="folder"
                title={f.path}
                fresh={fresh.has(f.name)}
                onPrefetch={() => prefetchGallery(f.path, thumbEdge())}
                onOpen={(newPane) => (newPane ? openGallery(f.path, { newPane: true }) : navigate(f.path))}
                onMenu={(x, y) => setMenu({ kind: "folder", name: f.name, path: f.path, x, y })}
              />
            ))}
            {shown.map((img) => (
              <GalleryCard
                key={img.path}
                img={img}
                fresh={fresh.has(img.name)}
                broken={broken.has(img.path)}
                onBroken={() => setBroken((b) => new Set(b).add(img.path))}
                onZoom={() => setZoomPath(img.path)}
                onOpenPane={() => openPane(img)}
                onMenu={(x, y) => setMenu({ kind: "image", name: img.name, path: img.path, x, y })}
                showTime={mode === "new"}
              />
            ))}
          </div>
          {shown.length < totals.count && (
            <div className="gal-more">
              <button
                type="button"
                className="ui-btn"
                onClick={() => {
                  const next = limit + PAGE_SIZE;
                  setLimit(next);
                  rememberGalleryView(path, { limit: next });
                }}
              >
                {tr("gallery.more")}
              </button>
            </div>
          )}
        </div>
      )}
      {current &&
        createPortal(
          <ImageLightbox
            // Versioned like the cards: reopening a picture already looked at costs no
            // request at all (the Agent answers `immutable` when `v` matches).
            // The picture at the size this screen can show, not the original file: the same
            // pixels for anything under the step, and about a ninth of the bytes (client.ts).
            src={displayURL(current.path, previewEdge(), current.mtime)}
            // The card's thumbnail is already decoded in this tab, so the enlarged view
            // paints immediately and sharpens when the original lands.
            placeholder={downloadURL(current.path, thumbEdge(), current.mtime)}
            path={current.path}
            alt={current.name}
            onClose={close}
            index={at + 1}
            total={images.length}
            onPrev={at > 0 ? () => setZoomPath(images[at - 1].path) : undefined}
            onNext={at < images.length - 1 ? () => setZoomPath(images[at + 1].path) : undefined}
          />,
          document.body,
        )}
      {menu &&
        createPortal(
          // Portalled and position:fixed, like every other right-click menu here: a menu drawn
          // inside `.gal-body` would be clipped by the scroll container it was opened in.
          // mousedown is stopped so the outside-click listener above does not close the menu
          // on the very press that is choosing an item.
          <ul
            className="ui-menu gal-ctxmenu"
            ref={menuRef}
            style={{ left: menu.x, top: menu.y }}
            role="menu"
            aria-label={tr("gallery.menu")}
            onMouseDown={(e) => e.stopPropagation()}
          >
            {/* A folder card offers "open in a second pane" too: its plain click navigates
                THIS pane, so without this the menu is the only way to keep the grid you are
                in. A picture already has that as the card's own corner button. */}
            {menu.kind === "folder" && (
              <li>
                <button
                  type="button"
                  className="ui-menu-item"
                  onClick={() => runMenu(() => openGallery(menu.path, { newPane: true }))}
                >
                  <Icon name="split-horizontal" /> {tr("gallery.open_folder_pane")}
                </button>
              </li>
            )}
            <li>
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => runMenu(() => copyText(menu.path, tr("gallery.copied_path")))}
              >
                <Icon name="copy" /> {tr("gallery.copy_path")}
              </button>
            </li>
            <li>
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => runMenu(() => copyText(menu.name, tr("gallery.copied_name")))}
              >
                <Icon name="copy" /> {tr(menu.kind === "folder" ? "gallery.copy_folder_name" : "gallery.copy_name")}
              </button>
            </li>
            {menuMadeBy && (
              <li>
                <button
                  type="button"
                  className="ui-menu-item"
                  onMouseDown={(e) => e.button === 1 && e.preventDefault()}
                  onAuxClick={(e) => e.button === 1 && runMenu(() => jumpToSession(menuMadeBy, true))}
                  onClick={(e) => {
                    const split = e.ctrlKey || e.metaKey;
                    runMenu(() => jumpToSession(menuMadeBy, split));
                  }}
                >
                  <Icon name="comment-discussion" /> {tr("gallery.open_session", { name: menuMadeBy.label })}
                </button>
              </li>
            )}
            <li>
              <button type="button" className="ui-menu-item" onClick={() => runMenu(() => void renameEntry(menu))}>
                <Icon name="edit" /> {tr(menu.kind === "folder" ? "gallery.rename_folder" : "gallery.rename")}
              </button>
            </li>
            <li>
              <button type="button" className="ui-menu-item danger" onClick={() => runMenu(() => void deleteEntry(menu))}>
                <Icon name="trash" /> {tr(menu.kind === "folder" ? "gallery.delete_folder" : "gallery.delete")}
              </button>
            </li>
          </ul>,
          document.body,
        )}
    </div>
  );
}

/**
 * A folder card: the way down into a subfolder, and the "Up" card that leaves one.
 *
 * One target, not two: a folder has nothing to enlarge, so the whole card navigates and the
 * image card's corner button has no counterpart here. Ctrl/⌘ and the middle button open the
 * folder in a second pane, the same convention every other row in the Console follows.
 */
function FolderCard({
  label,
  meta,
  cover,
  icon,
  title,
  fresh,
  onPrefetch,
  onOpen,
  onMenu,
}: {
  label: string;
  meta?: string;
  /** The newest picture inside, when the Agent described the folder. A folder named by a
   *  session UUID says nothing about what is in it; one picture says most of it. */
  cover?: GalleryImage;
  icon: string;
  title: string;
  fresh?: boolean;
  /** Read this folder's listing into the cache before the click lands — pointing at a folder
   *  is the earliest honest signal that somebody is about to open it. */
  onPrefetch?: () => void;
  onOpen: (newPane: boolean) => void;
  /** Open this folder's right-click menu. Absent on the "Up" card: it names the folder being
   *  left, and renaming or deleting that from here is never what the right-click meant. */
  onMenu?: (x: number, y: number) => void;
}) {
  const hoverTimer = useRef(0);
  const disarm = () => window.clearTimeout(hoverTimer.current);
  useEffect(() => disarm, []);
  const onContextMenu = (e: RMouseEvent) => {
    if (!onMenu) return; // no menu here, so leave the browser's own alone
    e.preventDefault();
    onMenu(e.clientX, e.clientY);
  };
  const onKeyDown = (e: RKeyboardEvent<HTMLDivElement>) => {
    if (!onMenu || !isContextMenuKey(e)) return;
    e.preventDefault();
    synthContextMenu(e.currentTarget);
  };
  // Gated exactly like an image card: a browse root with sixty subfolders would otherwise put
  // sixty covers in the queue before anyone has scrolled. No cover, no observer — "Up" and the
  // folders of an Agent that does not peek have nothing to wait for.
  const thumbRef = useRef<HTMLSpanElement | null>(null);
  const armed = useArmed(thumbRef, !!cover);
  const [coverFailed, setCoverFailed] = useState(false);
  return (
    <div
      className={"gal-card folder" + (fresh ? " gal-new" : "")}
      role="listitem"
      onContextMenu={onContextMenu}
      onKeyDown={onKeyDown}
    >
      <button
        type="button"
        className="gal-enter"
        title={title}
        onClick={(e) => onOpen(e.ctrlKey || e.metaKey)}
        // Pointer-down, not just hover: a touch has no hover at all, and on a mouse it is still
        // a frame or two ahead of the click.
        onPointerDown={() => onPrefetch?.()}
        onPointerEnter={() => {
          disarm();
          hoverTimer.current = window.setTimeout(() => onPrefetch?.(), HOVER_PREFETCH_MS);
        }}
        onPointerLeave={disarm}
        onMouseDown={(e) => e.button === 1 && e.preventDefault()}
        onAuxClick={(e) => e.button === 1 && onOpen(true)}
      >
        {/* The class follows the PICTURE, not the intent to have one: it turns the folder icon
            into a badge over the cover, and a tile that is still waiting to be armed would
            otherwise show that badge floating in an empty box. */}
        <span className={"gal-thumb" + (cover && !coverFailed && armed ? " cover" : "")} ref={thumbRef}>
          {cover && !coverFailed && armed ? (
            <img
              src={downloadURL(cover.path, thumbEdge(), cover.mtime)}
              alt=""
              loading="lazy"
              decoding="async"
              // Decorative: the card already says the folder's name, and a screen reader
              // reading out a file name nobody chose would only be noise.
              aria-hidden="true"
              onError={() => setCoverFailed(true)}
            />
          ) : null}
          <Icon name={icon} className="gal-folder-icon" />
        </span>
        <span className="gal-name" title={title}>
          {label}
        </span>
        <span className="gal-meta muted">{meta || ""}</span>
      </button>
    </div>
  );
}

/**
 * One card, split the way the transcript's file cards are (decision 5): the body enlarges
 * the picture, the corner button spends a pane on it. A thumbnail that failed to load
 * falls back to the whole card opening the pane — with no picture on screen there is
 * nothing to enlarge.
 */
function GalleryCard({
  img,
  fresh,
  broken,
  onBroken,
  onZoom,
  onOpenPane,
  onMenu,
  showTime,
}: {
  img: GalleryImage;
  fresh: boolean;
  broken: boolean;
  onBroken: () => void;
  onZoom: () => void;
  onOpenPane: () => void;
  /** Open the card's right-click menu at these viewport coordinates. */
  onMenu: (x: number, y: number) => void;
  showTime: boolean;
}) {
  const tr = useT();
  const onContextMenu = (e: RMouseEvent) => {
    e.preventDefault();
    onMenu(e.clientX, e.clientY);
  };
  // Menu key / Shift+F10 on the focused card. A native contextmenu event is synthesised on the
  // card rather than calling onMenu directly, so there is only ONE way in and the keyboard
  // cannot drift from the pointer (the rail rows do the same — contextMenuKey.ts).
  const onKeyDown = (e: RKeyboardEvent<HTMLDivElement>) => {
    if (!isContextMenuKey(e)) return;
    e.preventDefault();
    synthContextMenu(e.currentTarget);
  };
  // A relative time is only shown when the Agent actually sent one — never derived from
  // the file name, however tempting the unixnano in a generated one looks.
  const meta = showTime && img.mtime ? relTime(img.mtime * 1000) : humanSize(img.size);
  const thumbRef = useRef<HTMLSpanElement | null>(null);
  const armed = useArmed(thumbRef);
  const body = (
    <>
      <span className="gal-thumb" ref={thumbRef}>
        {broken ? (
          <Icon name="file-media" className="gal-thumb-none" />
        ) : armed ? (
          <img
            src={downloadURL(img.path, thumbEdge(), img.mtime)}
            alt={img.name}
            loading="lazy"
            decoding="async"
            fetchPriority="high"
            onError={onBroken}
          />
        ) : null}
      </span>
      <span className="gal-name" title={img.path}>
        {img.name}
      </span>
      <span className="gal-meta muted">{meta}</span>
    </>
  );
  if (broken) {
    // No picture on screen, so there is nothing to enlarge: the whole card becomes the
    // pane target, exactly as a non-image file card does in the transcript.
    return (
      <div
        className={"gal-card" + (fresh ? " gal-new" : "")}
        role="listitem"
        onContextMenu={onContextMenu}
        onKeyDown={onKeyDown}
      >
        <button
          type="button"
          className="gal-zoom"
          title={tr("gallery.open_in_pane", { name: img.name })}
          onClick={onOpenPane}
          onAuxClick={(e) => e.button === 1 && onOpenPane()}
        >
          {body}
        </button>
      </div>
    );
  }
  return (
    <div
      className={"gal-card image" + (fresh ? " gal-new" : "")}
      role="listitem"
      onContextMenu={onContextMenu}
      onKeyDown={onKeyDown}
    >
      <button type="button" className="gal-zoom" title={tr("gallery.zoom", { name: img.name })} onClick={onZoom}>
        {body}
      </button>
      <button
        type="button"
        className="gal-pane"
        title={tr("gallery.open_in_pane", { name: img.name })}
        aria-label={tr("gallery.open_in_pane", { name: img.name })}
        onClick={onOpenPane}
        onAuxClick={(e) => e.button === 1 && onOpenPane()}
      >
        <Icon name="split-horizontal" />
      </button>
    </div>
  );
}
