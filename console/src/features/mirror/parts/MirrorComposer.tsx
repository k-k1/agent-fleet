import type { KeyboardEvent as RKeyboardEvent } from "react";
import { sessionSettings } from "../../../core/api/client.ts";
import type { AgentDescriptor } from "../../../agents/registry.ts";
import type { Settings } from "../../../lib/settings.ts";
import { isQuickReplyPinned } from "../../../lib/quickReplies.ts";
import { t as tr } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { SuggestChipMenu } from "../SuggestChipMenu.tsx";
import { AttachChips } from "./AttachChips.tsx";
import { HistoryNav, HistorySearchButton } from "./HistoryNav.tsx";
import { HistorySearchBar } from "./HistorySearchBar.tsx";
import { SendColumn } from "./SendColumn.tsx";
import { SkillButton, SkillList } from "./SkillList.tsx";
import { SuggestRow } from "./SuggestRow.tsx";
import { SendQueueList } from "../sendQueue/SendQueueList.tsx";
import { injectsMidTurn } from "../sendQueue/queue.ts";
import type { useSendQueue } from "../sendQueue/useSendQueue.ts";
import type { composerInput } from "./composerInput.ts";
import type { MirrorActions } from "./useMirrorActions.ts";
import type { MirrorState } from "./useMirrorState.ts";
import type { useHistorySearch } from "./useHistorySearch.ts";
import type { useReplySuggest } from "./useReplySuggest.ts";
import type { useSkillPicker } from "./useSkillPicker.ts";

/** MirrorComposer is the live composer: suggestions, attachments, history, skills, input, send. */
export function MirrorComposer({
  session,
  settings,
  agent,
  managed,
  running,
  modSend,
  isPlan,
  history,
  recallPrev,
  recallNext,
  onKeyDown,
  st,
  actions,
  input,
  suggest,
  skillPicker,
  histSearch,
  sendQueue,
}: {
  session: string;
  settings: Settings;
  agent: AgentDescriptor;
  managed: boolean;
  running: boolean;
  modSend: boolean;
  isPlan: boolean;
  history: string[];
  recallPrev: () => void;
  recallNext: () => void;
  onKeyDown: (e: RKeyboardEvent) => void;
  st: MirrorState;
  actions: MirrorActions;
  input: ReturnType<typeof composerInput>;
  suggest: ReturnType<typeof useReplySuggest>;
  skillPicker: ReturnType<typeof useSkillPicker>;
  histSearch: ReturnType<typeof useHistorySearch>;
  sendQueue: ReturnType<typeof useSendQueue>;
}) {
  const {
    pendingPlan, mode, setMode, lastNonPlanMode, draft, setDraft, sending, attachments, pasting, filePickRef,
    setLightbox, histIdx, setHistIdx, inputRef,
  } = st;
  const { postInput, postKeys } = actions;
  const { addFiles, onPaste, removeAttachment, auqLocksComposer, decisionPending, composerLocked, send } = input;
  const canPasteImage = agent.caps.imagePaste;
  return (
    <div className="mirror-compose">
      {/* Reply suggestions: frequently used short replies plus candidates derived from the
          latest answer (Layer A), plus LLM candidates fetched with ✨ (v2). A click inserts
          one, ⌥+click sends it immediately. Full-width flex (.mirror-suggest) above the
          input row. */}
      {!composerLocked && (suggest.chips.length > 0 || settings.replySuggestEnabled) && (
        <SuggestRow
          rowRef={suggest.rowRef}
          chips={suggest.chips}
          pinned={settings.quickRepliesPinned}
          cycledText={suggest.cycledText}
          aiEnabled={!!settings.replySuggestEnabled}
          suggesting={suggest.suggesting}
          running={running}
          onFetchLlm={suggest.fetchLlmSuggestions}
          onNav={suggest.onNav}
          onChipKeyDown={suggest.onChipKeyDown}
          onChipClick={(e, text) => {
            if (suggest.chipMenu.clickSwallowed()) return; // release of the long-press that opened the menu
            suggest.applySuggestion(text, e.ctrlKey || e.altKey || e.metaKey);
          }}
          chipProps={suggest.chipMenu.chipProps}
        />
      )}
      {suggest.chipMenu.menu && (
        <SuggestChipMenu
          menu={suggest.chipMenu.menu}
          pinned={isQuickReplyPinned(settings.quickRepliesPinned, suggest.chipMenu.menu.text)}
          onClose={suggest.chipMenu.close}
          onTogglePin={suggest.togglePin}
          onForget={suggest.forgetSuggestion}
        />
      )}
      <SendQueueList
        items={sendQueue.items}
        paused={sendQueue.paused}
        injects={injectsMidTurn(agent.id, managed)}
        onEdit={sendQueue.edit}
        onRemove={sendQueue.remove}
        onMove={sendQueue.move}
        onSendNow={sendQueue.sendNow}
        onResume={sendQueue.resume}
      />
      <AttachChips
        attachments={attachments}
        pasting={pasting}
        onRemove={removeAttachment}
        onOpen={(url) => setLightbox({ src: url })}
      />
      {/* Ctrl+R history search. Full-width band above the input row; the match it is on is
          previewed in the textarea itself, so the two have to be read together. */}
      {histSearch.open && (
        <HistorySearchBar
          inputRef={histSearch.queryRef}
          query={histSearch.query}
          count={histSearch.count}
          pos={histSearch.pos}
          failed={histSearch.failed}
          onQuery={histSearch.onQuery}
          onKeyDown={histSearch.onQueryKeyDown}
          onBlur={histSearch.onQueryBlur}
          onCancel={histSearch.cancel}
        />
      )}
      <HistoryNav
        canPrev={history.length > 0}
        canNext={histIdx !== null}
        onPrev={recallPrev}
        onNext={recallNext}
      />
      {/* Skill picker (docs/log/50): a completion list floating over the composer. The mouse
          tracks the selection via onMouseMove and commits on click (mousedown calls
          preventDefault so focus is not stolen — same shape as CommandPalette), a tap commits
          directly, and the keyboard is driven by onKeyDown. While arguments are being typed
          (skillArgs) the list is passive: it has no keyboard selection, so no `sel` is set and
          only clicking works (which swaps the command while keeping the arguments). */}
      {skillPicker.listVisible && (
        <SkillList
          popRef={skillPicker.popRef}
          selRef={skillPicker.selRef}
          passive={skillPicker.passive}
          skills={skillPicker.skills}
          items={skillPicker.items}
          more={skillPicker.more}
          trigger={skillPicker.trigger}
          sel={skillPicker.sel}
          query={skillPicker.query}
          onHover={skillPicker.setSel}
          onPick={skillPicker.pick}
          onMore={skillPicker.unfold}
        />
      )}
      <HistorySearchButton open={histSearch.open} disabled={!histSearch.canOpen} onOpen={histSearch.openSearch} />
      {skillPicker.canSkills && (
        <SkillButton
          btnRef={skillPicker.btnRef}
          open={skillPicker.listVisible}
          disabled={composerLocked}
          trigger={skillPicker.trigger}
          onToggle={skillPicker.toggleFromButton}
        />
      )}
      {/* + attach: the drag&drop-less path (phones foremost, handy everywhere).
          Any file type; the same addFiles upload the paste/drop paths use. */}
      {canPasteImage && (
        <>
          <input
            ref={filePickRef}
            type="file"
            multiple
            hidden
            onChange={(e) => {
              const files = Array.from(e.target.files || []);
              e.target.value = ""; // allow re-picking the same file
              void addFiles(files);
            }}
          />
          <button
            type="button"
            className="ghost mirror-attach-btn"
            title={tr("mirror.attach_file")}
            disabled={composerLocked || pasting}
            onClick={() => filePickRef.current?.click()}
          >
            <Icon name="add" />
          </button>
        </>
      )}
      <textarea
        ref={inputRef}
        className="mirror-input"
        rows={2}
        placeholder={
          decisionPending
            ? pendingPlan
              ? tr("mirror.ph_plan_wait")
              : tr("mirror.ph_perm_wait")
            : auqLocksComposer
              ? tr("mirror.ph_question")
              : modSend
                ? tr("mirror.ph_mod")
                : tr("mirror.ph_enter")
        }
        disabled={composerLocked}
        value={draft}
        onChange={(e) => {
          setDraft(e.target.value);
          setHistIdx(null); // typing leaves history-recall mode
          skillPicker.trackTyping(e.target.value, e.target.selectionStart ?? e.target.value.length);
        }}
        onSelect={(e) => skillPicker.trackCaret(e.currentTarget.value, e.currentTarget.selectionStart ?? 0)}
        onKeyDown={onKeyDown}
        onPaste={onPaste}
      />
      <SendColumn
        showMode={!!(agent.caps.planMode && agent.planCycleKey)}
        isPlan={isPlan}
        modeLabel={mode}
        modeDisabled={sending || decisionPending}
        sendDisabled={(!draft.trim() && !attachments.length) || sending || composerLocked}
        onToggleMode={() => {
          const toPlan = !isPlan;
          // Optimistic label (codex/opencode only report the new mode after a turn);
          // the poll reconciles from the terminal via paneMode.
          setMode(toPlan ? "Plan" : lastNonPlanMode.current || agent.defaultModeLabel);
          // For a managed session the mode switch is a ThreadSettings update (POST
          // /settings → UpdateSettings, docs/log/27 §9.4-3), which takes effect on the
          // next turn's agent/mode. tui stays key-driven (planEnterCmd / planCycleKey).
          if (managed) {
            void sessionSettings(session, { mode: toPlan ? "plan" : "normal" });
            return;
          }
          // Low-level sends (no working status / no quick re-poll) so the optimistic
          // label holds until the regular poll reads the real mode.
          // A slash command starts no turn (the server's slashCmdRe keeps it out of
          // "working"), so the op is sent as start purely as a formality.
          if (toPlan && agent.planEnterCmd) postInput(agent.planEnterCmd, "start");
          else postKeys([agent.planCycleKey!]);
        }}
        onSend={() => send()}
        onQueue={st.busy ? input.queueDraft : undefined}
      />
    </div>
  );
}
