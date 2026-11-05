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
import { useEffect, useRef, useState } from "react";
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

type Phase =
  | { k: "idle" }
  | { k: "confirm" }
  | { k: "switching" }
  | { k: "done"; cancelPending: boolean; carried: boolean }
  // refused: the Agent changed nothing. rolledBack: it started and undid itself.
  | { k: "failed"; rolledBack: boolean; text: string };

export function RouteSwitchBanner({ session, state }: { session?: Session | null; state?: string }) {
  const name = session?.name ?? "";
  // The kind is not judged here (the Agent's preview refuses a kind with no metered route, as
  // route_switch_unsupported); a moving or moved source is not offered anything.
  const eligible = !!session && !session.moving && !session.movedTo;
  // Keyed by the session so another session's preview and outcome never speak for this one.
  return eligible ? <Band key={name} session={session!} state={state} /> : null;
}

function Band({ session, state }: { session: Session; state?: string }) {
  const tr_ = useT();
  const name = session.name;
  const [preview, setPreview] = useState<RouteSwitchPreview | null>(null);
  const [phase, setPhase] = useState<Phase>({ k: "idle" });
  const [tick, setTick] = useState(0);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const limited = isWindowStop(state);
  const watching = limited && (phase.k === "idle" || phase.k === "confirm");

  useEffect(() => {
    if (!watching) return;
    const id = setInterval(() => setTick((n) => n + 1), REFRESH_MS);
    return () => clearInterval(id);
  }, [watching]);
  useEffect(() => {
    if (!watching) return;
    let live = true;
    api(`api/sessions/${encodeURIComponent(name)}/route-switch`)
      .then((d) => live && setPreview(d && !d.error ? (d as RouteSwitchPreview) : null))
      .catch(() => live && setPreview(null));
    return () => {
      live = false;
    };
  }, [name, watching, tick]);

  const run = async () => {
    setPhase({ k: "switching" });
    const res = await apiJSON(`api/sessions/${encodeURIComponent(name)}/route-switch`, "POST", {}).catch(
      () => ({ error: { code: "network", message: tr("err.network") } as ApiError }),
    );
    if (!alive.current) return;
    if (res?.error) {
      const code = (res.error as ApiError).code;
      setPhase({ k: "failed", rolledBack: code === "route_switch_failed", text: errDetail(res.error as ApiError) });
      return;
    }
    // The switch only relaunches the session: nothing is typed into it. The carry-on goes as the
    // member's own message, so it lands on the new route like any turn.
    const sent = await apiJSON(`api/sessions/${encodeURIComponent(name)}/input`, "POST", {
      prompt: tr("mirror.route_switch_carry_on"),
      when_ready: true,
    }).catch(() => null);
    if (!alive.current) return;
    setPhase({ k: "done", cancelPending: !!res.bookingCancelPending, carried: !!sent && !sent.error });
  };

  if (phase.k === "switching") {
    return (
      <div className="mirror-attention mirror-route-switch" role="status" aria-busy="true">
        <Icon name="arrow-swap" />
        <span className="ma-text">{tr_("mirror.route_switch_progress")}</span>
      </div>
    );
  }
  if (phase.k === "done" || phase.k === "failed") {
    const close = () => {
      setPhase({ k: "idle" });
      setTick((n) => n + 1);
    };
    return (
      <div className="mirror-attention mirror-route-switch" role="status">
        <Icon name="arrow-swap" />
        <span className="ma-text">
          {phase.k === "done" ? (
            <>
              {tr_("mirror.route_switch_done")}
              {!phase.carried && " " + tr_("mirror.route_switch_carry_failed")}
              {phase.cancelPending && " " + tr_("mirror.route_switch_cancel_pending")}
            </>
          ) : phase.rolledBack ? (
            tr_("mirror.route_switch_rolled_back", { detail: phase.text })
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
        <button type="button" className="btn primary ma-btn" onClick={() => void run()}>
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
