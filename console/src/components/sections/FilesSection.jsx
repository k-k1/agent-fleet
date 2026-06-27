import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useApp } from "../../state.jsx";
import { api } from "../../api.js";
import { dirName } from "../../lib/filemeta.js";
import { fileIcon, dirIcon } from "../../lib/fileicons.js";
import Section from "../Section.jsx";

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
  const { filePath, showFile } = useApp();
  const [root, setRoot] = useState(null);
  const [open, setOpen] = useState(() => new Set()); // expanded dir paths
  const [cache, setCache] = useState({}); // dir path -> entries
  const [selected, setSelected] = useState(null); // path
  const [reloadKey, setReloadKey] = useState(0);
  const treeRef = useRef(null);
  const selRef = useRef(null);

  useEffect(() => {
    let alive = true;
    setOpen(new Set());
    setCache({});
    fsList("").then((d) => {
      if (!alive) return;
      const entries = d.entries || [];
      setRoot(entries);
      setSelected(entries[0] ? entries[0].name : null);
    });
    return () => {
      alive = false;
    };
  }, [reloadKey]);

  // Flatten the open tree into the list of visible rows. Single-child directory
  // chains are compacted into one row: name = "a/b/c", path = deepest segment,
  // segPaths = every folded segment (so collapse closes the whole chain).
  const rows = useMemo(() => {
    const out = [];
    const walk = (entries, parent, depth) => {
      for (const e of entries) {
        const path = parent ? parent + "/" + e.name : e.name;
        if (e.type !== "dir") {
          out.push({ path, name: e.name, type: "file", depth, segPaths: [path] });
          continue;
        }
        // Fold consecutive open single-subdir directories into one segment.
        let p = path;
        const segs = [e.name];
        const segPaths = [path];
        while (open.has(p) && soleChildDir(cache[p])) {
          const child = cache[p][0];
          p = p + "/" + child.name;
          segs.push(child.name);
          segPaths.push(p);
        }
        out.push({ path: p, name: segs.join("/"), type: "dir", depth, segPaths });
        if (open.has(p) && cache[p]) walk(cache[p], p, depth + 1);
      }
    };
    walk(root || [], "", 0);
    return out;
  }, [root, open, cache]);

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

  return (
    <Section
      title="Files"
      actions={
        <>
          <button
            className="ghost"
            title="すべて畳む"
            disabled={!hasOpen}
            onClick={collapseAll}
          >
            ⊟
          </button>
          <button className="ghost" title="更新" onClick={() => setReloadKey((k) => k + 1)}>
            ⟳
          </button>
        </>
      }
    >
      <ul
        className="fstree"
        tabIndex={0}
        ref={treeRef}
        onKeyDown={onKeyDown}
        role="tree"
        aria-label="ファイル"
      >
        {root === null && <li className="muted">…</li>}
        {root && root.length === 0 && <li className="muted">空</li>}
        {rows.map((r) => {
          const isOpen = r.type === "dir" && open.has(r.path);
          const isSel = r.path === selected;
          const isActiveFile = r.type === "file" && filePath === r.path;
          return (
            <li
              key={r.path}
              ref={isSel ? selRef : null}
              className={"fsrow" + (isSel ? " selected" : "")}
              style={{ paddingLeft: 4 + r.depth * 14 }}
              onClick={() => {
                treeRef.current?.focus();
                activate(r);
              }}
            >
              <span className={r.type === "dir" ? "fs-dir" : "fs-file" + (isActiveFile ? " active" : "")}>
                <span className="fs-chev">{r.type === "dir" ? (isOpen ? "▾" : "▸") : ""}</span>
                <span className="fs-ic">{r.type === "dir" ? dirIcon(isOpen) : fileIcon(r.name)}</span>
                {r.name}
              </span>
            </li>
          );
        })}
      </ul>
    </Section>
  );
}
