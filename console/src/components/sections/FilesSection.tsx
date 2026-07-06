import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent, DragEvent as RDragEvent, KeyboardEvent as RKeyboardEvent } from "react";
import { useApp } from "../../state.jsx";
import { api, uploadFiles, downloadURL, fsMkdir, fsNewFile, fsRename, fsDelete, assistantList, chatCreate } from "../../api.js";
import { dirName, imageFormat, isProbablyBinary } from "../../lib/filemeta.js";
import type { Assistant } from "../../types/assistant.ts";
import { placeFixed } from "../../lib/placeFixed.js";
import Section from "../Section.jsx";
import Icon from "../Icon.jsx";
import FileIcon, { DirIcon } from "../FileIcon.jsx";
import { useConfirm } from "../ConfirmProvider.jsx";
import { useToast } from "../ToastProvider.jsx";
import SendSelectionModal from "../SendSelectionModal.jsx";
import EmptyState from "../EmptyState.jsx";
import { useDismiss } from "../../lib/useDismiss.js";

// A server directory entry (api/fs/tree). Visible tree rows are flattened into Row.
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
}
// A git change in "changes" view (api/fs/changes), grouped by repo.
interface FsChange {
  path: string;
  repo: string;
  untracked?: boolean;
  index?: string;
  worktree?: string;
}
interface Menu {
  x: number;
  y: number;
  row: Row | null;
}

const joinPath = (d: string, n: string) => (d ? d + "/" + n : n);
const parentOf = (p: string) => {
  const i = p.lastIndexOf("/");
  return i < 0 ? "" : p.slice(0, i);
};

// git status (porcelain XY) -> a one-char badge + color class for the changes list.
function changeBadge(c: FsChange) {
  if (c.untracked) return { ch: "U", cls: "st-add", label: "未追跡" };
  const code = c.worktree !== " " && c.worktree !== "" ? c.worktree : c.index;
  if (code === "D") return { ch: "D", cls: "st-del", label: "削除" };
  if (code === "A") return { ch: "A", cls: "st-add", label: "追加" };
  if (code === "R" || code === "C") return { ch: code, cls: "st-mod", label: "改名" };
  return { ch: "M", cls: "st-mod", label: "変更" };
}

// A directory is a "passthrough" link in a compact chain when its sole entry is one
// subdirectory (no files) — these get folded into one row (a/b/c) so deep, single-
// child paths don't waste vertical space, and expanding one descends the whole chain.
const soleChildDir = (entries: Entry[] | undefined): Entry | null =>
  entries && entries.length === 1 && entries[0].type === "dir" ? entries[0] : null;

const fsList = (path: string) =>
  api(`api/fs/tree?path=${encodeURIComponent(path)}`).catch(() => ({ entries: [] }));

// Files: a lazily-expanded, read-only tree of the workspace home (denylist applied
// on the Agent). The tree is centrally managed (flattened visible rows + an open
// set) so it's keyboard-navigable: ↑/↓ move, → expand / into child, ← collapse /
// to parent, Enter opens a file (or toggles a folder).
export default function FilesSection() {
  const { filePath, showFile, showFileSplit, filesKey, reveal, wsState, openChat } = useApp();
  const askConfirm = useConfirm();
  const toast = useToast();
  const running = wsState === "running"; // WS down → file mutations are inert

  // Middle-click opens a file in a freshly split pane (like the Sessions list).
  // Suppress the mousedown default so the browser doesn't start autoscroll.
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
  const [root, setRoot] = useState<Entry[] | null>(null);
  const [open, setOpen] = useState<Set<string>>(() => new Set()); // expanded dir paths
  const [cache, setCache] = useState<Record<string, Entry[]>>({}); // dir path -> entries
  const [selected, setSelected] = useState<string | null>(null); // path
  const [browseRoot, setBrowseRoot] = useState(""); // absolute browse root (home), for "絶対パスをコピー"
  const [reloadKey, setReloadKey] = useState(0);
  const [view, setView] = useState(() => localStorage.getItem("af-files-view") || "tree"); // tree | changes
  const [changes, setChanges] = useState<FsChange[] | null>(null); // changes-mode: aggregated git status
  const [dropTarget, setDropTarget] = useState<string | null>(null); // dir path being hovered with a drag ("" = root)
  const [uploading, setUploading] = useState(false);
  const opsRef = useRef<HTMLDivElement>(null); // ＋ menu wrap (outside-click test + anchor)
  const opsMenuRef = useRef<HTMLDivElement>(null); // ＋ dropdown (clamped into the viewport)
  const [opsOpen, setOpsOpen] = useState(false); // ＋ (new / upload) header dropdown
  const [menu, setMenu] = useState<Menu | null>(null); // context menu: { x, y, row|null }
  const [subOpen, setSubOpen] = useState(false); // "アシスタントで開く" submenu expanded (docs/19 Phase C)
  const [assistants, setAssistants] = useState<Assistant[]>([]); // for the assistant submenu
  const [sendFile, setSendFile] = useState<string | null>(null); // file → "セッションに送る" modal target
  const ctxRef = useRef<HTMLUListElement>(null); // context menu (clamped into the viewport)
  const treeRef = useRef<HTMLUListElement>(null);
  const selRef = useRef<HTMLLIElement>(null);
  // VS Code-style "sticky scroll": the ancestor folders of the topmost visible row,
  // pinned at the top of the tree as you scroll so the current path stays in view.
  const wrapRef = useRef<HTMLDivElement>(null); // positioning context for the sticky overlay
  const scrollRef = useRef<HTMLElement | null>(null); // the .leftpane-scroll scroller (found lazily)
  const [sticky, setSticky] = useState<{ rows: Row[]; top: number }>({ rows: [], top: 0 });
  // Set when a selection change is an AUTO one (the first row picked on load), so the
  // "keep selected visible" scroll below skips it — otherwise a WS Start / files refresh
  // would yank the whole left nav to the top file. User-driven selection still scrolls.
  const skipScrollRef = useRef(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  // Mirror `open` into a ref so the reload effect can refetch the expanded dirs
  // without taking `open` as a dependency (which would refetch on every toggle).
  const openRef = useRef(open);
  useEffect(() => {
    openRef.current = open;
  }, [open]);
  // Mirror selected + cache into refs so the "follow the active file" effect can read the
  // current values without re-running on every selection change or cache write.
  const selectedRef = useRef(selected);
  useEffect(() => {
    selectedRef.current = selected;
  }, [selected]);
  const cacheRef = useRef(cache);
  useEffect(() => {
    cacheRef.current = cache;
  }, [cache]);

  const setViewPersist = useCallback((v: string) => {
    setView(v);
    localStorage.setItem("af-files-view", v);
  }, []);

  // Changes mode: aggregate git status across repos/. Refetch on view switch,
  // manual ⟳, and filesKey bumps (clone / workspace lifecycle / upload).
  useEffect(() => {
    if (view !== "changes") return;
    let alive = true;
    setChanges(null);
    api("api/fs/changes")
      .then((d) => alive && setChanges(d.changes || []))
      .catch(() => alive && setChanges([]));
    return () => {
      alive = false;
    };
  }, [view, reloadKey, filesKey]);

  // Replace one directory's cached entries from the server (after an upload), and
  // make sure it's expanded so the new file is visible.
  const refreshDir = useCallback(async (dir: string) => {
    const d = await fsList(dir);
    const entries = d.entries || [];
    if (dir === "") setRoot(entries);
    else {
      setCache((c) => ({ ...c, [dir]: entries }));
      setOpen((s) => new Set(s).add(dir));
    }
  }, []);

  // Upload dropped/picked files into `dir`. On a name collision the server
  // returns 409 + conflicts; confirm once, then resend with overwrite.
  const doUpload = useCallback(
    async (dir: string, fileList: FileList | null) => {
      const files = Array.from(fileList || []).filter((f) => f && f.size >= 0 && f.name);
      if (!files.length) return;
      setUploading(true);
      try {
        let res: any = await uploadFiles(dir, files);
        if (res.status === 409 && res.conflicts && res.conflicts.length) {
          const ok = await askConfirm({
            title: "ファイルを上書き",
            body: `${res.conflicts.join(", ")} は既に存在します。上書きしますか？`,
            confirmLabel: "上書きする",
            danger: true,
          });
          if (ok) {
            res = await uploadFiles(dir, files, { overwrite: true });
          }
        }
        if (res.error) toast("アップロード失敗: " + (res.error.message || res.error));
        await refreshDir(dir);
        setSelected(dir || (files[0] ? files[0].name : null));
      } finally {
        setUploading(false);
        setDropTarget(null);
      }
    },
    [refreshDir, askConfirm, toast],
  );

  // Drop onto a dir row uploads into it; onto the empty tree uploads into root.
  const onDropTo = useCallback(
    (dir: string) => (e: RDragEvent) => {
      e.preventDefault();
      e.stopPropagation();
      setDropTarget(null);
      if (!running) return; // WS stopped → ignore the drop (still prevented above)
      const files = e.dataTransfer?.files;
      if (files && files.length) doUpload(dir, files);
    },
    [doUpload, running],
  );
  const onDragOverTo = useCallback(
    (dir: string) => (e: RDragEvent) => {
      if (e.dataTransfer && Array.from(e.dataTransfer.types || []).includes("Files")) {
        // preventDefault even when stopped so the browser doesn't navigate to the
        // dropped file; just deny the drop and skip the upload affordance.
        e.preventDefault();
        e.stopPropagation();
        if (!running) {
          e.dataTransfer.dropEffect = "none";
          return;
        }
        e.dataTransfer.dropEffect = "copy";
        setDropTarget(dir);
      }
    },
    [running],
  );

  // Close the ＋ header menu on any outside click. Containment check (not
  // stopPropagation on the wrap): stopPropagation would swallow OTHER dropdowns'
  // document-level close listeners, leaving multiple menus open at once and
  // lifting several .pane-section into z-index:10, which stack-overlap.
  useDismiss(opsRef, opsOpen, () => setOpsOpen(false));

  // --- file operations (new / rename / delete) ---
  const newFolder = useCallback(
    async (parent: string) => {
      const name = window.prompt(`新しいフォルダ名（作成先: ${parent || "home"}）`, "");
      if (!name || !name.trim()) return;
      const p = joinPath(parent, name.trim());
      const res: any = await fsMkdir(p);
      if (res.error) return toast("作成失敗: " + (res.error.message || res.error));
      await refreshDir(parent);
      setSelected(p);
    },
    [refreshDir],
  );
  const newFile = useCallback(
    async (parent: string) => {
      const name = window.prompt(`新しいファイル名（作成先: ${parent || "home"}）`, "");
      if (!name || !name.trim()) return;
      const p = joinPath(parent, name.trim());
      const res: any = await fsNewFile(p);
      if (res.error) return toast("作成失敗: " + (res.error.message || res.error));
      await refreshDir(parent);
      setSelected(p);
      showFile(p);
    },
    [refreshDir, showFile],
  );
  const renameRow = useCallback(
    async (row: Row) => {
      const base = row.path.split("/").pop();
      const name = window.prompt("名前を変更", base);
      if (!name || !name.trim() || name.trim() === base) return;
      const parent = parentOf(row.path);
      const to = joinPath(parent, name.trim());
      const res: any = await fsRename(row.path, to);
      if (res.error) return toast("変更失敗: " + (res.error.message || res.error));
      await refreshDir(parent);
      setSelected(to);
    },
    [refreshDir],
  );
  const deleteRow = useCallback(
    async (row: Row) => {
      const ok = await askConfirm({
        title: "削除",
        body: `${row.path} を削除します。${row.type === "dir" ? "フォルダの中身ごと削除されます。" : ""}`,
        confirmLabel: "削除する",
        danger: true,
      });
      if (!ok) return;
      const res: any = await fsDelete(row.path);
      if (res.error) return toast("削除失敗: " + (res.error.message || res.error));
      await refreshDir(parentOf(row.path));
      setSelected(parentOf(row.path) || null);
    },
    [refreshDir, askConfirm, toast],
  );

  // Context menu: open at the cursor; close on outside click / Escape / scroll.
  const openMenu = useCallback((e: RMouseEvent, row: Row | null) => {
    e.preventDefault();
    e.stopPropagation();
    setSubOpen(false); // each open starts with the assistant submenu collapsed
    setMenu({ x: e.clientX, y: e.clientY, row });
  }, []);
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
  // runMenu closes the menu, then runs the action.
  const runMenu = (fn: () => void) => {
    setMenu(null);
    fn();
  };

  // Copy text to the clipboard with a toast; label names what was copied. Used by the
  // context-menu "名前をコピー / パスをコピー" items. navigator.clipboard needs a secure
  // context — fall back to a toast so a click never silently no-ops.
  const copyText = useCallback(
    (text: string, label: string) => {
      if (navigator.clipboard?.writeText) {
        navigator.clipboard.writeText(text).then(
          () => toast(`${label}をコピーしました`),
          () => toast("コピーに失敗しました"),
        );
      } else {
        toast("コピーに失敗しました");
      }
    },
    [toast],
  );

  // Assistant submenu (docs/19 Phase C): fetch the assistant list once so a right-click
  // can hand the file/dir to an assistant. Failure just leaves the submenu empty.
  useEffect(() => {
    assistantList()
      .then((r) => setAssistants(r.assistants || []))
      .catch(() => {});
  }, []);
  // A row is eligible for "アシスタントで開く" if it's a directory, or a file that isn't a
  // binary/image asset (translate/summarize/ask only make sense on readable content).
  const menuAssistable = (row: Row) =>
    row.type === "dir" || (!isProbablyBinary(row.name) && !imageFormat(row.name));
  // Create a chat from the assistant, attaching the row's file/dir, then open it with the
  // server-composed seed prefilled in the composer (never auto-sent).
  const openWithAssistant = async (a: Assistant, row: Row, verb: "translate" | "summarize" | "") => {
    try {
      const c = await chatCreate(a.id, row.name, { attachPath: row.path, seedVerb: verb });
      if (c && c.id) openChat(c.id, c.seed);
      else toast("チャットの作成に失敗しました");
    } catch {
      toast("チャットの作成に失敗しました");
    }
  };
  const translateAsst = assistants.find((a) => a.id === "translate");
  const summarizeAsst = assistants.find((a) => a.id === "general");
  // Keep the context menu on-screen: it opens at the raw cursor point, which can be
  // near a right/bottom/left edge on a phone. useLayoutEffect runs before paint so
  // there's no visible jump from the initial cursor position.
  useLayoutEffect(() => {
    if (menu && ctxRef.current)
      // Clamp within the left rail's scroll container so the menu stops short of its
      // vertical scrollbar instead of painting over it.
      placeFixed(ctxRef.current, menu.x, menu.y, ctxRef.current.closest<HTMLElement>(".leftpane-scroll"));
  }, [menu]);
  // The ＋ dropdown: promote to a viewport-clamped fixed menu below its button so it
  // can't be clipped by the pane's overflow or spill off a narrow screen (the button
  // sits mid-header, so the CSS right:0 anchoring opened it off the left edge).
  useLayoutEffect(() => {
    const el = opsMenuRef.current;
    const anchor = opsRef.current;
    if (!opsOpen || !el || !anchor) return;
    el.style.position = "fixed";
    el.style.right = "auto";
    const a = anchor.getBoundingClientRect();
    placeFixed(el, a.right - el.offsetWidth, a.bottom + 4);
  }, [opsOpen]);

  // Reload: on manual ⟳, and on filesKey bumps (clone / workspace stop·start).
  // PRESERVES the expanded folders (and selection): it refetches root and every
  // currently-open dir from the server, rather than collapsing the whole tree.
  useEffect(() => {
    let alive = true;
    (async () => {
      const r = await fsList("");
      if (!alive) return;
      const rootEntries = r.entries || [];
      setRoot(rootEntries);
      if (r.root) setBrowseRoot(r.root);
      // Refetch each open dir so the refresh reflects server state while keeping
      // it expanded. A dir that vanished returns no entries and just won't render.
      const fresh: Record<string, Entry[]> = {};
      for (const p of Array.from(openRef.current)) {
        const d = await fsList(p);
        if (!alive) return;
        fresh[p] = d.entries || [];
      }
      if (!alive) return;
      setCache(fresh);
      setSelected((s) => {
        if (s) return s; // keep the user's selection across a refresh
        skipScrollRef.current = true; // first-row auto-pick: don't scroll the nav
        return rootEntries[0] ? rootEntries[0].name : null;
      });
    })();
    return () => {
      alive = false;
    };
  }, [reloadKey, filesKey]);

  // Reveal: expand the tree to a home-relative path (e.g. "repos/foo") and select it
  // — used when a repo is clicked / just cloned. Refetches root + each segment fresh
  // (so a newly-cloned dir shows even if the tree was stale) and opens the chain.
  useEffect(() => {
    if (!reveal || !reveal.path) return;
    const revealPath: string = reveal.path;
    let alive = true;
    (async () => {
      const r = await fsList("");
      if (!alive) return;
      setRoot(r.entries || []);
      const segs = revealPath.split("/").filter(Boolean);
      const opened: string[] = [];
      let cur = "";
      for (const seg of segs) {
        cur = cur ? cur + "/" + seg : seg;
        const d = await fsList(cur);
        if (!alive) return;
        const entries = d.entries || [];
        setCache((c) => ({ ...c, [cur]: entries }));
        opened.push(cur);
      }
      if (!alive) return;
      setOpen((s) => {
        const n = new Set(s);
        opened.forEach((p) => n.add(p));
        return n;
      });
      setSelected(revealPath);
    })();
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reveal && reveal.n]);

  // Follow the active file: when the focused pane shows a file (context `filePath`), move
  // the tree cursor onto it — expanding its ancestor folders so it's visible; the
  // selected-scroll effect then brings it into view. Cached fetches keep pane switching
  // from spamming the server. Skips when the active pane isn't a file (filePath empty) or
  // the cursor is already there (e.g. the file was just opened from the tree).
  useEffect(() => {
    if (!filePath || filePath === selectedRef.current) return;
    let alive = true;
    (async () => {
      const segs = filePath.split("/").filter(Boolean);
      const toOpen: string[] = [];
      let cur = "";
      for (let i = 0; i < segs.length - 1; i++) {
        cur = cur ? cur + "/" + segs[i] : segs[i];
        const dir = cur; // capture per-iteration for the async setCache below
        if (!cacheRef.current[dir]) {
          const d = await fsList(dir);
          if (!alive) return;
          const entries = d.entries || [];
          setCache((c) => (c[dir] ? c : { ...c, [dir]: entries }));
        }
        toOpen.push(dir);
      }
      if (!alive) return;
      if (toOpen.length)
        setOpen((s) => {
          const n = new Set(s);
          toOpen.forEach((p) => n.add(p));
          return n;
        });
      setSelected(filePath);
    })();
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filePath]);

  // Flatten the open tree into the list of visible rows. Single-child directory
  // chains are compacted into one row: name = "a/b/c", path = deepest segment,
  // segPaths = every folded segment (so collapse closes the whole chain).
  //
  // Folding now applies even to CLOSED dirs — so the tree shows "a/b/c" without
  // having to expand it. That needs each visible dir's children to decide whether
  // it's a sole-child chain, so the walk records `need` (dir paths whose entries
  // aren't cached yet) and an effect prefetches them (one level each; chains
  // cascade). The descend into children still only happens when the row is open.
  const { rows, need } = useMemo(() => {
    const out: Row[] = [];
    const need: string[] = [];
    const walk = (entries: Entry[], parent: string, depth: number) => {
      for (const e of entries) {
        const path = parent ? parent + "/" + e.name : e.name;
        if (e.type !== "dir") {
          out.push({ path, name: e.name, type: "file", depth, segPaths: [path] });
          continue;
        }
        // Fold consecutive single-subdir directories into one segment (regardless of
        // open state), as far as we have their entries cached.
        let p = path;
        const segs = [e.name];
        const segPaths = [path];
        while (cache[p] && soleChildDir(cache[p])) {
          const child = cache[p][0];
          p = p + "/" + child.name;
          segs.push(child.name);
          segPaths.push(p);
        }
        // We don't yet know p's children — fetch them so we can decide/extend the fold.
        if (cache[p] === undefined) need.push(p);
        out.push({ path: p, name: segs.join("/"), type: "dir", depth, segPaths });
        if (open.has(p) && cache[p]) walk(cache[p], p, depth + 1);
      }
    };
    walk(root || [], "", 0);
    return { rows: out, need };
  }, [root, open, cache]);

  // Prefetch entries for visible dirs whose children we don't have yet, so closed
  // single-child chains fold. fetchInto is cached + guarded, so this converges.
  useEffect(() => {
    need.forEach((p) => fetchInto(p));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [need.join("\n")]);

  // fetchInto loads a directory's entries (cached) and returns them, so callers can
  // decide whether to keep descending through a single-child chain.
  const fetchInto = useCallback(
    async (path: string) => {
      if (cache[path]) return cache[path];
      const d = await fsList(path);
      const entries = d.entries || [];
      setCache((c) => (c[path] ? c : { ...c, [path]: entries }));
      return entries;
    },
    [cache],
  );

  // expand opens `path` and auto-descends through single-subdir directories (skipping
  // "empty" passthrough folders). Returns the deepest opened path so the caller can
  // keep the selection on the now-visible row.
  const expand = useCallback(
    async (path: string) => {
      const toOpen: string[] = [];
      let cur = path;
      for (let i = 0; i < 64; i++) {
        toOpen.push(cur);
        const entries = await fetchInto(cur);
        const child = soleChildDir(entries);
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
  const collapse = useCallback((row: Row) => {
    setOpen((s) => {
      const n = new Set(s);
      for (const p of row.segPaths || [row.path]) n.delete(p);
      return n;
    });
  }, []);

  const activate = useCallback(
    (row: Row) => {
      if (row.type === "file") {
        setSelected(row.path);
        showFile(row.path);
      } else if (open.has(row.path)) {
        setSelected(row.path);
        collapse(row);
      } else {
        expand(row.path).then((deep) => setSelected(deep));
      }
    },
    [open, collapse, expand, showFile],
  );

  // keep the selected row visible — but not for an auto-selection on load (skipScrollRef),
  // which would scroll the left nav to the top file on every WS Start / files refresh.
  useEffect(() => {
    if (skipScrollRef.current) {
      skipScrollRef.current = false;
      return;
    }
    selRef.current?.scrollIntoView({ block: "nearest" });
  }, [selected]);

  // Sticky scroll: recompute the pinned ancestor chain from the current scroll offset.
  // `over` is how far the tree's top has scrolled above the FILES bar's bottom edge; the
  // row sitting at that edge is rows[idx], whose ancestor folders we pin. The overlay is
  // absolutely placed `over` px down from the wrap top — i.e. just under the FILES bar,
  // not over it. (The FILES .pane-head is sticky at the scroller top; measuring from its
  // bottom, rather than the scroller top, keeps the folder sticky below the bar.)
  const MAX_STICKY = 5; // cap so the pinned stack never eats the (short) pane
  const recomputeSticky = useCallback(() => {
    const wrap = wrapRef.current;
    const tree = treeRef.current;
    if (!wrap || !tree) return;
    if (!scrollRef.current || !scrollRef.current.isConnected)
      scrollRef.current = tree.closest<HTMLElement>(".leftpane-scroll");
    const scroller = scrollRef.current;
    if (!scroller || !rows.length) {
      setSticky((s) => (s.rows.length ? { rows: [], top: 0 } : s));
      return;
    }
    // Anchor at the FILES bar's bottom (the sticky .pane-head) so the folder sticky sits
    // under it; fall back to the scroller top if the header can't be found.
    const head = wrap.closest(".pane-section")?.querySelector<HTMLElement>(".pane-head");
    const anchorTop = head ? head.getBoundingClientRect().bottom : scroller.getBoundingClientRect().top;
    const over = anchorTop - wrap.getBoundingClientRect().top;
    const first = tree.querySelector<HTMLElement>("li.fsrow");
    const rowH = (first && first.offsetHeight) || 22;
    if (over <= 1) {
      setSticky((s) => (s.rows.length ? { rows: [], top: 0 } : s));
      return;
    }
    const idx = Math.min(rows.length - 1, Math.floor(over / rowH));
    // Ancestor folders of rows[idx]: nearest preceding row at each shallower depth.
    const anc: Row[] = [];
    let need = rows[idx].depth - 1;
    for (let j = idx - 1; j >= 0 && need >= 0; j--) {
      if (rows[j].depth === need) {
        anc.unshift(rows[j]);
        need--;
      }
    }
    const capped = anc.length > MAX_STICKY ? anc.slice(anc.length - MAX_STICKY) : anc;
    setSticky({ rows: capped, top: over });
  }, [rows]);

  // Recompute on scroll (rAF-throttled), on resize, and whenever the visible rows change.
  useEffect(() => {
    if (view !== "tree") {
      setSticky({ rows: [], top: 0 });
      return;
    }
    const tree = treeRef.current;
    const scroller = tree?.closest<HTMLElement>(".leftpane-scroll");
    scrollRef.current = scroller || null;
    let raf = 0;
    const onScroll = () => {
      if (raf) return;
      raf = requestAnimationFrame(() => {
        raf = 0;
        recomputeSticky();
      });
    };
    scroller?.addEventListener("scroll", onScroll, { passive: true });
    window.addEventListener("resize", onScroll);
    recomputeSticky();
    return () => {
      if (raf) cancelAnimationFrame(raf);
      scroller?.removeEventListener("scroll", onScroll);
      window.removeEventListener("resize", onScroll);
    };
  }, [recomputeSticky, view]);

  // Click a pinned ancestor → select it and scroll the real row to the top.
  const jumpToRow = useCallback((path: string) => {
    setSelected(path);
    const tree = treeRef.current;
    const li = tree?.querySelector<HTMLElement>(`li[data-path="${CSS.escape(path)}"]`);
    li?.scrollIntoView({ block: "start" });
  }, []);

  const onKeyDown = (e: RKeyboardEvent) => {
    if (!rows.length) return;
    let idx = rows.findIndex((r) => r.path === selected);
    if (idx < 0) idx = 0;
    const cur = rows[idx];
    const select = (i: number) => setSelected(rows[Math.min(rows.length - 1, Math.max(0, i))].path);

    // Shift + ↑/↓ scrolls the viewer pane (right side) instead of moving selection.
    if (e.shiftKey && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
      e.preventDefault();
      const viewer = document.querySelector(".codeview") || document.querySelector(".md-scroll");
      if (viewer) viewer.scrollBy({ top: e.key === "ArrowDown" ? 60 : -60 });
      return;
    }
    // Ctrl/⌘ + ↑/↓ jumps to the previous / next folder (skips files).
    if ((e.ctrlKey || e.metaKey) && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
      e.preventDefault();
      const step = e.key === "ArrowDown" ? 1 : -1;
      for (let i = idx + step; i >= 0 && i < rows.length; i += step) {
        if (rows[i].type === "dir") {
          setSelected(rows[i].path);
          break;
        }
      }
      return;
    }

    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        select(idx + 1);
        break;
      case "ArrowUp":
        e.preventDefault();
        select(idx - 1);
        break;
      case "ArrowRight":
        e.preventDefault();
        if (cur.type === "dir") {
          if (!open.has(cur.path)) expand(cur.path).then((deep) => setSelected(deep));
          else select(idx + 1); // already open → step into first child
        }
        break;
      case "ArrowLeft":
        e.preventDefault();
        if (cur.type === "dir" && open.has(cur.path)) {
          collapse(cur);
        } else {
          const parent = dirName(cur.path);
          const pIdx = rows.findIndex(
            (r) => r.path === parent || (r.segPaths && r.segPaths.includes(parent)),
          );
          if (pIdx >= 0) setSelected(rows[pIdx].path);
        }
        break;
      case "Enter":
        e.preventDefault();
        activate(cur);
        break;
      default:
    }
  };

  const collapseAll = useCallback(() => {
    setOpen(new Set());
    // keep selection visible by snapping it back to its top-level ancestor
    setSelected((s: string | null) => (s ? s.split("/")[0] : s));
  }, []);
  const hasOpen = open.size > 0;
  const selRow = rows.find((r) => r.path === selected);
  const uploadDir = selRow ? (selRow.type === "dir" ? selRow.path : parentOf(selRow.path)) : "";
  // Context menu target dir: the row's dir (or its parent for a file), else root.
  const menuDir = menu && menu.row ? (menu.row.type === "dir" ? menu.row.path : parentOf(menu.row.path)) : "";

  const viewToggle = (
    <span className="seg sm files-view">
      <button
        type="button"
        className={"seg-btn" + (view === "tree" ? " active" : "")}
        title="ツリー"
        onClick={() => setViewPersist("tree")}
      >
        <Icon name="list-tree" />
        <span className="lbl">ツリー</span>
      </button>
      <button
        type="button"
        className={"seg-btn" + (view === "changes" ? " active" : "")}
        title="変更ファイルのみ"
        onClick={() => setViewPersist("changes")}
      >
        <Icon name="git-compare" />
        <span className="lbl">変更</span>
      </button>
    </span>
  );

  return (
    <Section
      id="files"
      title="Files"
      icon="files"
      actions={
        <>
          {view === "tree" && (
            <>
              <div className="launch-wrap files-ops" ref={opsRef}>
                <button
                  className="ghost"
                  title={running ? "新規 / アップロード" : "ワークスペース停止中"}
                  disabled={!running}
                  onClick={() => setOpsOpen((v) => !v)}
                >
                  <Icon name="add" />
                </button>
                {opsOpen && (
                  <div className="launch-menu" ref={opsMenuRef}>
                    {/* The target folder once, as a header, instead of repeating it
                        on every item. */}
                    <div className="menu-head" title={uploadDir || "home"}>
                      <Icon name="folder" />
                      <span className="menu-head-path">{uploadDir || "home"}</span>
                    </div>
                    <button onClick={() => { setOpsOpen(false); newFile(uploadDir); }}>
                      <Icon name="new-file" /> 新規ファイル
                    </button>
                    <button onClick={() => { setOpsOpen(false); newFolder(uploadDir); }}>
                      <Icon name="new-folder" /> 新規フォルダ
                    </button>
                    <button disabled={uploading} onClick={() => { setOpsOpen(false); fileInputRef.current?.click(); }}>
                      <Icon name="cloud-upload" spin={uploading} /> アップロード
                    </button>
                  </div>
                )}
              </div>
              <button className="ghost" title="すべて畳む" disabled={!hasOpen} onClick={collapseAll}>
                <Icon name="collapse-all" />
              </button>
            </>
          )}
          {viewToggle}
          <button className="ghost" title="更新" onClick={() => setReloadKey((k) => k + 1)}>
            <Icon name="refresh" />
          </button>
        </>
      }
    >
      <input
        ref={fileInputRef}
        type="file"
        multiple
        hidden
        onChange={(e) => {
          doUpload(uploadDir, e.target.files);
          e.target.value = "";
        }}
      />
      {view === "changes" ? (
        <ul className="fstree changeslist" role="list" aria-label="変更ファイル">
          {changes === null && <EmptyState loading message="読み込み中" />}
          {changes && changes.length === 0 && <EmptyState icon="check" message="変更はありません" />}
          {changes &&
            Object.entries(
              changes.reduce((acc: Record<string, FsChange[]>, c) => {
                (acc[c.repo] = acc[c.repo] || []).push(c);
                return acc;
              }, {} as Record<string, FsChange[]>),
            ).map(([repo, list]) => (
              <li key={repo} className="chg-group">
                <div className="chg-repo">{repo}</div>
                <ul>
                  {list.map((c) => {
                    const b = changeBadge(c);
                    const rel = c.path.slice(("repos/" + repo + "/").length);
                    const isSel = c.path === selected;
                    const isActive = filePath === c.path;
                    return (
                      <li
                        key={c.path}
                        className={"fsrow chg-row" + (isSel ? " selected" : "")}
                        title="中クリックで新ペインに開く"
                        onClick={() => {
                          setSelected(c.path);
                          showFile(c.path);
                        }}
                        {...onAuxOpen(c.path)}
                      >
                        <span className={"fs-file" + (isActive ? " active" : "")}>
                          <span className={"chg-badge " + b.cls}>{b.label}</span>
                          <span className="fs-ic"><FileIcon name={rel.split("/").pop() || ""} /></span>
                          {rel}
                        </span>
                      </li>
                    );
                  })}
                </ul>
              </li>
            ))}
        </ul>
      ) : (
        <div className="fstree-wrap" ref={wrapRef}>
        {sticky.rows.length > 0 && (
          <div className="fstree-sticky" style={{ top: sticky.top }} aria-hidden="true">
            {sticky.rows.map((r) => (
              <div
                key={r.path}
                className="fsrow fs-sticky-row"
                style={{ paddingLeft: 4 + r.depth * 14 }}
                title={r.path}
                onClick={() => jumpToRow(r.path)}
              >
                <span className="fs-dir">
                  <span className="fs-chev">▾</span>
                  <span className="fs-ic"><DirIcon open /></span>
                  {r.name}
                </span>
              </div>
            ))}
          </div>
        )}
        <ul
          className={"fstree" + (dropTarget === "" ? " drop-root" : "")}
          tabIndex={0}
          ref={treeRef}
          onKeyDown={onKeyDown}
          onDragOver={onDragOverTo("")}
          onDragLeave={() => setDropTarget(null)}
          onDrop={onDropTo("")}
          onContextMenu={(e) => e.target === e.currentTarget && openMenu(e, null)}
          role="tree"
          aria-label="ファイル"
        >
          {!running ? (
            <EmptyState icon="debug-disconnect" message="ワークスペース停止中" />
          ) : root === null ? (
            <EmptyState loading message="読み込み中" />
          ) : root.length === 0 ? (
            <EmptyState icon="new-file" message="ファイルがありません" hint="ここにドロップしてアップロード" />
          ) : null}
          {rows.map((r) => {
            const isOpen = r.type === "dir" && open.has(r.path);
            const isSel = r.path === selected;
            const isActiveFile = r.type === "file" && filePath === r.path;
            const isDir = r.type === "dir";
            return (
              <li
                key={r.path}
                data-path={r.path}
                ref={isSel ? selRef : null}
                className={
                  "fsrow" + (isSel ? " selected" : "") + (isDir && dropTarget === r.path ? " drop-hover" : "")
                }
                style={{ paddingLeft: 4 + r.depth * 14 }}
                title={isDir ? undefined : "Ctrl/中クリックで新ペインに開く"}
                onClick={(e) => {
                  treeRef.current?.focus();
                  // Ctrl/Cmd+click on a file mirrors the middle-click: open split.
                  if (!isDir && (e.ctrlKey || e.metaKey)) {
                    setSelected(r.path);
                    showFileSplit(r.path);
                    return;
                  }
                  activate(r);
                }}
                onContextMenu={(e) => openMenu(e, r)}
                onDragOver={isDir ? onDragOverTo(r.path) : undefined}
                onDrop={isDir ? onDropTo(r.path) : undefined}
                {...(isDir ? {} : onAuxOpen(r.path))}
              >
                <span className={isDir ? "fs-dir" : "fs-file" + (isActiveFile ? " active" : "")}>
                  <span className="fs-chev">{isDir ? (isOpen ? "▾" : "▸") : ""}</span>
                  <span className="fs-ic">{isDir ? <DirIcon open={isOpen} /> : <FileIcon name={r.name} />}</span>
                  {r.name}
                </span>
              </li>
            );
          })}
        </ul>
        </div>
      )}
      {menu && (
        <ul className="ctxmenu" ref={ctxRef} style={{ left: menu.x, top: menu.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
          <li onClick={() => runMenu(() => newFile(menuDir))}>
            <Icon name="new-file" /> 新規ファイル
          </li>
          <li onClick={() => runMenu(() => newFolder(menuDir))}>
            <Icon name="new-folder" /> 新規フォルダ
          </li>
          {menu.row && menuAssistable(menu.row) && assistants.length > 0 && (
            <li className={"ctx-sub" + (subOpen ? " open" : "")}>
              <span className="ctx-sub-head" onClick={(e) => { e.stopPropagation(); setSubOpen((o) => !o); }}>
                <span className="ctx-sub-label">
                  <Icon name="sparkle" /> アシスタントで開く
                </span>
                <span className="ctx-sub-arrow">{subOpen ? "▾" : "▸"}</span>
              </span>
              {subOpen && (
                <ul className="ctx-submenu">
                  {menu.row.type === "file" && translateAsst && (
                    <li onClick={() => runMenu(() => openWithAssistant(translateAsst, menu.row!, "translate"))}>翻訳</li>
                  )}
                  {menu.row.type === "file" && summarizeAsst && (
                    <li onClick={() => runMenu(() => openWithAssistant(summarizeAsst, menu.row!, "summarize"))}>要約</li>
                  )}
                  {menu.row.type === "file" && (translateAsst || summarizeAsst) && (
                    <li className="ctx-sep" aria-hidden="true" />
                  )}
                  {assistants.map((a) => (
                    <li key={a.id} onClick={() => runMenu(() => openWithAssistant(a, menu.row!, ""))}>
                      {a.name}
                    </li>
                  ))}
                </ul>
              )}
            </li>
          )}
          {menu.row && (
            <li onClick={() => runMenu(() => copyText(menu.row!.path.split("/").pop() || "", "名前"))}>
              <Icon name="copy" /> 名前をコピー
            </li>
          )}
          {menu.row && (
            <li onClick={() => runMenu(() => copyText(menu.row!.path, "相対パス"))}>
              <Icon name="copy" /> 相対パスをコピー
            </li>
          )}
          {menu.row && (
            <li
              onClick={() =>
                runMenu(() => copyText(browseRoot ? browseRoot + "/" + menu.row!.path : menu.row!.path, "絶対パス"))
              }
            >
              <Icon name="copy" /> 絶対パスをコピー
            </li>
          )}
          {menu.row && menu.row.type === "file" && (
            <li onClick={() => runMenu(() => setSendFile(menu.row!.path))}>
              <Icon name="send" /> セッションに送る…
            </li>
          )}
          {menu.row && menu.row.type === "file" && (
            <li>
              <a className="ctx-a" href={downloadURL(menu.row.path)} download onClick={() => setMenu(null)}>
                <Icon name="cloud-download" /> ダウンロード
              </a>
            </li>
          )}
          {menu.row && (
            <li onClick={() => runMenu(() => renameRow(menu.row!))}>
              <Icon name="edit" /> 名前を変更
            </li>
          )}
          {menu.row && (
            <li className="danger" onClick={() => runMenu(() => deleteRow(menu.row!))}>
              <Icon name="trash" /> 削除
            </li>
          )}
        </ul>
      )}
      {sendFile && <SendSelectionModal filePath={sendFile} onClose={() => setSendFile(null)} />}
    </Section>
  );
}
