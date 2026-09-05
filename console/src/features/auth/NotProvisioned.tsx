// NotProvisioned — the landing page for "signed in fine, but not on any tenant's roster yet"
// (docs/log/61 §61.10.2, P7-2).
//
// This is not an error but the normal first step on an invite-run deployment: since
// AF_PROVISION=invite became the default for new installs, this is the first screen someone
// sees before they are invited. Previously the normal Console opened in this state and every
// request was rejected with a 403, one toast at a time, with nothing saying what to do.
//
// The page answers exactly three things:
//   1. this is not a failure (sign-in worked);
//   2. the next step is to ask an administrator;
//   3. the address to give them. Administrators add people to the roster by address, so
//      without it there is always one extra round trip to work out which address was used —
//      and the more sign-in methods someone has, the less they know which one they used.
//
// A super_admin never reaches this page: the CP answers 200 even for a super_admin with zero
// memberships (tenants.go, decision 23), so whoever creates the first tenant is not locked out.
// Use ui/Button: .primary / .ghost only have styling inside the settings modal (scoped by
// :where(.settings-modal) in settings.css), and this page is outside it, so a bare
// <button className="primary"> would render with the browser default look.
import { Button } from "../../ui/Button.tsx";
import { rel, clearLocalState } from "../../core/api/client.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useT } from "../../lib/i18n/index.ts";

export function NotProvisioned() {
  const tr = useT();
  const whoami = useTenantStore((s) => s.whoami);
  const email = whoami?.email || whoami?.user || "";

  return (
    <div className="app-shell notprov">
      <main className="notprov-body">
        <div className="notprov-card">
          <span className="codicon codicon-mail notprov-icon" aria-hidden="true" />
          <h1>{tr("notprov.title")}</h1>
          <p className="notprov-lead">{tr("notprov.lead")}</p>
          {/* The address must be selectable and copyable, to paste to an administrator: plain
              text, in full, neither an image nor an ellipsis. */}
          {email && (
            <p className="notprov-who">
              {tr("notprov.signed_in_as")} <code>{email}</code>
            </p>
          )}
          <p className="notprov-hint">{tr("notprov.hint")}</p>
          <div className="notprov-acts">
            {/* Reload: lets someone who has just been added get in without signing out.
                init() would clear notProvisioned on success, but a plain reload is enough —
                being added is a human action, so polling for it would not pay. */}
            <Button variant="primary" icon="refresh" onClick={() => location.reload()}>
              {tr("notprov.retry")}
            </Button>
            <Button
              variant="ghost"
              icon="sign-out"
              onClick={() => {
                // Same sign-out order as TopBar: drop local state first, then go to the CP,
                // so the next account on this browser never sees the previous person's state.
                clearLocalState();
                location.assign(rel("oauth2/logout"));
              }}
            >
              {tr("notprov.switch_account")}
            </Button>
          </div>
        </div>
      </main>
    </div>
  );
}
