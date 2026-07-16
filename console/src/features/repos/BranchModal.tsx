// BranchModal — switch a repo's branch (filterable, newest-first; a remote-only
// name DWIMs into a tracking branch). Branch CREATION deliberately lives at the
// commit graph (explicit start point), not here. Port of components/BranchModal.
import { useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Modal } from "../../ui/Modal.tsx";
import { BranchList } from "./BranchList.tsx";
import type { Branch } from "./BranchList.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";

interface BranchModalProps {
  repoName: string;
  onClose?: () => void;
  onChecked: () => void;
}

export function BranchModal({ repoName, onClose, onChecked }: BranchModalProps) {
  const toast = useToast();
  const tr = useT();
  const [branches, setBranches] = useState<Branch[] | null>(null);
  const [current, setCurrent] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState("");

  useEffect(() => {
    let alive = true;
    api(`api/repos/${encodeURIComponent(repoName)}/branches`)
      .then((d) => {
        if (!alive) return;
        if (d && d.error) {
          setErr(d.error.message || d.error.code || tr("rp.fetch_failed"));
          setBranches([]);
        } else {
          setBranches(d.branches || []);
          setCurrent(d.current || "");
        }
      })
      .catch(() => {
        if (!alive) return;
        setErr(tr("rp.branches_fetch_failed"));
        setBranches([]);
      });
    return () => {
      alive = false;
    };
  }, [repoName]);

  const checkout = async (name: string) => {
    if (busy) return;
    setBusy(name);
    try {
      const res = await apiJSON(`api/repos/${encodeURIComponent(repoName)}/checkout`, "POST", { branch: name });
      if (res && res.error) {
        toast(tr("rp.checkout_failed", { err: errText(res.error) }));
        return;
      }
      onChecked();
    } finally {
      setBusy("");
    }
  };

  return (
    <Modal title={tr("rp.branch_switch_title", { repo: repoName })} onClose={onClose} lockClose={!!busy}>
      <div className="ui-modal-body">
        {err ? (
          <p className="pick-muted">{err}</p>
        ) : (
          <BranchList branches={branches} selected={current} onPick={checkout} busy={busy} disableActive autoFocus />
        )}
      </div>
    </Modal>
  );
}
