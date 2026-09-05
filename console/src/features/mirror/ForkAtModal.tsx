// ForkAtModal — the confirmation dialog for "fork from here" (docs/log/55). It starts a new
// session from a past user message in the mirror, carrying the context up to that point.
//
// This is not a handoff (HandoffModal), and the body says so outright because that is the
// distinction users get wrong: a handoff has an LLM summarize the conversation for another
// agent, a fork duplicates the conversation as-is on the same agent. The body also states
// that the original survives — if a fork looks destructive, nobody presses it in the
// situation it exists for (right after a wrong turn).
import { useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { apiJSON, errText } from "../../core/api/client.ts";
import type { ApiError } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";

// Preview of the fork point shown in the dialog. The full text is readable in the mirror, so
// enough to identify the message is enough.
const PREVIEW_CHARS = 240;

export interface ForkAtTarget {
  anchorId: string;
  text: string;
  // Number of user messages in this conversation carried into the fork (the fork point
  // itself is not counted).
  carried: number;
}

// The two ways to fork. They differ by one round trip only, so the choice lives inside the
// modal.
//  redo     … up to just before this message (i.e. retype it). The default.
//  continue … carry this message and the reply it got (i.e. continue in another direction).
type ForkMode = "redo" | "continue";

export function ForkAtModal({
  session,
  target,
  onDone,
  onClose,
}: {
  session: string;
  target: ForkAtTarget;
  // On a successful fork, hands over the new session name and the draft; opening the pane
  // and deciding what to do with the draft is the caller's job.
  onDone: (name: string, opts: { draft: string }) => void;
  onClose: () => void;
}) {
  const tr = useT();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [mode, setMode] = useState<ForkMode>("redo");

  const preview = target.text.length > PREVIEW_CHARS ? target.text.slice(0, PREVIEW_CHARS) + "…" : target.text;

  const run = async () => {
    if (busy) return;
    setBusy(true);
    setErr("");
    try {
      const d = await apiJSON(`api/sessions/${encodeURIComponent(session)}/fork`, "POST", {
        at: target.anchorId,
        include: mode === "continue",
      });
      // api() does not throw on failure; it returns {error:{code,message}} (client.ts).
      // Without checking that first, every server-supplied reason (unusable fork point,
      // a limit, the execution method) collapses into the generic message below.
      if (d?.error) throw d.error as ApiError;
      if (!d?.name) throw new Error("no session in fork response");
      // Only "redo" seeds the draft. In "continue" the message is still in the forked
      // conversation, so the same text in the composer would read as a duplicate.
      onDone(d.name as string, { draft: mode === "redo" ? target.text : "" });
      onClose();
    } catch (e) {
      // Stay open on failure: the fork point may just be stale (reloading the mirror fixes
      // it) or the path may not support forking at all (fork_at_unsupported), so show the
      // reason and let the reader decide.
      setErr(errText(e as ApiError) || tr("mirror.fork_at_failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={tr("mirror.fork_at_title")} onClose={onClose} lockClose={busy}>
      <div className="ui-modal-body">
        <div className="ui-field-hint">{tr("mirror.fork_at_intro")}</div>

        <div className="ui-field">
          <span className="ui-field-label">{tr("mirror.fork_at_point")}</span>
          <blockquote className="mirror-fork-preview">{preview}</blockquote>
        </div>

        <div className="ui-field">
          <span className="ui-field-label">{tr("mirror.fork_at_mode")}</span>
          {/* The two modes differ by one round trip, so offer them side by side. "redo" is
              the default: the commonest use is right after a wrong turn, where the fork
              point's own message should go too. */}
          <div className="ui-seg big" role="radiogroup" aria-label={tr("mirror.fork_at_mode")}>
            {(["redo", "continue"] as const).map((k) => (
              <button
                key={k}
                type="button"
                role="radio"
                aria-checked={mode === k}
                className={"seg-btn" + (mode === k ? " active" : "")}
                onClick={() => setMode(k)}
                disabled={busy}
              >
                {tr(k === "redo" ? "mirror.fork_at_mode_redo" : "mirror.fork_at_mode_continue")}
                <span className="seg-sub">
                  {tr(k === "redo" ? "mirror.fork_at_mode_redo_hint" : "mirror.fork_at_mode_continue_hint")}
                </span>
              </button>
            ))}
          </div>
        </div>

        <ul className="mirror-fork-facts">
          <li>
            {mode === "redo"
              ? tr("mirror.fork_at_carried", { count: String(target.carried) })
              : tr("mirror.fork_at_carried_incl", { count: String(target.carried + 1) })}
          </li>
          <li>{tr("mirror.fork_at_keeps_source")}</li>
          {mode === "redo" && <li>{tr("mirror.fork_at_draft")}</li>}
        </ul>

        {err && (
          <div className="managed-settings-error" role="alert">
            <Icon name="warning" /> {err}
          </div>
        )}
      </div>

      <footer className="ui-modal-foot">
        <button type="button" className="ui-btn ui-btn-ghost" onClick={onClose} disabled={busy}>
          {tr("common.cancel")}
        </button>
        <button type="button" className="ui-btn ui-btn-primary" onClick={() => void run()} disabled={busy}>
          {tr("mirror.fork_at_go")}
        </button>
      </footer>
    </Modal>
  );
}
