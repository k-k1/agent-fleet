import { useEffect, useState } from "react";
import { api } from "../api.js";
import Icon from "./Icon.jsx";

interface Entry {
  name: string;
  type: string;
}
interface RepoLite {
  name: string;
  branch?: string;
}

// DirPicker: browse the container's home tree and pick a directory to launch in.
// `value` is the home-relative path ("" = home); the CURRENT browsed path IS the
// selection (where you are is where it starts). Only directories are listed; click
// one to descend, a breadcrumb segment to jump back up. The server resolves a relative
// dir against home (see handleCreateSession), so the value maps 1:1 to fs/tree paths.
//
// `repos` (git working copies under ~/repos) are surfaced two ways: a quick list at
// home so a repo is reachable in one click, and a branch chip on any folder that is a
// known working copy — folding the old "既存から" repo picker into this one control.
export default function DirPicker({
  value,
  onChange,
  repos = [],
}: {
  value: string;
  onChange: (p: string) => void;
  repos?: RepoLite[];
}) {
  const [entries, setEntries] = useState<Entry[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState(false);
  const path = value; // home-relative; "" = home

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setErr(false);
    api(`api/fs/tree?path=${encodeURIComponent(path)}`)
      .then((d) => {
        if (!alive) return;
        setEntries((d.entries || []).filter((e: Entry) => e.type === "dir"));
      })
      .catch(() => alive && setErr(true))
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, [path]);

  const segs = path ? path.split("/") : [];
  const isHome = path === "";
  // Home-relative path → branch, for the git working copies (they live at ~/repos/<name>).
  const repoBranch = new Map(repos.map((r) => [`repos/${r.name}`, r.branch || ""]));
  const full = (name: string) => (path ? `${path}/${name}` : name);

  return (
    <div className="dirpick">
      <div className="dirpick-crumbs">
        <button type="button" className="dirpick-crumb" onClick={() => onChange("")} title="ホーム">
          <Icon name="home" /> ~
        </button>
        {segs.map((s, i) => (
          <span key={i} className="dirpick-seg">
            <span className="dirpick-slash">/</span>
            <button
              type="button"
              className="dirpick-crumb"
              onClick={() => onChange(segs.slice(0, i + 1).join("/"))}
            >
              {s}
            </button>
          </span>
        ))}
      </div>
      <div className="dirpick-list">
        {/* Quick repo list at home — one click into a working copy (was 「既存から」). */}
        {isHome && repos.length > 0 && (
          <>
            <div className="dirpick-head">リポジトリ</div>
            {repos.map((r) => (
              <button
                key={"repo-" + r.name}
                type="button"
                className="dirpick-row"
                onClick={() => onChange(`repos/${r.name}`)}
              >
                <Icon name="repo" className="dirpick-ic" />
                <span className="dirpick-name">{r.name}</span>
                {r.branch && <span className="dirpick-branch">{r.branch}</span>}
                <Icon name="chevron-right" className="dirpick-go" />
              </button>
            ))}
            <div className="dirpick-head">フォルダ</div>
          </>
        )}
        {loading ? (
          <div className="dirpick-empty muted">
            <Icon name="loading" spin /> 読み込み中…
          </div>
        ) : err ? (
          <div className="dirpick-empty muted">読み込めませんでした</div>
        ) : entries.length === 0 ? (
          <div className="dirpick-empty muted">サブフォルダはありません</div>
        ) : (
          entries.map((e) => {
            const branch = repoBranch.get(full(e.name)); // defined ⇒ this folder is a working copy
            return (
              <button
                key={e.name}
                type="button"
                className="dirpick-row"
                onClick={() => onChange(full(e.name))}
              >
                <Icon name={branch !== undefined ? "repo" : "folder"} className="dirpick-ic" />
                <span className="dirpick-name">{e.name}</span>
                {branch ? <span className="dirpick-branch">{branch}</span> : null}
                <Icon name="chevron-right" className="dirpick-go" />
              </button>
            );
          })
        )}
      </div>
      <div className="dirpick-cur">
        ここで起動: <code>~{path ? "/" + path : ""}</code>
      </div>
    </div>
  );
}
