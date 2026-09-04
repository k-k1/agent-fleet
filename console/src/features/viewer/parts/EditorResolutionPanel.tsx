// The resolution panel for a save that conflicted, or whose outcome is unknown (docs/log/44 §7).
// A presentational component holding no state at all: which stage it is in comes from
// model.phase, and its open state from the caller.
import { useT } from "../../../lib/i18n/index.ts";
import type { FileEditorModel } from "../../editor/model.ts";

export interface EditorResolutionPanelProps {
  model: FileEditorModel;
  open: boolean;
  onOpen(): void;
  onCancel(): void;
  onRetryConflict(): void;
  onRetryUnknown(): void;
  onResave(): void;
  onRiskAccept(): void;
  onTakeRemote(): void;
  onDiscardMine(): void;
  onManualMerge(): void;
  onCopyMine(): void;
  onClose(): void;
}

export function EditorResolutionPanel(props: EditorResolutionPanelProps) {
  const tr = useT();
  const { model } = props;
  if (!props.open) {
    return (
      <div className="file-editor-resolution collapsed" role="alert">
        <button type="button" autoFocus onClick={props.onOpen}>{tr("editor.resolve")}</button>
      </div>
    );
  }
  if (model.phase === "conflict") {
    return (
      <section className="file-editor-resolution" role="alert" aria-label={tr("editor.status.conflict")}>
        <h4>{tr("editor.conflict.title")}</h4>
        <p>{tr("editor.conflict.body")}</p>
        {model.conflict && <MineRemoteDiff mine={model.content} remote={model.conflict.content} />}
        <div className="file-editor-actions">
          <button type="button" autoFocus onClick={props.onTakeRemote}>{tr("editor.conflict.adopt_remote")}</button>
          <button type="button" onClick={props.onDiscardMine}>{tr("editor.conflict.discard_mine")}</button>
          <button type="button" onClick={props.onManualMerge}>{tr("editor.conflict.manual_merge")}</button>
          <button type="button" onClick={props.onCopyMine}>{tr("editor.copy_mine")}</button>
          <button type="button" onClick={props.onCancel}>{tr("editor.cancel")}</button>
        </div>
      </section>
    );
  }
  if (model.phase === "conflict_remote_unavailable") {
    return (
      <section className="file-editor-resolution" role="alert" aria-label={tr("editor.status.remote_unavailable")}>
        <h4>{tr("editor.status.remote_unavailable")}</h4>
        <p>{tr("editor.remote_unavailable.body")}</p>
        <div className="file-editor-actions">
          <button type="button" autoFocus onClick={props.onRetryConflict}>{tr("editor.retry_get")}</button>
          <button type="button" onClick={props.onCopyMine}>{tr("editor.copy_mine")}</button>
          <button type="button" onClick={props.onClose}>{tr("editor.close_without_save")}</button>
          <button type="button" onClick={props.onCancel}>{tr("editor.cancel")}</button>
        </div>
      </section>
    );
  }
  const observation = model.unknownObservation;
  return (
    <section className="file-editor-resolution" role="alert" aria-label={tr("editor.status.unknown")}>
      <h4>{tr("editor.status.unknown")}</h4>
      <p>
        {observation?.kind === "sent_live"
          ? tr("editor.unknown.sent_live")
          : observation?.kind === "old_base_live"
            ? tr("editor.unknown.old_base")
            : tr("editor.unknown.unavailable")}
      </p>
      <div className="file-editor-actions">
        {observation?.kind === "sent_live" && (
          <>
            <button type="button" autoFocus onClick={props.onResave}>{tr("editor.unknown.resave")}</button>
            <button type="button" onClick={props.onRiskAccept}>{tr("editor.unknown.accept_risk")}</button>
          </>
        )}
        {observation?.kind === "old_base_live" && (
          <button type="button" autoFocus onClick={props.onResave}>{tr("editor.unknown.resave_old")}</button>
        )}
        {(!observation || observation.kind === "unavailable") && (
          <button type="button" autoFocus onClick={props.onRetryUnknown}>{tr("editor.unknown.retry")}</button>
        )}
        <button type="button" onClick={props.onCopyMine}>{tr("editor.copy_mine")}</button>
        <button type="button" onClick={props.onClose}>{tr("editor.close_without_save")}</button>
        <button type="button" onClick={props.onCancel}>{tr("editor.cancel")}</button>
      </div>
    </section>
  );
}

function MineRemoteDiff({ mine, remote }: { mine: string; remote: string }) {
  const tr = useT();
  const mineLines = mine.split("\n");
  const remoteLines = remote.split("\n");
  let prefix = 0;
  while (
    prefix < mineLines.length &&
    prefix < remoteLines.length &&
    mineLines[prefix] === remoteLines[prefix]
  ) prefix++;
  let suffix = 0;
  while (
    suffix < mineLines.length - prefix &&
    suffix < remoteLines.length - prefix &&
    mineLines[mineLines.length - 1 - suffix] === remoteLines[remoteLines.length - 1 - suffix]
  ) suffix++;
  const column = (lines: string[], side: "mine" | "remote") => (
    <div className="file-diff-column">
      {/* `side` is the CSS identifier (diff-mine / diff-remote); the heading is a tr() label. */}
      <strong>{tr(side === "mine" ? "editor.diff.mine" : "editor.diff.remote")}</strong>
      <pre>
        {lines.map((line, index) => {
          const changed = index >= prefix && index < lines.length - suffix;
          return (
            <span key={index} className={changed ? `diff-${side}` : "diff-same"}>
              {changed ? (side === "mine" ? "− " : "+ ") : "  "}{line}{"\n"}
            </span>
          );
        })}
      </pre>
    </div>
  );
  return (
    <div className="file-mine-remote-diff" aria-label={tr("editor.diff_aria")}>
      {column(mineLines, "mine")}
      {column(remoteLines, "remote")}
    </div>
  );
}
