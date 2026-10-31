import { useCallback, useRef } from "react";
import { raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// useReauth: the sign-out-then-sign-in sequence behind ReauthButton, for the kinds whose
// CLI has no separate re-login (agy, cursor, kiro, muse). Each kind's start route refuses
// with 409 "already connected; disconnect first to re-authenticate" while a login exists,
// so the only way to sign in again is DELETE api/connections/<kind>, then that kind's own
// start flow, passed in as `then`.
//
// A sign-out that failed must stop here: the connect flow would otherwise run against a
// login that is still in place (a 409, or a poll that reads "connected" from the stale
// credentials). One run at a time: a second confirm while the first is in flight would open
// a second login flow and leave the two racing.
export function useReauth(kind: string, reload: () => void, setBusy: (b: boolean) => void) {
  const tr = useT();
  const toast = useToast();
  const running = useRef(false);
  return useCallback(
    async (then: () => void | Promise<void>) => {
      if (running.current) return;
      running.current = true;
      setBusy(true);
      try {
        let failure = "";
        try {
          const r = await raw(`api/connections/${kind}`, { method: "DELETE" });
          if (!r.ok) failure = `HTTP ${r.status}`;
        } catch (e) {
          failure = e instanceof Error ? e.message : String(e);
        }
        if (failure) {
          toast(tr("provider.reauth_signout_failed", { msg: failure }));
          return;
        }
        reload(); // put the status pill back to "not connected"; the flow view does not wait for it
        await then();
      } finally {
        running.current = false;
        setBusy(false);
      }
    },
    [kind, reload, setBusy, toast, tr],
  );
}
