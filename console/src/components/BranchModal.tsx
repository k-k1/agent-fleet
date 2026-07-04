import { useEffect, useState } from "react";
import { api, apiJSON } from "../api.js";
import BranchList from "./BranchList.jsx";
import type { Branch } from "./BranchList.jsx";
import Modal from "./Modal.jsx";
import { useToast } from "./ToastProvider.jsx";
import type { FormEvent } from "react";

// BranchModal: switch a repo's branch. Lists branches newest-commit-first with a
// filter (via BranchList); clicking one checks it out (a remote-only name DWIMs into
// a tracking branch). Backed by GET branches / POST checkout under api/repos/{name}.
interface BranchModalProps {
  repoName: string;
  onClose?: () => void;
  onChecked: () => void;
}

export default function BranchModal({ repoName, onClose, onChecked }: BranchModalProps) {
  const toast = useToast();
  const [branches, setBranches] = useState<Branch[] | null>(null); // null = loading
  const [current, setCurrent] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState("");
  const [newName, setNewName] = useState(""); // new-branch input

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

  // Checkout an existing branch (create=false) or create a new one off the current
  // HEAD (create=true). Shared error/busy handling; onChecked refreshes on success.
  const doCheckout = async (name: string, create: boolean) => {
    if (busy) return;
    setBusy(name);
    try {
      const res = await apiJSON(
        `api/repos/${encodeURIComponent(repoName)}/checkout`,
        "POST",
        create ? { branch: name, create: true } : { branch: name },
      );
      if (res && res.error) {
        toast((create ? "ブランチ作成に失敗: " : "ブランチ切替に失敗: ") + (res.error.message || res.error));
        return;
      }
      onChecked();
    } finally {
      setBusy("");
    }
  };

  const createBranch = (e: FormEvent) => {
    e.preventDefault();
    const name = newName.trim();
    if (name && !busy) doCheckout(name, true);
  };

  return (
    <Modal title={`ブランチ切替 — ${repoName}`} onClose={onClose} className="branch-modal" lockClose={!!busy}>
      <div className="modal-body">
        {/* New branch off the current HEAD. */}
        <form className="branch-new" onSubmit={createBranch}>
          <input
            className="branch-new-input"
            placeholder="新規ブランチ名（現在の HEAD から作成）"
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
          />
          <button type="submit" className="primary" disabled={!newName.trim() || !!busy}>
            作成して切替
          </button>
        </form>
        {err ? (
          <p className="muted pad">{err}</p>
        ) : (
          <BranchList
            branches={branches}
            selected={current}
            onPick={(name) => doCheckout(name, false)}
            busy={busy}
            disableActive
            autoFocus
          />
        )}
      </div>
    </Modal>
  );
}
