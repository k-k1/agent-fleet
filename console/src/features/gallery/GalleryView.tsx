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
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { createPortal } from "react-dom";
import { api, downloadURL, isTransientErr } from "../../core/api/client.ts";
import { humanSize } from "../../lib/filemeta.ts";
import { relTime } from "../../lib/intl.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { displayName } from "../../lib/sessionview.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useFilesStore } from "../files/store.ts";
import { isBusySession } from "../files/sessionRefresh.ts";
import { REVALIDATE_GAP_MS, WORKING_TICK_MS } from "../files/refreshPolicy.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { ImageLightbox } from "../viewer/ImageLightbox.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
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
import "./gallery.css";

/** Longest edge asked of the thumbnail endpoint. The SAME number the mirror's file cards
 *  use: the Agent's thumbnail cache is keyed by it, so a gallery that asked for 256 would
 *  make the workspace decode every shared picture a second time (decision 4). */
const THUMB = 512;

/** How long a newly-arrived card stays tinted. Must match the .gal-new animation in
 *  gallery.css — the class is dropped when this elapses, so a longer animation is cut
 *  off mid-fade. Same value and reasoning as the files tree. */
const FRESH_MS = 5000;

const baseName = (p: string): string => p.split("/").filter(Boolean).pop() || p;

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
  const running = useWorkspaceStore((s) => wsRunning(s.state));
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const filesTick = useFilesStore((s) => s.tick);
  // One boolean out of the session list, not the list itself: this view re-renders on it,
  // and `sessions` is a new array on every poll (see the mirror's polling notes).
  const anyBusy = useSessionsStore((s) => s.sessions.some(isBusySession));
  const sessionTitle = useSessionsStore((s) => (sessionName ? s.sessions.find((x) => x.name === sessionName) : undefined));

  const [entries, setEntries] = useState<FsEntry[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [limit, setLimit] = useState(PAGE_SIZE);
  const [fresh, setFresh] = useState<Set<string>>(() => new Set());
  const [broken, setBroken] = useState<Set<string>>(() => new Set());
  // The enlarged picture is remembered by PATH, not by position: a refresh that lands a
  // new generation shifts every index under "newest first", and an index would quietly
  // enlarge a different image while someone is looking at it.
  const [zoomPath, setZoomPath] = useState<string | null>(null);

  // Refs the refresh path reads: it runs from a timer / event, not from a render, so it
  // must not close over a stale listing or re-subscribe whenever one arrives.
  const namesRef = useRef<Set<string> | null>(null);
  const lastAutoAt = useRef(0);
  const freshTimer = useRef(0);

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
   * Read the folder. `initial` decides what a failure means: on the first read there is
   * nothing to lose, so it shows the error state; on a refresh the listing on screen is
   * kept, because one transient 502 (the CP's answer while the agent restarts) emptying a
   * gallery is a far worse lie than a stale one. Returns true when settled, which is what
   * useRetryLoad's backoff wants.
   */
  const load = useCallback(
    async (signal: AbortSignal, initial: boolean): Promise<boolean> => {
      let d: { entries?: FsEntry[]; error?: { code?: string } };
      try {
        d = await api(`api/fs/tree?path=${encodeURIComponent(path)}`);
      } catch {
        if (!signal.aborted && initial) setFailed(true);
        return false;
      }
      if (signal.aborted) return true;
      if (!d || isTransientErr(d)) return false;
      if (d.error || !Array.isArray(d.entries)) {
        if (initial) {
          setEntries(null);
          setFailed(true);
        }
        return true;
      }
      const next = d.entries;
      const names = new Set(next.map((e) => e?.name).filter(Boolean) as string[]);
      // What a re-read ADDED, so the reader sees a generation land. A first read adds
      // nothing: everything is new then, and flashing the whole grid teaches the eye to
      // ignore the tint.
      const before = namesRef.current;
      namesRef.current = names;
      lastAutoAt.current = Date.now();
      setFailed(false);
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
    setEntries(null);
    setFailed(false);
    setLimit(PAGE_SIZE);
    setZoomPath(null);
    namesRef.current = null;
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

  const close = useCallback(() => setZoomPath(null), []);
  // Back closes the lightbox instead of the pane. It is NOT inside ImageLightbox: whoever
  // opens it owns the history entry (the mirror does the same). Forget this and a phone's
  // Back press jumps straight past the picture.
  useBackClose(zoomPath ? close : undefined, !!zoomPath);

  /**
   * Walk into a folder (or up out of one) IN THIS PANE. Not openGallery: that dedupes on
   * the folder and would jump to a gallery of the same folder someone has open elsewhere,
   * which is the opposite of navigating.
   *
   * `gallerySession` is dropped on the way: it titles the pane "Generated images — <name>",
   * and carrying it into a different folder would leave the tab claiming a session whose
   * pictures are no longer on screen. `sort` is a preference for the pane, so it stays.
   */
  const navigate = (to: string) => {
    setPaneTarget(paneId, { content: { kind: "gallery", galleryPath: to, ...(sort ? { sort } : {}) } });
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
        {/* The way back out. It is in the HEAD rather than a card so it survives the empty
            state — a folder with no pictures must not be a dead end. */}
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
        {entries !== null && (
          <span className="gal-count">
            {folders.length > 0 && <>{tr("gallery.summary_folders", { n: folders.length })} · </>}
            {tr("gallery.summary", { count: totals.count, size: humanSize(totals.bytes) })}
            {shown.length < totals.count && <> · {tr("gallery.shown", { shown: shown.length, count: totals.count })}</>}
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
      {failed ? (
        <EmptyState icon="warning" title={tr("gallery.failed")} hint={path} />
      ) : empty ? (
        <EmptyState icon="file-media" title={tr("gallery.empty")} hint={path} />
      ) : (
        <div className="gal-body">
          <div className="gal-grid" role="list">
            {parent !== null && (
              <FolderCard
                label={tr("gallery.up")}
                icon="arrow-up"
                title={tr("gallery.up")}
                onOpen={(newPane) => (newPane ? openGallery(parent, { newPane: true }) : navigate(parent))}
              />
            )}
            {folders.map((f) => (
              <FolderCard
                key={f.path}
                label={named.get(f.path)?.label || f.name}
                meta={named.has(f.path) ? tr("gallery.folder_images", { n: named.get(f.path)!.count }) : undefined}
                icon="folder"
                title={f.path}
                fresh={fresh.has(f.name)}
                onOpen={(newPane) => (newPane ? openGallery(f.path, { newPane: true }) : navigate(f.path))}
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
                showTime={mode === "new"}
              />
            ))}
          </div>
          {shown.length < totals.count && (
            <div className="gal-more">
              <button type="button" className="ui-btn" onClick={() => setLimit((n) => n + PAGE_SIZE)}>
                {tr("gallery.more")}
              </button>
            </div>
          )}
        </div>
      )}
      {current &&
        createPortal(
          <ImageLightbox
            src={downloadURL(current.path)}
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
  icon,
  title,
  fresh,
  onOpen,
}: {
  label: string;
  meta?: string;
  icon: string;
  title: string;
  fresh?: boolean;
  onOpen: (newPane: boolean) => void;
}) {
  return (
    <div className={"gal-card folder" + (fresh ? " gal-new" : "")} role="listitem">
      <button
        type="button"
        className="gal-enter"
        title={title}
        onClick={(e) => onOpen(e.ctrlKey || e.metaKey)}
        onMouseDown={(e) => e.button === 1 && e.preventDefault()}
        onAuxClick={(e) => e.button === 1 && onOpen(true)}
      >
        <span className="gal-thumb">
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
  showTime,
}: {
  img: GalleryImage;
  fresh: boolean;
  broken: boolean;
  onBroken: () => void;
  onZoom: () => void;
  onOpenPane: () => void;
  showTime: boolean;
}) {
  const tr = useT();
  // A relative time is only shown when the Agent actually sent one — never derived from
  // the file name, however tempting the unixnano in a generated one looks.
  const meta = showTime && img.mtime ? relTime(img.mtime * 1000) : humanSize(img.size);
  const body = (
    <>
      <span className="gal-thumb">
        {broken ? (
          <Icon name="file-media" className="gal-thumb-none" />
        ) : (
          <img
            src={downloadURL(img.path, THUMB)}
            alt={img.name}
            loading="lazy"
            decoding="async"
            onError={onBroken}
          />
        )}
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
      <div className={"gal-card" + (fresh ? " gal-new" : "")} role="listitem">
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
    <div className={"gal-card image" + (fresh ? " gal-new" : "")} role="listitem">
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
