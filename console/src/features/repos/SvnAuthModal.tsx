// SvnAuthModal — "re-authenticate" for an SVN working copy (docs/log/41 amendment).
//
// The checkout dialog's "save credentials" is an opt-in, and until this existed, declining
// it was a one-way door: the password was used once and forgotten, so every later update
// failed with no way to supply it again. This is that way back, and it is deliberately
// framed as re-AUTHENTICATION rather than "credentials": what the user has in hand is an
// operation that just failed, not a wish to administer a credential store.
//
// The server URL is never typed here. It is asked of the working copy (GET svn-auth), and
// the credential is proven against that server before anything is stored — "saved" must
// not be able to mean "saved a typo", which is the failure that sent the user here.
import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";

interface SvnAuthInfo {
  url?: string;
  urlPrefix?: string;
  username?: string;
  hasCred?: boolean;
  trustCert?: boolean;
}

interface SvnAuthModalProps {
  repo: string;
  onClose: () => void;
  /** Fired after a credential was accepted and stored — the caller retries whatever
   *  failed (the update that opened this dialog). */
  onSaved?: () => void;
}

export function SvnAuthModal({ repo, onClose, onSaved }: SvnAuthModalProps) {
  const tr = useT();
  const toast = useToast();
  const [info, setInfo] = useState<SvnAuthInfo | null>(null);
  const [user, setUser] = useState("");
  const [pass, setPass] = useState("");
  const [trust, setTrust] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    let alive = true;
    api(`api/repos/${encodeURIComponent(repo)}/svn-auth`)
      .then((d) => {
        if (!alive || !d || d.error) return;
        setInfo(d as SvnAuthInfo);
        setUser((d.username as string) || "");
        setTrust(!!d.trustCert);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [repo]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setErr("");
    try {
      const d = await apiJSON(`api/repos/${encodeURIComponent(repo)}/svn-auth`, "POST", {
        username: user.trim(),
        password: pass,
        trustCert: trust,
      });
      if (d?.error) {
        // The server's verdict, in its own words: a rejected password and an unreachable
        // host need different fixes, and only svn can tell them apart.
        const code = typeof d.error === "object" ? (d.error as { code?: string }).code : "";
        setErr(code === "svn_auth_required" ? tr("rp.svn_auth_rejected") : errText(d.error));
        return;
      }
      toast(tr("rp.svn_auth_saved", { name: repo }), { kind: "success" });
      onSaved?.();
      onClose();
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={tr("rp.svn_auth_title", { name: repo })} onClose={onClose} as="form" onSubmit={submit} lockClose={busy}>
      <div className="ui-modal-body">
        <p className="ui-field-hint">{tr("rp.svn_auth_intro")}</p>
        <div className="ui-field">
          <span className="ui-field-label">{tr("rp.svn_auth_server")}</span>
          {/* The URL the credential will be stored under, not the folder's own URL: one
              entry covers every subtree checked out of the same repository. */}
          <code className="svn-auth-url">{info?.urlPrefix || info?.url || tr("common.loading")}</code>
          {info && !info.hasCred && <span className="ui-field-hint">{tr("rp.svn_auth_none_saved")}</span>}
        </div>
        <label className="ui-field">
          <span className="ui-field-label">{tr("rp.svn_auth_user")}</span>
          <input value={user} onChange={(e) => setUser(e.target.value)} autoComplete="off" />
        </label>
        <label className="ui-field">
          <span className="ui-field-label">{tr("rp.svn_auth_password")}</span>
          <input type="password" value={pass} onChange={(e) => setPass(e.target.value)} autoComplete="off" />
        </label>
        <label className="ui-field-inline">
          <input type="checkbox" checked={trust} onChange={(e) => setTrust(e.target.checked)} />
          {tr("rp.svn_trust")}
        </label>
        <p className="ui-field-hint">{tr("rp.svn_auth_scope_hint")}</p>
        {err && <p className="ui-field-hint svn-auth-err">{err}</p>}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          {tr("common.cancel")}
        </Button>
        <Button type="submit" variant="primary" disabled={busy}>
          {busy ? tr("rp.svn_auth_checking") : tr("rp.svn_auth_submit")}
        </Button>
      </footer>
    </Modal>
  );
}
