import { useT } from "../../lib/i18n/index.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useAwsConfirmStore, type AwsConfirmRequest } from "./store.ts";
import "./awsConfirm.css";

/** AwsConfirmCard asks the member whether a session may use a profile that asks for
 *  confirmation before use (ADR 0126). Everything it names comes from the Agent's list. */
export function AwsConfirmCard({ r, showSession }: { r: AwsConfirmRequest; showSession?: boolean }) {
  const tr = useT();
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
        <span>{showSession ? tr("awsconfirm.title_session", { session: r.session }) : tr("awsconfirm.title")}</span>
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
