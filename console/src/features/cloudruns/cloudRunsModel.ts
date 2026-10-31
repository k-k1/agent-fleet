import { t as tr, type MsgKey } from "../../lib/i18n/index.ts";

/** One af-aws-exec run, as the Agent's run spool holds it (ADR 0126 decision 1). It names the
 *  identity the command ran as and never the command's arguments. A run that started has no
 *  end: the wrapper becomes the command, so nothing sees how it ended; only a refused run has
 *  `endedAt` and `refused`. */
export interface CloudRun {
  id: string;
  provider: string;
  session?: string;
  startedAt: string;
  endedAt?: string;
  profile?: string;
  account?: string;
  role?: string;
  region?: string;
  source?: string;
  program?: string;
  service?: string;
  operation?: string;
  /** Let through by an approval that did not come through the Control Plane (ADR 0126). */
  approvedOutsideConsole?: boolean;
  refused?: string;
  /** The shared sign-in the profile logs in through. */
  signIn?: string;
  /** The SSM target of a session start. */
  target?: string;
  /** The identity is what Settings names for an SSM pane, not what STS confirmed, and the row has
   *  no exit status (the pane resolves its credentials in its own shell). Not an af-aws-exec run. */
  identityFromSettings?: boolean;
}

/** parseCloudRuns keeps the well-formed rows of a `cloudRuns` or `runs` payload. */
export function parseCloudRuns(v: unknown): CloudRun[] {
  if (!Array.isArray(v)) return [];
  return v.filter(
    (r): r is CloudRun =>
      !!r && typeof r === "object" && typeof (r as CloudRun).id === "string" && typeof (r as CloudRun).startedAt === "string",
  );
}

const REFUSED: Record<string, MsgKey> = {
  login_required: "cloudruns.refused_login_required",
  usage: "cloudruns.refused_usage",
  refused: "cloudruns.refused_refused",
  confirm_required: "cloudruns.refused_confirm",
};

/** The parts of a run's line, in order; empty parts are left out. */
export function cloudRunParts(r: CloudRun): string[] {
  const who = [r.account, r.role].filter(Boolean).join(" / ");
  const cmd = [r.program, r.service, r.operation].filter(Boolean).join(" ");
  const end = r.refused ? tr(REFUSED[r.refused] ?? "cloudruns.refused_refused") : "";
  return [
    r.provider.toUpperCase(),
    r.profile ?? "",
    who,
    r.region ?? "",
    r.source ? tr("cloudruns.via", { source: r.source }) : "",
    r.signIn ? tr("cloudruns.sign_in", { name: r.signIn }) : "",
    cmd,
    r.target ? tr("cloudruns.target", { target: r.target }) : "",
    r.identityFromSettings ? tr("cloudruns.from_settings") : "",
    r.approvedOutsideConsole ? tr("cloudruns.outside_console") : "",
    end,
  ].filter(Boolean);
}
