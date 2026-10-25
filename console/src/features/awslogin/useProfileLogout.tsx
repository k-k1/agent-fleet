// useProfileLogout — "Log out" of one Settings AWS profile, shared by the WS bar chip and the
// Settings > AWS profiles/SSM row. The Agent revokes and deletes only that profile's login
// (`aws sso logout` would sign out every profile). Asked first, because only a new device-code
// login undoes it.
//
// A role-chaining profile has no login of its own: it follows its source's, and the Agent refuses
// a logout aimed at it (409 chained_profile). Its Log out therefore ends the SOURCE's login, which
// signs out every chain that follows it, so the confirmation names the source and those chains
// first (the pattern of useGcpProfileLogout) and nothing is logged out unless that is confirmed.
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
  const refreshExpiry = useAwsLoginStore((s) => s.refreshExpiry);
  return useCallback(
    async (p) => {
      // The list the confirmation names must be the Agent's answer to this very ask, not the
      // store's: without it the confirmation could leave out a chain the logout signs out.
      await refreshExpiry();
      const list = useAwsLoginStore.getState().profiles;
      if (!list) {
        toast(tr("awslogin.logout_unreadable"));
        return false;
      }
      const me = list.find((x) => x.name === p.name);
      // Gone, or no longer signed in: the refreshed row already says so.
      if (!me || (me.state !== "signed_in" && me.state !== "renew")) return false;
      const target = me.source ? list.find((x) => x.name === me.source && !x.source) : me;
      if (!target) return false;
      const labelOf = (x: { name: string; label: string }) => x.label || x.name;
      const followers = list.filter((x) => x.source === target.name && x.name !== me.name).map(labelOf);
      const viaChain = target !== me;
      const profile = labelOf(me);
      const ok = await askConfirm({
        title: tr("awslogin.logout_confirm_title", { profile }),
        body: (
          <>
            {viaChain && <p>{tr("awslogin.logout_confirm_chain", { profile, source: labelOf(target) })}</p>}
            <p>{tr("awslogin.logout_confirm_body")}</p>
            {followers.length > 0 && (
              <p className="aws-logout-followers">
                {tr("awslogin.logout_confirm_followers", { source: labelOf(target), profiles: followers.join(", ") })}
              </p>
            )}
          </>
        ),
        confirmLabel: tr("awslogin.logout"),
        danger: true,
      });
      if (!ok) return false;
      const r = await logoutProfile(target.name);
      // Another press of the same profile is already running; its toast will say how it went.
      if (!r.ok && r.code === "busy") return false;
      if (!r.ok) {
        const key = REFUSALS[r.code];
        toast(key ? tr(key) : tr("awslogin.logout_failed", { msg: r.message || r.code }));
        return false;
      }
      // Named after the profile whose login ended: the source, for a chain.
      const ended = labelOf(target);
      if (r.revoked || r.alreadyEnded || r.noToken) toast(tr("awslogin.logout_done", { profile: ended }), { kind: "success" });
      // Signed out here; only the session at AWS was not ended.
      else toast(tr("awslogin.logout_not_revoked", { profile: ended, msg: r.message }), { kind: "warn", persist: true });
      return true;
    },
    [tr, askConfirm, toast, logoutProfile, refreshExpiry],
  );
}
