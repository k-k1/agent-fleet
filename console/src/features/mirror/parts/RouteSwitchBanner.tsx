// RouteSwitchBanner — the switch to the metered route offered where the window stop is felt
// (ADR 0128 decision 10, B1). A claude session stopped at its subscription's usage window either
// waits for the reset or goes on at once on the API route, which bills per token.
//
// Nothing here decides eligibility: the Agent's preview does, and the offer draws only when it
// says `allowed`. The POST runs the same checks again under the session's lock, so every refusal
// code is still mapped to text (err.route_switch_*) for the answer that arrives after the state
// changed. The band outlives the limit it was offered for: after a switch the session is no
// longer "limited", and the outcome (rolled back, resume cancel still pending, carry-on not
// sent) has to stay on screen until the member closes it.
import { useEffect, useState } from "react";
import { create } from "zustand";
import { api, apiJSON, errDetail, type ApiError } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr, useT } from "../../../lib/i18n/index.ts";
import { resumeClock } from "../../../lib/sessionview.ts";
import type { Session } from "../../../types/session.ts";

/** GET /api/sessions/{name}/route-switch: what a switch would do now (wire: routeSwitchPreview). */
export interface RouteSwitchPreview {
  allowed: boolean;
  from?: string;
  to?: string;
  code?: string;
  reason?: string;
  mayCostMore?: boolean;
  cacheNotShared?: boolean;
  accountSettingUrl?: string;
  accountSettingAffectsAll?: boolean;
  pending?: boolean;
}

/** A window stop: the one state a wait clears. A spend limit (spend_limit) is never offered. */
export const isWindowStop = (state?: string): boolean => state === "limited";

/** How often an open offer re-reads the preview; a limit lasts hours, so this is slow. */
const REFRESH_MS = 60_000;

/** The account setting is a link the member opens: only an https URL is ever rendered as one. */
export const safeSettingUrl = (u?: string): string | null => {
  try {
    const p = new URL(u ?? "");
    return p.protocol === "https:" ? p.href : null;
  } catch {
    return null;
  }
};

// The codes after which the Agent has proven it changed nothing: it refused before it stopped
// anything. Any other answer (route_switch_failed, a gateway timeout, a lost response, a code
// this Console does not know) leaves the outcome unproven, because the Agent keeps going after
// the request that started the switch is gone.
const NOTHING_CHANGED = new Set([
  "route_switch_unsupported",
  "route_switch_not_subscription",
  "route_switch_route_unproven",
  "route_switch_no_limit",
  "route_switch_spend_limit",
  "route_switch_state",
  "route_switch_busy",
  "route_switch_booking",
]);

type Phase =
  | { k: "idle" }
  | { k: "confirm" }
  | { k: "switching" }
  | { k: "done"; cancelPending: boolean; carried: boolean }
  | { k: "refused"; text: string }
  | { k: "incomplete"; text: string }
  | { k: "unknown"; text: string };

// One entry per session, outside React: the switch and the carry-on that follows it are the
// member's confirmed action and must finish when the mirror is closed or changed meanwhile, and
// their outcome has to be there when the session is shown again.
const useOutcomes = create<{ by: Record<string, Phase> }>(() => ({ by: {} }));
const setPhaseOf = (name: string, p: Phase) => useOutcomes.setState((s) => ({ by: { ...s.by, [name]: p } }));
// A constant: a selector that built a new object each read would never settle.
const IDLE: Phase = { k: "idle" };
const phaseOf = (by: Record<string, Phase>, name: string): Phase => by[name] ?? IDLE;

async function runSwitch(name: string): Promise<void> {
  setPhaseOf(name, { k: "switching" });
  const res = await apiJSON(`api/sessions/${encodeURIComponent(name)}/route-switch`, "POST", {}).catch(
    () => ({ error: { code: "network", message: tr("err.network") } as ApiError }),
  );
  if (res?.error) {
    const err = res.error as ApiError;
    const text = errDetail(err);
    setPhaseOf(
      name,
      NOTHING_CHANGED.has(err.code ?? "")
        ? { k: "refused", text }
        : err.code === "route_switch_failed"
          ? { k: "incomplete", text }
          : { k: "unknown", text },
    );
    return;
  }
  // The switch only relaunches the session: nothing is typed into it. The carry-on goes as the
  // member's own message, so it lands on the new route like any turn. The Agent answers 202
  // "queued": acceptance, not delivery.
  const sent = await apiJSON(`api/sessions/${encodeURIComponent(name)}/input`, "POST", {
    prompt: tr("mirror.route_switch_carry_on"),
    when_ready: true,
  }).catch(() => null);
  setPhaseOf(name, { k: "done", cancelPending: !!res.bookingCancelPending, carried: !!sent && !sent.error });
}

export function RouteSwitchBanner({ session, state }: { session?: Session | null; state?: string }) {
  const name = session?.name ?? "";
  // The kind is not judged here (the Agent's preview refuses a kind with no metered route, as
  // route_switch_unsupported); a moving or moved source is not offered anything.
  const eligible = !!session && !session.moving && !session.movedTo;
  // Keyed by the session so another session's preview never speaks for this one.
  return eligible ? <Band key={name} session={session!} state={state} /> : null;
}

function Band({ session, state }: { session: Session; state?: string }) {
  const tr_ = useT();
  const name = session.name;
  const phase = useOutcomes((s) => phaseOf(s.by, name));
  // What a preview is evidence for: this limit episode on this route. It changes when the limit
  // lifts or returns, when the booked reset moves, when the route does, and each time the member
  // acts, so an answer read before any of those is never shown after it.
  const [gen, setGen] = useState(0);
  const limited = isWindowStop(state);
  const scope = `${gen}|${limited}|${session.rateLimitResumeAt ?? ""}|${session.route?.route ?? ""}|${session.route?.connection ?? ""}`;
  const [read, setRead] = useState<{ scope: string; preview: RouteSwitchPreview | null } | null>(null);
  const preview = read && read.scope === scope ? read.preview : null;
  const [tick, setTick] = useState(0);
  const watching = limited && (phase.k === "idle" || phase.k === "confirm");

  // Leaving the limit ends the episode its preview was evidence for.
  useEffect(() => {
    if (!limited) setRead(null);
  }, [limited]);
  useEffect(() => {
    if (!watching) return;
    const id = setInterval(() => setTick((n) => n + 1), REFRESH_MS);
    return () => clearInterval(id);
  }, [watching]);
  useEffect(() => {
    if (!watching) return;
    let live = true;
    api(`api/sessions/${encodeURIComponent(name)}/route-switch`)
      .then((d) => live && setRead({ scope, preview: d && !d.error ? (d as RouteSwitchPreview) : null }))
      .catch(() => live && setRead({ scope, preview: null }));
    return () => {
      live = false;
    };
  }, [name, watching, tick, scope]);

  const setPhase = (p: Phase) => setPhaseOf(name, p);
  const run = () => {
    setGen((n) => n + 1);
    void runSwitch(name);
  };
  const close = () => {
    setGen((n) => n + 1);
    setPhase({ k: "idle" });
  };

  if (phase.k === "switching") {
    return (
      <div className="mirror-attention mirror-route-switch" role="status" aria-busy="true">
        <Icon name="arrow-swap" />
        <span className="ma-text">{tr_("mirror.route_switch_progress")}</span>
      </div>
    );
  }
  if (phase.k === "done" || phase.k === "refused" || phase.k === "incomplete" || phase.k === "unknown") {
    return (
      <div className="mirror-attention mirror-route-switch" role="status">
        <Icon name="arrow-swap" />
        <span className="ma-text">
          {phase.k === "done" ? (
            <>
              {tr_("mirror.route_switch_done")}{" "}
              {phase.carried ? tr_("mirror.route_switch_carry_queued") : tr_("mirror.route_switch_carry_failed")}
              {phase.cancelPending && " " + tr_("mirror.route_switch_cancel_pending")}
            </>
          ) : phase.k === "incomplete" ? (
            tr_("mirror.route_switch_incomplete", { detail: phase.text })
          ) : phase.k === "unknown" ? (
            tr_("mirror.route_switch_unknown", { detail: phase.text })
          ) : (
            tr_("mirror.route_switch_refused", { detail: phase.text })
          )}
        </span>
        <button type="button" className="btn ma-btn" onClick={close}>
          {tr_("mirror.route_switch_close")}
        </button>
      </div>
    );
  }

  // Nothing to offer: not a window stop, or the Agent says a switch would be refused (or an
  // earlier one is still settling, which the next read reports).
  if (!limited || !preview?.allowed || preview.pending) return null;
  const link = safeSettingUrl(preview.accountSettingUrl);
  const at = resumeClock(session.rateLimitResumeAt);

  if (phase.k === "confirm") {
    return (
      <div className="mirror-attention mirror-route-switch rs-confirm" role="group" aria-label={tr_("mirror.route_switch_confirm_title")}>
        <div className="ma-text">
          <strong>{tr_("mirror.route_switch_confirm_title")}</strong>
          {preview.mayCostMore && <p className="rs-cost">{tr_("mirror.route_switch_cost")}</p>}
          {preview.cacheNotShared && <p className="rs-cache">{tr_("mirror.route_switch_cache")}</p>}
          {link && (
            <p className="rs-credits">
              {tr_("mirror.route_switch_credits")}{" "}
              <a href={link} target="_blank" rel="noopener noreferrer">
                {tr_("mirror.route_switch_credits_link")}
              </a>
              {preview.accountSettingAffectsAll && " " + tr_("mirror.route_switch_credits_all")}
            </p>
          )}
        </div>
        <button type="button" className="btn primary ma-btn" onClick={run}>
          {tr_("mirror.route_switch_do")}
        </button>
        <button type="button" className="btn ma-btn" onClick={() => setPhase({ k: "idle" })}>
          {tr_("mirror.route_switch_cancel")}
        </button>
      </div>
    );
  }

  return (
    <div className="mirror-attention mirror-route-switch" role="status">
      <Icon name="arrow-swap" />
      <span className="ma-text">
        {at ? tr_("mirror.route_switch_offer_at", { at }) : tr_("mirror.route_switch_offer")}
      </span>
      <button type="button" className="btn primary ma-btn" onClick={() => setPhase({ k: "confirm" })}>
        {tr_("mirror.route_switch_action")}
      </button>
    </div>
  );
}

/** Forgets every session's outcome; for tests, which share this module's store. */
export const resetRouteSwitchOutcomes = (): void => useOutcomes.setState({ by: {} });
