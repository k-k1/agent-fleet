// GcpLoginHost — mounted once in the App. It shows a sticky toast for every pending
// af-gcloud-exec login request (ADR 0107 decision 3), withdraws it once the Agent no longer
// lists the request (logged in anywhere, cancelled, expired, the profile changed), and renders
// the login modal. It asks the Agent once on start, again whenever a gcp-login-required
// notification arrives (whose payload it never reads), and every few seconds only while a
// toast is up.
import { useEffect, useMemo, useRef } from "react";
import { dismissToast, toast } from "../../ui/toast.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useNotificationStore } from "../notifications/store.ts";
import { GcpLoginModal } from "./GcpLoginModal.tsx";
import { GcpProfileLoginModal } from "./GcpProfileLoginModal.tsx";
import { gcpWaitersLine, useGcpLoginStore, type GcpLoginRequest } from "./store.ts";

export const GCP_LOGIN_NOTICE = "gcp-login-required";
const POLL_MS = 4000;
const toastKey = (id: string) => "gcp-login:" + id;

function GcpLoginToast({ r }: { r: GcpLoginRequest }) {
  const tr = useT();
  const open = useGcpLoginStore((s) => s.open);
  const who = gcpWaitersLine(r);
  return (
    <span className="update-toast">
      <span className="update-toast-txt">
        <span>{tr("gcplogin.toast_title")}</span>
        <span className="update-toast-sub">
          {tr("gcplogin.toast_profile", { profile: r.label || r.profile, project: r.project })}
        </span>
        {who && <span className="update-toast-sub">{tr("gcplogin.toast_who", { who })}</span>}
      </span>
      <button type="button" className="update-toast-btn" onClick={() => open(r.id)}>
        {tr("gcplogin.toast_button")}
      </button>
    </span>
  );
}

export function GcpLoginHost() {
  const requests = useGcpLoginStore((s) => s.requests);
  const hidden = useGcpLoginStore((s) => s.hidden);
  const modal = useGcpLoginStore((s) => s.modal);
  const refresh = useGcpLoginStore((s) => s.refresh);
  const hide = useGcpLoginStore((s) => s.hide);
  const profileModal = useGcpLoginStore((s) => s.profileModal);
  const closeProfile = useGcpLoginStore((s) => s.closeProfile);
  const refreshProfiles = useGcpLoginStore((s) => s.refreshProfiles);
  const lastNoticeSeq = useRef(0);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const noticeSeq = useNotificationStore((s) =>
    s.items.reduce((m, n) => (n.kind === GCP_LOGIN_NOTICE && n.seq > m ? n.seq : m), 0),
  );
  useEffect(() => {
    if (noticeSeq > lastNoticeSeq.current) {
      lastNoticeSeq.current = noticeSeq;
      void refresh();
    }
  }, [noticeSeq, refresh]);

  const visible = useMemo(() => requests.filter((r) => !hidden[r.id]), [requests, hidden]);
  // key -> what the toast shows; a toast is re-issued only when that changed.
  const shown = useRef(new Map<string, string>());
  useEffect(() => {
    const next = new Map<string, string>();
    for (const r of visible) {
      const key = toastKey(r.id);
      const sig = JSON.stringify([r.label, r.profile, r.project, gcpWaitersLine(r)]);
      if (shown.current.get(key) === sig) {
        next.set(key, sig);
      } else if (toast(<GcpLoginToast r={r} />, { kind: "info", duration: 0, key, onClose: () => hide(r.id) })) {
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
  // the new one must not inherit the old attempt, its link or its code field. The profile
  // modal (the WS bar badge's) is keyed by profile for the same reason.
  return (
    <>
      {modal && <GcpLoginModal key={modal} id={modal} />}
      {profileModal && (
        <GcpProfileLoginModal
          key={profileModal.profile.name}
          profile={profileModal.profile}
          force={profileModal.force}
          onClose={() => {
            closeProfile();
            void refreshProfiles();
          }}
        />
      )}
    </>
  );
}
