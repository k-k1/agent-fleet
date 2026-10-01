// AwsLoginHost — mounted once in the App. It shows a sticky toast at the bottom for every
// pending af-aws-exec login request (ADR 0102 decision 2), withdraws it once the Agent no longer
// lists the request (logged in anywhere, cancelled, expired), and renders the login modal. It
// asks the Agent once on start, again whenever an aws-login-required notification arrives, and
// every few seconds only while a toast is up.
//
// It also warns before a cached SSO login ends (#1029): one toast per profile and end, from what
// GET /api/aws-login/profiles lists as expiring. It asks once on start, again whenever an
// aws-sso-expiring notification arrives (the Agent checks with its five-minute Settings poll),
// and once a minute only while such a toast is up, so a re-login withdraws it.
import { useEffect, useMemo, useRef } from "react";
import { dismissToast, toast } from "../../ui/toast.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useNotificationStore } from "../notifications/store.ts";
import { AwsLoginModal } from "./AwsLoginModal.tsx";
import { ProfileLoginModal } from "./ProfileLoginModal.tsx";
import { expiryKey, useAwsLoginStore, waitersLine, type AwsLoginRequest, type AwsProfileExpiry } from "./store.ts";

export const AWS_LOGIN_NOTICE = "aws-login-required";
export const AWS_EXPIRING_NOTICE = "aws-sso-expiring";
const POLL_MS = 4000;
const EXPIRY_POLL_MS = 60_000;
const toastKey = (id: string) => "aws-login:" + id;
const expiryToastKey = (p: AwsProfileExpiry) => "aws-sso-expiring:" + expiryKey(p);

// endTime shows the end in the browser's zone. An absolute time, not "in 12 minutes": the
// browser's clock may be off, and the Agent already decided that the end is near.
function endTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function AwsExpiryToast({ p }: { p: AwsProfileExpiry }) {
  const tr = useT();
  const open = useAwsLoginStore((s) => s.showProfile);
  return (
    <span className="update-toast">
      <span className="update-toast-txt">
        <span>{tr("awslogin.expiry_title", { time: endTime(p.expiresAt) })}</span>
        <span className="update-toast-sub">
          {tr("awslogin.toast_profile", { profile: p.label || p.name, account: p.accountId, role: p.roleName })}
        </span>
      </span>
      <button type="button" className="update-toast-btn" onClick={() => open(p)}>
        {tr("awslogin.toast_button")}
      </button>
    </span>
  );
}

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
  // key -> what the toast shows. Every poll returns a new array; a toast is re-issued only
  // when what it shows changed.
  const shown = useRef(new Map<string, string>());
  useEffect(() => {
    const next = new Map<string, string>();
    for (const r of visible) {
      const key = toastKey(r.id);
      const sig = JSON.stringify([r.label, r.profile, r.accountId, r.roleName, waitersLine(r)]);
      if (shown.current.get(key) === sig) {
        next.set(key, sig);
      } else if (toast(<AwsLoginToast r={r} />, { kind: "info", duration: 0, key, onClose: () => hide(r.id) })) {
        next.set(key, sig);
      }
    }
    for (const key of shown.current.keys()) {
      if (!next.has(key)) dismissToast(key);
    }
    shown.current = next;
  }, [visible, hide]);

  const polling = visible.length > 0;
  useEffect(() => {
    if (!polling) return;
    const t = window.setInterval(() => void refresh(), POLL_MS);
    return () => clearInterval(t);
  }, [polling, refresh]);

  // Keyed by the request: a notification for another request can switch the open modal, and
  // the new one must not inherit the old attempt, its code or its poll.
  return (
    <>
      {modal ? <AwsLoginModal key={modal} id={modal} /> : null}
      <AwsExpiryWarnings />
    </>
  );
}

function AwsExpiryWarnings() {
  const expiring = useAwsLoginStore((s) => s.expiring);
  const hidden = useAwsLoginStore((s) => s.hiddenExpiry);
  const profileModal = useAwsLoginStore((s) => s.profileModal);
  const refreshExpiry = useAwsLoginStore((s) => s.refreshExpiry);
  const hideExpiry = useAwsLoginStore((s) => s.hideExpiry);
  const closeProfile = useAwsLoginStore((s) => s.closeProfile);
  const lastNoticeSeq = useRef(0);

  useEffect(() => {
    void refreshExpiry();
  }, [refreshExpiry]);

  // The notification's payload names a profile, but anything in the outbox can be forged; it
  // is only the trigger to ask the Agent.
  const noticeSeq = useNotificationStore((s) =>
    s.items.reduce((m, n) => (n.kind === AWS_EXPIRING_NOTICE && n.seq > m ? n.seq : m), 0),
  );
  useEffect(() => {
    if (noticeSeq > lastNoticeSeq.current) {
      lastNoticeSeq.current = noticeSeq;
      void refreshExpiry();
    }
  }, [noticeSeq, refreshExpiry]);

  const visible = useMemo(() => expiring.filter((p) => !hidden[expiryKey(p)]), [expiring, hidden]);
  const shown = useRef(new Map<string, string>());
  useEffect(() => {
    const next = new Map<string, string>();
    for (const p of visible) {
      const key = expiryToastKey(p);
      const sig = JSON.stringify([p.label, p.accountId, p.roleName]);
      if (shown.current.get(key) === sig) {
        next.set(key, sig);
      } else if (
        toast(<AwsExpiryToast p={p} />, { kind: "warn", duration: 0, key, onClose: () => hideExpiry(expiryKey(p)) })
      ) {
        next.set(key, sig);
      }
    }
    for (const key of shown.current.keys()) {
      if (!next.has(key)) dismissToast(key);
    }
    shown.current = next;
  }, [visible, hideExpiry]);

  const polling = visible.length > 0;
  useEffect(() => {
    if (!polling) return;
    const t = window.setInterval(() => void refreshExpiry(), EXPIRY_POLL_MS);
    return () => clearInterval(t);
  }, [polling, refreshExpiry]);

  // Not tied to the warning list: a login in progress must survive the old end passing.
  return profileModal ? (
    <ProfileLoginModal key={profileModal.name} profile={profileModal} onClose={closeProfile} />
  ) : null;
}
