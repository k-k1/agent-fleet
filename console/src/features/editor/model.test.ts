import { describe, expect, it } from "vitest";
import {
  acceptUnknownRisk,
  beginSave,
  classifyUnknownRemote,
  conflictFound,
  createFileEditorModel,
  discardToBase,
  editBuffer,
  prepareUnknownResave,
  saveStateUnknown,
  saveSucceeded,
  startManualMerge,
} from "./model.ts";
import { revisionOf } from "./buffer.ts";

const initial = () => createFileEditorModel("p1", "repos/a.txt", "base\n", revisionOf("base\n"));

describe("file editor save model", () => {
  it("cleans when generation or revision still matches the sent snapshot", () => {
    const dirty = editBuffer(initial(), "first\n");
    const [saving, snapshot] = beginSave(dirty);
    expect(saveSucceeded(saving, snapshot, snapshot.bufferRevision).dirty).toBe(false);

    const typedDuringSave = editBuffer(saving, "second\n");
    const completed = saveSucceeded(typedDuringSave, snapshot, snapshot.bufferRevision);
    expect(completed.dirty).toBe(true);
    expect(completed.content).toBe("second\n");
    expect(completed.baseDiskRevision).toBe(snapshot.bufferRevision);

    const undoneToSnapshot = editBuffer(typedDuringSave, snapshot.content);
    const completedAfterUndo = saveSucceeded(
      undoneToSnapshot,
      snapshot,
      snapshot.bufferRevision,
    );
    expect(completedAfterUndo.bufferGeneration).not.toBe(snapshot.bufferGeneration);
    expect(completedAfterUndo.bufferRevision).toBe(snapshot.bufferRevision);
    expect(completedAfterUndo.dirty).toBe(false);
  });

  it("classifies unknown saves without auto-cleaning", () => {
    const dirty = editBuffer(initial(), "mine\n");
    const [saving, snapshot] = beginSave(dirty);
    const unknown = saveStateUnknown(saving, snapshot, "lost response");
    expect(unknown.dirty).toBe(true);
    expect(classifyUnknownRemote(snapshot, {
      path: snapshot.path,
      content: snapshot.content,
      revision: snapshot.bufferRevision,
    }).kind).toBe("sent_live");
    expect(classifyUnknownRemote(snapshot, {
      path: snapshot.path,
      content: "base\n",
      revision: snapshot.baseDiskRevision,
    }).kind).toBe("old_base_live");
    expect(classifyUnknownRemote(snapshot, {
      path: snapshot.path,
      content: "third\n",
      revision: revisionOf("third\n"),
    }).kind).toBe("third_revision");
  });

  it("risk acceptance updates the observed base and preserves later edits", () => {
    const dirty = editBuffer(initial(), "sent\n");
    const [saving, snapshot] = beginSave(dirty);
    let unknown = saveStateUnknown(saving, snapshot, "unknown");
    unknown = {
      ...unknown,
      unknownObservation: {
        kind: "sent_live",
        remote: { path: snapshot.path, content: snapshot.content, revision: snapshot.bufferRevision },
      },
    };
    expect(acceptUnknownRisk(unknown).phase).toBe("clean_risk_accepted");

    const later = editBuffer(unknown, "later\n");
    const accepted = acceptUnknownRisk(later);
    expect(accepted.dirty).toBe(true);
    expect(accepted.baseDiskRevision).toBe(snapshot.bufferRevision);

    const undoneToSnapshot = editBuffer(later, snapshot.content);
    expect(acceptUnknownRisk(undoneToSnapshot).dirty).toBe(false);
  });

  it("explicit re-save uses the observed revision as CAS base", () => {
    const dirty = editBuffer(initial(), "sent\n");
    const [saving, snapshot] = beginSave(dirty);
    const remoteRevision = revisionOf("remote\n");
    const unknown = {
      ...saveStateUnknown(saving, snapshot, "unknown"),
      unknownObservation: {
        kind: "old_base_live" as const,
        remote: { path: snapshot.path, content: "base\n", revision: snapshot.baseDiskRevision },
      },
    };
    expect(prepareUnknownResave(unknown).baseDiskRevision).toBe(snapshot.baseDiskRevision);
    expect(remoteRevision).not.toBe(snapshot.baseDiskRevision);
  });

  it("keeps mine separate on conflict and manual merge starts from remote", () => {
    const mine = editBuffer(initial(), "mine\n");
    const remote = { path: mine.path, content: "remote\n", revision: revisionOf("remote\n") };
    const conflict = conflictFound(mine, remote);
    expect(conflict.content).toBe("mine\n");
    expect(conflict.conflict?.content).toBe("remote\n");
    const editedConflict = editBuffer(conflict, "mine later\n");
    expect(editedConflict.phase).toBe("conflict");
    expect(editedConflict.conflict).toEqual(remote);
    const merge = startManualMerge(conflict);
    expect(merge.content).toBe("remote\n");
    expect(merge.baseDiskRevision).toBe(remote.revision);
    expect(merge.dirty).toBe(true);
  });

  it("does not escape SaveStateUnknown by typing", () => {
    const dirty = editBuffer(initial(), "sent\n");
    const [saving, snapshot] = beginSave(dirty);
    const unknown = saveStateUnknown(saving, snapshot, "unknown");
    const later = editBuffer(unknown, "later\n");
    expect(later.phase).toBe("save_state_unknown");
    expect(later.saveSnapshot).toEqual(snapshot);
    expect(() => beginSave(later)).toThrow("save not available");
  });

  it("discards the buffer content back to the latest saved base", () => {
    const dirty = editBuffer(initial(), "saved\n");
    const [saving, snapshot] = beginSave(dirty);
    const saved = saveSucceeded(saving, snapshot, snapshot.bufferRevision);
    const later = editBuffer(saved, "discard me\n");

    const discarded = discardToBase(later);
    expect(discarded.content).toBe("saved\n");
    expect(discarded.bufferRevision).toBe(snapshot.bufferRevision);
    expect(discarded.baseDiskContent).toBe("saved\n");
    expect(discarded.dirty).toBe(false);
    expect(discarded.phase).toBe("clean");
  });
});
