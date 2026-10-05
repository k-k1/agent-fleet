// useProfileLogout — "Log out" of one Settings AWS profile, shared by the WS bar chip and the
// Settings > AWS profiles/SSM row. The Agent revokes and deletes only that profile's login
// (`aws sso logout` would sign out every profile). Asked first, because only a new device-code
// login undoes it.
import { useCallback } from "react";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { REFUSALS } from "./ProfileLoginModal.tsx";
import { useAwsLoginStore, type AwsLoginTarget } from "./store.ts";

/** Returns a function that asks, logs out and says how it went; it resolves true once logged out. */
export function useProfileLogout(): (p: Pick<AwsLoginTarget, "name" | "label">) => Promise<boolean> {
  const tr = useT();
  const askConfirm = useConfirm();
  const toast = useToast();
  const logoutProfile = useAwsLoginStore((s) => s.logoutProfile);
  return useCallback(
    async (p) => {
      const profile = p.label || p.name;
      const ok = await askConfirm({
        title: tr("awslogin.logout_confirm_title", { profile }),
        body: tr("awslogin.logout_confirm_body"),
        confirmLabel: tr("awslogin.logout"),
        danger: true,
      });
      if (!ok) return false;
      const r = await logoutProfile(p.name);
      // Another press of the same profile is already running; its toast will say how it went.
      if (!r.ok && r.code === "busy") return false;
      if (!r.ok) {
        const key = REFUSALS[r.code];
        toast(key ? tr(key) : tr("awslogin.logout_failed", { msg: r.message || r.code }));
        return false;
      }
      if (r.revoked || r.alreadyEnded || r.noToken) toast(tr("awslogin.logout_done", { profile }), { kind: "success" });
      // Signed out here; only the session at AWS was not ended.
      else toast(tr("awslogin.logout_not_revoked", { profile, msg: r.message }), { kind: "warn", persist: true });
      return true;
    },
    [tr, askConfirm, toast, logoutProfile],
  );
}
