// ProfileLoginModal — "Log in" on a Settings > AWS profiles/SSM row (#1028). The same rule as the
// request modal (ADR 0102 decision 3): nothing starts on open, and the modal shows only the
// code of the attempt its own press created. There is no request behind it, so there is
// nothing to cancel, and no cancel hold stops it.
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";
import { useAwsLoginStore } from "./store.ts";
import { LoginAttemptStatus, useLoginAttempt } from "./useLoginAttempt.tsx";

export interface LoginProfile {
  /** The ~/.aws profile name the workspace knows the row by (the CP's `name`). */
  name: string;
  label: string;
  accountId: string;
  roleName: string;
}

// The refusals the Agent names, in the member's words; anything else shows the server's text.
const REFUSALS: Record<string, MsgKey> = {
  not_a_settings_profile: "awslogin.err_not_found",
  not_exported: "awslogin.err_not_exported",
  incomplete_profile: "awslogin.err_incomplete",
  settings_unavailable: "awslogin.err_settings_unavailable",
};

// onLoggedIn fires when this modal's own attempt finishes signed in. The Agent re-reads Settings
// before it starts one, so the login is for the row as saved, not for the last poll's copy.
export function ProfileLoginModal({
  profile,
  onClose,
  onLoggedIn,
}: {
  profile: LoginProfile;
  onClose: () => void;
  onLoggedIn?: () => void;
}) {
  const tr = useT();
  const refresh = useAwsLoginStore((s) => s.refresh);
  const refreshExpiry = useAwsLoginStore((s) => s.refreshExpiry);
  const base = `api/aws-login/profiles/${encodeURIComponent(profile.name)}`;
  // A login here settles any request for the same profile and moves its end, so both
  // toasts can go now.
  const a = useLoginAttempt(`${base}/start`, (att) => `${base}/attempts/${encodeURIComponent(att)}`, () => {
    void refresh();
    void refreshExpiry();
    onLoggedIn?.();
  });
  const refusal = a.phase === "failed" ? REFUSALS[a.errorCode] : undefined;
  return (
    <Modal title={tr("awslogin.modal_title", { profile: profile.label || profile.name })} onClose={onClose}>
      <div className="ui-modal-body">
        <dl className="aws-login-facts">
          <dt>{tr("awslogin.field_profile")}</dt>
          <dd>{profile.name}</dd>
          <dt>{tr("awslogin.field_account")}</dt>
          <dd>{profile.accountId}</dd>
          <dt>{tr("awslogin.field_role")}</dt>
          <dd>{profile.roleName}</dd>
        </dl>
        {a.phase === "idle" && <p className="ui-field-hint">{tr("awslogin.profile_intro")}</p>}
        <LoginAttemptStatus
          a={a}
          verifyHint={tr("awslogin.profile_verify_hint")}
          done={tr("awslogin.profile_done")}
          failed={refusal ? tr(refusal) : undefined}
        />
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          {tr("awslogin.close")}
        </Button>
        {!a.running && a.phase !== "done" && (
          <Button variant="primary" onClick={a.start}>
            {a.phase === "idle" ? tr("awslogin.start") : tr("awslogin.retry")}
          </Button>
        )}
      </footer>
    </Modal>
  );
}
