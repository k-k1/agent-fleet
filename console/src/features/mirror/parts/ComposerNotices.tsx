import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";

// コンポーサーが「使えない」5 つの状態。どれも入力欄の代わりに同じ帯
// （.mirror-compose.mirror-compose-resume）へ理由と、あれば次の一手を出す。分岐そのものは
// MirrorView が持つ（順序に意味がある — 下の各コメントを参照）。

/** 作業ディレクトリが消えた履歴。再開の導線は出さない（BuildLaunch が拒む）。 */
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

/** 履歴（未アタッチ）。ボタンは背後で再開し、alive になった時点でコンポーサーが生きる。 */
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

/** Workspace 停止中。送っても 502 になるだけなので、履歴として枠を出す。 */
export function WsStoppedNotice() {
  return (
    <div className="mirror-compose mirror-compose-resume">
      <span className="muted mirror-resume-hint">
        <Icon name="circle-slash" /> {tr("mirror.viewing_history_ws_stopped")}
      </span>
    </div>
  );
}

/** 端末に再開メニューが出ている。打鍵はメニューへ行くので、選びに行かせる。 */
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

/** codex の更新メニューが出ている。数字キー 1 つで選択＋確定なので、見送りの 2 択を直に出す。 */
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

/** アタッチ済みだがセッションがまだ立ち上がっていない（再開の途中）。 */
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
