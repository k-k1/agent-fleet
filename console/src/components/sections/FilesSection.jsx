import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useApp } from "../../state.jsx";
import { api } from "../../api.js";
import { dirName } from "../../lib/filemeta.js";
import Section from "../Section.jsx";

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

  // Flatten the open tree into the list of visible rows.
  const rows = useMemo(() => {
    const out = [];
    const walk = (entries, parent, depth) => {
      for (const e of entries) {
        const path = parent ? parent + "/" + e.name : e.name;
        out.push({ path, name: e.name, type: e.type, depth });
        if (e.type === "dir" && open.has(path) && cache[path]) {
          walk(cache[path], path, depth + 1);
        }
      }
    };
    walk(root || [], "", 0);
    return out;
  }, [root, open, cache]);

  const loadChildren = useCallback(
    async (path) => {
      if (cache[path]) return;
      const d = await fsList(path);
      setCache((c) => ({ ...c, [path]: d.entries || [] }));
    },
    [cache],
  );

  const expand = useCallback(
    async (path) => {
      await loadChildren(path);
      setOpen((s) => new Set(s).add(path));
    },
    [loadChildren],
  );
  const collapse = useCallback((path) => {
    setOpen((s) => {
      const n = new Set(s);
      n.delete(path);
      return n;
    });
  }, []);

  const activate = useCallback(
    (row) => {
      setSelected(row.path);
      if (row.type === "file") showFile(row.path);
      else if (open.has(row.path)) collapse(row.path);
      else expand(row.path);
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
          if (!open.has(cur.path)) expand(cur.path);
          else select(idx + 1); // already open → step into first child
        }
        break;
      case "ArrowLeft":
        e.preventDefault();
        if (cur.type === "dir" && open.has(cur.path)) {
          collapse(cur.path);
        } else {
          const parent = dirName(cur.path);
          const pIdx = rows.findIndex((r) => r.path === parent);
          if (pIdx >= 0) setSelected(parent);
        }
        break;
      case "Enter":
        e.preventDefault();
        activate(cur);
        break;
      default:
    }
  };

  return (
    <Section
      title="Files"
      actions={
        <button className="ghost" title="更新" onClick={() => setReloadKey((k) => k + 1)}>
          ⟳
        </button>
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
                {r.type === "dir" ? (isOpen ? "▾ " : "▸ ") : "　"}
                {r.name}
              </span>
            </li>
          );
        })}
      </ul>
    </Section>
  );
}
