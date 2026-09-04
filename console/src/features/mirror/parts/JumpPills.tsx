import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";

/**
 * Pills floating just above the input. They must be the LAST child of the body: sticky with a
 * bottom offset only holds an element up when it would otherwise fall below its natural
 * position, so placing them first strands them at the top (measured: 42,000px above the body).
 *
 * The wrapper is height:0 and the buttons are absolute inside it. Left in flow, the overflowing
 * button (measured 12px) extends the scrollable area, leaving a 12px gap even when scrolled to
 * the very end. With absolute at bottom:0 the button box grows upward from the wrapper and never
 * overflows past the end.
 *
 * The jump-to-reply-top pill points the other way and has its own condition (the top of the
 * latest reply is above the viewport). Both sit in the same row so that when both apply (reading
 * mid-reply while scrolled away from the end) the up and down choices appear side by side.
 */
export function JumpPills({
  showJump,
  showReplyTop,
  onJumpBottom,
  onJumpReplyTop,
}: {
  showJump: boolean;
  showReplyTop: boolean;
  onJumpBottom: () => void;
  onJumpReplyTop: () => void;
}) {
  if (!showJump && !showReplyTop) return null;
  return (
    <div className="mirror-jump-wrap">
      <div className="mirror-jump-row">
        {showReplyTop && (
          <button
            type="button"
            // Same pill as jump-to-latest; the extra class exists for verification. The
            // mirror-scroll harness decides "landed at the end" by the absence of the
            // jump-to-latest pill, and could not tell two bare .mirror-jump elements apart.
            className="mirror-jump mirror-jump-top"
            onClick={onJumpReplyTop}
            title={tr("mirror.jump_reply_top")}
            aria-label={tr("mirror.jump_reply_top")}
          >
            <Icon name="arrow-up" /> {tr("mirror.jump_reply_top")}
          </button>
        )}
        {showJump && (
          <button
            type="button"
            className="mirror-jump"
            onClick={onJumpBottom}
            title={tr("mirror.jump_latest")}
            aria-label={tr("mirror.jump_latest")}
          >
            <Icon name="arrow-down" /> {tr("mirror.jump_latest")}
          </button>
        )}
      </div>
    </div>
  );
}
