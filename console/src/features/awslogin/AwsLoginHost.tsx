// AwsLoginHost — mounted once in the App. It shows a sticky toast at the bottom for every
// pending af-aws-exec login request (ADR 0102 decision 2), withdraws it once the Agent no longer
// lists the request (logged in anywhere, cancelled, expired), and renders the login modal. It
// asks the Agent once on start, again whenever an aws-login-required notification arrives, and
// every few seconds only while a toast is up.
import { useEffect, useMemo, useRef } from "react";
import { dismissToast, toast } from "../../ui/toast.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useNotificationStore } from "../notifications/store.ts";
import { AwsLoginModal } from "./AwsLoginModal.tsx";
import { useAwsLoginStore, waitersLine, type AwsLoginRequest } from "./store.ts";

export const AWS_LOGIN_NOTICE = "aws-login-required";
const POLL_MS = 4000;
const toastKey = (id: string) => "aws-login:" + id;

function AwsLoginToast({ r }: { r: AwsLoginRequest }) {
  const tr = useT();
  const open = useAwsLoginStore((s) => s.open);
  const who = waitersLine(r);
  return (
    <span className="update-toast">
      <span className="update-toast-txt">
        <span>{tr("awslogin.toast_title")}</span>
        <span className="update-toast-sub">
          {tr("awslogin.toast_profile", { profile: r.label || r.profile, account: r.accountId, role: r.roleName })}
        </span>
        {who && <span className="update-toast-sub">{tr("awslogin.toast_who", { who })}</span>}
      </span>
      <button type="button" className="update-toast-btn" onClick={() => open(r.id)}>
        {tr("awslogin.toast_button")}
      </button>
    </span>
  );
}

export function AwsLoginHost() {
  const requests = useAwsLoginStore((s) => s.requests);
  const hidden = useAwsLoginStore((s) => s.hidden);
  const modal = useAwsLoginStore((s) => s.modal);
  const refresh = useAwsLoginStore((s) => s.refresh);
  const hide = useAwsLoginStore((s) => s.hide);
  const lastNoticeSeq = useRef(0);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // A new aws-login-required notification is the trigger to ask; its payload is never read.
  const noticeSeq = useNotificationStore((s) =>
    s.items.reduce((m, n) => (n.kind === AWS_LOGIN_NOTICE && n.seq > m ? n.seq : m), 0),
  );
  useEffect(() => {
    if (noticeSeq > lastNoticeSeq.current) {
      lastNoticeSeq.current = noticeSeq;
      void refresh();
    }
  }, [noticeSeq, refresh]);

  const visible = useMemo(() => requests.filter((r) => !hidden[r.id]), [requests, hidden]);
  const shownKeys = useRef(new Set<string>());
  useEffect(() => {
    const next = new Set<string>();
    for (const r of visible) {
      const key = toastKey(r.id);
      next.add(key);
      toast(<AwsLoginToast r={r} />, { kind: "info", duration: 0, key, onClose: () => hide(r.id) });
    }
    for (const key of shownKeys.current) {
      if (!next.has(key)) dismissToast(key);
    }
    shownKeys.current = next;
  }, [visible, hide]);

  const polling = visible.length > 0;
  useEffect(() => {
    if (!polling) return;
    const t = window.setInterval(() => void refresh(), POLL_MS);
    return () => clearInterval(t);
  }, [polling, refresh]);

  return modal ? <AwsLoginModal id={modal} /> : null;
}
