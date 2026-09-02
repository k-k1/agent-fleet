// English カタログ / ドメイン: sharing
// キー接頭辞: share
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。Record<keyof typeof ja...> で ja に無いキー / 足りないキーを tsc が落とす。
import type { sharing as jaSharing } from "../ja/sharing.ts";

export const sharing: Record<keyof typeof jaSharing, string> = {
  "share.shared_sessions": "Shared sessions",
  "share.list_title": "Shares",
  "share.reload": "Refresh",
  "share.create_title": "Share",
  "share.new": "New share…",
  "share.no_shares": "You haven't shared anything yet.",
  "share.unshare_confirm_title": "Unshare?",
  "share.unshare_confirm_body": "The recipient will lose access to this session/repo. Any copy they already saved cannot be recalled.",
  "share.owner_stopped": "The owner's workspace is stopped. History is available only while it is running.",
  "share.no_access": "You no longer have access to this shared session.",
  "share.load_failed": "Could not load shared history.",
  "share.user": "User",
  "share.assistant": "Assistant",
  "share.proposal_placeholder": "Propose a message to the owner…",
  "share.propose": "Propose to owner",
  "share.owner_approval_note": "Nothing is sent to the agent until the owner reviews and approves it.",
  "share.handoff_intro":
    "This session proposed the first prompt for a successor session. Only the owner can edit it or launch from it.",
  "share.proposal_failed": "Could not send the proposal.",
  "share.proposal_sent": "Proposal sent to the owner.",
  "share.pending": "{count} pending approval(s)",
  "share.pending_title": "Shared-session approvals",
  "share.reject": "Reject",
  "share.reconcile": "Check result",
  "share.outcome_unknown": "The send result could not be confirmed. It will not be retried automatically; you can check the recorded result.",
  "share.approve": "Approve and send",
  "share.session_scope": "Session",
  "share.repo_scope": "Project",
  "share.repo_scope_hint": "Sessions in the base working copy AND in its linked worktrees are shared.",
  "share.worktree_scope": "WT",
  "share.target": "Share target",
  "share.recipient": "Recipient",
  "share.recipient_search_ph": "Search by email…",
  "share.recipient_no_match": "No matching member found",
  "share.recipient_change": "Change",
  "share.permission": "Permission",
  "share.approval_required": "owner approval required",
  "share.permission_ro": "View only",
  "share.permission_rw": "Can propose",
  "share.create": "Share",
  "share.unshare": "Unshare",
  "share.save_failed": "Could not save the share.",
  "share.exposure_warning": "This shares the full conversation, including prompts, agent replies, and tool output. Secrets in a conversation cannot be detected reliably. Recipients can save what they view, and those copies cannot be recalled after unsharing. RW input reaches the agent only after owner approval.",
  "share.marks_warning": "Highlights drawn on the conversation are shared too. An RW recipient can draw their own, and those appear without approval (they never reach the agent). The login id of whoever drew a highlight is shown to you and to every other recipient.",
};
