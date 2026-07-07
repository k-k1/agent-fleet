// FilesSection — lazily-expanded tree of the workspace home (denylist applied
// Agent-side) + a git-changes view, with full file management (upload / mkdir /
// new file / rename / delete), keyboard navigation, single-child chain folding,
// sticky ancestor rows, and reveal-in-tree. Ported from the old console.
//
// Not yet ported: アシスタントで開く submenu (TODO(P5) — chat), セッションに送る
// (TODO(P6) — SendSelectionModal/memo queue).
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent, DragEvent as RDragEvent, KeyboardEvent as RKeyboardEvent } from "react";
import { api, uploadFiles, downloadURL, fsMkdir, fsNewFile, fsRename, fsDelete } from "../../core/api/client.ts";
import { dirName } from "../../lib/filemeta.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import FileIcon, { DirIcon } from "../../components/FileIcon.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useFilesStore } from "./store.ts";

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

// git status (porcelain XY) → a badge label + color class for the changes list.
function changeBadge(c: FsChange) {
  if (c.untracked) return { cls: "st-add", label: "未追跡" };
  const code = c.worktree !== " " && c.worktree !== "" ? c.worktree : c.index;
  if (code === "D") return { cls: "st-del", label: "削除" };
  if (code === "A") return { cls: "st-add", label: "追加" };
  if (code === "R" || code === "C") return { cls: "st-mod", label: "改名" };
  return { cls: "st-mod", label: "変更" };
}

// A directory is a "passthrough" link when its sole entry is one subdirectory —
// folded into one row (a/b/c) so deep single-child paths don't waste space.
const soleChildDir = (entries: Entry[] | undefined): Entry | null =>
  entries && entries.length === 1 && entries[0].type === "dir" ? entries[0] : null;

const fsList = (path: string) =>
  api(`api/fs/tree?path=${encodeURIComponent(path)}`).catch(() => ({ entries: [] }));

export function FilesSection() {
  const layout = useLayoutStore((s) => s.layout);
  const openTarget = useLayoutStore((s) => s.openTarget);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const running = useWorkspaceStore((s) => s.state) === "running";
  const reveal = useFilesStore((s) => s.reveal);
  const filesTick = useFilesStore((s) => s.tick);
  const askConfirm = useConfirm();
  const toast = useToast();

  // The active pane's file (content kind=file) — the tree follows it.
  const ac = activePane(layout)?.content;
  const filePath = ac && ac.kind === "file" ? ac.filePath : "";

  const showFile = useCallback(
    (path: string) => openTarget({ content: { kind: "file", filePath: path } }),
    [openTarget],
  );
  const showFileSplit = useCallback(
    (path: string) => openTargetInNew({ content: { kind: "file", filePath: path } }),
    [openTargetInNew],
  );

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

  const [root, setRoot] = useState<Entry[] | null>(null);
  const [open, setOpen] = useState<Set<string>>(() => new Set());
  const [cache, setCache] = useState<Record<string, Entry[]>>({});
  const [selected, setSelected] = useState<string | null>(null);
  const [browseRoot, setBrowseRoot] = useState(""); // absolute home, for 絶対パスをコピー
  const [reloadKey, setReloadKey] = useState(0);
  const [view, setView] = useState(() => localStorage.getItem("af-files-view") || "tree");
  const [changes, setChanges] = useState<FsChange[] | null>(null);
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const opsRef = useRef<HTMLDivElement>(null);
  const opsMenuRef = useRef<HTMLDivElement>(null);
  const [opsOpen, setOpsOpen] = useState(false);
  const [menu, setMenu] = useState<Menu | null>(null);
  const ctxRef = useRef<HTMLUListElement>(null);
  const treeRef = useRef<HTMLUListElement>(null);
  const selRef = useRef<HTMLLIElement>(null);
  // Sticky scroll: ancestor folders of the topmost visible row, pinned while scrolling.
  const wrapRef = useRef<HTMLDivElement>(null);
  const scrollRef = useRef<HTMLElement | null>(null);
  const [sticky, setSticky] = useState<{ rows: Row[]; top: number }>({ rows: [], top: 0 });
  // Auto-selection (first row on load) must not scroll the rail.
  const skipScrollRef = useRef(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const openRef = useRef(open);
  useEffect(() => {
    openRef.current = open;
  }, [open]);
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

  // Changes mode: aggregate git status across repos/.
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
  }, [view, reloadKey, filesTick]);

  // Replace one directory's cached entries (after an upload/create), expanded.
  const refreshDir = useCallback(async (dir: string) => {
    const d = await fsList(dir);
    const entries = d.entries || [];
    if (dir === "") setRoot(entries);
    else {
      setCache((c) => ({ ...c, [dir]: entries }));
      setOpen((s) => new Set(s).add(dir));
    }
  }, []);

  // Upload into `dir`. A 409 (name collision) confirms once, then overwrites.
  const doUpload = useCallback(
    async (dir: string, fileList: FileList | null) => {
      const files = Array.from(fileList || []).filter((f) => f && f.size >= 0 && f.name);
      if (!files.length) return;
      setUploading(true);
      try {
        let res = await uploadFiles(dir, files);
        if (res.status === 409 && Array.isArray(res.conflicts) && (res.conflicts as string[]).length) {
          const ok = await askConfirm({
            title: "ファイルを上書き",
            body: `${(res.conflicts as string[]).join(", ")} は既に存在します。上書きしますか？`,
            confirmLabel: "上書きする",
            danger: true,
          });
          if (ok) res = await uploadFiles(dir, files, { overwrite: true });
        }
        if (res.error) toast("アップロード失敗: " + ((res.error as { message?: string }).message || res.error));
        await refreshDir(dir);
        setSelected(dir || (files[0] ? files[0].name : null));
      } finally {
        setUploading(false);
        setDropTarget(null);
      }
    },
    [refreshDir, askConfirm, toast],
  );

  const onDropTo = useCallback(
    (dir: string) => (e: RDragEvent) => {
      e.preventDefault();
      e.stopPropagation();
      setDropTarget(null);
      if (!running) return;
      const files = e.dataTransfer?.files;
      if (files && files.length) void doUpload(dir, files);
    },
    [doUpload, running],
  );
  const onDragOverTo = useCallback(
    (dir: string) => (e: RDragEvent) => {
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
    },
    [running],
  );

  useDismiss(opsRef, opsOpen, () => setOpsOpen(false));

  // --- file operations ---
  const newFolder = useCallback(
    async (parent: string) => {
      const name = window.prompt(`新しいフォルダ名（作成先: ${parent || "home"}）`, "");
      if (!name || !name.trim()) return;
      const p = joinPath(parent, name.trim());
      const res = await fsMkdir(p);
      if (res.error) return toast("作成失敗: " + ((res.error as { message?: string }).message || res.error));
      await refreshDir(parent);
      setSelected(p);
    },
    [refreshDir, toast],
  );
  const newFile = useCallback(
    async (parent: string) => {
      const name = window.prompt(`新しいファイル名（作成先: ${parent || "home"}）`, "");
      if (!name || !name.trim()) return;
      const p = joinPath(parent, name.trim());
      const res = await fsNewFile(p);
      if (res.error) return toast("作成失敗: " + ((res.error as { message?: string }).message || res.error));
      await refreshDir(parent);
      setSelected(p);
      showFile(p);
    },
    [refreshDir, showFile, toast],
  );
  const renameRow = useCallback(
    async (row: Row) => {
      const base = row.path.split("/").pop();
      const name = window.prompt("名前を変更", base);
      if (!name || !name.trim() || name.trim() === base) return;
      const parent = parentOf(row.path);
      const to = joinPath(parent, name.trim());
      const res = await fsRename(row.path, to);
      if (res.error) return toast("変更失敗: " + ((res.error as { message?: string }).message || res.error));
      await refreshDir(parent);
      setSelected(to);
    },
    [refreshDir, toast],
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
      const res = await fsDelete(row.path);
      if (res.error) return toast("削除失敗: " + ((res.error as { message?: string }).message || res.error));
      await refreshDir(parentOf(row.path));
      setSelected(parentOf(row.path) || null);
    },
    [refreshDir, askConfirm, toast],
  );

  // Context menu: open at the cursor; close on outside click / Escape / blur.
  const openMenu = useCallback((e: RMouseEvent, row: Row | null) => {
    e.preventDefault();
    e.stopPropagation();
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
  const runMenu = (fn: () => void) => {
    setMenu(null);
    fn();
  };

  const copyText = useCallback(
    (text: string, label: string) => {
      if (navigator.clipboard?.writeText) {
        navigator.clipboard.writeText(text).then(
          () => toast(`${label}をコピーしました`, { kind: "success" }),
          () => toast("コピーに失敗しました"),
        );
      } else {
        toast("コピーに失敗しました");
      }
    },
    [toast],
  );

  useLayoutEffect(() => {
    if (menu && ctxRef.current)
      placeFixed(ctxRef.current, menu.x, menu.y, ctxRef.current.closest<HTMLElement>(".app-rail"));
  }, [menu]);
  // The ＋ dropdown: viewport-clamped fixed menu below its button.
  useLayoutEffect(() => {
    const el = opsMenuRef.current;
    const anchor = opsRef.current;
    if (!opsOpen || !el || !anchor) return;
    el.style.position = "fixed";
    el.style.right = "auto";
    const a = anchor.getBoundingClientRect();
    placeFixed(el, a.right - el.offsetWidth, a.bottom + 4);
  }, [opsOpen]);

  // Reload (manual ⟳ / files tick): refetch root + every open dir, PRESERVING
  // the expanded folders and selection.
  useEffect(() => {
    let alive = true;
    void (async () => {
      const r = await fsList("");
      if (!alive) return;
      const rootEntries = r.entries || [];
      setRoot(rootEntries);
      if (r.root) setBrowseRoot(r.root);
      const fresh: Record<string, Entry[]> = {};
      for (const p of Array.from(openRef.current)) {
        const d = await fsList(p);
        if (!alive) return;
        fresh[p] = d.entries || [];
      }
      if (!alive) return;
      setCache(fresh);
      setSelected((s) => {
        if (s) return s;
        skipScrollRef.current = true;
        return rootEntries[0] ? rootEntries[0].name : null;
      });
    })();
    return () => {
      alive = false;
    };
  }, [reloadKey, filesTick]);

  // Reveal: expand to a home-relative path and select it (repo click / clone).
  useEffect(() => {
    if (!reveal.path) return;
    const revealPath = reveal.path;
    let alive = true;
    void (async () => {
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
  }, [reveal.n]);

  // Follow the active file: when the focused pane shows a file, move the tree
  // cursor onto it, expanding ancestors.
  useEffect(() => {
    if (!filePath || filePath === selectedRef.current) return;
    let alive = true;
    void (async () => {
      const segs = filePath.split("/").filter(Boolean);
      const toOpen: string[] = [];
      let cur = "";
      for (let i = 0; i < segs.length - 1; i++) {
        cur = cur ? cur + "/" + segs[i] : segs[i];
        const dir = cur;
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

  // Flatten the open tree into visible rows; single-child chains fold into one
  // row (even closed — `need` prefetches unknown children to decide).
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
    walk(root || [], "", 0);
    return { rows: out, need };
  }, [root, open, cache]);

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

  // Prefetch entries for visible dirs whose children we don't have yet.
  useEffect(() => {
    need.forEach((p) => void fetchInto(p));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [need.join("\n")]);

  // expand opens `path`, auto-descending single-subdir chains.
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
        void expand(row.path).then((deep) => setSelected(deep));
      }
    },
    [open, collapse, expand, showFile],
  );

  // Keep the selected row visible (but not for load-time auto-selection).
  useEffect(() => {
    if (skipScrollRef.current) {
      skipScrollRef.current = false;
      return;
    }
    selRef.current?.scrollIntoView({ block: "nearest" });
  }, [selected]);

  // Sticky scroll: pin the ancestor chain of the row at the scroller's top edge.
  const MAX_STICKY = 5;
  const recomputeSticky = useCallback(() => {
    const wrap = wrapRef.current;
    const tree = treeRef.current;
    if (!wrap || !tree) return;
    if (!scrollRef.current || !scrollRef.current.isConnected)
      scrollRef.current = tree.closest<HTMLElement>(".app-rail-scroll");
    const scroller = scrollRef.current;
    if (!scroller || !rows.length) {
      setSticky((s) => (s.rows.length ? { rows: [], top: 0 } : s));
      return;
    }
    const anchorTop = scroller.getBoundingClientRect().top;
    const over = anchorTop - wrap.getBoundingClientRect().top;
    const first = tree.querySelector<HTMLElement>("li.fsrow");
    const rowH = (first && first.offsetHeight) || 22;
    if (over <= 1) {
      setSticky((s) => (s.rows.length ? { rows: [], top: 0 } : s));
      return;
    }
    const idx = Math.min(rows.length - 1, Math.floor(over / rowH));
    const anc: Row[] = [];
    let needDepth = rows[idx].depth - 1;
    for (let j = idx - 1; j >= 0 && needDepth >= 0; j--) {
      if (rows[j].depth === needDepth) {
        anc.unshift(rows[j]);
        needDepth--;
      }
    }
    const capped = anc.length > MAX_STICKY ? anc.slice(anc.length - MAX_STICKY) : anc;
    setSticky({ rows: capped, top: over });
  }, [rows]);

  useEffect(() => {
    if (view !== "tree") {
      setSticky({ rows: [], top: 0 });
      return;
    }
    const tree = treeRef.current;
    const scroller = tree?.closest<HTMLElement>(".app-rail-scroll");
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
          if (!open.has(cur.path)) void expand(cur.path).then((deep) => setSelected(deep));
          else select(idx + 1);
        }
        break;
      case "ArrowLeft":
        e.preventDefault();
        if (cur.type === "dir" && open.has(cur.path)) {
          collapse(cur);
        } else {
          const parent = dirName(cur.path);
          const pIdx = rows.findIndex((r) => r.path === parent || (r.segPaths && r.segPaths.includes(parent)));
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
    setSelected((s: string | null) => (s ? s.split("/")[0] : s));
  }, []);
  const hasOpen = open.size > 0;
  const selRow = rows.find((r) => r.path === selected);
  const uploadDir = selRow ? (selRow.type === "dir" ? selRow.path : parentOf(selRow.path)) : "";
  const menuDir = menu && menu.row ? (menu.row.type === "dir" ? menu.row.path : parentOf(menu.row.path)) : "";

  return (
    <Section
      id="files"
      title="Files"
      icon="files"
      actions={
        <>
          {view === "tree" && (
            <>
              <div className="files-ops" ref={opsRef}>
                <button
                  type="button"
                  className="ui-btn ui-btn-ghost ui-iconbtn"
                  title={running ? "新規 / アップロード" : "ワークスペース停止中"}
                  disabled={!running}
                  onClick={() => setOpsOpen((v) => !v)}
                >
                  <Icon name="add" />
                </button>
                {opsOpen && (
                  <div className="ui-menu" ref={opsMenuRef}>
                    <div className="files-menu-head" title={uploadDir || "home"}>
                      <Icon name="folder" />
                      <span className="files-menu-path">{uploadDir || "home"}</span>
                    </div>
                    <button type="button" className="ui-menu-item" onClick={() => { setOpsOpen(false); void newFile(uploadDir); }}>
                      <Icon name="new-file" /> 新規ファイル
                    </button>
                    <button type="button" className="ui-menu-item" onClick={() => { setOpsOpen(false); void newFolder(uploadDir); }}>
                      <Icon name="new-folder" /> 新規フォルダ
                    </button>
                    <button type="button" className="ui-menu-item" disabled={uploading} onClick={() => { setOpsOpen(false); fileInputRef.current?.click(); }}>
                      <Icon name="cloud-upload" spin={uploading} /> アップロード
                    </button>
                  </div>
                )}
              </div>
              <button
                type="button"
                className="ui-btn ui-btn-ghost ui-iconbtn"
                title="すべて畳む"
                disabled={!hasOpen}
                onClick={collapseAll}
              >
                <Icon name="collapse-all" />
              </button>
            </>
          )}
          <span className="ui-seg sm files-view">
            <button type="button" className={"seg-btn" + (view === "tree" ? " active" : "")} title="ツリー" onClick={() => setViewPersist("tree")}>
              <Icon name="list-tree" /> ツリー
            </button>
            <button type="button" className={"seg-btn" + (view === "changes" ? " active" : "")} title="変更ファイルのみ" onClick={() => setViewPersist("changes")}>
              <Icon name="git-compare" /> 変更
            </button>
          </span>
          <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn" title="更新" onClick={() => setReloadKey((k) => k + 1)}>
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
          void doUpload(uploadDir, e.target.files);
          e.target.value = "";
        }}
      />
      {view === "changes" ? (
        <ul className="fstree changeslist" role="list" aria-label="変更ファイル">
          {changes === null && <EmptyState icon="loading" title="読み込み中…" />}
          {changes && changes.length === 0 && <EmptyState icon="check" title="変更はありません" />}
          {changes &&
            Object.entries(
              changes.reduce((acc: Record<string, FsChange[]>, c) => {
                (acc[c.repo] = acc[c.repo] || []).push(c);
                return acc;
              }, {}),
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
                          <span className="fs-ic">
                            <FileIcon name={rel.split("/").pop() || ""} />
                          </span>
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
                    <span className="fs-ic">
                      <DirIcon open />
                    </span>
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
              <EmptyState icon="debug-disconnect" title="ワークスペース停止中" />
            ) : root === null ? (
              <EmptyState icon="loading" title="読み込み中…" />
            ) : root.length === 0 ? (
              <EmptyState icon="new-file" title="ファイルがありません" hint="ここにドロップしてアップロード" />
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
                  className={"fsrow" + (isSel ? " selected" : "") + (isDir && dropTarget === r.path ? " drop-hover" : "")}
                  style={{ paddingLeft: 4 + r.depth * 14 }}
                  title={isDir ? undefined : "Ctrl/中クリックで新ペインに開く"}
                  onClick={(e) => {
                    treeRef.current?.focus();
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
        <ul className="ui-menu files-ctxmenu" ref={ctxRef} style={{ left: menu.x, top: menu.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
          <li>
            <button type="button" className="ui-menu-item" onClick={() => runMenu(() => void newFile(menuDir))}>
              <Icon name="new-file" /> 新規ファイル
            </button>
          </li>
          <li>
            <button type="button" className="ui-menu-item" onClick={() => runMenu(() => void newFolder(menuDir))}>
              <Icon name="new-folder" /> 新規フォルダ
            </button>
          </li>
          {/* TODO(P5): アシスタントで開く submenu / TODO(P6): セッションに送る… */}
          {menu.row && (
            <>
              <li>
                <button type="button" className="ui-menu-item" onClick={() => runMenu(() => copyText(menu.row!.path.split("/").pop() || "", "名前"))}>
                  <Icon name="copy" /> 名前をコピー
                </button>
              </li>
              <li>
                <button type="button" className="ui-menu-item" onClick={() => runMenu(() => copyText(menu.row!.path, "相対パス"))}>
                  <Icon name="copy" /> 相対パスをコピー
                </button>
              </li>
              <li>
                <button
                  type="button"
                  className="ui-menu-item"
                  onClick={() => runMenu(() => copyText(browseRoot ? browseRoot + "/" + menu.row!.path : menu.row!.path, "絶対パス"))}
                >
                  <Icon name="copy" /> 絶対パスをコピー
                </button>
              </li>
              {menu.row.type === "file" && (
                <li>
                  <a className="ui-menu-item files-ctx-a" href={downloadURL(menu.row.path)} download onClick={() => setMenu(null)}>
                    <Icon name="cloud-download" /> ダウンロード
                  </a>
                </li>
              )}
              <li>
                <button type="button" className="ui-menu-item" onClick={() => runMenu(() => void renameRow(menu.row!))}>
                  <Icon name="edit" /> 名前を変更
                </button>
              </li>
              <li>
                <button type="button" className="ui-menu-item danger" onClick={() => runMenu(() => void deleteRow(menu.row!))}>
                  <Icon name="trash" /> 削除
                </button>
              </li>
            </>
          )}
        </ul>
      )}
    </Section>
  );
}
