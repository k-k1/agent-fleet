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
import { api, raw, rawJSON } from "../../core/api/client.ts";

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
    // Fresh create (New Session): /stop removes the just-created session entirely.
    // Resume (`start`): the session already existed — /halt stops it but KEEPS the
    // meta/row, so aborting the login doesn't delete the user's session.
    try {
      await raw(`api/sessions/${encodeURIComponent(name)}/${start ? "halt" : "stop"}`, { method: "POST" });
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
          <>
            <p className="ui-field-hint">
              {tr("sx.ssm_verify_hint")}
            </p>
            {code && (
              <div className="ssm-code-row">
                <span className="ui-field-label">{tr("sx.ssm_code_label")}</span>
                <span className="ssm-code">{code}</span>
              </div>
            )}
            <div>
              <Button
                variant="primary"
                icon="link-external"
                disabled={!url}
                onClick={() => url && window.open(url, "_blank", "noopener")}
              >
                {tr("sx.ssm_sign_in")}
              </Button>
            </div>
            <p className="ui-field-hint">
              {tr("sx.ssm_warn")}
            </p>
          </>
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
