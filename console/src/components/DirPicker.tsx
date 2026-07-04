import { useEffect, useState } from "react";
import { api } from "../api.js";
import Icon from "./Icon.jsx";

interface Entry {
  name: string;
  type: string;
}

// DirPicker: browse the container's home tree and pick a directory to launch in.
// `value` is the home-relative path ("" = home); the CURRENT browsed path IS the
// selection (there's no separate "select" — where you are is where it starts). Only
// directories are listed; click one to descend, a breadcrumb segment to jump back up.
// The server resolves a relative dir against home (see handleCreateSession), so the
// value maps 1:1 to the fs/tree paths shown here.
export default function DirPicker({
  value,
  onChange,
}: {
  value: string;
  onChange: (p: string) => void;
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
        {loading ? (
          <div className="dirpick-empty muted">
            <Icon name="loading" spin /> 読み込み中…
          </div>
        ) : err ? (
          <div className="dirpick-empty muted">読み込めませんでした</div>
        ) : entries.length === 0 ? (
          <div className="dirpick-empty muted">サブフォルダはありません</div>
        ) : (
          entries.map((e) => (
            <button
              key={e.name}
              type="button"
              className="dirpick-row"
              onClick={() => onChange(path ? `${path}/${e.name}` : e.name)}
            >
              <Icon name="folder" className="dirpick-ic" />
              <span className="dirpick-name">{e.name}</span>
              <Icon name="chevron-right" className="dirpick-go" />
            </button>
          ))
        )}
      </div>
      <div className="dirpick-cur">
        ここで起動: <code>~{path ? "/" + path : ""}</code>
      </div>
    </div>
  );
}
