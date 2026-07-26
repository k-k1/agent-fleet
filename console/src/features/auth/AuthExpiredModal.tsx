// AuthExpiredModal — surfaced when the Control Plane login session expires. The
// fetch wrapper (a 401 on any API call) or a dropped terminal socket trips
// signalAuthExpired; this dialog then replaces the old silent full-page bounce to
// /login. It reassures the user that their running sessions keep working in the
// workspace (a lapsed browser login does not stop them) and offers an explicit
// re-login. Any dismissal — the ✕, the backdrop, Escape, the device back button —
// also re-logins: with an expired cookie every request 401s, so re-authenticating
// is the only useful action.
import { useEffect, useState } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { subscribeAuthExpired, relogin } from "../../core/auth/authExpired.ts";
import { confirmDirtyNavigation } from "../editor/dirtyRegistry.ts";

export function AuthExpiredModal() {
  const tr = useT();
  const [shown, setShown] = useState(false);
  // subscribeAuthExpired fires immediately if the latch already flipped before mount.
  useEffect(() => subscribeAuthExpired(() => setShown(true)), []);
  if (!shown) return null;
  const guardedRelogin = () => {
    void confirmDirtyNavigation("logout").then((proceed) => {
      if (proceed) relogin();
    });
  };
  return (
    <Modal title={tr("auth.expired_title")} onClose={guardedRelogin}>
      <div className="ui-modal-body">
        <p>{tr("auth.expired_body")}</p>
        <p>{tr("auth.expired_relogin_hint")}</p>
      </div>
      <footer className="ui-modal-foot">
        <Button variant="primary" icon="sign-in" onClick={guardedRelogin}>
          {tr("auth.relogin")}
        </Button>
      </footer>
    </Modal>
  );
}
