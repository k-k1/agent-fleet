// ProjectFiles — the rail's file tree: a focused, self-contained lazy tree rooted
// at one folder (the FilesSection mounts it once at "repos", so the top level is
// the working copies themselves): expand/collapse (with single-child chain
// folding), open a file into a pane, drag-drop upload, and a right-click menu for
// the core file ops. It reuses the shared primitives (api fs endpoints,
// FileIcon/DirIcon, the .fstree/.fsrow classes); per-copy git changes live in the
// SCM pane, not here. Mounted only while its section is open, so the fetch is lazy.
import { createPortal } from "react-dom";
import { Fragment, useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent, DragEvent as RDragEvent, KeyboardEvent as RKeyboardEvent } from "react";
import { api, uploadFiles, downloadURL, fsMkdir, fsNewFile, fsRename, fsDelete, fsSearch, isTransientErr } from "../../core/api/client.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import FileIcon, { DirIcon } from "../../ui/FileIcon.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useFilesStore } from "../files/store.ts";
import { useReposStore } from "../repos/store.ts";
import { useFilesFilter } from "./filesFilter.ts";
import { normQuery } from "./filter.ts";
import { useActiveWorkingSet, folderBase, autoAddToActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { WorkingCopyLabel } from "./WorkingCopyLabel.tsx";
import { isContextMenuKey, menuAnchor } from "./contextMenuKey.ts";
import { stickyAncestors } from "./stickyTree.ts";
import { chatCreate } from "../chat/api.ts";
import { openChat } from "../chat/open.ts";

interface Entry {
  name: string;
  type: string;
}
interface Row {
  path: string;
  name: string;
  type: string;
  depth: number;
  segPaths: string[];
  /** Search results only: the directory prefix (relative to the tree root),
   * shown muted before the filename so a flat match keeps its context. */
  sub?: string;
}
interface Menu {
  x: number;
  y: number;
  row: Row;
}

const joinPath = (d: string, n: string) => (d ? d + "/" + n : n);
const parentOf = (p: string) => {
  const i = p.lastIndexOf("/");
  return i < 0 ? "" : p.slice(0, i);
};
const baseName = (p: string) => p.split("/").pop() || p;
const repoOf = (p: string) => (p.startsWith("repos/") ? p.slice(6).split("/")[0] : "");

// A directory is a "passthrough" link when its sole entry is one subdirectory —
// folded into one row (a/b/c) so deep single-child paths don't waste space.
const soleChildDir = (entries: Entry[] | undefined): Entry | null =>
  entries && entries.length === 1 && entries[0].type === "dir" ? entries[0] : null;

const fsList = (path: string) =>
  api(`api/fs/tree?path=${encodeURIComponent(path)}`).catch(() => ({ entries: [] }));

// Same listing, but for the paths that are ALREADY on screen (the auto-refresh
// below, and the re-expand revalidation): it must be able to say "I don't know"
// so a stale row survives a failure.
//
// ★ fsList above answers a dropped fetch with `{entries: []}`, which is
// indistinguishable from an empty directory. Writing that back is fine on a
// mount (nothing to lose) and fatal on a refresh: one transient 502 — the CP's
// plain-text answer while the agent restarts — would EMPTY the tree at the end
// of a turn, which is a far worse lie than a stale listing.
//   null    → keep what is on screen (transport failure, 5xx, odd body)
//   "gone"  → the directory no longer exists (terminal 404): drop it
type Fresh = Entry[] | null | "gone";
const fsListFresh = async (path: string): Promise<Fresh> => {
  let d: { entries?: Entry[]; error?: { code?: string } };
  try {
    d = await api(`api/fs/tree?path=${encodeURIComponent(path)}`);
  } catch {
    return null; // network drop
  }
  if (!d || isTransientErr(d)) return null;
  if (d.error) return d.error.code === "not_dir" ? "gone" : null;
  return Array.isArray(d.entries) ? d.entries : null;
};

const sameEntries = (a: Entry[] | undefined, b: Entry[]): boolean =>
  !!a && a.length === b.length && a.every((e, i) => e.name === b[i].name && e.type === b[i].type);

// Shared empty row list. It must be ONE stable reference, not a fresh literal per
// render: displayRows below falls back to it while a search is in flight, and it
// feeds the deps of the sticky-lineage layout effect. A new [] each render made
// those deps change every render — see the comment at that effect.
const NO_ROWS: Row[] = [];

interface ProjectFilesProps {
  /** The tree's home-relative root folder ("" = home itself, the rail default). */
  root: string;
  /** Mark top-level working-copy folders with the repo/worktree icon (matching
   * the リポジトリ section rows) instead of the plain folder — for the tree
   * rooted at "repos", whose depth-0 dirs ARE the working copies. */
  markRepos?: boolean;
  /** When set, a non-empty 絞り込み query switches this tree to a flat, recursive
   * filename search over the whole subtree (rg-backed) instead of filtering only
   * the loaded rows. Enabled for the repos tree; the home tree keeps the cheap
   * visible-row filter. */
  searchable?: boolean;
  /** Add non-interactive working-copy headings to recursive repos results. */
  groupByRepo?: boolean;
  /**
   * This is the secondary "home" tree mounted below the primary one. Both are rooted
   * so that a path under repos/ matches BOTH, and answering a reveal twice means two
   * scrolls, two selections and a fight over focus. The secondary one stands down for
   * paths the repos tree owns.
   */
  secondary?: boolean;
}

export function ProjectFiles({ root, markRepos, searchable, groupByRepo, secondary }: ProjectFilesProps) {
  const repos = useReposStore((s) => s.repos);
  const layout = useLayoutStore((s) => s.layout);
  const openTarget = useLayoutStore((s) => s.openTarget);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const running = useWorkspaceStore((s) => s.state) === "running";
  const reveal = useFilesStore((s) => s.reveal);
  const filesTick = useFilesStore((s) => s.tick);
  const scopedRefresh = useFilesStore((s) => s.scoped);
  const q = useFilesFilter((s) => s.q);
  const nq = normQuery(q);
  // Declared here (not next to the search effect below) because the auto-refresh
  // above it stands down while a recursive search owns the rows.
  const searchMode = !!searchable && !!nq;
  const wset = useActiveWorkingSet(); // 作業グループ (docs/log/52) — repos ツリーの絞り込み
  const focusInput = useFilesFilter((s) => s.focusInput);
  const focusTreeN = useFilesFilter((s) => s.focusTreeN);
  const askConfirm = useConfirm();
  const tr = useT();
  const toast = useToast();

  const ac = activePane(layout)?.content;
  const activeFile = ac && ac.kind === "file" ? ac.filePath : "";

  const [entries, setEntries] = useState<Entry[] | null>(null); // root folder's children
  const [open, setOpen] = useState<Set<string>>(() => new Set());
  const [cache, setCache] = useState<Record<string, Entry[]>>({});
  const [selected, setSelected] = useState<string | null>(null);
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  const [menu, setMenu] = useState<Menu | null>(null);
  // Recursive-search state (searchable trees, query active): rows are flat file
  // hits; null = not searching / no fetch yet.
  const [searchRows, setSearchRows] = useState<Row[] | null>(null);
  const [searchTrunc, setSearchTrunc] = useState(false);
  const [searching, setSearching] = useState(false);
  const [collapsedRepos, setCollapsedRepos] = useState<Set<string>>(() => new Set());
  const menuRef = useRef<HTMLUListElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const treeRef = useRef<HTMLUListElement>(null);
  const wrapRef = useRef<HTMLDivElement>(null);
  const uid = useId();
  const [sticky, setSticky] = useState<{ rows: Row[]; top: number }>({ rows: [], top: 0 });
  // Refs the auto-refresh reads (it runs on a store tick, not on a render, so a
  // closure over state would be a render behind).
  const menuOpenRef = useRef(false);
  menuOpenRef.current = !!menu;
  const liveDirsRef = useRef<string[]>([]);
  const revalidatingRef = useRef<Set<string>>(new Set());
  const mountedScopedRef = useRef(scopedRefresh.n);

  const showFile = useCallback((p: string) => openTarget({ content: { kind: "file", filePath: p } }), [openTarget]);
  const showFileSplit = useCallback((p: string) => openTargetInNew({ content: { kind: "file", filePath: p } }), [openTargetInNew]);

  // Open an ad-hoc assistant chat for a Files verb (docs/log/30 ②): translate / summarize
  // the file with the verb persona baked in — no standing assistant. The Agent attaches
  // the file's dir as knowledge and returns a seed prompt; openChat prefills the composer
  // (never auto-sent — the user reviews and hits Enter).
  const openVerbChat = useCallback(
    async (filePath: string, verb: "translate" | "summarize") => {
      try {
        const title =
          verb === "translate"
            ? t("proj.verb_title.translate", { name: baseName(filePath) })
            : t("proj.verb_title.summarize", { name: baseName(filePath) });
        const c = await chatCreate("", title, { attachPath: filePath, seedVerb: verb });
        if (c && c.id) {
          autoAddToActiveWorkingSet("convs", c.id); // docs/log/52 §1
          openChat(c.id, c.seed);
        } else toast(t("send.chat_create_failed"));
      } catch {
        toast(t("send.chat_create_failed"));
      }
    },
    [toast],
  );

  // Load (mount / manual refresh via files tick): fetch the root folder's children
  // and re-fetch every currently-open dir, preserving expansion + selection.
  // WS 起動直後は agent 不通で fsList が {error: http_5xx} を返す（fsList の .catch は本物の
  // 例外だけ拾う）。running 中の過渡的失敗はバックオフ再試行し、停止中はそのまま空を確定して
  // 無駄なポーリングを避ける。
  useRetryLoad(async (signal) => {
    const r = await fsList(root);
    if (signal.aborted) return true;
    if (isTransientErr(r) && running) return false; // WS agent still booting — retry
    setEntries(r.entries || []);
    const opened = [...open];
    if (opened.length) {
      const pairs = await Promise.all(opened.map(async (p) => [p, (await fsList(p)).entries || []] as const));
      if (signal.aborted) return true;
      setCache((c) => {
        const n = { ...c };
        for (const [p, e] of pairs) n[p] = e;
        return n;
      });
    }
    return true;
  }, [root, filesTick, running]);

  // Apply a batch of re-reads: replace only what actually changed (an unchanged
  // batch returns the same object, so nothing re-renders), keep what failed, and
  // forget a directory that is gone.
  const applyFresh = useCallback((pairs: (readonly [string, Fresh])[]) => {
    setCache((c) => {
      const n = { ...c };
      let changed = false;
      for (const [p, e] of pairs) {
        if (e === null) continue; // couldn't read it — leave the rows alone
        if (e === "gone") {
          if (p in n) {
            delete n[p];
            changed = true;
          }
          continue;
        }
        if (sameEntries(n[p], e)) continue;
        n[p] = e;
        changed = true;
      }
      return changed ? n : c;
    });
  }, []);

  // Re-read one directory in the background (one in flight per path).
  const revalidate = useCallback(
    (path: string) => {
      const inFlight = revalidatingRef.current;
      if (inFlight.has(path)) return;
      inFlight.add(path);
      void fsListFresh(path)
        .then((e) => applyFresh([[path, e] as const]))
        .finally(() => inFlight.delete(path));
    },
    [applyFresh],
  );

  const fetchInto = useCallback(
    async (path: string) => {
      // Re-opening a folder shows the cached listing at once and re-reads it
      // behind that (stale-while-revalidate). Without this, anything an agent
      // added or removed while the folder was collapsed stayed invisible even
      // after collapsing and expanding it again — which reads as "the tree is
      // broken", since that IS the gesture people try first.
      if (cache[path]) {
        revalidate(path);
        return cache[path];
      }
      const d = await fsList(path);
      const e = d.entries || [];
      setCache((c) => (c[path] ? c : { ...c, [path]: e }));
      return e;
    },
    [cache, revalidate],
  );

  // expand opens `path`, auto-descending single-subdir chains.
  const expand = useCallback(
    async (path: string) => {
      const toOpen: string[] = [];
      let cur = path;
      for (let i = 0; i < 64; i++) {
        toOpen.push(cur);
        const e = await fetchInto(cur);
        const child = soleChildDir(e);
        if (!child) break;
        cur = cur + "/" + child.name;
      }
      setOpen((s) => {
        const n = new Set(s);
        for (const p of toOpen) n.add(p);
        return n;
      });
      return cur;
    },
    [fetchInto],
  );
  const collapse = useCallback((rowSegs: string[]) => {
    setOpen((s) => {
      const n = new Set(s);
      for (const p of rowSegs) n.delete(p);
      return n;
    });
  }, []);

  // Flatten the open tree into visible rows; single-child chains fold into one row.
  const { rows, need } = useMemo(() => {
    const out: Row[] = [];
    const need: string[] = [];
    const walk = (es: Entry[], parent: string, depth: number) => {
      for (const e of es) {
        const path = joinPath(parent, e.name);
        if (e.type !== "dir") {
          out.push({ path, name: e.name, type: "file", depth, segPaths: [path] });
          continue;
        }
        let p = path;
        const segs = [e.name];
        const segPaths = [path];
        while (cache[p] && soleChildDir(cache[p])) {
          const child = cache[p][0];
          p = p + "/" + child.name;
          segs.push(child.name);
          segPaths.push(p);
        }
        if (cache[p] === undefined) need.push(p);
        out.push({ path: p, name: segs.join("/"), type: "dir", depth, segPaths });
        if (open.has(p) && cache[p]) walk(cache[p], p, depth + 1);
      }
    };
    walk(entries || [], root, 0);
    return { rows: out, need };
  }, [entries, open, cache, root]);

  // The directories whose listing is actually IN USE on screen: every visible
  // dir row (the tree already prefetches those — a folded a/b/c row is decided
  // by them) and the levels a folded row passes through. The auto-refresh
  // re-reads these and nothing else, so it costs what is on screen, not what
  // the tree holds: a folder scrolled past long ago keeps its stale cache until
  // it is re-opened, and fetchInto revalidates it then.
  const liveDirs = useMemo(() => {
    const out: string[] = [];
    for (const r of rows) if (r.type === "dir") for (const p of r.segPaths) if (cache[p]) out.push(p);
    return out;
  }, [rows, cache]);
  liveDirsRef.current = liveDirs;

  // Scoped auto-refresh: a session's turn ended, so re-read what is on screen
  // under its working copy (features/files/sessionRefresh.ts). Deliberately NOT
  // the whole tree — that is what the 更新 button still does.
  useEffect(() => {
    const prefix = scopedRefresh.prefix;
    // A tick that predates this mount is already covered by the initial load
    // (the tree is unmounted while its section is collapsed, so this is the
    // normal case, not an edge one).
    if (scopedRefresh.n === mountedScopedRef.current) return;
    if (!prefix || !running) return;
    if (secondary && (prefix === "repos" || prefix.startsWith("repos/"))) return; // the repos tree owns it
    if (root && prefix !== root && !prefix.startsWith(root + "/")) return; // another tree's business
    // In search mode the rows are rg hits, and re-running that query on every
    // turn end would be a recursive search per turn. The tree underneath is
    // refreshed here anyway, so leaving the query alone loses nothing.
    if (searchMode) return;
    // The context menu holds a row it is about to act on; pulling that row out
    // from under it turns the next click into an error toast.
    if (menuOpenRef.current) return;
    const targets = liveDirsRef.current.filter((p) => p === prefix || p.startsWith(prefix + "/"));
    // A working copy can also appear or vanish under the turn (a worktree added
    // or removed), which shows in its PARENT's listing — one extra read when
    // that parent is this tree's root. It is also the whole cost of a session
    // whose folder is not under ~/repos at all (a shell in the home dir): its
    // prefix matches no row, and this single read is all that happens.
    const alsoRoot = parentOf(prefix) === root;
    if (!targets.length && !alsoRoot) return;
    let alive = true;
    void (async () => {
      const [rootFresh, pairs] = await Promise.all([
        alsoRoot ? fsListFresh(root) : Promise.resolve<Fresh>(null),
        Promise.all(targets.map(async (p) => [p, await fsListFresh(p)] as const)),
      ]);
      if (!alive) return;
      if (Array.isArray(rootFresh)) setEntries((cur) => (sameEntries(cur ?? undefined, rootFresh) ? cur : rootFresh));
      applyFresh(pairs);
    })();
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scopedRefresh.n]);

  // Quick filter: keep rows whose displayed name matches, plus the ancestor dirs
  // that lead to them (so the match keeps its place in the tree). Rows are
  // pre-order with a depth, so an ancestor stack resolves lineage in one pass.
  // It filters the rows currently shown (loaded / expanded) — the same scope as
  // the リポジトリ filter.
  const filteredRows = useMemo(() => {
    if (!nq) return rows;
    const keep = new Array<boolean>(rows.length).fill(false);
    const stack: number[] = [];
    for (let i = 0; i < rows.length; i++) {
      const r = rows[i];
      while (stack.length && rows[stack[stack.length - 1]].depth >= r.depth) stack.pop();
      if (r.name.toLowerCase().includes(nq)) {
        keep[i] = true;
        for (const idx of stack) keep[idx] = true;
      }
      if (r.type === "dir") stack.push(i);
    }
    return rows.filter((_, i) => keep[i]);
  }, [rows, nq]);

  // Recursive search (searchable tree + active query): fetch the whole-subtree
  // matches from the backend (debounced), turned into flat file rows. The tree
  // filter above still runs but is bypassed for display in this mode.
  useEffect(() => {
    if (!searchMode) {
      setSearchRows(null);
      setSearchTrunc(false);
      setSearching(false);
      return;
    }
    let alive = true;
    setSearching(true);
    const t = setTimeout(async () => {
      const { results, truncated } = await fsSearch(root, q);
      if (!alive) return;
      const prefix = root ? root + "/" : "";
      const rows: Row[] = results.map((p) => {
        const relp = p.startsWith(prefix) ? p.slice(prefix.length) : p;
        const name = relp.split("/").pop() || relp;
        // dir prefix without the trailing slash — shown muted after the filename.
        return { path: p, name, type: "file", depth: 0, segPaths: [p], sub: relp.slice(0, Math.max(0, relp.length - name.length - 1)) };
      });
      setSearchRows(rows);
      setSearchTrunc(truncated);
      setSearching(false);
    }, 160);
    return () => {
      alive = false;
      clearTimeout(t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchMode, q, root, filesTick]);

  // The rows actually shown / navigated: flat search hits in search mode, else
  // the (tree-)filtered rows — both scoped to the active 作業グループ (docs/log/52)
  // for the repos-rooted tree: a row is kept when its top-level working copy
  // belongs to the group (worktree folders resolve via their "<base>@" prefix).
  const allRows = searchMode ? searchRows ?? NO_ROWS : filteredRows;
  const displayRows = useMemo(() => {
    if (!wset || root !== "repos") return allRows;
    return allRows.filter((r) => {
      const top = repoOf(r.path);
      return !top || wset.repos.includes(folderBase(top));
    });
  }, [allRows, wset, root]);
  const navigationRows = useMemo(
    () => (searchMode && groupByRepo ? displayRows.filter((r) => !collapsedRepos.has(repoOf(r.path))) : displayRows),
    [searchMode, groupByRepo, displayRows, collapsedRepos],
  );
  const repoResultCounts = useMemo(() => {
    const counts = new Map<string, number>();
    if (searchMode && groupByRepo) {
      for (const row of displayRows) {
        const repo = repoOf(row.path);
        counts.set(repo, (counts.get(repo) || 0) + 1);
      }
    }
    return counts;
  }, [searchMode, groupByRepo, displayRows]);

  // The rail, rather than this tree, owns scrolling. Mirror the directory
  // lineage at the visible edge into an overlay beneath the sticky section and
  // filter headers. Search results are flat, so they deliberately opt out.
  useLayoutEffect(() => {
    const wrap = wrapRef.current;
    const tree = treeRef.current;
    const scroller = wrap?.closest<HTMLElement>(".app-rail-scroll");
    if (!wrap || !tree || !scroller || searchMode) {
      // Bail out when already cleared, like the two setSticky calls below. An
      // unconditional `setSticky({rows:[],top:0})` stores a NEW object every run,
      // so it re-renders even when nothing changed — and entering search mode
      // re-ran this effect on every one of those renders (displayRows was a fresh
      // [] while the debounced search was in flight). That loop hit React's
      // "maximum update depth" (error #185), which unmounts the whole root: the
      // Console went blank the moment a query was typed into the ファイル filter.
      setSticky((old) => (old.rows.length || old.top ? { rows: [], top: 0 } : old));
      return;
    }
    let frame = 0;
    const update = () => {
      frame = 0;
      const wrapRect = wrap.getBoundingClientRect();
      const scrollRect = scroller.getBoundingClientRect();
      const section = wrap.closest<HTMLElement>(".ui-section");
      const headBottom = section?.querySelector<HTMLElement>(":scope > .ui-section-head")?.getBoundingClientRect().bottom ?? scrollRect.top;
      const filterBottom = section?.querySelector<HTMLElement>(".proj-filter-bar")?.getBoundingClientRect().bottom ?? headBottom;
      const edge = Math.max(scrollRect.top, headBottom, filterBottom);
      if (wrapRect.top >= edge || wrapRect.bottom <= edge) {
        setSticky((old) => (old.rows.length ? { rows: [], top: 0 } : old));
        return;
      }
      const elements = tree.querySelectorAll<HTMLElement>(":scope > li[data-path]");
      let through = -1;
      for (const el of elements) {
        if (el.getBoundingClientRect().top > edge + 0.5) break;
        const idx = Number(el.dataset.rowIndex);
        if (Number.isFinite(idx)) through = idx;
      }
      const limit = Math.min(7, Math.max(1, Math.floor((scrollRect.height * 0.4) / 22)));
      const nextRows = stickyAncestors(displayRows, through, limit);
      // Clamp to the tree's foot so the overlay is pushed away before the next
      // tree/section begins, matching native sticky containment.
      const top = Math.max(0, Math.min(edge - wrapRect.top, wrapRect.height - nextRows.length * 22));
      setSticky((old) =>
        old.top === top && old.rows.length === nextRows.length && old.rows.every((r, i) => r.path === nextRows[i].path)
          ? old
          : { rows: nextRows, top },
      );
    };
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(update);
    };
    update();
    scroller.addEventListener("scroll", schedule, { passive: true });
    const ro = new ResizeObserver(schedule);
    ro.observe(scroller);
    ro.observe(wrap);
    return () => {
      scroller.removeEventListener("scroll", schedule);
      ro.disconnect();
      if (frame) cancelAnimationFrame(frame);
    };
  }, [displayRows, searchMode]);

  // Prefetch entries for visible dirs whose children we don't have yet.
  useEffect(() => {
    need.forEach((p) => void fetchInto(p));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [need.join("\n")]);

  const activate = useCallback(
    (row: Row) => {
      if (row.type === "file") {
        setSelected(row.path);
        showFile(row.path);
      } else if (open.has(row.path)) {
        setSelected(row.path);
        collapse(row.segPaths);
      } else {
        void expand(row.path).then((deep) => setSelected(deep));
      }
    },
    [open, collapse, expand, showFile],
  );

  // Keep the selected row in view when moving through it by keyboard.
  const scrollRowIntoView = useCallback((path: string) => {
    treeRef.current?.querySelector<HTMLElement>(`li[data-path="${CSS.escape(path)}"]`)?.scrollIntoView({ block: "nearest" });
  }, []);

  // Keyboard navigation over the visible (filtered) rows: ↑↓ move the selection,
  // →← open/close a folder (or step into / out to the parent), Enter opens a file
  // (Ctrl/⌘+Enter in a new pane) or toggles a folder, and Ctrl/⌘+F jumps to the
  // filter box.
  const onTreeKeyDown = useCallback(
    (e: RKeyboardEvent<HTMLUListElement>) => {
      if ((e.target as HTMLElement).closest(".files-search-group-toggle")) return;
      if ((e.ctrlKey || e.metaKey) && (e.key === "f" || e.key === "F")) {
        e.preventDefault();
        focusInput();
        return;
      }
      if (menu) return; // let the context menu own the keyboard while it's open
      const list = navigationRows;
      if (!list.length) return;
      const idx = list.findIndex((r) => r.path === selected);
      // Menu key / Shift+F10 opens the selected row's right-click menu at its position.
      if (isContextMenuKey(e)) {
        const r = list[idx];
        if (r) {
          e.preventDefault();
          const el = treeRef.current?.querySelector<HTMLElement>(`li[data-path="${CSS.escape(r.path)}"]`);
          const a = el ? menuAnchor(el) : { x: 0, y: 0 };
          setMenu({ x: a.x, y: a.y, row: r });
        }
        return;
      }
      const pick = (to: number) => {
        const r = list[Math.max(0, Math.min(to, list.length - 1))];
        if (r) {
          setSelected(r.path);
          scrollRowIntoView(r.path);
        }
      };
      switch (e.key) {
        case "ArrowDown":
          e.preventDefault();
          pick(idx < 0 ? 0 : idx + 1);
          break;
        case "ArrowUp":
          e.preventDefault();
          pick(idx < 0 ? 0 : idx - 1);
          break;
        case "ArrowRight": {
          e.preventDefault();
          const r = list[idx];
          if (!r) return pick(0);
          if (r.type !== "dir") return;
          if (!open.has(r.path)) void expand(r.path).then((deep) => setSelected(deep));
          else pick(idx + 1); // already open → step into the first child
          break;
        }
        case "ArrowLeft": {
          e.preventDefault();
          const r = list[idx];
          if (!r) return;
          if (r.type === "dir" && open.has(r.path)) {
            collapse(r.segPaths);
            return;
          }
          // else step out to the nearest shallower row (the parent).
          for (let j = idx - 1; j >= 0; j--) {
            if (list[j].depth < r.depth) {
              pick(j);
              break;
            }
          }
          break;
        }
        case "Enter": {
          e.preventDefault();
          const r = list[idx];
          if (!r) return;
          if (r.type === "file") {
            setSelected(r.path);
            if (e.ctrlKey || e.metaKey) showFileSplit(r.path);
            else showFile(r.path);
          } else if (open.has(r.path)) {
            collapse(r.segPaths);
          } else {
            void expand(r.path).then((deep) => setSelected(deep));
          }
          break;
        }
      }
    },
    [navigationRows, selected, open, menu, expand, collapse, showFile, showFileSplit, focusInput, scrollRowIntoView],
  );

  // Enter in the filter box hands focus to the active searchable tree:
  // focus the tree and land the selection on the first visible row if it's astray.
  useEffect(() => {
    if (!focusTreeN || !searchable) return;
    treeRef.current?.focus();
    setSelected((cur) => {
      if (cur && navigationRows.some((r) => r.path === cur)) return cur;
      const first = navigationRows[0];
      if (first) scrollRowIntoView(first.path);
      return first ? first.path : cur;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusTreeN]);

  // Reveal: when something requests a path under the root (a clone just landed,
  // or a repo row's フォルダを開く), expand its ancestor chain — and the target
  // itself when it's a directory — then select it. root "" (home) contains every
  // home-relative path.
  useEffect(() => {
    const p = reveal.path;
    if (!p || (root && p !== root && !p.startsWith(root + "/"))) return;
    if (secondary && (p === "repos" || p.startsWith("repos/"))) return; // the repos tree owns it
    let alive = true;
    void (async () => {
      const rel = !root ? p : p === root ? "" : p.slice(root.length + 1);
      const segs = rel ? rel.split("/").filter(Boolean) : [];
      if (!segs.length) return; // the root itself has no row to select
      let cur = root;
      const toOpen: string[] = [];
      for (let i = 0; i < segs.length - 1; i++) {
        cur = joinPath(cur, segs[i]); // joinPath: a "" root must not yield "/xxx"
        await fetchInto(cur);
        if (!alive) return;
        toOpen.push(cur);
      }
      if (!alive) return;
      if (toOpen.length) setOpen((s) => new Set([...s, ...toOpen]));
      // Directory target (the usual case: a working-copy folder) → open it too.
      const parentEntries = await fetchInto(segs.length > 1 ? cur : root);
      if (!alive) return;
      if (parentEntries.find((e: Entry) => e.name === segs[segs.length - 1])?.type === "dir") {
        await expand(p);
        if (!alive) return;
      }
      setSelected(p);
      // Selecting isn't enough — bring the revealed row into view. The row only
      // mounts after the ancestor-expand re-render commits, so retry across a few
      // frames until its <li> exists, then scroll it into the middle of the tree.
      // When the reveal was asked for (reveal.focus), hand keyboard focus to the tree
      // at the same moment: the reader is now looking at that row, so ↑↓ should walk
      // from it instead of doing nothing until they click.
      let tries = 0;
      const scrollToRow = () => {
        if (!alive) return;
        const el = treeRef.current?.querySelector<HTMLElement>(`li[data-path="${CSS.escape(p)}"]`);
        if (el) {
          el.scrollIntoView({ block: "center" });
          if (reveal.focus) treeRef.current?.focus({ preventScroll: true });
        } else if (tries++ < 10) requestAnimationFrame(scrollToRow);
      };
      requestAnimationFrame(scrollToRow);
    })();
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reveal.n]);

  // Middle-click opens a file in a freshly split pane.
  const onAuxOpen = (path: string) => ({
    onMouseDown: (e: RMouseEvent) => e.button === 1 && e.preventDefault(),
    onAuxClick: (e: RMouseEvent) => {
      if (e.button === 1) {
        e.preventDefault();
        setSelected(path);
        showFileSplit(path);
      }
    },
  });

  // --- upload (drag-drop) ---
  const refreshDir = useCallback(
    async (dir: string) => {
      const d = await fsList(dir);
      const e = d.entries || [];
      if (dir === root) setEntries(e);
      else {
        setCache((c) => ({ ...c, [dir]: e }));
        setOpen((s) => new Set(s).add(dir));
      }
    },
    [root],
  );
  const doUpload = useCallback(
    async (dir: string, fileList: FileList | null) => {
      const files = Array.from(fileList || []).filter((f) => f && f.name);
      if (!files.length) return;
      let res = await uploadFiles(dir, files);
      if (res.status === 409 && Array.isArray(res.conflicts) && (res.conflicts as string[]).length) {
        const ok = await askConfirm({
          title: t("proj.overwrite_title"),
          body: t("proj.overwrite_body", { names: (res.conflicts as string[]).join(", ") }),
          confirmLabel: t("proj.overwrite_confirm"),
          danger: true,
        });
        if (ok) res = await uploadFiles(dir, files, { overwrite: true });
      }
      if (res.error) toast(t("proj.upload_failed", { msg: (res.error as { message?: string }).message || String(res.error) }));
      await refreshDir(dir);
      setDropTarget(null);
    },
    [refreshDir, askConfirm, toast],
  );
  const onDropTo = (dir: string) => (e: RDragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDropTarget(null);
    if (!running) return;
    const files = e.dataTransfer?.files;
    if (files && files.length) void doUpload(dir, files);
  };
  const onDragOverTo = (dir: string) => (e: RDragEvent) => {
    if (e.dataTransfer && Array.from(e.dataTransfer.types || []).includes("Files")) {
      e.preventDefault();
      e.stopPropagation();
      if (!running) {
        e.dataTransfer.dropEffect = "none";
        return;
      }
      e.dataTransfer.dropEffect = "copy";
      setDropTarget(dir);
    }
  };

  // --- file ops (right-click menu) ---
  const newFolder = async (parent: string) => {
    const name = window.prompt(t("proj.new_folder_prompt", { parent: baseName(parent) }), "");
    if (!name || !name.trim()) return;
    const p = joinPath(parent, name.trim());
    const res = await fsMkdir(p);
    if (res.error) return toast(t("proj.create_failed", { msg: (res.error as { message?: string }).message || String(res.error) }));
    await refreshDir(parent);
    setSelected(p);
  };
  const newFile = async (parent: string) => {
    const name = window.prompt(t("proj.new_file_prompt", { parent: baseName(parent) }), "");
    if (!name || !name.trim()) return;
    const p = joinPath(parent, name.trim());
    const res = await fsNewFile(p);
    if (res.error) return toast(t("proj.create_failed", { msg: (res.error as { message?: string }).message || String(res.error) }));
    await refreshDir(parent);
    setSelected(p);
    showFile(p);
  };
  const renameRow = async (row: Row) => {
    const base = baseName(row.path);
    const name = window.prompt(t("proj.rename_prompt"), base);
    if (!name || !name.trim() || name.trim() === base) return;
    const parent = parentOf(row.path);
    const to = joinPath(parent, name.trim());
    const res = await fsRename(row.path, to);
    if (res.error) return toast(t("proj.rename_failed", { msg: (res.error as { message?: string }).message || String(res.error) }));
    await refreshDir(parent);
    setSelected(to);
  };
  const deleteRow = async (row: Row) => {
    const ok = await askConfirm({
      title: t("proj.delete_title"),
      body: t("proj.delete_body", { path: row.path, dirNote: row.type === "dir" ? t("proj.delete_dir_note") : "" }),
      confirmLabel: t("common.delete_do"),
      danger: true,
    });
    if (!ok) return;
    const res = await fsDelete(row.path);
    if (res.error) return toast(t("proj.delete_failed", { msg: (res.error as { message?: string }).message || String(res.error) }));
    await refreshDir(parentOf(row.path));
    setSelected(parentOf(row.path) || null);
  };
  const copyText = (text: string, label: string) => {
    if (navigator.clipboard?.writeText)
      navigator.clipboard.writeText(text).then(
        () => toast(t("proj.copied", { label }), { kind: "success" }),
        () => toast(t("common.copy_failed")),
      );
    else toast(t("common.copy_failed"));
  };

  // Context menu: open at the cursor; close on outside click / Escape / blur.
  useEffect(() => {
    if (!menu) return;
    const close = () => setMenu(null);
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && close();
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", onKey);
    window.addEventListener("blur", close);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("blur", close);
    };
  }, [menu]);
  // Clamp EVERY render, before paint: the JSX re-applies the raw cursor coords
  // as inline style on each re-render (store polls re-render this component
  // while the menu is open), which would undo a one-shot clamp and push a menu
  // opened near the rail's foot back off-screen.
  useLayoutEffect(() => {
    if (menu && menuRef.current) placeFixed(menuRef.current, menu.x, menu.y, wrapRef.current?.closest<HTMLElement>(".app-rail"));
  });
  const runMenu = (fn: () => void) => {
    setMenu(null);
    fn();
  };
  const menuDir = menu ? (menu.row.type === "dir" ? menu.row.path : parentOf(menu.row.path)) : root;

  return (
    <div ref={wrapRef} className="proj-files fstree-wrap">
      <input
        ref={fileInputRef}
        type="file"
        multiple
        hidden
        onChange={(e) => {
          void doUpload(root, e.target.files);
          e.target.value = "";
        }}
      />
      <ul
        ref={treeRef}
        className={"fstree proj-fstree" + (dropTarget === root ? " drop-root" : "")}
        role="tree"
        aria-label={tr("proj.files_aria")}
        tabIndex={0}
        aria-activedescendant={(() => {
          const i = displayRows.findIndex((r) => r.path === selected && navigationRows.includes(r));
          return i >= 0 ? `${uid}-${i}` : undefined;
        })()}
        onKeyDown={onTreeKeyDown}
        onMouseDown={() => treeRef.current?.focus({ preventScroll: true })}
        onDragOver={onDragOverTo(root)}
        onDragLeave={() => setDropTarget(null)}
        onDrop={onDropTo(root)}
      >
        {!running ? (
          <EmptyState icon="debug-disconnect" title={tr("mirror.ws_stopped")} />
        ) : searchMode ? (
          searching && !searchRows ? (
            <EmptyState icon="loading" title={tr("start.searching")} />
          ) : displayRows.length === 0 ? (
            <li className="proj-sub-empty">{tr("proj.no_match", { q: q.trim() })}</li>
          ) : null
        ) : entries === null ? (
          <EmptyState icon="loading" title={tr("chat.ph_loading")} />
        ) : entries.length === 0 ? (
          <li className="proj-sub-empty">{tr("proj.empty")}</li>
        ) : nq && displayRows.length === 0 ? (
          <li className="proj-sub-empty">{tr("proj.no_match", { q: q.trim() })}</li>
        ) : null}
        {displayRows.map((r, i) => {
          const isOpen = r.type === "dir" && open.has(r.path);
          const isSel = r.path === selected;
          const isActiveFile = r.type === "file" && activeFile === r.path;
          const isDir = r.type === "dir";
          // The working copy this row IS (depth 0 under repos/), if any: it wears
          // that リポジトリ row's icon, and a worktree also shows its branch —
          // the folder is "<base>@<slug>" and never says what is checked out.
          // segPaths[0] survives chain folding (the row name may read "repo/sub").
          const wc = markRepos && isDir && r.depth === 0
            ? repos.find((x) => x.name === baseName(r.segPaths[0]))
            : undefined;
          return (
            <Fragment key={r.path}>
            {searchMode && groupByRepo && (i === 0 || repoOf(displayRows[i - 1].path) !== repoOf(r.path)) && (
              <li className="files-search-group" role="presentation">
                <button
                  type="button"
                  className="files-search-group-toggle"
                  title={repoOf(r.path)}
                  aria-expanded={!collapsedRepos.has(repoOf(r.path))}
                  onMouseDown={(e) => e.stopPropagation()}
                  onClick={() => {
                    const repo = repoOf(r.path);
                    setCollapsedRepos((current) => {
                      const next = new Set(current);
                      if (next.has(repo)) next.delete(repo);
                      else next.add(repo);
                      return next;
                    });
                    if (selected && repoOf(selected) === repo) setSelected(null);
                  }}
                >
                  <Icon name={collapsedRepos.has(repoOf(r.path)) ? "chevron-right" : "chevron-down"} />
                  <Icon name="root-folder" />
                  {/* Same handle as the 変更 view's bands: project + branch. */}
                  <span className="files-search-group-name">
                    <WorkingCopyLabel folder={repoOf(r.path)} />
                  </span>
                  <span className="files-search-group-count">{repoResultCounts.get(repoOf(r.path))}</span>
                </button>
              </li>
            )}
            {!collapsedRepos.has(repoOf(r.path)) && (
            <li
              id={`${uid}-${i}`}
              data-path={r.path}
              data-row-index={i}
              role="treeitem"
              aria-selected={isSel}
              {...(isDir ? { "aria-expanded": isOpen } : {})}
              className={"fsrow" + (isSel ? " selected" : "") + (isDir && dropTarget === r.path ? " drop-hover" : "")}
              style={{ paddingLeft: 4 + r.depth * 14 }}
              title={isDir ? undefined : tr("proj.open_new_pane")}
              onClick={(e) => {
                if (!isDir && (e.ctrlKey || e.metaKey)) {
                  setSelected(r.path);
                  showFileSplit(r.path);
                  return;
                }
                activate(r);
              }}
              onContextMenu={(e) => {
                e.preventDefault();
                e.stopPropagation();
                setMenu({ x: e.clientX, y: e.clientY, row: r });
              }}
              onDragOver={isDir ? onDragOverTo(r.path) : undefined}
              onDrop={isDir ? onDropTo(r.path) : undefined}
              {...(isDir ? {} : onAuxOpen(r.path))}
            >
              <span className={isDir ? "fs-dir" : "fs-file" + (isActiveFile ? " active" : "")}>
                <span className="fs-chev">{isDir ? (isOpen ? "▾" : "▸") : ""}</span>
                <span className="fs-ic">
                  {!isDir ? (
                    <FileIcon name={r.name} />
                  ) : wc ? (
                    <Icon name={wc.worktree ? "git-branch" : "root-folder"} className="fi-folder" />
                  ) : (
                    <DirIcon open={isOpen} />
                  )}
                </span>
                <span className="fs-name">
                  {r.name}
                  {r.sub ? <span className="fs-sub"> {r.sub}</span> : null}
                </span>
                {/* A worktree's branch, outside .fs-name so the two shrink
                    independently instead of the name's ellipsis eating it. */}
                {wc?.worktree && wc.branch ? <span className="fs-branch">{wc.branch}</span> : null}
              </span>
            </li>
            )}
            </Fragment>
          );
        })}
        {searchMode && searchTrunc && displayRows.length > 0 && (
          <li className="proj-sub-empty">{tr("proj.limit_reached")}</li>
        )}
      </ul>
      {sticky.rows.length > 0 && (
        <div className="fstree-sticky" style={{ top: sticky.top }} role="presentation">
          {sticky.rows.map((r) => {
            const isOpen = open.has(r.path);
            return (
              <div
                key={r.path}
                className="fsrow fs-sticky-row"
                style={{ paddingLeft: 4 + r.depth * 14 }}
                title={r.path}
                onClick={() => treeRef.current?.querySelector<HTMLElement>(`li[data-path="${CSS.escape(r.path)}"]`)?.scrollIntoView({ block: "center" })}
              >
                <button
                  type="button"
                  className="fs-sticky-caret"
                  aria-label={tr("proj.collapse", { name: r.name })}
                  onClick={(e) => {
                    e.stopPropagation();
                    collapse(r.segPaths);
                  }}
                >
                  {isOpen ? "▾" : "▸"}
                </button>
                <span className="fs-ic"><DirIcon open={isOpen} /></span>
                <span className="fs-name">{r.name}</span>
              </div>
            );
          })}
        </div>
      )}
      {menu &&
        createPortal(
          <ul className="ui-menu files-ctxmenu" ref={menuRef} style={{ left: menu.x, top: menu.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
            <li>
              <button type="button" className="ui-menu-item" onClick={() => runMenu(() => void newFile(menuDir))}>
                <Icon name="new-file" /> {tr("proj.new_file")}
              </button>
            </li>
            <li>
              <button type="button" className="ui-menu-item" onClick={() => runMenu(() => void newFolder(menuDir))}>
                <Icon name="new-folder" /> {tr("proj.new_folder")}
              </button>
            </li>
            <li>
              <button type="button" className="ui-menu-item" onClick={() => runMenu(() => copyText(baseName(menu.row.path), t("proj.name")))}>
                <Icon name="copy" /> {tr("proj.copy_name")}
              </button>
            </li>
            <li>
              <button type="button" className="ui-menu-item" onClick={() => runMenu(() => copyText(menu.row.path, t("proj.rel_path")))}>
                <Icon name="copy" /> {tr("proj.copy_rel_path")}
              </button>
            </li>
            {menu.row.type === "file" && (
              <li>
                <button
                  type="button"
                  className="ui-menu-item"
                  onClick={() => runMenu(() => openTarget({ content: { kind: "read", filePath: menu.row.path } }))}
                >
                  <Icon name="book" /> {tr("proj.open_reader")}
                </button>
              </li>
            )}
            {menu.row.type === "file" && (
              <li>
                <a className="ui-menu-item files-ctx-a" href={downloadURL(menu.row.path)} download onClick={() => setMenu(null)}>
                  <Icon name="cloud-download" /> {tr("proj.download")}
                </a>
              </li>
            )}
            {menu.row.type === "file" && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => runMenu(() => void openVerbChat(menu.row.path, "translate"))}>
                  <Icon name="globe" /> {tr("proj.translate")}
                </button>
              </li>
            )}
            {menu.row.type === "file" && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => runMenu(() => void openVerbChat(menu.row.path, "summarize"))}>
                  <Icon name="list-selection" /> {tr("proj.summarize")}
                </button>
              </li>
            )}
            <li>
              <button type="button" className="ui-menu-item" onClick={() => runMenu(() => void renameRow(menu.row))}>
                <Icon name="edit" /> {tr("proj.rename")}
              </button>
            </li>
            <li>
              <button type="button" className="ui-menu-item danger" onClick={() => runMenu(() => void deleteRow(menu.row))}>
                <Icon name="trash" /> {tr("common.delete")}
              </button>
            </li>
          </ul>,
          document.body,
        )}
    </div>
  );
}
