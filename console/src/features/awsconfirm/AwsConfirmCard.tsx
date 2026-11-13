import type { ReactNode } from "react";
import { useT } from "../../lib/i18n/index.ts";
import { Trans } from "../../lib/i18n/Trans.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { openSessionFromList } from "../sessions/open.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useAwsConfirmStore, type AwsConfirmRequest } from "./store.ts";
import "./awsConfirm.css";

/** AwsConfirmCard asks the member whether a session may use a profile that asks for
 *  confirmation before use (ADR 0126). Everything it names comes from the Agent's list. */
export function AwsConfirmCard({ r: given, showSession }: { r: AwsConfirmRequest; showSession?: boolean }) {
  const tr = useT();
  // A toast is shown once per request id and keeps the element it was given, while a run of
  // another operation joins the same request: read the latest list so that a write asked for
  // after a read is on the card before the member allows both.
  const r = useAwsConfirmStore((s) => s.requests.find((x) => x.id === given.id)) ?? given;
  const toast = useToast();
  const answer = useAwsConfirmStore((s) => s.answer);
  const busy = useAwsConfirmStore((s) => !!s.answering[r.id]);
  const go = async (verb: "approve" | "deny") => {
    const code = await answer(r, verb);
    if (code) toast(tr("awsconfirm.answer_failed", { code }));
  };
  return (
    <div className="aws-confirm" role="alert">
      <div className="aws-confirm-head">
        <Icon name="shield" />
        <span>{showSession ? <Trans k="awsconfirm.title_session" vars={{ session: r.session }} components={[<SessionLink name={r.session} />]} /> : tr("awsconfirm.title")}</span>
      </div>
      <ul className="aws-confirm-targets">
        {r.targets.map((t) => (
          <li key={t.profile}>
            <b>{t.label || t.profile}</b> · {t.accountId} / {t.roleName}
            {t.region ? " · " + t.region : ""}
            {t.source ? " · " + tr("awsconfirm.via", { source: t.source }) : ""}
          </li>
        ))}
      </ul>
      {r.commands && r.commands.length > 0 && (
        <div className="aws-confirm-sub">{tr("awsconfirm.commands", { commands: r.commands.join(", ") })}</div>
      )}
      <div className="aws-confirm-actions">
        <button type="button" className="primary" disabled={busy} onClick={() => void go("approve")}>
          {tr("awsconfirm.approve")}
        </button>
        <button type="button" className="ghost" disabled={busy} onClick={() => void go("deny")}>
          {tr("awsconfirm.deny")}
        </button>
      </div>
    </div>
  );
}

/** SessionLink opens the asking session the way its row in the left pane does. The text stays
 *  the session's name, which the Agent vouches for; the title, which the session can set
 *  itself, only goes in the tooltip so it cannot pass for another session. */
function SessionLink({ name, children }: { name: string; children?: ReactNode }) {
  const tr = useT();
  const s = useSessionsStore((st) => st.sessions.find((x) => x.name === name));
  const running = useWorkspaceStore((st) => st.state === "running");
  const toast = useToast();
  if (!s) return <>{children}</>;
  const open = (split: boolean) => {
    if (!openSessionFromList(s, split, running)) toast(tr("awsconfirm.open_failed"));
  };
  return (
    <button
      type="button"
      className="linklike"
      title={s.title ? s.title : undefined}
      onClick={(e) => open(e.ctrlKey || e.metaKey)}
    >
      {children}
    </button>
  );
}

/** MirrorConfirmCards shows the requests of the session whose mirror this is, above the
 *  composer, and keeps their toasts away while it is on screen. */
export function MirrorConfirmCards({ session }: { session: string }) {
  const requests = useAwsConfirmStore((s) => s.requests);
  const answering = useAwsConfirmStore((s) => s.answering);
  const mine = requests.filter((r) => r.session === session && !answering[r.id]);
  if (!mine.length) return null;
  return (
    <div className="aws-confirm-mirror">
      {mine.map((r) => (
        <AwsConfirmCard key={r.id} r={r} />
      ))}
    </div>
  );
}
