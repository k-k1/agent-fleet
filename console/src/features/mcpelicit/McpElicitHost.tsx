// McpElicitHost, mounted once in the App, keeps the MCP tool calls held for a person's
// confirmation (ADR 0146 decision 6) in view. It asks the Agent on start, whenever an
// mcp-elicitation notification arrives, and every few seconds while a call is held. A call shows
// as a card in its session's mirror when that mirror is on screen, and as a sticky toast
// otherwise; a call no session could be named for (a shared daemon's) is always a toast.
import { useEffect, useMemo, useRef } from "react";
import { useInterval } from "../../lib/useInterval.ts";
import { dismissToast, toast } from "../../ui/toast.ts";
import { useAwsConfirmStore } from "../awsconfirm/store.ts";
import { useNotificationStore } from "../notifications/store.ts";
import { McpElicitCard } from "./McpElicitCard.tsx";
import { useMcpElicitStore } from "./store.ts";

export const MCP_ELICITATION_NOTICE = "mcp-elicitation";
const POLL_MS = 3000;
const toastKey = (id: string) => "mcp-elicit:" + id;

export function McpElicitHost() {
  const items = useMcpElicitStore((s) => s.items);
  const answering = useMcpElicitStore((s) => s.answering);
  const refresh = useMcpElicitStore((s) => s.refresh);
  // Which mirrors are on screen is kept by the AWS confirmation store, which MirrorView reports
  // to; both kinds of card go to the same place.
  const mirrors = useAwsConfirmStore((s) => s.mirrors);
  const lastNoticeSeq = useRef(0);

  useEffect(() => {
    void refresh();
  }, [refresh]);

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
    () => items.filter((e) => !answering[e.id] && !(e.session && mirrors[e.session])),
    [items, answering, mirrors],
  );
  const shown = useRef(new Set<string>());
  useEffect(() => {
    const next = new Set<string>();
    for (const e of toasted) {
      const key = toastKey(e.id);
      if (shown.current.has(key) || toast(<McpElicitCard e={e} showSession />, { kind: "warn", duration: 0, key })) {
        next.add(key);
      }
    }
    for (const key of shown.current) {
      if (!next.has(key)) dismissToast(key);
    }
    shown.current = next;
  }, [toasted]);

  useInterval(() => void refresh(), items.length > 0 ? POLL_MS : null);

  return null;
}
