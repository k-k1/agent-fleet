// ProjectFiles — the rail's file tree: a focused, self-contained lazy tree rooted
// at one folder (the FilesSection mounts it once at "repos", so the top level is
// the working copies themselves): expand/collapse (with single-child chain
// folding), open a file into a pane, drag-drop upload, and a right-click menu for
// the core file ops. It reuses the shared primitives (api fs endpoints,
// FileIcon/DirIcon, the .fstree/.fsrow classes); per-copy git changes live in the
// SCM pane, not here. Mounted only while its section is open, so the fetch is lazy.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent, DragEvent as RDragEvent } from "react";
import { api, uploadFiles, downloadURL, fsMkdir, fsNewFile, fsRename, fsDelete } from "../../core/api/client.ts";
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
}

export function ProjectFiles({ root, markRepos }: ProjectFilesProps) {
  const repos = useReposStore((s) => s.repos);
  const layout = useLayoutStore((s) => s.layout);
  const openTarget = useLayoutStore((s) => s.openTarget);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const running = useWorkspaceStore((s) => s.state) === "running";
  const reveal = useFilesStore((s) => s.reveal);
  const filesTick = useFilesStore((s) => s.tick);
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
  const menuRef = useRef<HTMLUListElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

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
        className={"fstree proj-fstree" + (dropTarget === root ? " drop-root" : "")}
        role="tree"
        aria-label="ファイル"
        onDragOver={onDragOverTo(root)}
        onDragLeave={() => setDropTarget(null)}
        onDrop={onDropTo(root)}
      >
        {!running ? (
          <EmptyState icon="debug-disconnect" title="ワークスペース停止中" />
        ) : entries === null ? (
          <EmptyState icon="loading" title="読み込み中…" />
        ) : entries.length === 0 ? (
          <li className="proj-sub-empty">ファイルなし（ここにドロップでアップロード）</li>
        ) : null}
        {rows.map((r) => {
          const isOpen = r.type === "dir" && open.has(r.path);
          const isSel = r.path === selected;
          const isActiveFile = r.type === "file" && activeFile === r.path;
          const isDir = r.type === "dir";
          return (
            <li
              key={r.path}
              data-path={r.path}
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
                {r.name}
              </span>
            </li>
          );
        })}
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
