// useProfileLogout — "Log out" of one Settings AWS profile, shared by the WS bar chip and the
// Settings > AWS profiles/SSM row. The Agent revokes and deletes only that profile's login
// (`aws sso logout` would sign out every profile). Asked first, because only a new device-code
// login undoes it. A profile in a shared sign-in (ADR 0125) shares one login with the other
// members, so the confirmation names the sign-in and every member it signs out.
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
// others: further members of the same shared sign-in (ADR 0125) to log out through when the Agent
// refuses p as not exported (the member's own ~/.aws can hold back one member and not another);
// they share one login, so the one confirmation covers them.
export function useProfileLogout(): (p: Pick<AwsLoginTarget, "name" | "label">, others?: string[]) => Promise<boolean> {
  const tr = useT();
  const askConfirm = useConfirm();
  const toast = useToast();
  const logoutProfile = useAwsLoginStore((s) => s.logoutProfile);
  const refreshExpiry = useAwsLoginStore((s) => s.refreshExpiry);
  return useCallback(
    async (p, others = []) => {
      // The list the confirmation names must be the Agent's answer to this very ask, not the
      // store's: without it the confirmation could leave out a chain the logout signs out.
      // A failed ask returns null and leaves the previous list in the store, which must not be
      // mistaken for this answer (an empty array is a real answer, null is not).
      const fresh = await refreshExpiry();
      const list = fresh === null ? null : useAwsLoginStore.getState().profiles;
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
      // A shared sign-in (ADR 0125) has one login: every member is signed out, and every chain
      // that follows any of them.
      const signIn = target.signIn || "";
      const inLogin = (name: string) => name === target.name || (!!signIn && list.some((y) => y.name === name && y.signIn === signIn));
      const members = signIn ? list.filter((x) => x.signIn === signIn && x.name !== me.name && !x.source).map(labelOf) : [];
      const followers = list.filter((x) => !!x.source && inLogin(x.source) && x.name !== me.name).map(labelOf);
      const viaChain = target !== me;
      const profile = labelOf(me);
      const ok = await askConfirm({
        title: signIn && !viaChain ? tr("awslogin.logout_confirm_title_signin", { signIn }) : tr("awslogin.logout_confirm_title", { profile }),
        body: (
          <>
            {viaChain && <p>{tr("awslogin.logout_confirm_chain", { profile, source: labelOf(target) })}</p>}
            <p>{tr("awslogin.logout_confirm_body")}</p>
            {signIn && (
              <p className="aws-logout-signin">
                {members.length > 0
                  ? tr("awslogin.logout_confirm_signin", { signIn, profiles: members.join(", ") })
                  : tr("awslogin.logout_confirm_signin_alone", { signIn })}
              </p>
            )}
            {followers.length > 0 && (
              <p className="aws-logout-followers">
                {tr("awslogin.logout_confirm_followers", { source: signIn || labelOf(target), profiles: followers.join(", ") })}
              </p>
            )}
          </>
        ),
        confirmLabel: tr("awslogin.logout"),
        danger: true,
      });
      if (!ok) return false;
      let r = await logoutProfile(target.name);
      // Only a profile the fresh list still shows in this sign-in shares the login the member
      // confirmed: one moved to another sign-in meanwhile would sign that one out.
      const sameLogin = (n: string) => list.some((x) => x.name === n && !x.source && x.signIn === signIn);
      for (const name of signIn ? others.filter(sameLogin) : []) {
        if (r.ok || r.code !== "not_exported") break;
        r = await logoutProfile(name);
      }
      // Another press of the same profile is already running; its toast will say how it went.
      if (!r.ok && r.code === "busy") return false;
      if (!r.ok) {
        const key = REFUSALS[r.code];
        toast(key ? tr(key) : tr("awslogin.logout_failed", { msg: r.message || r.code }));
        return false;
      }
      // Named after the login that ended: the sign-in, or the source for a chain.
      const ended = signIn || labelOf(target);
      if (r.revoked || r.alreadyEnded || r.noToken) toast(tr("awslogin.logout_done", { profile: ended }), { kind: "success" });
      // Signed out here; only the session at AWS was not ended.
      else toast(tr("awslogin.logout_not_revoked", { profile: ended, msg: r.message }), { kind: "warn", persist: true });
      return true;
    },
    [tr, askConfirm, toast, logoutProfile, refreshExpiry],
  );
}
