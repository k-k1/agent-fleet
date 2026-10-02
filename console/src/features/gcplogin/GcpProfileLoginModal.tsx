// GcpProfileLoginModal — "Log in" / "Log in again" on a Settings > Google Cloud row (ADR 0107
// decision 3). The same rule as the request modal: nothing starts on open, and the link and
// the code field belong only to the attempt this modal's own press created. "Log in again"
// passes force, so gcloud signs the member in afresh instead of reusing a stored credential
// (for a login the member knows was revoked).
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";
import { useGcpLoginStore } from "./store.ts";
import { GcpLoginAttemptView, useGcpLoginAttempt } from "./useGcpLoginAttempt.tsx";

export interface GcpLoginProfile {
  /** The profile name the workspace knows the row by (the CP's `name`). */
  name: string;
  label: string;
  project: string;
  account: string;
}

// The refusals the Agent names, in the member's words; anything else shows the server's text.
export const GCP_REFUSALS: Record<string, MsgKey> = {
  not_a_settings_profile: "gcplogin.err_not_found",
  not_exported: "gcplogin.err_not_exported",
  settings_unavailable: "gcplogin.err_settings_unavailable",
  profile_changed: "gcplogin.profile_changed",
  busy: "gcplogin.err_busy",
};

export function GcpProfileLoginModal({
  profile,
  force,
  onClose,
}: {
  profile: GcpLoginProfile;
  force: boolean;
  onClose: () => void;
}) {
  const tr = useT();
  const refresh = useGcpLoginStore((s) => s.refresh);
  const refreshProfiles = useGcpLoginStore((s) => s.refreshProfiles);
  const base = `api/gcp-login/profiles/${encodeURIComponent(profile.name)}`;
  // A login here settles any request for the same profile, so its toast can go now, and the
  // WS bar badge reads the new state.
  const a = useGcpLoginAttempt(`${base}/start${force ? "?force=1" : ""}`, base, () => {
    void refresh();
    void refreshProfiles();
  });
  const refusal = a.phase === "failed" ? GCP_REFUSALS[a.errorCode] : undefined;
  return (
    <Modal title={tr("gcplogin.modal_title", { profile: profile.label || profile.name })} onClose={onClose}>
      <div className="ui-modal-body">
        <dl className="aws-login-facts">
          <dt>{tr("gcplogin.field_profile")}</dt>
          <dd>{profile.name}</dd>
          <dt>{tr("gcplogin.field_project")}</dt>
          <dd>{profile.project}</dd>
          <dt>{tr("gcplogin.field_account")}</dt>
          <dd>{profile.account || tr("gcplogin.account_at_login")}</dd>
        </dl>
        {a.phase === "idle" && <p className="ui-field-hint">{tr(force ? "gcplogin.profile_intro_again" : "gcplogin.profile_intro")}</p>}
        <GcpLoginAttemptView a={a} done={tr("gcplogin.profile_done")} failed={refusal ? tr(refusal) : undefined} />
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          {tr("gcplogin.close")}
        </Button>
        {!a.running && a.phase !== "done" && (
          <Button variant="primary" onClick={a.start}>
            {a.phase === "idle" ? tr(force ? "gcplogin.start_again" : "gcplogin.start") : tr("gcplogin.retry")}
          </Button>
        )}
      </footer>
    </Modal>
  );
}
