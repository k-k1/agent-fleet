// SsmLoginModal — drives the SSM SSO handshake for one session WITHOUT attaching
// the terminal yet. Shared by New Session (after create) and the Sessions list
// (resume). Polls /api/sessions/{name}/ssm-login:
//   authorize → shows the device-auth URL + code (manual open — the user must
//               verify the code first; device-code phishing guard)
//   pending   → connecting (the cached-token case goes straight to ready)
//   ready     → onReady(name) so the caller attaches
//   error     → shows the failure
// `start` (resume) first POSTs /start; `force` re-authenticates.
import { useEffect, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { DeviceCodeView } from "./DeviceCodeView.tsx";
import { api, raw, rawJSON, sessionDelete } from "../../core/api/client.ts";

interface SsmLoginModalProps {
  name: string;
  start?: boolean;
  force?: boolean;
  onReady: (name: string) => void;
  onCancel: () => void;
}

export function SsmLoginModal({ name, start = false, force = false, onReady, onCancel }: SsmLoginModalProps) {
  const [phase, setPhase] = useState("pending");
  const [url, setUrl] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const tr = useT();

  useEffect(() => {
    let alive = true;
    const poll = async () => {
      if (!alive) return;
      let d = null;
      try {
        d = await api(`api/sessions/${encodeURIComponent(name)}/ssm-login`);
      } catch {
        d = null;
      }
      if (!alive) return;
      if (d && !d.error) {
        if (d.phase === "authorize") {
          if (d.url) setUrl(d.url);
          if (d.code) setCode(d.code);
        }
        if (d.phase === "ready") {
          onReady(name);
          return;
        }
        setPhase(d.phase);
        if (d.phase === "error") {
          setError(d.message || "");
          return;
        }
      }
      if (alive) setTimeout(poll, 1500);
    };
    const run = async () => {
      if (start) {
        try {
          await rawJSON(`api/sessions/${encodeURIComponent(name)}/start${force ? "?force=1" : ""}`, "POST");
        } catch {
          /* the poll surfaces the failure */
        }
      }
      setTimeout(poll, start ? 900 : 600);
    };
    void run();
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name]);

  const cancel = async () => {
    // Fresh create (New Session): delete the just-created session (it goes to the trash like
    // every delete, ADR 0101 — a meta-only entry). Resume (`start`): the session already
    // existed — /halt stops it but KEEPS the meta/row, so aborting the login doesn't delete
    // the user's session.
    try {
      await (start
        ? raw(`api/sessions/${encodeURIComponent(name)}/halt`, { method: "POST" })
        : sessionDelete(name, { stop: true }));
    } catch {
      /* best effort */
    }
    onCancel();
  };

  return (
    <Modal title={tr("sx.ssm_title", { name })} onClose={cancel}>
      <div className="ui-modal-body">
        {phase === "error" ? (
          <p className="ssm-error">{tr("sx.ssm_login_failed")}{error ? " " + error : ""}</p>
        ) : phase === "authorize" ? (
          <DeviceCodeView url={url} code={code} hint={tr("sx.ssm_verify_hint")} />
        ) : (
          <p className="ui-field-hint">{tr("sx.ssm_connecting")}</p>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={cancel}>
          {tr("sx.cancel")}
        </Button>
      </footer>
    </Modal>
  );
}
