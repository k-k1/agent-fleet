import { useEffect, useState } from "react";
import { api } from "../../../core/api/client.ts";
import { Modal } from "../../../ui/Modal.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// InternalRepoBrowser: a clone-free, read-only view of an internal repo served by
// the CP (api/internal-git/repos/{name}/tree|blob). Pick a branch, walk the tree via
// breadcrumbs, and preview a text file; binary / too-large / LFS blobs show a note
// instead of bytes. No write surface.
interface Entry {
  name: string;
  type: "tree" | "blob";
  size: number;
}
interface Blob {
  path: string;
  size: number;
  content?: string;
  binary?: boolean;
  too_large?: boolean;
  lfs?: boolean;
  lfs_oid?: string;
}

export function InternalRepoBrowser({ name, onClose }: { name: string; onClose: () => void }) {
  const tr = useT();
  const [branches, setBranches] = useState<string[]>([]);
  const [ref, setRef] = useState("");
  const [path, setPath] = useState("");
  const [entries, setEntries] = useState<Entry[] | null>(null);
  const [treeErr, setTreeErr] = useState("");
  const [file, setFile] = useState<string | null>(null);
  const [blob, setBlob] = useState<Blob | null>(null);

  const base = `api/internal-git/repos/${encodeURIComponent(name)}`;

  // Load branches once; default the ref to the repo's default branch.
  useEffect(() => {
    api(`${base}/branches`)
      .then((d) => {
        if (d && !d.error) {
          setBranches(d.branches || []);
          setRef(d.default_branch || (d.branches && d.branches[0]) || "");
        }
      })
      .catch(() => {});
  }, [name]); // eslint-disable-line react-hooks/exhaustive-deps

  // Load the tree whenever ref or path changes.
  useEffect(() => {
    if (!ref) return;
    let alive = true;
    setEntries(null);
    setTreeErr("");
    api(`${base}/tree?ref=${encodeURIComponent(ref)}&path=${encodeURIComponent(path)}`)
      .then((d) => {
        if (!alive) return;
        if (d && d.error) {
          setTreeErr(d.error.message || tr("igb.fetch_failed"));
          setEntries([]);
        } else {
          setEntries(d.entries || []);
        }
      })
      .catch(() => alive && (setTreeErr(tr("igb.fetch_failed")), setEntries([])));
    return () => {
      alive = false;
    };
  }, [ref, path]); // eslint-disable-line react-hooks/exhaustive-deps

  // Load a file's blob when one is selected.
  useEffect(() => {
    if (!file) {
      setBlob(null);
      return;
    }
    let alive = true;
    setBlob(null);
    api(`${base}/blob?ref=${encodeURIComponent(ref)}&path=${encodeURIComponent(file)}`)
      .then((d) => alive && setBlob(d && !d.error ? d : { path: file, size: 0 }))
      .catch(() => alive && setBlob({ path: file, size: 0 }));
    return () => {
      alive = false;
    };
  }, [file]); // eslint-disable-line react-hooks/exhaustive-deps

  const openEntry = (e: Entry) => {
    const full = path ? `${path}/${e.name}` : e.name;
    if (e.type === "tree") {
      setFile(null);
      setPath(full);
    } else {
      setFile(full);
    }
  };

  const goToCrumb = (i: number) => {
    setFile(null);
    setPath(i < 0 ? "" : segs.slice(0, i + 1).join("/"));
  };

  const segs = path ? path.split("/") : [];

  return (
    <Modal title={tr("igb.title", { name })} onClose={onClose} className="ig-browser">
      <div className="ui-modal-body ig-body">
        <div className="ig-toolbar">
          <select value={ref} onChange={(e) => (setFile(null), setPath(""), setRef(e.target.value))}>
            {branches.length === 0 && <option value={ref}>{ref || tr("igb.no_branch")}</option>}
            {branches.map((b) => (
              <option key={b} value={b}>
                {b}
              </option>
            ))}
          </select>
          <nav className="ig-crumbs">
            <button type="button" className="ig-crumb" onClick={() => goToCrumb(-1)}>
              {name}
            </button>
            {segs.map((s, i) => (
              <span key={i}>
                <span className="ig-sep">/</span>
                <button type="button" className="ig-crumb" onClick={() => goToCrumb(i)}>
                  {s}
                </button>
              </span>
            ))}
          </nav>
        </div>

        <div className="ig-panes">
          <div className="ig-tree">
            {entries === null ? (
              <p className="muted pad">{tr("common.loading")}</p>
            ) : treeErr ? (
              <p className="muted pad">{treeErr}</p>
            ) : entries.length === 0 ? (
              <p className="muted pad">{tr("igb.empty")}</p>
            ) : (
              <ul className="ig-entries">
                {entries.map((e) => (
                  <li key={e.name}>
                    <button
                      type="button"
                      className={"ig-entry" + (file === (path ? `${path}/${e.name}` : e.name) ? " active" : "")}
                      onClick={() => openEntry(e)}
                    >
                      <span className="ig-icon">{e.type === "tree" ? "📁" : "📄"}</span>
                      <span className="ig-name">{e.name}</span>
                      {e.type === "blob" && <span className="ig-size">{fmtSize(e.size)}</span>}
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div className="ig-view">
            {!file ? (
              <p className="muted pad">{tr("igb.select_file")}</p>
            ) : blob === null ? (
              <p className="muted pad">{tr("common.loading")}</p>
            ) : blob.too_large ? (
              <p className="muted pad">{tr("igb.too_large", { size: fmtSize(blob.size) })}</p>
            ) : blob.lfs ? (
              <p className="muted pad">{tr("igb.lfs", { oid: blob.lfs_oid ? `（sha256:${blob.lfs_oid.slice(0, 12)}…）` : "" })}</p>
            ) : blob.binary ? (
              <p className="muted pad">{tr("igb.binary", { size: fmtSize(blob.size) })}</p>
            ) : (
              <pre className="ig-blob">
                <code>{blob.content}</code>
              </pre>
            )}
          </div>
        </div>
      </div>
    </Modal>
  );
}

function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}
