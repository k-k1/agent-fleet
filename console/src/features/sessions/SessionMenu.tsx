// SessionMenu — the session context menu itself, lifted out of SessionRow so the
// SAME items appear wherever a session is shown: the left-pane row's ⋯ /
// right-click, and a right-click on that session's tab in the tabbed grid. The
// caller owns the open state and the placement (a row anchors under its ⋯
// button and clamps inside the rail; a tab opens at the cursor), because those
// are the only two things that actually differ.
//
// It stays mounted while closed on purpose: the 引き継ぎ / 共有 dialogs it owns
// outlive the menu, and unmounting on close would take them down with it.
import { createPortal } from "react-dom";
import { useLayoutEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { copyText } from "../../lib/clipboard.ts";
import { sessionFolder } from "../../lib/project.ts";
import { useSettings } from "../../lib/settings.ts";
import { workingSetList, toggleWorkingSetMember } from "../../lib/workingSetsStore.ts";
import { useReposStore } from "../repos/store.ts";
import { useT } from "../../lib/i18n/index.ts";
import { displayName, remainingShort, KEEP_AWAKE_HOURS } from "../../lib/sessionview.ts";
import { agentOf } from "../../agents/registry.ts";
import { openSessionTerminal, openSessionChat } from "./open.ts";
import { writeLS } from "../mirror/transcript/blocks.tsx";
import { useSessionUI } from "./ui.ts";
import { useSessionsStore } from "./store.ts";
import { HandoffModal } from "./HandoffModal.tsx";
import { ShareCreateModal } from "../sharing/ShareCreateModal.tsx";
import type { SessionActions } from "./useSessionActions.tsx";
import type { Session } from "../../types/session.ts";

interface SessionMenuProps {
  s: Session;
  actions: SessionActions;
  /** The workspace agent is reachable — resume/handoff/recreate need it. */
  running: boolean;
  open: boolean;
  /** Position the (position:fixed) menu element; runs on every render while open. */
  place: (el: HTMLElement) => void;
  /** Containers whose mousedown must NOT dismiss (e.g. the ⋯ toggle's wrapper). */
  keepOpenRefs?: Array<RefObject<HTMLElement | null>>;
  onClose: () => void;
}

export function SessionMenu({ s, actions, running, open, place, keepOpenRefs, onClose }: SessionMenuProps) {
  const openRename = useSessionUI((u) => u.openRename);
  const openBranchRename = useSessionUI((u) => u.openBranchRename);
  const openSsmResume = useSessionUI((u) => u.openSsmResume);
  const startSession = useSessionsStore((st) => st.start);
  const toast = useToast();
  const tr = useT();
  const menuElRef = useRef<HTMLDivElement>(null);
  const [handoffOpen, setHandoffOpen] = useState(false);
  const [shareOpen, setShareOpen] = useState(false);
  useDismiss([menuElRef, ...(keepOpenRefs ?? [])], open, onClose);
  useMenuRoving(menuElRef, open);
  // The dropdown is position:fixed and re-placed every render — a row near the
  // rail's foot must not push its menu below the viewport (the old absolute
  // top:26px did), and the width the caller aligns against is only known once
  // the items are laid out.
  useLayoutEffect(() => {
    const el = menuElRef.current;
    if (!open || !el) return;
    place(el);
  });
  // useMenuRoving pulls focus into the menu on open, so closing it destroys the
  // focused element and focus falls to <body> — after a keyboard open (メニューキー
  // → Esc, or activating an item) the user is left nowhere to arrow from. Hand focus
  // back to whatever opened the menu, but only when the menu really still had it: a
  // click that landed on some other control must keep the focus the user chose.
  //
  // A LAYOUT effect on purpose. useMenuRoving focuses the first item from a passive
  // effect, so a passive capture here would read the menu item as "the opener" —
  // layout effects run first, before anything has moved focus.
  useLayoutEffect(() => {
    if (!open) return;
    const opener = document.activeElement as HTMLElement | null;
    if (!opener || opener === document.body) return; // opened by mouse — nothing to give back
    return () => {
      const now = document.activeElement;
      if ((!now || now === document.body) && opener.isConnected) opener.focus();
    };
  }, [open]);
  // The session's immutable id (e.g. "sk7f3q9") — the thing shown as ID in the
  // row tooltip. The menu label shows the concrete value, not jargon ("slug").
  const copyId = () => {
    void copyText(s.name).then((ok) =>
      ok ? toast(tr("srow.id_copied", { name: s.name }), { kind: "success" }) : toast(tr("common.copy_failed")),
    );
  };

  const dead = !s.alive && s.resumable === false; // dir gone → can't resume
  // 作業グループ (docs/log/52): direct assignment is for repo-less sessions only —
  // a session living in a working copy inherits that repo's membership instead.
  const wsets = workingSetList(useSettings());
  const repos = useReposStore((st) => st.repos);
  const folder = sessionFolder(s);
  const repoLess = !folder || !repos.some((r) => r.name === folder);

  return (
    <>
      {open &&
        createPortal(
          <div className="ui-menu sess-menu" ref={menuElRef} onMouseDown={(e) => e.stopPropagation()}>
            {/* Resume — kinds with no in-chat resume. SSM resumes through the login
                modal (SSO handshake before attach). */}
            {!s.alive && !dead && running && !agentOf(s.kind).caps.chat && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  onClose();
                  if (s.kind === "ssm") openSsmResume(s.name, false);
                  else openSessionTerminal(s.name);
                }}
              >
                <Icon name="play" /> {tr("srow.resume")}
              </button>
            )}
            {/* Chat-capable kinds (claude/codex/opencode) have no in-menu resume today —
                opening the row shows the mirror read-only, and its own "再開して続ける"
                button is the only resume path. This mirrors that same POST /start then
                opens the chat pane directly. */}
            {!s.alive && !dead && running && agentOf(s.kind).caps.chat && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  onClose();
                  void startSession(s.name).then(() => openSessionChat(s.name));
                }}
              >
                <Icon name="play" /> {tr("srow.resume")}
              </button>
            )}
            {!s.alive && !dead && running && s.kind === "ssm" && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  onClose();
                  openSsmResume(s.name, true);
                }}
              >
                <Icon name="key" /> {tr("srow.relogin_resume")}
              </button>
            )}
            {s.alive && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  onClose();
                  void actions.halt(s.name, displayName(s));
                }}
              >
                <Icon name="debug-stop" /> {tr("srow.stop")}
              </button>
            )}
            {!dead && running && agentOf(s.kind).managedDriver && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  onClose();
                  void actions.switchDriver(s);
                }}
              >
                <Icon name={s.driver === "managed" ? "terminal" : "server-process"} />
                {s.driver === "managed" ? tr("sess.switch_to_tui") : tr("sess.switch_to_managed")}
              </button>
            )}
            {/* 変更ファイル（docs/log/68）— ミラーを開いて帯を広げる。専用のペイン種は
                作らない（ADR 0049 決定 4）ので、「覗く」導線はミラーを開くこと自体で、
                その開閉状態は帯が per-session に憶えている localStorage を先に書いて
                渡す。転写を持たない kind（shell/ssm）には一覧の材料が無い。 */}
            {agentOf(s.kind).caps.transcript && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  onClose();
                  writeLS("af.mirror-files-open." + s.name, "1");
                  openSessionChat(s.name);
                }}
              >
                <Icon name="edit" /> {tr("srow.changed_files")}
              </button>
            )}
            {s.remoteUrl && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  onClose();
                  window.open(s.remoteUrl, "_blank", "noopener");
                }}
              >
                <Icon name="link-external" /> {tr("srow.open_remote")}
              </button>
            )}
            <button
              type="button"
              className="ui-menu-item"
              onClick={() => {
                onClose();
                copyId();
              }}
            >
              <Icon name="copy" /> {tr("srow.copy_id", { name: s.name })}
            </button>
            <button
              type="button"
              className="ui-menu-item"
              onClick={() => {
                onClose();
                openRename(s);
              }}
            >
              <Icon name="edit" /> {tr("srow.rename")}
            </button>
            {/* Worktree sessions only: rename the worktree's branch (deferred
                naming); AI suggestion uses THIS session. */}
            {s.worktree && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  onClose();
                  openBranchRename(s);
                }}
              >
                <Icon name="repo-forked" /> {tr("srow.rename_branch")}
              </button>
            )}
            {/* Handoff to another agent — one unified item for every session that
                has a conversation (caps.transcript); the target is chosen in the
                modal, which then calls the operator assistant directly. */}
            {agentOf(s.kind).caps.transcript && !dead && running && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  onClose();
                  setHandoffOpen(true);
                }}
              >
                <Icon name="git-branch" /> {tr("srow.handoff")}
              </button>
            )}
            {/* セッション共有: 同一テナントの別ユーザーへ会話を共有する。shell/SSM は
                transcript が無く共有対象外(docs/log/59)。 */}
            {agentOf(s.kind).caps.transcript && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  onClose();
                  setShareOpen(true);
                }}
              >
                <Icon name="broadcast" /> {tr("srow.share")}
              </button>
            )}
            {/* 作業グループ (docs/log/52): membership toggles — repo-less rows only. */}
            {repoLess && wsets.length > 0 && (
              <>
                <div className="ui-menu-caption">{tr("wset.menu_caption")}</div>
                {wsets.map((w) => (
                  <button
                    key={w.id}
                    type="button"
                    className="ui-menu-item"
                    onClick={() => {
                      onClose();
                      toggleWorkingSetMember(w.id, "sessions", s.name);
                    }}
                  >
                    <Icon name="check" className={w.sessions.includes(s.name) ? "wset-check" : "wset-check off"} /> {w.name}
                  </button>
                ))}
              </>
            )}
            {/* 削除ロック（docs/log/45）: この行を削除保護に固定/解除する。保護の実体は
                Agent 側（403）なので、ここは切替と見た目の抑止だけを担う。 */}
            <button
              type="button"
              className="ui-menu-item"
              onClick={() => {
                onClose();
                void actions.setLocked(s, !s.locked);
              }}
            >
              <Icon name={s.locked ? "unlock" : "lock"} />{" "}
              {s.locked ? tr("srow.unlock") : tr("srow.lock")}
            </button>
            {/* 停止しないピン（docs/log/75）: アイドル自動停止からこのセッションと Workspace を
                期限付きで守る。shell / ssm では「ジョブが走っているか」を af が判定できない
                （放置された less と実行中のビルドが前景コマンド名で区別できず、ssm は常に
                aws を張る）ため、推測ではなく宣言に倒した逃げ道。 */}
            <button
              type="button"
              className="ui-menu-item"
              title={tr("srow.keep_awake_hint")}
              onClick={() => {
                onClose();
                void actions.setKeepAwake(s, remainingShort(s.keepAwakeUntil) ? 0 : KEEP_AWAKE_HOURS);
              }}
            >
              <Icon name="watch" />{" "}
              {remainingShort(s.keepAwakeUntil)
                ? tr("srow.keep_awake_off")
                : tr("srow.keep_awake_on", { hours: KEEP_AWAKE_HOURS })}
            </button>
            {agentOf(s.kind).caps.ephemeral ? (
              <button
                type="button"
                className="ui-menu-item danger"
                disabled={s.locked}
                title={s.locked ? tr("srow.locked_hint") : undefined}
                onClick={() => {
                  onClose();
                  void actions.deleteSession(s);
                }}
              >
                <Icon name="trash" /> {tr("common.delete_do")}
              </button>
            ) : (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  onClose();
                  void actions.archive(s);
                }}
              >
                <Icon name="archive" /> {tr("srow.archive")}
              </button>
            )}
            {!dead && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  onClose();
                  void actions.recreate(s.name, displayName(s));
                }}
              >
                <Icon name="refresh" /> {tr("srow.recreate")}
              </button>
            )}
          </div>,
          document.body,
        )}
      {handoffOpen && <HandoffModal session={s} actions={actions} onClose={() => setHandoffOpen(false)} />}
      {shareOpen && (
        <ShareCreateModal initialTarget={`session:${s.name}`} onClose={() => setShareOpen(false)} />
      )}
    </>
  );
}
