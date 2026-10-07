// useGcpProfileLogout — "Log out" of one Settings Google Cloud profile, shared by the WS bar
// badge and the Settings > Google Cloud row (#1850). gcloud keeps one credential per account,
// so the Agent signs the workspace out of the profile's account, and every profile selecting
// that account goes with it: the confirmation names them first. Nothing is revoked at Google.
import { useCallback } from "react";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { GCP_REFUSALS } from "./GcpProfileLoginModal.tsx";
import { getTenant } from "../../core/api/client.ts";
import { useGcpLoginStore } from "./store.ts";

/** Returns a function that asks, logs out and says how it went; it resolves true once logged out. */
export function useGcpProfileLogout(): (p: { name: string; label: string }) => Promise<boolean> {
  const tr = useT();
  const askConfirm = useConfirm();
  const toast = useToast();
  const logoutProfile = useGcpLoginStore((s) => s.logoutProfile);
  const loadProfiles = useGcpLoginStore((s) => s.loadProfiles);
  return useCallback(
    async (p) => {
      const profile = p.label || p.name;
      // Everything below is about the workspace of the tenant pressed under; the store sends
      // nothing once another is selected.
      const tenant = getTenant();
      // The list the confirmation names must be the Agent's answer to this very ask, not the
      // store's (an older ask answering later can put its list there): without it the
      // confirmation could leave out a profile the logout signs out.
      const list = await loadProfiles();
      if (!list) {
        if (getTenant() === tenant) toast(tr("gcplogin.logout_unreadable"));
        return false;
      }
      const me = list.find((x) => x.name === p.name);
      // Not signed in any more (or gone): the refreshed row already says so.
      if (!me || me.state !== "signed_in" || !me.account) return false;
      const account = me.account;
      const labelOf = (name: string) => {
        const x = list.find((y) => y.name === name);
        return x?.label || name;
      };
      const others = account
        ? list.filter((x) => x.name !== p.name && x.account === account && x.state === "signed_in").map((x) => x.label || x.name)
        : [];
      const ok = await askConfirm({
        title: tr("gcplogin.logout_confirm_title", { profile }),
        body: (
          <>
            <p>{tr("gcplogin.logout_confirm_body", { account })}</p>
            {others.length > 0 && (
              <p className="gcp-logout-others">
                {tr("gcplogin.logout_confirm_others", {
                  profiles: others.join(", "),
                })}
              </p>
            )}
          </>
        ),
        confirmLabel: tr("gcplogin.logout"),
        danger: true,
      });
      if (!ok) return false;
      const r = await logoutProfile(p.name, account, tenant);
      // Another press of the same profile is already running, and its toast will say how it
      // went; or the member moved to another tenant, whose screen this is not about.
      if (!r.ok && (r.code === "in_flight" || r.code === "tenant_changed")) return false;
      if (!r.ok) {
        const key = r.code === "account_changed" ? "gcplogin.logout_account_changed" : GCP_REFUSALS[r.code];
        toast(key ? tr(key) : tr("gcplogin.logout_failed", { msg: r.message || r.code }));
        return false;
      }
      const also = r.profiles.filter((n) => n !== p.name).map(labelOf);
      toast(
        also.length > 0
          ? tr("gcplogin.logout_done_with", {
              profile,
              profiles: also.join(", "),
            })
          : tr("gcplogin.logout_done", { profile }),
        { kind: "success" },
      );
      return true;
    },
    [tr, askConfirm, toast, logoutProfile, loadProfiles],
  );
}
