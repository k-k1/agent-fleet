import { useEffect, useState } from "react";
import { api, apiJSON } from "../api.js";
import BranchList from "./BranchList.jsx";
import Modal from "./Modal.jsx";

// BranchModal: switch a repo's branch. Lists branches newest-commit-first with a
// filter (via BranchList); clicking one checks it out (a remote-only name DWIMs into
// a tracking branch). Backed by GET branches / POST checkout under api/repos/{name}.
export default function BranchModal({ repoName, onClose, onChecked }) {
  const [branches, setBranches] = useState(null); // null = loading
  const [current, setCurrent] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState("");

  useEffect(() => {
    let alive = true;
    api(`api/repos/${encodeURIComponent(repoName)}/branches`)
      .then((d) => {
        if (!alive) return;
        if (d && d.error) {
          setErr(d.error.message || d.error.code || "取得に失敗しました");
          setBranches([]);
        } else {
          setBranches(d.branches || []);
          setCurrent(d.current || "");
        }
      })
      .catch(() => {
        if (!alive) return;
        setErr("ブランチを取得できませんでした");
        setBranches([]);
      });
    return () => {
      alive = false;
    };
  }, [repoName]);

  const checkout = async (name) => {
    if (busy) return;
    setBusy(name);
    try {
      const res = await apiJSON(
        `api/repos/${encodeURIComponent(repoName)}/checkout`,
        "POST",
        { branch: name },
      );
      if (res && res.error) {
        alert("ブランチ切替に失敗: " + (res.error.message || res.error));
        return;
      }
      onChecked();
    } finally {
      setBusy("");
    }
  };

  return (
    <Modal title={`ブランチ切替 — ${repoName}`} onClose={onClose} className="branch-modal" lockClose={!!busy}>
      <div className="modal-body">
        {err ? (
          <p className="muted pad">{err}</p>
        ) : (
          <BranchList branches={branches} selected={current} onPick={checkout} busy={busy} disableActive autoFocus />
        )}
      </div>
    </Modal>
  );
}
