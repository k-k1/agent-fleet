import { useEffect, useState } from "react";
import { useApp } from "../../state.jsx";
import { api } from "../../api.js";
import Section from "../Section.jsx";

const fsList = (path) =>
  api(`api/fs/tree?path=${encodeURIComponent(path)}`).catch(() => ({ entries: [] }));

// Files: a lazily-expanded, read-only tree of the workspace home (denylist applied
// on the Agent). Clicking a file shows its contents in the main area.
export default function FilesSection() {
  const { filePath, showFile } = useApp();
  const [root, setRoot] = useState(null);
  const [reloadKey, setReloadKey] = useState(0);

  useEffect(() => {
    let alive = true;
    fsList("").then((d) => alive && setRoot(d.entries || []));
    return () => {
      alive = false;
    };
  }, [reloadKey]);

  return (
    <Section
      title="Files"
      actions={
        <button className="ghost" title="更新" onClick={() => setReloadKey((k) => k + 1)}>
          ⟳
        </button>
      }
    >
      <ul className="fstree">
        {root === null && <li className="muted">…</li>}
        {root && root.length === 0 && <li className="muted">空</li>}
        {(root || []).map((e) => (
          <FsNode key={e.name} entry={e} parent="" onOpen={showFile} active={filePath} />
        ))}
      </ul>
    </Section>
  );
}

function FsNode({ entry, parent, onOpen, active }) {
  const full = parent ? parent + "/" + entry.name : entry.name;
  const [open, setOpen] = useState(false);
  const [children, setChildren] = useState(null);

  if (entry.type === "dir") {
    const toggle = async () => {
      const next = !open;
      setOpen(next);
      if (next && children === null) {
        const d = await fsList(full);
        setChildren(d.entries || []);
      }
    };
    return (
      <li>
        <span className="fs-dir" onClick={toggle}>
          {open ? "▾ " : "▸ "}
          {entry.name}
        </span>
        {open && (
          <ul>
            {children === null && <li className="muted">…</li>}
            {(children || []).map((c) => (
              <FsNode key={c.name} entry={c} parent={full} onOpen={onOpen} active={active} />
            ))}
          </ul>
        )}
      </li>
    );
  }
  return (
    <li>
      <span className={"fs-file" + (active === full ? " active" : "")} onClick={() => onOpen(full)}>
        {"   " + entry.name}
      </span>
    </li>
  );
}
