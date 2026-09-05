import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { Trans } from "../../../lib/i18n/Trans.tsx";

/**
 * Status bands stacked above the transcript. None of them is part of the conversation: each is a
 * one-liner saying what the terminal is doing and what the user can do about it. Their conditions
 * are not mutually exclusive, so they simply stack vertically.
 */
export function MirrorBanners({
  isPlan,
  termState,
  compactProg,
  suggestedTitle,
  titleActing,
  onOpenTerminal,
  onSkipUpdate,
  onAcceptTitle,
  onDismissTitle,
}: {
  isPlan: boolean;
  termState: string;
  compactProg: { pct: number; elapsed?: string } | null;
  suggestedTitle: string;
  titleActing: boolean;
  onOpenTerminal: () => void;
  onSkipUpdate: () => void;
  onAcceptTitle: () => void;
  onDismissTitle: () => void;
}) {
  return (
    <>
      {isPlan && (
        <div className="mirror-planmode">
          <Icon name="debug-pause" /> {tr("mirror.plan_mode_note")}
        </div>
      )}
      {termState === "resume" && (
        // The startup resume menu is showing in the terminal (invisible from chat) —
        // prompt the user to go choose. "2. Resume full session as-is" keeps the full
        // context; the recommended summary option would drop it.
        <div className="mirror-attention">
          <Icon name="warning" />
          <span className="ma-text">{tr("mirror.resume_choice_note")}</span>
          <button type="button" className="btn primary ma-btn" onClick={onOpenTerminal}>
            <Icon name="terminal" /> {tr("mirror.open_terminal")}
          </button>
        </div>
      )}
      {termState === "update" && (
        // codex's startup update menu is showing in the terminal (invisible from chat).
        // "1. Update now" exits the process and the tmux session dies with it — CLI
        // updates belong to the image pin — so the offered action is skip. The digit
        // key alone selects and confirms (verified on 0.144.3), hence a single "2".
        <div className="mirror-attention">
          <Icon name="warning" />
          <span className="ma-text">{tr("mirror.codex_update_note")}</span>
          <button type="button" className="btn primary ma-btn" onClick={onSkipUpdate}>
            {tr("mirror.skip_continue")}
          </button>
        </div>
      )}
      {termState === "compacting" && (
        <div className="mirror-compacting">
          <div className="mc-head">
            <Icon name="loading" spin /> {tr("mirror.compacting")}
            {compactProg?.elapsed && <span className="mc-elapsed">{compactProg.elapsed}</span>}
            {compactProg && compactProg.pct >= 0 && <span className="mc-pct">{compactProg.pct}%</span>}
          </div>
          {compactProg && compactProg.pct >= 0 && (
            <div className="mc-track" role="progressbar" aria-valuenow={compactProg.pct} aria-valuemin={0} aria-valuemax={100}>
              <div className="mc-fill" style={{ width: compactProg.pct + "%" }} />
            </div>
          )}
        </div>
      )}
      {suggestedTitle && (
        <div className="mirror-title-suggest">
          <Icon name="lightbulb" />
          <span className="mts-text">
            <Trans k="mirror.title_suggestion" vars={{ title: suggestedTitle }} components={[<strong />]} />
          </span>
          <button type="button" className="btn primary mts-btn" disabled={titleActing} onClick={onAcceptTitle}>
            <Icon name={titleActing ? "loading" : "check"} spin={titleActing} /> {tr("mirror.adopt")}
          </button>
          <button
            type="button"
            className="icon mts-dismiss"
            disabled={titleActing}
            onClick={onDismissTitle}
            title={tr("mirror.dismiss_suggestion")}
          >
            <Icon name="close" />
          </button>
        </div>
      )}
    </>
  );
}
