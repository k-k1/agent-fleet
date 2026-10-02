// AwsLoginModal — the member's side of ADR 0102 decision 3. Nothing starts on open: the
// device code is created only when the member presses "Log in", and the modal shows the URL
// and code of the attempt its own press created, polled by that attempt id. If another start
// replaces it, the modal says so and never switches to the other attempt's code.
import { useRef } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { apiJSON } from "../../core/api/client.ts";
import { useAwsLoginStore, waitersLine } from "./store.ts";
import { LoginAttemptStatus, useLoginAttempt } from "./useLoginAttempt.tsx";

export function AwsLoginModal({ id }: { id: string }) {
  const tr = useT();
  const req = useAwsLoginStore((s) => s.requests.find((r) => r.id === id));
  const close = useAwsLoginStore((s) => s.close);
  const refresh = useAwsLoginStore((s) => s.refresh);
  // The request can resolve while the modal is open (a login elsewhere). Remember what it was
  // so the finished modal can still say which profile it was about.
  const shown = useRef(req);
  if (req) shown.current = req;
  const r = shown.current;
  const hint = r ? `?profile=${encodeURIComponent(r.profile)}` : "";
  const base = `api/aws-login/${encodeURIComponent(id)}`;
  const refreshExpiry = useAwsLoginStore((s) => s.refreshExpiry);
  // The login also changes the profile's state, which the WS bar chip reads from the other list.
  const a = useLoginAttempt(`${base}/start${hint}`, (att) => `${base}/attempts/${encodeURIComponent(att)}`, () => {
    void refresh();
    void refreshExpiry();
  });
  const { phase, running } = a;

  const cancelRequest = async () => {
    await apiJSON(`api/aws-login/${encodeURIComponent(id)}/cancel${hint}`, "POST").catch(() => null);
    void refresh();
    close();
  };

  if (!r) {
    return (
      <Modal title={tr("awslogin.modal_title_generic")} onClose={close}>
        <div className="ui-modal-body">
          <p className="ui-field-hint">{tr("awslogin.not_pending")}</p>
        </div>
        <footer className="ui-modal-foot">
          <Button variant="ghost" onClick={close}>
            {tr("awslogin.close")}
          </Button>
        </footer>
      </Modal>
    );
  }

  const who = waitersLine(r);
  return (
    <Modal title={tr("awslogin.modal_title", { profile: r.label || r.profile })} onClose={close}>
      <div className="ui-modal-body">
        <dl className="aws-login-facts">
          <dt>{tr("awslogin.field_profile")}</dt>
          <dd>{r.profile}</dd>
          <dt>{tr("awslogin.field_account")}</dt>
          <dd>{r.accountId}</dd>
          <dt>{tr("awslogin.field_role")}</dt>
          <dd>{r.roleName}</dd>
          {who && (
            <>
              <dt>{tr("awslogin.field_who")}</dt>
              <dd>{who}</dd>
            </>
          )}
        </dl>
        {phase === "idle" && <p className="ui-field-hint">{tr("awslogin.intro")}</p>}
        <LoginAttemptStatus a={a} verifyHint={tr("awslogin.verify_hint")} done={tr("awslogin.done")} />
        {phase !== "done" && <p className="ui-field-hint">{tr("awslogin.close_hint")}</p>}
      </div>
      <footer className="ui-modal-foot">
        {phase !== "done" && (
          <Button variant="ghost" onClick={cancelRequest}>
            {tr("awslogin.cancel_request")}
          </Button>
        )}
        <Button variant="ghost" onClick={close}>
          {tr("awslogin.close")}
        </Button>
        {!running && phase !== "done" && (
          <Button variant="primary" onClick={a.start}>
            {phase === "idle" ? tr("awslogin.start") : tr("awslogin.retry")}
          </Button>
        )}
      </footer>
    </Modal>
  );
}
