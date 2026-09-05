import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";

// The five states in which the composer is unusable. Each replaces the input with the same band
// (.mirror-compose.mirror-compose-resume) carrying the reason and, where there is one, the next
// step. MirrorView owns the branching itself, and its order matters; see the notes below.

/** History whose working directory is gone. No resume path is offered: BuildLaunch refuses it. */
export function DirGoneNotice() {
  return (
    // Dir removed: no resume path, so drop the button and just say so — the
    // history above is fully readable, it simply can't be continued.
    <div className="mirror-compose mirror-compose-resume">
      <span className="muted mirror-resume-hint">
        <Icon name="circle-slash" /> {tr("mirror.folder_missing_history")}
      </span>
    </div>
  );
}

/** History (not attached). The button resumes in the background; the composer comes alive once
 *  the session is. */
export function ResumeNotice({ running, onResume }: { running: boolean; onResume: () => void }) {
  return (
    // History (read-only): the session isn't attached, so input is disabled. The
    // button attaches (resumes) in the background while keeping this chat open —
    // the composer enables once the session is live (alive from the poll).
    <div className="mirror-compose mirror-compose-resume">
      <button
        type="button"
        className="btn primary mirror-resume"
        disabled={!running}
        title={running ? tr("mirror.resume_session") : tr("mirror.ws_stopped")}
        onClick={onResume}
      >
        <Icon name="play" /> {tr("mirror.resume_continue")}
      </button>
      <span className="muted mirror-resume-hint">
        {running ? tr("mirror.viewing_history_resume") : tr("mirror.viewing_history_ws_stopped")}
      </span>
    </div>
  );
}

/** Workspace stopped. Sending would only 502, so the chat is framed as history. */
export function WsStoppedNotice() {
  return (
    <div className="mirror-compose mirror-compose-resume">
      <span className="muted mirror-resume-hint">
        <Icon name="circle-slash" /> {tr("mirror.viewing_history_ws_stopped")}
      </span>
    </div>
  );
}

/** The resume menu is up in the terminal. Keystrokes go to the menu, so send the user there. */
export function TerminalResumeNotice({ onOpenTerminal }: { onOpenTerminal: () => void }) {
  return (
    // Resume menu is up in the terminal: block the composer (keystrokes would go to
    // the menu) and send the user there to choose.
    <div className="mirror-compose mirror-compose-resume">
      <button type="button" className="btn primary mirror-resume" onClick={onOpenTerminal}>
        <Icon name="terminal" /> {tr("mirror.select_in_terminal")}
      </button>
      <span className="muted mirror-resume-hint">{tr("mirror.resume_choice_hint")}</span>
    </div>
  );
}

/** codex's update menu is up. A single digit key both selects and confirms, so the two skip
 *  choices are offered directly. */
export function TerminalUpdateNotice({ onSkip, onSkipUntilNext }: { onSkip: () => void; onSkipUntilNext: () => void }) {
  return (
    // codex's update menu is up: block the composer (typed digits would pick menu
    // entries) and offer the two skip choices directly — each digit key selects and
    // confirms on its own, so one key dismisses the menu.
    <div className="mirror-compose mirror-compose-resume">
      <button type="button" className="btn primary mirror-resume" onClick={onSkip}>
        {tr("mirror.skip_continue")}
      </button>
      <button type="button" className="btn mirror-resume" onClick={onSkipUntilNext}>
        {tr("mirror.skip_until_next")}
      </button>
      <span className="muted mirror-resume-hint">{tr("mirror.update_choice_hint")}</span>
    </div>
  );
}

/** Attached, but the session is still coming up (resume in flight). */
export function ResumingNotice() {
  return (
    // Attached but the session is still coming up (resume in flight).
    <div className="mirror-compose mirror-compose-resume">
      <span className="muted mirror-resuming">
        <Icon name="loading" spin /> {tr("mirror.resuming")}
      </span>
    </div>
  );
}
