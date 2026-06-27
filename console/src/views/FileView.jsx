import { useEffect, useState } from "react";
import { useApp } from "../state.jsx";
import { api } from "../api.js";

// FileView shows a single file's contents (read-only). Driven by the file the user
// clicked in the Files section. Binary / truncated files are reported, not dumped.
export default function FileView() {
  const { filePath } = useApp();
  const [data, setData] = useState(null);
  const [err, setErr] = useState("");

  useEffect(() => {
    if (!filePath) return;
    let alive = true;
    setData(null);
    setErr("");
    api(`api/fs/file?path=${encodeURIComponent(filePath)}`)
      .then((d) => {
        if (!alive) return;
        if (d && d.error) setErr(d.error.message || "読み込めません");
        else setData(d);
      })
      .catch(() => alive && setErr("読み込めません"));
    return () => {
      alive = false;
    };
  }, [filePath]);

  const body = err
    ? `(${err})`
    : data == null
      ? "…"
      : data.binary
        ? `(バイナリ, ${data.size || 0} bytes)`
        : (data.content ?? "");

  return (
    <div className="fileview">
      <header className="view-head">
        <span className="view-title mono">
          {filePath}
          {data?.truncated ? " (truncated)" : ""}
        </span>
      </header>
      <pre className="filebody">{body}</pre>
    </div>
  );
}
