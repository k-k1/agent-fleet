import { t as tr, type MsgKey } from "../../lib/i18n/index.ts";
import type { Group, Part } from "../mirror/transcript/types.ts";

/** One af-aws-exec or af-gcloud-exec run, as the Agent's run spool holds it (ADR 0126 decision 1).
 *  It names the identity the command ran as and never the command's arguments, except the SSM
 *  target (`target`), the one argument an SSM row may carry. An af-aws-exec run that started has
 *  no end: the wrapper becomes the command, so nothing sees how it ended; only a refused run has
 *  `endedAt` and `refused`. af-gcloud-exec supervises its command, so its rows also get `exit`. */
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
  /** The command's exit status, when its wrapper saw it end (af-gcloud-exec). */
  exit?: number;
  /** A Google Cloud run's project, the account its token was minted for, and the service
   *  account it impersonates. */
  project?: string;
  principal?: string;
  impersonates?: string;
}

/** sameRuns keeps the previous array when the rows did not change: it feeds the block memo in
 *  MirrorView, and a fresh array every poll would rebuild every block. The whole row is compared:
 *  a run is written in steps (begin, the resolved identity, the end), and a key of a few fields
 *  misses a step that sets only others (a Google Cloud run's project and account). */
export function sameRuns(prev: CloudRun[], next: CloudRun[]): CloudRun[] {
  return JSON.stringify(prev) === JSON.stringify(next) ? prev : next;
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

/** The parts of a run's line, in order; empty parts are left out. `brief` leaves out the identity
 *  (profile, account and role or project and account, region): a group whose runs all share one
 *  names it once on its head, and repeating it on every line is the noise the group removes. */
export function cloudRunParts(r: CloudRun, brief = false): string[] {
  const who = r.provider === "gcp" ? gcpWho(r) : [r.account, r.role].filter(Boolean).join(" / ");
  const cmd = [r.program, r.service, r.operation].filter(Boolean).join(" ");
  const end = r.refused
    ? tr(REFUSED[r.refused] ?? "cloudruns.refused_refused")
    : typeof r.exit === "number"
      ? tr("cloudruns.exit", { code: r.exit })
      : "";
  return [
    brief ? "" : r.profile ?? "",
    brief ? "" : who,
    brief ? "" : r.region ?? "",
    r.source ? tr("cloudruns.via", { source: r.source }) : "",
    r.signIn ? tr("cloudruns.sign_in", { name: r.signIn }) : "",
    cmd,
    r.target ? tr("cloudruns.target", { target: r.target }) : "",
    r.identityFromSettings ? tr("cloudruns.from_settings") : "",
    r.approvedOutsideConsole ? tr("cloudruns.outside_console") : "",
    end,
  ].filter(Boolean);
}

function gcpWho(r: CloudRun): string {
  const as = r.impersonates ? tr("cloudruns.impersonates", { sa: r.impersonates, by: r.principal ?? "" }) : r.principal ?? "";
  return [r.project, as].filter(Boolean).join(" / ");
}

/** providerLabel is the provider's name as a reader knows it ("AWS", "Google Cloud"). */
export function providerLabel(provider: string): string {
  return provider === "gcp" ? "Google Cloud" : provider ? provider.toUpperCase() : "Cloud";
}

/** The WS bar's brand icon of a provider, so a run reads as the same cloud as the bar's badge. */
export function providerIcon(provider: string): string {
  return provider === "aws" ? "brand:aws" : provider === "gcp" ? "brand:gcp" : "cloud";
}

/** failedRun: the wrapper refused the run, or the command it supervised exited non-zero. */
export const failedRun = (r: CloudRun) => !!r.refused || (typeof r.exit === "number" && r.exit !== 0);

/** identityKey groups runs that ran as the same identity, so a summary names it once. */
function identityKey(r: CloudRun): string {
  return [r.provider, r.profile, r.account, r.role, r.project, r.principal, r.impersonates, r.region].join("\x1f");
}

/** The summary line of several runs: one identity once with the count, or the profiles of
 *  several. Failures are counted apart, so a refused run is never hidden inside the count. */
export function cloudRunSummary(runs: CloudRun[]): { text: string; failed: number; providers: string[]; oneIdentity: boolean } {
  const providers = [...new Set(runs.map((r) => r.provider))];
  const failed = runs.filter(failedRun).length;
  const ids = new Set(runs.map(identityKey));
  let text: string;
  if (ids.size === 1) {
    const r = runs[0];
    const who = r.provider === "gcp" ? gcpWho(r) : [r.account, r.role].filter(Boolean).join(" / ");
    text = [r.profile ?? "", who, r.region ?? ""].filter(Boolean).join(" · ");
  } else {
    const profiles = [...new Set(runs.map((r) => r.profile || "?"))];
    text = profiles.join(", ");
  }
  return { text, failed, providers, oneIdentity: ids.size === 1 };
}

// ── Placement on tool calls ──────────────────────────────────────────────────────────────

// Shell tools of the kinds the mirror draws. A run started by a script is attributed to the
// shell call that ran the script, so only a shell call may take a run it does not name.
const SHELL_TOOL = /^(bash|shell|exec_command|local_shell|run_shell_command|run_in_terminal|execute_command|terminal)$/i;

// The wrapper as a command word: at the start, or after a space, a path, a quote or a shell
// operator, and followed by a space, a quote, an operator or the end. `my-af-aws-exec` is another
// program, and `af-aws-exec()` defines a function rather than running one, so "(" never follows.
const WRAPPER: Record<string, RegExp> = {
  aws: /(?:^|[\s/'"`;&|(])af-aws-exec(?:[\s'"`;&|)<>]|$)/,
  gcp: /(?:^|[\s/'"`;&|(])af-gcloud-exec(?:[\s'"`;&|)<>]|$)/,
};

const commandOf = (p: Part) => p.command || p.info || "";

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** namesRun says whether a call's command visibly runs this provider's wrapper with this
 *  run's profile. Best effort: a label cut at 80 characters may hide the profile. */
function namesRun(text: string, r: CloudRun): boolean {
  const w = WRAPPER[r.provider];
  if (!w || !w.test(text)) return false;
  if (!r.profile) return true;
  return new RegExp("--profile(?:=|\\s+)['\"]?" + escapeRe(r.profile) + "(?:['\"\\s]|$)").test(text);
}

/** toolKey addresses one part of one block: the block's stable idx and the part's position. */
export const toolKey = (groupIdx: number, partIdx: number) => groupIdx + ":" + partIdx;

export interface RunPlacement {
  /** Runs drawn on a tool call, keyed by toolKey. */
  onTool: Map<string, CloudRun[]>;
  /** Runs no call could take: drawn between the turns, collapsed. */
  loose: CloudRun[];
}

/**
 * placeRuns attributes each run to the tool call that made it. The candidates are the calls of
 * the assistant block the run started in (no member prompt between the call and the run), made
 * at or before the run's start. Among them, the latest call whose command names the wrapper and
 * the run's profile wins: a loop or a long command can start several runs after later calls.
 * Without one, the latest shell call takes it (a script that runs the wrapper itself). A run that
 * no call can take, or a block with no per-row times, stays loose.
 */
export function placeRuns(groups: Group[], runs: CloudRun[]): RunPlacement {
  const onTool = new Map<string, CloudRun[]>();
  const loose: CloudRun[] = [];
  const blocks = groups.map((g) => ({ g, start: g.ts ? Date.parse(g.ts) : NaN }));
  for (const r of runs) {
    const at = Date.parse(r.startedAt);
    // An SSM pane's start is the member's press, not a command any call ran (ADR 0126 decision 3).
    if (r.identityFromSettings || Number.isNaN(at)) {
      loose.push(r);
      continue;
    }
    // The newest block started at or before the run: the run happened in it, or after it. A block
    // without a time (a just-sent echo, a queued bubble) is skipped: it has not reached the
    // transcript, so the agent has not run that prompt and no run can come from it.
    let b = -1;
    for (let i = 0; i < blocks.length; i++) {
      if (Number.isFinite(blocks[i].start) && blocks[i].start <= at) b = i;
    }
    const g = b >= 0 ? blocks[b].g : undefined;
    if (!g || g.role === "user" || typeof g.idx !== "number" || !g.partTs) {
      loose.push(r);
      continue;
    }
    let named = -1;
    let shell = -1;
    g.parts.forEach((p, i) => {
      if (p.kind !== "tool") return;
      const ts = g.partTs?.[i];
      const ms = ts ? Date.parse(ts) : NaN;
      if (!Number.isFinite(ms) || ms > at) return;
      if (namesRun(commandOf(p), r)) named = i;
      if (SHELL_TOOL.test(p.tool || "")) shell = i;
    });
    const i = named >= 0 ? named : shell;
    if (i < 0) {
      loose.push(r);
      continue;
    }
    const k = toolKey(g.idx, i);
    const list = onTool.get(k);
    if (list) list.push(r);
    else onTool.set(k, [r]);
  }
  return { onTool, loose };
}
