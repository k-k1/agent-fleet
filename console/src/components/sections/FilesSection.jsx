import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useApp } from "../../state.jsx";
import { api, uploadFiles, downloadURL, fsMkdir, fsNewFile, fsRename, fsDelete } from "../../api.js";
import { dirName } from "../../lib/filemeta.js";
import Section from "../Section.jsx";
import Icon from "../Icon.jsx";
import FileIcon, { DirIcon } from "../FileIcon.jsx";

const joinPath = (d, n) => (d ? d + "/" + n : n);
const parentOf = (p) => {
  const i = p.lastIndexOf("/");
  return i < 0 ? "" : p.slice(0, i);
};

// git status (porcelain XY) -> a one-char badge + color class for the changes list.
function changeBadge(c) {
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
const soleChildDir = (entries) =>
  entries && entries.length === 1 && entries[0].type === "dir" ? entries[0] : null;

const fsList = (path) =>
  api(`api/fs/tree?path=${encodeURIComponent(path)}`).catch(() => ({ entries: [] }));

// Files: a lazily-expanded, read-only tree of the workspace home (denylist applied
// on the Agent). The tree is centrally managed (flattened visible rows + an open
// set) so it's keyboard-navigable: ↑/↓ move, → expand / into child, ← collapse /
// to parent, Enter opens a file (or toggles a folder).
export default function FilesSection() {
  const { filePath, showFile, showFileSplit, filesKey, reveal, wsState } = useApp();
  const running = wsState === "running"; // WS down → file mutations are inert

  // Middle-click opens a file in a freshly split pane (like the Sessions list).
  // Suppress the mousedown default so the browser doesn't start autoscroll.
  const onAuxOpen = (path) => ({
    onMouseDown: (e) => e.button === 1 && e.preventDefault(),
    onAuxClick: (e) => {
      if (e.button === 1) {
        e.preventDefault();
        setSelected(path);
        showFileSplit(path);
      }
    },
  });
  const [root, setRoot] = useState(null);
  const [open, setOpen] = useState(() => new Set()); // expanded dir paths
  const [cache, setCache] = useState({}); // dir path -> entries
  const [selected, setSelected] = useState(null); // path
  const [reloadKey, setReloadKey] = useState(0);
  const [view, setView] = useState(() => localStorage.getItem("af-files-view") || "tree"); // tree | changes
  const [changes, setChanges] = useState(null); // changes-mode: aggregated git status
  const [dropTarget, setDropTarget] = useState(null); // dir path being hovered with a drag ("" = root)
  const [uploading, setUploading] = useState(false);
  const [opsOpen, setOpsOpen] = useState(false); // ＋ (new / upload) header dropdown
  const [menu, setMenu] = useState(null); // context menu: { x, y, row|null }
  const treeRef = useRef(null);
  const selRef = useRef(null);
  const fileInputRef = useRef(null);
  // Mirror `open` into a ref so the reload effect can refetch the expanded dirs
  // without taking `open` as a dependency (which would refetch on every toggle).
  const openRef = useRef(open);
  useEffect(() => {
    openRef.current = open;
  }, [open]);

  const setViewPersist = useCallback((v) => {
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
  const refreshDir = useCallback(async (dir) => {
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
    async (dir, fileList) => {
      const files = Array.from(fileList || []).filter((f) => f && f.size >= 0 && f.name);
      if (!files.length) return;
      setUploading(true);
      try {
        let res = await uploadFiles(dir, files);
        if (res.status === 409 && res.conflicts && res.conflicts.length) {
          if (window.confirm(`${res.conflicts.join(", ")} は既に存在します。上書きしますか？`)) {
            res = await uploadFiles(dir, files, { overwrite: true });
          }
        }
        if (res.error) window.alert("アップロード失敗: " + (res.error.message || res.error));
        await refreshDir(dir);
        setSelected(dir || (files[0] ? files[0].name : null));
      } finally {
        setUploading(false);
        setDropTarget(null);
      }
    },
    [refreshDir],
  );

  // Drop onto a dir row uploads into it; onto the empty tree uploads into root.
  const onDropTo = useCallback(
    (dir) => (e) => {
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
    (dir) => (e) => {
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

  // Close the ＋ header menu on any outside click (the wrap stops its own clicks).
  useEffect(() => {
    if (!opsOpen) return;
    const close = () => setOpsOpen(false);
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [opsOpen]);

  // --- file operations (new / rename / delete) ---
  const newFolder = useCallback(
    async (parent) => {
      const name = window.prompt(`新しいフォルダ名（作成先: ${parent || "home"}）`, "");
      if (!name || !name.trim()) return;
      const p = joinPath(parent, name.trim());
      const res = await fsMkdir(p);
      if (res.error) return window.alert("作成失敗: " + (res.error.message || res.error));
      await refreshDir(parent);
      setSelected(p);
    },
    [refreshDir],
  );
  const newFile = useCallback(
    async (parent) => {
      const name = window.prompt(`新しいファイル名（作成先: ${parent || "home"}）`, "");
      if (!name || !name.trim()) return;
      const p = joinPath(parent, name.trim());
      const res = await fsNewFile(p);
      if (res.error) return window.alert("作成失敗: " + (res.error.message || res.error));
      await refreshDir(parent);
      setSelected(p);
      showFile(p);
    },
    [refreshDir, showFile],
  );
  const renameRow = useCallback(
    async (row) => {
      const base = row.path.split("/").pop();
      const name = window.prompt("名前を変更", base);
      if (!name || !name.trim() || name.trim() === base) return;
      const parent = parentOf(row.path);
      const to = joinPath(parent, name.trim());
      const res = await fsRename(row.path, to);
      if (res.error) return window.alert("変更失敗: " + (res.error.message || res.error));
      await refreshDir(parent);
      setSelected(to);
    },
    [refreshDir],
  );
  const deleteRow = useCallback(
    async (row) => {
      if (!window.confirm(`${row.path} を削除しますか？${row.type === "dir" ? "（中身ごと）" : ""}`)) return;
      const res = await fsDelete(row.path);
      if (res.error) return window.alert("削除失敗: " + (res.error.message || res.error));
      await refreshDir(parentOf(row.path));
      setSelected(parentOf(row.path) || null);
    },
    [refreshDir],
  );

  // Context menu: open at the cursor; close on outside click / Escape / scroll.
  const openMenu = useCallback((e, row) => {
    e.preventDefault();
    e.stopPropagation();
    setMenu({ x: e.clientX, y: e.clientY, row });
  }, []);
  useEffect(() => {
    if (!menu) return;
    const close = () => setMenu(null);
    const onKey = (e) => e.key === "Escape" && close();
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
  const runMenu = (fn) => {
    setMenu(null);
    fn();
  };

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
      // Refetch each open dir so the refresh reflects server state while keeping
      // it expanded. A dir that vanished returns no entries and just won't render.
      const fresh = {};
      for (const p of Array.from(openRef.current)) {
        const d = await fsList(p);
        if (!alive) return;
        fresh[p] = d.entries || [];
      }
      if (!alive) return;
      setCache(fresh);
      setSelected((s) => s || (rootEntries[0] ? rootEntries[0].name : null));
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
    let alive = true;
    (async () => {
      const r = await fsList("");
      if (!alive) return;
      setRoot(r.entries || []);
      const segs = reveal.path.split("/").filter(Boolean);
      const opened = [];
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
      setSelected(reveal.path);
    })();
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reveal && reveal.n]);

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
    const out = [];
    const need = [];
    const walk = (entries, parent, depth) => {
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
    async (path) => {
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
    async (path) => {
      const toOpen = [];
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
  const collapse = useCallback((row) => {
    setOpen((s) => {
      const n = new Set(s);
      for (const p of row.segPaths || [row.path]) n.delete(p);
      return n;
    });
  }, []);

  const activate = useCallback(
    (row) => {
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

  // keep the selected row visible
  useEffect(() => {
    selRef.current?.scrollIntoView({ block: "nearest" });
  }, [selected]);

  const onKeyDown = (e) => {
    if (!rows.length) return;
    let idx = rows.findIndex((r) => r.path === selected);
    if (idx < 0) idx = 0;
    const cur = rows[idx];
    const select = (i) => setSelected(rows[Math.min(rows.length - 1, Math.max(0, i))].path);

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
    setSelected((s) => (s ? s.split("/")[0] : s));
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
      </button>
      <button
        type="button"
        className={"seg-btn" + (view === "changes" ? " active" : "")}
        title="変更ファイルのみ"
        onClick={() => setViewPersist("changes")}
      >
        <Icon name="git-compare" />
      </button>
    </span>
  );

  return (
    <Section
      title="Files"
      icon="files"
      actions={
        <>
          {view === "tree" && (
            <>
              <div className="launch-wrap files-ops" onMouseDown={(e) => e.stopPropagation()}>
                <button
                  className="ghost"
                  title={running ? "新規 / アップロード" : "ワークスペース停止中"}
                  disabled={!running}
                  onClick={() => setOpsOpen((v) => !v)}
                >
                  <Icon name="add" />
                </button>
                {opsOpen && (
                  <div className="launch-menu">
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
          {changes === null && <li className="muted">…</li>}
          {changes && changes.length === 0 && <li className="muted">変更なし</li>}
          {changes &&
            Object.entries(
              changes.reduce((acc, c) => {
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
                          <span className={"chg-badge " + b.cls} title={b.label}>{b.ch}</span>
                          <span className="fs-ic"><FileIcon name={rel.split("/").pop()} /></span>
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
            <li className="muted">ワークスペース停止中</li>
          ) : root === null ? (
            <li className="muted">…</li>
          ) : root.length === 0 ? (
            <li className="muted">空（ここにファイルをドロップ）</li>
          ) : null}
          {rows.map((r) => {
            const isOpen = r.type === "dir" && open.has(r.path);
            const isSel = r.path === selected;
            const isActiveFile = r.type === "file" && filePath === r.path;
            const isDir = r.type === "dir";
            return (
              <li
                key={r.path}
                ref={isSel ? selRef : null}
                className={
                  "fsrow" + (isSel ? " selected" : "") + (isDir && dropTarget === r.path ? " drop-hover" : "")
                }
                style={{ paddingLeft: 4 + r.depth * 14 }}
                title={isDir ? undefined : "中クリックで新ペインに開く"}
                onClick={() => {
                  treeRef.current?.focus();
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
      )}
      {menu && (
        <ul className="ctxmenu" style={{ left: menu.x, top: menu.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
          <li onClick={() => runMenu(() => newFile(menuDir))}>新規ファイル</li>
          <li onClick={() => runMenu(() => newFolder(menuDir))}>新規フォルダ</li>
          {menu.row && menu.row.type === "file" && (
            <li>
              <a className="ctx-a" href={downloadURL(menu.row.path)} download onClick={() => setMenu(null)}>
                ダウンロード
              </a>
            </li>
          )}
          {menu.row && <li onClick={() => runMenu(() => renameRow(menu.row))}>名前を変更</li>}
          {menu.row && (
            <li className="danger" onClick={() => runMenu(() => deleteRow(menu.row))}>
              削除
            </li>
          )}
        </ul>
      )}
    </Section>
  );
}
