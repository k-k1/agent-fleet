import { t as tr, type MsgKey } from "../../lib/i18n/index.ts";

/** One af-aws-exec run, as the Agent's run spool holds it (ADR 0126 decision 1). It names the
 *  identity the command ran as and never the command's arguments. */
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
  exitCode?: number;
  durationMs?: number;
  refused?: string;
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
};

/** fmtDuration renders a run's length: milliseconds under a second, then seconds, then m s. */
export function fmtDuration(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  const s = ms / 1000;
  if (s < 10) return `${s.toFixed(1)} s`;
  if (s < 60) return `${Math.round(s)} s`;
  const m = Math.floor(s / 60);
  return `${m} m ${Math.round(s - m * 60)} s`;
}

/** The parts of a run's line, in order; empty parts are left out. */
export function cloudRunParts(r: CloudRun): string[] {
  const who = [r.account, r.role].filter(Boolean).join(" / ");
  const cmd = [r.program, r.service, r.operation].filter(Boolean).join(" ");
  let end = "";
  if (r.refused) end = tr(REFUSED[r.refused] ?? "cloudruns.refused_refused");
  else if (typeof r.exitCode === "number") {
    end = tr("cloudruns.exit", { code: r.exitCode });
    if (typeof r.durationMs === "number") end += " · " + fmtDuration(r.durationMs);
  } else end = tr("cloudruns.running");
  return [
    r.provider.toUpperCase(),
    r.profile ?? "",
    who,
    r.region ?? "",
    r.source ? tr("cloudruns.via", { source: r.source }) : "",
    cmd,
    end,
  ].filter(Boolean);
}
