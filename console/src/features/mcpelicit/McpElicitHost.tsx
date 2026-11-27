// McpElicitHost keeps the MCP tool calls held for a person's confirmation (ADR 0146 decision 6)
// in view. It asks the Agent on start, whenever an mcp-elicitation notification arrives, every
// few seconds while a call is held, and now and then while none is (a lost notification or a
// failed read must not hide a call until its time runs out). A call shows as a card in its
// session's mirror when that mirror is on screen, and as a sticky toast otherwise; a call no
// session could be named for (a shared daemon's) is always a toast. Closing a toast declines the
// call, as closing a card does in the ADR.
//
// The App mounts one with toasts; a popped-out pane mounts one without, so its mirror's cards
// still come, while the main window keeps the toasts.
import { useEffect, useMemo, useRef } from "react";
import { errText } from "../../core/api/client.ts";
import { t } from "../../lib/i18n/index.ts";
import { useInterval } from "../../lib/useInterval.ts";
import { dismissToast, toast } from "../../ui/toast.ts";
import { useAwsConfirmStore } from "../awsconfirm/store.ts";
import { useNotificationStore } from "../notifications/store.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { McpElicitCard, useHeld, useNow } from "./McpElicitCard.tsx";
import { isSending, live, ownerKey, useMcpElicitStore } from "./store.ts";

export const MCP_ELICITATION_NOTICE = "mcp-elicitation";
const POLL_HELD_MS = 3000;
const POLL_IDLE_MS = 30_000;
const toastKey = (id: string) => "mcp-elicit:" + id;

export function McpElicitHost({ toasts = true }: { toasts?: boolean }) {
  const items = useHeld();
  const until = useMcpElicitStore((s) => s.until);
  const refresh = useMcpElicitStore((s) => s.refresh);
  // Which mirrors are on screen is kept by the AWS confirmation store, which MirrorView reports
  // to; both kinds of card go to the same place.
  const mirrors = useAwsConfirmStore((s) => s.mirrors);
  const now = useNow(items.length > 0);
  const lastNoticeSeq = useRef(0);
  // Calls whose toast the member closed: declined, or being declined, so not toasted again.
  const closedIds = useRef(new Set<string>());

  // On start, and whenever the tenant or the member changes: the old list is dropped and read
  // again for them, in this one effect so nothing can run between the two.
  const tenant = useTenantStore((s) => s.tenant);
  const identityRev = useTenantStore((s) => s.identityRev);
  useEffect(() => {
    useMcpElicitStore.getState().reset();
    void refresh();
  }, [refresh, tenant, identityRev]);

  // The notification is only the trigger to ask; its payload is never read.
  const noticeSeq = useNotificationStore((s) =>
    s.items.reduce((m, n) => (n.kind === MCP_ELICITATION_NOTICE && n.seq > m ? n.seq : m), 0),
  );
  useEffect(() => {
    if (noticeSeq > lastNoticeSeq.current) {
      lastNoticeSeq.current = noticeSeq;
      void refresh();
    }
  }, [noticeSeq, refresh]);

  const toasted = useMemo(
    () =>
      toasts
        ? items.filter(
            (e) =>
              live(until, e, now) &&
              !closedIds.current.has(e.id) &&
              !(e.session && Object.hasOwn(mirrors, e.session) && mirrors[e.session] > 0),
          )
        : [],
    [toasts, items, until, mirrors, now],
  );
  const shown = useRef(new Set<string>());
  useEffect(() => {
    const next = new Set<string>();
    for (const e of toasted) {
      const key = toastKey(e.id);
      const closed = () => {
        const who = ownerKey();
        shown.current.delete(key);
        closedIds.current.add(e.id);
        void useMcpElicitStore
          .getState()
          .answer(e, null)
          .then((err) => {
            if (!err || ownerKey() !== who) return;
            closedIds.current.delete(e.id);
            if (err.code === "stale") return;
            // Not declined (an answer from the card was already on its way, or the decline
            // failed): the toast comes back, showing how it ends.
            if (err.code !== "busy") toast(t("mcpelicit.answer_failed", { reason: errText(err) }), { kind: "warn" });
            void useMcpElicitStore.getState().refresh();
          });
      };
      if (shown.current.has(key) || toast(<McpElicitCard id={e.id} showSession />, {
          kind: "warn",
          duration: 0,
          key,
          onClose: closed,
          // While an answer from the card is on its way or given, the X does nothing: the toast
          // stays with the member's choices until the answer settles.
          canClose: () => !isSending(e.id),
          closeLabel: t("mcpelicit.decline"),
        })) {
        next.add(key);
      }
    }
    for (const key of shown.current) {
      if (!next.has(key)) dismissToast(key);
    }
    shown.current = next;
    const held = new Set(items.map((e) => e.id));
    for (const id of closedIds.current) if (!held.has(id)) closedIds.current.delete(id);
  }, [toasted, items]);
  // A toast outlives its host otherwise (the App can swap to a screen without one).
  useEffect(
    () => () => {
      for (const key of shown.current) dismissToast(key);
      shown.current.clear();
    },
    [],
  );

  useInterval(() => void refresh(), items.length > 0 ? POLL_HELD_MS : POLL_IDLE_MS);

  return null;
}
