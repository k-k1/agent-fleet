// AuthExpiredModal — surfaced when the Control Plane login session expires. The
// fetch wrapper (a 401 on any API call) or a dropped terminal socket trips
// signalAuthExpired; this dialog then replaces the old silent full-page bounce to
// /login. It reassures the user that their running sessions keep working in the
// workspace (a lapsed browser login does not stop them) and offers an explicit
// re-login. Any dismissal — the ✕, the backdrop, Escape, the device back button —
// also re-logins: with an expired cookie every request 401s, so re-authenticating
// is the only useful action.
import { useEffect, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { subscribeAuthExpired, relogin } from "../../core/auth/authExpired.ts";

export function AuthExpiredModal() {
  const [shown, setShown] = useState(false);
  // subscribeAuthExpired fires immediately if the latch already flipped before mount.
  useEffect(() => subscribeAuthExpired(() => setShown(true)), []);
  if (!shown) return null;
  return (
    <Modal title="ログインの有効期限が切れました" onClose={relogin}>
      <div className="ui-modal-body">
        <p>
          ログインセッションの有効期限が切れました。作業中のセッションはワークスペース上で
          そのまま動き続けています（ブラウザのログイン切れでは停止しません）。
        </p>
        <p>再ログインすると、この画面に戻って作業を続けられます。</p>
      </div>
      <footer className="ui-modal-foot">
        <Button variant="primary" icon="sign-in" onClick={relogin}>
          再ログイン
        </Button>
      </footer>
    </Modal>
  );
}
