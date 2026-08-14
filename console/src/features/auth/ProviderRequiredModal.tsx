// ProviderRequiredModal — surfaced when the active tenant only accepts a sign-in
// method other than the one this session used (docs/61 §61.9.4 + ADR0043 決定 18).
//
// A session holds exactly one provider on purpose, so switching to a department
// that requires a different IdP means signing in again. The Control Plane answers
// `provider_required` instead of a plain 403 precisely so this dialog can exist:
// being told "not allowed" when the fix is one click away is how support tickets
// are made. Dismissing is a real option here — unlike an expired session, the
// person can simply go back to a tenant they are already signed in for.
import { useEffect, useState } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import {
  clearProviderRequired,
  reloginForTenant,
  subscribeProviderRequired,
} from "../../core/auth/providerRequired.ts";
import type { ProviderRequired } from "../../core/auth/providerRequired.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { confirmDirtyNavigation } from "../editor/dirtyRegistry.ts";

export function ProviderRequiredModal() {
  const tr = useT();
  const tenants = useTenantStore((s) => s.tenants);
  const [req, setReq] = useState<ProviderRequired | null>(null);
  useEffect(() => subscribeProviderRequired(setReq), []);
  if (!req) return null;

  // The provider comes from the tenant list rather than the error payload: the
  // Console already holds allowed_providers per membership, and keeping the error
  // shape at {code,message} avoids widening the API's error contract.
  const t = tenants.find((x) => x.slug === req.tenant);
  const provider = req.provider || t?.allowed_providers?.[0] || "";
  const label = t?.name || req.tenant;

  const dismiss = () => {
    clearProviderRequired();
    setReq(null);
  };
  const signIn = () => {
    void confirmDirtyNavigation("logout").then((proceed) => {
      if (proceed) reloginForTenant({ tenant: req.tenant, provider });
    });
  };
  return (
    <Modal title={tr("auth.provider_required_title")} onClose={dismiss}>
      <div className="ui-modal-body">
        <p>{tr("auth.provider_required_body", { tenant: label })}</p>
        <p>{tr("auth.provider_required_hint")}</p>
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={dismiss}>
          {tr("common.cancel")}
        </Button>
        <Button variant="primary" icon="sign-in" onClick={signIn}>
          {tr("auth.provider_required_signin")}
        </Button>
      </footer>
    </Modal>
  );
}
