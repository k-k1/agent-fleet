// SubdirPicker — pick the folder INSIDE a working copy that a new session starts in
// (createReq.subdir → Meta.Subdir). Collapsed to a one-line summary by default, since
// almost every launch wants the repo root; "browse" expands a browser rooted at the repo
// (api/fs/tree, home-relative "repos/<repo>/…" like the rest of the Console) whose
// CURRENT path is the selection, mirroring DirPicker. The text field stays editable
// so a known path can be typed (or pasted) without walking the tree.
//
// For a worktree launch the tree shown is the PARENT copy's: the worktree does not
// exist yet. The layouts normally match; when they don't, the Agent rejects the
// missing path at create rather than silently landing somewhere else.
import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { normalizeSubdir } from "./subdirPath.ts";

interface Entry {
  name: string;
  type: string;
}

export function SubdirPicker({
  repo,
  value,
  onChange,
}: {
  repo: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const [entries, setEntries] = useState<Entry[]>([]);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState(false);
  const segs = value ? value.split("/") : [];
  const browsePath = ["repos", repo, ...segs].join("/");

  useEffect(() => {
    if (!open) return;
    let alive = true;
    setLoading(true);
    setErr(false);
    api(`api/fs/tree?path=${encodeURIComponent(browsePath)}`)
      .then((d) => {
        if (!alive) return;
        setEntries((d.entries || []).filter((e: Entry) => e.type === "dir"));
      })
      .catch(() => alive && setErr(true))
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, [open, browsePath]);

  return (
    <div className="subdirpick">
      <div className="subdirpick-row">
        <span className="subdirpick-root">{repo}/</span>
        <input
          className="subdirpick-input"
          value={value}
          onChange={(e) => onChange(normalizeSubdir(e.target.value))}
          placeholder={tr("launch.subdir_ph")}
        />
        <button type="button" className="ghost subdirpick-toggle" onClick={() => setOpen((o) => !o)}>
          <Icon name={open ? "chevron-down" : "folder"} /> {tr("launch.subdir_browse")}
        </button>
      </div>
      {open && (
        <div className="dirpick">
          <div className="dirpick-crumbs">
            <button type="button" className="dirpick-crumb" onClick={() => onChange("")}>
              <Icon name="repo" /> {repo}
            </button>
            {segs.map((s, i) => (
              <span key={i} className="dirpick-seg">
                <span className="dirpick-slash">/</span>
                <button type="button" className="dirpick-crumb" onClick={() => onChange(segs.slice(0, i + 1).join("/"))}>
                  {s}
                </button>
              </span>
            ))}
          </div>
          <div className="dirpick-list">
            {loading ? (
              <div className="dirpick-empty">
                <Icon name="loading" spin /> {tr("rp.loading")}
              </div>
            ) : err ? (
              <div className="dirpick-empty">{tr("rp.load_failed")}</div>
            ) : entries.length === 0 ? (
              <div className="dirpick-empty">{tr("rp.no_subfolders")}</div>
            ) : (
              entries.map((e) => (
                <button
                  key={e.name}
                  type="button"
                  className="dirpick-row"
                  onClick={() => onChange(value ? `${value}/${e.name}` : e.name)}
                >
                  <Icon name="folder" className="dirpick-ic" />
                  <span className="dirpick-name">{e.name}</span>
                  <Icon name="chevron-right" className="dirpick-go" />
                </button>
              ))
            )}
          </div>
          <div className="dirpick-cur">
            {tr("rp.launch_here")} <code>{value ? `${repo}/${value}` : repo}</code>
          </div>
        </div>
      )}
    </div>
  );
}
