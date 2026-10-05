// GcpLoginModal — the member's side of ADR 0107 decision 3 for a request an agent filed.
// Nothing starts on open: gcloud's login starts only when the member presses "Log in", and the
// sign-in link and the code field belong to the attempt that press created. Opened from a
// notification or a toast, it shows what the Agent lists for the request and nothing else.
import { useRef } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { apiJSON } from "../../core/api/client.ts";
import { gcpWaitersLine, useGcpLoginStore } from "./store.ts";
import { GcpLoginAttemptView, useGcpLoginAttempt } from "./useGcpLoginAttempt.tsx";
import { GCP_REFUSALS } from "./GcpProfileLoginModal.tsx";

export function GcpLoginModal({ id }: { id: string }) {
  const tr = useT();
  const req = useGcpLoginStore((s) => s.requests.find((r) => r.id === id));
  const close = useGcpLoginStore((s) => s.close);
  const refresh = useGcpLoginStore((s) => s.refresh);
  // The request can resolve while the modal is open (a login elsewhere). Remember what it was
  // so the finished modal can still say which profile it was about.
  const shown = useRef(req);
  if (req) shown.current = req;
  const r = shown.current;
  const hint = r ? `?profile=${encodeURIComponent(r.profile)}` : "";
  const profileBase = `api/gcp-login/profiles/${encodeURIComponent(r?.profile ?? "")}`;
  const a = useGcpLoginAttempt(`api/gcp-login/${encodeURIComponent(id)}/start${hint}`, profileBase, () => void refresh());

  const cancelRequest = async () => {
    await apiJSON(`api/gcp-login/${encodeURIComponent(id)}/cancel${hint}`, "POST").catch(() => null);
    void refresh();
    close();
  };

  if (!r) {
    return (
      <Modal title={tr("gcplogin.modal_title_generic")} onClose={close}>
        <div className="ui-modal-body">
          <p className="ui-field-hint">{tr("gcplogin.not_pending")}</p>
        </div>
        <footer className="ui-modal-foot">
          <Button variant="ghost" onClick={close}>
            {tr("gcplogin.close")}
          </Button>
        </footer>
      </Modal>
    );
  }

  const who = gcpWaitersLine(r);
  const refusal = a.phase === "failed" ? GCP_REFUSALS[a.errorCode] : undefined;
  return (
    <Modal title={tr("gcplogin.modal_title", { profile: r.label || r.profile })} onClose={close}>
      <div className="ui-modal-body">
        <dl className="aws-login-facts">
          <dt>{tr("gcplogin.field_profile")}</dt>
          <dd>{r.profile}</dd>
          <dt>{tr("gcplogin.field_project")}</dt>
          <dd>{r.project}</dd>
          <dt>{tr("gcplogin.field_account")}</dt>
          <dd>{r.account || tr("gcplogin.account_at_login")}</dd>
          {who && (
            <>
              <dt>{tr("gcplogin.field_who")}</dt>
              <dd>{who}</dd>
            </>
          )}
        </dl>
        {a.phase === "idle" && <p className="ui-field-hint">{tr(r.relogin ? "gcplogin.intro_relogin" : "gcplogin.intro")}</p>}
        <GcpLoginAttemptView a={a} done={tr("gcplogin.done")} failed={refusal ? tr(refusal) : undefined} />
        {a.phase !== "done" && <p className="ui-field-hint">{tr("gcplogin.close_hint")}</p>}
      </div>
      <footer className="ui-modal-foot">
        {a.phase !== "done" && (
          <Button variant="ghost" onClick={cancelRequest}>
            {tr("gcplogin.cancel_request")}
          </Button>
        )}
        <Button variant="ghost" onClick={close}>
          {tr("gcplogin.close")}
        </Button>
        {!a.running && a.phase !== "done" && (
          <Button variant="primary" onClick={a.start}>
            {a.phase === "idle" ? tr("gcplogin.start") : tr("gcplogin.retry")}
          </Button>
        )}
      </footer>
    </Modal>
  );
}
