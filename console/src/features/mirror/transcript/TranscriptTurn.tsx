// transcript/TranscriptTurn — one conversation block.
//
// A header (who + model + origin badges), the body (user prompt as text, assistant reply
// as Markdown with folded tool traces), and a footer (time + token usage + copy).
// Subagent (sidechain) turns get a distinct label and tint.
//
// Named TranscriptTurn rather than Turn: `Turn` is the raw-transcript interface in
// types.ts, and the two only coexisted before because a value and a type can share a
// name inside one file. Split across modules that overlap would be a trap.

import { useEffect, useReducer, useRef } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import FileIcon from "../../../ui/FileIcon.tsx";
import { fmtTok } from "../../../lib/fmttok.ts";
import { prettyModel } from "../../../lib/modelName.ts";
import { t as tr } from "../../../lib/i18n/index.ts";
import { splitPastedImages } from "../../../lib/pastedImages.ts";
import { MarkdownView } from "../../viewer/MarkdownView.tsx";
import { textOfParts, workSplit } from "../mirrorParts.ts";
import { footTime } from "../turnTime.ts";
import { canBranchFrom } from "../forkAt.ts";
import { foldParts, peerIntentOf, peerSenderOf, spendOf } from "./model.ts";
import { paintTurnMarks } from "./markPaint.ts";
import { chipPart, turnFiles } from "./turnFiles.ts";
import type { Group, Part } from "./types.ts";
import type { TranscriptCaps } from "./capabilities.ts";
import {
  BashBlock,
  CmdChip,
  CopyButton,
  DelegationCard,
  ErrorBlock,
  PastedThumb,
  PlanBlock,
  QuestionBlock,
  ThinkingBlock,
  ToolRun,
  TurnSpendBar,
  TurnTtsButtons,
  UserFileBlock,
  WorkDisclosure,
  formatTS,
} from "./blocks.tsx";

export function TranscriptTurn({
  turn,
  caps,
  foldWork,
  defaultWorkOpen,
}: {
  turn: Group;
  caps: TranscriptCaps;
  foldWork: boolean;
  defaultWorkOpen: boolean;
}) {
  const isUser = turn.role === "user";
  const agentName = caps.agentName;
  const who = isUser ? caps.userName || tr("chat.you") : turn.sidechain ? tr("mirror.subagent") : agentName;
  const ctxTok = turn.inTok + turn.cacheRead + turn.cacheCreate;
  const spend = spendOf(turn);
  const maxSpend = caps.maxSpend || 0;
  const bodyEl = useRef<HTMLDivElement>(null); // カラオケ朗読（turnTts）とマーカーの本文 DOM
  // 印を被せるのは MarkdownView が innerHTML を描いたあと（子の effect が先に走るので、
  // この effect の時点では本文が出来ている — DocView のプランコメントと同じ作法）。
  const painted = useRef("");
  const marks = caps.marks;
  useEffect(() => {
    if (!bodyEl.current || !marks) return;
    painted.current = paintTurnMarks(bodyEl.current, marks.byRoot, marks.authorSlot, painted.current);
  });
  // 作業過程の畳み込み（folded）と開閉（open）。ターン単位の状態だが、いま載っている
  // ターンごと ref に持つ: ミラーはペインがセッションを持ち替えても再マウントされず
  // （props が変わるだけ）、転写の idx はセッション毎に 0 から振られるので、同じ位置の
  // コンポーネントに別の会話のターンが載る。id が変わったら前のターンの状態は捨てる。
  //
  // **folded は片道。** foldWork は生きたポーリング値（セッションの working）から来るので
  // 平気で往復する — ストリーミング中に一瞬 idle を拾う、operator/定時実行/peer の新しい
  // プロンプトが転写へ届く前に working が立つ、といった具合に。往復させるとそのたびに
  // 作業過程が展開⇄畳みで入れ替わり、読んでいる最中に本文の高さが跳ねて位置がズレる。
  // 一度畳んだターンは開き直さない（中身は summary をクリックすれば読める）。
  //
  // **open を決めるのは「初めて畳まれた瞬間の defaultWorkOpen」だけ** ＝そのとき末尾を
  // 追っていたかどうか。以後は追従が切れても戻っても追随せず、読者のクリックだけが変える。
  const work = useRef({ id: "", folded: false, open: false, defaulted: false });
  const [, redrawWork] = useReducer((n: number) => n + 1, 0);
  const workId = (caps.session || "") + "#" + turn.idx;
  if (work.current.id !== workId) work.current = { id: workId, folded: false, open: false, defaulted: false };
  if (foldWork) work.current.folded = true;
  const split = !isUser && work.current.folded ? workSplit(turn.parts) : null;
  if (split && !work.current.defaulted) {
    work.current.defaulted = true;
    work.current.open = defaultWorkOpen;
  }
  const workOpen = work.current.open;
  const edited = isUser ? [] : turnFiles(turn.parts);
  const copyText = split ? textOfParts(turn.parts.slice(split.at)) : turn.text;
  const renderAssistantParts = (parts: Part[]) =>
    foldParts(parts).map((item) =>
      // Consecutive tool traces collapse into one foldable row (Edit/Write bursts
      // between paragraphs). A lone tool renders inline (ToolRun handles length 1).
      item.kind === "toolrun" ? (
        <ToolRun key={"tr" + item.tools[0].i} tools={item.tools} onOpenDiff={caps.openDiff} />
      ) : item.p.kind === "question" ? (
        // A question from the transcript is history, never clickable. 回答済み is claimed
        // only when the answer is actually here: claude writes the tool_use at ASK time,
        // so a part CAN arrive still open (the Agent hides the one whose card is pending,
        // but a shared/exported transcript has no such card to defer to). Badging that
        // 回答済み with nothing to show was the old, hardcoded lie.
        <QuestionBlock
          key={item.i}
          questions={item.p.questions}
          answered={!!item.p.answer}
          answer={item.p.answer}
          declined={item.p.declined}
        />
      ) : item.p.kind === "plan" ? (
        // A historical plan — show the outcome, open in a pane when this view can (the
        // shared view has no pane to open, so PlanBlock omits it). Same rule as the
        // question above: 決定済み only once the tool_result (or the optimistic 却下 mark)
        // says so, not merely because the plan reached the transcript.
        <PlanBlock
          key={item.i}
          plan={item.p.plan}
          session={caps.session}
          answered={!!item.p.answer}
          outcome={item.p.answer}
          forceRejected={caps.isRejectedPlan ? caps.isRejectedPlan(item.p.plan || "") : false}
          onOpen={caps.openPlan ? () => caps.openPlan!(item.p.plan || "") : undefined}
          onSendComments={caps.sendPlanComments ? () => caps.sendPlanComments!(item.p.plan || "") : undefined}
          sendDisabled={caps.planSendDisabled}
        />
      ) : item.p.kind === "userfile" ? (
        // Files the agent shared via SendUserFile — a panel; each opens in a pane. With
        // no way to open one (shared view), the panel is not rendered at all.
        caps.openFile ? (
          <UserFileBlock
            key={item.i}
            files={item.p.files}
            caption={item.p.caption}
            onOpen={caps.openFile}
            fileURL={caps.fileURL}
          />
        ) : null
      ) : item.p.kind === "thinking" ? (
        // The agent's chain-of-thought (codex reasoning / opencode reasoning),
        // collapsed unless this agent's 動作設定 asks for it expanded.
        <ThinkingBlock
          key={item.i}
          text={item.p.text}
          defaultOpen={!!caps.expandThinking}
          baseDir={turn.cwd}
          repo={caps.repo}
          onOpenFile={caps.openFile}
        />
      ) : item.p.kind === "delegation" ? (
        <DelegationCard key={item.i} p={item.p} agentName={agentName} />
      ) : item.p.kind === "error" ? (
        // The turn failed instead of answering (the agent's own error record, e.g.
        // auth/quota/rate-limit) — never fold it away.
        <ErrorBlock
          key={item.i}
          info={item.p.info}
          text={item.p.text}
          cause={item.p.cause}
          agentName={agentName}
          onReauth={caps.onReauth}
        />
      ) : (
        <MarkdownView
          key={item.i}
          source={item.p.text}
          baseDir={turn.cwd}
          repo={caps.repo}
          onOpenFile={caps.openFile}
          // マーカーを数える範囲はこの part ひとつ（docs/log/69 §69.3）。ブロック相対ではなく
          // 元ターン由来の root なので、共有先の tail 窓がずれても同じ場所を指す。
          markRoot={caps.marks ? turn.origins[item.i] : undefined}
          markKind={item.p.kind}
        />
      ),
    );
  const fromOperator = isUser && turn.source === "operator";
  // Schedule origin (docs/log/38): the prompt was fired by scheduled execution — either a
  // timed fire ("schedule") or a run-now ("schedule-manual") — badged so schedule-driven
  // turns are never mistaken for typed or operator input, and 定期/手動 read apart.
  const fromSchedule = isUser && (turn.source === "schedule" || turn.source === "schedule-manual");
  const scheduleManual = isUser && turn.source === "schedule-manual";
  // Automatic resume (docs/log/47 §4-6): the agent itself re-sent 「続けて」 after the turn was
  // cut off. Nobody typed it and no operator sent it, so it needs its own badge — an
  // unattributed "続けて" in the transcript is the most confusing kind of injected turn.
  const fromAutoResume = isUser && turn.source === "auto-resume";
  // Peer origin (docs/log/58 / ADR 0041): ANOTHER SESSION typed into this one. Neither the
  // user nor the operator sent it, and this badge is its ONLY visualisation — the
  // sender's name exists nowhere else on this side except the server-built envelope
  // prefix, so read it back from the text.
  //
  // 由来タグ(source)が無くても封筒があれば peer と見なす。タグはサーバが投入時に覚える
  // 別ストア由来で、①記録が済む前に取ってきたターン（増分ポーリングは持っているターンを取り直さ
  // ないので、そのまま固定される）②長寿命セッションで記録が上限を超えて押し出された、のどちらでも
  // 落ちる（docs/log/58 §58.15）。封筒はサーバが本文の先頭に必ず付ける（呼び出し元には組ませない）
  // ので、表示の根拠としてはタグと同格 — そして落ちたときに残る唯一の痕跡。
  //
  // 封筒が無い着信もある: claude 自前の cross-session チャネル（docs/log/58 §58.16）は AF を
  // 通らないので封筒が付かない。そこは Agent が転写の `origin.name` から送信者を起こして
  // `peerFrom` に載せてくるので、封筒 → それ、の順で拾う。
  const peerFrom = isUser ? (peerSenderOf(turn.text ?? "") ?? turn.peerFrom ?? null) : null;
  const fromPeer = isUser && (turn.source === "peer" || !!peerFrom);
  const peerIntent = fromPeer ? peerIntentOf(turn.text ?? "") : null;
  // Chat-bridge origin (docs/log/37 P2a): a reply the user sent from Discord/Slack, injected
  // into the session — badged distinctly from self-typed input, like operator turns.
  const chatProvider = isUser
    ? turn.source === "discord"
      ? "Discord"
      : turn.source === "slack"
        ? "Slack"
        : null
    : null;
  return (
    <div
      className={
        "mirror-turn " +
        (isUser ? "user" : "assistant") +
        (turn.sidechain ? " sidechain" : "") +
        (fromOperator ? " from-operator" : "") +
        (fromSchedule ? " from-schedule" : "") +
        (fromAutoResume ? " from-schedule" : "") +
        (fromPeer ? " from-peer" : "") +
        (chatProvider ? " from-chat" : "")
      }
      data-turn-idx={turn.idx}
    >
      <div className="mirror-turn-head">
        <span className="mt-who">{who}</span>
        {fromOperator && (
          // This user turn was injected by the fleet operator (docs/log/30 ②), not typed by
          // the user — badge it so the two are never confused.
          <span className="mt-op" title={tr("mirror.from_operator_title")}>
            <Icon name="broadcast" /> {tr("mirror.from_operator")}
          </span>
        )}
        {fromSchedule && (
          // Fired by scheduled execution (docs/log/38) — timed (定時) vs run-now (手動発火).
          <span
            className="mt-op mt-sched"
            title={tr(scheduleManual ? "mirror.from_schedule_manual_title" : "mirror.from_schedule_title")}
          >
            <Icon name={scheduleManual ? "play" : "clock"} />{" "}
            {tr(scheduleManual ? "mirror.from_schedule_manual" : "mirror.from_schedule")}
          </span>
        )}
        {fromAutoResume && (
          // Re-sent by the agent after a cut-off (docs/log/47 §4-6) — self-repair, not an instruction.
          <span className="mt-op mt-sched" title={tr("mirror.from_auto_resume_title")}>
            <Icon name="sync" /> {tr("mirror.from_auto_resume")}
          </span>
        )}
        {fromPeer && (
          // Sent by another session (docs/log/58) — not the user, not the operator.
          <span className="mt-op mt-peer" title={tr("mirror.from_peer_title")}>
            <Icon name="arrow-swap" />{" "}
            {peerFrom ? tr("mirror.from_peer_named", { name: peerFrom }) : tr("mirror.from_peer")}
          </span>
        )}
        {peerIntent && (
          // The message kind (docs/log/58 §58.14). Worth its own chip because it is the reason
          // a message did or did not get an answer — answer / notice are terminal.
          <span className="mt-op mt-peer mt-peer-kind" title={tr(`mirror.peer_intent_title.${peerIntent}`)}>
            {tr(`mirror.peer_intent.${peerIntent}`)}
          </span>
        )}
        {chatProvider && (
          // Sent from a chat bridge (docs/log/37 P2a) — a phone reply, not typed at the console.
          <span className="mt-op mt-chat" title={tr("mirror.from_chat_title")}>
            <Icon name="comment-discussion" /> {tr("mirror.from_chat", { provider: chatProvider })}
          </span>
        )}
        {turn.queued ? (
          <span className="mt-pending mt-queued" title={tr("mirror.queued_title")}>
            <Icon name="history" /> {tr("mirror.queued")}
          </span>
        ) : (
          turn.pending && (
            <span className="mt-pending" title={tr("mirror.pending_title")}>
              <Icon name="loading" spin /> {tr("mirror.pending")}
            </span>
          )
        )}
        {!isUser && turn.model && <span className="mt-model">{prettyModel(turn.model)}</span>}
        {!isUser && turn.effort && (
          <span className="mt-effort" title={tr("mirror.effort_hint")}>
            {turn.effort}
          </span>
        )}
      </div>
      <div className="mirror-turn-body" ref={bodyEl}>
        {isUser && turn.bash ? (
          // A `!`-run shell command (coalesceUserActions): render each as a terminal block
          // with the command line and its collapsed output, rather than hiding it as noise.
          turn.parts.map((p, i) => (
            <BashBlock key={i} command={p.text} stdout={p.output} stderr={p.stderr} />
          ))
        ) : isUser && turn.cmd ? (
          // A `/`-run slash command / skill invocation: a compact chip marking the action.
          turn.parts.map((p, i) => <CmdChip key={i} name={p.text} args={p.info} />)
        ) : isUser ? (
          (() => {
            // Split off any pasted-image references so the bubble shows the user's words
            // plus clickable thumbnails, not the machine-facing paths.
            const { text, images, files } = splitPastedImages(turn.text || "");
            return (
              <>
                {text && (
                  <MarkdownView
                    source={text}
                    breaks
                    baseDir={turn.cwd}
                    repo={caps.repo}
                    onOpenFile={caps.openFile}
                    // ユーザーの吹き出しが描くのは parts ではなくブロックの text なので、
                    // 2 行以上畳んだブロックでは root が空になる（groupTurns 参照）。
                    markRoot={caps.marks ? turn.bodyRoot : undefined}
                    markKind=""
                  />
                )}
                {images.length > 0 && caps.loadPastedImage && (
                  <div className="mt-imgs">
                    {images.map((nm) => (
                      <PastedThumb key={nm} name={nm} load={caps.loadPastedImage!} onOpen={caps.openImage} />
                    ))}
                  </div>
                )}
                {files.length > 0 && (
                  // Non-image attachments (drag&drop / ＋): a name chip — the file itself
                  // lives under the session's pasted dir for the agent to read.
                  <div className="mt-attach-files">
                    {files.map((nm) => (
                      <span className="mt-attach-file" key={nm} title={nm}>
                        <FileIcon name={nm} />
                        <span className="mt-attach-file-name">{nm}</span>
                      </span>
                    ))}
                  </div>
                )}
              </>
            );
          })()
        ) : split ? (
          <>
            <WorkDisclosure
              tools={split.tools}
              responses={split.responses}
              open={workOpen}
              onToggle={() => {
                work.current.open = !work.current.open;
                redrawWork();
              }}
            >
              {renderAssistantParts(turn.parts.slice(0, split.at))}
            </WorkDisclosure>
            {renderAssistantParts(turn.parts.slice(split.at))}
          </>
        ) : (
          renderAssistantParts(turn.parts)
        )}
      </div>
      {/* Files this reply edited, as chips — the answer to 「さっき直したのはどれ」
          without expanding a folded ToolRun and reading tool traces. Only where a diff
          can actually be opened: the shared view has no pane, and its DTO drops the path
          anyway (transcript/capabilities.ts — absent capability, no affordance). */}
      {!isUser && caps.openDiff && edited.length > 0 && (
        <div className="mirror-turn-files">
          <Icon name="edit" className="mtf-mark" />
          {edited.map((f) => (
            <button
              type="button"
              key={f.file}
              className={"mtf-chip" + (f.verb === "delete" ? " mtf-deleted" : "")}
              title={f.file}
              disabled={f.edits.length === 0}
              onClick={() => caps.openDiff!(chipPart(f))}
            >
              <FileIcon name={f.name} />
              <span className="mtf-name">{f.name}</span>
            </button>
          ))}
        </div>
      )}
      <div className="mirror-turn-foot">
        {/* The block's END: when this reply landed, not when the agent started working on
            it (turnTime.ts). The start stays reachable as a tooltip on a spanning turn. */}
        {footTime(turn) && (
          <span
            className="mt-time muted"
            title={
              turn.ts && turn.endTs && turn.endTs !== turn.ts
                ? tr("mirror.time_span_hint", { start: formatTS(turn.ts), end: formatTS(turn.endTs) })
                : undefined
            }
          >
            {formatTS(footTime(turn))}
          </span>
        )}
        {turn.outTok > 0 && (
          <span className="mt-tok muted" title={tr("mirror.token_hint")}>
            ↑{fmtTok(ctxTok)} ↓{fmtTok(turn.outTok)}
          </span>
        )}
        {!isUser && spend > 0 && maxSpend > 0 && (
          <TurnSpendBar fresh={turn.inTok} create={turn.cacheCreate} out={turn.outTok} max={maxSpend} />
        )}
        {!isUser && caps.tts && <TurnTtsButtons turn={turn} tts={caps.tts} body={bodyEl} />}
        {/* Branch from this prompt (docs/log/55). canBranchFrom holds the rule (landed user
            turn with an anchor): a block without one can't be pointed at, and offering it
            would fork the whole conversation instead of the point the user clicked. */}
        {caps.forkAt && canBranchFrom(turn) && (
          <button
            type="button"
            className="ghost xs mt-fork"
            title={tr("mirror.fork_at_title")}
            onClick={() => caps.forkAt!(turn)}
          >
            <Icon name="repo-forked" /> {tr("mirror.fork_at")}
          </button>
        )}
        <CopyButton text={copyText} />
      </div>
    </div>
  );
}
