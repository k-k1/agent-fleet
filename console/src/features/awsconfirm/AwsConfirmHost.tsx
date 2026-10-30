// AwsConfirmHost, mounted once in the App, keeps the "confirm before use" requests (ADR 0126)
// in view. It asks the Agent on start, whenever an aws-use-confirm notification arrives, and
// every few seconds while a request is pending. A request shows as a card in its session's
// mirror when that mirror is on screen, and as a sticky toast otherwise.
import { useEffect, useMemo, useRef } from "react";
import { dismissToast, toast } from "../../ui/toast.ts";
import { useNotificationStore } from "../notifications/store.ts";
import { AwsConfirmCard } from "./AwsConfirmCard.tsx";
import { useAwsConfirmStore } from "./store.ts";

export const AWS_CONFIRM_NOTICE = "aws-use-confirm";
const POLL_MS = 3000;
const toastKey = (id: string) => "aws-confirm:" + id;

export function AwsConfirmHost() {
  const requests = useAwsConfirmStore((s) => s.requests);
  const answering = useAwsConfirmStore((s) => s.answering);
  const mirrors = useAwsConfirmStore((s) => s.mirrors);
  const refresh = useAwsConfirmStore((s) => s.refresh);
  const lastNoticeSeq = useRef(0);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // The notification is only the trigger to ask; its payload is never read.
  const noticeSeq = useNotificationStore((s) =>
    s.items.reduce((m, n) => (n.kind === AWS_CONFIRM_NOTICE && n.seq > m ? n.seq : m), 0),
  );
  useEffect(() => {
    if (noticeSeq > lastNoticeSeq.current) {
      lastNoticeSeq.current = noticeSeq;
      void refresh();
    }
  }, [noticeSeq, refresh]);

  const toasted = useMemo(
    () => requests.filter((r) => !answering[r.id] && !mirrors[r.session]),
    [requests, answering, mirrors],
  );
  const shown = useRef(new Set<string>());
  useEffect(() => {
    const next = new Set<string>();
    for (const r of toasted) {
      const key = toastKey(r.id);
      if (shown.current.has(key) || toast(<AwsConfirmCard r={r} showSession />, { kind: "warn", duration: 0, key })) {
        next.add(key);
      }
    }
    for (const key of shown.current) {
      if (!next.has(key)) dismissToast(key);
    }
    shown.current = next;
  }, [toasted]);

  const polling = requests.length > 0;
  useEffect(() => {
    if (!polling) return;
    const t = window.setInterval(() => void refresh(), POLL_MS);
    return () => clearInterval(t);
  }, [polling, refresh]);

  return null;
}
