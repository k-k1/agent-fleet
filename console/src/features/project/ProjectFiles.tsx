// ProjectFiles — the rail's file tree: a focused, self-contained lazy tree rooted
// at one folder (the FilesSection mounts it once at "repos", so the top level is
// the working copies themselves): expand/collapse (with single-child chain
// folding), open a file into a pane, drag-drop upload, and a right-click menu for
// the core file ops. It reuses the shared primitives (api fs endpoints,
// FileIcon/DirIcon, the .fstree/.fsrow classes); per-copy git changes live in the
// SCM pane, not here. Mounted only while its section is open, so the fetch is lazy.
import { Fragment, useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent, DragEvent as RDragEvent, KeyboardEvent as RKeyboardEvent } from "react";
import { api, uploadFiles, downloadURL, fsMkdir, fsNewFile, fsRename, fsDelete, fsSearch } from "../../core/api/client.ts";
import FileIcon, { DirIcon } from "../../ui/FileIcon.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useFilesStore } from "../files/store.ts";
import { useReposStore } from "../repos/store.ts";
import { useFilesFilter } from "./filesFilter.ts";
import { normQuery } from "./filter.ts";

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
}

export function ProjectFiles({ root, markRepos, searchable, groupByRepo }: ProjectFilesProps) {
  const repos = useReposStore((s) => s.repos);
  const layout = useLayoutStore((s) => s.layout);
  const openTarget = useLayoutStore((s) => s.openTarget);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const running = useWorkspaceStore((s) => s.state) === "running";
  const reveal = useFilesStore((s) => s.reveal);
  const filesTick = useFilesStore((s) => s.tick);
  const q = useFilesFilter((s) => s.q);
  const nq = normQuery(q);
  const focusInput = useFilesFilter((s) => s.focusInput);
  const focusTreeN = useFilesFilter((s) => s.focusTreeN);
  const askConfirm = useConfirm();
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
  const menuRef = useRef<HTMLUListElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const treeRef = useRef<HTMLUListElement>(null);
  const uid = useId();

  const showFile = useCallback((p: string) => openTarget({ content: { kind: "file", filePath: p } }), [openTarget]);
  const showFileSplit = useCallback((p: string) => openTargetInNew({ content: { kind: "file", filePath: p } }), [openTargetInNew]);

  // Load (mount / manual refresh via files tick): fetch the root folder's children
  // and re-fetch every currently-open dir, preserving expansion + selection.
  useEffect(() => {
    let alive = true;
    void (async () => {
      const r = await fsList(root);
      if (!alive) return;
      setEntries(r.entries || []);
      const opened = [...open];
      if (opened.length) {
        const pairs = await Promise.all(opened.map(async (p) => [p, (await fsList(p)).entries || []] as const));
        if (!alive) return;
        setCache((c) => {
          const n = { ...c };
          for (const [p, e] of pairs) n[p] = e;
          return n;
        });
      }
    })();
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [root, filesTick]);

  const fetchInto = useCallback(
    async (path: string) => {
      if (cache[path]) return cache[path];
      const d = await fsList(path);
      const e = d.entries || [];
      setCache((c) => (c[path] ? c : { ...c, [path]: e }));
      return e;
    },
    [cache],
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
  const searchMode = !!searchable && !!nq;
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
  // the (tree-)filtered rows.
  const displayRows = searchMode ? searchRows ?? [] : filteredRows;

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
      if ((e.ctrlKey || e.metaKey) && (e.key === "f" || e.key === "F")) {
        e.preventDefault();
        focusInput();
        return;
      }
      if (menu) return; // let the context menu own the keyboard while it's open
      const list = displayRows;
      if (!list.length) return;
      const idx = list.findIndex((r) => r.path === selected);
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
    [displayRows, selected, open, menu, expand, collapse, showFile, showFileSplit, focusInput, scrollRowIntoView],
  );

  // Enter in the filter box hands focus to the active searchable tree:
  // focus the tree and land the selection on the first visible row if it's astray.
  useEffect(() => {
    if (!focusTreeN || !searchable) return;
    treeRef.current?.focus();
    setSelected((cur) => {
      if (cur && displayRows.some((r) => r.path === cur)) return cur;
      const first = displayRows[0];
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
          title: "ファイルを上書き",
          body: `${(res.conflicts as string[]).join(", ")} は既に存在します。上書きしますか？`,
          confirmLabel: "上書きする",
          danger: true,
        });
        if (ok) res = await uploadFiles(dir, files, { overwrite: true });
      }
      if (res.error) toast("アップロード失敗: " + ((res.error as { message?: string }).message || res.error));
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
    const name = window.prompt(`新しいフォルダ名（作成先: ${baseName(parent)}）`, "");
    if (!name || !name.trim()) return;
    const p = joinPath(parent, name.trim());
    const res = await fsMkdir(p);
    if (res.error) return toast("作成失敗: " + ((res.error as { message?: string }).message || res.error));
    await refreshDir(parent);
    setSelected(p);
  };
  const newFile = async (parent: string) => {
    const name = window.prompt(`新しいファイル名（作成先: ${baseName(parent)}）`, "");
    if (!name || !name.trim()) return;
    const p = joinPath(parent, name.trim());
    const res = await fsNewFile(p);
    if (res.error) return toast("作成失敗: " + ((res.error as { message?: string }).message || res.error));
    await refreshDir(parent);
    setSelected(p);
    showFile(p);
  };
  const renameRow = async (row: Row) => {
    const base = baseName(row.path);
    const name = window.prompt("名前を変更", base);
    if (!name || !name.trim() || name.trim() === base) return;
    const parent = parentOf(row.path);
    const to = joinPath(parent, name.trim());
    const res = await fsRename(row.path, to);
    if (res.error) return toast("変更失敗: " + ((res.error as { message?: string }).message || res.error));
    await refreshDir(parent);
    setSelected(to);
  };
  const deleteRow = async (row: Row) => {
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
  };
  const copyText = (text: string, label: string) => {
    if (navigator.clipboard?.writeText)
      navigator.clipboard.writeText(text).then(
        () => toast(`${label}をコピーしました`, { kind: "success" }),
        () => toast("コピーに失敗しました"),
      );
    else toast("コピーに失敗しました");
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
    if (menu && menuRef.current) placeFixed(menuRef.current, menu.x, menu.y, menuRef.current.closest<HTMLElement>(".app-rail"));
  });
  const runMenu = (fn: () => void) => {
    setMenu(null);
    fn();
  };
  const menuDir = menu ? (menu.row.type === "dir" ? menu.row.path : parentOf(menu.row.path)) : root;

  return (
    <div className="proj-files">
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
        aria-label="ファイル"
        tabIndex={0}
        aria-activedescendant={(() => {
          const i = displayRows.findIndex((r) => r.path === selected);
          return i >= 0 ? `${uid}-${i}` : undefined;
        })()}
        onKeyDown={onTreeKeyDown}
        onMouseDown={() => treeRef.current?.focus({ preventScroll: true })}
        onDragOver={onDragOverTo(root)}
        onDragLeave={() => setDropTarget(null)}
        onDrop={onDropTo(root)}
      >
        {!running ? (
          <EmptyState icon="debug-disconnect" title="ワークスペース停止中" />
        ) : searchMode ? (
          searching && !searchRows ? (
            <EmptyState icon="loading" title="検索中…" />
          ) : displayRows.length === 0 ? (
            <li className="proj-sub-empty">「{q.trim()}」に一致するファイルはありません</li>
          ) : null
        ) : entries === null ? (
          <EmptyState icon="loading" title="読み込み中…" />
        ) : entries.length === 0 ? (
          <li className="proj-sub-empty">ファイルなし（ここにドロップでアップロード）</li>
        ) : nq && displayRows.length === 0 ? (
          <li className="proj-sub-empty">「{q.trim()}」に一致するファイルはありません</li>
        ) : null}
        {displayRows.map((r, i) => {
          const isOpen = r.type === "dir" && open.has(r.path);
          const isSel = r.path === selected;
          const isActiveFile = r.type === "file" && activeFile === r.path;
          const isDir = r.type === "dir";
          return (
            <Fragment key={r.path}>
            {searchMode && groupByRepo && (i === 0 || repoOf(displayRows[i - 1].path) !== repoOf(r.path)) && (
              <li className="files-search-group" role="presentation">
                <Icon name="root-folder" /> {repoOf(r.path)}
              </li>
            )}
            <li
              id={`${uid}-${i}`}
              data-path={r.path}
              role="treeitem"
              aria-selected={isSel}
              {...(isDir ? { "aria-expanded": isOpen } : {})}
              className={"fsrow" + (isSel ? " selected" : "") + (isDir && dropTarget === r.path ? " drop-hover" : "")}
              style={{ paddingLeft: 4 + r.depth * 14 }}
              title={isDir ? undefined : "Ctrl/中クリックで新ペインに開く"}
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
                  {(() => {
                    if (!isDir) return <FileIcon name={r.name} />;
                    // Working-copy folders (depth 0 under repos/) wear the same
                    // icon as their リポジトリ row; segPaths[0] survives chain
                    // folding (the row name may read "repo/sub").
                    if (markRepos && r.depth === 0) {
                      const wc = repos.find((x) => x.name === baseName(r.segPaths[0]));
                      if (wc) return <Icon name={wc.worktree ? "git-branch" : "root-folder"} className="fi-folder" />;
                    }
                    return <DirIcon open={isOpen} />;
                  })()}
                </span>
                <span className="fs-name">
                  {r.name}
                  {r.sub ? <span className="fs-sub"> {r.sub}</span> : null}
                </span>
              </span>
            </li>
            </Fragment>
          );
        })}
        {searchMode && searchTrunc && displayRows.length > 0 && (
          <li className="proj-sub-empty">上限に達しました。絞り込みを追加してください</li>
        )}
      </ul>
      {menu && (
        <ul className="ui-menu files-ctxmenu" ref={menuRef} style={{ left: menu.x, top: menu.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
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
          <li>
            <button type="button" className="ui-menu-item" onClick={() => runMenu(() => copyText(baseName(menu.row.path), "名前"))}>
              <Icon name="copy" /> 名前をコピー
            </button>
          </li>
          <li>
            <button type="button" className="ui-menu-item" onClick={() => runMenu(() => copyText(menu.row.path, "相対パス"))}>
              <Icon name="copy" /> 相対パスをコピー
            </button>
          </li>
          {menu.row.type === "file" && (
            <li>
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => runMenu(() => openTarget({ content: { kind: "read", filePath: menu.row.path } }))}
              >
                <Icon name="book" /> 朗読で開く
              </button>
            </li>
          )}
          {menu.row.type === "file" && (
            <li>
              <a className="ui-menu-item files-ctx-a" href={downloadURL(menu.row.path)} download onClick={() => setMenu(null)}>
                <Icon name="cloud-download" /> ダウンロード
              </a>
            </li>
          )}
          <li>
            <button type="button" className="ui-menu-item" onClick={() => runMenu(() => void renameRow(menu.row))}>
              <Icon name="edit" /> 名前を変更
            </button>
          </li>
          <li>
            <button type="button" className="ui-menu-item danger" onClick={() => runMenu(() => void deleteRow(menu.row))}>
              <Icon name="trash" /> 削除
            </button>
          </li>
        </ul>
      )}
    </div>
  );
}
