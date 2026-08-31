import { describe, expect, it } from "vitest";
import {
  acceptUnknownRisk,
  adoptRemote,
  applySuggestion,
  beginSave,
  classifyUnknownRemote,
  conflictFound,
  createFileEditorModel,
  createRemoteSnapshot,
  discardToBase,
  editBuffer,
  followExternal,
  observeExternal,
  prepareUnknownResave,
  saveStateUnknown,
  saveSucceeded,
  setSuggestion,
  startManualMerge,
} from "./model.ts";
import { revisionOf } from "./buffer.ts";

const initial = () => createFileEditorModel("p1", "repos/a.txt", "base\n", revisionOf("base\n"));

describe("file editor save model", () => {
  it("records and validates the fetched time in a remote snapshot", () => {
    const content = "remote\n";
    expect(createRemoteSnapshot(
      "repos/a.txt",
      content,
      revisionOf(content),
      123_456,
    )).toEqual({
      path: "repos/a.txt",
      content,
      revision: revisionOf(content),
      fetchedAt: 123_456,
    });
    expect(() => createRemoteSnapshot(
      "repos/a.txt",
      content,
      revisionOf(content),
      Number.NaN,
    )).toThrow("invalid fetchedAt");
  });

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
      fetchedAt: 100,
    }).kind).toBe("sent_live");
    expect(classifyUnknownRemote(snapshot, {
      path: snapshot.path,
      content: "base\n",
      revision: snapshot.baseDiskRevision,
      fetchedAt: 101,
    }).kind).toBe("old_base_live");
    expect(classifyUnknownRemote(snapshot, {
      path: snapshot.path,
      content: "third\n",
      revision: revisionOf("third\n"),
      fetchedAt: 102,
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
        remote: {
          path: snapshot.path,
          content: snapshot.content,
          revision: snapshot.bufferRevision,
          fetchedAt: 100,
        },
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
        remote: {
          path: snapshot.path,
          content: "base\n",
          revision: snapshot.baseDiskRevision,
          fetchedAt: 100,
        },
      },
    };
    expect(prepareUnknownResave(unknown).baseDiskRevision).toBe(snapshot.baseDiskRevision);
    expect(remoteRevision).not.toBe(snapshot.baseDiskRevision);
  });

  it("keeps mine separate on conflict and manual merge starts from remote", () => {
    const mine = editBuffer(initial(), "mine\n");
    const remote = {
      path: mine.path,
      content: "remote\n",
      revision: revisionOf("remote\n"),
      fetchedAt: 100,
    };
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

  it("discards a conflict to the fetched remote snapshot", () => {
    const mine = editBuffer(initial(), "mine\n");
    const remote = {
      path: mine.path,
      content: "remote\n",
      revision: revisionOf("remote\n"),
      fetchedAt: 123_456,
    };
    const conflict = conflictFound(mine, remote);

    expect(conflict.conflict?.fetchedAt).toBe(123_456);
    const discarded = discardToBase(conflict);
    expect(discarded.content).toBe(remote.content);
    expect(discarded.bufferRevision).toBe(remote.revision);
    expect(discarded.baseDiskContent).toBe(remote.content);
    expect(discarded.baseDiskRevision).toBe(remote.revision);
    expect(discarded.dirty).toBe(false);
    expect(discarded.phase).toBe("clean");
  });

  it("refuses to invent a discard base while the live disk is unknown", () => {
    const dirty = editBuffer(initial(), "mine\n");
    const [saving, snapshot] = beginSave(dirty);
    expect(() => discardToBase(saving)).toThrow("discard target unavailable");
    expect(() => discardToBase(
      saveStateUnknown(saving, snapshot, "unknown"),
    )).toThrow("discard target unavailable");
  });
});

describe("external change observation and follow (docs/log/44 §7)", () => {
  const remoteOf = (content: string) =>
    createRemoteSnapshot("repos/a.txt", content, revisionOf(content), 1_000);

  it("records an advisory without moving the phase, for every phase", () => {
    const dirty = editBuffer(initial(), "mine\n");
    const observed = observeExternal(dirty, { kind: "changed", revision: revisionOf("ext\n") });
    expect(observed.phase).toBe("dirty");
    expect(observed.dirty).toBe(true);
    expect(observed.content).toBe("mine\n");
    expect(observed.baseDiskRevision).toBe(revisionOf("base\n"));
    expect(observed.externalObservation).toEqual({ kind: "changed", revision: revisionOf("ext\n") });

    const conflicted = conflictFound(dirty, remoteOf("remote\n"));
    const observedConflict = observeExternal(conflicted, { kind: "missing" });
    expect(observedConflict.phase).toBe("conflict");
    expect(observeExternal(observedConflict, null).externalObservation).toBeNull();
  });

  it("follows a clean buffer onto the remote snapshot and bumps the follow epoch", () => {
    const followed = followExternal(initial(), remoteOf("ext\n"));
    expect(followed.content).toBe("ext\n");
    expect(followed.baseDiskRevision).toBe(revisionOf("ext\n"));
    expect(followed.bufferRevision).toBe(revisionOf("ext\n"));
    expect(followed.dirty).toBe(false);
    expect(followed.phase).toBe("clean");
    expect(followed.followEpoch).toBe(1);
    expect(followed.bufferGeneration).toBe(1);
    expect(followed.externalObservation).toBeNull();
  });

  it("follows a risk-accepted clean base and retires the risk marker", () => {
    const [saving, snapshot] = beginSave(editBuffer(initial(), "mine\n"));
    const unknown = saveStateUnknown(saving, snapshot, "lost");
    const observed = {
      ...unknown,
      unknownObservation: {
        kind: "sent_live" as const,
        remote: remoteOf("mine\n"),
      },
    };
    const accepted = acceptUnknownRisk(observed);
    expect(accepted.phase).toBe("clean_risk_accepted");
    expect(accepted.riskAccepted).toBe(true);
    const followed = followExternal(accepted, remoteOf("ext\n"));
    expect(followed.phase).toBe("clean");
    expect(followed.riskAccepted).toBe(false);
    expect(followed.followEpoch).toBe(1);
  });

  it("refuses to follow a dirty buffer", () => {
    expect(() => followExternal(editBuffer(initial(), "mine\n"), remoteOf("ext\n")))
      .toThrow("follow unavailable");
  });

  it("clears the advisory when a save, a conflict snapshot, or a remote adoption lands", () => {
    const observation = { kind: "changed" as const, revision: revisionOf("ext\n") };
    const dirty = observeExternal(editBuffer(initial(), "mine\n"), observation);

    const [saving, snapshot] = beginSave(dirty);
    expect(saveSucceeded(saving, snapshot, revisionOf("mine\n")).externalObservation).toBeNull();

    const conflicted = conflictFound(dirty, remoteOf("remote\n"));
    expect(conflicted.externalObservation).toBeNull();
    expect(adoptRemote(conflictFound(dirty, remoteOf("remote\n"))).externalObservation).toBeNull();
  });

  it("keeps a discard undoable: discardToBase preserves the follow epoch", () => {
    const followed = followExternal(initial(), remoteOf("ext\n"));
    const discarded = discardToBase(editBuffer(followed, "typed\n"));
    expect(discarded.followEpoch).toBe(1);
    expect(discarded.content).toBe("ext\n");
  });
});

describe("AI suggestion (docs/log/44 §4)", () => {
  const envelopeFor = (model: ReturnType<typeof initial>, replacement = "head\n") => ({
    kind: "edit_suggestion" as const,
    version: 1 as const,
    paneId: model.paneId,
    filePath: model.path,
    requestId: "req-1",
    sourceRevision: model.bufferRevision,
    suggestion: {
      summary: "書き換え",
      replacement,
      range: { from: 0, to: 4 },
      baseRevision: model.bufferRevision,
    },
  });

  it("records the envelope as an advisory beside the phase", () => {
    const model = setSuggestion(initial(), envelopeFor(initial()));
    expect(model.phase).toBe("clean");
    expect(model.dirty).toBe(false);
    expect(model.suggestion?.requestId).toBe("req-1");
    expect(setSuggestion(model, null).suggestion).toBeNull();
  });

  it("accept applies through editBuffer: dirty, new revision, suggestion retired", () => {
    const model = setSuggestion(initial(), envelopeFor(initial()));
    const applied = applySuggestion(model);
    expect(applied.content).toBe("head\n\n");
    expect(applied.dirty).toBe(true);
    expect(applied.phase).toBe("dirty");
    expect(applied.bufferRevision).toBe(revisionOf("head\n\n"));
    expect(applied.suggestion).toBeNull();
  });

  it("a buffer edit after receipt derives staleness and accept refuses", () => {
    const model = editBuffer(setSuggestion(initial(), envelopeFor(initial())), "typed\n");
    // 提案は保持されたまま（パネルが stale を導出して表示する）…
    expect(model.suggestion).not.toBeNull();
    // …だが適用は revision 三重一致で拒否される。
    expect(() => applySuggestion(model)).toThrow("suggestion_stale");
    expect(model.suggestion?.suggestion.baseRevision).not.toBe(model.bufferRevision);
  });

  it("accept without a pending suggestion refuses", () => {
    expect(() => applySuggestion(initial())).toThrow("suggestion_invalid");
  });
});
